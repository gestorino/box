package dbmigrate

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func open(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func version(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRunAppliesFromTheCurrentVersion(t *testing.T) {
	db := open(t)
	if err := Run(db, []string{`CREATE TABLE t (a INTEGER);`}); err != nil {
		t.Fatal(err)
	}
	if v := version(t, db); v != 1 {
		t.Fatalf("version %d", v)
	}
	// migrations[0] would fail if it ran again.
	if err := Run(db, []string{`CREATE TABLE t (a INTEGER);`, `ALTER TABLE t ADD COLUMN b INTEGER;`}); err != nil {
		t.Fatal(err)
	}
	if v := version(t, db); v != 2 {
		t.Fatalf("version %d", v)
	}
	if _, err := db.Exec(`INSERT INTO t (a, b) VALUES (1, 2)`); err != nil {
		t.Fatal(err)
	}
}

func TestRunRefusesANewerDatabase(t *testing.T) {
	db := open(t)
	if err := Run(db, []string{`CREATE TABLE t (a INTEGER);`, `ALTER TABLE t ADD COLUMN b INTEGER;`}); err != nil {
		t.Fatal(err)
	}
	if err := Run(db, []string{`CREATE TABLE t (a INTEGER);`}); err == nil {
		t.Fatal("a newer database was accepted")
	}
}
