package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"strconv"
	"strings"
)

const (
	passwordKey        = "password_hash"
	passwordScheme     = "pbkdf2-sha256"
	passwordIterations = 600000
	passwordSaltLen    = 16
	passwordKeyLen     = 32
)

type PasswordStore struct {
	db *sql.DB
}

func NewPasswordStore(db *sql.DB) *PasswordStore {
	return &PasswordStore{db: db}
}

func (p *PasswordStore) IsSet() bool {
	var one int
	err := p.db.QueryRow(`SELECT 1 FROM settings WHERE key = ?`, passwordKey).Scan(&one)
	if err != nil {
		if err != sql.ErrNoRows {
			log.Printf("password lookup: %v", err)
		}
		return false
	}
	return true
}

func (p *PasswordStore) SetInitial(password string) (bool, error) {
	encoded, err := hashPassword(password)
	if err != nil {
		return false, err
	}

	res, err := p.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`, passwordKey, encoded)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (p *PasswordStore) Verify(password string) bool {
	var encoded string
	err := p.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, passwordKey).Scan(&encoded)
	if err != nil {
		if err != sql.ErrNoRows {
			log.Printf("password lookup: %v", err)
		}
		return false
	}

	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordScheme {
		log.Printf("password verify: invalid stored hash format")
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		log.Printf("password verify: invalid iteration count")
		return false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil {
		log.Printf("password verify: invalid salt")
		return false
	}
	expected, err := hex.DecodeString(parts[3])
	if err != nil {
		log.Printf("password verify: invalid hash")
		return false
	}

	actual, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
	if err != nil {
		log.Printf("password verify: %v", err)
		return false
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, passwordKeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%s$%s", passwordScheme, passwordIterations, hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}
