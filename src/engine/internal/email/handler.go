package email

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"mime"
	"net/http"
	netmail "net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-smtp"

	"concierge/engine/internal/gen/listen/v1/listenv1connect"
	"concierge/engine/internal/secrets"
	"concierge/engine/internal/verify"
)

// htmlCSP lets an email's HTML show its own styles and its images, inline or from the network (the
// owner opening an email loads them, as a mail app does), and nothing else: no scripts, no fonts or
// other remote loads; links open a new tab.
const htmlCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data: http: https:; sandbox allow-popups allow-popups-to-escape-sandbox"

var (
	errBadConnect    = errors.New(`body must be {"address", "app_password", "history": "copy"|"from_now", "copy_on_mobile"}`)
	errBadDisconnect = errors.New(`body must be {"delete_emails": true|false}`)
	errBadRemove     = errors.New(`body must be {"delete_data": true|false, "force": true|false}`)
	errBadCheckTo    = errors.New("must be an email address, like you@example.com")
	errBadSend       = errors.New(`body must be {"to": one address, "subject", "text": 1–100000 bytes}, or {"thread", "text"} for a reply`)
	errBadRead       = errors.New(`body must be {"thread"}`)
	errNoThread      = errors.New("id is required")
	errNotFound      = errors.New("not found")
)

