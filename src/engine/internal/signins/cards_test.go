package signins

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"concierge/engine/internal/browser"
)

func TestV17GivesCardsActionsAndDropsTheirSource(t *testing.T) {
	dir, err := os.MkdirTemp("", "si")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "signins.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// v16's sites: a card shipped by the catalog, with its source.
	if _, err := db.Exec(`CREATE TABLE sites (
		key TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL, link TEXT NOT NULL,
		icon TEXT NOT NULL, source TEXT NOT NULL, added_at TEXT NOT NULL);
		INSERT INTO sites VALUES ('shop.test', 'Shop', 'A shop.', 'https://www.shop.test/', '', 'catalog', '2026-11-03T10:00:00Z');
		PRAGMA user_version = 16;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, found, err := s.Card(context.Background(), shopKey)
	if err != nil || !found || c.Name != "Shop" || c.Actions == nil || len(c.Actions) != 0 {
		t.Fatalf("card = %+v %v %v", c, found, err)
	}
	var has, v int
	s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sites') WHERE name = 'source'`).Scan(&has)
	s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if has != 0 || v != 20 {
		t.Fatalf("source column %d, v = %d", has, v)
	}
}

func TestSuggestAnswersTheWorkersCardWithoutStoringIt(t *testing.T) {
	f := newFixture(t)
	f.worker.onCard = func(body browser.Message) (browser.Message, error) {
		return browser.Message{"name": "  The Barber\n", "description": "Cuts hair.\nAnd beards.", "icon": tinyPNG,
			"actions":       []any{act(" Get a haircut ", "Which barber", " which barber "), act("get a haircut"), act("Find a barber")},
			"actions_error": ""}, nil
	}
	got, err := f.svc.Suggest(context.Background(), "https://www.barber.co.uk/pt/shop?x=1")
	if err != nil {
		t.Fatal(err)
	}
	want := Suggestion{Key: "barber.co.uk", Name: "The Barber", Description: "Cuts hair. And beards.",
		Link: "https://www.barber.co.uk/pt/shop?x=1", Icon: tinyPNG, Actions: []Action{{Text: "Get a haircut", Params: []string{"Which barber"}, Effect: "none"}, {Text: "Find a barber", Params: []string{}, Effect: "none"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("suggestion = %+v", got)
	}
	if f.worker.cards[0]["url"] != "https://www.barber.co.uk/pt/shop?x=1" {
		t.Fatalf("asked = %v", f.worker.cards)
	}
	if _, found, _ := f.store.Card(context.Background(), "barber.co.uk"); found {
		t.Fatal("a suggestion was stored")
	}
}

func TestASuggestionFromWhatTheModelKnowsSaysSo(t *testing.T) {
	f := newFixture(t)
	f.worker.onCard = func(browser.Message) (browser.Message, error) {
		return browser.Message{"name": "Shop", "description": "Sells things.", "icon": "", "actions": []any{act("Order a thing")},
			"actions_error": "", "actions_note": "from what the model knows of the site: its page could not be read"}, nil
	}
	got, err := f.svc.Suggest(context.Background(), "https://new.test/")
	if err != nil || got.ActionsNote != "from what the model knows of the site: its page could not be read" {
		t.Fatalf("suggestion = %+v %v", got, err)
	}
}

func TestSuggestForACardedSiteAnswersThatCard(t *testing.T) {
	f := newFixture(t)
	got, err := f.svc.Suggest(context.Background(), "https://pt.shop.test/elsewhere")
	if err != nil || !got.Exists || got.Key != shopKey || len(f.worker.cards) != 0 {
		t.Fatalf("suggestion = %+v %v, asked %d", got, err, len(f.worker.cards))
	}
}

func TestSuggestWithoutAGoodPageStillSuggestsTheSiteByItsKey(t *testing.T) {
	f := newFixture(t)
	f.worker.onCard = func(browser.Message) (browser.Message, error) { return nil, errors.New("worker gone") }
	got, err := f.svc.Suggest(context.Background(), "https://new.test/")
	// Nothing read is nothing said: the console names a new card by its key, and an edit keeps its own.
	if err != nil || got.Key != "new.test" || got.Name != "" || got.Actions == nil || len(got.Actions) != 0 ||
		got.ActionsError != "the page could not be read" {
		t.Fatalf("suggestion = %+v %v", got, err)
	}
}

func TestSuggestNeedsAnHTTPLink(t *testing.T) {
	f := newFixture(t)
	for _, link := range []string{"", "ftp://x.test/", "https://", "not a link", "https://co.uk/"} {
		if _, err := f.svc.Suggest(context.Background(), link); !errors.Is(err, ErrBadCard) {
			t.Errorf("%q: %v", link, err)
		}
	}
}

func card(o func(*CardInput)) CardInput {
	in := CardInput{Name: "Mine", Description: "My site.", Link: "https://www.mine.test/start", Icon: tinyPNG,
		Actions: []Action{{Text: "Order a thing", Params: []string{"What", " what ", "How many"}}, {Text: " Track an order "},
			{Text: "order a thing"}, {Text: ""}}}
	if o != nil {
		o(&in)
	}
	return in
}

func TestCreateCardKeepsTheOwnersCard(t *testing.T) {
	f := newFixture(t)
	v, err := f.svc.CreateCard(context.Background(), card(nil))
	if err != nil {
		t.Fatal(err)
	}
	if v.Key != "mine.test" || v.Name != "Mine" || !v.HasIcon || !reflect.DeepEqual(v.Actions, []Action{{Text: "Order a thing", Params: []string{"What", "How many"}, Effect: "none"}, {Text: "Track an order", Params: []string{}, Effect: "none"}}) {
		t.Fatalf("site = %+v", v)
	}
	if _, err := f.svc.CreateCard(context.Background(), card(nil)); !errors.Is(err, ErrCardExists) {
		t.Fatalf("second = %v", err)
	}
}

func TestACardMustBeOneTheConsoleCanShow(t *testing.T) {
	f := newFixture(t)
	for name, o := range map[string]func(*CardInput){
		"no name":         func(c *CardInput) { c.Name = "  " },
		"long name":       func(c *CardInput) { c.Name = strings.Repeat("n", 81) },
		"two lines":       func(c *CardInput) { c.Description = "a\nb" },
		"long text":       func(c *CardInput) { c.Description = strings.Repeat("d", 201) },
		"not http":        func(c *CardInput) { c.Link = "ftp://mine.test/" },
		"not a png":       func(c *CardInput) { c.Icon = "data:image/svg+xml;base64,PHN2Zz4=" },
		"long action":     func(c *CardInput) { c.Actions = []Action{{Text: strings.Repeat("a", 81)}} },
		"two-line action": func(c *CardInput) { c.Actions = []Action{{Text: "a\nb"}} },
		"thirteen":        func(c *CardInput) { c.Actions = actions(strings.Split("a b c d e f g h i j k l m", " ")...) },
		"long detail":     func(c *CardInput) { c.Actions = []Action{{Text: "a", Params: []string{strings.Repeat("p", 41)}}} },
		"five details":    func(c *CardInput) { c.Actions = []Action{{Text: "a", Params: []string{"p", "q", "r", "s", "t"}}} },
	} {
		if _, err := f.svc.CreateCard(context.Background(), card(o)); !errors.Is(err, ErrBadCard) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.svc.CreateCard(context.Background(), card(func(c *CardInput) { c.Icon = "" })); err != nil {
		t.Fatalf("no icon: %v", err)
	}
}

func TestUpdateCardKeepsItsSite(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v, err := f.svc.UpdateCard(ctx, shopKey, card(func(c *CardInput) { c.Link = "https://pt.shop.test/new"; c.Name = "Shop PT" }))
	if err != nil || v.Name != "Shop PT" || v.Link != "https://pt.shop.test/new" || len(v.Actions) != 2 {
		t.Fatalf("site = %+v %v", v, err)
	}
	if _, err := f.svc.UpdateCard(ctx, shopKey, card(nil)); !errors.Is(err, ErrBadCard) {
		t.Fatalf("another site's link: %v", err)
	}
	if _, err := f.svc.UpdateCard(ctx, "nowhere.test", card(func(c *CardInput) { c.Link = "https://nowhere.test/" })); !errors.Is(err, ErrUnknownSite) {
		t.Fatalf("no card: %v", err)
	}
}

func TestAnyCardWithoutAnAccountCanBeRemoved(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.signedUp(t, "registered")
	if err := f.svc.RemoveCard(ctx, shopKey); !errors.Is(err, ErrCardInUse) {
		t.Fatalf("with an account: %v", err)
	}
	if err := f.store.DeleteAccount(ctx, shopKey); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RemoveCard(ctx, shopKey); err != nil {
		t.Fatalf("without: %v", err)
	}
}

func TestCardsOverHTTP(t *testing.T) {
	f := newHandlerFixture(t)
	f.worker.onCard = func(browser.Message) (browser.Message, error) {
		return browser.Message{"name": "New", "description": "", "icon": "", "actions": []any{act("Do a thing", "What")}, "actions_error": ""}, nil
	}
	if rec := f.do("POST", "/api/v1/accounts/suggest", `{"link":"https://new.test/"}`); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"actions":[{"text":"Do a thing","params":["What"],"effect":"none"}]`) {
		t.Fatalf("suggest: %d %s", rec.Code, rec.Body)
	}
	body := `{"name":"New","description":"","link":"https://new.test/","icon":"","actions":[{"text":"Do a thing","params":["What"]}]}`
	if rec := f.do("POST", "/api/v1/accounts", body); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"key":"new.test"`) {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("POST", "/api/v1/accounts", body); rec.Code != http.StatusConflict {
		t.Fatalf("again: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("PUT", "/api/v1/accounts/new.test", strings.Replace(body, `"New"`, `"Renamed"`, 1)); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"name":"Renamed"`) {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("PUT", "/api/v1/accounts/new.test", `{"name":"","description":"","link":"https://new.test/","icon":"","actions":[]}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "name") {
		t.Fatalf("bad: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do("GET", "/api/v1/accounts", ""); !strings.Contains(rec.Body.String(), `"actions":[{"text":"Do a thing","params":["What"],"effect":"none"}]`) ||
		strings.Contains(rec.Body.String(), `"source"`) {
		t.Fatalf("list: %s", rec.Body)
	}
}

func TestReadingThePageAgainSuggestsForACardedSiteToo(t *testing.T) {
	f := newFixture(t)
	f.worker.onCard = func(browser.Message) (browser.Message, error) {
		return browser.Message{"name": "Shop Again", "description": "", "icon": "", "actions": []any{act("Order a thing")}}, nil
	}
	got, err := f.svc.SuggestAgain(context.Background(), "https://pt.shop.test/x")
	if err != nil || got.Exists || got.Key != shopKey || got.Name != "Shop Again" || len(f.worker.cards) != 1 {
		t.Fatalf("suggestion = %+v %v", got, err)
	}
}

func TestTheSitesPageCarriesItsEditableCard(t *testing.T) {
	f := newHandlerFixture(t)
	if _, err := f.svc.UpdateCard(context.Background(), shopKey, card(func(c *CardInput) { c.Link = "https://www.shop.test/start" })); err != nil {
		t.Fatal(err)
	}
	rec := f.do("GET", "/api/v1/accounts/shop.test", "")
	if !strings.Contains(rec.Body.String(), `"card":{"name":"Mine","description":"My site.","link":"https://www.shop.test/start","icon":"`+tinyPNG+`","actions":[{"text":"Order a thing","params":["What","How many"],"effect":"none"},{"text":"Track an order","params":[],"effect":"none"}]}`) {
		t.Fatalf("site: %s", rec.Body)
	}
	if rec := f.do("POST", "/api/v1/accounts/suggest", `{"link":"https://www.shop.test/","again":true}`); !strings.Contains(rec.Body.String(), `"key":"shop.test"`) ||
		strings.Contains(rec.Body.String(), `"exists"`) {
		t.Fatalf("again: %s", rec.Body)
	}
}

// act is an action as the worker's JSON decodes it: its words and the details said for it.
func act(text string, params ...string) map[string]any {
	ps := []any{}
	for _, p := range params {
		ps = append(ps, p)
	}
	return map[string]any{"text": text, "params": ps}
}

// actions are actions without details.
func actions(texts ...string) []Action {
	out := []Action{}
	for _, t := range texts {
		out = append(out, Action{Text: t})
	}
	return out
}

func TestV18GivesEachStoredActionItsDetails(t *testing.T) {
	dir, err := os.MkdirTemp("", "si")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "signins.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// v17's sites: actions as words alone.
	if _, err := db.Exec(`CREATE TABLE sites (
		key TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL, link TEXT NOT NULL,
		icon TEXT NOT NULL, actions TEXT NOT NULL DEFAULT '[]', added_at TEXT NOT NULL);
		INSERT INTO sites VALUES ('shop.test', 'Shop', '', 'https://shop.test/', '', '["Order a thing","Track an order"]', '2026-11-03T10:00:00Z');
		PRAGMA user_version = 17;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, _, err := s.Card(context.Background(), shopKey)
	if err != nil || !reflect.DeepEqual(c.Actions, []Action{{Text: "Order a thing", Params: []string{}, Effect: "none"}, {Text: "Track an order", Params: []string{}, Effect: "none"}}) {
		t.Fatalf("actions = %+v %v", c.Actions, err)
	}
}
