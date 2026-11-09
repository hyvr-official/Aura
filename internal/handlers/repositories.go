package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"aura/internal/repositories"
)

type repositoryRow struct {
	Repository repositories.Repository
	State      repositories.FolderState
	Branch     string
}

type repositoriesIndexData struct {
	Repositories []repositoryRow
	Flash        string
}

type repositoriesShowData struct {
	Repository  repositories.Repository
	State       repositories.FolderState
	Info        repositories.Info
	InfoError   string
	Branches    []string
	BranchError string
	WebhookURL  string
	WebhookPort string
	WebhookLogs []repositories.WebhookLog
	Flash       string
	FlashType   string
}

type repositoriesUpsertData struct {
	Repository repositories.Repository
	Errors     map[string]string
}

var repositoryFlashes = map[string]string{
	"created":             "Repository created. Add the deploy key below to GitHub before cloning.",
	"updated":             "Repository updated.",
	"remote_failed":       "Repository updated, but changing the remote URL of the cloned folder failed. Check the server logs.",
	"deleted":             "Repository deleted. The files on disk were not removed.",
	"key_generated":       "A new SSH key was generated. Update the deploy key in GitHub.",
	"key_failed":          "Generating a new SSH key failed. Check the server logs.",
	"key_file_failed":     "The SSH key was saved, but writing it to ~/.ssh failed. Check the server logs.",
	"cloned":              "Repository cloned.",
	"pulled":              "Latest changes pulled.",
	"switched":            "Branch changed.",
	"failed":              "Operation failed. See the output below.",
	"busy":                "Another operation is already running for this repository.",
	"invalid_branch":      "Select a valid branch.",
	"webhook_regenerated": "A new webhook URL was generated. Update the webhook in GitHub or GitLab.",
	"post_pull_saved":     "Post pull commands saved.",
}

var repositoryFlashTypes = map[string]string{
	"remote_failed":   "warning",
	"key_failed":      "danger",
	"key_file_failed": "warning",
	"failed":          "danger",
	"busy":            "warning",
	"invalid_branch":  "danger",
}

func (h *Handlers) RepositoriesIndex(w http.ResponseWriter, r *http.Request) {
	list, err := h.repos.List()
	if err != nil {
		serverError(w, "list repositories", err)
		return
	}
	rows := make([]repositoryRow, 0, len(list))
	for _, repo := range list {
		row := repositoryRow{Repository: repo, State: repositories.State(repo.Path)}
		if row.State.IsRepo() {
			if info, err := repositories.ReadInfo(repo); err == nil {
				row.Branch = info.Branch
			}
		}
		rows = append(rows, row)
	}
	h.render(w, http.StatusOK, "repositories/index.html", page{
		Title:  "Repositories",
		Active: "repositories",
		Data:   repositoriesIndexData{Repositories: rows, Flash: repositoryFlashes[r.URL.Query().Get("status")]},
	})
}

