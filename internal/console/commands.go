package console

import (
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxNameLength    = 100
	MaxCommandLength = 65536
)

type Command struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Command   string    `json:"command"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (c *Command) Normalize() {
	c.Name = strings.TrimSpace(c.Name)
	c.Command = strings.ReplaceAll(c.Command, "\r\n", "\n")
	c.Command = strings.ReplaceAll(c.Command, "\r", "\n")
	c.Command = strings.Trim(c.Command, "\n")
}

func (c Command) Validate() map[string]string {
	errs := map[string]string{}
	if utf8.RuneCountInString(c.Name) > MaxNameLength {
		errs["name"] = "Name must be 100 characters or less."
	}
	if strings.TrimSpace(c.Command) == "" {
		errs["command"] = "Command is required."
	} else if len(c.Command) > MaxCommandLength {
		errs["command"] = "Command is too long."
	}
	return errs
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

const columns = "id, name, command, created_at, updated_at"

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Command, error) {
	var c Command
	err := row.Scan(&c.ID, &c.Name, &c.Command, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (s *Store) List() ([]Command, error) {
	rows, err := s.db.Query("SELECT " + columns + " FROM console_commands ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []Command{}
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

func (s *Store) Get(id int64) (Command, error) {
	return scan(s.db.QueryRow("SELECT "+columns+" FROM console_commands WHERE id = ?", id))
}

func (s *Store) Create(c *Command) error {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		"INSERT INTO console_commands (name, command, created_at, updated_at) VALUES (?, ?, ?, ?)",
		c.Name, c.Command, now, now,
	)
	if err != nil {
		return err
	}
	c.ID, err = res.LastInsertId()
	c.CreatedAt, c.UpdatedAt = now, now
	return err
}

func (s *Store) Update(c *Command) error {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		"UPDATE console_commands SET name = ?, command = ?, updated_at = ? WHERE id = ?",
		c.Name, c.Command, now, c.ID,
	)
	if err != nil {
		return err
	}
	c.UpdatedAt = now
	return affected(res)
}

func (s *Store) Delete(id int64) error {
	res, err := s.db.Exec("DELETE FROM console_commands WHERE id = ?", id)
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
