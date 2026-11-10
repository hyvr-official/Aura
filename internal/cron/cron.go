package cron

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	LogDir           = "/var/log/aura/cron"
	SpoolDir         = "/var/spool/cron/crontabs"
	SystemCrontab    = "/etc/crontab"
	SystemCronDir    = "/etc/cron.d"
	SourceCrontab    = "crontab"
	MaxCommandLength = 1000
	MaxCrontabLength = 256 * 1024
	marker           = "# aura-cron:"
	backupMarker     = "# aura-backup"
)

var (
	ErrCronMissing = errors.New("cron is not installed")
	ErrNotFound    = errors.New("cron job not found")
	ErrReadOnly    = errors.New("cron job is read-only")
	ErrUnknownUser = errors.New("user does not exist on this server")
)

var (
	userPattern      = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*\$?$`)
	cronDFilePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	envPattern       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*=`)
	managedPattern   = regexp.MustCompile(`^\( (.*) \) >> '/var/log/aura/cron/([a-f0-9]{16})\.log' 2>&1 # aura-cron:([a-f0-9]{16})$`)
	percentPattern   = regexp.MustCompile(`(^|[^\\])%`)
)

var mu sync.Mutex

var keywords = map[string]string{
	"@reboot":   "At system startup",
	"@yearly":   "Every year",
	"@annually": "Every year",
	"@monthly":  "Every month",
	"@weekly":   "Every week",
	"@daily":    "Every day",
	"@midnight": "Every day",
	"@hourly":   "Every hour",
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

var dayNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

var dayLabels = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

type fieldSpec struct {
	name  string
	min   int
	max   int
	names map[string]int
}

var fieldSpecs = []fieldSpec{
	{"minute", 0, 59, nil},
	{"hour", 0, 23, nil},
	{"day of month", 1, 31, nil},
	{"month", 1, 12, monthNames},
	{"day of week", 0, 7, dayNames},
}

type Job struct {
	ID        string
	User      string
	Source    string
	Schedule  string
	Command   string
	Raw       string
	LogID     string
	Protected bool
	line      int
}

func (j Job) System() bool {
	return j.Source != SourceCrontab
}

func (j Job) Editable() bool {
	return !j.System() && !j.Protected
}

func (j Job) HasOwnLog() bool {
	return j.LogID != ""
}

func (j Job) LogPath() string {
	if j.LogID == "" {
		return ""
	}
	return logPath(j.LogID)
}

func (j Job) SourceLabel() string {
	if j.System() {
		return j.Source
	}
	return "crontab"
}

func (j Job) ScheduleLabel() string {
	return Describe(j.Schedule)
}

type Input struct {
	User     string
	Schedule string
	Command  string
}

func (in *Input) Normalize() {
	in.User = strings.TrimSpace(in.User)
	in.Schedule = strings.Join(strings.Fields(in.Schedule), " ")
	in.Command = strings.TrimSpace(in.Command)
}

func (in Input) Validate() map[string]string {
	errs := map[string]string{}
	if in.User == "" {
		errs["user"] = "User is required."
	} else if !userExists(in.User) {
		errs["user"] = "User does not exist on this server."
	}
	if msg := scheduleError(in.Schedule); msg != "" {
		errs["schedule"] = msg
	}
	switch {
	case in.Command == "":
		errs["command"] = "Command is required."
	case strings.ContainsAny(in.Command, "\r\n"):
		errs["command"] = "Command must be a single line."
	case len(in.Command) > MaxCommandLength:
		errs["command"] = fmt.Sprintf("Command must be at most %d characters.", MaxCommandLength)
	case percentPattern.MatchString(in.Command):
		errs["command"] = `Escape % as \% in cron commands.`
	}
	return errs
}

func Describe(schedule string) string {
	if label, ok := keywords[schedule]; ok {
		return label
	}
	f := strings.Fields(schedule)
	if len(f) != 5 {
		return "Custom"
	}
	minute, hour, dom, month, dow := f[0], f[1], f[2], f[3], f[4]
	restAny := dom == "*" && month == "*" && dow == "*"
	switch {
	case minute == "*" && hour == "*" && restAny:
		return "Every minute"
	case strings.HasPrefix(minute, "*/") && isNumber(minute[2:]) && hour == "*" && restAny:
		return "Every " + minute[2:] + " minutes"
	case isNumber(minute) && hour == "*" && restAny:
		return "Every hour at minute " + minute
	case isNumber(minute) && strings.HasPrefix(hour, "*/") && isNumber(hour[2:]) && restAny:
		return "Every " + hour[2:] + " hours at minute " + minute
	case isNumber(minute) && isNumber(hour) && restAny:
		return "Every day at " + clock(hour, minute)
	case isNumber(minute) && isNumber(hour) && dom == "*" && month == "*" && isNumber(dow):
		d, _ := strconv.Atoi(dow)
		if d >= 0 && d < len(dayLabels) {
			return "Every " + dayLabels[d] + " at " + clock(hour, minute)
		}
	case isNumber(minute) && isNumber(hour) && isNumber(dom) && month == "*" && dow == "*":
		return "Day " + dom + " of every month at " + clock(hour, minute)
	}
	return "Custom"
}

func List() ([]Job, error) {
	if _, err := exec.LookPath("crontab"); err != nil {
		return nil, ErrCronMissing
	}
	users, err := crontabUsers()
	if err != nil {
		return nil, err
	}
	var jobs []Job
	for _, u := range users {
		content, err := readCrontab(u)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, parseCrontab(content, SourceCrontab, u)...)
	}
	jobs = append(jobs, systemJobs()...)
	sort.SliceStable(jobs, func(i, j int) bool {
		a, b := jobs[i], jobs[j]
		if a.System() != b.System() {
			return !a.System()
		}
		if a.System() {
			return a.Source < b.Source
		}
		return a.User < b.User
	})
	return jobs, nil
}

