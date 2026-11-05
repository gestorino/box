package api

import (
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// guardWrites refuses a write to /api/ unless it is JSON and, when the browser says where it came
// from, same-origin. There is no login yet, so this is what keeps a web page — one the owner opens
// on the LAN, or one the browser worker's Chrome visits on this very box — from changing anything:
// a cross-site page cannot send JSON without a CORS preflight the engine never approves.
// Non-browser clients send neither Origin nor Sec-Fetch-Site and cannot be a cross-site request.
func guardWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWrite(r.Method) && strings.HasPrefix(r.URL.Path, "/api/") && !allowedWrite(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"cross-site or non-JSON write refused"}` + "\n"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isWrite(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func allowedWrite(r *http.Request) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host
	}
	return true
}
