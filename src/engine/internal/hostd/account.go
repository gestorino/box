package hostd

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"concierge/engine/internal/hostsettings"
)

// Address is a postal address; every part may be empty. Country is an ISO 3166-1 alpha-2 code.
type Address struct {
	Street     string `json:"street"`
	Line2      string `json:"line2"`
	PostalCode string `json:"postal_code"`
	City       string `json:"city"`
	Country    string `json:"country"`
}

// Card is the owner's payment card. Number is only ever read from a Save: Account answers the card
// masked (Brand, Last4), and the full number comes only from CardNumber. No security code is kept.
type Card struct {
	Name   string `json:"name"`
	Expiry string `json:"expiry"`
	Number string `json:"number,omitempty"`
	Brand  string `json:"brand,omitempty"`
	Last4  string `json:"last4,omitempty"`
}

// Contacts are the owner's own phone and email: where the box reaches them (setup checks first),
// never the box's SIM or mailbox. Either may be empty.
type Contacts struct {
	Phone string `json:"phone"`
	Email string `json:"email"`
}

// Account is who the box acts for: the owner's names (also read by the daemons, as hostsettings.Owner),
// the phone number (the SIM's, the one WhatsApp and Telegram are linked to; sign-ups give it), the
// owner's own contacts, the address, the billing address and the card. Only the names ever leave
// hostd's console socket.
type Account struct {
	hostsettings.Owner
	Phone       string   `json:"phone"`
	Contacts    Contacts `json:"contacts"`
	Address     Address  `json:"address"`
	BillingSame bool     `json:"billing_same"`
	Billing     Address  `json:"billing"`
	Card        *Card    `json:"card"`
}

// cardNumberSecret is the card number's name in hostd's secretd namespace.
const cardNumberSecret = "card-number"

// ErrAccount is a refused Account; the message names the field, never its value.
var ErrAccount = errors.New("account")

// ErrNoCard: no card number is kept (no card, or a card whose number secretd does not hold).
var ErrNoCard = errors.New("no card number kept")

// maxAccountBody is the account PUT's own limit: 200-rune Cyrillic fields outgrow maxBody.
const maxAccountBody = 16 << 10

type accountError string

func (e accountError) Error() string        { return string(e) }
func (e accountError) Is(target error) bool { return target == ErrAccount }

const (
	maxText   = 200
	maxNameRu = 60
)

var expiryRe = regexp.MustCompile(`^(0[1-9]|1[0-2])/[0-9]{2}$`)

// phoneRe is an international number (E.164) once its spaces are gone: +, a country code, 7–15 digits.
var phoneRe = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// cleanText trims s; ok is false when it is longer than max runes or holds a control character.
func cleanText(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	return s, utf8.RuneCountInString(s) <= max && !strings.ContainsFunc(s, unicode.IsControl)
}

// isEmail: one bare address, no display name or angle brackets.
func isEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Name == "" && a.Address == s
}

func isCountry(c string) bool {
	return c == "" || len(c) == 2 && c[0] >= 'A' && c[0] <= 'Z' && c[1] >= 'A' && c[1] <= 'Z'
}

