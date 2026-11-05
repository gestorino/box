package calls

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"concierge/engine/internal/hostsettings"
	"concierge/engine/internal/keyfile"
	"concierge/engine/internal/realtime"
	"concierge/engine/internal/secrets"
	"concierge/engine/internal/verify"
)

// openWait bounds opening the voice session: a dead uplink answers "call not placed" in seconds.
var openWait = 15 * time.Second

// SessionSpec is what a voice session is opened with: its standing instructions, the call's language
// (a Languages code, which the transcriber is told), the realtime model, whether the model leads the
// conversation, and the tools it may call.
type SessionSpec struct {
	Instructions, Language, Model string
	Lead                          bool
	Tools                         []realtime.Tool
}

// errNoKey refuses a call or a check while no OpenAI key is stored: the owner sets one in Voice's setup.
const errNoKey = "no OpenAI key"

// secretName is the key's name in calld's namespace in secretd.
const secretName = "openai"

// maxCallBody bounds a prompt call's body: maxPrompt runes of any script, escaped, with room to spare.
const maxCallBody = 16 << 10

// NewHandler is calld's API: the history, the owner's OpenAI key, the owner's prompt call, the setup
// check, and the remove. The engine forwards /api/v1/calls* here with /api/v1 stripped. A call runs on
// ctx, calld's own, not on its request's: the console going away mid-call never cuts it, calld
// shutting down does. A call or a check without a stored key is refused before anything is dialled.
// voice is the Voice settings and the owner's names (Account) from hostd, fresh as of now; false
// until hostd first answered.
func NewHandler(ctx context.Context, a *Archive, c *Caller, ai OpenAI, voice func() (hostsettings.Voice, bool), owner func() (hostsettings.Owner, bool)) http.Handler {
	mux := http.NewServeMux()
	// storedKey is the key as of now; false once it has answered: 409 without one.
	storedKey := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		key, ok, err := ai.Keys.Get(r.Context(), secretName)
		if err != nil {
			secretsFailed(w, "reading", err)
			return "", false
		}
		if !ok {
			writeJSON(w, http.StatusConflict, map[string]string{"error": errNoKey})
		}
		return key, ok
	}
	// The owner's OpenAI key: whether one is stored and its masked form, never the key; a key is kept
	// only once OpenAI accepted it.
	mux.HandleFunc("GET /calls/key", func(w http.ResponseWriter, r *http.Request) {
		key, ok, err := ai.Keys.Get(r.Context(), secretName)
		if err != nil {
			secretsFailed(w, "reading", err)
			return
		}
		view := struct {
			Set    bool   `json:"set"`
			Masked string `json:"masked"`
		}{Set: ok}
		if ok {
			view.Masked = keyfile.Masked(key, "sk-")
		}
		writeJSON(w, http.StatusOK, view)
	})
	mux.HandleFunc("PUT /calls/key", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Key *string `json:"key"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || in.Key == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": `body must be {"key": "..."}`})
			return
		}
		key := strings.TrimSpace(*in.Key)
		if !keyfile.WellFormed(key) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the key must be one line of letters, digits and symbols"})
			return
		}
		switch err := ai.Checker.Check(r.Context(), key); {
		case errors.Is(err, ErrKeyRefused):
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": ErrKeyRefused.Error()})
			return
		case err != nil:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": ErrOpenAIUnreachable.Error()})
			return
		}
		if err := ai.Keys.Put(r.Context(), secretName, key); err != nil {
			secretsFailed(w, "storing", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// The owner's call: the model rings the number and holds the conversation from the owner's prompt
	// on the model the owner chose, until it ends the call, the other side hangs up or the limit
	// passes. It answers when the call is over. Nothing here is logged but error types.
	mux.HandleFunc("POST /calls/call", func(w http.ResponseWriter, r *http.Request) {
		key, ok := storedKey(w, r)
		if !ok {
			return
		}
		var in struct {
			Number   string `json:"number"`
			Language string `json:"language"`
			Opening  string `json:"opening"`
			Prompt   string `json:"prompt"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCallBody))
		dec.DisallowUnknownFields()
		err := dec.Decode(&in)
		number, okNumber := verify.Number(in.Number)
		lang, okLang := languageOf(in.Language)
		prompt := strings.TrimSpace(in.Prompt)
		if err != nil || !okNumber || !okLang || !slices.Contains(Openings, in.Opening) ||
			prompt == "" || utf8.RuneCountInString(prompt) > maxPrompt {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the call needs a number, a language (en, pt, ru or es), an opening and a prompt of up to 2,000 characters"})
			return
		}
		v, known := voice()
		o, ownerKnown := owner()
		if !known || !ownerKnown {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the Voice settings aren't available yet"})
			return
		}
		if o.FirstName == "" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "set your name in Account first"})
			return
		}
		instr := promptInstructions(lang, in.Opening, prompt, o)
		open := func(ctx context.Context) (Talker, error) {
			octx, cancel := context.WithTimeout(ctx, openWait) // the dial and setup only: the session outlives it
			defer cancel()
			return ai.Open(octx, key, SessionSpec{Instructions: instr, Language: lang.Code, Model: v.Model, Lead: true, Tools: []realtime.Tool{EndCall}})
		}
		out, err := c.Converse(ctx, number, open, promptCall(lang, instr, wrapBefore, silenceWait, hangupGrace), Held{Limit: promptLimit, Prompt: prompt})
		switch {
		case errors.Is(err, ErrBusy):
			writeJSON(w, http.StatusConflict, map[string]string{"error": ErrBusy.Error()})
		case errors.Is(err, realtime.ErrRefused):
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": realtime.ErrRefused.Error()})
		case err != nil:
			slog.Error("calls: placing the prompt call", "err", fmt.Sprintf("%T", err)) // the type only: no number in the log
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "call not placed"})
		default:
			writeJSON(w, http.StatusOK, map[string]string{"outcome": out})
		}
	})
	// The setup check, in memory only like the other daemons': a voice model asks "What is A plus B?"
	// in the owner's language and calld judges the spoken answer.
	var (
		checkMu sync.Mutex
		passed  bool
	)
	mux.HandleFunc("POST /calls/check", func(w http.ResponseWriter, r *http.Request) {
		key, ok := storedKey(w, r)
		if !ok {
			return
		}
		var in struct {
			Number   string  `json:"number"`
			Language *string `json:"language"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		err := dec.Decode(&in)
		number, ok := verify.Number(in.Number)
		if err != nil || !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": verify.ErrBadNumber.Error()})
			return
		}
		lang := Languages[0]
		if in.Language != nil {
			if lang, ok = languageOf(*in.Language); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "language must be en, pt, ru or es"})
				return
			}
		}
		ch := verify.New(rand.IntN, time.Now())
		open := func(ctx context.Context) (Talker, error) {
			octx, cancel := context.WithTimeout(ctx, openWait) // the dial and setup only: the session outlives it
			defer cancel()
			model := hostsettings.VoiceModels[0]
			if v, known := voice(); known {
				model = v.Model
			}
			return ai.Open(octx, key, SessionSpec{Instructions: CheckInstructions, Language: lang.Code, Model: model})
		}
		out, err := c.Converse(ctx, number, open, spokenCheck(ch, lang, answerWait), Held{Limit: talkLimit})
		switch {
		case errors.Is(err, ErrBusy):
			writeJSON(w, http.StatusConflict, map[string]string{"error": ErrBusy.Error()})
		case errors.Is(err, realtime.ErrRefused):
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": realtime.ErrRefused.Error()})
		case err != nil:
			slog.Error("calls: placing the check call", "err", fmt.Sprintf("%T", err))
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "call not placed"})
		default:
			checkMu.Lock()
			passed = out == "passed"
			checkMu.Unlock()
			writeJSON(w, http.StatusOK, map[string]string{"outcome": out})
		}
	})
	mux.HandleFunc("GET /calls/check", func(w http.ResponseWriter, _ *http.Request) {
		checkMu.Lock()
		state := "none"
		if passed {
			state = "passed"
		}
		checkMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"state": state})
	})
	mux.HandleFunc("GET /calls", func(w http.ResponseWriter, r *http.Request) {
		limit := 500
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 5000 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be 1–5000"})
				return
			}
			limit = n
		}
		calls, err := a.Calls(r.Context(), limit)
		if err != nil {
			slog.Error("calls: request failed", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "call history unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, calls)
	})
	mux.HandleFunc("GET /calls/unread", func(w http.ResponseWriter, r *http.Request) {
		n, err := a.Unseen(r.Context())
		if err != nil {
			slog.Error("calls: request failed", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "call history unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"count": n})
	})
	mux.HandleFunc("POST /calls/seen", func(w http.ResponseWriter, r *http.Request) {
		if err := a.MarkSeen(r.Context()); err != nil {
			slog.Error("calls: request failed", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "call history unavailable"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// A call's transcript and recording. Anything but a positive integer id is a plain 404, so
	// probing reveals nothing; the recording is never logged, and never cached (see private).
	mux.HandleFunc("GET /calls/{id}/turns", func(w http.ResponseWriter, r *http.Request) {
		private(w)
		id, ok := callID(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		known, err := a.Exists(r.Context(), id)
		if err != nil {
			slog.Error("calls: request failed", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "call history unavailable"})
			return
		}
		if !known {
			http.NotFound(w, r)
			return
		}
		turns, err := a.Turns(r.Context(), id)
		if err != nil {
			slog.Error("calls: request failed", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "call history unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, turns)
	})
	mux.HandleFunc("GET /calls/{id}/audio", func(w http.ResponseWriter, r *http.Request) {
		private(w)
		id, ok := callID(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(a.AudioPath(id))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "audio/wav")
		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="call-%d.wav"`, id))
		}
		http.ServeContent(w, r, "", time.Time{}, f) // Content-Length and Range, so the player can seek
	})
	// The owner removed Voice: the key is forgotten either way; the history is kept or wiped.
	// A key secretd can't delete is 502 "secrets unavailable" and nothing changes, forced or not:
	// the owner retries rather than leave the key behind.
	mux.HandleFunc("POST /calls/remove", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DeleteData bool `json:"delete_data"`
			Force      bool `json:"force"` // every connector's remove takes it; Voice has no logout to force past
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {delete_data, force}"})
			return
		}
		if err := ai.Keys.Delete(r.Context(), secretName); err != nil {
			secretsFailed(w, "removing", err)
			return
		}
		if in.DeleteData {
			if err := a.Wipe(r.Context()); err != nil {
				slog.Error("calls: remove failed", "err", err)
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "call history unavailable"})
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// callID is the {id} of a /calls/{id}/… route: a positive integer, nothing else.
// private keeps a transcript or recording out of the browser's cache, so deleting it (or the debug
// retention running out) leaves no copy behind, and stops the browser sniffing it into another type.
func private(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func callID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// secretsFailed is secretd not doing what was asked: logged (its errors name the secret, never the
// key) and answered 502 "secrets unavailable", never as an absent key.
func secretsFailed(w http.ResponseWriter, doing string, err error) {
	slog.Error("calls: "+doing+" the OpenAI key", "err", err)
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": secrets.ErrUnavailable.Error()})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
