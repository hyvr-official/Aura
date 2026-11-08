package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"aura/internal/projects"
	"aura/internal/repositories"
)

type projectRow struct {
	Project           projects.Project
	Repository        repositories.Repository
	RepositoryMissing bool
}

type projectsIndexData struct {
	Projects []projectRow
	Flash    string
}

type projectsShowData struct {
	Project           projects.Project
	Repository        repositories.Repository
	RepositoryMissing bool
	State             repositories.FolderState
	Info              repositories.Info
	InfoError         string
	Dir               string
	DirReady          bool
	Folders           []projects.Folder
	FoldersError      string
	Flash             string
	FlashType         string
}

type projectsUpsertData struct {
	Project      projects.Project
	Repositories []repositories.Repository
	Errors       map[string]string
}

var projectFlashes = map[string]string{
	"created":        "Project created.",
	"updated":        "Project updated.",
	"deleted":        "Project deleted. The files on disk and running containers were not removed.",
	"pulled":         "Latest changes pulled.",
	"deployed":       "Deployment finished.",
	"stopped":        "Project is down.",
	"failed":         "Operation failed. See the output below.",
	"busy":           "Another operation is already running for this project.",
	"invalid_folder": "Select a valid folder.",
	"not_repository": "This project is not linked to a cloned repository.",
}

var projectFlashTypes = map[string]string{
	"failed":         "danger",
	"busy":           "warning",
	"invalid_folder": "danger",
	"not_repository": "danger",
}

func (h *Handlers) ProjectsIndex(w http.ResponseWriter, r *http.Request) {
	list, err := h.projects.List()
	if err != nil {
		serverError(w, "list projects", err)
		return
	}
	repoList, err := h.repos.List()
	if err != nil {
		serverError(w, "list repositories", err)
		return
	}
	repoByID := make(map[int64]repositories.Repository, len(repoList))
	for _, repo := range repoList {
		repoByID[repo.ID] = repo
	}
	rows := make([]projectRow, 0, len(list))
	for _, p := range list {
		row := projectRow{Project: p}
		if p.IsRepository() {
			repo, ok := repoByID[p.RepositoryID]
			row.Repository = repo
			row.RepositoryMissing = !ok
		}
		rows = append(rows, row)
	}
	h.render(w, http.StatusOK, "projects_index.html", page{
		Title:  "Projects",
		Active: "projects",
		Data:   projectsIndexData{Projects: rows, Flash: projectFlashes[r.URL.Query().Get("status")]},
	})
}