// isCardNumber: 12–19 digits that pass the Luhn check.
func isCardNumber(n string) bool {
	if len(n) < 12 || len(n) > 19 {
		return false
	}
	sum := 0
	for i := range len(n) {
		d := int(n[len(n)-1-i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		if i%2 == 1 {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

// cardBrand tells the brand from a valid number's prefix, or "" when it isn't one of these.
func cardBrand(n string) string {
	two, _ := strconv.Atoi(n[:2])
	four, _ := strconv.Atoi(n[:4])
	switch {
	case n[0] == '4':
		return "visa"
	case two >= 51 && two <= 55, four >= 2221 && four <= 2720:
		return "mastercard"
	case two == 34, two == 37:
		return "amex"
	}
	return ""
}

// field is one text row of Account: its settings key, the label a refusal names, and its limit.
type field struct {
	key, label string
	p          *string
	max        int
}

// addressFields is an address's rows under prefix (address or billing).
func addressFields(prefix string, a *Address) []field {
	return []field{
		{prefix + "_street", prefix + " street", &a.Street, maxText},
		{prefix + "_line2", prefix + " line 2", &a.Line2, maxText},
		{prefix + "_postal_code", prefix + " postal code", &a.PostalCode, maxText},
		{prefix + "_city", prefix + " city", &a.City, maxText},
		{prefix + "_country", prefix + " country", &a.Country, maxText},
	}
}

// accountFields is Account's text rows in the form's order, so a refusal names the first bad one.
func accountFields(a *Account) []field {
	f := []field{
		{"account_first_name", "first name", &a.FirstName, maxText},
		{"account_last_name", "last name", &a.LastName, maxText},
		{"account_name_ru", "Russian name", &a.NameRu, maxNameRu},
		{"account_phone", "phone", &a.Phone, maxText},
		{"contact_phone", "contact phone", &a.Contacts.Phone, maxText},
		{"contact_email", "contact email", &a.Contacts.Email, maxText},
	}
	f = append(f, addressFields("address", &a.Address)...)
	return append(f, addressFields("billing", &a.Billing)...)
}

// row reads one settings row into into; found is false when there is none.
func (s *Settings) row(ctx context.Context, key string, into *string) (found bool, err error) {
	switch err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(into); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// Account is everything but the card's number; Card is nil when none is stored.
func (s *Settings) Account(ctx context.Context) (Account, error) {
	a := Account{BillingSame: true}
	var same string
	var c Card
	rows := []field{{key: "billing_same", p: &same}, {key: "card_name", p: &c.Name}, {key: "card_expiry", p: &c.Expiry},
		{key: "card_brand", p: &c.Brand}}
	for _, f := range append(accountFields(&a), rows...) {
		if _, err := s.row(ctx, f.key, f.p); err != nil {
			return Account{}, err
		}
	}
	if same != "" {
		a.BillingSame = same == "1"
	}
	switch found, err := s.row(ctx, "card_last4", &c.Last4); {
	case err != nil:
		return Account{}, err
	case found:
		a.Card = &c
	}
	return a, nil
}

// SetAccount stores the names, the addresses and the card; everything is checked first, so a refusal
// stores nothing. A nil Card keeps the stored card as it is.
func (s *Settings) SetAccount(ctx context.Context, a Account) error {
	vals := map[string]string{"billing_same": "0"}
	var number string
	if a.BillingSame {
		vals["billing_same"] = "1"
	}
	for _, f := range accountFields(&a) {
		v, ok := cleanText(*f.p, f.max)
		if !ok {
			return accountError(f.label + ": too long, or has a control character")
		}
		if strings.HasSuffix(f.key, "_country") && !isCountry(v) {
			return accountError(f.label + ": must be a two-letter country code such as PT")
		}
		switch f.key {
		case "account_phone", "contact_phone":
			if v = strings.ReplaceAll(v, " ", ""); v != "" && !phoneRe.MatchString(v) {
				return accountError(f.label + ": must be an international number such as +351912345678")
			}
		case "contact_email":
			if v != "" && !isEmail(v) {
				return accountError(f.label + ": must be an email address such as you@example.com")
			}
		}
		vals[f.key] = v
	}
	if a.Card != nil {
		card, err := s.cardRows(ctx, *a.Card)
		if err != nil {
			return err
		}
		maps.Copy(vals, card)
		number = strings.ReplaceAll(a.Card.Number, " ", "")
	}
	// The number goes to secretd before any row changes: if it can't be kept, nothing is stored.
	if number != "" {
		if err := s.secrets.Put(ctx, cardNumberSecret, number); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, v := range vals {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// cardRows checks a card from a Save and returns the rows it stores; the number itself goes to
// secretd (SetAccount). A card without a number keeps the stored one.
func (s *Settings) cardRows(ctx context.Context, c Card) (map[string]string, error) {
	name, ok := cleanText(c.Name, maxText)
	if !ok {
		return nil, accountError("card name: too long, or has a control character")
	}
	number := strings.ReplaceAll(c.Number, " ", "")
	if number != "" && !isCardNumber(number) {
		return nil, accountError("card number: not a valid card number")
	}
	if number == "" {
		var last4 string
		switch found, err := s.row(ctx, "card_last4", &last4); {
		case err != nil:
			return nil, err
		case !found:
			return nil, accountError("card number: a card needs a number")
		}
	}
	expiry := strings.TrimSpace(c.Expiry)
	if !expiryRe.MatchString(expiry) {
		return nil, accountError("card expiry: must be MM/YY, such as 08/29")
	}
	rows := map[string]string{"card_name": name, "card_expiry": expiry}
	if number != "" {
		rows["card_last4"] = number[len(number)-4:]
		rows["card_brand"] = cardBrand(number)
	}
	return rows, nil
}

// CardNumber reads the stored card's full number from secretd: the one answer that carries it.
func (s *Settings) CardNumber(ctx context.Context) (string, error) {
	n, found, err := s.secrets.Get(ctx, cardNumberSecret)
	switch {
	case err != nil:
		return "", err
	case !found:
		return "", ErrNoCard
	}
	return n, nil
}

// RemoveCard deletes the number from secretd, then every row of the card; removing when there is
// none is not an error. If secretd can't be reached nothing is removed.
func (s *Settings) RemoveCard(ctx context.Context) error {
	if err := s.secrets.Delete(ctx, cardNumberSecret); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings
		WHERE key IN ('card_name', 'card_expiry', 'card_last4', 'card_brand')`); err != nil {
		return err
	}
	// secure_delete zeroes the freed pages; the checkpoint empties the WAL.
	_, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

// private keeps the account (names, addresses, above all the card number) out of the browser's cache,
// so Remove leaves no copy behind, and stops the browser sniffing it into another type.
func private(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func accountRoutes(mux *http.ServeMux, st *Settings) {
	mux.HandleFunc("GET /settings/account", func(w http.ResponseWriter, r *http.Request) {
		private(w)
		a, err := st.Account(r.Context())
		reply(w, "account", http.StatusOK, a, err)
	})
	mux.HandleFunc("PUT /settings/account", func(w http.ResponseWriter, r *http.Request) {
		private(w)
		var in Account
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAccountBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			reply(w, "account-update", 0, nil, errBadRequest("body must be {owner_first_name, owner_last_name, owner_name_ru, phone, contacts, address, billing_same, billing, card}"))
			return
		}
		err := st.SetAccount(r.Context(), in)
		if errors.Is(err, ErrAccount) {
			err = errBadRequest(err.Error())
		}
		reply(w, "account-update", http.StatusNoContent, nil, err)
	})
	mux.HandleFunc("GET /settings/account/card-number", func(w http.ResponseWriter, r *http.Request) {
		private(w)
		n, err := st.CardNumber(r.Context())
		reply(w, "account-card-number", http.StatusOK, map[string]string{"number": n}, err)
	})
	mux.HandleFunc("DELETE /settings/account/card", func(w http.ResponseWriter, r *http.Request) {
		private(w)
		reply(w, "account-card-remove", http.StatusNoContent, nil, st.RemoveCard(r.Context()))
	})
}
