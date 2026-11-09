package hostd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"

	"concierge/engine/internal/hostsettings"
	"concierge/engine/internal/secrets"
)

// Retention is the daemons' retention, owned here. The JSON contract lives in package hostsettings.
type Retention = hostsettings.Retention

// CallsKeep is the Voice keep switches, owned here. The JSON contract lives in package hostsettings.
type CallsKeep = hostsettings.CallsKeep

// Voice is the model for calls, owned here. The JSON contract lives in package hostsettings.
type Voice = hostsettings.Voice

var ErrVoice = errors.New("voice model must be one of the listed models")

var ErrDebugDays = errors.New("debug retention must be 7, 30 or 90 days")

var allowedDebugDays = map[int]bool{7: true, 30: true, 90: true}

var ErrRetention = errors.New("retention must be 0 (forever), 30, 90, 365 or 1095 days")

var allowedDays = map[int]bool{0: true, 30: true, 90: true, 365: true, 1095: true}

// Settings is hostd's own store of the box's settings (settings.db in its state directory). The
// card's number is kept in secretd.
type Settings struct {
	db      *sql.DB
	secrets *secrets.Client
}

// OpenSettings opens settings.db; the card's number is kept through sec.
func OpenSettings(path string, sec *secrets.Client) (*Settings, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_pragma=secure_delete(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Settings{db: db, secrets: sec}, nil
}

// migrate brings settings.db to version 5. Version 2: a connector has a state, setting_up or added;
// version 1 listed added connectors only, before setup was verified, and those count as verified (the
// owner's ruling). Version 3: the owner's name moves from the Voice settings into Account, split at
// the last space into first and last name. Version 4: the card number's old sealed row is deleted
// (the number lives in secretd). Version 5: the chosen agent (`agent`) becomes the primary
// (`agent_primary`): kept only while it is added, else the first added agent, else none.
func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > 5 {
		return fmt.Errorf("settings.db is version %d, newer than this hostd", v)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	switch v {
	case 0:
		_, err = tx.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
			CREATE TABLE connectors (name TEXT PRIMARY KEY, state TEXT NOT NULL CHECK (state IN ('setting_up', 'added')));`)
	case 1:
		_, err = tx.Exec(`ALTER TABLE connectors ADD COLUMN state TEXT NOT NULL DEFAULT 'added' CHECK (state IN ('setting_up', 'added'));`)
	}
	if err == nil && v < 3 {
		err = voiceNameToAccount(tx)
	}
	if err == nil && v < 4 {
		_, err = tx.Exec(`DELETE FROM settings WHERE key = 'card_number_sealed'; PRAGMA user_version = 4`)
	}
	if err == nil && v < 5 {
		_, err = tx.Exec(`UPDATE settings SET key = 'agent_primary' WHERE key = 'agent';
			INSERT INTO settings (key, value) VALUES ('agent_primary', '') ON CONFLICT DO NOTHING;
			UPDATE settings SET value = '' WHERE key = 'agent_primary'
				AND value NOT IN (SELECT name FROM connectors WHERE state = 'added');
			UPDATE settings SET value = COALESCE((SELECT name FROM connectors
				WHERE state = 'added' AND name IN ('claude', 'codex') ORDER BY name LIMIT 1), '')
				WHERE key = 'agent_primary' AND value = '';
			PRAGMA user_version = 5`)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// voiceNameToAccount moves voice_name and voice_name_ru into the Account rows and deletes them.
func voiceNameToAccount(tx *sql.Tx) error {
	var name string
	switch err := tx.QueryRow(`SELECT value FROM settings WHERE key = 'voice_name'`).Scan(&name); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	}
	first, last := name, ""
	if i := strings.LastIndex(name, " "); i >= 0 {
		first, last = strings.TrimSpace(name[:i]), strings.TrimSpace(name[i+1:])
	}
	_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('account_first_name', ?), ('account_last_name', ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, first, last)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO settings (key, value) SELECT 'account_name_ru', value FROM settings WHERE key = 'voice_name_ru'
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM settings WHERE key IN ('voice_name', 'voice_name_ru')`)
	return err
}

func (s *Settings) Close() error { return s.db.Close() }

// defaultRetention is a year of each until the owner chooses.
var defaultRetention = Retention{SMSDays: 365, CallsDays: 365, EmailDays: 365}

func (s *Settings) Retention(ctx context.Context) (Retention, error) {
	r := defaultRetention
	for key, into := range map[string]*int{"sms_retention_days": &r.SMSDays, "calls_retention_days": &r.CallsDays, "email_retention_days": &r.EmailDays} {
		var v string
		switch err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return Retention{}, err
		default:
			n, err := strconv.Atoi(v)
			if err != nil {
				return Retention{}, err
			}
			*into = n
		}
	}
	return r, nil
}

