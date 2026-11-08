package containers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var volumePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]+$`)

type Volume struct {
	Name       string   `json:"Name"`
	Driver     string   `json:"Driver"`
	Scope      string   `json:"Scope"`
	Mountpoint string   `json:"Mountpoint"`
	Labels     string   `json:"Labels"`
	UsedBy     []string `json:"-"`
}

func (v Volume) InUse() bool {
	return len(v.UsedBy) > 0
}

func ValidVolumeName(name string) bool {
	return volumePattern.MatchString(name)
}

func ListVolumes() ([]Volume, error) {
	out, err := run("volume", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	var list []Volume
	err = decodeLines(out, func(line []byte) error {
		var v Volume
		if err := json.Unmarshal(line, &v); err != nil {
			return fmt.Errorf("parse docker volume ls output: %w", err)
		}
		list = append(list, v)
		return nil
	})
	if err != nil {
		return nil, err
	}
	usage, err := volumeUsage()
	if err != nil {
		return nil, err
	}
	for i := range list {
		names := usage[list[i].Name]
		sort.Strings(names)
		list[i].UsedBy = names
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

func volumeUsage() (map[string][]string, error) {
	out, err := run("ps", "-a", "--no-trunc", "--format", "{{.Names}}\t{{.Mounts}}")
	if err != nil {
		return nil, err
	}
	usage := map[string][]string{}
	err = decodeLines(out, func(line []byte) error {
		parts := strings.SplitN(string(line), "\t", 2)
		if len(parts) != 2 {
			return nil
		}
		for _, mount := range strings.Split(parts[1], ",") {
			mount = strings.TrimSpace(mount)
			if mount == "" {
				continue
			}
			usage[mount] = append(usage[mount], parts[0])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return usage, nil
}

func CreateVolume(name, driver string) error {
	if driver == "" {
		driver = "local"
	}
	_, err := run("volume", "create", "--driver", driver, name)
	return err
}

func RemoveVolume(name string) error {
	_, err := run("volume", "rm", name)
	return err
}
