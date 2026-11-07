package hostd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeUnits records "start <unit>" / "stop <unit>" in order; fail[call] makes that call fail.
type fakeUnits struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error
}

func (f *fakeUnits) do(verb, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := verb + " " + unit
	f.calls = append(f.calls, call)
	return f.fail[call]
}
func (f *fakeUnits) Start(_ context.Context, unit string) error { return f.do("start", unit) }
func (f *fakeUnits) Stop(_ context.Context, unit string) error  { return f.do("stop", unit) }
func (f *fakeUnits) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// connectorRig is a service with a SIM, a settings store and recorded units.
func connectorRig(t *testing.T) (*Service, *fakeUnits, *fakeMM, *Settings) {
	t.Helper()
	st, _ := openSettings(t)
	units := &fakeUnits{fail: map[string]error{}}
	mm := &fakeMM{sim: SIMInfo{Present: true}}
	return NewService(newFakeNM(), mm, units, st, newFakeClock().now), units, mm, st
}

// added lists the connectors hostd runs (setting up or added), in order.
func added(t *testing.T, s *Service) []string {
	t.Helper()
	c, err := s.Connectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range c.Connectors {
		if x.State != "not_added" {
			out = append(out, x.Name)
		}
	}
	return out
}

func stateOf(t *testing.T, s *Service, name string) string {
	t.Helper()
	c, err := s.Connectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range c.Connectors {
		if x.Name == name {
			return x.State
		}
	}
	t.Fatalf("no %s", name)
	return ""
}

func TestConnectorsListsFiveInOrderNoneAdded(t *testing.T) {
	s, _, _, _ := connectorRig(t)
	c, err := s.Connectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Connector{{Name: "email", State: "not_added", Kind: "channel"}, {Name: "voice", State: "not_added", NeedsSIM: true, Kind: "channel"}, {Name: "sms", State: "not_added", NeedsSIM: true, Kind: "channel"}, {Name: "whatsapp", State: "not_added", Kind: "channel"}, {Name: "telegram", State: "not_added", Kind: "channel"}, {Name: "claude", State: "not_added", Kind: "agent"}, {Name: "codex", State: "not_added", Kind: "agent"}}
	if !c.SIM || !reflect.DeepEqual(c.Connectors, want) {
		t.Fatalf("%+v", c)
	}
}

func TestAddStartsSocketThenServiceAndRecords(t *testing.T) {
	s, units, _, _ := connectorRig(t)
	if err := s.AddConnector(ctx, "sms"); err != nil {
		t.Fatal(err)
	}
	if got := units.Calls(); !reflect.DeepEqual(got, []string{"start concierge-sms.socket", "start concierge-sms.service"}) {
		t.Fatalf("%v", got)
	}
	if got := added(t, s); !reflect.DeepEqual(got, []string{"sms"}) {
		t.Fatalf("%v", got)
	}
}

func TestAddVoiceOrSMSWithoutSIMIsRefused(t *testing.T) {
	for _, mm := range []*fakeMM{{sim: SIMInfo{Present: false}}, {err: errors.New("no modem")}} {
		s, units, rigMM, _ := connectorRig(t)
		*rigMM = *mm
		for _, name := range []string{"voice", "sms"} {
			if err := s.AddConnector(ctx, name); !errors.Is(err, ErrNoSIM) {
				t.Fatalf("%s: %v", name, err)
			}
		}
		if len(units.Calls()) != 0 || len(added(t, s)) != 0 {
			t.Fatalf("%v %v", units.Calls(), added(t, s))
		}
		if c, _ := s.Connectors(ctx); c.SIM {
			t.Fatal("sim reported present")
		}
		if err := s.AddConnector(ctx, "email"); err != nil { // email needs no SIM
			t.Fatal(err)
		}
	}
}

func TestAddUnknownNameIsNotFound(t *testing.T) {
	s, units, _, _ := connectorRig(t)
	for _, name := range []string{"concierge-engine", "", "SMS", "hostd"} {
		if err := s.AddConnector(ctx, name); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%q: %v", name, err)
		}
		if err := s.RemoveConnector(ctx, name); !errors.Is(err, ErrNotFound) {
			t.Fatalf("remove %q: %v", name, err)
		}
	}
	if len(units.Calls()) != 0 {
		t.Fatalf("%v", units.Calls())
	}
}

