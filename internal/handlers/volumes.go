package handlers

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"aura/internal/containers"
)

type volumeRow struct {
	containers.Volume
	FilesURL string
}

type volumesIndexData struct {
	Volumes       []volumeRow
	DockerMissing bool
	Flash         string
	FlashType     string
}

var volumeFlashes = map[string]string{
	"created": "Volume created.",
	"deleted": "Volume deleted.",
	"invalid": "Invalid volume name or driver.",
	"failed":  "Volume action failed. Check the server logs.",
}

func (h *Handlers) VolumesIndex(w http.ResponseWriter, r *http.Request) {
	data := volumesIndexData{}
	list, err := containers.ListVolumes()
	if errors.Is(err, containers.ErrDockerMissing) {
		data.DockerMissing = true
	} else if err != nil {
		serverError(w, "list volumes", err)
		return
	}
	for _, v := range list {
		row := volumeRow{Volume: v}
		if v.Mountpoint != "" {
			if resolved, err := h.files.Resolve(v.Mountpoint); err == nil {
				row.FilesURL = "/files?" + url.Values{"path": {resolved}}.Encode()
			}
		}
		data.Volumes = append(data.Volumes, row)
	}
	status := r.URL.Query().Get("status")
	data.Flash = volumeFlashes[status]
	data.FlashType = "success"
	if status == "failed" || status == "invalid" {
		data.FlashType = "danger"
	}
	h.render(w, http.StatusOK, "volumes_index.html", page{
		Title:  "Volumes",
		Active: "volumes",
		Data:   data,
	})
}

func (h *Handlers) VolumesStore(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	driver := strings.TrimSpace(r.FormValue("driver"))
	if !containers.ValidVolumeName(name) || (driver != "" && !containers.ValidVolumeName(driver)) {
		http.Redirect(w, r, "/volumes?status=invalid", http.StatusSeeOther)
		return
	}
	if err := containers.CreateVolume(name, driver); err != nil {
		log.Printf("create volume %s: %v", name, err)
		http.Redirect(w, r, "/volumes?status=failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/volumes?status=created", http.StatusSeeOther)
}

func (h *Handlers) VolumesDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !containers.ValidVolumeName(name) {
		http.NotFound(w, r)
		return
	}
	if err := containers.RemoveVolume(name); err != nil {
		log.Printf("delete volume %s: %v", name, err)
		http.Redirect(w, r, "/volumes?status=failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/volumes?status=deleted", http.StatusSeeOther)
}
