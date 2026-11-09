package hostd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"concierge/engine/internal/hostsettings"
	"concierge/engine/internal/secrets/secretstest"
)

func openSettings(t *testing.T) (*Settings, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.db")
	s, err := OpenSettings(path, secretstest.Start(t, "hostd"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestRetentionDefaultsValidationAndReopen(t *testing.T) {
	s, path := openSettings(t)
	if r, _ := s.Retention(ctx); r != (Retention{SMSDays: 365, CallsDays: 365, EmailDays: 365}) {
		t.Fatalf("defaults %+v", r)
	}
	if err := s.SetRetention(ctx, Retention{SMSDays: 45, CallsDays: 30, EmailDays: 30}); !errors.Is(err, ErrRetention) {
		t.Fatalf("45 days: %v", err)
	}
	if err := s.SetRetention(ctx, Retention{SMSDays: 30, CallsDays: 0, EmailDays: 90}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	again, err := OpenSettings(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if r, _ := again.Retention(ctx); r != (Retention{SMSDays: 30, CallsDays: 0, EmailDays: 90}) {
		t.Fatalf("after reopen %+v", r)
	}
}

func TestRetentionDefaultsAndEmail(t *testing.T) {
	s, _ := openSettings(t)
	if r, _ := s.Retention(ctx); r.EmailDays != 365 {
		t.Fatalf("email default %+v", r)
	}
	if err := s.SetRetention(ctx, Retention{SMSDays: 30, CallsDays: 90, EmailDays: 1095}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Retention(ctx); r != (Retention{SMSDays: 30, CallsDays: 90, EmailDays: 1095}) {
		t.Fatalf("round trip %+v", r)
	}
	if err := s.SetRetention(ctx, Retention{SMSDays: 30, CallsDays: 90, EmailDays: 7}); !errors.Is(err, ErrRetention) {
		t.Fatalf("7 days of email: %v", err)
	}
}

func TestSettingsRoutesOnHostdAndTheReadOnlySocket(t *testing.T) {
	svc, _, _, _ := newService()
	st, _ := openSettings(t)
	h := NewHandler(svc, st)
	if rec := call(h, "PUT", "/settings/retention", `{"sms_retention_days":90,"calls_retention_days":1095,"email_retention_days":30}`); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "PUT", "/settings/retention", `{"sms_retention_days":7,"calls_retention_days":30,"email_retention_days":30}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("7 days: %d", rec.Code)
	}
	if rec := call(h, "GET", "/settings/retention", ""); strings.TrimSpace(rec.Body.String()) != `{"sms_retention_days":90,"calls_retention_days":1095,"email_retention_days":30}` {
		t.Fatalf("get: %s", rec.Body)
	}
	ro := NewSettingsHandler(st, svc)
	if rec := call(ro, "GET", "/settings", ""); strings.TrimSpace(rec.Body.String()) != `{"sms_retention_days":90,"calls_retention_days":1095,"email_retention_days":30,"calls_keep_transcript":false,"calls_keep_audio":false,"calls_debug_days":30,"voice_model":"gpt-realtime-2.1","owner_first_name":"","owner_last_name":"","owner_name_ru":""}` {
		t.Fatalf("read-only get: %s", rec.Body)
	}
	for _, m := range []string{"PUT", "POST", "DELETE"} {
		if rec := call(ro, m, "/settings", "{}"); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s on the read-only socket: %d", m, rec.Code)
		}
	}
	if rec := call(ro, "GET", "/network/wifi", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("the read-only socket must serve nothing but /settings: %d", rec.Code)
	}
}

func TestSettingsSocketServesUplink(t *testing.T) {
	svc, nm, _, _ := newService()
	st, _ := openSettings(t)
	nm.uplink = "mobile"
	ro := NewSettingsHandler(st, svc)
	if rec := call(ro, "GET", "/uplink", ""); strings.TrimSpace(rec.Body.String()) != `{"uplink":"mobile"}` {
		t.Fatalf("uplink: %d %s", rec.Code, rec.Body)
	}
	if rec := call(ro, "POST", "/uplink", "{}"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /uplink: %d", rec.Code)
	}
}

// The connectors added before setup was verified (a v1 settings.db) count as verified: the owner's
// ruling.
func TestSettingsMigratesV1RowsToAdded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE connectors (name TEXT PRIMARY KEY);
		INSERT INTO connectors (name) VALUES ('email'), ('sms');
		INSERT INTO settings (key, value) VALUES ('sms_retention_days', '90');
		PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenSettings(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.States(ctx)
	if err != nil || fmt.Sprint(got) != "map[email:added sms:added]" {
		t.Fatalf("%v %v", got, err)
	}
	if r, _ := s.Retention(ctx); r.SMSDays != 90 {
		t.Fatalf("settings lost: %+v", r)
	}
	var v int
	s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 5 {
		t.Fatalf("user_version %d", v)
	}
	s.Close()
	again, err := OpenSettings(path, nil) // a v3 file opens as it is
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got, _ := again.States(ctx); fmt.Sprint(got) != "map[email:added sms:added]" {
		t.Fatalf("reopen: %v", got)
	}
}

// The card number's old sealed row is deleted once when settings open; nothing else changes.
func TestSettingsDropsTheOldSealedCardRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE connectors (name TEXT PRIMARY KEY, state TEXT NOT NULL CHECK (state IN ('setting_up', 'added')));
		INSERT INTO settings (key, value) VALUES ('card_number_sealed', 'c2VhbGVk'), ('card_last4', '4242'), ('sms_retention_days', '90');
		PRAGMA user_version = 3;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenSettings(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.db.Query(`SELECT key FROM settings ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		rows.Scan(&k)
		keys = append(keys, k)
	}
	if fmt.Sprint(keys) != "[agent_primary card_last4 sms_retention_days]" {
		t.Fatalf("keys = %v", keys)
	}
	var v int
	s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 5 {
		t.Fatalf("user_version %d", v)
	}
}

func TestCallsKeepDefaultsOff(t *testing.T) {
	st, _ := openSettings(t)
	got, err := st.CallsKeep(context.Background())
	if err != nil || got != (CallsKeep{Transcript: false, Audio: false, DebugDays: 30}) {
		t.Fatalf("defaults: %+v %v", got, err)
	}
}

func TestCallsKeepRoundTrips(t *testing.T) {
	st, path := openSettings(t)
	ctx := context.Background()
	want := CallsKeep{Transcript: true, Audio: true, DebugDays: 90}
	if err := st.SetCallsKeep(ctx, want); err != nil {
		t.Fatal(err)
	}
	st.Close()
	again, err := OpenSettings(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got, err := again.CallsKeep(ctx); err != nil || got != want {
		t.Fatalf("after reopen: %+v %v", got, err)
	}
}

func TestCallsKeepRefusesOtherDays(t *testing.T) {
	st, _ := openSettings(t)
	ctx := context.Background()
	if err := st.SetCallsKeep(ctx, CallsKeep{Transcript: true, DebugDays: 15}); !errors.Is(err, ErrDebugDays) {
		t.Fatalf("15 days: %v", err)
	}
	if got, _ := st.CallsKeep(ctx); got.Transcript {
		t.Fatalf("a refused write changed the setting: %+v", got)
	}
}

func TestCallsKeepRoutes(t *testing.T) {
	svc, _, _, _ := newService()
	st, _ := openSettings(t)
	h := NewHandler(svc, st)
	if rec := call(h, "PUT", "/settings/calls-keep", `{"calls_keep_transcript":true,"calls_keep_audio":false,"calls_debug_days":7}`); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "PUT", "/settings/calls-keep", `{"calls_keep_transcript":true,"calls_keep_audio":false,"calls_debug_days":15}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("15 days: %d", rec.Code)
	}
	if rec := call(h, "PUT", "/settings/calls-keep", `{"calls_keep_transcript":true,"nope":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec := call(h, "GET", "/settings/calls-keep", ""); strings.TrimSpace(rec.Body.String()) != `{"calls_keep_transcript":true,"calls_keep_audio":false,"calls_debug_days":7}` {
		t.Fatalf("get: %s", rec.Body)
	}
}

func TestTheReadOnlySocketCarriesCallsKeep(t *testing.T) {
	svc, _, _, _ := newService()
	st, _ := openSettings(t)
	if err := st.SetCallsKeep(context.Background(), CallsKeep{Audio: true, DebugDays: 7}); err != nil {
		t.Fatal(err)
	}
	ro := NewSettingsHandler(st, svc)
	var got hostsettings.Settings
	rec := call(ro, "GET", "/settings", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.CallsKeep != (CallsKeep{Audio: true, DebugDays: 7}) || got.SMSDays != 365 {
		t.Fatalf("read-only get: %s", rec.Body)
	}
}

func TestVoiceDefaultsToTheFirstModel(t *testing.T) {
	st, _ := openSettings(t)
	got, err := st.Voice(context.Background())
	if err != nil || got != (Voice{Model: "gpt-realtime-2.1"}) {
		t.Fatalf("defaults: %+v %v", got, err)
	}
}

func TestVoiceRoundTrips(t *testing.T) {
	st, path := openSettings(t)
	ctx := context.Background()
	want := Voice{Model: "gpt-realtime-2.1-mini"}
	if err := st.SetVoice(ctx, want); err != nil {
		t.Fatal(err)
	}
	st.Close()
	again, err := OpenSettings(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got, err := again.Voice(ctx); err != nil || got != want {
		t.Fatalf("after reopen: %+v %v", got, err)
	}
}

func TestVoiceRefusesUnknownModels(t *testing.T) {
	st, _ := openSettings(t)
	ctx := context.Background()
	svc, _, _, _ := newService()
	h := NewHandler(svc, st)
	v := Voice{Model: "gpt-4o"}
	if err := st.SetVoice(ctx, v); !errors.Is(err, ErrVoice) {
		t.Fatalf("model: %v", err)
	}
	body, _ := json.Marshal(v)
	if rec := call(h, "PUT", "/settings/voice", string(body)); rec.Code != http.StatusBadRequest {
		t.Fatalf("put: %d", rec.Code)
	}
}

func TestVoiceRoutes(t *testing.T) {
	svc, _, _, _ := newService()
	st, _ := openSettings(t)
	h := NewHandler(svc, st)
	if rec := call(h, "PUT", "/settings/voice", `{"voice_model":"gpt-realtime-2"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "PUT", "/settings/voice", `{"voice_model":"gpt-realtime-2","nope":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec := call(h, "PUT", "/settings/voice", `{"voice_model":"gpt-realtime-2","voice_name":"Alex"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("the name moved to Account: %d", rec.Code)
	}
	if rec := call(h, "GET", "/settings/voice", ""); strings.TrimSpace(rec.Body.String()) != `{"voice_model":"gpt-realtime-2"}` {
		t.Fatalf("get: %s", rec.Body)
	}
}

func TestTheReadOnlySocketCarriesVoice(t *testing.T) {
	svc, _, _, _ := newService()
	st, _ := openSettings(t)
	if err := st.SetVoice(context.Background(), Voice{Model: "gpt-realtime-2"}); err != nil {
		t.Fatal(err)
	}
	var got hostsettings.Settings
	rec := call(NewSettingsHandler(st, svc), "GET", "/settings", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Voice != (Voice{Model: "gpt-realtime-2"}) {
		t.Fatalf("read-only get: %s", rec.Body)
	}
}

func TestAgentSettingsRoundTrip(t *testing.T) {
	svc, _, _, _ := newService()
	st, _ := openSettings(t)
	h := NewHandler(svc, st)
	addAgents(t, st, "claude", "codex")
	if rec := call(h, "GET", "/settings/agent", ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"primary":"claude","secondary":"codex","answer":{"whatsapp":false}}` {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	// a sent secondary is ignored
	if rec := call(h, "PUT", "/settings/agent", `{"primary":"codex","secondary":"nobody","answer":{"whatsapp":true}}`); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	want := `{"primary":"codex","secondary":"claude","answer":{"whatsapp":true}}`
	if rec := call(h, "GET", "/settings/agent", ""); strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("get: %s", rec.Body)
	}
	rec := call(h, "PUT", "/settings/agent", `{"primary":"gpt","answer":{"whatsapp":false}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "primary") {
		t.Fatalf("gpt: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "PUT", "/settings/agent", `{"primary":"claude","nope":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	if rec := call(h, "GET", "/settings/agent", ""); strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("refused puts changed it: %s", rec.Body)
	}
	if rec := call(NewSettingsHandler(st, svc), "GET", "/settings/agent", ""); rec.Code == http.StatusOK {
		t.Fatal("the read-only socket serves /settings/agent")
	}
}

// addAgents lists each name as added, in order, through Begin and Verify.
func addAgents(t *testing.T, st *Settings, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := st.Begin(ctx, n); err != nil {
			t.Fatal(err)
		}
		if ok, err := st.Verify(ctx, n); err != nil || !ok {
			t.Fatalf("verify %s: %v %v", n, ok, err)
		}
	}
}

func wantAgent(t *testing.T, st *Settings, primary, secondary string) {
	t.Helper()
	a, err := st.Agent(ctx)
	if err != nil || a.Primary != primary || a.Secondary != secondary {
		t.Fatalf("agent = %+v %v, want primary %q secondary %q", a, err, primary, secondary)
	}
}

func TestFirstVerifiedAgentBecomesPrimary(t *testing.T) {
	st, _ := openSettings(t)
	addAgents(t, st, "codex")
	wantAgent(t, st, "codex", "")
	addAgents(t, st, "claude")
	wantAgent(t, st, "codex", "claude")
}

func TestVerifyAChannelSetsNoPrimary(t *testing.T) {
	st, _ := openSettings(t)
	addAgents(t, st, "whatsapp")
	wantAgent(t, st, "", "")
}

func TestForgetPrimaryPromotesTheOther(t *testing.T) {
	st, _ := openSettings(t)
	addAgents(t, st, "claude", "codex")
	if err := st.Forget(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	wantAgent(t, st, "codex", "")
	if err := st.Forget(ctx, "codex"); err != nil {
		t.Fatal(err)
	}
	wantAgent(t, st, "", "")
}

func TestForgetPrimaryWithOtherNotAddedClears(t *testing.T) {
	st, _ := openSettings(t)
	addAgents(t, st, "claude")
	if err := st.Begin(ctx, "codex"); err != nil {
		t.Fatal(err)
	}
	if err := st.Forget(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	wantAgent(t, st, "", "")
}

func TestSetAgentRefusesAnAgentNotAdded(t *testing.T) {
	svc, _, _, _ := newService()
	st, _ := openSettings(t)
	addAgents(t, st, "claude")
	if err := st.SetAgent(ctx, Agent{Primary: "codex"}); !errors.Is(err, ErrAgentNotAdded) {
		t.Fatalf("err = %v", err)
	}
	if rec := call(NewHandler(svc, st), "PUT", "/settings/agent", `{"primary":"codex","answer":{"whatsapp":false}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
}

func TestSetAgentRefusesNoPrimaryWhileOneIsAdded(t *testing.T) {
	st, _ := openSettings(t)
	addAgents(t, st, "claude")
	if err := st.SetAgent(ctx, Agent{Primary: ""}); !errors.Is(err, ErrAgentNotAdded) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetAgentAcceptsNoPrimaryWhileNoneIsAdded(t *testing.T) {
	st, _ := openSettings(t)
	if err := st.SetAgent(ctx, Agent{Answer: AgentAnswer{WhatsApp: true}}); err != nil {
		t.Fatal(err)
	}
}

func TestSwapPrimary(t *testing.T) {
	st, _ := openSettings(t)
	addAgents(t, st, "claude", "codex")
	if err := st.SetAgent(ctx, Agent{Primary: "codex", Answer: AgentAnswer{WhatsApp: true}}); err != nil {
		t.Fatal(err)
	}
	wantAgent(t, st, "codex", "claude")
	if a, _ := st.Agent(ctx); !a.Answer.WhatsApp {
		t.Fatalf("answer lost: %+v", a)
	}
}

func TestMigrateV5MovesAgentToPrimary(t *testing.T) {
	for _, c := range []struct{ name, agent, rows, want string }{
		{"chosen and added", "codex", `('codex', 'added')`, "codex"},
		{"none chosen, claude added", "", `('claude', 'added')`, "claude"},
		{"chosen but not added", "codex", `('codex', 'setting_up'), ('claude', 'added')`, "claude"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.db")
			db, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
				CREATE TABLE connectors (name TEXT PRIMARY KEY, state TEXT NOT NULL CHECK (state IN ('setting_up', 'added')));
				INSERT INTO connectors (name, state) VALUES ` + c.rows + `;
				INSERT INTO settings (key, value) VALUES ('agent', '` + c.agent + `');
				PRAGMA user_version = 4;`); err != nil {
				t.Fatal(err)
			}
			db.Close()
			s, err := OpenSettings(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if a, err := s.Agent(ctx); err != nil || a.Primary != c.want {
				t.Fatalf("agent = %+v %v, want %q", a, err, c.want)
			}
			var n int
			s.db.QueryRow(`SELECT count(*) FROM settings WHERE key = 'agent'`).Scan(&n)
			if n != 0 {
				t.Fatal("the old agent key is still there")
			}
		})
	}
}
