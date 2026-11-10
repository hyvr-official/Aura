package handlers

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"

	"aura/internal/cron"

	"github.com/gorilla/websocket"
)

type cronIndexData struct {
	Jobs        []cron.Job
	Users       []string
	CronMissing bool
	Flash       string
	FlashType   string
	Form        cron.Input
	Errors      map[string]string
	EditID      string
	FormOpen    bool
}

type cronCrontabData struct {
	User        string
	Users       []string
	Content     string
	Error       string
	Saved       bool
	CronMissing bool
}

var cronFlashes = map[string]string{
	"created":   "Cron job created.",
	"updated":   "Cron job updated.",
	"deleted":   "Cron job deleted.",
	"not_found": "Cron job not found. It may have been changed outside Aura, reload the page and try again.",
	"read_only": "This cron job is read-only.",
	"failed":    "Cron action failed. Check the server logs.",
}

func (h *Handlers) CronIndex(w http.ResponseWriter, r *http.Request) {
	data := cronIndexData{Form: cron.Input{User: "root", Schedule: "* * * * *"}}
	status := r.URL.Query().Get("status")
	data.Flash = cronFlashes[status]
	switch status {
	case "failed", "not_found":
		data.FlashType = "danger"
	case "read_only":
		data.FlashType = "warning"
	default:
		data.FlashType = "success"
	}
	h.renderCronIndex(w, http.StatusOK, data)
}

func (h *Handlers) CronStore(w http.ResponseWriter, r *http.Request) {
	in := cronInputFromForm(r)
	if errs := in.Validate(); len(errs) > 0 {
		h.renderCronForm(w, in, "", errs)
		return
	}
	if err := cron.Create(in); err != nil {
		log.Printf("create cron job: %v", err)
		h.renderCronForm(w, in, "", map[string]string{"form": err.Error()})
		return
	}
	http.Redirect(w, r, "/cron?status=created", http.StatusSeeOther)
}

func (h *Handlers) CronUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in := cronInputFromForm(r)
	if errs := in.Validate(); len(errs) > 0 {
		h.renderCronForm(w, in, id, errs)
		return
	}
	err := cron.Update(id, in)
	switch {
	case errors.Is(err, cron.ErrNotFound):
		http.Redirect(w, r, "/cron?status=not_found", http.StatusSeeOther)
	case errors.Is(err, cron.ErrReadOnly):
		http.Redirect(w, r, "/cron?status=read_only", http.StatusSeeOther)
	case err != nil:
		log.Printf("update cron job %s: %v", id, err)
		h.renderCronForm(w, in, id, map[string]string{"form": err.Error()})
	default:
		http.Redirect(w, r, "/cron?status=updated", http.StatusSeeOther)
	}
}

func (h *Handlers) CronDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := cron.Delete(id)
	switch {
	case errors.Is(err, cron.ErrNotFound):
		http.Redirect(w, r, "/cron?status=not_found", http.StatusSeeOther)
	case errors.Is(err, cron.ErrReadOnly):
		http.Redirect(w, r, "/cron?status=read_only", http.StatusSeeOther)
	case err != nil:
		log.Printf("delete cron job %s: %v", id, err)
		http.Redirect(w, r, "/cron?status=failed", http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/cron?status=deleted", http.StatusSeeOther)
	}
}

func (h *Handlers) CronLogs(w http.ResponseWriter, r *http.Request) {
	job, err := cron.Get(r.PathValue("id"))
	if errors.Is(err, cron.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, "get cron job", err)
		return
	}
	conn, err := shellUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("cron logs upgrade %s: %v", job.ID, err)
		return
	}
	defer conn.Close()
	header := "\x1b[90m==> cron run history (journalctl -u cron), this job has no output log"
	if job.HasOwnLog() {
		header = "\x1b[90m==> " + job.LogPath()
	}
	conn.WriteMessage(websocket.BinaryMessage, []byte(header+" <==\x1b[0m\r\n"))
	cmd := cron.Logs(job, 1000)
	out := &wsWriter{conn: conn}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		log.Printf("cron logs start %s: %v", job.ID, err)
		conn.WriteMessage(websocket.BinaryMessage, []byte("Failed to read logs: "+err.Error()+"\r\n"))
		return
	}
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cmd.Process.Signal(syscall.SIGTERM)
				return
			}
		}
	}()
	cmd.Wait()
	conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
}

func (h *Handlers) CronCrontab(w http.ResponseWriter, r *http.Request) {
	owner := strings.TrimSpace(r.URL.Query().Get("user"))
	if owner == "" {
		owner = "root"
	}
	data := cronCrontabData{User: owner}
	content, err := cron.ReadCrontab(owner)
	switch {
	case errors.Is(err, cron.ErrCronMissing):
		data.CronMissing = true
	case errors.Is(err, cron.ErrUnknownUser):
		http.NotFound(w, r)
		return
	case err != nil:
		serverError(w, "read crontab", err)
		return
	}
	data.Content = content
	data.Saved = r.URL.Query().Get("status") == "saved"
	h.renderCronCrontab(w, http.StatusOK, data)
}

func (h *Handlers) CronCrontabSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, cron.MaxCrontabLength*2)
	owner := strings.TrimSpace(r.PostFormValue("user"))
	content := r.PostFormValue("content")
	err := cron.SaveCrontab(owner, content)
	if errors.Is(err, cron.ErrUnknownUser) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("save crontab %s: %v", owner, err)
		h.renderCronCrontab(w, http.StatusUnprocessableEntity, cronCrontabData{User: owner, Content: content, Error: err.Error(), CronMissing: errors.Is(err, cron.ErrCronMissing)})
		return
	}
	http.Redirect(w, r, "/cron/crontab?status=saved&user="+url.QueryEscape(owner), http.StatusSeeOther)
}

func (h *Handlers) renderCronCrontab(w http.ResponseWriter, status int, data cronCrontabData) {
	data.Users = cron.Users()
	if !slices.Contains(data.Users, data.User) {
		data.Users = append(data.Users, data.User)
	}
	h.render(w, status, "cron/crontab.html", page{
		Title:  "Crontab · " + data.User,
		Active: "cron",
		Data:   data,
	})
}

func (h *Handlers) renderCronForm(w http.ResponseWriter, in cron.Input, id string, errs map[string]string) {
	h.renderCronIndex(w, http.StatusUnprocessableEntity, cronIndexData{Form: in, EditID: id, Errors: errs, FormOpen: true})
}

func (h *Handlers) renderCronIndex(w http.ResponseWriter, status int, data cronIndexData) {
	list, err := cron.List()
	if errors.Is(err, cron.ErrCronMissing) {
		data.CronMissing = true
	} else if err != nil {
		serverError(w, "list cron jobs", err)
		return
	}
	data.Jobs = list
	data.Users = cron.Users()
	h.render(w, status, "cron/index.html", page{
		Title:  "Cron",
		Active: "cron",
		Data:   data,
	})
}

func cronInputFromForm(r *http.Request) cron.Input {
	in := cron.Input{
		User:     r.PostFormValue("user"),
		Schedule: r.PostFormValue("schedule"),
		Command:  r.PostFormValue("command"),
	}
	in.Normalize()
	return in
}
