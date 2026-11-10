package handlers

import (
	"encoding/json"
	"net/http"

	"aura/internal/system"
)

func (h *Handlers) Metrics(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, "metrics/index.html", page{Title: "Metrics", Active: "metrics"})
}

func (h *Handlers) MetricsStats(w http.ResponseWriter, r *http.Request) {
	sortBy := r.URL.Query().Get("sort")
	if sortBy != "memory" {
		sortBy = "cpu"
	}
	usage, err := system.Read(sortBy)
	if err != nil {
		serverError(w, "read system stats", err)
		return
	}
	w.Header().Set("X-Metrics-Stats", "1")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(usage)
}
