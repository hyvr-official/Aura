package startup

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"aura/internal/backups"
	"aura/internal/proxy"
)

type requirement struct {
	pkgs     []string
	commands []string
}

type Check struct {
	Name    string
	OK      bool
	Message string
}

type Report struct {
	Checks []Check
	RanAt  time.Time
}

func (r Report) Failed() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return true
		}
	}
	return false
}

var requirements = []requirement{
	{pkgs: []string{"cron"}, commands: []string{"crontab"}},
	{pkgs: []string{"p7zip-full"}, commands: []string{"7z", "7zz", "7za"}},
	{pkgs: []string{"nginx"}, commands: []string{"nginx"}},
	{pkgs: []string{"certbot", "python3-certbot-nginx"}, commands: []string{"certbot"}},
	{pkgs: []string{"git"}, commands: []string{"git"}},
	{pkgs: []string{"openssh-client"}, commands: []string{"ssh-keygen"}},
	{pkgs: []string{"docker.io", "docker-compose-v2"}, commands: []string{"docker"}},
}

var (
	runMu   sync.Mutex
	stateMu sync.RWMutex
	last    Report
)

func Last() Report {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return last
}

func Run() Report {
	runMu.Lock()
	defer runMu.Unlock()

	log.Printf("startup: checking requirements")

	checks := make([]Check, len(requirements))
	var missing []string
	var missingIdx []int
	for i, r := range requirements {
		name := strings.Join(r.pkgs, ", ")
		if installed(r) {
			log.Printf("startup: %s found", name)
			checks[i] = Check{Name: name, OK: true, Message: "Found"}
			continue
		}
		missing = append(missing, r.pkgs...)
		missingIdx = append(missingIdx, i)
	}
	if len(missing) > 0 {
		log.Printf("startup: installing %s", strings.Join(missing, ", "))
		installErr := aptInstall(missing)
		if installErr != nil {
			log.Printf("startup: install packages: %v", installErr)
		} else {
			log.Printf("startup: installed %s", strings.Join(missing, ", "))
		}
		for _, i := range missingIdx {
			r := requirements[i]
			name := strings.Join(r.pkgs, ", ")
			switch {
			case installed(r):
				checks[i] = Check{Name: name, OK: true, Message: "Installed"}
			case installErr != nil:
				checks[i] = Check{Name: name, Message: "Install failed: " + installErr.Error()}
			default:
				checks[i] = Check{Name: name, Message: "Not found after install"}
			}
		}
	}

	if err := backups.EnsureCron(); err != nil {
		log.Printf("startup: backup cron: %v", err)
		checks = append(checks, Check{Name: "Backup cron", Message: err.Error()})
	} else {
		log.Printf("startup: backup cron is set (daily at 00:00)")
		checks = append(checks, Check{Name: "Backup cron", OK: true, Message: "Set (daily at 00:00)"})
	}

	if err := proxy.EnsureRunning(); err != nil {
		log.Printf("startup: nginx: %v", err)
		checks = append(checks, Check{Name: "Nginx service", Message: err.Error()})
	} else {
		log.Printf("startup: nginx is running")
		checks = append(checks, Check{Name: "Nginx service", OK: true, Message: "Running"})
	}

	if err := ensureDockerRunning(); err != nil {
		log.Printf("startup: docker: %v", err)
		checks = append(checks, Check{Name: "Docker service", Message: err.Error()})
	} else {
		log.Printf("startup: docker is running")
		checks = append(checks, Check{Name: "Docker service", OK: true, Message: "Running"})
	}

	report := Report{Checks: checks, RanAt: time.Now()}
	stateMu.Lock()
	last = report
	stateMu.Unlock()
	return report
}

func installed(r requirement) bool {
	for _, c := range r.commands {
		if _, err := exec.LookPath(c); err == nil {
			return true
		}
	}
	return false
}

func aptInstall(pkgs []string) error {
	install := append([]string{"apt-get", "install", "-y", "--no-install-recommends"}, pkgs...)
	if err := runRoot(install...); err == nil {
		return nil
	}
	if err := runRoot("apt-get", "update"); err != nil {
		return err
	}
	return runRoot(install...)
}

func ensureDockerRunning() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("docker is not installed")
	}
	return runRoot("systemctl", "enable", "--now", "docker")
}

func runRoot(args ...string) error {
	if os.Geteuid() != 0 {
		args = append([]string{"sudo", "-n"}, args...)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", strings.Join(args, " "), err, lastLines(string(out), 5))
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
