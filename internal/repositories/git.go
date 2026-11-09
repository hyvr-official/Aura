package repositories

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ActionClone    = "clone"
	ActionPull     = "pull"
	ActionCheckout = "checkout"
	ActionWebhook  = "webhook"
)

const (
	StateMissing  = "missing"
	StateEmpty    = "empty"
	StateRepo     = "repo"
	StateNotEmpty = "not_empty"
	StateNotDir   = "not_dir"
)

const (
	operationTimeout = 5 * time.Minute
	postPullTimeout  = 30 * time.Minute
	listTimeout      = 20 * time.Second
)

var (
	ErrBusy       = errors.New("another operation is already running for this repository")
	ErrGitMissing = errors.New("git is not installed, install it with: apt install git")
)

type FolderState string

func (s FolderState) CanClone() bool { return s == StateMissing || s == StateEmpty }
func (s FolderState) IsRepo() bool   { return s == StateRepo }
func (s FolderState) IsBlocked() bool {
	return s == StateNotEmpty || s == StateNotDir
}

func (s FolderState) Label() string {
	switch s {
	case StateMissing:
		return "Folder missing"
	case StateEmpty:
		return "Empty folder"
	case StateRepo:
		return "Cloned"
	case StateNotEmpty:
		return "Not a git repository"
	case StateNotDir:
		return "Not a folder"
	}
	return string(s)
}

func (s FolderState) Badge() string {
	switch s {
	case StateRepo:
		return "text-bg-success"
	case StateNotEmpty, StateNotDir:
		return "text-bg-warning"
	}
	return "text-bg-secondary"
}

type Commit struct {
	Hash        string
	ShortHash   string
	AuthorName  string
	AuthorEmail string
	Date        time.Time
	Subject     string
}

func (c Commit) DateLabel() string {
	if c.Date.IsZero() {
		return "—"
	}
	return c.Date.Local().Format("2006-01-02 15:04")
}

type Info struct {
	Branch string
	Commit Commit
}

func (i Info) Detached() bool { return i.Branch == "HEAD" }

func State(dir string) FolderState {
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return StateMissing
	}
	if err != nil {
		return StateNotEmpty
	}
	if !info.IsDir() {
		return StateNotDir
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return StateRepo
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return StateNotEmpty
	}
	return StateEmpty
}

type Git struct {
	mu   sync.Mutex
	busy map[int64]bool
}

func NewGit() *Git {
	return &Git{busy: map[int64]bool{}}
}

func (g *Git) lock(id int64) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.busy[id] {
		return nil, ErrBusy
	}
	g.busy[id] = true
	return func() {
		g.mu.Lock()
		delete(g.busy, id)
		g.mu.Unlock()
	}, nil
}

type session struct {
	repo    Repository
	keyPath string
	ctx     context.Context
	log     strings.Builder
}