func (h *Handlers) RepositoriesShow(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.findRepository(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	data := repositoriesShowData{
		Repository:  repo,
		State:       repositories.State(repo.Path),
		WebhookPort: h.cfg.WebhookPort,
		Flash:       repositoryFlashes[status],
		FlashType:   repositoryFlashTypes[status],
	}
	if data.FlashType == "" {
		data.FlashType = "success"
	}
	if err := h.repos.EnsureWebhookToken(&data.Repository); err != nil {
		log.Printf("ensure webhook token for repository %q: %v", repo.Name, err)
	}
	if data.Repository.WebhookToken != "" {
		data.WebhookURL = h.webhookURL(r, data.Repository)
	}
	logs, err := h.repos.ListWebhookLogs(repo.ID)
	if err != nil {
		log.Printf("list webhook logs for repository %q: %v", repo.Name, err)
	}
	data.WebhookLogs = logs
	if data.State.IsRepo() {
		info, err := repositories.ReadInfo(repo)
		if err != nil {
			data.InfoError = err.Error()
		}
		data.Info = info
		branches, err := h.git.Branches(repo)
		if err != nil {
			data.BranchError = err.Error()
		}
		data.Branches = branches
	}
	h.render(w, http.StatusOK, "repositories/show.html", page{
		Title:  repo.Name,
		Active: "repositories",
		Data:   data,
	})
}

func (h *Handlers) RepositoriesCreate(w http.ResponseWriter, r *http.Request) {
	h.renderRepositoryForm(w, http.StatusOK, repositories.Repository{}, nil)
}

func (h *Handlers) RepositoriesStore(w http.ResponseWriter, r *http.Request) {
	repo := repositoryFromForm(r)
	errs := repo.Validate()
	if _, ok := errs["Path"]; !ok {
		switch repositories.State(repo.Path) {
		case repositories.StateNotDir:
			errs["Path"] = "This path is a file, not a folder."
		case repositories.StateNotEmpty, repositories.StateRepo:
			errs["Path"] = "This folder is not empty. Choose an empty or new folder."
		}
	}
	if len(errs) > 0 {
		h.renderRepositoryForm(w, http.StatusUnprocessableEntity, repo, errs)
		return
	}
	public, private, err := repositories.GenerateKey("aura-" + repo.Slug())
	if err != nil {
		log.Printf("generate ssh key for repository %q: %v", repo.Name, err)
		h.renderRepositoryForm(w, http.StatusUnprocessableEntity, repo, map[string]string{"General": "Generating the SSH key failed: " + err.Error()})
		return
	}
	repo.PublicKey, repo.PrivateKey = public, private
	if err := h.repos.Create(&repo); err != nil {
		serverError(w, "create repository", err)
		return
	}
	status := "created"
	if _, err := repositories.SaveKeyFile(repo); err != nil {
		log.Printf("save ssh key file for repository %q: %v", repo.Name, err)
		status = "key_file_failed"
	}
	http.Redirect(w, r, fmt.Sprintf("/repositories/%d?status=%s", repo.ID, status), http.StatusSeeOther)
}

func (h *Handlers) RepositoriesEdit(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.findRepository(w, r)
	if !ok {
		return
	}
	h.renderRepositoryForm(w, http.StatusOK, repo, nil)
}

func (h *Handlers) RepositoriesUpdate(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.findRepository(w, r)
	if !ok {
		return
	}
	repo := repositoryFromForm(r)
	repo.ID = existing.ID
	if errs := repo.Validate(); len(errs) > 0 {
		h.renderRepositoryForm(w, http.StatusUnprocessableEntity, repo, errs)
		return
	}
	err := h.repos.Update(&repo)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, "update repository", err)
		return
	}
	repo.PrivateKey = existing.PrivateKey
	status := "updated"
	if repo.URL != existing.URL && repositories.State(repo.Path).IsRepo() {
		if err := h.git.SetRemote(repo); err != nil {
			log.Printf("set remote for repository %q: %v", repo.Name, err)
			status = "remote_failed"
		}
	}
	if err := h.git.MoveKey(existing, repo); err != nil {
		log.Printf("move ssh key file for repository %q: %v", repo.Name, err)
		status = "key_file_failed"
	}
	http.Redirect(w, r, fmt.Sprintf("/repositories/%d?status=%s", repo.ID, status), http.StatusSeeOther)
}

func (h *Handlers) RepositoriesKey(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.findRepository(w, r)
	if !ok {
		return
	}
	target := fmt.Sprintf("/repositories/%d", repo.ID)
	public, private, err := repositories.GenerateKey("aura-" + repo.Slug())
	if err != nil {
		log.Printf("generate ssh key for repository %q: %v", repo.Name, err)
		http.Redirect(w, r, target+"?status=key_failed", http.StatusSeeOther)
		return
	}
	if err := h.repos.UpdateKey(repo.ID, public, private); err != nil {
		serverError(w, "update repository key", err)
		return
	}
	status := "key_generated"
	repo.PublicKey, repo.PrivateKey = public, private
	if _, err := repositories.SaveKeyFile(repo); err != nil {
		log.Printf("save ssh key file for repository %q: %v", repo.Name, err)
		status = "key_file_failed"
	}
	http.Redirect(w, r, target+"?status="+status, http.StatusSeeOther)
}

