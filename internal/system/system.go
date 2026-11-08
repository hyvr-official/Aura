package system

import (
	"bufio"
	"errors"
	"math"
	"os"
	"os/user"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const processLimit = 20

type Usage struct {
	CPU         float64   `json:"cpu"`
	MemUsed     uint64    `json:"memUsed"`
	MemTotal    uint64    `json:"memTotal"`
	MemPercent  float64   `json:"memPercent"`
	SwapUsed    uint64    `json:"swapUsed"`
	SwapTotal   uint64    `json:"swapTotal"`
	SwapPercent float64   `json:"swapPercent"`
	DiskUsed    uint64    `json:"diskUsed"`
	DiskTotal   uint64    `json:"diskTotal"`
	DiskPercent float64   `json:"diskPercent"`
	Processes   []Process `json:"processes"`
}

type Process struct {
	PID        int     `json:"pid"`
	Name       string  `json:"name"`
	User       string  `json:"user"`
	State      string  `json:"state"`
	CPU        float64 `json:"cpu"`
	Memory     uint64  `json:"memory"`
	MemPercent float64 `json:"memPercent"`
}

type Summary struct {
	CPU        float64 `json:"cpu"`
	MemUsed    uint64  `json:"memUsed"`
	MemTotal   uint64  `json:"memTotal"`
	MemPercent float64 `json:"memPercent"`
}

type memInfo struct {
	total     uint64
	available uint64
	swapTotal uint64
	swapFree  uint64
}

type procSample struct {
	name  string
	state string
	uid   string
	ticks uint64
	rss   uint64
}

func Read(sortBy string) (Usage, error) {
	var usage Usage
	idle1, total1, err := readCPU()
	if err != nil {
		return usage, err
	}
	procs1 := readProcesses()
	time.Sleep(500 * time.Millisecond)
	idle2, total2, err := readCPU()
	if err != nil {
		return usage, err
	}
	procs2 := readProcesses()

	mem, err := memory()
	if err != nil {
		return usage, err
	}
	diskUsed, diskTotal, diskPercent, err := disk("/")
	if err != nil {
		return usage, err
	}

	var totalDelta float64
	if total2 > total1 {
		totalDelta = float64(total2 - total1)
		usage.CPU = round((1 - float64(idle2-idle1)/totalDelta) * 100)
	}
	if mem.available > mem.total {
		mem.available = mem.total
	}
	usage.MemTotal = mem.total
	usage.MemUsed = mem.total - mem.available
	usage.MemPercent = percent(usage.MemUsed, usage.MemTotal)
	if mem.swapFree > mem.swapTotal {
		mem.swapFree = mem.swapTotal
	}
	usage.SwapTotal = mem.swapTotal
	usage.SwapUsed = mem.swapTotal - mem.swapFree
	usage.SwapPercent = percent(usage.SwapUsed, usage.SwapTotal)
	usage.DiskUsed = diskUsed
	usage.DiskTotal = diskTotal
	usage.DiskPercent = round(diskPercent)
	usage.Processes = topProcesses(procs1, procs2, totalDelta, mem.total, sortBy)
	return usage, nil
}

func ReadSummary() (Summary, error) {
	var summary Summary
	idle1, total1, err := readCPU()
	if err != nil {
		return summary, err
	}
	time.Sleep(500 * time.Millisecond)
	idle2, total2, err := readCPU()
	if err != nil {
		return summary, err
	}
	mem, err := memory()
	if err != nil {
		return summary, err
	}
	if total2 > total1 {
		summary.CPU = round((1 - float64(idle2-idle1)/float64(total2-total1)) * 100)
	}
	if mem.available > mem.total {
		mem.available = mem.total
	}
	summary.MemTotal = mem.total
	summary.MemUsed = mem.total - mem.available
	summary.MemPercent = percent(summary.MemUsed, summary.MemTotal)
	return summary, nil
}

func round(v float64) float64 {
	return math.Round(v*10) / 10
}

func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return round(float64(used) / float64(total) * 100)
}

