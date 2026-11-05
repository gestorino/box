package signins

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"concierge/engine/internal/browser"
	"concierge/engine/internal/secrets"
)

func TestSiteKeyIsTheRegistrableDomain(t *testing.T) {
	for host, want := range map[string]string{
		"pt.booksy.com": "booksy.com", "www.booksy.com": "booksy.com", "booksy.com": "booksy.com",
		"www.thefork.pt": "thefork.pt", "WWW.X.CO.UK": "x.co.uk", "a.b.shop.test.": "shop.test",
		"127.0.0.1": "127.0.0.1", "localhost": "localhost",
	} {
		if got, err := siteKey(host); err != nil || got != want {
			t.Errorf("siteKey(%q) = %q, %v; want %q", host, got, err, want)
		}
	}
	if _, err := siteKey("co.uk"); err == nil {
		t.Error("a public suffix has a key")
	}
}

func TestAKeyNamesOnlyOneDirectory(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", "../x", "x..y", "-x.test", "UPPER.test", "thefork_pt"} {
		if isKey(bad) {
			t.Errorf("isKey(%q)", bad)
		}
	}
}

func TestSitesListsCardsWithTheirAccounts(t *testing.T) {
	f := newFixture(t)
	f.signedUp(t, "registered")
	if _, err := f.store.InsertCard(context.Background(), Card{Key: "mine.test", Name: "Mine", Link: "https://mine.test/",
		Icon: tinyPNG, AddedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	sites, err := f.svc.Sites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// By name: every card is the owner's.
	if len(sites) != 2 || sites[0].Key != "mine.test" || sites[1].Key != shopKey {
		t.Fatalf("sites = %+v", sites)
	}
	mine, shop := sites[0], sites[1]
	if shop.HasIcon || shop.Account == nil || shop.Account.State != "registered" ||
		shop.Account.Email != "owner+shop@example.com" {
		t.Fatalf("shop = %+v %+v", shop, shop.Account)
	}
	if !mine.HasIcon || mine.Account != nil {
		t.Fatalf("mine = %+v", mine)
	}
}

func TestRemoveForgetsTheAccountAndItsProfileButKeepsItsRuns(t *testing.T) {
	f := newFixture(t)
	f.signedUp(t, "registered")
	id, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://www.shop.test/a", Action: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	if err := f.svc.RemoveAccount(context.Background(), shopKey); err != nil {
		t.Fatal(err)
	}
	if _, has := f.account(t); has {
		t.Fatal("the account is still there")
	}
	if _, has := f.password(t); has {
		t.Fatal("the password is still in secretd")
	}
	if got := f.worker.forgot(); len(got) != 1 || got[0]["type"] != "forget_profile" || got[0]["site"] != shopKey {
		t.Fatalf("the worker was asked %v", got)
	}
	if _, found, err := f.svc.AgentRun(context.Background(), id); err != nil || !found {
		t.Fatalf("the errand went with the account: %v %v", found, err)
	}
	if err := f.svc.RemoveAccount(context.Background(), shopKey); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("second remove = %v", err)
	}
}

// The profile goes first: a secretd that then fails leaves an account to remove again, never a
// password-less account still signed in.
func TestRemoveAccountAsksTheWorkerToForgetTheProfile(t *testing.T) {
	f := newFixture(t)
	f.signedUp(t, "registered")
	var passwordThen bool
	f.worker.onForget = func(typ string, body browser.Message) {
		if typ == "forget_profile" {
			_, passwordThen = f.password(t)
		}
	}
	if err := f.svc.RemoveAccount(context.Background(), shopKey); err != nil {
		t.Fatal(err)
	}
	if got := f.worker.forgot(); len(got) != 1 || got[0]["type"] != "forget_profile" || got[0]["site"] != shopKey {
		t.Fatalf("the worker was asked %v", got)
	}
	if !passwordThen {
		t.Fatal("the password was deleted before the profile")
	}
	if _, has := f.password(t); has {
		t.Fatal("the password is still in secretd")
	}
}