func (h *Handlers) RepositoriesClone(w http.ResponseWriter, r *http.Request) {
	h.repositoryAction(w, r, repositories.ActionClone, "cloned", h.git.Clone)
}

func (h *Handlers) RepositoriesPull(w http.ResponseWriter, r *http.Request) {
	h.repositoryAction(w, r, repositories.ActionPull, "pulled", h.git.Pull)
}

func (h *Handlers) RepositoriesCheckout(w http.ResponseWriter, r *http.Request) {
	branch := strings.TrimSpace(r.PostFormValue("branch"))
	if branch == "" {
		id, ok := repositoryID(w, r)
		if ok {
			http.Redirect(w, r, fmt.Sprintf("/repositories/%d?status=invalid_branch", id), http.StatusSeeOther)
		}
		return
	}
	h.repositoryAction(w, r, repositories.ActionCheckout, "switched", func(repo repositories.Repository) (string, error) {
		return h.git.Checkout(repo, branch)
	})
}

func (h *Handlers) RepositoriesDelete(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.findRepository(w, r)
	if !ok {
		return
	}
	err := h.repos.Delete(repo.ID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, "delete repository", err)
		return
	}
	if err := repositories.RemoveKeyFile(repo); err != nil {
		log.Printf("remove ssh key file for repository %q: %v", repo.Name, err)
	}
	if err := h.repos.DeleteWebhookLogs(repo.ID); err != nil {
		log.Printf("delete webhook logs for repository %q: %v", repo.Name, err)
	}
	http.Redirect(w, r, "/repositories?status=deleted", http.StatusSeeOther)
}

func (h *Handlers) repositoryAction(w http.ResponseWriter, r *http.Request, action, okStatus string, fn func(repositories.Repository) (string, error)) {
	repo, ok := h.findRepository(w, r)
	if !ok {
		return
	}
	target := fmt.Sprintf("/repositories/%d", repo.ID)
	output, err := fn(repo)
	if errors.Is(err, repositories.ErrBusy) {
		http.Redirect(w, r, target+"?status=busy", http.StatusSeeOther)
		return
	}
	if serr := h.repos.SaveAction(repo.ID, action, output); serr != nil {
		log.Printf("save repository action %q: %v", repo.Name, serr)
	}
	if err != nil {
		log.Printf("repository %q %s: %v", repo.Name, action, err)
		http.Redirect(w, r, target+"?status=failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, target+"?status="+okStatus, http.StatusSeeOther)
}

func (h *Handlers) renderRepositoryForm(w http.ResponseWriter, status int, repo repositories.Repository, errs map[string]string) {
	title := "Create repository"
	if repo.ID != 0 {
		title = "Edit repository"
	}
	h.render(w, status, "repositories/upsert.html", page{
		Title:  title,
		Active: "repositories",
		Data:   repositoriesUpsertData{Repository: repo, Errors: errs},
	})
}

func repositoryFromForm(r *http.Request) repositories.Repository {
	repo := repositories.Repository{
		Name: r.PostFormValue("name"),
		URL:  r.PostFormValue("url"),
		Path: r.PostFormValue("path"),
	}
	repo.Normalize()
	return repo
}

func (h *Handlers) findRepository(w http.ResponseWriter, r *http.Request) (repositories.Repository, bool) {
	id, ok := repositoryID(w, r)
	if !ok {
		return repositories.Repository{}, false
	}
	repo, err := h.repos.Get(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return repositories.Repository{}, false
	}
	if err != nil {
		serverError(w, "get repository", err)
		return repositories.Repository{}, false
	}
	return repo, true
}

func repositoryID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}
