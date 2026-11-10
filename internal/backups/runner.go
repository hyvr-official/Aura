package backups

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"aura/internal/timezone"
)

var ErrAlreadyRunning = errors.New("a backup for this task is already running")

type Runner struct {
	store *Store
	mu    sync.Mutex
}

func NewRunner(store *Store) *Runner {
	return &Runner{store: store}
}

func (r *Runner) RunAll(trigger string) {
	list, err := r.store.List()
	if err != nil {
		log.Printf("backup: list tasks: %v", err)
		return
	}
	now := time.Now()
	for _, b := range list {
		if b.IsManual() {
			log.Printf("backup %q: manual only, skipped", b.Name)
			continue
		}
		if !b.IsActive() {
			log.Printf("backup %q: inactive, skipped", b.Name)
			continue
		}
		last, ok, err := r.store.LastSuccess(b.ID)
		if err != nil {
			log.Printf("backup %q: last success: %v", b.Name, err)
			continue
		}
		if !b.IsDue(last, ok, now) {
			log.Printf("backup %q: not due (%s)", b.Name, b.FrequencyLabel())
			continue
		}
		log.Printf("backup %q: starting", b.Name)
		if err := r.RunOne(b, trigger); err != nil {
			log.Printf("backup %q: %v", b.Name, err)
			continue
		}
		log.Printf("backup %q: done", b.Name)
	}
}

func (r *Runner) Start(b Backup, trigger string) (Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	running, err := r.store.HasRunning(b.ID)
	if err != nil {
		return Run{}, err
	}
	if running {
		return Run{}, ErrAlreadyRunning
	}
	run, err := r.store.StartRun(b.ID, trigger)
	if err != nil {
		return run, err
	}
	if _, err := r.store.PruneRuns(); err != nil {
		log.Printf("backup: prune old runs: %v", err)
	}
	return run, nil
}

func (r *Runner) RunOne(b Backup, trigger string) error {
	run, err := r.Start(b, trigger)
	if err != nil {
		return err
	}
	return r.Execute(b, run)
}

func (r *Runner) Execute(b Backup, run Run) (err error) {
	var logs strings.Builder
	logf := func(format string, args ...any) {
		fmt.Fprintf(&logs, "[%s] %s\n", timezone.Format(time.Now(), "2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
	}

	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
		run.Status = StatusSuccess
		if err != nil {
			run.Status = StatusFailed
			logf("Failed: %v", err)
		} else {
			logf("Finished successfully")
		}
		run.Log = logs.String()
		if ferr := r.store.FinishRun(&run); ferr != nil {
			log.Printf("backup %q: save run: %v", b.Name, ferr)
		}
	}()

	logf("Starting %s backup of %s", run.Trigger, strings.Join(b.Paths, ", "))

	if len(b.Paths) == 0 {
		return errors.New("no backup paths configured")
	}
	for _, p := range b.Paths {
		info, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("source path: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("source path %s is not a directory", p)
		}
	}

	sevenZip, err := findSevenZip()
	if err != nil {
		return err
	}

	if b.PreRunEnabled {
		logf("Running pre run script")
		cmd := exec.Command("bash", "-e", "-c", b.PreRunScript)
		cmd.Dir = b.Paths[0]
		output, err := cmd.CombinedOutput()
		if s := strings.TrimSpace(string(output)); s != "" {
			logs.WriteString(s + "\n")
		}
		if err != nil {
			return fmt.Errorf("pre run script: %w", err)
		}
		logf("Pre run script finished")
	}

	dir := filepath.Join(tempDir(), strconv.FormatInt(b.ID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create temp folder: %w", err)
	}
	for _, name := range cleanTemp(dir) {
		logf("Removed incomplete archive: %s", name)
	}

	out := filepath.Join(dir, archiveName(b, time.Now()))
	defer os.Remove(out)
	logf("Archive: %s", out)

	args := append([]string{sevenZip, "a", "-t7z", "-mx=1", "-mmt=1", "-snl", "-bd", "-y", out}, b.Paths...)
	if ionice, lerr := exec.LookPath("ionice"); lerr == nil {
		args = append([]string{ionice, "-c", "3"}, args...)
	} else {
		logf("Warning: ionice not found, running without idle IO priority")
	}
	if nice, lerr := exec.LookPath("nice"); lerr == nil {
		args = append([]string{nice, "-n", "19"}, args...)
	} else {
		logf("Warning: nice not found, running without low CPU priority")
	}
	logf("Command: %s", strings.Join(args, " "))

	cmd := exec.Command(args[0], args[1:]...)
	output, err := cmd.CombinedOutput()
	if s := strings.TrimSpace(string(output)); s != "" {
		logs.WriteString(s + "\n")
	}
	if err != nil {
		return fmt.Errorf("7z: %w", err)
	}

	stat, err := os.Stat(out)
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	run.FileSize = stat.Size()
	logf("Archive size: %s", run.SizeLabel())

	if b.IsLocal() {
		logf("Moving to %s", localDir(b))
	} else {
		logf("Uploading to %s", b.SourceLabel())
	}
	dest, err := upload(context.Background(), b, out)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	run.FilePath = dest
	if b.IsLocal() {
		logf("Moved: %s", dest)
	} else {
		logf("Uploaded: %s", dest)
	}

	if b.LimitEnabled {
		logf("Removing backups above limit of %d", b.LimitCount)
		removed, perr := prune(context.Background(), b)
		for _, name := range removed {
			logf("Removed old backup: %s", name)
		}
		if perr != nil {
			logf("Warning: remove old backups: %v", perr)
		} else if len(removed) == 0 {
			logf("No old backups to remove")
		}
	}
	return nil
}

func tempDir() string {
	return filepath.Join(os.TempDir(), "aura-backups")
}

func cleanTemp(dir string) []string {
	matches, err := filepath.Glob(filepath.Join(dir, "*.7z*"))
	if err != nil {
		return nil
	}
	var removed []string
	for _, m := range matches {
		if err := os.RemoveAll(m); err == nil {
			removed = append(removed, filepath.Base(m))
		}
	}
	return removed
}

func findSevenZip() (string, error) {
	for _, name := range []string{"7z", "7zz", "7za"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("7z not found, install it with: apt install p7zip-full")
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "backup"
	}
	return s
}

func archiveName(b Backup, t time.Time) string {
	t = timezone.In(t)
	return fmt.Sprintf("%s-%s-%s-%d.7z", slugify(b.Name), strings.ToLower(t.Format("02-January-2006")), t.Format("03-04-PM"), t.Unix())
}

func ownsArchive(b Backup, name string) bool {
	if strings.HasPrefix(name, fmt.Sprintf("%d-", b.ID)) && strings.HasSuffix(name, ".7z") {
		return true
	}
	pattern := `^` + regexp.QuoteMeta(slugify(b.Name)) + `-\d{2}-[a-z]+-\d{4}-\d{2}-\d{2}-(AM|PM)-\d+\.7z$`
	return regexp.MustCompile(pattern).MatchString(name)
}