func Get(id string) (Job, error) {
	jobs, err := List()
	if err != nil {
		return Job{}, err
	}
	for _, j := range jobs {
		if j.ID == id {
			return j, nil
		}
	}
	return Job{}, ErrNotFound
}

func Create(in Input) error {
	mu.Lock()
	defer mu.Unlock()
	if _, err := exec.LookPath("crontab"); err != nil {
		return ErrCronMissing
	}
	lid, err := newLogID()
	if err != nil {
		return err
	}
	if err := ensureLog(lid, in.User); err != nil {
		return err
	}
	content, err := readCrontab(in.User)
	if err != nil {
		return err
	}
	if err := writeCrontab(in.User, appendLine(content, managedLine(in, lid))); err != nil {
		removeLog(lid)
		return err
	}
	return nil
}

func Update(id string, in Input) error {
	mu.Lock()
	defer mu.Unlock()
	job, err := Get(id)
	if err != nil {
		return err
	}
	if !job.Editable() {
		return ErrReadOnly
	}
	lid := job.LogID
	if lid == "" {
		if lid, err = newLogID(); err != nil {
			return err
		}
	}
	if err := ensureLog(lid, in.User); err != nil {
		return err
	}
	line := managedLine(in, lid)
	content, err := readCrontab(job.User)
	if err != nil {
		return err
	}
	if job.User == in.User {
		updated, ok := replaceLine(content, job, &line)
		if !ok {
			return ErrNotFound
		}
		return writeCrontab(job.User, updated)
	}
	stripped, ok := replaceLine(content, job, nil)
	if !ok {
		return ErrNotFound
	}
	target, err := readCrontab(in.User)
	if err != nil {
		return err
	}
	if err := writeCrontab(in.User, appendLine(target, line)); err != nil {
		return err
	}
	return writeCrontab(job.User, stripped)
}

func Delete(id string) error {
	mu.Lock()
	defer mu.Unlock()
	job, err := Get(id)
	if err != nil {
		return err
	}
	if !job.Editable() {
		return ErrReadOnly
	}
	content, err := readCrontab(job.User)
	if err != nil {
		return err
	}
	updated, ok := replaceLine(content, job, nil)
	if !ok {
		return ErrNotFound
	}
	if err := writeCrontab(job.User, updated); err != nil {
		return err
	}
	if job.LogID != "" {
		removeLog(job.LogID)
	}
	return nil
}

func ReadCrontab(owner string) (string, error) {
	if _, err := exec.LookPath("crontab"); err != nil {
		return "", ErrCronMissing
	}
	if !userExists(owner) {
		return "", ErrUnknownUser
	}
	return readCrontab(owner)
}

