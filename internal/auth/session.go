package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log"
	"time"
)

const (
	CookieName = "aura_session"
	SessionTTL = 12 * time.Hour
)

type SessionStore struct {
	db *sql.DB
}

func NewSessionStore(db *sql.DB) *SessionStore {
	return &SessionStore{db: db}
}

func (s *SessionStore) Create() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)

	now := time.Now()
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, now.Unix()); err != nil {
		log.Printf("sessions cleanup: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO sessions (token, expires_at) VALUES (?, ?)`, token, now.Add(SessionTTL).Unix()); err != nil {
		return "", err
	}

	return token, nil
}

func (s *SessionStore) Valid(token string) bool {
	if token == "" {
		return false
	}

	var expiresAt int64
	err := s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token = ?`, token).Scan(&expiresAt)
	if err != nil {
		if err != sql.ErrNoRows {
			log.Printf("sessions lookup: %v", err)
		}
		return false
	}
	if time.Now().Unix() >= expiresAt {
		s.Destroy(token)
		return false
	}
	return true
}

func (s *SessionStore) Destroy(token string) {
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token); err != nil {
		log.Printf("sessions destroy: %v", err)
	}
}
