package database

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS backups (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			name             TEXT     NOT NULL,
			path             TEXT     NOT NULL,
			frequency        TEXT     NOT NULL,
			source           TEXT     NOT NULL,
			source_folder    TEXT     NOT NULL DEFAULT '',
			limit_enabled    INTEGER  NOT NULL DEFAULT 0,
			limit_count      INTEGER  NOT NULL DEFAULT 0,
			s3_access_key    TEXT     NOT NULL DEFAULT '',
			s3_secret        TEXT     NOT NULL DEFAULT '',
			s3_region        TEXT     NOT NULL DEFAULT '',
			s3_bucket        TEXT     NOT NULL DEFAULT '',
			s3_storage_class TEXT     NOT NULL DEFAULT '',
			local_path       TEXT     NOT NULL DEFAULT '',
			pre_run_enabled  INTEGER  NOT NULL DEFAULT 0,
			pre_run_script   TEXT     NOT NULL DEFAULT '',
			created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS backup_runs (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			backup_id   INTEGER  NOT NULL,
			trigger     TEXT     NOT NULL,
			status      TEXT     NOT NULL,
			file_path   TEXT     NOT NULL DEFAULT '',
			file_size   INTEGER  NOT NULL DEFAULT 0,
			log         TEXT     NOT NULL DEFAULT '',
			started_at  DATETIME NOT NULL,
			finished_at DATETIME
		);

		CREATE INDEX IF NOT EXISTS idx_backup_runs_backup_id ON backup_runs (backup_id, id);

		CREATE TABLE IF NOT EXISTS repositories (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			name           TEXT     NOT NULL,
			url            TEXT     NOT NULL,
			path           TEXT     NOT NULL,
			public_key     TEXT     NOT NULL DEFAULT '',
			private_key    TEXT     NOT NULL DEFAULT '',
			last_action    TEXT     NOT NULL DEFAULT '',
			last_output    TEXT     NOT NULL DEFAULT '',
			last_action_at DATETIME,
			created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS repository_webhook_logs (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			repository_id INTEGER  NOT NULL,
			event         TEXT     NOT NULL DEFAULT '',
			ref           TEXT     NOT NULL DEFAULT '',
			status        TEXT     NOT NULL,
			log           TEXT     NOT NULL DEFAULT '',
			started_at    DATETIME NOT NULL,
			finished_at   DATETIME
		);

		CREATE INDEX IF NOT EXISTS idx_repository_webhook_logs_repository_id ON repository_webhook_logs (repository_id, id);

		CREATE TABLE IF NOT EXISTS projects (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			name           TEXT     NOT NULL,
			type           TEXT     NOT NULL,
			path           TEXT     NOT NULL DEFAULT '',
			repository_id  INTEGER  NOT NULL DEFAULT 0,
			last_action    TEXT     NOT NULL DEFAULT '',
			last_folder    TEXT     NOT NULL DEFAULT '',
			last_output    TEXT     NOT NULL DEFAULT '',
			last_action_at DATETIME,
			created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS console_commands (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT     NOT NULL DEFAULT '',
			command    TEXT     NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS sessions (
			token      TEXT    PRIMARY KEY,
			expires_at INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
	`)
	if err != nil {
		return err
	}
	if err := renameColumn(db, "backups", "s3_folder", "source_folder"); err != nil {
		return err
	}
	if err := addColumns(db, "repositories", []string{
		"webhook_token TEXT NOT NULL DEFAULT ''",
		"post_pull_commands TEXT NOT NULL DEFAULT ''",
	}); err != nil {
		return err
	}
	return addColumns(db, "backups", []string{
		"source_folder TEXT NOT NULL DEFAULT ''",
		"limit_enabled INTEGER NOT NULL DEFAULT 0",
		"limit_count INTEGER NOT NULL DEFAULT 0",
		"s3_access_key TEXT NOT NULL DEFAULT ''",
		"s3_secret TEXT NOT NULL DEFAULT ''",
		"s3_region TEXT NOT NULL DEFAULT ''",
		"s3_bucket TEXT NOT NULL DEFAULT ''",
		"s3_storage_class TEXT NOT NULL DEFAULT ''",
		"local_path TEXT NOT NULL DEFAULT ''",
		"pre_run_enabled INTEGER NOT NULL DEFAULT 0",
		"pre_run_script TEXT NOT NULL DEFAULT ''",
	})
}

func columnNames(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	existing := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		existing[name] = true
	}
	return existing, rows.Err()
}

func renameColumn(db *sql.DB, table, from, to string) error {
	existing, err := columnNames(db, table)
	if err != nil {
		return err
	}
	if !existing[from] || existing[to] {
		return nil
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", table, from, to))
	return err
}

func addColumns(db *sql.DB, table string, defs []string) error {
	existing, err := columnNames(db, table)
	if err != nil {
		return err
	}
	for _, def := range defs {
		if existing[strings.Fields(def)[0]] {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, def)); err != nil {
			return err
		}
	}
	return nil
}
