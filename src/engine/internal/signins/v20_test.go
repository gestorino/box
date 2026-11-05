package signins

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestV20DropsThePasswordColumnAndKeepsTheRows(t *testing.T) {
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
	// v19's accounts: the old password column, empty since secretd.
	if _, err := db.Exec(`CREATE TABLE accounts (
		key TEXT PRIMARY KEY, email TEXT NOT NULL, password BLOB NOT NULL, owner_json TEXT NOT NULL,
		state TEXT NOT NULL, reason TEXT NOT NULL, run INTEGER NOT NULL, created_at TEXT NOT NULL,
		footprint TEXT NOT NULL DEFAULT '', footprint_clicks TEXT NOT NULL DEFAULT '[]');
		INSERT INTO accounts VALUES ('shop.test', 'a@b.c', x'', '{}', 'registered', '', 1, '2026-11-03T10:00:00Z', 'https://shop.test/me', '[]');
		PRAGMA user_version = 19;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, has, err := s.Account(context.Background(), shopKey)
	if err != nil || !has || a.Email != "a@b.c" || a.Footprint != "https://shop.test/me" {
		t.Fatalf("account = %+v %v %v", a, has, err)
	}
	var n, v int
	s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('accounts') WHERE name = 'password'`).Scan(&n)
	s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if n != 0 || v != 20 {
		t.Fatalf("password columns = %d, v = %d", n, v)
	}
}