func (h *Handlers) ProjectsShow(w http.ResponseWriter, r *http.Request) {
	p, ok := h.findProject(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	data := projectsShowData{
		Project:   p,
		Flash:     projectFlashes[status],
		FlashType: projectFlashTypes[status],
	}
	if data.FlashType == "" {
		data.FlashType = "success"
	}
	dir, repo, missing, err := h.projectDir(p)
	if err != nil {
		serverError(w, "get project repository", err)
		return
	}
	data.Dir, data.Repository, data.RepositoryMissing = dir, repo, missing
	if p.IsRepository() && !missing {
		data.State = repositories.State(repo.Path)
		if data.State.IsRepo() {
			info, err := repositories.ReadInfo(repo)
			if err != nil {
				data.InfoError = err.Error()
			}
			data.Info = info
		}
	}
	data.DirReady = dir != "" && (p.IsFolder() || data.State.IsRepo())
	if data.DirReady {
		folders, err := projects.ScanFolders(dir)
		if err != nil {
			data.FoldersError = err.Error()
		}
		data.Folders = folders
	}
	h.render(w, http.StatusOK, "projects_show.html", page{
		Title:  p.Name,
		Active: "projects",
		Data:   data,
	})
}

func (h *Handlers) ProjectsCreate(w http.ResponseWriter, r *http.Request) {
	h.renderProjectForm(w, http.StatusOK, projects.Project{Type: projects.TypeFolder}, nil)
}

func (h *Handlers) ProjectsStore(w http.ResponseWriter, r *http.Request) {
	p := projectFromForm(r, r.PostFormValue("type"))
	errs := h.validateProject(p)
	if len(errs) > 0 {
		h.renderProjectForm(w, http.StatusUnprocessableEntity, p, errs)
		return
	}
	if err := h.projects.Create(&p); err != nil {
		serverError(w, "create project", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/projects/%d?status=created", p.ID), http.StatusSeeOther)
}

func (h *Handlers) ProjectsEdit(w http.ResponseWriter, r *http.Request) {
	p, ok := h.findProject(w, r)
	if !ok {
		return
	}
	h.renderProjectForm(w, http.StatusOK, p, nil)
}

func (h *Handlers) ProjectsUpdate(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.findProject(w, r)
	if !ok {
		return
	}
	p := projectFromForm(r, existing.Type)
	p.ID = existing.ID
	if errs := h.validateProject(p); len(errs) > 0 {
		h.renderProjectForm(w, http.StatusUnprocessableEntity, p, errs)
		return
	}
	err := h.projects.Update(&p)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, "update project", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/projects/%d?status=updated", p.ID), http.StatusSeeOther)
}

func (h *Handlers) ProjectsPull(w http.ResponseWriter, r *http.Request) {
	p, ok := h.findProject(w, r)
	if !ok {
		return
	}
	target := fmt.Sprintf("/projects/%d", p.ID)
	if !p.IsRepository() {
		http.Redirect(w, r, target+"?status=not_repository", http.StatusSeeOther)
		return
	}
	repo, err := h.repos.Get(p.RepositoryID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !repositories.State(repo.Path).IsRepo()) {
		http.Redirect(w, r, target+"?status=not_repository", http.StatusSeeOther)
		return
	}
	if err != nil {
		serverError(w, "get project repository", err)
		return
	}
	output, err := h.git.Pull(repo)
	if errors.Is(err, repositories.ErrBusy) {
		http.Redirect(w, r, target+"?status=busy", http.StatusSeeOther)
		return
	}
	if serr := h.repos.SaveAction(repo.ID, repositories.ActionPull, output); serr != nil {
		log.Printf("save repository action %q: %v", repo.Name, serr)
	}
	if serr := h.projects.SaveAction(p.ID, projects.ActionPull, "", output); serr != nil {
		log.Printf("save project action %q: %v", p.Name, serr)
	}
	if err != nil {
		log.Printf("project %q pull: %v", p.Name, err)
		http.Redirect(w, r, target+"?status=failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, target+"?status=pulled", http.StatusSeeOther)
}

func (h *Handlers) ProjectsDeploy(w http.ResponseWriter, r *http.Request) {
	h.projectAction(w, r, projects.ActionDeploy, "deployed", h.docker.Deploy)
}

func (h *Handlers) ProjectsDown(w http.ResponseWriter, r *http.Request) {
	h.projectAction(w, r, projects.ActionDown, "stopped", h.docker.Down)
}

func (h *Handlers) ProjectsDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := projectID(w, r)
	if !ok {
		return
	}
	err := h.projects.Delete(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, "delete project", err)
		return
	}
	http.Redirect(w, r, "/projects?status=deleted", http.StatusSeeOther)
}

func (h *Handlers) projectAction(w http.ResponseWriter, r *http.Request, action, okStatus string, fn func(projects.Project, projects.Folder) (string, error)) {
	p, ok := h.findProject(w, r)
	if !ok {
		return
	}
	target := fmt.Sprintf("/projects/%d", p.ID)
	folder, found, err := h.projectFolder(p, strings.TrimSpace(r.PostFormValue("folder")))
	if err != nil {
		serverError(w, "get project folder", err)
		return
	}
	if !found {
		http.Redirect(w, r, target+"?status=invalid_folder", http.StatusSeeOther)
		return
	}
	output, err := fn(p, folder)
	if errors.Is(err, projects.ErrBusy) {
		http.Redirect(w, r, target+"?status=busy", http.StatusSeeOther)
		return
	}
	if serr := h.projects.SaveAction(p.ID, action, folder.Rel, output); serr != nil {
		log.Printf("save project action %q: %v", p.Name, serr)
	}
	if err != nil {
		log.Printf("project %q %s %s: %v", p.Name, action, folder.Rel, err)
		http.Redirect(w, r, target+"?status=failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, target+"?status="+okStatus, http.StatusSeeOther)
}

func (h *Handlers) projectFolder(p projects.Project, rel string) (projects.Folder, bool, error) {
	if rel == "" {
		return projects.Folder{}, false, nil
	}
	dir, _, missing, err := h.projectDir(p)
	if err != nil {
		return projects.Folder{}, false, err
	}
	if missing || dir == "" {
		return projects.Folder{}, false, nil
	}
	folders, err := projects.ScanFolders(dir)
	if err != nil {
		return projects.Folder{}, false, nil
	}
	for _, f := range folders {
		if f.Rel == rel {
			return f, true, nil
		}
	}
	return projects.Folder{}, false, nil
}

func (h *Handlers) projectDir(p projects.Project) (string, repositories.Repository, bool, error) {
	if p.IsFolder() {
		return p.Path, repositories.Repository{}, false, nil
	}
	repo, err := h.repos.Get(p.RepositoryID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", repositories.Repository{}, true, nil
	}
	if err != nil {
		return "", repositories.Repository{}, false, err
	}
	return repo.Path, repo, false, nil
}

func (h *Handlers) validateProject(p projects.Project) map[string]string {
	errs := p.Validate()
	if p.IsFolder() {
		if _, ok := errs["Path"]; !ok {
			info, err := os.Stat(p.Path)
			if err != nil {
				errs["Path"] = "This folder does not exist."
			} else if !info.IsDir() {
				errs["Path"] = "This path is a file, not a folder."
			}
		}
	}
	if p.IsRepository() {
		if _, ok := errs["RepositoryID"]; !ok {
			if _, err := h.repos.Get(p.RepositoryID); err != nil {
				errs["RepositoryID"] = "Select a valid repository."
			}
		}
	}
	return errs
}

func (h *Handlers) renderProjectForm(w http.ResponseWriter, status int, p projects.Project, errs map[string]string) {
	repoList, err := h.repos.List()
	if err != nil {
		serverError(w, "list repositories", err)
		return
	}
	title := "Create project"
	if p.ID != 0 {
		title = "Edit project"
	}
	h.render(w, status, "projects_upsert.html", page{
		Title:  title,
		Active: "projects",
		Data:   projectsUpsertData{Project: p, Repositories: repoList, Errors: errs},
	})
}

func projectFromForm(r *http.Request, typ string) projects.Project {
	repoID, _ := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("repository_id")), 10, 64)
	p := projects.Project{
		Name:         r.PostFormValue("name"),
		Type:         typ,
		Path:         r.PostFormValue("path"),
		RepositoryID: repoID,
	}
	p.Normalize()
	return p
}

func (h *Handlers) findProject(w http.ResponseWriter, r *http.Request) (projects.Project, bool) {
	id, ok := projectID(w, r)
	if !ok {
		return projects.Project{}, false
	}
	p, err := h.projects.Get(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return projects.Project{}, false
	}
	if err != nil {
		serverError(w, "get project", err)
		return projects.Project{}, false
	}
	return p, true
}

func projectID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}