func SaveCrontab(owner, content string) error {
	mu.Lock()
	defer mu.Unlock()
	if _, err := exec.LookPath("crontab"); err != nil {
		return ErrCronMissing
	}
	if !userExists(owner) {
		return ErrUnknownUser
	}
	if len(content) > MaxCrontabLength {
		return fmt.Errorf("crontab must be at most %d bytes", MaxCrontabLength)
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.TrimRight(content, "\n")
	if content != "" {
		content += "\n"
	}
	for _, j := range parseCrontab(content, SourceCrontab, owner) {
		if j.LogID != "" {
			if err := ensureLog(j.LogID, owner); err != nil {
				return err
			}
		}
	}
	return writeCrontab(owner, content)
}

func Logs(j Job, lines int) *exec.Cmd {
	n := strconv.Itoa(lines)
	if j.LogID != "" {
		return exec.Command("tail", "-n", n, "-F", j.LogPath())
	}
	command := j.Command
	if len(command) > 100 {
		command = command[:100]
	}
	return exec.Command("journalctl", "-u", "cron", "-n", n, "-f", "--no-pager", "-o", "short-iso", "--grep", regexp.QuoteMeta(command))
}

func Users() []string {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return []string{"root"}
	}
	defer f.Close()
	var list []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), ":")
		if len(parts) < 7 || !userPattern.MatchString(parts[0]) {
			continue
		}
		uid, err := strconv.Atoi(parts[2])
		if err != nil {
			continue
		}
		shell := parts[6]
		if uid == 0 || (uid >= 1000 && uid != 65534 && !strings.HasSuffix(shell, "nologin") && !strings.HasSuffix(shell, "false")) {
			list = append(list, parts[0])
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if (list[i] == "root") != (list[j] == "root") {
			return list[i] == "root"
		}
		return list[i] < list[j]
	})
	if len(list) == 0 {
		list = []string{"root"}
	}
	return list
}

func parseCrontab(content, source, owner string) []Job {
	system := source != SourceCrontab
	var jobs []Job
	seen := map[string]int{}
	for i, raw := range strings.Split(content, "\n") {
		schedule, lineUser, command, ok := parseLine(raw, system)
		if !ok {
			continue
		}
		if !system {
			lineUser = owner
		}
		key := source + "\x00" + lineUser + "\x00" + raw
		seen[key]++
		sum := sha1.Sum([]byte(key + "\x00" + strconv.Itoa(seen[key])))
		j := Job{
			ID:        hex.EncodeToString(sum[:])[:12],
			User:      lineUser,
			Source:    source,
			Schedule:  schedule,
			Command:   command,
			Raw:       raw,
			Protected: strings.Contains(raw, backupMarker),
			line:      i,
		}
		if m := managedPattern.FindStringSubmatch(command); m != nil && m[2] == m[3] {
			j.Command = m[1]
			j.LogID = m[2]
		}
		jobs = append(jobs, j)
	}
	return jobs
}

func parseLine(raw string, system bool) (string, string, string, bool) {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") || envPattern.MatchString(line) {
		return "", "", "", false
	}
	n := 5
	if strings.HasPrefix(line, "@") {
		n = 1
	}
	if system {
		n++
	}
	fields, command, ok := splitFields(line, n)
	if !ok {
		return "", "", "", false
	}
	lineUser := ""
	if system {
		lineUser = fields[n-1]
		fields = fields[:n-1]
	}
	return strings.Join(fields, " "), lineUser, command, true
}

func splitFields(line string, n int) ([]string, string, bool) {
	rest := strings.TrimLeft(line, " \t")
	fields := make([]string, 0, n)
	for len(fields) < n {
		i := strings.IndexAny(rest, " \t")
		if i < 0 {
			return nil, "", false
		}
		fields = append(fields, rest[:i])
		rest = strings.TrimLeft(rest[i:], " \t")
	}
	return fields, rest, rest != ""
}