func TestRemoveAccountWithNoWorkerChangesNothing(t *testing.T) {
	f := newHandlerFixture(t)
	f.signedUp(t, "registered")
	f.worker.down = true
	rec := f.do("POST", "/api/v1/accounts/"+shopKey+"/remove", "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "the browser worker is not available") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, has := f.account(t); !has {
		t.Fatal("the account went")
	}
	if pw, has := f.password(t); !has || pw != testPassword {
		t.Fatal("the password went")
	}
	var journaled int
	f.store.db.QueryRow(`SELECT COUNT(*) FROM journal WHERE site = ?`, shopKey).Scan(&journaled)
	if journaled != 0 {
		t.Fatalf("journaled %d", journaled)
	}
}

func TestAnErrandOnARegisteredSiteUsesItsProfileAndCredentials(t *testing.T) {
	f := newFixture(t)
	f.signedUp(t, "registered")
	if _, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://pt.shop.test/place/1", Action: "do it"}); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	body := f.worker.sent()[0]
	if body["site"] != shopKey || body["session"] != shopKey {
		t.Fatalf("body = %v", body)
	}
	if !reflect.DeepEqual(body["allowed_domains"], []string{shopKey, "*." + shopKey}) {
		t.Fatalf("allowed_domains = %v", body["allowed_domains"])
	}
	want := map[string]string{"email": "owner+shop@example.com", "secret:password": testPassword}
	if !reflect.DeepEqual(body["values"], want) {
		t.Fatalf("values = %v", body["values"])
	}
}

// A registered account always has its password in secretd: one that doesn't is an error, never an
// errand that signs in with nothing.
func TestAnErrandOnAnAccountWithoutItsPasswordDoesNotStart(t *testing.T) {
	f := newFixture(t)
	f.signedUp(t, "registered")
	if err := f.secrets.Delete(context.Background(), "signins/"+shopKey+"/password"); err != nil {
		t.Fatal(err)
	}
	_, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/x", Action: "do it"})
	if !errors.Is(err, ErrNoPassword) {
		t.Fatalf("err = %v", err)
	}
	// the owner is told what to do about it, not a generic failure
	code, out := runsCall(t, NewRunsHandler(f.svc), "POST", "/api/v1/errands", `{"url":"https://shop.test/x","action":"do it"}`)
	if code != http.StatusConflict || out["error"] != "the box's password for this site is missing — remove the account and register again" {
		t.Fatalf("route: %d %v", code, out)
	}
	f.svc.runs.Wait()
	if len(f.worker.sent()) != 0 {
		t.Fatal("an errand started")
	}
}

// secretd down while removing: the account stays, to be removed again.
func TestRemoveKeepsTheAccountWhenSecretsAreUnavailable(t *testing.T) {
	f := newFixture(t)
	f.signedUp(t, "registered")
	f.secretdDown(t)
	if err := f.svc.RemoveAccount(context.Background(), shopKey); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if _, has := f.account(t); !has {
		t.Fatal("the account went without its password")
	}
}

func TestAnErrandOnAWarnAccountIsAThrowaway(t *testing.T) {
	f := newFixture(t)
	f.signedUp(t, "warn")
	if _, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/", Action: "do it"}); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	body := f.worker.sent()[0]
	if body["session"] != "" || body["values"] != nil {
		t.Fatalf("body = %v", body)
	}
}

func TestCheckSignInSignsInWithTheAccountInItsProfile(t *testing.T) {
	f := newFixture(t)
	f.signedUp(t, "registered")
	id, err := f.svc.CheckSignIn(context.Background(), shopKey)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	body := f.worker.sent()[0]
	if body["url"] != "https://www.shop.test/start" || body["action"] != checkAction || body["session"] != shopKey || body["site"] != shopKey ||
		body["kind"] != "check" {
		t.Fatalf("body = %v", body)
	}
	want := map[string]string{"email": "owner+shop@example.com", "secret:password": testPassword}
	if !reflect.DeepEqual(body["values"], want) {
		t.Fatalf("values = %v", body["values"])
	}
	d, _, _ := f.svc.AgentRun(context.Background(), id)
	if d.Kind != "check" || d.State != "ended" || d.Reason != "signed in" {
		t.Fatalf("check = %+v", d.AgentRunView)
	}
}

