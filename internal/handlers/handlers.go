package handlers

import (
	"html/template"
	"log"
	"net/http"

	"aura/internal/auth"
	"aura/internal/backups"
	"aura/internal/config"
	"aura/internal/console"
	"aura/internal/files"
	"aura/internal/projects"
	"aura/internal/repositories"
)

type Handlers struct {
	cfg       *config.Config
	sessions  *auth.SessionStore
	passwords *auth.PasswordStore
	templates *template.Template
	backups   *backups.Store
	runner    *backups.Runner
	files     *files.Manager
	repos     *repositories.Store
	git       *repositories.Git
	projects  *projects.Store
	docker    *projects.Docker
	commands  *console.Store
}

func New(cfg *config.Config, sessions *auth.SessionStore, passwords *auth.PasswordStore, templates *template.Template, backups *backups.Store, runner *backups.Runner, files *files.Manager, repos *repositories.Store, git *repositories.Git, projectStore *projects.Store, docker *projects.Docker, commands *console.Store) *Handlers {
	return &Handlers{cfg: cfg, sessions: sessions, passwords: passwords, templates: templates, backups: backups, runner: runner, files: files, repos: repos, git: git, projects: projectStore, docker: docker, commands: commands}
}

func (h *Handlers) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

type page struct {
	Title  string
	Active string
	Data   any
}
