package auth

import "net/http"

func IsLoggedIn(sessions *SessionStore, r *http.Request) bool {
	cookie, err := r.Cookie(CookieName)
	return err == nil && sessions.Valid(cookie.Value)
}

func RequireAuth(sessions *SessionStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsLoggedIn(sessions, r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}
