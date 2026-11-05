package signins

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"concierge/engine/internal/browser"
	"concierge/engine/internal/secrets"
)

// registers plays a registration's errand: the worker asks for nothing and reports success.
func (f *fixture) register(t *testing.T) int64 {
	t.Helper()
	id, err := f.svc.Register(context.Background(), shopKey)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	return id
}

func TestRegisterRunsAnErrandThatCreatesAnAccount(t *testing.T) {
	f := newFixture(t)
	id := f.register(t)
	body := f.worker.sent()[0]
	values, _ := body["values"].(map[string]string)
	if body["url"] != "https://www.shop.test/start" || body["action"] != "create an account" || body["kind"] != "register" ||
		body["session"] != shopKey || body["site"] != shopKey {
		t.Fatalf("body = %v", body)
	}
	if values["email"] != "owner+shop@example.com" || values["first_name"] != "Alex" ||
		values["last_name"] != "Sample" || values["full_name"] != "Alex Sample" ||
		values["phone"] != "+351912345678" || len(values) != 6 {
		t.Fatalf("values = %v", values)
	}
	a, has := f.account(t)
	if !has || a.State != "registered" || a.Run != id || a.Email != values["email"] {
		t.Fatalf("account = %+v %v", a, has)
	}
	if pw, ok := f.password(t); !ok || pw != values["secret:password"] {
		t.Fatal("the password in secretd is not the one the errand filled")
	}
	d, _, _ := f.svc.AgentRun(context.Background(), id)
	if d.Kind != "register" || d.State != "ended" || d.Reason != "registered" || d.Action != "create an account" {
		t.Fatalf("errand = %+v", d.AgentRunView)
	}
	if n, _ := f.store.Registrations(context.Background(), shopKey); n != 1 {
		t.Fatalf("journaled %d", n)
	}
}

func TestRegisterRefusesASiteWithoutACardOrWithAnAccount(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.Register(context.Background(), "nowhere.test"); !errors.Is(err, ErrUnknownSite) {
		t.Fatalf("no card: %v", err)
	}
	f.signedUp(t, "registered")
	if _, err := f.svc.Register(context.Background(), shopKey); !errors.Is(err, ErrAccountExists) {
		t.Fatalf("an account: %v", err)
	}
}

func TestRegisterSaysWhatTheBoxLacks(t *testing.T) {
	f := newFixture(t)
	f.svc.cfg.Mail = fakeMail{}
	f.host.phone, f.host.last = "", ""
	var notReady *NotReadyError
	if _, err := f.svc.Register(context.Background(), shopKey); !errors.As(err, &notReady) ||
		!reflect.DeepEqual(notReady.Missing, []string{"email", "phone", "names"}) {
		t.Fatalf("err = %v", err)
	}
	if _, has := f.account(t); has {
		t.Fatal("a refused registration wrote an account")
	}
}

