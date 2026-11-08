package repositories

import (
	"database/sql"
	"path"
	"regexp"
	"strings"
	"time"
)

type Repository struct {
	ID         int64
	Name       string
	URL        string
	Path       string
	PublicKey  string
	PrivateKey string

	LastAction   string
	LastOutput   string
	LastActionAt time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

var (
	scpURLPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)
	sshURLPattern = regexp.MustCompile(`^ssh://[^\s]+$`)
	nonSlug       = regexp.MustCompile(`[^a-z0-9]+`)
)

func (r *Repository) Normalize() {
	r.Name = strings.TrimSpace(r.Name)
	r.URL = strings.TrimSpace(r.URL)
	r.Path = strings.TrimSpace(r.Path)
	if r.Path != "" {
		r.Path = path.Clean(r.Path)
	}
}

func (r Repository) Validate() map[string]string {
	errs := map[string]string{}
	if r.Name == "" {
		errs["Name"] = "Name is required."
	}
	if r.URL == "" {
		errs["URL"] = "Repository URL is required."
	} else if !scpURLPattern.MatchString(r.URL) && !sshURLPattern.MatchString(r.URL) {
		errs["URL"] = "Use an SSH URL, for example git@github.com:owner/repo.git."
	}
	if r.Path == "" {
		errs["Path"] = "Folder path is required."
	} else if !strings.HasPrefix(r.Path, "/") {
		errs["Path"] = "Folder path must be an absolute path."
	} else if r.Path == "/" {
		errs["Path"] = "Folder path cannot be the root folder."
	}
	return errs
}

func (r Repository) Slug() string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(r.Name), "-"), "-")
	if s == "" {
		return "repository"
	}
	return s
}

func (r Repository) LastActionLabel() string {
	switch r.LastAction {
	case ActionClone:
		return "Clone"
	case ActionPull:
		return "Pull"
	case ActionCheckout:
		return "Change branch"
	}
	return r.LastAction
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

const columns = "id, name, url, path, public_key, private_key, last_action, last_output, last_action_at, created_at, updated_at"

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Repository, error) {
	var r Repository
	var lastActionAt sql.NullTime
	err := row.Scan(
		&r.ID, &r.Name, &r.URL, &r.Path,
		&r.PublicKey, &r.PrivateKey,
		&r.LastAction, &r.LastOutput, &lastActionAt,
		&r.CreatedAt, &r.UpdatedAt,
	)
	if lastActionAt.Valid {
		r.LastActionAt = lastActionAt.Time
	}
	return r, err
}

func (s *Store) List() ([]Repository, error) {
	rows, err := s.db.Query("SELECT " + columns + " FROM repositories ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Repository
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, rows.Err()
}

func (s *Store) Get(id int64) (Repository, error) {
	return scan(s.db.QueryRow("SELECT "+columns+" FROM repositories WHERE id = ?", id))
}

func (s *Store) Create(r *Repository) error {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO repositories (name, url, path, public_key, private_key, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.URL, r.Path, r.PublicKey, r.PrivateKey, now, now,
	)
	if err != nil {
		return err
	}
	r.ID, err = res.LastInsertId()
	r.CreatedAt, r.UpdatedAt = now, now
	return err
}

func (s *Store) Update(r *Repository) error {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		"UPDATE repositories SET name = ?, url = ?, path = ?, updated_at = ? WHERE id = ?",
		r.Name, r.URL, r.Path, now, r.ID,
	)
	if err != nil {
		return err
	}
	r.UpdatedAt = now
	return affected(res)
}

func (s *Store) UpdateKey(id int64, public, private string) error {
	res, err := s.db.Exec(
		"UPDATE repositories SET public_key = ?, private_key = ?, updated_at = ? WHERE id = ?",
		public, private, time.Now().UTC(), id,
	)
	if err != nil {
		return err
	}
	return affected(res)
}

func (s *Store) SaveAction(id int64, action, output string) error {
	res, err := s.db.Exec(
		"UPDATE repositories SET last_action = ?, last_output = ?, last_action_at = ? WHERE id = ?",
		action, output, time.Now().UTC(), id,
	)
	if err != nil {
		return err
	}
	return affected(res)
}

func (s *Store) Delete(id int64) error {
	res, err := s.db.Exec("DELETE FROM repositories WHERE id = ?", id)
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
