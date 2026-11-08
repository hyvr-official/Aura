package projects

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	ActionPull   = "pull"
	ActionDeploy = "deploy"
	ActionDown   = "down"
)

const (
	operationTimeout = 30 * time.Minute
	maxScanDepth     = 5
)

var (
	ErrBusy          = errors.New("another operation is already running for this project")
	ErrDockerMissing = errors.New("docker is not installed")
)

var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
}

type Folder struct {
	Rel           string
	Abs           string
	ComposeFile   string
	HasDockerfile bool
}

func (f Folder) Label() string {
	if f.Rel == "." {
		return "Project root"
	}
	return filepath.Base(f.Rel)
}

func (f Folder) IsCompose() bool { return f.ComposeFile != "" }

func (f Folder) Kind() string {
	if f.IsCompose() {
		return "Compose"
	}
	return "Dockerfile"
}

func (f Folder) File() string {
	if f.IsCompose() {
		return f.ComposeFile
	}
	return "Dockerfile"
}

func ScanFolders(root string) ([]Folder, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", root)
	}
	var list []Folder
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return fs.SkipDir
		}
		if rel != "." {
			name := d.Name()
			if strings.HasPrefix(name, ".") || skipDirs[name] {
				return fs.SkipDir
			}
			if strings.Count(rel, string(filepath.Separator))+1 > maxScanDepth {
				return fs.SkipDir
			}
		}
		if f, ok := detect(p, filepath.ToSlash(rel)); ok {
			list = append(list, f)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Rel == "." {
			return list[j].Rel != "."
		}
		if list[j].Rel == "." {
			return false
		}
		return list[i].Rel < list[j].Rel
	})
	return list, nil
}

func detect(dir, rel string) (Folder, bool) {
	f := Folder{Rel: rel, Abs: dir}
	for _, name := range composeFiles {
		if isFile(filepath.Join(dir, name)) {
			f.ComposeFile = name
			break
		}
	}
	f.HasDockerfile = isFile(filepath.Join(dir, "Dockerfile"))
	return f, f.ComposeFile != "" || f.HasDockerfile
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func ContainerName(p Project, f Folder) string {
	name := "root"
	if f.Rel != "." {
		name = slug(f.Rel, "root")
	}
	return "aura-project-" + strconv.FormatInt(p.ID, 10) + "-" + name
}

type Docker struct {
	mu   sync.Mutex
	busy map[int64]bool
}

func NewDocker() *Docker {
	return &Docker{busy: map[int64]bool{}}
}

func (d *Docker) lock(id int64) (func(), error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.busy[id] {
		return nil, ErrBusy
	}
	d.busy[id] = true
	return func() {
		d.mu.Lock()
		delete(d.busy, id)
		d.mu.Unlock()
	}, nil
}

type session struct {
	docker string
	ctx    context.Context
	log    strings.Builder
}

func (d *Docker) start(p Project) (*session, func(), error) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return nil, nil, ErrDockerMissing
	}
	unlock, err := d.lock(p.ID)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	return &session{docker: docker, ctx: ctx}, func() {
		cancel()
		unlock()
	}, nil
}

func (s *session) run(dir string, args ...string) error {
	fmt.Fprintf(&s.log, "$ docker %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(s.ctx, s.docker, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if text := strings.TrimRight(string(out), "\n"); text != "" {
		s.log.WriteString(text + "\n")
	}
	if errors.Is(s.ctx.Err(), context.DeadlineExceeded) {
		return errors.New("operation timed out")
	}
	if err != nil {
		return fmt.Errorf("docker %s: %v", args[0], err)
	}
	return nil
}

func (s *session) finish(err error) (string, error) {
	if err != nil {
		fmt.Fprintf(&s.log, "Failed: %v\n", err)
	} else {
		s.log.WriteString("Finished successfully\n")
	}
	return s.log.String(), err
}

func (d *Docker) Deploy(p Project, f Folder) (string, error) {
	s, done, err := d.start(p)
	if err != nil {
		return err.Error(), err
	}
	defer done()
	if f.IsCompose() {
		return s.finish(s.run(f.Abs, "compose", "-f", f.ComposeFile, "up", "-d", "--build"))
	}
	name := ContainerName(p, f)
	if err := s.run(f.Abs, "build", "-t", name, "."); err != nil {
		return s.finish(err)
	}
	s.run(f.Abs, "rm", "-f", name)
	return s.finish(s.run(f.Abs, "run", "-d", "--name", name, "--restart", "unless-stopped", name))
}

func (d *Docker) Down(p Project, f Folder) (string, error) {
	s, done, err := d.start(p)
	if err != nil {
		return err.Error(), err
	}
	defer done()
	if f.IsCompose() {
		return s.finish(s.run(f.Abs, "compose", "-f", f.ComposeFile, "down"))
	}
	return s.finish(s.run(f.Abs, "rm", "-f", ContainerName(p, f)))
}
