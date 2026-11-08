// Package relay is relayd: the box claimed to its owner's account, and held on a relay so phones
// reach it from the internet. Design: docs/superpowers/specs/2026-11-08-remote-access-box-design.md.
package relay

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"concierge/engine/internal/dbmigrate"

	_ "modernc.org/sqlite"
)

// State is where the box is in its claim.
type State string

const (
	Unclaimed State = "unclaimed"
	Claiming  State = "claiming"
	Claimed   State = "claimed"
)

// Identity is who the box is to the service. Email is the account's, the claim's (sent with its
// start and its finish); Name, BoxDomain and Relay are set once claimed.
type Identity struct {
	BoxID     string
	State     State
	Email     string
	Name      string
	BoxDomain string
	Relay     string
}

// Device is a device certificate the box issued.
type Device struct {
	Serial  string    `json:"serial"`
	Label   string    `json:"label"`
	Issued  time.Time `json:"issued"`
	Expires time.Time `json:"expires"`
}

// Store is relay.db: the box's identity (one row) and the device certificates it issued.
type Store struct{ db *sql.DB }

var migrations = []string{`
CREATE TABLE identity (
	id INTEGER PRIMARY KEY CHECK (id = 1), box_id TEXT NOT NULL, state TEXT NOT NULL,
	email TEXT NOT NULL DEFAULT '', name TEXT NOT NULL DEFAULT '',
	box_domain TEXT NOT NULL DEFAULT '', relay TEXT NOT NULL DEFAULT '');
CREATE TABLE devices (
	serial TEXT PRIMARY KEY, label TEXT NOT NULL, issued TEXT NOT NULL, expires TEXT NOT NULL);`,
}

// OpenStore opens (creating if needed) the database at path: WAL, synchronous=FULL.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := dbmigrate.Run(db, migrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("relay schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Identity is the box's identity; the first call makes its box_id.
func (s *Store) Identity(ctx context.Context) (Identity, error) {
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO identity (id, box_id, state) VALUES (1, ?, ?)`,
		uuid.NewString(), string(Unclaimed)); err != nil {
		return Identity{}, err
	}
	var id Identity
	var st string
	err := s.db.QueryRowContext(ctx, `SELECT box_id, state, email, name, box_domain, relay FROM identity WHERE id = 1`).
		Scan(&id.BoxID, &st, &id.Email, &id.Name, &id.BoxDomain, &id.Relay)
	id.State = State(st)
	return id, err
}

// SetIdentity replaces the identity (its BoxID included).
func (s *Store) SetIdentity(ctx context.Context, id Identity) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO identity (id, box_id, state, email, name, box_domain, relay)
		VALUES (1, ?, ?, ?, ?, ?, ?)`, id.BoxID, string(id.State), id.Email, id.Name, id.BoxDomain, id.Relay)
	return err
}

// Reset makes the box a new one: a new box_id, unclaimed, no devices.
func (s *Store) Reset(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM devices`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO identity (id, box_id, state) VALUES (1, ?, ?)`,
		uuid.NewString(), string(Unclaimed)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AddDevice(ctx context.Context, d Device) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO devices (serial, label, issued, expires) VALUES (?, ?, ?, ?)`,
		d.Serial, d.Label, d.Issued.UTC().Format(time.RFC3339), d.Expires.UTC().Format(time.RFC3339))
	return err
}

// Devices are the issued certificates, oldest first.
func (s *Store) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT serial, label, issued, expires FROM devices ORDER BY issued, serial`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var issued, expires string
		if err := rows.Scan(&d.Serial, &d.Label, &issued, &expires); err != nil {
			return nil, err
		}
		if d.Issued, err = time.Parse(time.RFC3339, issued); err != nil {
			return nil, err
		}
		if d.Expires, err = time.Parse(time.RFC3339, expires); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) RemoveDevice(ctx context.Context, serial string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE serial = ?`, serial)
	return err
}

// ActiveSerial says whether serial is an issued certificate not yet expired at now. A store error
// counts as not active.
func (s *Store) ActiveSerial(serial string, now time.Time) bool {
	var expires string
	if err := s.db.QueryRow(`SELECT expires FROM devices WHERE serial = ?`, serial).Scan(&expires); err != nil {
		return false
	}
	t, err := time.Parse(time.RFC3339, expires)
	return err == nil && now.Before(t)
}