func TestAddThatFailsToStartIsNotRecorded(t *testing.T) {
	s, units, _, _ := connectorRig(t)
	units.fail["start concierge-email.service"] = errors.New("job failed")
	if err := s.AddConnector(ctx, "email"); err == nil {
		t.Fatal("no error")
	}
	if len(added(t, s)) != 0 {
		t.Fatalf("%v", added(t, s))
	}
}

func TestRemoveStopsServiceThenSocketAndForgets(t *testing.T) {
	s, units, _, _ := connectorRig(t)
	if err := s.AddConnector(ctx, "whatsapp"); err != nil {
		t.Fatal(err)
	}
	units.calls = nil
	if err := s.RemoveConnector(ctx, "whatsapp"); err != nil {
		t.Fatal(err)
	}
	if got := units.Calls(); !reflect.DeepEqual(got, []string{"stop concierge-whatsapp.service", "stop concierge-whatsapp.socket"}) {
		t.Fatalf("%v", got)
	}
	if len(added(t, s)) != 0 {
		t.Fatalf("%v", added(t, s))
	}
	if err := s.RemoveConnector(ctx, "whatsapp"); err != nil {
		t.Fatalf("again: %v", err)
	}
}

func TestReconcileStartsAddedStopsTheRest(t *testing.T) {
	s, units, _, st := connectorRig(t)
	for _, n := range []string{"sms", "email"} {
		if err := st.Begin(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := st.Verify(ctx, "sms"); !ok || err != nil { // setting up and added both run
		t.Fatal(ok, err)
	}
	s.Reconcile(ctx)
	want := []string{
		"start concierge-email.socket", "start concierge-email.service",
		"stop concierge-calls.service", "stop concierge-calls.socket",
		"start concierge-sms.socket", "start concierge-sms.service",
		"stop concierge-whatsapp.service", "stop concierge-whatsapp.socket",
		"stop concierge-telegram.service", "stop concierge-telegram.socket",
		"stop concierge-claude.service", "stop concierge-claude.socket",
		"stop concierge-codex.service", "stop concierge-codex.socket",
	}
	if got := units.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestReconcileSurvivesAFailingUnit(t *testing.T) {
	s, units, _, st := connectorRig(t)
	st.Begin(ctx, "sms")
	st.Begin(ctx, "telegram")
	units.fail["start concierge-sms.service"] = errors.New("bad binary")
	s.Reconcile(ctx)
	if got := units.Calls(); !slices.Contains(got, "start concierge-telegram.service") {
		t.Fatalf("stopped at the failure: %v", got)
	}
}

func TestConnectorRoutes(t *testing.T) {
	s, units, mm, st := connectorRig(t)
	h := NewHandler(s, st)
	rec := call(h, "GET", "/connectors", "")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), `{"sim":true,"connectors":[{"name":"email","state":"not_added","needs_sim":false,"kind":"channel"}`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/connectors/sms", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "DELETE", "/connectors/sms", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body)
	}
	units.fail["start concierge-email.socket"] = errors.New("org.freedesktop.systemd1 said no")
	if rec := call(h, "POST", "/connectors/email", ""); rec.Code != http.StatusBadGateway || strings.TrimSpace(rec.Body.String()) != `{"error":"systemd request failed"}` {
		t.Fatalf("unit failure: %d %s", rec.Code, rec.Body)
	}
	mm.sim.Present = false
	if rec := call(h, "POST", "/connectors/voice", ""); rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"needs a SIM — none in the box"}` {
		t.Fatalf("no sim: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "DELETE", "/connectors/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", rec.Code)
	}
}

func TestConnectorsSurviveReopen(t *testing.T) {
	st, path := openSettings(t)
	if err := st.Begin(ctx, "telegram"); err != nil {
		t.Fatal(err)
	}
	st.Begin(ctx, "sms")
	st.Verify(ctx, "sms")
	st.Close()
	again, err := OpenSettings(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	got, err := again.States(ctx)
	if err != nil || fmt.Sprint(got) != "map[sms:added telegram:setting_up]" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAddIsSettingUpUntilVerified(t *testing.T) {
	s, units, _, _ := connectorRig(t)
	if err := s.AddConnector(ctx, "sms"); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, s, "sms"); got != "setting_up" {
		t.Fatal(got)
	}
	if len(units.Calls()) != 2 {
		t.Fatalf("not started: %v", units.Calls())
	}
	if err := s.VerifyConnector(ctx, "sms"); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, s, "sms"); got != "added" {
		t.Fatal(got)
	}
}

func TestAddKeepsAnAddedConnectorAdded(t *testing.T) {
	s, _, _, _ := connectorRig(t)
	s.AddConnector(ctx, "email")
	s.VerifyConnector(ctx, "email")
	if err := s.AddConnector(ctx, "email"); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, s, "email"); got != "added" {
		t.Fatal(got)
	}
}

func TestVerifyNeedsSettingUp(t *testing.T) {
	s, _, _, _ := connectorRig(t)
	if err := s.VerifyConnector(ctx, "sms"); !errors.Is(err, ErrNotSettingUp) {
		t.Fatal(err)
	}
	if got := stateOf(t, s, "sms"); got != "not_added" {
		t.Fatal(got)
	}
	if err := s.VerifyConnector(ctx, "fax"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

// Cancel racing a confirm in flight: the verify that arrives after the remove brings nothing back.
func TestRemoveThenVerifyStaysRemoved(t *testing.T) {
	s, _, _, _ := connectorRig(t)
	s.AddConnector(ctx, "whatsapp")
	s.RemoveConnector(ctx, "whatsapp")
	if err := s.VerifyConnector(ctx, "whatsapp"); !errors.Is(err, ErrNotSettingUp) {
		t.Fatal(err)
	}
	if got := stateOf(t, s, "whatsapp"); got != "not_added" {
		t.Fatal(got)
	}
}

func TestVerifiedRoute(t *testing.T) {
	s, _, _, st := connectorRig(t)
	h := NewHandler(s, st)
	if rec := call(h, "POST", "/connectors/sms/verified", ""); rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"error":"not being set up"}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "POST", "/connectors/sms", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("add: %d", rec.Code)
	}
	if rec := call(h, "POST", "/connectors/sms/verified", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "GET", "/connectors", ""); !strings.Contains(rec.Body.String(), `{"name":"sms","state":"added","needs_sim":true,"kind":"channel"}`) {
		t.Fatal(rec.Body)
	}
	if rec := call(h, "POST", "/connectors/fax/verified", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", rec.Code)
	}
}

func TestAgentsAreUnitsOfTheirOwnKind(t *testing.T) {
	s, units, _, _ := connectorRig(t)
	c, err := s.Connectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, x := range c.Connectors {
		kinds[x.Name] = x.Kind
		if x.Kind == "agent" && x.NeedsSIM {
			t.Fatalf("%s needs a SIM", x.Name)
		}
	}
	want := map[string]string{"email": "channel", "voice": "channel", "sms": "channel", "whatsapp": "channel", "telegram": "channel", "claude": "agent", "codex": "agent"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("%v", kinds)
	}
	if err := s.AddConnector(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	if got := units.Calls(); !reflect.DeepEqual(got, []string{"start concierge-claude.socket", "start concierge-claude.service"}) {
		t.Fatalf("%v", got)
	}
	if stateOf(t, s, "claude") != "setting_up" {
		t.Fatal(stateOf(t, s, "claude"))
	}
	if err := s.VerifyConnector(ctx, "claude"); err != nil || stateOf(t, s, "claude") != "added" {
		t.Fatal(err)
	}
	if err := s.RemoveConnector(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	if got := units.Calls(); got[len(got)-1] != "stop concierge-claude.socket" || stateOf(t, s, "claude") != "not_added" {
		t.Fatalf("%v", got)
	}
}

func TestTheChosenAgentIsClearedWhenItsUnitIsRemoved(t *testing.T) {
	s, _, _, st := connectorRig(t)
	if err := s.AddConnector(ctx, "codex"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgent(ctx, Agent{Agent: "codex", Answer: AgentAnswer{WhatsApp: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveConnector(ctx, "claude"); err != nil { // another agent: the choice stays
		t.Fatal(err)
	}
	if a, _ := st.Agent(ctx); a.Agent != "codex" {
		t.Fatalf("%+v", a)
	}
	if err := s.RemoveConnector(ctx, "codex"); err != nil {
		t.Fatal(err)
	}
	a, err := st.Agent(ctx)
	if err != nil || a.Agent != "" || !a.Answer.WhatsApp {
		t.Fatalf("%+v %v", a, err)
	}
}