func systemJobs() []Job {
	var jobs []Job
	if data, err := os.ReadFile(SystemCrontab); err == nil {
		jobs = append(jobs, parseCrontab(string(data), SystemCrontab, "")...)
	}
	entries, err := os.ReadDir(SystemCronDir)
	if err != nil {
		return jobs
	}
	for _, e := range entries {
		if e.IsDir() || !cronDFilePattern.MatchString(e.Name()) {
			continue
		}
		p := filepath.Join(SystemCronDir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		jobs = append(jobs, parseCrontab(string(data), p, "")...)
	}
	return jobs
}

func crontabUsers() ([]string, error) {
	entries, err := os.ReadDir(SpoolDir)
	if errors.Is(err, os.ErrPermission) {
		if u, cerr := user.Current(); cerr == nil {
			return []string{u.Username}, nil
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var users []string
	for _, e := range entries {
		if e.Type().IsRegular() && userPattern.MatchString(e.Name()) {
			users = append(users, e.Name())
		}
	}
	return users, nil
}

func replaceLine(content string, job Job, replacement *string) (string, bool) {
	for _, j := range parseCrontab(content, SourceCrontab, job.User) {
		if j.ID != job.ID {
			continue
		}
		lines := strings.Split(content, "\n")
		if replacement != nil {
			lines[j.line] = *replacement
		} else {
			lines = append(lines[:j.line], lines[j.line+1:]...)
		}
		updated := strings.TrimRight(strings.Join(lines, "\n"), "\n")
		if updated == "" {
			return "", true
		}
		return updated + "\n", true
	}
	return "", false
}

func appendLine(content, line string) string {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return line + "\n"
	}
	return content + "\n" + line + "\n"
}

func managedLine(in Input, lid string) string {
	return fmt.Sprintf("%s ( %s ) >> %s 2>&1 %s%s", in.Schedule, in.Command, shellQuote(logPath(lid)), marker, lid)
}

func scheduleError(schedule string) string {
	if schedule == "" {
		return "Schedule is required."
	}
	if strings.HasPrefix(schedule, "@") {
		if _, ok := keywords[schedule]; ok {
			return ""
		}
		return "Unknown schedule keyword."
	}
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return "Schedule must have 5 fields: minute, hour, day of month, month and day of week."
	}
	for i, f := range fields {
		if !validField(strings.ToLower(f), fieldSpecs[i]) {
			return "Invalid " + fieldSpecs[i].name + " field: " + f
		}
	}
	return ""
}

func validField(field string, spec fieldSpec) bool {
	for _, part := range strings.Split(field, ",") {
		rng, step, hasStep := strings.Cut(part, "/")
		if hasStep {
			n, err := strconv.Atoi(step)
			if err != nil || n < 1 || n > spec.max {
				return false
			}
		}
		if rng == "*" {
			continue
		}
		lo, hi, isRange := strings.Cut(rng, "-")
		a, ok := fieldValue(lo, spec)
		if !ok {
			return false
		}
		if !isRange {
			if hasStep {
				return false
			}
			continue
		}
		b, ok := fieldValue(hi, spec)
		if !ok || b < a {
			return false
		}
	}
	return true
}

func fieldValue(s string, spec fieldSpec) (int, bool) {
	if v, ok := spec.names[s]; ok {
		return v, true
	}
	if !isNumber(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < spec.min || n > spec.max {
		return 0, false
	}
	return n, true
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func clock(hour, minute string) string {
	h, _ := strconv.Atoi(hour)
	m, _ := strconv.Atoi(minute)
	return fmt.Sprintf("%02d:%02d", h, m)
}

func userExists(name string) bool {
	if !userPattern.MatchString(name) {
		return false
	}
	_, err := user.Lookup(name)
	return err == nil
}

func newLogID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func logPath(lid string) string {
	return filepath.Join(LogDir, lid+".log")
}

func ensureLog(lid, owner string) error {
	if err := os.MkdirAll(LogDir, 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(logPath(lid), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	f.Close()
	u, err := user.Lookup(owner)
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if os.Geteuid() == 0 {
		return os.Chown(logPath(lid), uid, gid)
	}
	return nil
}

func removeLog(lid string) {
	os.Remove(logPath(lid))
}

func readCrontab(owner string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("crontab", "-l", "-u", owner)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "no crontab") {
			return "", nil
		}
		return "", fmt.Errorf("crontab -l -u %s: %v: %s", owner, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func writeCrontab(owner, content string) error {
	cmd := exec.Command("crontab", "-u", owner, "-")
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("crontab -u %s: %v: %s", owner, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
