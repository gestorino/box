package signins

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// v13 is the described-site schema as the box holds it before v14.
const v13 = `
CREATE TABLE versions (site TEXT NOT NULL, version INTEGER NOT NULL, date TEXT NOT NULL, yaml TEXT NOT NULL,
	signature TEXT NOT NULL, account_json TEXT NOT NULL, uploaded_at TEXT NOT NULL, PRIMARY KEY (site, version));
CREATE TABLE accounts (site TEXT PRIMARY KEY, registered INTEGER NOT NULL, email TEXT NOT NULL, password BLOB NOT NULL,
	owner_json TEXT NOT NULL, state TEXT NOT NULL, reason TEXT NOT NULL, checked_at TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE journal (id INTEGER PRIMARY KEY, site TEXT NOT NULL, event TEXT NOT NULL, email TEXT NOT NULL,
	phone TEXT NOT NULL, closure_path TEXT NOT NULL, at TEXT NOT NULL);
CREATE TABLE runs (id INTEGER PRIMARY KEY, site TEXT NOT NULL, kind TEXT NOT NULL, state TEXT NOT NULL,
	reason TEXT NOT NULL, started_at TEXT NOT NULL, ended_at TEXT NOT NULL);
CREATE TABLE run_lines (id INTEGER PRIMARY KEY, run INTEGER NOT NULL, at TEXT NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL);
CREATE TABLE paths (site TEXT NOT NULL, version INTEGER NOT NULL, flow TEXT NOT NULL, screen INTEGER NOT NULL,
	state TEXT NOT NULL, path_json TEXT NOT NULL, run INTEGER NOT NULL, at TEXT NOT NULL, PRIMARY KEY (site, version, flow, screen, state));
CREATE TABLE run_usage (run INTEGER PRIMARY KEY, calls INTEGER NOT NULL, model TEXT NOT NULL, input_tokens INTEGER NOT NULL,
	cached_tokens INTEGER NOT NULL, cache_write_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
	cost_usd REAL NOT NULL, priced INTEGER NOT NULL, partial INTEGER NOT NULL);
CREATE TABLE errands (run INTEGER PRIMARY KEY, url TEXT NOT NULL, action TEXT NOT NULL,
	report TEXT NOT NULL DEFAULT '', served_json TEXT NOT NULL DEFAULT '[]');
CREATE TABLE run_points (run INTEGER NOT NULL, seq INTEGER NOT NULL, key TEXT NOT NULL, name TEXT NOT NULL, state TEXT NOT NULL,
	at TEXT NOT NULL, detail TEXT NOT NULL, PRIMARY KEY (run, key));
CREATE TABLE run_parts (run INTEGER NOT NULL, n INTEGER NOT NULL, started_at TEXT NOT NULL, ended_at TEXT NOT NULL, PRIMARY KEY (run, n));
CREATE TABLE run_clicks (run INTEGER NOT NULL, at TEXT NOT NULL, x INTEGER NOT NULL, y INTEGER NOT NULL);

INSERT INTO versions VALUES ('booksy', 9, 'd', 'y', 's', '{"home_url": "https://old.booksy.com/"}', 't'),
	('booksy', 10, 'd', 'y', 's', '{"home_url": "https://booksy.com/pt-pt/"}', 't'),
	('thefork_pt', 3, 'd', 'y', 's', '{"home_url": "https://www.thefork.pt/"}', 't'),
	('idealista_pt', 1, 'd', 'y', 's', '{"home_url": "https://www.idealista.pt/"}', 't'),
	('nohome', 1, 'd', 'y', 's', '{}', 't');
INSERT INTO accounts VALUES ('booksy', 1, 'owner+booksy11@example.com', X'0102', '{"first_name":"A"}', 'signed_in', '', 'c', '2026-10-30T00:00:00Z'),
	('thefork_pt', 1, 'owner+thefork_pt@example.com', X'0304', '{}', 'warn', 'checked out', 'c', '2026-10-31T00:00:00Z'),
	('idealista_pt', 0, 'owner+idealista_pt@example.com', X'05', '{}', 'warn', 'the registration was interrupted', '', '2026-11-01T00:00:00Z'),
	('nohome', 1, 'owner+nohome@example.com', X'06', '{}', 'signed_in', '', '', '2026-11-01T00:00:00Z');
INSERT INTO journal VALUES (1, 'booksy', 'registering', 'owner+booksy11@example.com', '+351', '/close', 't'),
	(2, 'thefork_pt', 'registering', 'owner+thefork_pt@example.com', '+351', '', 't');
INSERT INTO runs VALUES (1, 'booksy', 'register', 'signed_in', '', '2026-10-30T00:00:00Z', '2026-10-30T00:01:00Z'), (2, 'booksy', 'check', 'signed_in', '', '2026-10-30T00:00:00Z', '2026-10-30T00:01:00Z'),
	(3, 'booksy', 'errand', 'ended', 'done', '2026-10-30T00:00:00Z', '2026-10-30T00:01:00Z'), (4, 'shop_test', 'errand', 'ended', 'done', '2026-10-30T00:00:00Z', '2026-10-30T00:01:00Z'),
	(5, 'thefork_pt', 'sign_in', 'warn', '', '2026-10-30T00:00:00Z', '2026-10-30T00:01:00Z');
INSERT INTO run_lines (run, at, kind, text) VALUES (1, 't', 'box', 'registering'), (3, 't', 'box', 'an errand');
INSERT INTO run_points VALUES (1, 1, 'k', 'n', 'reached', 't', '');
INSERT INTO run_parts VALUES (1, 1, 't', 't'), (3, 1, 't', 't');
INSERT INTO errands (run, url, action) VALUES (3, 'https://booksy.com/x', 'book'), (4, 'https://www.shop.test/', 'buy');
INSERT INTO paths VALUES ('booksy', 10, 'register', 1, 'approved', '[]', 1, 't');
PRAGMA user_version = 13;
`