func (s *Settings) SetRetention(ctx context.Context, r Retention) error {
	if !allowedDays[r.SMSDays] || !allowedDays[r.CallsDays] || !allowedDays[r.EmailDays] {
		return ErrRetention
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, v := range map[string]int{"sms_retention_days": r.SMSDays, "calls_retention_days": r.CallsDays, "email_retention_days": r.EmailDays} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, strconv.Itoa(v)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// defaultCallsKeep keeps nothing; the debug days apply once the owner turns a switch on.
var defaultCallsKeep = CallsKeep{DebugDays: 30}

func (s *Settings) CallsKeep(ctx context.Context) (CallsKeep, error) {
	k := defaultCallsKeep
	for key, set := range map[string]func(string) error{
		"calls_keep_transcript": func(v string) error { k.Transcript = v == "1"; return nil },
		"calls_keep_audio":      func(v string) error { k.Audio = v == "1"; return nil },
		"calls_debug_days": func(v string) (err error) {
			k.DebugDays, err = strconv.Atoi(v)
			return err
		},
	} {
		var v string
		switch err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return CallsKeep{}, err
		default:
			if err := set(v); err != nil {
				return CallsKeep{}, err
			}
		}
	}
	return k, nil
}

func (s *Settings) SetCallsKeep(ctx context.Context, k CallsKeep) error {
	if !allowedDebugDays[k.DebugDays] {
		return ErrDebugDays
	}
	flag := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, v := range map[string]string{"calls_keep_transcript": flag(k.Transcript), "calls_keep_audio": flag(k.Audio), "calls_debug_days": strconv.Itoa(k.DebugDays)} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Settings) Voice(ctx context.Context) (Voice, error) {
	v := Voice{Model: hostsettings.VoiceModels[0]}
	switch err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'voice_model'`).Scan(&v.Model); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return Voice{}, err
	}
	return v, nil
}

func (s *Settings) SetVoice(ctx context.Context, v Voice) error {
	if !slices.Contains(hostsettings.VoiceModels, v.Model) {
		return ErrVoice
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('voice_model', ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, v.Model)
	return err
}

// States is each listed connector's state, setting_up or added (hostd runs exactly these); a
// connector not listed is not added.
func (s *Settings) States(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, state FROM connectors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, st string
		if err := rows.Scan(&n, &st); err != nil {
			return nil, err
		}
		out[n] = st
	}
	return out, rows.Err()
}

// Begin lists name as setting up; one already listed keeps its state.
func (s *Settings) Begin(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO connectors (name, state) VALUES (?, 'setting_up') ON CONFLICT DO NOTHING`, name)
	return err
}

// Verify marks a listed connector added; ok is false when it isn't listed. The first agent added
// becomes the primary, in the same transaction.
func (s *Settings) Verify(ctx context.Context, name string) (ok bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE connectors SET state = 'added' WHERE name = ?`, name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if isAgent(name) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('agent_primary', ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value WHERE value = ''`, name); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// Forget unlists name, in one transaction with what goes with it: when it is the primary agent the
// other agent takes over if it is added (else there is none), and when it is WhatsApp the agent's
// answering there is switched off.
func (s *Settings) Forget(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM connectors WHERE name = ?`, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE settings SET value = COALESCE((SELECT name FROM connectors
		WHERE state = 'added' AND name = ?), '') WHERE key = 'agent_primary' AND value = ?`, otherAgent(name), name); err != nil {
		return err
	}
	if name == "whatsapp" {
		if _, err := tx.ExecContext(ctx, `UPDATE settings SET value = '0' WHERE key = 'agent_answer_whatsapp'`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func isAgent(name string) bool {
	return slices.ContainsFunc(connectors, func(c connectorUnit) bool { return c.name == name && c.kind == "agent" })
}

// otherAgent is the agent that is not name ("" when name is not one of the two).
func otherAgent(name string) string {
	if !isAgent(name) {
		return ""
	}
	for _, c := range connectors {
		if c.kind == "agent" && c.name != name {
			return c.name
		}
	}
	return ""
}

// Agent is the owner's primary agent (an added agent, or none), the other one when it is added
// (the secondary, computed, never stored), and where the agent answers. Only the console reads it,
// through hostd's main socket.
type Agent struct {
	Primary   string      `json:"primary"`
	Secondary string      `json:"secondary"`
	Answer    AgentAnswer `json:"answer"`
}

type AgentAnswer struct {
	WhatsApp bool `json:"whatsapp"`
}

var ErrAgentNotAdded = errors.New("the primary must be an added agent")

func (s *Settings) Agent(ctx context.Context) (Agent, error) {
	var a Agent
	for key, set := range map[string]func(string){
		"agent_primary":         func(v string) { a.Primary = v },
		"agent_answer_whatsapp": func(v string) { a.Answer.WhatsApp = v == "1" },
	} {
		var v string
		switch err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return Agent{}, err
		default:
			set(v)
		}
	}
	if other := otherAgent(a.Primary); other != "" {
		states, err := s.States(ctx)
		if err != nil {
			return Agent{}, err
		}
		if states[other] == "added" {
			a.Secondary = other
		}
	}
	return a, nil
}

// SetAgent keeps the primary and where the agent answers; a sent Secondary is ignored. The primary
// must be an added agent, and may be empty only while no agent is added.
func (s *Settings) SetAgent(ctx context.Context, a Agent) error {
	states, err := s.States(ctx)
	if err != nil {
		return err
	}
	if a.Primary == "" {
		for _, c := range connectors {
			if c.kind == "agent" && states[c.name] == "added" {
				return ErrAgentNotAdded
			}
		}
	} else if !isAgent(a.Primary) || states[a.Primary] != "added" {
		return ErrAgentNotAdded
	}
	whatsapp := "0"
	if a.Answer.WhatsApp {
		whatsapp = "1"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, v := range map[string]string{"agent_primary": a.Primary, "agent_answer_whatsapp": whatsapp} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NewSettingsHandler is what hostd serves on its read-only settings socket — the daemons' only
// way in: GET /settings and GET /uplink (which link is up, so emaild can hold its bulk copy off
// mobile data), nothing else.
func NewSettingsHandler(s *Settings, svc *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		all := hostsettings.Settings{}
		var err error
		if all.Retention, err = s.Retention(r.Context()); err == nil {
			all.CallsKeep, err = s.CallsKeep(r.Context())
		}
		if err == nil {
			all.Voice, err = s.Voice(r.Context())
		}
		if err == nil {
			var a Account
			a, err = s.Account(r.Context())
			all.Owner = a.Owner
		}
		reply(w, "settings", http.StatusOK, all, err)
	})
	mux.HandleFunc("GET /uplink", func(w http.ResponseWriter, r *http.Request) {
		up, err := svc.Uplink(r.Context())
		reply(w, "uplink", http.StatusOK, map[string]string{"uplink": up}, err)
	})
	for _, path := range []string{"/settings", "/uplink"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
		})
	}
	return mux
}

