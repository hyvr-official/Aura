package repositories

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"
)

const (
	WebhookQueued  = "queued"
	WebhookRunning = "running"
	WebhookSuccess = "success"
	WebhookFailed  = "failed"
	WebhookSkipped = "skipped"
)

const webhookLogLimit = 10

type WebhookLog struct {
	ID           int64
	RepositoryID int64
	Event        string
	Ref          string
	Status       string
	Log          string
	StartedAt    time.Time
	FinishedAt   sql.NullTime
}

func (l WebhookLog) StatusBadge() string {
	switch l.Status {
	case WebhookSuccess:
		return "text-bg-success"
	case WebhookFailed:
		return "text-bg-danger"
	case WebhookSkipped:
		return "text-bg-secondary"
	case WebhookQueued:
		return "text-bg-info"
	default:
		return "text-bg-warning"
	}
}

func (l WebhookLog) Duration() string {
	if !l.FinishedAt.Valid {
		return "—"
	}
	return l.FinishedAt.Time.Sub(l.StartedAt).Round(time.Second).String()
}

func NewWebhookToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

const webhookLogColumns = "id, repository_id, event, ref, status, log, started_at, finished_at"

func scanWebhookLog(row scanner) (WebhookLog, error) {
	var l WebhookLog
	err := row.Scan(&l.ID, &l.RepositoryID, &l.Event, &l.Ref, &l.Status, &l.Log, &l.StartedAt, &l.FinishedAt)
	return l, err
}

func (s *Store) StartWebhookLog(repositoryID int64, event, ref, status string) (WebhookLog, error) {
	l := WebhookLog{RepositoryID: repositoryID, Event: event, Ref: ref, Status: status, StartedAt: time.Now().UTC()}
	res, err := s.db.Exec(
		"INSERT INTO repository_webhook_logs (repository_id, event, ref, status, started_at) VALUES (?, ?, ?, ?, ?)",
		l.RepositoryID, l.Event, l.Ref, l.Status, l.StartedAt,
	)
	if err != nil {
		return l, err
	}
	l.ID, err = res.LastInsertId()
	if err != nil {
		return l, err
	}
	_, err = s.db.Exec(
		`DELETE FROM repository_webhook_logs WHERE repository_id = ? AND status NOT IN (?, ?) AND id NOT IN (
			SELECT id FROM repository_webhook_logs WHERE repository_id = ? ORDER BY id DESC LIMIT ?
		)`,
		repositoryID, WebhookRunning, WebhookQueued, repositoryID, webhookLogLimit,
	)
	return l, err
}

func (s *Store) MarkWebhookLogRunning(l *WebhookLog) error {
	l.Status = WebhookRunning
	res, err := s.db.Exec("UPDATE repository_webhook_logs SET status = ? WHERE id = ?", l.Status, l.ID)
	if err != nil {
		return err
	}
	return affected(res)
}

func (s *Store) FailInterruptedWebhookLogs() error {
	_, err := s.db.Exec(
		"UPDATE repository_webhook_logs SET status = ?, log = log || ?, finished_at = ? WHERE status IN (?, ?)",
		WebhookFailed, "Interrupted: Aura was restarted before this delivery finished.\n", time.Now().UTC(), WebhookRunning, WebhookQueued,
	)
	return err
}

func (s *Store) FinishWebhookLog(l *WebhookLog) error {
	l.FinishedAt = sql.NullTime{Time: time.Now().UTC(), Valid: true}
	res, err := s.db.Exec(
		"UPDATE repository_webhook_logs SET status = ?, log = ?, finished_at = ? WHERE id = ?",
		l.Status, l.Log, l.FinishedAt.Time, l.ID,
	)
	if err != nil {
		return err
	}
	return affected(res)
}

func (s *Store) ListWebhookLogs(repositoryID int64) ([]WebhookLog, error) {
	rows, err := s.db.Query(
		"SELECT "+webhookLogColumns+" FROM repository_webhook_logs WHERE repository_id = ? ORDER BY id DESC LIMIT ?",
		repositoryID, webhookLogLimit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []WebhookLog
	for rows.Next() {
		l, err := scanWebhookLog(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, l)
	}
	return list, rows.Err()
}

func (s *Store) DeleteWebhookLogs(repositoryID int64) error {
	_, err := s.db.Exec("DELETE FROM repository_webhook_logs WHERE repository_id = ?", repositoryID)
	return err
}
