package timezone

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	_ "time/tzdata"
)

const (
	settingKey      = "timezone"
	clockSettingKey = "time_format"
	zoneinfoDir     = "/usr/share/zoneinfo"
	Clock12         = "12"
	Clock24         = "24"
	DefaultClock    = Clock12
)

var (
	current     atomic.Pointer[time.Location]
	currentName atomic.Value
	clock       atomic.Value
	optionsOnce sync.Once
	options     []string
	regions     = []string{"Africa", "America", "Antarctica", "Asia", "Atlantic", "Australia", "Europe", "Indian", "Pacific"}
)

var ErrInvalid = errors.New("invalid timezone")

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Load() error {
	return errors.Join(s.loadZone(), s.loadClock())
}

func (s *Store) loadClock() error {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, clockSettingKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		clock.Store(DefaultClock)
		return nil
	}
	if err != nil {
		clock.Store(DefaultClock)
		return err
	}
	if !validClock(value) {
		value = DefaultClock
	}
	clock.Store(value)
	return nil
}

func (s *Store) SaveClock(value string) error {
	value = strings.TrimSpace(value)
	if !validClock(value) {
		return ErrInvalid
	}
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, clockSettingKey, value)
	if err != nil {
		return err
	}
	clock.Store(value)
	return nil
}

func validClock(value string) bool {
	return value == Clock12 || value == Clock24
}

func Clock() string {
	if value, ok := clock.Load().(string); ok {
		return value
	}
	return DefaultClock
}

func layoutFor(layout string) string {
	if Clock() != Clock12 {
		return layout
	}
	if strings.Contains(layout, "15:04:05") {
		return strings.Replace(layout, "15:04:05", "03:04:05 PM", 1)
	}
	return strings.Replace(layout, "15:04", "03:04 PM", 1)
}

func (s *Store) loadZone() error {
	var name string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, settingKey).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		apply("", time.Local)
		return nil
	}
	if err != nil {
		apply("", time.Local)
		return err
	}
	loc, err := resolve(name)
	if err != nil {
		apply("", time.Local)
		return err
	}
	apply(name, loc)
	return nil
}

func (s *Store) Save(name string) error {
	name = strings.TrimSpace(name)
	loc, err := resolve(name)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, settingKey, name)
	if err != nil {
		return err
	}
	apply(name, loc)
	return nil
}

func resolve(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	if name == "Local" || strings.Contains(name, "..") {
		return nil, ErrInvalid
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, ErrInvalid
	}
	return loc, nil
}

func apply(name string, loc *time.Location) {
	current.Store(loc)
	currentName.Store(name)
}

func Location() *time.Location {
	if loc := current.Load(); loc != nil {
		return loc
	}
	return time.Local
}

func Name() string {
	if name, ok := currentName.Load().(string); ok {
		return name
	}
	return ""
}

func ServerName() string {
	return time.Local.String()
}

func Now() time.Time {
	return time.Now().In(Location())
}

func In(t time.Time) time.Time {
	return t.In(Location())
}

func Format(t time.Time, layout string) string {
	if t.IsZero() {
		return ""
	}
	return t.In(Location()).Format(layoutFor(layout))
}

func Options() []string {
	optionsOnce.Do(func() {
		options = loadOptions()
	})
	return options
}

func loadOptions() []string {
	seen := map[string]bool{}
	var names []string
	for _, region := range regions {
		root := filepath.Join(zoneinfoDir, region)
		if _, err := os.Stat(root); err != nil {
			continue
		}
		filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(zoneinfoDir, path)
			if err != nil {
				return nil
			}
			name := filepath.ToSlash(rel)
			if seen[name] {
				return nil
			}
			if _, err := time.LoadLocation(name); err != nil {
				return nil
			}
			seen[name] = true
			names = append(names, name)
			return nil
		})
	}
	sort.Strings(names)
	return append([]string{"UTC"}, names...)
}
