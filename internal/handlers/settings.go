package handlers

import (
	"errors"
	"log"
	"net/http"
	"time"

	"aura/internal/backups"
	"aura/internal/startup"
	"aura/internal/timezone"
)

type settingsData struct {
	Report     startup.Report
	Timezone   string
	Timezones  []string
	ServerZone string
	Clock      string
	Now        time.Time
	Flash      string
	Error      string
}

var settingsFlashes = map[string]string{
	"timezone_updated": "Timezone updated.",
	"clock_updated":    "Time format updated.",
}

var settingsErrors = map[string]string{
	"timezone_invalid": "Choose a valid timezone.",
	"timezone_failed":  "Could not save the timezone. Check the server logs.",
	"clock_invalid":    "Choose a valid time format.",
	"clock_failed":     "Could not save the time format. Check the server logs.",
}

func (h *Handlers) SettingsIndex(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	h.render(w, http.StatusOK, "settings/index.html", page{
		Title:  "Settings",
		Active: "settings",
		Data: settingsData{
			Report:     startup.Last(),
			Timezone:   timezone.Name(),
			Timezones:  timezone.Options(),
			ServerZone: timezone.ServerName(),
			Clock:      timezone.Clock(),
			Now:        time.Now(),
			Flash:      settingsFlashes[status],
			Error:      settingsErrors[status],
		},
	})
}

func (h *Handlers) SettingsTimezone(w http.ResponseWriter, r *http.Request) {
	if err := h.timezones.Save(r.PostFormValue("timezone")); err != nil {
		if errors.Is(err, timezone.ErrInvalid) {
			http.Redirect(w, r, "/settings?status=timezone_invalid", http.StatusSeeOther)
			return
		}
		log.Printf("save timezone: %v", err)
		http.Redirect(w, r, "/settings?status=timezone_failed", http.StatusSeeOther)
		return
	}
	if err := backups.EnsureCron(); err != nil {
		log.Printf("backup cron: %v", err)
	}
	http.Redirect(w, r, "/settings?status=timezone_updated", http.StatusSeeOther)
}

func (h *Handlers) SettingsTimeFormat(w http.ResponseWriter, r *http.Request) {
	if err := h.timezones.SaveClock(r.PostFormValue("time_format")); err != nil {
		if errors.Is(err, timezone.ErrInvalid) {
			http.Redirect(w, r, "/settings?status=clock_invalid", http.StatusSeeOther)
			return
		}
		log.Printf("save time format: %v", err)
		http.Redirect(w, r, "/settings?status=clock_failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/settings?status=clock_updated", http.StatusSeeOther)
}

func (h *Handlers) SettingsChecks(w http.ResponseWriter, r *http.Request) {
	h.renderChecks(w)
}

func (h *Handlers) renderChecks(w http.ResponseWriter) {
	report := startup.Run()
	w.Header().Set("X-Checks-Results", "1")
	h.render(w, http.StatusOK, "checks_results", report)
}
