package containers

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var ErrNotFound = errors.New("container not found")

type Details struct {
	ID              string          `json:"Id"`
	RawName         string          `json:"Name"`
	Created         string          `json:"Created"`
	Path            string          `json:"Path"`
	Args            []string        `json:"Args"`
	RestartCount    int             `json:"RestartCount"`
	ImageID         string          `json:"Image"`
	State           DetailsState    `json:"State"`
	Config          DetailsConfig   `json:"Config"`
	HostConfig      DetailsHost     `json:"HostConfig"`
	NetworkSettings DetailsNetworks `json:"NetworkSettings"`
	GraphDriver     GraphDriver     `json:"GraphDriver"`
	Mounts          []Mount         `json:"Mounts"`
}

type GraphDriver struct {
	Name string            `json:"Name"`
	Data map[string]string `json:"Data"`
}

type DetailsState struct {
	Status     string  `json:"Status"`
	Pid        int     `json:"Pid"`
	Running    bool    `json:"Running"`
	Paused     bool    `json:"Paused"`
	Restarting bool    `json:"Restarting"`
	ExitCode   int     `json:"ExitCode"`
	Error      string  `json:"Error"`
	StartedAt  string  `json:"StartedAt"`
	FinishedAt string  `json:"FinishedAt"`
	Health     *Health `json:"Health"`
}

type Health struct {
	Status string `json:"Status"`
}

type DetailsConfig struct {
	Image      string            `json:"Image"`
	Hostname   string            `json:"Hostname"`
	User       string            `json:"User"`
	WorkingDir string            `json:"WorkingDir"`
	Env        []string          `json:"Env"`
	Cmd        []string          `json:"Cmd"`
	Entrypoint []string          `json:"Entrypoint"`
	Labels     map[string]string `json:"Labels"`
}

type DetailsHost struct {
	RestartPolicy RestartPolicy `json:"RestartPolicy"`
	NetworkMode   string        `json:"NetworkMode"`
	Privileged    bool          `json:"Privileged"`
}

type RestartPolicy struct {
	Name              string `json:"Name"`
	MaximumRetryCount int    `json:"MaximumRetryCount"`
}

type DetailsNetworks struct {
	Ports    map[string][]PortBinding `json:"Ports"`
	Networks map[string]NetworkAttach `json:"Networks"`
}

type PortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type NetworkAttach struct {
	IPAddress  string `json:"IPAddress"`
	Gateway    string `json:"Gateway"`
	MacAddress string `json:"MacAddress"`
}

type Mount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Driver      string `json:"Driver"`
	Mode        string `json:"Mode"`
	RW          bool   `json:"RW"`
}

type KeyValue struct {
	Key   string
	Value string
}

type NetworkRow struct {
	Name string
	NetworkAttach
}

func (m Mount) IsVolume() bool {
	return m.Type == "volume"
}

func (m Mount) TypeClass() string {
	switch m.Type {
	case "volume":
		return "primary"
	case "bind":
		return "info"
	default:
		return "secondary"
	}
}

func (d Details) Name() string {
	return strings.TrimPrefix(d.RawName, "/")
}

func (d Details) ShortID() string {
	if len(d.ID) > 12 {
		return d.ID[:12]
	}
	return d.ID
}

func (d Details) Running() bool {
	return d.State.Running
}

func (d Details) StateClass() string {
	return stateClass(d.State.Status)
}

func (d Details) HealthClass() string {
	if d.State.Health == nil {
		return "secondary"
	}
	switch d.State.Health.Status {
	case "healthy":
		return "success"
	case "starting":
		return "warning"
	case "unhealthy":
		return "danger"
	default:
		return "secondary"
	}
}

func (d Details) CommandLine() string {
	parts := append([]string{}, d.Config.Entrypoint...)
	parts = append(parts, d.Config.Cmd...)
	if len(parts) == 0 {
		parts = append([]string{d.Path}, d.Args...)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func (d Details) RestartPolicyLabel() string {
	name := d.HostConfig.RestartPolicy.Name
	if name == "" {
		name = "no"
	}
	if name == "on-failure" && d.HostConfig.RestartPolicy.MaximumRetryCount > 0 {
		return fmt.Sprintf("%s (max %d)", name, d.HostConfig.RestartPolicy.MaximumRetryCount)
	}
	return name
}

func (d Details) PortList() []string {
	keys := make([]string, 0, len(d.NetworkSettings.Ports))
	for k := range d.NetworkSettings.Ports {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var list []string
	for _, k := range keys {
		bindings := d.NetworkSettings.Ports[k]
		if len(bindings) == 0 {
			list = append(list, k+" (not published)")
			continue
		}
		for _, b := range bindings {
			host := b.HostIP
			if host == "" {
				host = "0.0.0.0"
			}
			list = append(list, host+":"+b.HostPort+" → "+k)
		}
	}
	return list
}

func (d Details) NetworkList() []NetworkRow {
	var list []NetworkRow
	for name, n := range d.NetworkSettings.Networks {
		list = append(list, NetworkRow{Name: name, NetworkAttach: n})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

func (d Details) EnvList() []KeyValue {
	var list []KeyValue
	for _, e := range d.Config.Env {
		key, value, _ := strings.Cut(e, "=")
		list = append(list, KeyValue{Key: key, Value: value})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return list
}

func (d Details) LabelList() []KeyValue {
	var list []KeyValue
	for k, v := range d.Config.Labels {
		list = append(list, KeyValue{Key: k, Value: v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return list
}

func (d Details) RootFolder() string {
	if !d.State.Running {
		return ""
	}
	if dir := d.GraphDriver.Data["MergedDir"]; dir != "" {
		return dir
	}
	if d.State.Pid > 0 {
		return fmt.Sprintf("/proc/%d/root", d.State.Pid)
	}
	return ""
}

func (d Details) ComposeProject() string {
	return d.Config.Labels["com.docker.compose.project"]
}

func (d Details) ComposeService() string {
	return d.Config.Labels["com.docker.compose.service"]
}

func (d Details) CreatedLabel() string {
	return timeLabel(d.Created)
}

func (d Details) StartedLabel() string {
	return timeLabel(d.State.StartedAt)
}

func (d Details) FinishedLabel() string {
	return timeLabel(d.State.FinishedAt)
}

func timeLabel(value string) string {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || t.Year() <= 1 {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func Inspect(id string) (*Details, error) {
	out, err := run("inspect", "--type", "container", id)
	if err != nil {
		if strings.Contains(err.Error(), "No such") {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var list []Details
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("parse docker inspect output: %w", err)
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

func ContainerStats(id string) (*Stats, error) {
	out, err := run("stats", "--no-stream", "--no-trunc", "--format", "{{json .}}", id)
	if err != nil {
		return nil, err
	}
	var stats *Stats
	err = decodeLines(out, func(line []byte) error {
		var s Stats
		if err := json.Unmarshal(line, &s); err != nil {
			return fmt.Errorf("parse docker stats output: %w", err)
		}
		stats = &s
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stats, nil
}
