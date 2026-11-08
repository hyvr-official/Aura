package console

import (
	"os"
	"os/exec"
)

func Shell() *exec.Cmd {
	if os.Geteuid() == 0 {
		cmd := exec.Command("bash", "-l")
		cmd.Dir = "/root"
		cmd.Env = append(os.Environ(), "HOME=/root", "USER=root", "LOGNAME=root", "SHELL=/bin/bash", "TERM=xterm-256color")
		return cmd
	}
	cmd := exec.Command("sudo", "-n", "-i", "-u", "root")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	return cmd
}
