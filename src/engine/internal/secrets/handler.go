package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

const (
	maxNameBytes  = 200
	maxValueBytes = 16 << 10
	// A body either way is {"value": "<up to 16 KiB>"}; JSON escapes can make a 16 KiB value up to
	// six times longer. The handler reads a PUT and the client reads an answer to this limit.
	maxBodyBytes = 6*maxValueBytes + 64
)

type peerKey struct{}

type peer struct {
	uid uint32
	err error
}

// ConnContext is http.Server.ConnContext: it keeps the kernel's answer about who is on the
// connection, which is the only thing that picks a caller.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	uid, err := PeerUID(c)
	return context.WithValue(ctx, peerKey{}, peer{uid, err})
}

// NewHandler serves secrets to the callers (peer uid -> namespace). The request never names the namespace.
func NewHandler(st *Store, callers map[uint32]string) http.Handler {
	h := &handler{st: st, callers: callers}
	return http.HandlerFunc(h.serve)
}

type handler struct {
	st      *Store
	callers map[uint32]string
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request) {
	ns, ok := h.caller(r)
	if !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not a caller"})
		return
	}
	// Not a ServeMux: it would redirect a path it cleans (a//b, ../x) instead of refusing the name.
	switch path := r.URL.Path; {
	case strings.HasPrefix(path, "/secrets/"):
		name := strings.TrimPrefix(path, "/secrets/")
		switch r.Method {
		case http.MethodGet:
			h.get(w, r, ns, name)
		case http.MethodPut:
			h.put(w, r, ns, name)
		case http.MethodDelete:
			h.delete(w, r, ns, name)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// caller is the namespace of the connection's peer. No peer (served without ConnContext), a peer the
// kernel didn't name, or a uid that is no caller is refused, and logged without the name asked for.
func (h *handler) caller(r *http.Request) (string, bool) {
	p, ok := r.Context().Value(peerKey{}).(peer)
	if !ok || p.err != nil {
		slog.Warn("secrets", "op", "refused", "uid", "unknown")
		return "", false
	}
	ns, ok := h.callers[p.uid]
	if !ok {
		slog.Warn("secrets", "op", "refused", "uid", p.uid)
	}
	return ns, ok
}

func (h *handler) get(w http.ResponseWriter, r *http.Request, ns, name string) {
	if !validName(name) {
		badName(w)
		return
	}
	slog.Info("secrets", "caller", ns, "op", "read", "name", name)
	v, found, err := h.st.Get(r.Context(), ns, name)
	if err != nil {
		storeFailed(w, ns, "read", name, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such secret"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": v})
}

func (h *handler) put(w http.ResponseWriter, r *http.Request, ns, name string) {
	if !validName(name) {
		badName(w)
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	var body struct {
		Value *string `json:"value"`
	}
	if err := dec.Decode(&body); err != nil || body.Value == nil || *body.Value == "" || len(*body.Value) > maxValueBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body is {\"value\": a string of 1 to 16384 bytes}"})
		return
	}
	slog.Info("secrets", "caller", ns, "op", "write", "name", name)
	if err := h.st.Put(r.Context(), ns, name, *body.Value); err != nil {
		storeFailed(w, ns, "write", name, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request, ns, name string) {
	if !validName(name) {
		badName(w)
		return
	}
	slog.Info("secrets", "caller", ns, "op", "delete", "name", name)
	if err := h.st.Delete(r.Context(), ns, name); err != nil {
		storeFailed(w, ns, "delete", name, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validName: "/"-separated segments of [a-z0-9._-], none empty, none "." or "..", at most 200 bytes.
func validName(name string) bool {
	if name == "" || len(name) > maxNameBytes {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if !validSegment(seg) {
			return false
		}
	}
	return true
}

func validSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func badName(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad name"})
}

// storeFailed answers 500 and logs whose secret failed and how (a value that doesn't open says a lost
// or re-minted master key); never a value.
func storeFailed(w http.ResponseWriter, ns, op, name string, err error) {
	if errors.Is(err, ErrDoesNotOpen) {
		slog.Error("secrets", "caller", ns, "op", op, "name", name, "err", ErrDoesNotOpen.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": ErrDoesNotOpen.Error()})
		return
	}
	slog.Error("secrets store failed", "caller", ns, "op", op, "name", name, "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "secrets store failed"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
