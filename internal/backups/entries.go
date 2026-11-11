package backups

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type skipFunc func(path, reason string)

func archiveEntries(paths []string, skip skipFunc) (string, []string, error) {
	abs := make([]string, 0, len(paths))
	for _, p := range paths {
		a, err := filepath.Abs(p)
		if err != nil {
			return "", nil, fmt.Errorf("source path: %w", err)
		}
		abs = append(abs, a)
	}

	base := commonParent(abs)
	var entries []string
	for _, p := range abs {
		list, _, err := collectEntries(p, skip)
		if err != nil {
			return "", nil, err
		}
		for _, e := range list {
			rel, err := filepath.Rel(base, e)
			if err != nil {
				return "", nil, fmt.Errorf("relative path: %w", err)
			}
			entries = append(entries, rel)
		}
	}
	return base, entries, nil
}

func collectEntries(dir string, skip skipFunc) ([]string, bool, error) {
	children, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, fmt.Errorf("read folder: %w", err)
	}

	clean := true
	var entries []string
	for _, c := range children {
		path := filepath.Join(dir, c.Name())
		if strings.ContainsAny(c.Name(), "\r\n") {
			skip(path, "name contains a line break")
			clean = false
			continue
		}
		mode := c.Type()
		switch {
		case mode.IsDir():
			list, ok, err := collectEntries(path, skip)
			if err != nil {
				return nil, false, err
			}
			if !ok {
				clean = false
			}
			entries = append(entries, list...)
		case mode.IsRegular(), mode&fs.ModeSymlink != 0:
			entries = append(entries, path)
		default:
			skip(path, fileKind(mode))
			clean = false
		}
	}

	if clean {
		return []string{dir}, true, nil
	}
	return entries, false, nil
}

func commonParent(paths []string) string {
	base := filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		for !isWithin(base, p) {
			parent := filepath.Dir(base)
			if parent == base {
				break
			}
			base = parent
		}
	}
	return base
}

func isWithin(base, path string) bool {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func fileKind(mode fs.FileMode) string {
	switch {
	case mode&fs.ModeCharDevice != 0:
		return "character device"
	case mode&fs.ModeDevice != 0:
		return "block device"
	case mode&fs.ModeNamedPipe != 0:
		return "named pipe"
	case mode&fs.ModeSocket != 0:
		return "socket"
	default:
		return "special file"
	}
}
