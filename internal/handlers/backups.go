package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"aura/internal/backups"
)

type backupsIndexData struct {
	Backups []backups.Backup
	Flash   string
}

type backupsShowData struct {
	Backup    backups.Backup
	Runs      []backups.Run
	Running   bool
	Flash     string
	FlashType string
}

type backupsUpsertData struct {
	Backup         backups.Backup
	Errors         map[string]string
	Frequencies    []backups.Option
	Sources        []backups.Option
	StorageClasses []backups.Option
}

var backupFlashes = map[string]string{
	"created": "Backup created.",
	"updated": "Backup updated.",
	"deleted": "Backup deleted.",
	"started": "Backup started.",
	"running": "A backup for this task is already running.",
}

func (h *Handlers) BackupsIndex(w http.ResponseWriter, r *http.Request) {
	list, err := h.backups.List()
	if err != nil {
		serverError(w, "list backups", err)
		return
	}
	lastRuns, err := h.backups.LastRuns()
	if err != nil {
		serverError(w, "list last backup runs", err)
		return
	}
	for i := range list {
		list[i].LastRunAt = lastRuns[list[i].ID]
	}
	h.render(w, http.StatusOK, "backups_index.html", page{
		Title:  "Backups",
		Active: "backups",
		Data:   backupsIndexData{Backups: list, Flash: backupFlashes[r.URL.Query().Get("status")]},
	})
}

func (h *Handlers) BackupsShow(w http.ResponseWriter, r *http.Request) {
	b, ok := h.findBackup(w, r)
	if !ok {
		return
	}
	runs, err := h.backups.ListRuns(b.ID, 50)
	if err != nil {
		serverError(w, "list backup runs", err)
		return
	}
	running, err := h.backups.HasRunning(b.ID)
	if err != nil {
		serverError(w, "check running backup", err)
		return
	}
	status := r.URL.Query().Get("status")
	flashType := "success"
	if status == "running" {
		flashType = "warning"
	}
	h.render(w, http.StatusOK, "backups_show.html", page{
		Title:  b.Name,
		Active: "backups",
		Data: backupsShowData{
			Backup:    b,
			Runs:      runs,
			Running:   running,
			Flash:     backupFlashes[status],
			FlashType: flashType,
		},
	})
}

func (h *Handlers) BackupsRun(w http.ResponseWriter, r *http.Request) {
	b, ok := h.findBackup(w, r)
	if !ok {
		return
	}
	target := fmt.Sprintf("/backups/%d", b.ID)
	run, err := h.runner.Start(b, backups.TriggerManual)
	if errors.Is(err, backups.ErrAlreadyRunning) {
		http.Redirect(w, r, target+"?status=running", http.StatusSeeOther)
		return
	}
	if err != nil {
		serverError(w, "start backup", err)
		return
	}
	go func() {
		if err := h.runner.Execute(b, run); err != nil {
			log.Printf("backup %q: %v", b.Name, err)
		}
	}()
	http.Redirect(w, r, target+"?status=started", http.StatusSeeOther)
}

func (h *Handlers) BackupsCreate(w http.ResponseWriter, r *http.Request) {
	h.renderBackupForm(w, http.StatusOK, backups.Backup{}, nil)
}

func (h *Handlers) BackupsStore(w http.ResponseWriter, r *http.Request) {
	b := backupFromForm(r)
	if errs := b.Validate(); len(errs) > 0 {
		h.renderBackupForm(w, http.StatusUnprocessableEntity, b, errs)
		return
	}
	if err := h.backups.Create(&b); err != nil {
		serverError(w, "create backup", err)
		return
	}
	http.Redirect(w, r, "/backups?status=created", http.StatusSeeOther)
}

func (h *Handlers) BackupsEdit(w http.ResponseWriter, r *http.Request) {
	b, ok := h.findBackup(w, r)
	if !ok {
		return
	}
	h.renderBackupForm(w, http.StatusOK, b, nil)
}

func (h *Handlers) BackupsUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := backupID(w, r)
	if !ok {
		return
	}
	b := backupFromForm(r)
	b.ID = id
	if errs := b.Validate(); len(errs) > 0 {
		h.renderBackupForm(w, http.StatusUnprocessableEntity, b, errs)
		return
	}
	err := h.backups.Update(&b)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, "update backup", err)
		return
	}
	http.Redirect(w, r, "/backups?status=updated", http.StatusSeeOther)
}

func (h *Handlers) BackupsDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := backupID(w, r)
	if !ok {
		return
	}
	err := h.backups.Delete(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, "delete backup", err)
		return
	}
	http.Redirect(w, r, "/backups?status=deleted", http.StatusSeeOther)
}

func (h *Handlers) renderBackupForm(w http.ResponseWriter, status int, b backups.Backup, errs map[string]string) {
	title := "Create backup"
	if b.ID != 0 {
		title = "Edit backup"
	}
	h.render(w, status, "backups_upsert.html", page{
		Title:  title,
		Active: "backups",
		Data: backupsUpsertData{
			Backup:         b,
			Errors:         errs,
			Frequencies:    backups.Frequencies,
			Sources:        backups.Sources,
			StorageClasses: backups.StorageClasses,
		},
	})
}

func backupFromForm(r *http.Request) backups.Backup {
	b := backups.Backup{
		Name:           r.PostFormValue("name"),
		Path:           r.PostFormValue("path"),
		Frequency:      r.PostFormValue("frequency"),
		Source:         r.PostFormValue("source"),
		SourceFolder:   r.PostFormValue("source_folder"),
		LimitEnabled:   r.PostFormValue("limit_enabled") == "1",
		S3AccessKey:    r.PostFormValue("s3_access_key"),
		S3Secret:       r.PostFormValue("s3_secret"),
		S3Region:       r.PostFormValue("s3_region"),
		S3Bucket:       r.PostFormValue("s3_bucket"),
		S3StorageClass: r.PostFormValue("s3_storage_class"),
		LocalPath:      r.PostFormValue("local_path"),
		PreRunEnabled:  r.PostFormValue("pre_run_enabled") == "1",
		PreRunScript:   r.PostFormValue("pre_run_script"),
	}
	b.LimitCount, _ = strconv.Atoi(strings.TrimSpace(r.PostFormValue("limit_count")))
	b.Normalize()
	return b
}

func (h *Handlers) findBackup(w http.ResponseWriter, r *http.Request) (backups.Backup, bool) {
	id, ok := backupID(w, r)
	if !ok {
		return backups.Backup{}, false
	}
	b, err := h.backups.Get(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return backups.Backup{}, false
	}
	if err != nil {
		serverError(w, "get backup", err)
		return backups.Backup{}, false
	}
	return b, true
}

func backupID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

func serverError(w http.ResponseWriter, action string, err error) {
	log.Printf("%s: %v", action, err)
	http.Error(w, "Internal server error", http.StatusInternalServerError)
}