func readCPU() (uint64, uint64, error) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var idle, total uint64
		for i, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return 0, 0, err
			}
			total += value
			if i == 3 || i == 4 {
				idle += value
			}
		}
		return idle, total, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	return 0, 0, errors.New("cpu line not found in /proc/stat")
}

func memory() (memInfo, error) {
	var info memInfo
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return info, err
	}
	defer file.Close()
	var hasTotal, hasAvailable bool
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		value *= 1024
		switch fields[0] {
		case "MemTotal:":
			info.total = value
			hasTotal = true
		case "MemAvailable:":
			info.available = value
			hasAvailable = true
		case "SwapTotal:":
			info.swapTotal = value
		case "SwapFree:":
			info.swapFree = value
		}
	}
	if err := scanner.Err(); err != nil {
		return info, err
	}
	if !hasTotal || !hasAvailable {
		return info, errors.New("memory info not found in /proc/meminfo")
	}
	return info, nil
}

func disk(path string) (uint64, uint64, float64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, 0, err
	}
	size := uint64(stat.Bsize)
	total := stat.Blocks * size
	used := (stat.Blocks - stat.Bfree) * size
	avail := stat.Bavail * size
	var percent float64
	if used+avail > 0 {
		percent = float64(used) / float64(used+avail) * 100
	}
	return used, total, percent, nil
}

func readProcesses() map[int]procSample {
	samples := map[int]procSample{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return samples
	}
	pageSize := uint64(os.Getpagesize())
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() {
			continue
		}
		sample, ok := readProcess(pid, pageSize)
		if ok {
			samples[pid] = sample
		}
	}
	return samples
}

func readProcess(pid int, pageSize uint64) (procSample, bool) {
	var sample procSample
	base := "/proc/" + strconv.Itoa(pid)
	data, err := os.ReadFile(base + "/stat")
	if err != nil {
		return sample, false
	}
	text := string(data)
	open := strings.IndexByte(text, '(')
	end := strings.LastIndexByte(text, ')')
	if open < 0 || end < open {
		return sample, false
	}
	sample.name = text[open+1 : end]
	fields := strings.Fields(text[end+1:])
	if len(fields) < 22 {
		return sample, false
	}
	sample.state = fields[0]
	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	rss, _ := strconv.ParseUint(fields[21], 10, 64)
	sample.ticks = utime + stime
	sample.rss = rss * pageSize
	sample.uid = readUID(base + "/status")
	return sample, true
}

func readUID(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) > 1 {
				return fields[1]
			}
			return ""
		}
	}
	return ""
}

var (
	userNames   = map[string]string{}
	userNamesMu sync.Mutex
)

func userName(uid string) string {
	if uid == "" {
		return ""
	}
	userNamesMu.Lock()
	defer userNamesMu.Unlock()
	if name, ok := userNames[uid]; ok {
		return name
	}
	name := uid
	if u, err := user.LookupId(uid); err == nil {
		name = u.Username
	}
	userNames[uid] = name
	return name
}

func topProcesses(before, after map[int]procSample, totalDelta float64, memTotal uint64, sortBy string) []Process {
	list := make([]Process, 0, len(after))
	for pid, sample := range after {
		process := Process{
			PID:        pid,
			Name:       sample.name,
			State:      sample.state,
			Memory:     sample.rss,
			MemPercent: percent(sample.rss, memTotal),
		}
		if prev, ok := before[pid]; ok && totalDelta > 0 && sample.ticks >= prev.ticks {
			process.CPU = round(float64(sample.ticks-prev.ticks) / totalDelta * 100)
		}
		list = append(list, process)
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if sortBy == "memory" {
			if a.Memory != b.Memory {
				return a.Memory > b.Memory
			}
			return a.CPU > b.CPU
		}
		if a.CPU != b.CPU {
			return a.CPU > b.CPU
		}
		return a.Memory > b.Memory
	})
	if len(list) > processLimit {
		list = list[:processLimit]
	}
	for i := range list {
		list[i].User = userName(after[list[i].PID].uid)
	}
	return list
}