// NewHandler is emaild's API; the engine forwards /api/v1/email/* here with /api/v1 stripped. Reads
// serve the account the box follows, or the one it last followed (its kept emails). ls serves the
// engine's listeners.
func NewHandler(s *Service, a *Archive, ls listenv1connect.ListenServiceHandler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(listenv1connect.NewListenServiceHandler(ls))
	mux.HandleFunc("GET /email/status", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "status", http.StatusOK, s.Status(), nil)
	})
	mux.HandleFunc("POST /email/connect", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Address      string `json:"address"`
			AppPassword  string `json:"app_password"`
			History      string `json:"history"`
			CopyOnMobile bool   `json:"copy_on_mobile"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		err := dec.Decode(&in)
		// Google shows an app password in groups of four; the address is case-insensitive.
		address := strings.ToLower(strings.TrimSpace(in.Address))
		password := strings.Join(strings.Fields(in.AppPassword), "")
		if err != nil || !strings.Contains(address, "@") || password == "" || in.History != "copy" && in.History != "from_now" {
			reply(w, "connect", 0, nil, errBadConnect)
			return
		}
		reply(w, "connect", http.StatusAccepted, struct{}{}, s.Connect(r.Context(), address, password, in.History, in.CopyOnMobile))
	})
	mux.HandleFunc("POST /email/disconnect", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DeleteEmails bool `json:"delete_emails"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			reply(w, "disconnect", 0, nil, errBadDisconnect)
			return
		}
		reply(w, "disconnect", http.StatusNoContent, nil, s.Disconnect(r.Context(), in.DeleteEmails))
	})
	mux.HandleFunc("POST /email/remove", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DeleteData bool `json:"delete_data"`
			Force      bool `json:"force"` // every connector's remove takes it; Email has no logout to force past
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			reply(w, "remove", 0, nil, errBadRemove)
			return
		}
		reply(w, "remove", http.StatusNoContent, nil, s.Remove(r.Context(), in.DeleteData))
	})
	mux.HandleFunc("POST /email/send", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			To      string `json:"to"`
			Subject string `json:"subject"`
			Text    string `json:"text"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
		dec.DisallowUnknownFields()
		err := dec.Decode(&in)
		to, ok := oneAddress(in.To)
		if err != nil || !ok || !okText(in.Text) ||
			len(in.Subject) > 998 || strings.ContainsAny(in.Subject, "\r\n") {
			reply(w, "send", 0, nil, errBadSend)
			return
		}
		_, err = s.Send(r.Context(), Outgoing{To: to, Subject: in.Subject, Text: in.Text})
		reply(w, "send", http.StatusAccepted, struct{}{}, err)
	})
	mux.HandleFunc("POST /email/reply", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Thread string `json:"thread"`
			Text   string `json:"text"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || in.Thread == "" || !okText(in.Text) {
			reply(w, "send", 0, nil, errBadSend)
			return
		}
		m, err := a.ReplyTarget(r.Context(), s.Status().Address, in.Thread)
		if err == nil {
			m.Text = in.Text
			_, err = s.Send(r.Context(), m)
		}
		reply(w, "send", http.StatusAccepted, struct{}{}, err)
	})
	// The setup check: an email to the address the owner typed, answered in its thread (the reply
	// reaches this mailbox, so the archive reads it).
	s.SetChecking(verify.Routes(mux, "/email", verify.Daemon{
		To: func(typed string) (string, error) {
			if to, ok := oneAddress(typed); ok {
				return to, nil
			}
			return "", errBadCheckTo
		},
		Send: func(ctx context.Context, to, text string) (string, error) {
			id, err := s.Send(ctx, Outgoing{To: to, Subject: "Concierge check", Text: text})
			if errors.Is(err, ErrState) {
				return "", fmt.Errorf("email: %w", verify.ErrNotReady)
			}
			return id, err
		},
		Replies: func(ctx context.Context, thread string, _ time.Time) ([]string, error) {
			emails, err := a.Thread(ctx, s.Status().Address, thread)
			var out []string
			for _, e := range emails {
				out = append(out, e.Text)
			}
			return out, err
		},
		IntN: rand.IntN,
		Now:  time.Now,
	}))
	mux.HandleFunc("GET /email/threads", func(w http.ResponseWriter, r *http.Request) {
		threads := []Thread{}
		var err error
		if account := s.Status().Address; account != "" {
			threads, err = a.Threads(r.Context(), account)
		}
		reply(w, "threads", http.StatusOK, threads, err)
	})
	mux.HandleFunc("GET /email/unread", func(w http.ResponseWriter, r *http.Request) {
		n, err := s.Unread(r.Context())
		reply(w, "unread", http.StatusOK, map[string]int{"count": n}, err)
	})
	mux.HandleFunc("POST /email/read", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Thread string `json:"thread"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || in.Thread == "" {
			reply(w, "read", 0, nil, errBadRead)
			return
		}
		reply(w, "read", http.StatusNoContent, nil, s.MarkRead(r.Context(), in.Thread))
	})
	mux.HandleFunc("GET /email/thread", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			reply(w, "thread", 0, nil, errNoThread)
			return
		}
		emails, err := a.Thread(r.Context(), s.Status().Address, id)
		reply(w, "thread", http.StatusOK, emails, err)
	})
	mux.HandleFunc("GET /email/emails/{id}/html", func(w http.ResponseWriter, r *http.Request) {
		raw, err := rawOf(r, s, a)
		var doc []byte
		if err == nil {
			if doc, err = HTMLDocument(raw); errors.Is(err, errNoHTML) {
				err = errNotFound
			}
		}
		if err != nil {
			reply(w, "html", 0, nil, err)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", htmlCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(doc)
	})
	mux.HandleFunc("GET /email/emails/{id}/attachments/{idx}", func(w http.ResponseWriter, r *http.Request) {
		raw, err := rawOf(r, s, a)
		idx, perr := strconv.Atoi(r.PathValue("idx"))
		if err == nil && perr != nil {
			err = errNotFound
		}
		var at Attachment
		var body []byte
		if err == nil {
			if at, body, err = OpenAttachment(raw, idx); err != nil {
				err = errNotFound
			}
		}
		if err != nil {
			reply(w, "attachment", 0, nil, err)
			return
		}
		name := at.Filename
		if name == "" {
			name = "attachment"
		}
		typ := at.ContentType
		if _, _, err := mime.ParseMediaType(typ); err != nil {
			typ = "application/octet-stream"
		}
		// Always a download, never rendered with the console's origin.
		w.Header().Set("Content-Type", typ)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
	})
	return mux
}

// rawOf is the original of email {id}, if it belongs to the account the reads serve.
func rawOf(r *http.Request, s *Service, a *Archive) ([]byte, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, errNotFound
	}
	raw, err := a.Raw(r.Context(), s.Status().Address, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return raw, err
}

// okText is a body the owner may send: not empty, at most 100 000 bytes.
func okText(t string) bool { return t != "" && len(t) <= 100000 }

// reply answers in JSON. Errors never carry a subject, body, address or password.
func reply(w http.ResponseWriter, op string, status int, body any, err error) {
	switch {
	case err == nil:
	case errors.Is(err, ErrAuth):
		status, body = http.StatusUnprocessableEntity, map[string]string{"error": ErrAuth.Error()}
	case errors.Is(err, secrets.ErrUnavailable):
		status, body = http.StatusBadGateway, map[string]string{"error": secrets.ErrUnavailable.Error()}
	case errors.Is(err, ErrState):
		status, body = http.StatusConflict, map[string]string{"error": err.Error()}
	case errors.Is(err, errNotFound):
		status, body = http.StatusNotFound, map[string]string{"error": err.Error()}
	case errors.Is(err, errBadConnect), errors.Is(err, errBadDisconnect), errors.Is(err, errBadRemove), errors.Is(err, errNoThread),
		errors.Is(err, errBadSend), errors.Is(err, errBadRead):
		status, body = http.StatusBadRequest, map[string]string{"error": err.Error()}
	case op == "send":
		// A server's refusal can quote the recipient (an SMTP 550 names it): log its code only.
		var se *smtp.SMTPError
		code := 0
		if errors.As(err, &se) {
			code = se.Code
		}
		slog.Warn("email: sending", "smtp_code", code, "err_type", fmt.Sprintf("%T", err))
		status, body = http.StatusBadGateway, map[string]string{"error": "email not sent"}
	case op == "connect":
		slog.Warn("email: connecting", "err", err)
		status, body = http.StatusBadGateway, map[string]string{"error": "could not reach the mail server"}
	default:
		slog.Error("email: request failed", "op", op, "err", err)
		status, body = http.StatusBadGateway, map[string]string{"error": "email archive unavailable"}
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

// oneAddress is s as one bare address, lowercased: no display name, no list.
func oneAddress(s string) (string, bool) {
	to := strings.ToLower(strings.TrimSpace(s))
	addr, err := netmail.ParseAddress(to)
	return to, err == nil && addr.Address == to
}
