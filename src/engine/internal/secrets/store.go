// Package secrets is the sealed store behind secretd: every value AES-256-GCM sealed under the box's
// master key, bound to its own namespace and name, in secrets.db.
package secrets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"concierge/engine/internal/dbmigrate"
	"concierge/engine/internal/seal"

	_ "modernc.org/sqlite"
)

// ErrDoesNotOpen: a stored value that fails to open (tampered, moved to another row, sealed under another key).
var ErrDoesNotOpen = errors.New("secret does not open")

var migrations = []string{`
CREATE TABLE secrets (
	namespace TEXT NOT NULL, name TEXT NOT NULL, sealed BLOB NOT NULL, updated_at TEXT NOT NULL,
	PRIMARY KEY (namespace, name));`,
}

type Store struct {
	db     *sql.DB
	sealer *seal.Sealer
}

// OpenStore opens secrets.db at path, sealing under key (32 raw bytes).
func OpenStore(path string, key []byte) (*Store, error) {
	sealer, err := seal.NewKey(key)
	if err != nil {
		return nil, fmt.Errorf("secrets master key: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := dbmigrate.Run(db, migrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("secrets schema: %w", err)
	}
	return &Store{db: db, sealer: sealer}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func ad(ns, name string) []byte { return []byte(ns + "\x00" + name) }

// Get returns the value; a row that does not open is ErrDoesNotOpen, never the crypto error.
func (s *Store) Get(ctx context.Context, ns, name string) (string, bool, error) {
	var box []byte
	err := s.db.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE namespace = ? AND name = ?`, ns, name).Scan(&box)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	plain, err := s.sealer.Open(box, ad(ns, name))
	if err != nil {
		return "", false, ErrDoesNotOpen
	}
	return string(plain), true, nil
}

func (s *Store) Put(ctx context.Context, ns, name, value string) error {
	box, err := s.sealer.Seal([]byte(value), ad(ns, name))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO secrets (namespace, name, sealed, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (namespace, name) DO UPDATE SET sealed = excluded.sealed, updated_at = excluded.updated_at`,
		ns, name, box, time.Now().UTC().Format(time.RFC3339))
	return err
}

// Delete removes a secret; an absent one is not an error.
func (s *Store) Delete(ctx context.Context, ns, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM secrets WHERE namespace = ? AND name = ?`, ns, name)
	return err
}
