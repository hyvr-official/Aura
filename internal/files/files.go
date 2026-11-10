package files

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"aura/internal/timezone"
)

const MaxEditSize = 2 * 1024 * 1024

var (
	ErrOutsideRoot = errors.New("path is outside the allowed root")
	ErrInvalidName = errors.New("invalid name")
	ErrExists      = errors.New("a file or folder with that name already exists")
	ErrNotText     = errors.New("file is not a text file")
	ErrTooLarge    = errors.New("file is too large to edit")
	ErrIsRoot      = errors.New("the root folder cannot be changed")
	ErrNotDir      = errors.New("path is not a folder")
	ErrNotFile     = errors.New("path is not a regular file")
)

type Entry struct {
	Name    string
	Path    string
	IsDir   bool
	IsLink  bool
	Size    int64
	Mode    string
	ModTime time.Time
}

func (e Entry) SizeLabel() string {
	if e.IsDir {
		return "-"
	}
	return FormatSize(e.Size)
}

func (e Entry) ModTimeLabel() string {
	return timezone.Format(e.ModTime, "2006-01-02 15:04")
}

type Crumb struct {
	Name string
	Path string
}

type Manager struct {
	root string
}

func New(root string) *Manager {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = filepath.Clean(root)
	}
	return &Manager{root: abs}
}

func (m *Manager) Root() string {
	return m.root
}

func (m *Manager) Resolve(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return m.root, nil
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(m.root, p)
	}
	abs, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	if !m.within(abs) {
		return "", ErrOutsideRoot
	}
	return abs, nil
}

func (m *Manager) within(p string) bool {
	if p == m.root {
		return true
	}
	prefix := m.root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(p, prefix)
}

func (m *Manager) IsRoot(p string) bool {
	return p == m.root
}

func ValidName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return false
	}
	return !strings.ContainsAny(name, "/\x00")
}

func (m *Manager) List(dir string) ([]Entry, error) {
	dir, err := m.Resolve(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrNotDir
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	list := make([]Entry, 0, len(items))
	for _, item := range items {
		full := filepath.Join(dir, item.Name())
		linfo, err := item.Info()
		if err != nil {
			continue
		}
		info := linfo
		isLink := linfo.Mode()&os.ModeSymlink != 0
		if isLink {
			if target, err := os.Stat(full); err == nil {
				info = target
			}
		}
		list = append(list, Entry{
			Name:    item.Name(),
			Path:    full,
			IsDir:   info.IsDir(),
			IsLink:  isLink,
			Size:    info.Size(),
			Mode:    linfo.Mode().String(),
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].IsDir != list[j].IsDir {
			return list[i].IsDir
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	return list, nil
}

func (m *Manager) Stat(p string) (string, os.FileInfo, error) {
	p, err := m.Resolve(p)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", nil, err
	}
	return p, info, nil
}

func (m *Manager) Breadcrumbs(dir string) []Crumb {
	crumbs := []Crumb{{Name: m.root, Path: m.root}}
	rel, err := filepath.Rel(m.root, dir)
	if err != nil || rel == "." {
		return crumbs
	}
	current := m.root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		crumbs = append(crumbs, Crumb{Name: part, Path: current})
	}
	return crumbs
}

func (m *Manager) Parent(p string) string {
	if m.IsRoot(p) {
		return ""
	}
	return filepath.Dir(p)
}

func (m *Manager) CreateFile(dir, name string) (string, error) {
	target, err := m.child(dir, name)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if errors.Is(err, os.ErrExist) {
		return "", ErrExists
	}
	if err != nil {
		return "", err
	}
	return target, f.Close()
}

func (m *Manager) CreateDir(dir, name string) (string, error) {
	target, err := m.child(dir, name)
	if err != nil {
		return "", err
	}
	err = os.Mkdir(target, 0755)
	if errors.Is(err, os.ErrExist) {
		return "", ErrExists
	}
	return target, err
}

func (m *Manager) child(dir, name string) (string, error) {
	if !ValidName(name) {
		return "", ErrInvalidName
	}
	dir, err := m.Resolve(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", ErrNotDir
	}
	return filepath.Join(dir, name), nil
}

func (m *Manager) Rename(p, name string) (string, error) {
	if !ValidName(name) {
		return "", ErrInvalidName
	}
	p, err := m.Resolve(p)
	if err != nil {
		return "", err
	}
	if m.IsRoot(p) {
		return "", ErrIsRoot
	}
	if _, err := os.Lstat(p); err != nil {
		return "", err
	}
	target := filepath.Join(filepath.Dir(p), name)
	if target == p {
		return target, nil
	}
	if _, err := os.Lstat(target); err == nil {
		return "", ErrExists
	}
	return target, os.Rename(p, target)
}

func (m *Manager) Delete(p string) error {
	p, err := m.Resolve(p)
	if err != nil {
		return err
	}
	if m.IsRoot(p) {
		return ErrIsRoot
	}
	info, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(p)
	}
	return os.Remove(p)
}

func (m *Manager) Read(p string) (string, error) {
	p, info, err := m.Stat(p)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", ErrNotFile
	}
	if info.Size() > MaxEditSize {
		return "", ErrTooLarge
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "", ErrNotText
	}
	return string(data), nil
}

func (m *Manager) Open(p string) (*os.File, os.FileInfo, error) {
	p, info, err := m.Stat(p)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, ErrNotFile
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, nil, err
	}
	return f, info, nil
}

func (m *Manager) Write(p, content string) error {
	p, err := m.Resolve(p)
	if err != nil {
		return err
	}
	if len(content) > MaxEditSize {
		return ErrTooLarge
	}
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrNotFile
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".aura-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		return err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		os.Chown(tmpName, int(st.Uid), int(st.Gid))
	}
	return os.Rename(tmpName, target)
}

func FormatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