func TestACheckThatCouldNotSignInSaysSoAndKeepsTheAccount(t *testing.T) {
	f := newFixture(t)
	f.signedUp(t, "registered")
	f.worker.onCheck = func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
		return browser.Message{"outcome": "done", "success": false, "report": "the password was refused"}, nil
	}
	id, err := f.svc.CheckSignIn(context.Background(), shopKey)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	if d, _, _ := f.svc.AgentRun(context.Background(), id); d.Reason != "not signed in: the password was refused" {
		t.Fatalf("check = %+v", d.AgentRunView)
	}
	if a, has := f.account(t); !has || a.State != "registered" {
		t.Fatalf("account = %+v %v", a, has)
	}
}

func TestCheckSignInNeedsARegisteredAccount(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.CheckSignIn(context.Background(), "nowhere.test"); !errors.Is(err, ErrUnknownSite) {
		t.Fatalf("no card: %v", err)
	}
	if _, err := f.svc.CheckSignIn(context.Background(), shopKey); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("no account: %v", err)
	}
	f.signedUp(t, "warn")
	if _, err := f.svc.CheckSignIn(context.Background(), shopKey); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("an interrupted registration: %v", err)
	}
	if len(f.worker.sent()) != 0 {
		t.Fatal("a refused check ran")
	}
}

func TestACheckInFlightShowsAsCheckingAndHoldsTheSite(t *testing.T) {
	f := newFixture(t)
	f.signedUp(t, "registered")
	release := make(chan struct{})
	f.worker.onCheck = func(context.Context, browser.Message, browser.Ask) (browser.Message, error) {
		<-release
		return browser.Message{"outcome": "done", "success": true}, nil
	}
	id, err := f.svc.CheckSignIn(context.Background(), shopKey)
	if err != nil {
		t.Fatal(err)
	}
	sites, err := f.svc.Sites(context.Background())
	if err != nil || sites[0].Phase != phaseChecking || sites[0].Run != id {
		t.Fatalf("sites = %+v, %v", sites, err)
	}
	if _, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/", Action: "do it"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("an errand meanwhile: %v", err)
	}
	close(release)
	f.svc.runs.Wait()
}

func TestASitesPageListsItsRegistrationsAndSignIns(t *testing.T) {
	f := newFixture(t)
	f.worker.onAgent = func(_ context.Context, body browser.Message, _ browser.Ask) (browser.Message, error) {
		return browser.Message{"outcome": "done", "success": true, "report": "made"}, nil
	}
	reg := f.register(t)
	signsIn := []browser.Message{{"event": "action", "payload": map[string]any{"kind": "fill", "target": "email"}},
		{"event": "action", "payload": map[string]any{"kind": "fill", "target": "secret:password"}}}
	f.worker.progress = signsIn
	signed, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/a", Action: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	f.worker.progress = nil
	plain, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/b", Action: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	if _, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://other.test/", Action: "do it"}); err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()
	check, err := f.svc.CheckSignIn(context.Background(), shopKey)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.runs.Wait()

	v, runs, found, err := f.svc.Site(context.Background(), shopKey)
	if err != nil || !found || v.Key != shopKey || v.Account == nil {
		t.Fatalf("site = %+v %v %v", v, found, err)
	}
	var got []string
	for _, r := range runs {
		got = append(got, fmt.Sprintf("%d %s %v", r.ID, r.Kind, r.SignedIn))
	}
	// Only its registrations, sign-in checks (the registration's own follow-up among them) and
	// sign-ins: an errand that didn't sign in is the Errands page's.
	want := []string{fmt.Sprintf("%d check false", check), fmt.Sprintf("%d errand true", signed),
		fmt.Sprintf("%d check false", reg+1), fmt.Sprintf("%d register false", reg)}
	_ = plain
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runs = %v, want %v", got, want)
	}
	// The card's latest status is the newest of them, on the page and in the list alike.
	if v.Latest == nil || v.Latest.ID != check || v.Latest.Reason != "signed in" {
		t.Fatalf("latest = %+v", v.Latest)
	}
	sites, err := f.svc.Sites(context.Background())
	if err != nil || sites[0].Latest == nil || sites[0].Latest.ID != check {
		t.Fatalf("sites = %+v, %v", sites, err)
	}
	if _, _, found, _ := f.svc.Site(context.Background(), "nowhere.test"); found {
		t.Fatal("a site with no card was found")
	}
}
