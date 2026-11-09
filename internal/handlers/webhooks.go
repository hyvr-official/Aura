package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"aura/internal/repositories"
)

const (
	webhookBodyLimit     = 25 << 20
	webhookWaitLimit     = 40 * time.Minute
	webhookRetryInterval = 3 * time.Second
)

type webhookPayload struct {
	Ref string `json:"ref"`
}

func (h *Handlers) WebhookReceive(w http.ResponseWriter, r *http.Request) {
	repo, err := h.repos.GetByWebhookToken(r.PathValue("token"))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("webhook: find repository: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	event := webhookEvent(r)
	payload := readWebhookPayload(w, r)

	if event == "ping" {
		h.skipWebhook(repo, event, payload.Ref, "Ping received, the webhook is configured correctly.")
		writeWebhookResponse(w, http.StatusOK, "pong")
		return
	}
	if !repositories.State(repo.Path).IsRepo() {
		h.skipWebhook(repo, event, payload.Ref, fmt.Sprintf("Skipped: the folder %s is not a cloned git repository. Clone the repository first.", repo.Path))
		writeWebhookResponse(w, http.StatusOK, "skipped: repository is not cloned")
		return
	}
	branch, err := repositories.CurrentBranch(repo)
	if err != nil {
		h.skipWebhook(repo, event, payload.Ref, "Failed: could not read the current branch: "+err.Error())
		writeWebhookResponse(w, http.StatusOK, "skipped: could not read the current branch")
		return
	}
	if payload.Ref != "" && payload.Ref != "refs/heads/"+branch {
		h.skipWebhook(repo, event, payload.Ref, fmt.Sprintf("Skipped: the push was to %s, but the repository is on branch %s.", payload.Ref, branch))
		writeWebhookResponse(w, http.StatusOK, "skipped: push is not for the current branch")
		return
	}

	writeWebhookResponse(w, http.StatusAccepted, h.dispatchWebhook(repo, event, payload.Ref))
}

type webhookQueue struct {
	mu      sync.Mutex
	running map[int64]bool
	pending map[int64]repositories.WebhookLog
}

func newWebhookQueue() *webhookQueue {
	return &webhookQueue{running: map[int64]bool{}, pending: map[int64]repositories.WebhookLog{}}
}

func (h *Handlers) dispatchWebhook(repo repositories.Repository, event, ref string) string {
	h.hooks.mu.Lock()
	defer h.hooks.mu.Unlock()
	if h.hooks.running[repo.ID] {
		entry, err := h.repos.StartWebhookLog(repo.ID, event, ref, repositories.WebhookQueued)
		if err != nil {
			log.Printf("webhook %q: start log: %v", repo.Name, err)
		}
		if old, ok := h.hooks.pending[repo.ID]; ok {
			old.Status = repositories.WebhookSkipped
			old.Log = "Skipped: a newer delivery arrived while this one was queued. The newer delivery pulls the latest changes.\n"
			h.finishWebhookLog(repo.Name, &old)
		}
		h.hooks.pending[repo.ID] = entry
		return "accepted: pull queued after the running pull"
	}
	entry, err := h.repos.StartWebhookLog(repo.ID, event, ref, repositories.WebhookRunning)
	if err != nil {
		log.Printf("webhook %q: start log: %v", repo.Name, err)
	}
	h.hooks.running[repo.ID] = true
	go h.webhookWorker(repo.ID, entry)
	return "accepted: pull started"
}

func (h *Handlers) webhookWorker(id int64, entry repositories.WebhookLog) {
	for {
		h.runWebhookPull(id, entry)
		h.hooks.mu.Lock()
		next, ok := h.hooks.pending[id]
		if !ok {
			delete(h.hooks.running, id)
			h.hooks.mu.Unlock()
			return
		}
		delete(h.hooks.pending, id)
		h.hooks.mu.Unlock()
		if next.ID != 0 {
			if err := h.repos.MarkWebhookLogRunning(&next); err != nil {
				log.Printf("webhook repository %d: mark log running: %v", id, err)
			}
		}
		entry = next
	}
}

func (h *Handlers) runWebhookPull(id int64, entry repositories.WebhookLog) {
	repo, err := h.repos.Get(id)
	if err != nil {
		entry.Status = repositories.WebhookFailed
		entry.Log = "Failed: could not load the repository: " + err.Error() + "\n"
		h.finishWebhookLog(fmt.Sprint(id), &entry)
		return
	}
	var output string
	waited := false
	deadline := time.Now().Add(webhookWaitLimit)
	for {
		output, err = h.git.Pull(repo)
		if !errors.Is(err, repositories.ErrBusy) || time.Now().After(deadline) {
			break
		}
		waited = true
		time.Sleep(webhookRetryInterval)
	}
	if errors.Is(err, repositories.ErrBusy) {
		entry.Status = repositories.WebhookFailed
		entry.Log = fmt.Sprintf("Failed: another operation on this repository was still running after waiting %s.\n", webhookWaitLimit)
		h.finishWebhookLog(repo.Name, &entry)
		return
	}
	entry.Log = output
	if waited {
		entry.Log = "Waited for another operation on this repository to finish.\n" + output
	}
	entry.Status = repositories.WebhookSuccess
	if err != nil {
		entry.Status = repositories.WebhookFailed
		log.Printf("webhook %q: pull: %v", repo.Name, err)
	}
	if serr := h.repos.SaveAction(repo.ID, repositories.ActionWebhook, output); serr != nil {
		log.Printf("webhook %q: save action: %v", repo.Name, serr)
	}
	h.finishWebhookLog(repo.Name, &entry)
}

func (h *Handlers) finishWebhookLog(name string, entry *repositories.WebhookLog) {
	if entry.ID == 0 {
		return
	}
	if err := h.repos.FinishWebhookLog(entry); err != nil {
		log.Printf("webhook %q: finish log: %v", name, err)
	}
}

func (h *Handlers) skipWebhook(repo repositories.Repository, event, ref, message string) {
	entry, err := h.repos.StartWebhookLog(repo.ID, event, ref, repositories.WebhookRunning)
	if err != nil {
		log.Printf("webhook %q: start log: %v", repo.Name, err)
		return
	}
	entry.Status = repositories.WebhookSkipped
	entry.Log = message + "\n"
	if err := h.repos.FinishWebhookLog(&entry); err != nil {
		log.Printf("webhook %q: finish log: %v", repo.Name, err)
	}
}

func webhookEvent(r *http.Request) string {
	for _, name := range []string{"X-GitHub-Event", "X-Gitlab-Event"} {
		if v := strings.TrimSpace(r.Header.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

func readWebhookPayload(w http.ResponseWriter, r *http.Request) webhookPayload {
	var payload webhookPayload
	r.Body = http.MaxBytesReader(w, r.Body, webhookBodyLimit)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if raw := r.PostFormValue("payload"); raw != "" {
			json.Unmarshal([]byte(raw), &payload)
		}
		return payload
	}
	json.NewDecoder(r.Body).Decode(&payload)
	return payload
}

func writeWebhookResponse(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintln(w, message)
}

func (h *Handlers) webhookURL(r *http.Request, repo repositories.Repository) string {
	host := r.Host
	if name, _, err := net.SplitHostPort(r.Host); err == nil {
		host = name
	}
	host = strings.Trim(host, "[]")
	return "http://" + net.JoinHostPort(host, h.cfg.WebhookPort) + "/hooks/" + repo.WebhookToken
}

func (h *Handlers) RepositoriesWebhookRegenerate(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.findRepository(w, r)
	if !ok {
		return
	}
	if err := h.repos.RegenerateWebhookToken(&repo); err != nil {
		serverError(w, "regenerate repository webhook token", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/repositories/%d?status=webhook_regenerated", repo.ID), http.StatusSeeOther)
}

func (h *Handlers) RepositoriesPostPull(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.findRepository(w, r)
	if !ok {
		return
	}
	commands := strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("post_pull_commands"), "\r\n", "\n"))
	err := h.repos.UpdatePostPullCommands(repo.ID, commands)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, "update repository post pull commands", err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/repositories/%d?status=post_pull_saved", repo.ID), http.StatusSeeOther)
}
