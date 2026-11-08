package containers

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var ErrDockerMissing = errors.New("docker is not installed")

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

type Container struct {
	ID        string `json:"ID"`
	Name      string `json:"Names"`
	Image     string `json:"Image"`
	State     string `json:"State"`
	Status    string `json:"Status"`
	Ports     string `json:"Ports"`
	CreatedAt string `json:"CreatedAt"`
	Stats     *Stats `json:"-"`
}

type Stats struct {
	ID       string `json:"ID"`
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
	MemPerc  string `json:"MemPerc"`
}

func (c Container) Running() bool {
	return c.State == "running"
}

func (c Container) StateClass() string {
	return stateClass(c.State)
}

func stateClass(state string) string {
	switch state {
	case "running":
		return "success"
	case "paused", "restarting":
		return "warning"
	case "dead":
		return "danger"
	default:
		return "secondary"
	}
}

func ValidID(id string) bool {
	return idPattern.MatchString(id)
}

func List() ([]Container, error) {
	out, err := run("ps", "-a", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var list []Container
	err = decodeLines(out, func(line []byte) error {
		var c Container
		if err := json.Unmarshal(line, &c); err != nil {
			return fmt.Errorf("parse docker ps output: %w", err)
		}
		list = append(list, c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

func AllStats() (map[string]Stats, error) {
	out, err := run("stats", "--no-stream", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	stats := map[string]Stats{}
	err = decodeLines(out, func(line []byte) error {
		var s Stats
		if err := json.Unmarshal(line, &s); err != nil {
			return fmt.Errorf("parse docker stats output: %w", err)
		}
		stats[s.ID] = s
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stats, nil
}

func ListWithStats() ([]Container, error) {
	var stats map[string]Stats
	var statsErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		stats, statsErr = AllStats()
	}()
	list, err := List()
	wg.Wait()
	if err != nil {
		return nil, err
	}
	if statsErr != nil {
		log.Printf("container stats: %v", statsErr)
	}
	for i := range list {
		if s, ok := stats[list[i].ID]; ok {
			list[i].Stats = &s
		}
	}
	return list, nil
}

func decodeLines(out string, fn func([]byte) error) error {
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if err := fn([]byte(line)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func Start(id string) error {
	_, err := run("start", id)
	return err
}

func Stop(id string) error {
	_, err := run("stop", id)
	return err
}

func Restart(id string) error {
	_, err := run("restart", id)
	return err
}

func Remove(id string) error {
	_, err := run("rm", id)
	return err
}

func Logs(id string, tail int) (*exec.Cmd, error) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return nil, ErrDockerMissing
	}
	return exec.Command(docker, "logs", "--follow", "--tail", strconv.Itoa(tail), "--timestamps", id), nil
}

func Shell(id string) (*exec.Cmd, error) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return nil, ErrDockerMissing
	}
	return exec.Command(docker, "exec", "-it", "-e", "TERM=xterm-256color", id, "sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"), nil
}

func run(args ...string) (string, error) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return "", ErrDockerMissing
	}
	out, err := exec.Command(docker, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
