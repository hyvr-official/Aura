package handlers

import (
	"log"
	"net/http"

	"aura/internal/auth"
)

type loginData struct {
	Error string
}

type setupData struct {
	Error string
}

func (h *Handlers) LoginForm(w http.ResponseWriter, r *http.Request) {
	if !h.passwords.IsSet() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if auth.IsLoggedIn(h.sessions, r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.render(w, http.StatusOK, "login.html", loginData{})
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	if !h.passwords.IsSet() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	password := r.PostFormValue("password")

	if !h.passwords.Verify(password) {
		h.render(w, http.StatusUnauthorized, "login.html", loginData{Error: "Invalid password."})
		return
	}

	if err := h.startSession(w, r); err != nil {
		log.Printf("session create: %v", err)
		http.Error(w, "Could not create session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handlers) SetupForm(w http.ResponseWriter, r *http.Request) {
	if h.passwords.IsSet() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	h.render(w, http.StatusOK, "setup.html", setupData{})
}

func (h *Handlers) SetupChecks(w http.ResponseWriter, r *http.Request) {
	if h.passwords.IsSet() {
		http.Error(w, "Setup is already complete", http.StatusForbidden)
		return
	}
	h.renderChecks(w)
}

func (h *Handlers) Setup(w http.ResponseWriter, r *http.Request) {
	if h.passwords.IsSet() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	password := r.PostFormValue("password")
	confirm := r.PostFormValue("password_confirm")

	if password == "" {
		h.render(w, http.StatusUnprocessableEntity, "setup.html", setupData{Error: "Password is required."})
		return
	}
	if password != confirm {
		h.render(w, http.StatusUnprocessableEntity, "setup.html", setupData{Error: "Passwords do not match."})
		return
	}

	created, err := h.passwords.SetInitial(password)
	if err != nil {
		http.Error(w, "Could not save password", http.StatusInternalServerError)
		return
	}
	if !created {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if err := h.startSession(w, r); err != nil {
		log.Printf("session create: %v", err)
		http.Error(w, "Could not create session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handlers) startSession(w http.ResponseWriter, r *http.Request) error {
	token, err := h.sessions.Create()
	if err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(auth.SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.CookieName); err == nil {
		h.sessions.Destroy(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
