package hostd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"concierge/engine/internal/hostsettings"
	"concierge/engine/internal/secrets"
	"concierge/engine/internal/secrets/secretstest"
)

func TestAccountDefaultsEmptyWithBillingSame(t *testing.T) {
	st, _ := openSettings(t)
	got, err := st.Account(context.Background())
	if err != nil || got != (Account{BillingSame: true}) {
		t.Fatalf("defaults: %+v %v", got, err)
	}
}

func TestAccountRoundTripsAndTrims(t *testing.T) {
	st, path := openSettings(t)
	ctx := context.Background()
	in := Account{
		Owner:       hostsettings.Owner{FirstName: "  Alex ", LastName: "Sample", NameRu: " Алекса"},
		Phone:       " +351 912 345 678 ",
		Contacts:    Contacts{Phone: " +44 7700 900123 ", Email: " me@example.org "},
		Address:     Address{Street: " Rua A 1 ", Line2: "2 Esq", PostalCode: "1000-001", City: "Lisboa", Country: "PT"},
		BillingSame: false,
		Billing:     Address{Street: "Rua B 2", City: "Porto", Country: "PT"},
	}
	if err := st.SetAccount(ctx, in); err != nil {
		t.Fatal(err)
	}
	want := in
	want.FirstName, want.NameRu, want.Address.Street, want.Phone = "Alex", "Алекса", "Rua A 1", "+351912345678"
	want.Contacts = Contacts{Phone: "+447700900123", Email: "me@example.org"}
	st.Close()
	again, err := OpenSettings(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got, err := again.Account(ctx); err != nil || got != want {
		t.Fatalf("after reopen: %+v %v (want %+v)", got, err, want)
	}
}

func TestAccountRefusesAndNamesTheField(t *testing.T) {
	st, _ := openSettings(t)
	ctx := context.Background()
	for field, mod := range map[string]func(*Account){
		"address street:":      func(a *Account) { a.Address.Street = strings.Repeat("a", 201) },
		"Russian name:":        func(a *Account) { a.NameRu = strings.Repeat("я", 61) },
		"address country:":     func(a *Account) { a.Address.Country = "prt" },
		"address city:":        func(a *Account) { a.Address.City = "Li\tsbon" },
		"address postal code:": func(a *Account) { a.Address.PostalCode = "1000\n001" },
		"billing city:":        func(a *Account) { a.Billing.City = "Li\tsbon" },
		"phone:":               func(a *Account) { a.Phone = "912345678" },
		"contact phone:":       func(a *Account) { a.Contacts.Phone = "912345678" },
		"contact email:":       func(a *Account) { a.Contacts.Email = "me" },
		// Several bad fields: the first in the form's order is named, every time.
		"first name:": func(a *Account) {
			a.FirstName, a.Address.City, a.Billing.Country = "N\x00", "Li\tsbon", "prt"
		},
	} {
		a := Account{Owner: hostsettings.Owner{FirstName: "Alex"}, BillingSame: true}
		mod(&a)
		for range 5 {
			err := st.SetAccount(ctx, a)
			if !errors.Is(err, ErrAccount) || !strings.HasPrefix(err.Error(), field) {
				t.Fatalf("%s %v", field, err)
			}
		}
	}
	// Not international, not digits, a leading zero, too short.
	for _, phone := range []string{"912345678", "+351 91x 345 678", "+0351912345678", "+3519"} {
		a := Account{Phone: phone, BillingSame: true}
		if err := st.SetAccount(ctx, a); !errors.Is(err, ErrAccount) || !strings.HasPrefix(err.Error(), "phone:") {
			t.Fatalf("%q: %v", phone, err)
		}
	}
	// Not an address, a display name, angle brackets, two addresses.
	for _, email := range []string{"me", "Me <me@example.org>", "<me@example.org>", "a@example.org, b@example.org", "me@"} {
		a := Account{Contacts: Contacts{Email: email}, BillingSame: true}
		if err := st.SetAccount(ctx, a); !errors.Is(err, ErrAccount) || !strings.HasPrefix(err.Error(), "contact email:") {
			t.Fatalf("%q: %v", email, err)
		}
	}
	if got, _ := st.Account(ctx); got != (Account{BillingSame: true}) {
		t.Fatalf("a refused save stored something: %+v", got)
	}
}

func TestTheVoiceNameMovesIntoAccount(t *testing.T) {
	for voiceName, want := range map[string][2]string{
		"Alex":        {"Alex", ""},
		"Alex Sample": {"Alex", "Sample"},
		"":            {"", ""},
	} {
		path := filepath.Join(t.TempDir(), "settings.db")
		db, err := sql.Open("sqlite", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
			CREATE TABLE connectors (name TEXT PRIMARY KEY, state TEXT NOT NULL CHECK (state IN ('setting_up', 'added')));
			INSERT INTO settings (key, value) VALUES ('voice_model', 'gpt-realtime-2'), ('voice_name', ?), ('voice_name_ru', 'Алекса');
			PRAGMA user_version = 2;`, voiceName); err != nil {
			t.Fatal(err)
		}
		db.Close()
		st, err := OpenSettings(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		a, err := st.Account(ctx)
		if err != nil || a.FirstName != want[0] || a.LastName != want[1] || a.NameRu != "Алекса" {
			t.Fatalf("%q: %+v %v", voiceName, a, err)
		}
		if v, _ := st.Voice(ctx); v.Model != "gpt-realtime-2" {
			t.Fatalf("the model was lost: %+v", v)
		}
		var n, ver int
		st.db.QueryRow(`SELECT count(*) FROM settings WHERE key LIKE 'voice_name%'`).Scan(&n)
		st.db.QueryRow(`PRAGMA user_version`).Scan(&ver)
		if n != 0 || ver != 4 {
			t.Fatalf("%q: %d voice_name rows left, user_version %d", voiceName, n, ver)
		}
		st.Close()
	}
}

func TestTheReadOnlySocketCarriesOnlyTheNames(t *testing.T) {
	svc, _, _, _ := newService()
	st, _ := openSettings(t)
	if err := st.SetAccount(ctx, Account{
		Owner:       hostsettings.Owner{FirstName: "Alex", LastName: "Sample", NameRu: "Алекса"},
		Phone:       "+351912345678",
		Contacts:    Contacts{Phone: "+447700900123", Email: "me@example.org"},
		Address:     Address{Street: "Rua A 1"},
		BillingSame: true,
	}); err != nil {
		t.Fatal(err)
	}
	rec := call(NewSettingsHandler(st, svc), "GET", "/settings", "")
	var keys map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if keys["owner_first_name"] != "Alex" || keys["owner_last_name"] != "Sample" || keys["owner_name_ru"] != "Алекса" {
		t.Fatalf("names: %s", rec.Body)
	}
	for _, k := range []string{"address", "street", "card", "billing", "phone", "contacts", "voice_name", "voice_name_ru"} {
		if _, ok := keys[k]; ok {
			t.Fatalf("%q on the read-only socket: %s", k, rec.Body)
		}
	}
	if b := rec.Body.String(); strings.Contains(b, "Rua A") || strings.Contains(b, "912345678") || strings.Contains(b, "7700900123") || strings.Contains(b, "example.org") {
		t.Fatalf("an address on the read-only socket: %s", rec.Body)
	}
}

func TestAccountRoutes(t *testing.T) {
	svc, _, _, _ := newService()
	st, _ := openSettings(t)
	h := NewHandler(svc, st)
	body := `{"owner_first_name":"Alex","owner_last_name":"Sample","owner_name_ru":"Алекса","address":{"street":"Rua A 1","line2":"","postal_code":"1000-001","city":"Lisboa","country":"PT"},"billing_same":true,"billing":{"street":"","line2":"","postal_code":"","city":"","country":""}}`
	if rec := call(h, "PUT", "/settings/account", body); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	rec := call(h, "GET", "/settings/account", "")
	var got Account
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.FirstName != "Alex" || got.Address.City != "Lisboa" || !got.BillingSame || got.Card != nil {
		t.Fatalf("get: %s %v", rec.Body, err)
	}
	if rec := call(h, "PUT", "/settings/account", `{"owner_first_name":"N","nope":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	rec = call(h, "PUT", "/settings/account", `{"owner_first_name":"N","address":{"country":"prt"},"billing_same":true}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "country") {
		t.Fatalf("bad country: %d %s", rec.Code, rec.Body)
	}
}

func withCard(c *Card) Account {
	return Account{Owner: hostsettings.Owner{FirstName: "Alex"}, BillingSame: true, Card: c}
}

func TestACardIsKeptInSecretdAndReadMasked(t *testing.T) {
	st, _ := openSettings(t)
	if err := st.SetAccount(ctx, withCard(&Card{Name: "A S", Expiry: "08/29", Number: "4242 4242 4242 4242"})); err != nil {
		t.Fatal(err)
	}
	a, err := st.Account(ctx)
	if err != nil || a.Card == nil || *a.Card != (Card{Name: "A S", Expiry: "08/29", Brand: "visa", Last4: "4242"}) {
		t.Fatalf("account: %+v %+v %v", a, a.Card, err)
	}
	if n, err := st.CardNumber(ctx); err != nil || n != "4242424242424242" {
		t.Fatalf("card number: %q %v", n, err)
	}
	if n, found, err := st.secrets.Get(ctx, "card-number"); err != nil || !found || n != "4242424242424242" {
		t.Fatalf("secretd's card-number: %q %v %v", n, found, err)
	}
}

func TestTheNumberIsNeverInSettingsDB(t *testing.T) {
	st, path := openSettings(t)
	if err := st.SetAccount(ctx, withCard(&Card{Name: "A S", Expiry: "08/29", Number: "4242 4242 4242 4242"})); err != nil {
		t.Fatal(err)
	}
	rows, err := st.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(v, "4242424242424242") || strings.Contains(v, "424242424242") {
			t.Fatalf("%s holds the number in the clear", k)
		}
	}
	for _, f := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(f)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "424242424242") {
			t.Fatalf("%s holds the number in the clear", filepath.Base(f))
		}
	}
}

func TestCardNumbersAreCheckedNotMangled(t *testing.T) {
	st, _ := openSettings(t)
	for _, c := range []struct {
		card  Card
		field string
	}{
		{Card{Expiry: "08/29", Number: "4242-4242-4242-4242"}, "card number:"},
		{Card{Expiry: "08/29", Number: "4242424242424241"}, "card number:"},     // fails Luhn
		{Card{Expiry: "08/29", Number: "42424242420"}, "card number:"},          // passes Luhn, 11 digits
		{Card{Expiry: "08/29", Number: "42424242424242424242"}, "card number:"}, // passes Luhn, 20 digits
		{Card{Expiry: "13/29", Number: "4242424242424242"}, "card expiry:"},
		{Card{Expiry: "00/29", Number: "4242424242424242"}, "card expiry:"},
		{Card{Expiry: "8/29", Number: "4242424242424242"}, "card expiry:"},
		{Card{Expiry: "08/2029", Number: "4242424242424242"}, "card expiry:"},
		{Card{Name: "N\tT", Expiry: "08/29", Number: "4242424242424242"}, "card name:"},
	} {
		err := st.SetAccount(ctx, withCard(&c.card))
		if !errors.Is(err, ErrAccount) || !strings.HasPrefix(err.Error(), c.field) {
			t.Fatalf("%+v: %v", c.card, err)
		}
		if strings.Contains(err.Error(), "4242") {
			t.Fatalf("the refusal echoes the number: %v", err)
		}
	}
	if a, _ := st.Account(ctx); a.Card != nil || a.FirstName != "" {
		t.Fatalf("a refused save stored something: %+v", a)
	}
	for _, n := range []string{"5555555555554444", "2223003122003222", "378282246310005", " 4242 4242 4242 4242 "} {
		if err := st.SetAccount(ctx, withCard(&Card{Expiry: "12/30", Number: n})); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
}

func TestTheBrandComesFromTheNumber(t *testing.T) {
	for digits, want := range map[string]string{
		"4242424242424242": "visa",
		"5105105105105100": "mastercard", "5555555555554444": "mastercard",
		"2221000000000009": "mastercard", "2720990000000000": "mastercard",
		"2220990000000000": "", "2721000000000000": "", "5000000000000000": "", "5600000000000000": "",
		"340000000000009": "amex", "378282246310005": "amex", "350000000000000": "",
		"6011111111111117": "",
	} {
		if got := cardBrand(digits); got != want {
			t.Fatalf("%s: %q, want %q", digits, got, want)
		}
	}
}

func TestACardNeedsANumberAndAnExpiry(t *testing.T) {
	st, _ := openSettings(t)
	if err := st.SetAccount(ctx, withCard(&Card{Name: "A S", Expiry: "08/29"})); !errors.Is(err, ErrAccount) || !strings.HasPrefix(err.Error(), "card number:") {
		t.Fatalf("no number, none stored: %v", err)
	}
	if err := st.SetAccount(ctx, withCard(&Card{Name: "A S", Number: "4242424242424242"})); !errors.Is(err, ErrAccount) || !strings.HasPrefix(err.Error(), "card expiry:") {
		t.Fatalf("no expiry: %v", err)
	}
	if a, _ := st.Account(ctx); a.Card != nil {
		t.Fatalf("a refused card was stored: %+v", a.Card)
	}
	if _, err := st.CardNumber(ctx); !errors.Is(err, ErrNoCard) {
		t.Fatalf("number of a refused card: %v", err)
	}
}

func TestANumberlessCardKeepsTheStoredNumber(t *testing.T) {
	st, _ := openSettings(t)
	if err := st.SetAccount(ctx, withCard(&Card{Name: "A S", Expiry: "08/29", Number: "4242424242424242"})); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccount(ctx, withCard(&Card{Name: "X", Expiry: "09/30"})); err != nil {
		t.Fatal(err)
	}
	a, _ := st.Account(ctx)
	if a.Card == nil || *a.Card != (Card{Name: "X", Expiry: "09/30", Brand: "visa", Last4: "4242"}) {
		t.Fatalf("card: %+v", a.Card)
	}
	if n, err := st.CardNumber(ctx); err != nil || n != "4242424242424242" {
		t.Fatalf("card number: %q %v", n, err)
	}
	// No card in the body keeps the stored card as it is.
	if err := st.SetAccount(ctx, withCard(nil)); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.Account(ctx); a.Card == nil || a.Card.Name != "X" {
		t.Fatalf("a save without a card changed it: %+v", a.Card)
	}
}

func TestASaveThatCantKeepTheNumberChangesNothing(t *testing.T) {
	st, _ := openSettings(t)
	st.secrets = secretstest.Down(t)
	in := Account{
		Owner:       hostsettings.Owner{FirstName: "Alex", LastName: "Sample"},
		Address:     Address{City: "Lisboa", Country: "PT"},
		BillingSame: true,
	}
	if err := st.SetAccount(ctx, in); err != nil {
		t.Fatalf("a save without a card needs no secretd: %v", err)
	}
	withNumber := in
	withNumber.FirstName = "Changed"
	withNumber.Card = &Card{Expiry: "08/29", Number: "4242424242424242"}
	err := st.SetAccount(ctx, withNumber)
	if !errors.Is(err, secrets.ErrUnavailable) || strings.Contains(err.Error(), "4242") {
		t.Fatalf("a number with secretd down: %v", err)
	}
	a, err := st.Account(ctx)
	if err != nil || a != in {
		t.Fatalf("after a refused card: %+v %v (want %+v)", a, err, in)
	}
	var n int
	st.db.QueryRow(`SELECT count(*) FROM settings WHERE key LIKE 'card%'`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d card rows stored without secretd", n)
	}
}

func TestRemoveCard(t *testing.T) {
	st, _ := openSettings(t)
	if err := st.SetAccount(ctx, withCard(&Card{Name: "A S", Expiry: "08/29", Number: "4242424242424242"})); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveCard(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := st.secrets.Get(ctx, "card-number"); err != nil || found {
		t.Fatalf("secretd after remove: %v %v", found, err)
	}
	if a, _ := st.Account(ctx); a.Card != nil || a.FirstName != "Alex" {
		t.Fatalf("after remove: %+v %+v", a, a.Card)
	}
	if _, err := st.CardNumber(ctx); !errors.Is(err, ErrNoCard) {
		t.Fatalf("card number after remove: %v", err)
	}
	var n int
	st.db.QueryRow(`SELECT count(*) FROM settings WHERE key LIKE 'card%'`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d card rows left", n)
	}
	if err := st.RemoveCard(ctx); err != nil {
		t.Fatalf("removing no card: %v", err)
	}
}

func TestRemoveCardFailsWhileSecretdIsDown(t *testing.T) {
	st, _ := openSettings(t)
	if err := st.SetAccount(ctx, withCard(&Card{Name: "A S", Expiry: "08/29", Number: "4242424242424242"})); err != nil {
		t.Fatal(err)
	}
	up := st.secrets
	st.secrets = secretstest.Down(t)
	if err := st.RemoveCard(ctx); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("remove with secretd down: %v", err)
	}
	if a, _ := st.Account(ctx); a.Card == nil {
		t.Fatal("the card rows went while the number stayed")
	}
	st.secrets = up
	if n, err := st.CardNumber(ctx); err != nil || n != "4242424242424242" {
		t.Fatalf("number: %q %v", n, err)
	}
}

func TestCardRoutes(t *testing.T) {
	svc, _, _, _ := newService()
	st, _ := openSettings(t)
	h := NewHandler(svc, st)
	if rec := call(h, "GET", "/settings/account/card-number", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("no card: %d %s", rec.Code, rec.Body)
	}
	body := `{"owner_first_name":"Alex","billing_same":true,"card":{"name":"A S","expiry":"08/29","number":"4242 4242 4242 4242"}}`
	if rec := call(h, "PUT", "/settings/account", body); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	rec := call(h, "GET", "/settings/account", "")
	mustBePrivate(t, rec)
	var got struct {
		Card map[string]string `json:"card"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil ||
		got.Card["last4"] != "4242" || got.Card["brand"] != "visa" || got.Card["expiry"] != "08/29" || got.Card["name"] != "A S" {
		t.Fatalf("get: %s %v", rec.Body, err)
	}
	if _, ok := got.Card["number"]; ok || strings.Contains(rec.Body.String(), "424242424242") {
		t.Fatalf("the number in GET /settings/account: %s", rec.Body)
	}
	rec = call(h, "GET", "/settings/account/card-number", "")
	mustBePrivate(t, rec)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"number":"4242424242424242"}` {
		t.Fatalf("card number: %d %s", rec.Code, rec.Body)
	}
	rec = call(h, "PUT", "/settings/account", `{"billing_same":true,"card":{"expiry":"08/29","number":"4242424242424241"}}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "card number: not a valid card number") {
		t.Fatalf("bad number: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "DELETE", "/settings/account/card", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "GET", "/settings/account/card-number", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("after delete: %d %s", rec.Code, rec.Body)
	}

	if rec := call(h, "PUT", "/settings/account", body); rec.Code != http.StatusNoContent {
		t.Fatalf("put again: %d %s", rec.Code, rec.Body)
	}
	st.secrets = secretstest.Down(t)
	rec = call(h, "PUT", "/settings/account", strings.Replace(body, "Alex", "Changed", 1))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "secrets unavailable") || strings.Contains(rec.Body.String(), "4242") {
		t.Fatalf("save with secretd down: %d %s", rec.Code, rec.Body)
	}
	rec = call(h, "GET", "/settings/account/card-number", "")
	if rec.Code != http.StatusBadGateway || strings.TrimSpace(rec.Body.String()) != `{"error":"secrets unavailable"}` {
		t.Fatalf("show with secretd down: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, "DELETE", "/settings/account/card", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("remove with secretd down: %d %s", rec.Code, rec.Body)
	}
}

func TestACardWhoseNumberIsMissingInSecretdIsNamed(t *testing.T) {
	st, _ := openSettings(t)
	if err := st.SetAccount(ctx, withCard(&Card{Name: "A S", Expiry: "08/29", Number: "4242424242424242"})); err != nil {
		t.Fatal(err)
	}
	if err := st.secrets.Delete(ctx, "card-number"); err != nil {
		t.Fatal(err)
	}
	svc, _, _, _ := newService()
	rec := call(NewHandler(svc, st), "GET", "/settings/account/card-number", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no card number kept") || strings.Contains(rec.Body.String(), `"number"`) {
		t.Fatalf("missing number: %d %s", rec.Code, rec.Body)
	}
}

func mustBePrivate(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("the answer may be cached: %v", rec.Header())
	}
}

func TestAnAccountOfLongCyrillicFieldsIsAccepted(t *testing.T) {
	svc, _, _, _ := newService()
	st, _ := openSettings(t)
	long := strings.Repeat("я", 200)
	addr := map[string]string{"street": long, "line2": long, "postal_code": long, "city": long, "country": "PT"}
	body, _ := json.Marshal(map[string]any{
		"owner_first_name": long, "owner_last_name": long, "owner_name_ru": strings.Repeat("я", 60),
		"address": addr, "billing_same": false, "billing": addr,
	})
	if rec := call(NewHandler(svc, st), "PUT", "/settings/account", string(body)); rec.Code != http.StatusNoContent {
		t.Fatalf("put: %d %s (%d bytes)", rec.Code, rec.Body, len(body))
	}
}
