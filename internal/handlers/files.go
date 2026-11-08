package handlers

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"

	"aura/internal/files"
)

type filesIndexData struct {
	Path      string
	Parent    string
	Crumbs    []files.Crumb
	Entries   []files.Entry
	Open      string
	Flash     string
	FlashType string
}

type fileContentResponse struct {
	OK      bool   `json:"ok"`
	Path    string `json:"path"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Mode    string `json:"mode"`
	Size    string `json:"size"`
}

type fileErrorResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

var fileFlashes = map[string]string{
	"created":  "Created successfully.",
	"renamed":  "Renamed successfully.",
	"deleted":  "Deleted successfully.",
	"exists":   "A file or folder with that name already exists.",
	"invalid":  "Invalid name. Names cannot be empty, \".\", \"..\" or contain \"/\".",
	"notext":   "This file is not a text file and cannot be edited.",
	"toolarge": "This file is too large to edit (limit 2 MB).",
	"notfound": "The file or folder was not found.",
	"denied":   "Permission denied.",
	"isroot":   "The root folder cannot be renamed or deleted.",
	"failed":   "The action failed. Check the server logs.",
}

var fileSuccessFlashes = map[string]bool{
	"created": true,
	"renamed": true,
	"deleted": true,
}

func (h *Handlers) FilesIndex(w http.ResponseWriter, r *http.Request) {
	requested, err := h.files.Resolve(r.URL.Query().Get("path"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, info, err := h.files.Stat(requested)
	if err != nil {
		if h.files.IsRoot(requested) {
			serverError(w, "stat files root", err)
			return
		}
		h.fileRedirect(w, r, h.files.Root(), fileErrorStatus(err))
		return
	}
	if !info.IsDir() {
		http.Redirect(w, r, fileOpenURL(p), http.StatusSeeOther)
		return
	}
	data := filesIndexData{
		Path:   p,
		Parent: h.files.Parent(p),
		Crumbs: h.files.Breadcrumbs(p),
	}
	if open := r.URL.Query().Get("open"); open != "" {
		if resolved, err := h.files.Resolve(open); err == nil {
			data.Open = resolved
		}
	}
	entries, err := h.files.List(p)
	status := r.URL.Query().Get("status")
	if err != nil {
		log.Printf("list files %s: %v", p, err)
		status = fileErrorStatus(err)
	}
	data.Entries = entries
	data.Flash = fileFlashes[status]
	data.FlashType = "danger"
	if fileSuccessFlashes[status] {
		data.FlashType = "success"
	}
	h.render(w, http.StatusOK, "files_index.html", page{
		Title:  "File Manager",
		Active: "files",
		Data:   data,
	})
}

func (h *Handlers) FilesContent(w http.ResponseWriter, r *http.Request) {
	p, info, err := h.files.Stat(r.URL.Query().Get("path"))
	if err == nil && info.IsDir() {
		err = files.ErrNotFile
	}
	var content string
	if err == nil {
		content, err = h.files.Read(p)
	}
	if err != nil {
		writeFileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fileContentResponse{
		OK:      true,
		Path:    p,
		Name:    filepath.Base(p),
		Content: content,
		Mode:    info.Mode().String(),
		Size:    files.FormatSize(info.Size()),
	})
}

func (h *Handlers) FilesDownload(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	f, info, err := h.files.Open(p)
	if errors.Is(err, files.ErrOutsideRoot) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("download %s: %v", p, err)
		h.fileRedirect(w, r, filepath.Dir(p), fileErrorStatus(err))
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func (h *Handlers) FilesCreate(w http.ResponseWriter, r *http.Request) {
	dir := r.PostFormValue("dir")
	name := r.PostFormValue("name")
	var target string
	var err error
	if r.PostFormValue("type") == "folder" {
		target, err = h.files.CreateDir(dir, name)
	} else {
		target, err = h.files.CreateFile(dir, name)
		if err == nil {
			http.Redirect(w, r, fileOpenURL(target)+"&status=created", http.StatusSeeOther)
			return
		}
	}
	if err != nil {
		log.Printf("create %s in %s: %v", name, dir, err)
		h.fileRedirect(w, r, dir, fileErrorStatus(err))
		return
	}
	h.fileRedirect(w, r, dir, "created")
}

func (h *Handlers) FilesRename(w http.ResponseWriter, r *http.Request) {
	p := r.PostFormValue("path")
	name := r.PostFormValue("name")
	target, err := h.files.Rename(p, name)
	if err != nil {
		log.Printf("rename %s to %s: %v", p, name, err)
		h.fileRedirect(w, r, filepath.Dir(p), fileErrorStatus(err))
		return
	}
	h.fileRedirect(w, r, filepath.Dir(target), "renamed")
}

func (h *Handlers) FilesDelete(w http.ResponseWriter, r *http.Request) {
	p := r.PostFormValue("path")
	if err := h.files.Delete(p); err != nil {
		log.Printf("delete %s: %v", p, err)
		h.fileRedirect(w, r, filepath.Dir(p), fileErrorStatus(err))
		return
	}
	h.fileRedirect(w, r, filepath.Dir(p), "deleted")
}

func (h *Handlers) FilesSave(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, files.MaxEditSize*4)
	p := r.PostFormValue("path")
	if err := h.files.Write(p, r.PostFormValue("content")); err != nil {
		log.Printf("save %s: %v", p, err)
		writeFileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fileErrorResponse{OK: true})
}

func (h *Handlers) fileRedirect(w http.ResponseWriter, r *http.Request, dir, status string) {
	resolved, err := h.files.Resolve(dir)
	if err != nil {
		resolved = h.files.Root()
	}
	values := url.Values{"path": {resolved}}
	if status != "" {
		values.Set("status", status)
	}
	http.Redirect(w, r, "/files?"+values.Encode(), http.StatusSeeOther)
}

func fileOpenURL(p string) string {
	return "/files?" + url.Values{"path": {filepath.Dir(p)}, "open": {p}}.Encode()
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeFileError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, files.ErrOutsideRoot), errors.Is(err, fs.ErrNotExist):
		status = http.StatusNotFound
	case errors.Is(err, fs.ErrPermission):
		status = http.StatusForbidden
	case errors.Is(err, files.ErrTooLarge), errors.Is(err, files.ErrNotFile), errors.Is(err, files.ErrNotText):
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, fileErrorResponse{OK: false, Error: fileFlashes[fileErrorStatus(err)]})
}

func fileErrorStatus(err error) string {
	switch {
	case errors.Is(err, files.ErrExists):
		return "exists"
	case errors.Is(err, files.ErrInvalidName):
		return "invalid"
	case errors.Is(err, files.ErrNotText), errors.Is(err, files.ErrNotFile):
		return "notext"
	case errors.Is(err, files.ErrTooLarge):
		return "toolarge"
	case errors.Is(err, files.ErrIsRoot):
		return "isroot"
	case errors.Is(err, files.ErrOutsideRoot), errors.Is(err, fs.ErrNotExist):
		return "notfound"
	case errors.Is(err, fs.ErrPermission):
		return "denied"
	default:
		return "failed"
	}
}