// The password the box generates lives in secretd only: the accounts table has no column for one, and an errand
// on the account fills the one secretd keeps.
func TestRegistrationKeepsThePasswordInSecrets(t *testing.T) {
	f := newFixture(t)
	f.register(t)
	filled, _ := f.worker.sent()[0]["values"].(map[string]string)
	if pw, ok := f.password(t); !ok || pw == "" || pw != filled["secret:password"] {
		t.Fatal("secretd does not keep the password the registration filled")
	}
	var has int
	if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('accounts') WHERE name = 'password'`).Scan(&has); err != nil || has != 0 {
		t.Fatalf("the accounts table has a password column (%d, %v)", has, err)
	}
	if _, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/x", Action: "do it"}); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	sent := f.worker.sent()
	if got, _ := sent[len(sent)-1]["values"].(map[string]string); got["secret:password"] != filled["secret:password"] {
		t.Fatal("the errand did not fill the password secretd keeps")
	}
}

// A password secretd can't keep is no registration: nothing is written, nothing runs.
func TestARegistrationWhosePasswordCannotBeKeptDoesNotStart(t *testing.T) {
	f := newFixture(t)
	f.svc.cfg.HasKey = func(context.Context) (bool, error) { return true, nil }
	f.secretdDown(t)
	if _, err := f.svc.Register(context.Background(), shopKey); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	f.svc.runs.Wait()
	if _, has := f.account(t); has || len(f.worker.sent()) != 0 {
		t.Fatalf("account %v, runs %d", has, len(f.worker.sent()))
	}
	if f.svc.phase(shopKey) != "" {
		t.Fatal("the site is still held")
	}
}

func TestASecondRegisterWhileOneRunsIsBusy(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	f.worker.onAgent = func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
		<-release
		return browser.Message{"outcome": "done", "success": true, "report": "made"}, nil
	}
	if _, err := f.svc.Register(context.Background(), shopKey); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Register(context.Background(), shopKey); !errors.Is(err, ErrBusy) {
		t.Fatalf("second: %v", err)
	}
	if _, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/", Action: "do it"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("an errand meanwhile: %v", err)
	}
	close(release)
	f.svc.runs.Wait()
	if n, _ := f.store.Registrations(context.Background(), shopKey); n != 1 {
		t.Fatalf("journaled %d", n)
	}
}

func TestAFailedRegistrationLeavesNoAccountAndNoProfile(t *testing.T) {
	f := newFixture(t)
	f.worker.onAgent = func(_ context.Context, body browser.Message, _ browser.Ask) (browser.Message, error) {
		return browser.Message{"outcome": "done", "success": false, "report": "the site wants a card"}, nil
	}
	id := f.register(t)
	if _, has := f.account(t); has {
		t.Fatal("a failed registration kept its account")
	}
	if _, has := f.password(t); has {
		t.Fatal("a failed registration kept its password in secretd")
	}
	// Forgotten before the run, and again after it failed.
	if got := f.worker.types(); !reflect.DeepEqual(got, []string{"forget_profile", "run_agent", "forget_profile"}) {
		t.Fatalf("the worker was asked %v", got)
	}
	if d, _, _ := f.svc.AgentRun(context.Background(), id); d.Reason != "not registered: the site wants a card" || d.Report != "the site wants a card" {
		t.Fatalf("errand = %s %q", d.State, d.Reason)
	}
}

func TestAnInterruptedRegistrationIsWarnAndStartsOverNextTime(t *testing.T) {
	f := newFixture(t)
	f.worker.onAgent = asksQuestion(make(chan answer, 1))
	id, err := f.svc.Register(context.Background(), shopKey)
	if err != nil {
		t.Fatal(err)
	}
	soon(t, "the question is held", func() bool { return f.runDetail(t, id).Question != nil })
	if err := f.svc.Stop(id); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	a, has := f.account(t)
	if !has || a.State != "warn" || a.Reason != "the registration was interrupted" || a.Run != id {
		t.Fatalf("account = %+v %v", a, has)
	}
	// The interrupted attempt keeps its profile: only the one before its run was forgotten.
	if got := f.worker.types(); !reflect.DeepEqual(got, []string{"forget_profile", "run_agent"}) {
		t.Fatalf("the worker was asked %v", got)
	}
	first := a.Email

	f.worker.onAgent = func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
		if got := f.worker.types(); got[len(got)-2] != "forget_profile" {
			return nil, errors.New("started inside the interrupted attempt's profile")
		}
		return browser.Message{"outcome": "done", "success": true, "report": "made"}, nil
	}
	f.register(t)
	a, _ = f.account(t)
	if a.State != "registered" || a.Email == first || a.Email != "owner+shop2@example.com" {
		t.Fatalf("account = %+v (first %s)", a, first)
	}
}

func TestTheSMSCodeIsOneThatArrivedAfterTheRunStarted(t *testing.T) {
	f := newFixture(t)
	f.sms.Add(smsFrom("Shop", "Your code 111111"))
	got := make(chan answer, 1)
	f.worker.onAgent = func(ctx context.Context, _ browser.Message, ask browser.Ask) (browser.Message, error) {
		go func() {
			time.Sleep(50 * time.Millisecond)
			f.sms.Add(smsFrom("Shop", "Your code 482913"))
		}()
		v, ok := ask(ctx, browser.AskRequest{Name: "sms_code", Why: "the code"})
		got <- answer{v, ok}
		return browser.Message{"outcome": "done", "success": true}, nil
	}
	f.svc.codes = 5 * time.Second
	reg := f.register(t)
	if a := <-got; a != (answer{"482913", true}) {
		t.Fatalf("code = %+v", a)
	}
	if log := f.runTextOf(t, reg); !strings.Contains(log, "box: Waiting for the SMS code") || !strings.Contains(log, "box: The SMS code arrived") ||
		strings.Contains(log, "482913") {
		t.Fatalf("log:\n%s", log)
	}
}

func TestNoSMSCodeIsATimeout(t *testing.T) {
	f := newFixture(t)
	got := make(chan answer, 1)
	f.worker.onAgent = func(ctx context.Context, _ browser.Message, ask browser.Ask) (browser.Message, error) {
		v, ok := ask(ctx, browser.AskRequest{Name: "sms_code", Why: "the code"})
		got <- answer{v, ok}
		return browser.Message{"outcome": "done", "success": false}, nil
	}
	f.register(t)
	if a := <-got; a.ok {
		t.Fatalf("code = %+v", a)
	}
	if log := f.runText(t); !strings.Contains(log, "box: No SMS code came") {
		t.Fatalf("log:\n%s", log)
	}
}

func TestEachEmailAskIsTheNextEmailToTheAccountAddressSinceTheRunStarted(t *testing.T) {
	f := newFixture(t)
	f.svc.cfg.Now = time.Now
	to := "owner+shop@example.com"
	f.inbox.Add(emailTo(to, "old@shop.test", "Before the run", "<p>1111</p>"))
	f.inbox.Add(emailTo("someone-else@gmail.com", "x@shop.test", "Not ours", "<p>2222</p>"))
	got := make(chan string, 2)
	f.worker.onAgent = func(ctx context.Context, _ browser.Message, ask browser.Ask) (browser.Message, error) {
		f.inbox.Add(emailTo(to, "hi@shop.test", "Welcome", "<p>Hello</p>"))
		go func() {
			time.Sleep(50 * time.Millisecond)
			f.inbox.Add(emailTo(to, "hi@shop.test", "Confirm", `<a href="https://shop.test/c?t=s3cr3t">Confirm</a>`))
		}()
		for range 2 {
			v, ok := ask(ctx, browser.AskRequest{Name: "email", Why: "the email"})
			if !ok {
				return nil, errors.New("no email")
			}
			got <- v
		}
		return browser.Message{"outcome": "done", "success": true}, nil
	}
	f.svc.codes = 5 * time.Second
	time.Sleep(10 * time.Millisecond) // the run starts after the old email was stored
	reg := f.register(t)
	for _, want := range []string{"Welcome", "Confirm"} {
		var mail struct{ Subject, Text, HTML string }
		raw := <-got
		if err := json.Unmarshal([]byte(raw), &mail); err != nil || mail.Subject != want {
			t.Fatalf("email = %s, %v; want %s", raw, err, want)
		}
	}
	if log := f.runTextOf(t, reg); strings.Contains(log, "s3cr3t") || !strings.Contains(log, "box: An email arrived") {
		t.Fatalf("log:\n%s", log)
	}
}

func TestAnErrandOnAnAccountGetsItsCodesToo(t *testing.T) {
	f := newFixture(t)
	f.signedUp(t, "registered")
	got := make(chan answer, 1)
	f.worker.onAgent = func(ctx context.Context, _ browser.Message, ask browser.Ask) (browser.Message, error) {
		go f.sms.Add(smsFrom("Shop", "code 7788"))
		v, ok := ask(ctx, browser.AskRequest{Name: "sms_code", Why: "the code"})
		got <- answer{v, ok}
		return browser.Message{"outcome": "done", "success": true}, nil
	}
	f.svc.codes = 5 * time.Second
	if _, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/", Action: "do it"}); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	if a := <-got; a != (answer{"7788", true}) {
		t.Fatalf("code = %+v", a)
	}
}

func TestAnErrandWithoutAnAccountGetsNoCodes(t *testing.T) {
	f := newFixture(t)
	got := make(chan answer, 1)
	f.worker.onAgent = func(ctx context.Context, _ browser.Message, ask browser.Ask) (browser.Message, error) {
		v, ok := ask(ctx, browser.AskRequest{Name: "sms_code", Why: "the code"})
		got <- answer{v, ok}
		return browser.Message{"outcome": "done", "success": true}, nil
	}
	if _, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/", Action: "do it"}); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	if a := <-got; a.ok {
		t.Fatalf("code = %+v", a)
	}
}

func TestTheNewPasswordFollowsTheRecipe(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		p := newPassword()
		if len(p) != 16 || !strings.ContainsAny(p, "abcdefghijklmnopqrstuvwxyz") || !strings.ContainsAny(p, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") ||
			!strings.ContainsAny(p, "0123456789") || strings.Count(p, "!") != 1 || strings.Trim(p, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!") != "" {
			t.Fatalf("password %q", p)
		}
		seen[p] = true
	}
	if len(seen) != 1000 {
		t.Fatalf("%d distinct of 1000", len(seen))
	}
}

func TestRegisterOverHTTPAnswersTheRun(t *testing.T) {
	f := newHandlerFixture(t)
	if rec := f.do("POST", "/api/v1/accounts/suggest", `{"link":"ftp://x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("a bad link: %d %s", rec.Code, rec.Body)
	}
	rec := f.do("POST", "/api/v1/accounts/shop.test/register", ``)
	var started struct{ Run int64 }
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &started) != nil || started.Run == 0 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("POST", "/api/v1/accounts/shop.test/register", ``); rec.Code != http.StatusConflict {
		t.Fatalf("again: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("POST", "/api/v1/accounts/nowhere.test/register", ``); rec.Code != http.StatusNotFound {
		t.Fatalf("no card: %d %s", rec.Code, rec.Body)
	}
	f.host.phone = ""
	if err := f.store.DeleteAccount(context.Background(), shopKey); err != nil {
		t.Fatal(err)
	}
	if rec := f.do("POST", "/api/v1/accounts/shop.test/register", ``); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"missing":["phone"]`) {
		t.Fatalf("not ready: %d %s", rec.Code, rec.Body)
	}
	f.setKey(t, false)
	if rec := f.do("POST", "/api/v1/accounts/shop.test/register", ``); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no OpenRouter key") {
		t.Fatalf("no key: %d %s", rec.Code, rec.Body)
	}
}

// An email asked for too early came back empty: the next ask is still the first email.
func TestAnEmailAskThatTimedOutDoesNotSkipTheNextEmail(t *testing.T) {
	f := newFixture(t)
	f.svc.cfg.Now = time.Now
	to := "owner+shop@example.com"
	got := make(chan answer, 2)
	f.worker.onAgent = func(ctx context.Context, _ browser.Message, ask browser.Ask) (browser.Message, error) {
		v, ok := ask(ctx, browser.AskRequest{Name: "email", Why: "the email"})
		got <- answer{v, ok}
		f.inbox.Add(emailTo(to, "hi@shop.test", "Confirm", "<p>hi</p>"))
		v, ok = ask(ctx, browser.AskRequest{Name: "email", Why: "the email"})
		got <- answer{v, ok}
		return browser.Message{"outcome": "done", "success": true}, nil
	}
	f.register(t)
	if a := <-got; a.ok {
		t.Fatalf("first ask = %+v", a)
	}
	if a := <-got; !a.ok || !strings.Contains(a.value, `"subject":"Confirm"`) {
		t.Fatalf("second ask = %+v", a)
	}
}

// identityWriter is hostd answering the Account while another registration finishes on the site.
type identityWriter struct {
	*fakeHost
	during func()
}

func (h identityWriter) Owner(ctx context.Context) (Owner, error) {
	h.during()
	return h.fakeHost.Owner(ctx)
}

// A registration that ends between Register's look at the account and its claim on the site: the
// account it made is never overwritten.
func TestRegisterLooksAgainOnceItHoldsTheSite(t *testing.T) {
	f := newFixture(t)
	f.svc.cfg.Owner = identityWriter{fakeHost: f.host, during: func() { f.signedUp(t, "registered") }}
	if _, err := f.svc.Register(context.Background(), shopKey); !errors.Is(err, ErrAccountExists) {
		t.Fatalf("err = %v", err)
	}
	if a, _ := f.account(t); a.State != "registered" || a.Email != "owner+shop@example.com" {
		t.Fatalf("account = %+v", a)
	}
	if n, _ := f.store.Registrations(context.Background(), shopKey); n != 0 {
		t.Fatalf("journaled %d", n)
	}
}

// Register forgets the profile before it keeps a password: a worker that isn't there leaves no
// orphan secret.
func TestRegisterWithNoWorkerWritesNoPassword(t *testing.T) {
	f := newHandlerFixture(t)
	f.worker.down = true
	rec := f.do("POST", "/api/v1/accounts/"+shopKey+"/register", "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "the browser worker is not available") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, has := f.password(t); has {
		t.Fatal("a password was kept")
	}
	if _, has := f.account(t); has {
		t.Fatal("an account was written")
	}
	if n, _ := f.store.Registrations(context.Background(), shopKey); n != 0 {
		t.Fatalf("journaled %d", n)
	}
	if _, err := f.svc.Register(context.Background(), shopKey); errors.Is(err, ErrBusy) {
		t.Fatal("the site stayed claimed")
	}
}

func TestRegisterForgetsTheProfileBeforeThePassword(t *testing.T) {
	f := newFixture(t)
	passwordThen := true
	f.worker.onForget = func(typ string, body browser.Message) {
		if typ == "forget_profile" {
			_, passwordThen = f.password(t)
		}
	}
	f.register(t)
	if got := f.worker.forgot(); len(got) == 0 || got[0]["type"] != "forget_profile" || got[0]["site"] != shopKey {
		t.Fatalf("the worker was asked %v", got)
	}
	if passwordThen {
		t.Fatal("the password was kept before the profile was forgotten")
	}
	if _, has := f.password(t); !has {
		t.Fatal("no password after the registration")
	}
}
