package backups

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"aura/internal/timezone"
)

const cronMarker = "# aura-backup"

func EnsureCron() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	want := fmt.Sprintf("%s cd %s && %s backup >> %s 2>&1 %s",
		cronSchedule(), shellQuote(wd), shellQuote(exe), shellQuote(filepath.Join(wd, "backups.log")), cronMarker)

	current, err := readCrontab()
	if err != nil {
		return err
	}

	var lines []string
	found := false
	for _, line := range strings.Split(strings.TrimRight(current, "\n"), "\n") {
		if strings.Contains(line, cronMarker) {
			if found {
				continue
			}
			found = true
			if line == want {
				lines = append(lines, line)
				continue
			}
			line = want
		}
		if line != "" || len(lines) > 0 {
			lines = append(lines, line)
		}
	}
	if !found {
		lines = append(lines, want)
	}

	updated := strings.Join(lines, "\n") + "\n"
	if updated == current {
		return nil
	}
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(updated)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("crontab -: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func cronSchedule() string {
	loc := timezone.Location()
	now := time.Now().In(loc)
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc).In(time.Local)
	return fmt.Sprintf("%d %d * * *", next.Minute(), next.Hour())
}

func readCrontab() (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("crontab", "-l")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "no crontab") {
			return "", nil
		}
		return "", fmt.Errorf("crontab -l: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
