package signins

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A card is a site in the catalog: its name, one line about it, where it starts, its icon and the
// actions the owner can start an errand with there — nothing about how, which the agent finds live.
// The catalog is the box's own: every card comes through the API, suggested from the site's page
// and edited by the owner (cards.go).
type Card struct {
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Link        string    `json:"link"`
	Icon        string    `json:"-"` // a PNG data URL, served at /api/v1/accounts/{key}/icon
	Actions     []Action  `json:"actions"`
	AddedAt     time.Time `json:"added_at"`
}

// Action is what the owner can start an errand with on a site: its words, the details the owner says
// for it ("What", "When"), each a field of its own on the Errands page, and its effect on the site's
// appointments (appointments.go): none, create, change, cancel or sync.
type Action struct {
	Text   string   `json:"text"`
	Params []string `json:"params"`
	Effect string   `json:"effect"`
}

// effects are what an action may do to the site's appointments.
var effects = map[string]bool{"none": true, "create": true, "change": true, "cancel": true, "sync": true}

const (
	iconPrefix     = "data:image/png;base64,"
	iconMaxBytes   = 16 << 10
	nameMax        = 80
	descriptionMax = 200
	actionsMax     = 12
	actionMax      = 80
	paramsMax      = 4
	paramMax       = 40
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// iconPNG is the icon's PNG bytes; nil for no icon. Only a PNG, never anything a browser would run.
func iconPNG(icon string) ([]byte, error) {
	if icon == "" {
		return nil, nil
	}
	if !strings.HasPrefix(icon, iconPrefix) {
		return nil, errors.New("the icon must be a PNG data URL")
	}
	png, err := base64.StdEncoding.DecodeString(icon[len(iconPrefix):])
	if err != nil || !bytes.HasPrefix(png, pngSignature) {
		return nil, errors.New("the icon must be a PNG data URL")
	}
	if len(png) > iconMaxBytes {
		return nil, fmt.Errorf("the icon is over %d KiB", iconMaxBytes>>10)
	}
	return png, nil
}

const cardCols = `key, name, description, link, icon, actions, added_at`

func scanCard(r scanner) (Card, error) {
	var c Card
	var actions, at string
	if err := r.Scan(&c.Key, &c.Name, &c.Description, &c.Link, &c.Icon, &actions, &at); err != nil {
		return Card{}, err
	}
	if err := json.Unmarshal([]byte(actions), &c.Actions); err != nil {
		return Card{}, err
	}
	if c.Actions == nil {
		c.Actions = []Action{}
	}
	for i := range c.Actions {
		if c.Actions[i].Params == nil {
			c.Actions[i].Params = []string{}
		}
	}
	var err error
	c.AddedAt, err = parseTS(at)
	return c, err
}

// Cards is every card, by name.
func (s *Store) Cards(ctx context.Context) ([]Card, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cardCols+` FROM sites ORDER BY lower(name), key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Card{}
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Card(ctx context.Context, key string) (Card, bool, error) {
	c, err := scanCard(s.db.QueryRowContext(ctx, `SELECT `+cardCols+` FROM sites WHERE key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Card{}, false, nil
	}
	return c, err == nil, err
}

// InsertCard stores a new card; false when the site has one already (which stays as it is).
func (s *Store) InsertCard(ctx context.Context, c Card) (bool, error) {
	actions, err := actionsJSON(c.Actions)
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO sites (`+cardCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (key) DO NOTHING`, c.Key, c.Name, c.Description, c.Link, c.Icon, actions, ts(c.AddedAt))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// UpdateCard writes the card's name, description, link, icon and actions; its key and when it was
// added stay.
func (s *Store) UpdateCard(ctx context.Context, c Card) error {
	actions, err := actionsJSON(c.Actions)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE sites SET name = ?, description = ?, link = ?, icon = ?, actions = ?
		WHERE key = ?`, c.Name, c.Description, c.Link, c.Icon, actions, c.Key)
	return err
}

func actionsJSON(actions []Action) (string, error) {
	if actions == nil {
		actions = []Action{}
	}
	raw, err := json.Marshal(actions)
	return string(raw), err
}

// DeleteCard removes the site's card and the appointments kept on it.
func (s *Store) DeleteCard(ctx context.Context, key string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM sites WHERE key = ?`, key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM appointments WHERE site = ?`, key); err != nil {
		return err
	}
	return tx.Commit()
}