func settingsRoutes(mux *http.ServeMux, st *Settings) {
	mux.HandleFunc("GET /settings/retention", func(w http.ResponseWriter, r *http.Request) {
		ret, err := st.Retention(r.Context())
		reply(w, "retention", http.StatusOK, ret, err)
	})
	mux.HandleFunc("PUT /settings/retention", func(w http.ResponseWriter, r *http.Request) {
		var in Retention
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			reply(w, "retention-update", 0, nil, errBadRequest("body must be {sms_retention_days, calls_retention_days, email_retention_days}"))
			return
		}
		err := st.SetRetention(r.Context(), in)
		if errors.Is(err, ErrRetention) {
			err = errBadRequest(err.Error())
		}
		reply(w, "retention-update", http.StatusNoContent, nil, err)
	})
}

func callsKeepRoutes(mux *http.ServeMux, st *Settings) {
	mux.HandleFunc("GET /settings/calls-keep", func(w http.ResponseWriter, r *http.Request) {
		k, err := st.CallsKeep(r.Context())
		reply(w, "calls-keep", http.StatusOK, k, err)
	})
	mux.HandleFunc("PUT /settings/calls-keep", func(w http.ResponseWriter, r *http.Request) {
		var in CallsKeep
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			reply(w, "calls-keep-update", 0, nil, errBadRequest("body must be {calls_keep_transcript, calls_keep_audio, calls_debug_days}"))
			return
		}
		err := st.SetCallsKeep(r.Context(), in)
		if errors.Is(err, ErrDebugDays) {
			err = errBadRequest(err.Error())
		}
		reply(w, "calls-keep-update", http.StatusNoContent, nil, err)
	})
}

func voiceRoutes(mux *http.ServeMux, st *Settings) {
	mux.HandleFunc("GET /settings/voice", func(w http.ResponseWriter, r *http.Request) {
		v, err := st.Voice(r.Context())
		reply(w, "voice", http.StatusOK, v, err)
	})
	mux.HandleFunc("PUT /settings/voice", func(w http.ResponseWriter, r *http.Request) {
		var in Voice
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			reply(w, "voice-update", 0, nil, errBadRequest("body must be {voice_model}"))
			return
		}
		err := st.SetVoice(r.Context(), in)
		if errors.Is(err, ErrVoice) {
			err = errBadRequest(err.Error())
		}
		reply(w, "voice-update", http.StatusNoContent, nil, err)
	})
}

func agentRoutes(mux *http.ServeMux, st *Settings) {
	mux.HandleFunc("GET /settings/agent", func(w http.ResponseWriter, r *http.Request) {
		a, err := st.Agent(r.Context())
		reply(w, "agent", http.StatusOK, a, err)
	})
	mux.HandleFunc("PUT /settings/agent", func(w http.ResponseWriter, r *http.Request) {
		var in Agent
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			reply(w, "agent-update", 0, nil, errBadRequest("body must be {primary, answer: {whatsapp}}"))
			return
		}
		err := st.SetAgent(r.Context(), in)
		if errors.Is(err, ErrAgentNotAdded) {
			err = errBadRequest(err.Error())
		}
		reply(w, "agent-update", http.StatusNoContent, nil, err)
	})
}
