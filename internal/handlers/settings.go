package handlers

import (
	"net/http"

	"aura/internal/startup"
)

type settingsData struct {
	Report startup.Report
}

func (h *Handlers) SettingsIndex(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, "settings_index.html", page{
		Title:  "Settings",
		Active: "settings",
		Data:   settingsData{Report: startup.Last()},
	})
}

func (h *Handlers) SettingsChecks(w http.ResponseWriter, r *http.Request) {
	h.renderChecks(w)
}

func (h *Handlers) renderChecks(w http.ResponseWriter) {
	report := startup.Run()
	w.Header().Set("X-Checks-Results", "1")
	h.render(w, http.StatusOK, "checks_results", report)
}
