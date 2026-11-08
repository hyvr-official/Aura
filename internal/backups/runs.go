package backups

import (
	"database/sql"
	"fmt"
	"path"
	"time"
)

const (
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusFailed  = "failed"

	TriggerCron   = "cron"
	TriggerManual = "manual"
)

const runRetention = 30 * 24 * time.Hour

const staleAfter = 24 * time.Hour

type Run struct {
	ID         int64
	BackupID   int64
	Trigger    string
	Status     string
	FilePath   string
	FileSize   int64
	Log        string
	StartedAt  time.Time
	FinishedAt sql.NullTime
}

func (r Run) StatusBadge() string {
	switch r.Status {
	case StatusSuccess:
		return "text-bg-success"
	case StatusFailed:
		return "text-bg-danger"
	default:
		return "text-bg-warning"
	}
}

func (r Run) Duration() string {
	if !r.FinishedAt.Valid {
		return "—"
	}
	return r.FinishedAt.Time.Sub(r.StartedAt).Round(time.Second).String()
}

func (r Run) SizeLabel() string {
	if r.FileSize <= 0 {
		return "—"
	}
	const unit = 1024
	if r.FileSize < unit {
		return fmt.Sprintf("%d B", r.FileSize)
	}
	div, exp := int64(unit), 0
	for n := r.FileSize / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(r.FileSize)/float64(div), "KMGTPE"[exp])
}

func (r Run) FileName() string {
	if r.FilePath == "" {
		return ""
	}
	return path.Base(r.FilePath)
}

const runColumns = "id, backup_id, trigger, status, file_path, file_size, log, started_at, finished_at"

func scanRun(row scanner) (Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.BackupID, &r.Trigger, &r.Status, &r.FilePath, &r.FileSize, &r.Log, &r.StartedAt, &r.FinishedAt)
	return r, err
}

func (s *Store) StartRun(backupID int64, trigger string) (Run, error) {
	r := Run{BackupID: backupID, Trigger: trigger, Status: StatusRunning, StartedAt: time.Now().UTC()}
	res, err := s.db.Exec(
		"INSERT INTO backup_runs (backup_id, trigger, status, started_at) VALUES (?, ?, ?, ?)",
		r.BackupID, r.Trigger, r.Status, r.StartedAt,
	)
	if err != nil {
		return r, err
	}
	r.ID, err = res.LastInsertId()
	return r, err
}

func (s *Store) FinishRun(r *Run) error {
	r.FinishedAt = sql.NullTime{Time: time.Now().UTC(), Valid: true}
	res, err := s.db.Exec(
		"UPDATE backup_runs SET status = ?, file_path = ?, file_size = ?, log = ?, finished_at = ? WHERE id = ?",
		r.Status, r.FilePath, r.FileSize, r.Log, r.FinishedAt.Time, r.ID,
	)
	if err != nil {
		return err
	}
	return affected(res)
}

func (s *Store) ListRuns(backupID int64, limit int) ([]Run, error) {
	rows, err := s.db.Query("SELECT "+runColumns+" FROM backup_runs WHERE backup_id = ? ORDER BY id DESC LIMIT ?", backupID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, rows.Err()
}

func (s *Store) PruneRuns() (int64, error) {
	res, err := s.db.Exec(
		`DELETE FROM backup_runs
		WHERE status != ? AND started_at < ?
		AND id NOT IN (SELECT MAX(id) FROM backup_runs WHERE status = ? GROUP BY backup_id)`,
		StatusRunning, time.Now().UTC().Add(-runRetention), StatusSuccess,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) LastSuccess(backupID int64) (t time.Time, ok bool, err error) {
	r, err := scanRun(s.db.QueryRow(
		"SELECT "+runColumns+" FROM backup_runs WHERE backup_id = ? AND status = ? ORDER BY id DESC LIMIT 1",
		backupID, StatusSuccess,
	))
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return r.StartedAt, true, nil
}

func (s *Store) LastRuns() (map[int64]time.Time, error) {
	rows, err := s.db.Query("SELECT backup_id, started_at FROM backup_runs WHERE id IN (SELECT MAX(id) FROM backup_runs GROUP BY backup_id)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	last := map[int64]time.Time{}
	for rows.Next() {
		var id int64
		var t time.Time
		if err := rows.Scan(&id, &t); err != nil {
			return nil, err
		}
		last[id] = t
	}
	return last, rows.Err()
}

func (s *Store) HasRunning(backupID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM backup_runs WHERE backup_id = ? AND status = ? AND started_at > ?",
		backupID, StatusRunning, time.Now().UTC().Add(-staleAfter),
	).Scan(&n)
	return n > 0, err
}
