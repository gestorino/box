package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// Login is the console's one account (HTTP Basic, like .htaccess). Only a bcrypt hash of the
// password is held.
type Login struct {
	User         string
	PasswordHash string
}

func (l Login) Validate() error {
	if l.User == "" || l.PasswordHash == "" {
		return errors.New("console login is not configured: set the user and the password's bcrypt hash")
	}
	return nil
}

// requireLogin answers 401 to every request without the right credentials, except GET /healthz.
// A verified Authorization header is remembered (as a SHA-256), so the console's polling does not
// pay for bcrypt on every request.
func requireLogin(l Login, next http.Handler) http.Handler {
	var mu sync.Mutex
	var known [32]byte
	var haveKnown bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		header := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		mu.Lock()
		remembered := haveKnown && subtle.ConstantTimeCompare(header[:], known[:]) == 1
		mu.Unlock()
		if !remembered {
			user, pass, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(l.User)) != 1 ||
				bcrypt.CompareHashAndPassword([]byte(l.PasswordHash), []byte(pass)) != nil {
				w.Header().Set("WWW-Authenticate", `Basic realm="Concierge", charset="UTF-8"`)
				http.Error(w, "login required", http.StatusUnauthorized)
				return
			}
			mu.Lock()
			known, haveKnown = header, true
			mu.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}
