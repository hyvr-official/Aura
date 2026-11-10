package handlers

import (
	"encoding/json"
	"net/http"

	"aura/internal/system"
)

func (h *Handlers) Dashboard(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, "dashboard.html", page{Title: "Dashboard", Active: "dashboard"})
}

func (h *Handlers) SystemUsage(w http.ResponseWriter, r *http.Request) {
	summary, err := system.ReadSummary()
	if err != nil {
		serverError(w, "read system usage", err)
		return
	}
	w.Header().Set("X-System-Usage", "1")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}
