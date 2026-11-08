package projects

import (
	"database/sql"
	"path"
	"regexp"
	"strings"
	"time"
)

const (
	TypeFolder     = "folder"
	TypeRepository = "repository"
)

type Project struct {
	ID           int64
	Name         string
	Type         string
	Path         string
	RepositoryID int64

	LastAction   string
	LastFolder   string
	LastOutput   string
	LastActionAt time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func (p *Project) Normalize() {
	p.Name = strings.TrimSpace(p.Name)
	p.Type = strings.TrimSpace(p.Type)
	p.Path = strings.TrimSpace(p.Path)
	if p.Path != "" {
		p.Path = path.Clean(p.Path)
	}
	switch p.Type {
	case TypeFolder:
		p.RepositoryID = 0
	case TypeRepository:
		p.Path = ""
	}
}

func (p Project) Validate() map[string]string {
	errs := map[string]string{}
	if p.Name == "" {
		errs["Name"] = "Name is required."
	}
	switch p.Type {
	case TypeFolder:
		if p.Path == "" {
			errs["Path"] = "Folder path is required."
		} else if !strings.HasPrefix(p.Path, "/") {
			errs["Path"] = "Folder path must be an absolute path."
		} else if p.Path == "/" {
			errs["Path"] = "Folder path cannot be the root folder."
		}
	case TypeRepository:
		if p.RepositoryID <= 0 {
			errs["RepositoryID"] = "Select a repository."
		}
	default:
		errs["Type"] = "Select a valid type."
	}
	return errs
}

func (p Project) IsFolder() bool     { return p.Type == TypeFolder }
func (p Project) IsRepository() bool { return p.Type == TypeRepository }

func (p Project) TypeLabel() string {
	switch p.Type {
	case TypeFolder:
		return "Folder"
	case TypeRepository:
		return "Repository"
	}
	return p.Type
}

func (p Project) Slug() string {
	return slug(p.Name, "project")
}

func (p Project) LastActionLabel() string {
	switch p.LastAction {
	case ActionPull:
		return "Pull"
	case ActionDeploy:
		return "Deploy"
	case ActionDown:
		return "Down"
	}
	return p.LastAction
}

func slug(s, fallback string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return fallback
	}
	return s
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

const columns = "id, name, type, path, repository_id, last_action, last_folder, last_output, last_action_at, created_at, updated_at"

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Project, error) {
	var p Project
	var lastActionAt sql.NullTime
	err := row.Scan(
		&p.ID, &p.Name, &p.Type, &p.Path, &p.RepositoryID,
		&p.LastAction, &p.LastFolder, &p.LastOutput, &lastActionAt,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if lastActionAt.Valid {
		p.LastActionAt = lastActionAt.Time
	}
	return p, err
}

func (s *Store) List() ([]Project, error) {
	rows, err := s.db.Query("SELECT " + columns + " FROM projects ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Project
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

func (s *Store) Get(id int64) (Project, error) {
	return scan(s.db.QueryRow("SELECT "+columns+" FROM projects WHERE id = ?", id))
}

func (s *Store) Create(p *Project) error {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO projects (name, type, path, repository_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		p.Name, p.Type, p.Path, p.RepositoryID, now, now,
	)
	if err != nil {
		return err
	}
	p.ID, err = res.LastInsertId()
	p.CreatedAt, p.UpdatedAt = now, now
	return err
}

func (s *Store) Update(p *Project) error {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		"UPDATE projects SET name = ?, path = ?, repository_id = ?, updated_at = ? WHERE id = ?",
		p.Name, p.Path, p.RepositoryID, now, p.ID,
	)
	if err != nil {
		return err
	}
	p.UpdatedAt = now
	return affected(res)
}

func (s *Store) SaveAction(id int64, action, folder, output string) error {
	res, err := s.db.Exec(
		"UPDATE projects SET last_action = ?, last_folder = ?, last_output = ?, last_action_at = ? WHERE id = ?",
		action, folder, output, time.Now().UTC(), id,
	)
	if err != nil {
		return err
	}
	return affected(res)
}

func (s *Store) Delete(id int64) error {
	res, err := s.db.Exec("DELETE FROM projects WHERE id = ?", id)
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
