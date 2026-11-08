package backups

import (
	"database/sql"
	"fmt"
	"path"
	"strings"
	"time"
)

type Backup struct {
	ID        int64
	Name      string
	Path      string
	Frequency string
	Source    string

	SourceFolder string
	LimitEnabled bool
	LimitCount   int

	S3AccessKey    string
	S3Secret       string
	S3Region       string
	S3Bucket       string
	S3StorageClass string

	LocalPath string

	PreRunEnabled bool
	PreRunScript  string

	CreatedAt time.Time
	UpdatedAt time.Time

	LastRunAt time.Time
}

type Option struct {
	Value string
	Label string
}

const FrequencyManual = "manual"

var Frequencies = []Option{
	{Value: "daily", Label: "Daily"},
	{Value: "weekly", Label: "Weekly"},
	{Value: "monthly", Label: "Monthly"},
	{Value: FrequencyManual, Label: "Manually"},
}

const (
	SourceAWSS3 = "aws_s3"
	SourceLocal = "local"
)

var Sources = []Option{
	{Value: SourceAWSS3, Label: "AWS S3"},
	{Value: SourceLocal, Label: "Local"},
}

var StorageClasses = []Option{
	{Value: "GLACIER", Label: "Glacier"},
	{Value: "STANDARD", Label: "Standard"},
}

func (b Backup) FrequencyLabel() string    { return label(Frequencies, b.Frequency) }
func (b Backup) SourceLabel() string       { return label(Sources, b.Source) }
func (b Backup) StorageClassLabel() string { return label(StorageClasses, b.S3StorageClass) }
func (b Backup) IsAWSS3() bool             { return b.Source == SourceAWSS3 }
func (b Backup) IsLocal() bool             { return b.Source == SourceLocal }
func (b Backup) IsManual() bool            { return b.Frequency == FrequencyManual }

func (b Backup) LastRunLabel() string {
	if b.LastRunAt.IsZero() {
		return "Never"
	}
	return timeAgo(b.LastRunAt, time.Now())
}

func timeAgo(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return ago(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return ago(int(d/time.Hour), "hour")
	case d < 30*24*time.Hour:
		return ago(int(d/(24*time.Hour)), "day")
	case d < 365*24*time.Hour:
		return ago(int(d/(30*24*time.Hour)), "month")
	default:
		return ago(int(d/(365*24*time.Hour)), "year")
	}
}

func ago(n int, unit string) string {
	if n != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s ago", n, unit)
}

func label(options []Option, value string) string {
	for _, o := range options {
		if o.Value == value {
			return o.Label
		}
	}
	return value
}

func (b Backup) IsDue(lastSuccess time.Time, hasSuccess bool, now time.Time) bool {
	if b.IsManual() {
		return false
	}
	if !hasSuccess {
		return true
	}
	last, today := day(lastSuccess), day(now)
	switch b.Frequency {
	case "weekly":
		return !today.Before(last.AddDate(0, 0, 7))
	case "monthly":
		return !today.Before(last.AddDate(0, 1, 0))
	default:
		return true
	}
}

