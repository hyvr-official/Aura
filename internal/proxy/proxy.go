package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	AvailableDir = "/etc/nginx/sites-available"
	EnabledDir   = "/etc/nginx/sites-enabled"
)

var (
	ErrNginxMissing = errors.New("nginx is not installed")
	ErrNotFound     = errors.New("site not found")
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

type TestError struct {
	Output string
}

func (e *TestError) Error() string {
	return "nginx config test failed: " + e.Output
}

type Site struct {
	Name     string
	Enabled  bool
	Domains  []string
	Listen   string
	Upstream string
	SSL      bool
	ModTime  time.Time
	Config   string
}

func (s Site) DomainsLabel() string {
	if len(s.Domains) == 0 {
		return "—"
	}
	return strings.Join(s.Domains, ", ")
}

func (s Site) UpstreamLabel() string {
	if s.Upstream == "" {
		return "—"
	}
	return s.Upstream
}

func (s Site) ModifiedLabel() string {
	if s.ModTime.IsZero() {
		return "—"
	}
	return s.ModTime.Local().Format("2006-01-02 15:04")
}

func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

func Exists(name string) bool {
	_, err := os.Lstat(availablePath(name))
	return err == nil
}

func List() ([]Site, error) {
	if _, err := exec.LookPath("nginx"); err != nil {
		return nil, ErrNginxMissing
	}
	entries, err := os.ReadDir(AvailableDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Site
	for _, e := range entries {
		if e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		s, err := Get(e.Name())
		if err != nil {
			continue
		}
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

func Get(name string) (Site, error) {
	if !ValidName(name) {
		return Site{}, ErrNotFound
	}
	p := availablePath(name)
	info, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return Site{}, ErrNotFound
	}
	if err != nil {
		return Site{}, err
	}
	if info.IsDir() {
		return Site{}, ErrNotFound
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Site{}, err
	}
	s := Site{Name: name, ModTime: info.ModTime(), Config: string(data)}
	_, err = os.Lstat(enabledPath(name))
	s.Enabled = err == nil
	parse(&s)
	return s, nil
}

func parse(s *Site) {
	seen := map[string]bool{}
	for _, line := range strings.Split(s.Config, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(strings.ReplaceAll(line, ";", " ; "))
		if len(fields) < 2 {
			continue
		}
		args := fields[1:]
		if i := indexOf(args, ";"); i >= 0 {
			args = args[:i]
		}
		if len(args) == 0 {
			continue
		}
		switch fields[0] {
		case "server_name":
			for _, d := range args {
				if d == "_" || d == "\"\"" || seen[d] {
					continue
				}
				seen[d] = true
				s.Domains = append(s.Domains, d)
			}
		case "listen":
			if s.Listen == "" {
				s.Listen = args[0]
			}
			if indexOf(args, "ssl") >= 0 {
				s.SSL = true
			}
		case "proxy_pass":
			if s.Upstream == "" {
				s.Upstream = args[0]
			}
		case "ssl_certificate":
			s.SSL = true
		}
	}
}

func indexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}

func Create(in Input) error {
	if _, err := exec.LookPath("nginx"); err != nil {
		return ErrNginxMissing
	}
	if Exists(in.Name) {
		return fmt.Errorf("site %q already exists", in.Name)
	}
	config, err := in.Config()
	if err != nil {
		return err
	}
	if err := writeFile(availablePath(in.Name), []byte(config)); err != nil {
		return err
	}
	if err := symlink(availablePath(in.Name), enabledPath(in.Name)); err != nil {
		remove(availablePath(in.Name))
		return err
	}
	if err := test(); err != nil {
		remove(enabledPath(in.Name))
		remove(availablePath(in.Name))
		return err
	}
	return reload()
}

func Save(name, config string) error {
	if !ValidName(name) {
		return ErrNotFound
	}
	old, err := os.ReadFile(availablePath(name))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := writeFile(availablePath(name), []byte(config)); err != nil {
		return err
	}
	if err := test(); err != nil {
		if rerr := writeFile(availablePath(name), old); rerr != nil {
			return fmt.Errorf("%v (restore failed: %v)", err, rerr)
		}
		return err
	}
	return reload()
}

func Enable(name string) error {
	if !Exists(name) {
		return ErrNotFound
	}
	if err := symlink(availablePath(name), enabledPath(name)); err != nil {
		return err
	}
	if err := test(); err != nil {
		remove(enabledPath(name))
		return err
	}
	return reload()
}

func Disable(name string) error {
	if !Exists(name) {
		return ErrNotFound
	}
	if err := remove(enabledPath(name)); err != nil {
		return err
	}
	if err := test(); err != nil {
		return err
	}
	return reload()
}

func Delete(name string) error {
	if !Exists(name) {
		return ErrNotFound
	}
	if err := remove(enabledPath(name)); err != nil {
		return err
	}
	if err := remove(availablePath(name)); err != nil {
		return err
	}
	if err := test(); err != nil {
		return err
	}
	return reload()
}

func IssueSSL(domains []string, email string) error {
	if len(domains) == 0 {
		return errors.New("no domains to issue a certificate for")
	}
	if _, err := exec.LookPath("certbot"); err != nil {
		return errors.New("certbot is not installed")
	}
	args := []string{"certbot", "--nginx", "--non-interactive", "--agree-tos", "--redirect"}
	for _, d := range domains {
		args = append(args, "-d", d)
	}
	if email != "" {
		args = append(args, "-m", email)
	} else {
		args = append(args, "--register-unsafely-without-email")
	}
	_, err := run(args...)
	return err
}

func EnsureRunning() error {
	if _, err := exec.LookPath("nginx"); err != nil {
		return ErrNginxMissing
	}
	_, err := run("systemctl", "enable", "--now", "nginx")
	return err
}

func test() error {
	out, err := command("nginx", "-t").CombinedOutput()
	if err != nil {
		return &TestError{Output: strings.TrimSpace(string(out))}
	}
	return nil
}

func reload() error {
	_, err := run("systemctl", "reload-or-restart", "nginx")
	return err
}

func availablePath(name string) string {
	return filepath.Join(AvailableDir, name)
}

func enabledPath(name string) string {
	return filepath.Join(EnabledDir, name)
}

func isRoot() bool {
	return os.Geteuid() == 0
}

func writeFile(path string, data []byte) error {
	if isRoot() {
		return os.WriteFile(path, data, 0644)
	}
	cmd := exec.Command("sudo", "-n", "tee", path)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write %s: %v: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func symlink(target, link string) error {
	if isRoot() {
		if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Symlink(target, link)
	}
	_, err := run("ln", "-sfn", target, link)
	return err
}

func remove(path string) error {
	if isRoot() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	_, err := run("rm", "-f", path)
	return err
}

func command(args ...string) *exec.Cmd {
	if !isRoot() {
		args = append([]string{"sudo", "-n"}, args...)
	}
	return exec.Command(args[0], args[1:]...)
}

func run(args ...string) (string, error) {
	out, err := command(args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %v: %s", strings.Join(args, " "), err, lastLines(string(out), 5))
	}
	return string(out), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