func v13Store(t *testing.T) (*Store, string) {
	t.Helper()
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
	if _, err := db.Exec(v13); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestV14MovesRegisteredAccountsToTheirDomain(t *testing.T) {
	s, _ := v13Store(t)
	ctx := context.Background()
	a, has, err := s.Account(ctx, "booksy.com")
	if err != nil || !has {
		t.Fatalf("booksy.com: %v %v", has, err)
	}
	if a.Email != "owner+booksy11@example.com" || a.Owner["first_name"] != "A" || a.State != "registered" || a.CreatedAt.IsZero() {
		t.Fatalf("booksy.com = %+v", a)
	}
	// A registered account whose last check warned is still the box's account.
	if a, has, _ := s.Account(ctx, "thefork.pt"); !has || a.State != "registered" {
		t.Fatalf("thefork.pt = %+v %v", a, has)
	}
	if n, _ := s.Registrations(ctx, "booksy.com"); n != 1 {
		t.Fatalf("booksy.com registrations = %d", n)
	}
	runs, err := s.Runs(ctx, "booksy.com")
	if err != nil || len(runs) != 1 || runs[0].ID != 3 {
		t.Fatalf("booksy.com runs = %+v %v", runs, err)
	}
	if runs, _ := s.Runs(ctx, "shop.test"); len(runs) != 1 || runs[0].ID != 4 {
		t.Fatalf("shop.test runs = %+v", runs)
	}
	for _, table := range []string{"versions", "paths", "run_points"} {
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, table).Scan(&n)
		if n != 0 {
			t.Errorf("table %s is still there", table)
		}
	}
	var v int
	s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 20 { // and on through v20
		t.Fatalf("user_version = %d", v)
	}
}

func TestV14DropsTheDescriptionEraRuns(t *testing.T) {
	s, _ := v13Store(t)
	var got []int64
	rows, err := s.db.Query(`SELECT id FROM runs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		got = append(got, id)
	}
	rows.Close()
	if !reflect.DeepEqual(got, []int64{3, 4}) {
		t.Fatalf("runs = %v: only the errands should stay", got)
	}
	var lines, parts int
	s.db.QueryRow(`SELECT COUNT(*) FROM run_lines`).Scan(&lines)
	s.db.QueryRow(`SELECT COUNT(*) FROM run_parts`).Scan(&parts)
	if lines != 1 || parts != 1 {
		t.Fatalf("lines = %d, parts = %d: only the errand's should stay", lines, parts)
	}
}

func TestV14DropsAnAccountThatWasNeverRegistered(t *testing.T) {
	s, _ := v13Store(t)
	ctx := context.Background()
	for _, key := range []string{"idealista.pt", "idealista_pt", "nohome"} {
		if _, has, err := s.Account(ctx, key); err != nil || has {
			t.Errorf("account %s: %v %v", key, has, err)
		}
	}
}

func TestTheErrandsTableIsNowAgentRuns(t *testing.T) {
	// A store from before kept every agent run's facts in "errands": opening it renames the table,
	// its rows kept.
	s, path := openTestStore(t)
	if _, err := s.db.Exec(`DROP TABLE agent_runs;
		CREATE TABLE errands (run INTEGER PRIMARY KEY, url TEXT NOT NULL, action TEXT NOT NULL,
			report TEXT NOT NULL DEFAULT '', served_json TEXT NOT NULL DEFAULT '[]');
		INSERT INTO errands (run, url, action, report) VALUES (7, 'https://shop.test/a', 'do it', 'done')`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var url, report string
	if err := s.db.QueryRow(`SELECT url, report FROM agent_runs WHERE run = 7`).Scan(&url, &report); err != nil ||
		url != "https://shop.test/a" || report != "done" {
		t.Fatalf("agent_runs row 7: %q %q %v", url, report, err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'errands'`).Scan(&n)
	if n != 0 {
		t.Fatal("the errands table is still there")
	}
}

func TestAFreshStoreIsV20(t *testing.T) {
	s, _ := openTestStore(t)
	var v int
	s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 20 {
		t.Fatalf("v = %d", v)
	}
}
