package startup

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"aura/internal/backups"
	"aura/internal/proxy"
)

type requirement struct {
	pkgs     []string
	commands []string
}

var requirements = []requirement{
	{pkgs: []string{"cron"}, commands: []string{"crontab"}},
	{pkgs: []string{"p7zip-full"}, commands: []string{"7z", "7zz", "7za"}},
	{pkgs: []string{"nginx"}, commands: []string{"nginx"}},
	{pkgs: []string{"certbot", "python3-certbot-nginx"}, commands: []string{"certbot"}},
	{pkgs: []string{"git"}, commands: []string{"git"}},
	{pkgs: []string{"openssh-client"}, commands: []string{"ssh-keygen"}},
}

func Run() {
	log.Printf("startup: checking requirements")

	var missing []string
	for _, r := range requirements {
		if installed(r) {
			log.Printf("startup: %s found", strings.Join(r.pkgs, ", "))
			continue
		}
		missing = append(missing, r.pkgs...)
	}
	if len(missing) > 0 {
		log.Printf("startup: installing %s", strings.Join(missing, ", "))
		if err := aptInstall(missing); err != nil {
			log.Printf("startup: install packages: %v", err)
		} else {
			log.Printf("startup: installed %s", strings.Join(missing, ", "))
		}
	}

	if err := backups.EnsureCron(); err != nil {
		log.Printf("startup: backup cron: %v", err)
	} else {
		log.Printf("startup: backup cron is set (daily at 00:00)")
	}

	if err := proxy.EnsureRunning(); err != nil {
		log.Printf("startup: nginx: %v", err)
	} else {
		log.Printf("startup: nginx is running")
	}
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
	if err := apt(install...); err == nil {
		return nil
	}
	if err := apt("apt-get", "update"); err != nil {
		return err
	}
	return apt(install...)
}

func apt(args ...string) error {
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