func day(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

func (b *Backup) Normalize() {
	b.Name = strings.TrimSpace(b.Name)
	b.Path = strings.TrimSpace(b.Path)
	b.Frequency = strings.TrimSpace(b.Frequency)
	b.Source = strings.TrimSpace(b.Source)
	b.SourceFolder = strings.Trim(strings.TrimSpace(b.SourceFolder), "/")
	b.S3AccessKey = strings.TrimSpace(b.S3AccessKey)
	b.S3Secret = strings.TrimSpace(b.S3Secret)
	b.S3Region = strings.TrimSpace(b.S3Region)
	b.S3Bucket = strings.TrimSpace(b.S3Bucket)
	b.S3StorageClass = strings.TrimSpace(b.S3StorageClass)
	b.LocalPath = strings.TrimSpace(b.LocalPath)
	if b.LocalPath != "" {
		b.LocalPath = path.Clean(b.LocalPath)
	}
	b.PreRunScript = strings.TrimSpace(strings.ReplaceAll(b.PreRunScript, "\r\n", "\n"))
}

func (b Backup) Validate() map[string]string {
	errs := map[string]string{}
	if b.Name == "" {
		errs["Name"] = "Name is required."
	}
	if b.Path == "" {
		errs["Path"] = "Path is required."
	}
	if !valid(Frequencies, b.Frequency) {
		errs["Frequency"] = "Select a valid frequency."
	}
	if !valid(Sources, b.Source) {
		errs["Source"] = "Select a valid source."
	}
	if b.LimitEnabled && b.LimitCount < 1 {
		errs["LimitCount"] = "Limit count must be at least 1."
	}
	if b.IsAWSS3() {
		if b.S3AccessKey == "" {
			errs["S3AccessKey"] = "AWS access key is required."
		}
		if b.S3Secret == "" {
			errs["S3Secret"] = "AWS secret is required."
		}
		if b.S3Region == "" {
			errs["S3Region"] = "S3 region is required."
		}
		if b.S3Bucket == "" {
			errs["S3Bucket"] = "S3 bucket name is required."
		}
		if !valid(StorageClasses, b.S3StorageClass) {
			errs["S3StorageClass"] = "Select a valid storage class."
		}
	}
	if b.IsLocal() {
		if b.LocalPath == "" {
			errs["LocalPath"] = "Local path is required."
		} else if !strings.HasPrefix(b.LocalPath, "/") {
			errs["LocalPath"] = "Local path must be an absolute path."
		}
	}
	if b.PreRunEnabled && b.PreRunScript == "" {
		errs["PreRunScript"] = "Pre run script is required."
	}
	return errs
}

func valid(options []Option, value string) bool {
	for _, o := range options {
		if o.Value == value {
			return true
		}
	}
	return false
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

const columns = "id, name, path, frequency, source, source_folder, limit_enabled, limit_count, s3_access_key, s3_secret, s3_region, s3_bucket, s3_storage_class, local_path, pre_run_enabled, pre_run_script, created_at, updated_at"

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Backup, error) {
	var b Backup
	err := row.Scan(
		&b.ID, &b.Name, &b.Path, &b.Frequency, &b.Source,
		&b.SourceFolder, &b.LimitEnabled, &b.LimitCount,
		&b.S3AccessKey, &b.S3Secret, &b.S3Region, &b.S3Bucket, &b.S3StorageClass,
		&b.LocalPath,
		&b.PreRunEnabled, &b.PreRunScript,
		&b.CreatedAt, &b.UpdatedAt,
	)
	return b, err
}

func (s *Store) List() ([]Backup, error) {
	rows, err := s.db.Query("SELECT " + columns + " FROM backups ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Backup
	for rows.Next() {
		b, err := scan(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, b)
	}
	return list, rows.Err()
}

func (s *Store) Get(id int64) (Backup, error) {
	return scan(s.db.QueryRow("SELECT "+columns+" FROM backups WHERE id = ?", id))
}

func (s *Store) Create(b *Backup) error {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO backups (name, path, frequency, source, source_folder, limit_enabled, limit_count, s3_access_key, s3_secret, s3_region, s3_bucket, s3_storage_class, local_path, pre_run_enabled, pre_run_script, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.Name, b.Path, b.Frequency, b.Source,
		b.SourceFolder, b.LimitEnabled, b.LimitCount,
		b.S3AccessKey, b.S3Secret, b.S3Region, b.S3Bucket, b.S3StorageClass,
		b.LocalPath,
		b.PreRunEnabled, b.PreRunScript,
		now, now,
	)
	if err != nil {
		return err
	}
	b.ID, err = res.LastInsertId()
	b.CreatedAt, b.UpdatedAt = now, now
	return err
}

func (s *Store) Update(b *Backup) error {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`UPDATE backups SET name = ?, path = ?, frequency = ?, source = ?,
		source_folder = ?, limit_enabled = ?, limit_count = ?,
		s3_access_key = ?, s3_secret = ?, s3_region = ?, s3_bucket = ?, s3_storage_class = ?,
		local_path = ?,
		pre_run_enabled = ?, pre_run_script = ?,
		updated_at = ? WHERE id = ?`,
		b.Name, b.Path, b.Frequency, b.Source,
		b.SourceFolder, b.LimitEnabled, b.LimitCount,
		b.S3AccessKey, b.S3Secret, b.S3Region, b.S3Bucket, b.S3StorageClass,
		b.LocalPath,
		b.PreRunEnabled, b.PreRunScript,
		now, b.ID,
	)
	if err != nil {
		return err
	}
	b.UpdatedAt = now
	return affected(res)
}

func (s *Store) Delete(id int64) error {
	if _, err := s.db.Exec("DELETE FROM backup_runs WHERE backup_id = ?", id); err != nil {
		return err
	}
	res, err := s.db.Exec("DELETE FROM backups WHERE id = ?", id)
	if err != nil {
		return err
	}
	return affected(res)
}

func affected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
