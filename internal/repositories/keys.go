package repositories

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func GenerateKey(comment string) (string, string, error) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		return "", "", errors.New("ssh-keygen is not installed, install it with: apt install openssh-client")
	}
	dir, err := os.MkdirTemp("", "aura-key-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(dir)

	keyPath := filepath.Join(dir, "key")
	out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", comment, "-f", keyPath, "-q").CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("ssh-keygen: %v: %s", err, strings.TrimSpace(string(out)))
	}
	private, err := os.ReadFile(keyPath)
	if err != nil {
		return "", "", err
	}
	public, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(string(public)), string(private), nil
}

func KeyFilePath(r Repository) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", r.KeyFileName()), nil
}

func SaveKeyFile(r Repository) (string, error) {
	keyPath, err := KeyFilePath(r)
	if err != nil {
		return "", err
	}
	private := r.PrivateKey
	dir := filepath.Dir(keyPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if !strings.HasSuffix(private, "\n") {
		private += "\n"
	}
	tmp, err := os.CreateTemp(dir, ".aura-key-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", err
	}
	if _, err := tmp.WriteString(private); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := os.Rename(tmpPath, keyPath); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	return keyPath, nil
}

func RemoveKeyFile(r Repository) error {
	keyPath, err := KeyFilePath(r)
	if err != nil {
		return err
	}
	if err := os.Remove(keyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func SSHCommand(keyPath string) string {
	return "ssh -i '" + keyPath + "' -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"
}

func writeKey(private string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "aura-git-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	keyPath := filepath.Join(dir, "key")
	if !strings.HasSuffix(private, "\n") {
		private += "\n"
	}
	if err := os.WriteFile(keyPath, []byte(private), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return keyPath, cleanup, nil
}
