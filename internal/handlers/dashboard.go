package handlers

import (
	"encoding/json"
	"net/http"

	"aura/internal/system"
)

func (h *Handlers) Dashboard(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, "dashboard.html", page{Title: "Dashboard", Active: "dashboard"})
}

func (h *Handlers) DashboardStats(w http.ResponseWriter, r *http.Request) {
	sortBy := r.URL.Query().Get("sort")
	if sortBy != "memory" {
		sortBy = "cpu"
	}
	usage, err := system.Read(sortBy)
	if err != nil {
		serverError(w, "read system stats", err)
		return
	}
	w.Header().Set("X-Dashboard-Stats", "1")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(usage)
}