func (g *Git) start(r Repository, timeout time.Duration, locked bool) (*session, func(), error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, nil, ErrGitMissing
	}
	unlock := func() {}
	if locked {
		u, err := g.lock(r.ID)
		if err != nil {
			return nil, nil, err
		}
		unlock = u
	}
	keyPath, removeKey, err := writeKey(r.PrivateKey)
	if err != nil {
		unlock()
		return nil, nil, fmt.Errorf("write ssh key: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	s := &session{repo: r, keyPath: keyPath, ctx: ctx}
	return s, func() {
		cancel()
		removeKey()
		unlock()
	}, nil
}

func (s *session) run(dir string, args ...string) (string, error) {
	full := append([]string{"-c", "safe.directory=" + s.repo.Path}, args...)
	fmt.Fprintf(&s.log, "$ git %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(s.ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -i '"+s.keyPath+"' -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=accept-new",
	)
	out, err := cmd.CombinedOutput()
	if text := strings.TrimRight(string(out), "\n"); text != "" {
		s.log.WriteString(text + "\n")
	}
	if errors.Is(s.ctx.Err(), context.DeadlineExceeded) {
		return string(out), errors.New("operation timed out")
	}
	if err != nil {
		return string(out), fmt.Errorf("git %s: %v", args[0], err)
	}
	return string(out), nil
}

func (s *session) script(dir, script string) error {
	s.log.WriteString("$ post pull commands\n")
	cmd := exec.CommandContext(s.ctx, "bash", "-e", "-c", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if text := strings.TrimRight(string(out), "\n"); text != "" {
		s.log.WriteString(text + "\n")
	}
	if errors.Is(s.ctx.Err(), context.DeadlineExceeded) {
		return errors.New("post pull commands timed out")
	}
	if err != nil {
		return fmt.Errorf("post pull commands: %v", err)
	}
	return nil
}

func (s *session) persistKey() (string, error) {
	keyPath, err := SaveKeyFile(s.repo)
	if err != nil {
		return "", fmt.Errorf("save ssh key file: %w", err)
	}
	return keyPath, nil
}

func (s *session) configureSSH() error {
	keyPath, err := s.persistKey()
	if err != nil {
		return err
	}
	_, err = s.run(s.repo.Path, "config", "core.sshCommand", SSHCommand(keyPath))
	return err
}

func (s *session) finish(err error) (string, error) {
	if err != nil {
		fmt.Fprintf(&s.log, "Failed: %v\n", err)
	} else {
		s.log.WriteString("Finished successfully\n")
	}
	return s.log.String(), err
}

func (g *Git) Clone(r Repository) (string, error) {
	s, done, err := g.start(r, operationTimeout, true)
	if err != nil {
		return err.Error(), err
	}
	defer done()
	state := State(r.Path)
	if !state.CanClone() {
		return s.finish(fmt.Errorf("folder %s is not empty", r.Path))
	}
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o755); err != nil {
		return s.finish(fmt.Errorf("create parent folder: %w", err))
	}
	keyPath, err := s.persistKey()
	if err != nil {
		return s.finish(err)
	}
	_, err = s.run(filepath.Dir(r.Path), "clone", "-c", "core.sshCommand="+SSHCommand(keyPath), "--depth", "1", r.URL, r.Path)
	return s.finish(err)
}

func (g *Git) Pull(r Repository) (string, error) {
	timeout := operationTimeout
	if strings.TrimSpace(r.PostPullCommands) != "" {
		timeout += postPullTimeout
	}
	s, done, err := g.start(r, timeout, true)
	if err != nil {
		return err.Error(), err
	}
	defer done()
	if !State(r.Path).IsRepo() {
		return s.finish(fmt.Errorf("folder %s is not a git repository", r.Path))
	}
	if err := s.configureSSH(); err != nil {
		return s.finish(err)
	}
	out, err := s.run(r.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return s.finish(err)
	}
	branch := strings.TrimSpace(out)
	if branch == "" || branch == "HEAD" {
		return s.finish(errors.New("repository is not on a branch, change the branch first"))
	}
	if _, err := s.run(r.Path, "fetch", "--depth", "1", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch); err != nil {
		return s.finish(err)
	}
	if _, err := s.run(r.Path, "reset", "--hard", "origin/"+branch); err != nil {
		return s.finish(err)
	}
	if strings.TrimSpace(r.PostPullCommands) != "" {
		return s.finish(s.script(r.Path, r.PostPullCommands))
	}
	return s.finish(nil)
}

func CurrentBranch(r Repository) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", ErrGitMissing
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "safe.directory="+r.Path, "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = r.Path
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %v: %s", err, lastLine(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *Git) Checkout(r Repository, branch string) (string, error) {
	s, done, err := g.start(r, operationTimeout, true)
	if err != nil {
		return err.Error(), err
	}
	defer done()
	if !State(r.Path).IsRepo() {
		return s.finish(fmt.Errorf("folder %s is not a git repository", r.Path))
	}
	if branch == "" || strings.HasPrefix(branch, "-") {
		return s.finish(errors.New("invalid branch name"))
	}
	if err := s.configureSSH(); err != nil {
		return s.finish(err)
	}
	if _, err := s.run(r.Path, "check-ref-format", "--branch", branch); err != nil {
		return s.finish(errors.New("invalid branch name"))
	}
	if _, err := s.run(r.Path, "fetch", "--depth", "1", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch); err != nil {
		return s.finish(err)
	}
	_, err = s.run(r.Path, "checkout", "-f", "-B", branch, "origin/"+branch)
	return s.finish(err)
}

func (g *Git) Branches(r Repository) ([]string, error) {
	s, done, err := g.start(r, listTimeout, false)
	if err != nil {
		return nil, err
	}
	defer done()
	out, err := s.run(r.Path, "ls-remote", "--heads", "origin")
	if err == nil {
		return parseHeads(out), nil
	}
	remoteErr := fmt.Errorf("%v: %s", err, lastLine(out))
	local, lerr := s.run(r.Path, "for-each-ref", "--format=%(refname:lstrip=3)", "refs/remotes/origin")
	if lerr != nil {
		return nil, remoteErr
	}
	var list []string
	for _, line := range strings.Split(local, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && line != "HEAD" {
			list = append(list, line)
		}
	}
	sort.Strings(list)
	return list, remoteErr
}

func (g *Git) SetRemote(r Repository) error {
	s, done, err := g.start(r, listTimeout, true)
	if err != nil {
		return err
	}
	defer done()
	out, err := s.run(r.Path, "remote", "set-url", "origin", r.URL)
	if err != nil {
		return fmt.Errorf("%v: %s", err, lastLine(out))
	}
	return nil
}

func (g *Git) MoveKey(old, r Repository) error {
	if old.KeyFileName() == r.KeyFileName() {
		return nil
	}
	s, done, err := g.start(r, listTimeout, true)
	if err != nil {
		return err
	}
	defer done()
	if State(r.Path).IsRepo() {
		if err := s.configureSSH(); err != nil {
			return err
		}
	} else if _, err := s.persistKey(); err != nil {
		return err
	}
	return RemoveKeyFile(old)
}

func ReadInfo(r Repository) (Info, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return Info{}, ErrGitMissing
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "safe.directory=" + r.Path}, args...)...)
		cmd.Dir = r.Path
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", args[0], err, lastLine(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	var info Info
	branch, err := git("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return info, err
	}
	info.Branch = branch
	out, err := git("log", "-1", "--format=%H%x1f%h%x1f%an%x1f%ae%x1f%aI%x1f%s")
	if err != nil {
		return info, err
	}
	parts := strings.SplitN(out, "\x1f", 6)
	if len(parts) == 6 {
		info.Commit = Commit{
			Hash:        parts[0],
			ShortHash:   parts[1],
			AuthorName:  parts[2],
			AuthorEmail: parts[3],
			Subject:     parts[5],
		}
		info.Commit.Date, _ = time.Parse(time.RFC3339, parts[4])
	}
	return info, nil
}

func parseHeads(out string) []string {
	var list []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "refs/heads/") {
			continue
		}
		list = append(list, strings.TrimPrefix(fields[1], "refs/heads/"))
	}
	sort.Strings(list)
	return list
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
