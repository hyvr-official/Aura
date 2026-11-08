package handlers

import (
	"database/sql"
	"errors"
	"log"
	"net/http"

	"aura/internal/console"
)

type consoleCommandResponse struct {
	OK      bool              `json:"ok"`
	Error   string            `json:"error,omitempty"`
	Errors  map[string]string `json:"errors,omitempty"`
	Command *console.Command  `json:"command,omitempty"`
}

func (h *Handlers) ConsoleIndex(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, "console_index.html", page{
		Title:  "Console",
		Active: "console",
	})
}

func (h *Handlers) ConsoleShell(w http.ResponseWriter, r *http.Request) {
	conn, err := shellUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("console upgrade: %v", err)
		return
	}
	defer conn.Close()
	servePTY(conn, console.Shell(), "console")
}

func (h *Handlers) ConsoleCommandsIndex(w http.ResponseWriter, r *http.Request) {
	list, err := h.commands.List()
	if err != nil {
		log.Printf("list console commands: %v", err)
		writeJSON(w, http.StatusInternalServerError, consoleCommandResponse{Error: "Failed to load saved commands."})
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) ConsoleCommandsStore(w http.ResponseWriter, r *http.Request) {
	c, ok := consoleCommandFromForm(w, r)
	if !ok {
		return
	}
	if err := h.commands.Create(&c); err != nil {
		log.Printf("create console command: %v", err)
		writeJSON(w, http.StatusInternalServerError, consoleCommandResponse{Error: "Failed to save command."})
		return
	}
	writeJSON(w, http.StatusOK, consoleCommandResponse{OK: true, Command: &c})
}

func (h *Handlers) ConsoleCommandsUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := backupID(w, r)
	if !ok {
		return
	}
	c, ok := consoleCommandFromForm(w, r)
	if !ok {
		return
	}
	c.ID = id
	if err := h.commands.Update(&c); err != nil {
		writeConsoleCommandError(w, "update", err)
		return
	}
	saved, err := h.commands.Get(id)
	if err != nil {
		writeConsoleCommandError(w, "get", err)
		return
	}
	writeJSON(w, http.StatusOK, consoleCommandResponse{OK: true, Command: &saved})
}

func (h *Handlers) ConsoleCommandsDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := backupID(w, r)
	if !ok {
		return
	}
	if err := h.commands.Delete(id); err != nil {
		writeConsoleCommandError(w, "delete", err)
		return
	}
	writeJSON(w, http.StatusOK, consoleCommandResponse{OK: true})
}

func consoleCommandFromForm(w http.ResponseWriter, r *http.Request) (console.Command, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, console.MaxCommandLength*4)
	c := console.Command{
		Name:    r.PostFormValue("name"),
		Command: r.PostFormValue("command"),
	}
	c.Normalize()
	if errs := c.Validate(); len(errs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, consoleCommandResponse{Error: "Please fix the errors below.", Errors: errs})
		return c, false
	}
	return c, true
}

func writeConsoleCommandError(w http.ResponseWriter, action string, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, consoleCommandResponse{Error: "Saved command not found."})
		return
	}
	log.Printf("%s console command: %v", action, err)
	writeJSON(w, http.StatusInternalServerError, consoleCommandResponse{Error: "Failed to " + action + " command."})
}
