package signins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

// Account is the box's own account on a site, keyed by the site's key; its password is in secretd
// (passwordName), never here. State is registered, or warn when its registration was cut short.
type Account struct {
	Key       string
	Email     string
	Owner     map[string]string
	State     string
	Reason    string
	Run       int64 // its registration run
	CreatedAt time.Time
	// Footprint is the page a model sign-in proved signed in on, which a check reads without the
	// model; "" when the account has none (footprint.go). FootprintClicks are the controls, by their
	// visible names, it clicks there first.
	Footprint       string
	FootprintClicks []string
}

type Store struct {
	db *sql.DB
}

// The schema at v20. A database from before is brought here by migrateToV14 to migrateToV20.
const schema = `
-- The catalog: each site's card (catalog.go).
CREATE TABLE IF NOT EXISTS sites (
	key TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL, link TEXT NOT NULL,
	icon TEXT NOT NULL, actions TEXT NOT NULL DEFAULT '[]', added_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS accounts (
	key TEXT PRIMARY KEY, email TEXT NOT NULL, owner_json TEXT NOT NULL,
	state TEXT NOT NULL, reason TEXT NOT NULL, run INTEGER NOT NULL, created_at TEXT NOT NULL,
	footprint TEXT NOT NULL DEFAULT '', footprint_clicks TEXT NOT NULL DEFAULT '[]');
-- What happened on a site, by its key: every registration's address, kept after its account goes.
CREATE TABLE IF NOT EXISTS journal (
	id INTEGER PRIMARY KEY, site TEXT NOT NULL, event TEXT NOT NULL, email TEXT NOT NULL,
	phone TEXT NOT NULL, at TEXT NOT NULL);
-- Each run on a site, and its log (runs.go).
CREATE TABLE IF NOT EXISTS runs (
	id INTEGER PRIMARY KEY, site TEXT NOT NULL, kind TEXT NOT NULL, state TEXT NOT NULL,
	reason TEXT NOT NULL, started_at TEXT NOT NULL, ended_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS runs_site ON runs (site, id);
CREATE TABLE IF NOT EXISTS run_lines (
	id INTEGER PRIMARY KEY, run INTEGER NOT NULL, at TEXT NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS run_lines_run ON run_lines (run, id);
-- What a run spent on the model (runs.go).
CREATE TABLE IF NOT EXISTS run_usage (
	run INTEGER PRIMARY KEY, calls INTEGER NOT NULL, model TEXT NOT NULL, input_tokens INTEGER NOT NULL,
	cached_tokens INTEGER NOT NULL, cache_write_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL,
	cost_usd REAL NOT NULL, priced INTEGER NOT NULL, partial INTEGER NOT NULL);
-- An agent run's own facts (agent.go), of every kind: its link and action, the model's report, and
-- the calls and billed cost by provider (JSON). Pruned with its run.
CREATE TABLE IF NOT EXISTS agent_runs (
	run INTEGER PRIMARY KEY, url TEXT NOT NULL, action TEXT NOT NULL,
	report TEXT NOT NULL DEFAULT '', served_json TEXT NOT NULL DEFAULT '[]');
-- A run's video, in parts, and the clicks marked on it (recording.go).
CREATE TABLE IF NOT EXISTS run_parts (
	run INTEGER NOT NULL, n INTEGER NOT NULL, started_at TEXT NOT NULL, ended_at TEXT NOT NULL,
	PRIMARY KEY (run, n));
CREATE TABLE IF NOT EXISTS run_clicks (
	run INTEGER NOT NULL, at TEXT NOT NULL, x INTEGER NOT NULL, y INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS run_clicks_run ON run_clicks (run);
-- What errands arranged on a site (appointments.go): kept until the owner forgets one or the card goes.
CREATE TABLE IF NOT EXISTS appointments (
	id INTEGER PRIMARY KEY, site TEXT NOT NULL, title TEXT NOT NULL, date TEXT NOT NULL, time TEXT NOT NULL,
	place TEXT NOT NULL, details_json TEXT NOT NULL, status TEXT NOT NULL, run INTEGER NOT NULL,
	created_at TEXT NOT NULL, updated_at TEXT NOT NULL, checked_at TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS appointments_site ON appointments (site);
`

// OpenStore opens (creating, owner-only) the sign-ins database.
func OpenStore(path string) (*Store, error) {
	// Create the file 0600 first so no window exists where it is group/world readable; SQLite
	// gives its -wal/-shm files the main file's mode.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	err = renameErrandsTable(db)
	if err == nil {
		err = migrateToV14(db)
	}
	if err == nil {
		err = migrateToV15(db)
	}
	if err == nil {
		err = addAccountColumn(db, 16, "footprint_clicks", `TEXT NOT NULL DEFAULT '[]'`)
	}
	if err == nil {
		err = migrateToV17(db)
	}
	if err == nil {
		err = migrateToV18(db)
	}
	if err == nil {
		err = migrateToV19(db)
	}
	if err == nil {
		err = migrateToV20(db)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := sweepRuns(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// renameErrandsTable gives a store from before its agent runs' table under its name: "errands" held
// every kind's facts. Keyed by the old table being there, before any versioned step (migrateToV14
// reads the new name); the schema has just made an empty agent_runs beside it.
func renameErrandsTable(db *sql.DB) error {
	var old int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'errands'`).Scan(&old); err != nil || old == 0 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rows int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM agent_runs`).Scan(&rows); err != nil {
		return err
	}
	if rows != 0 {
		return errors.New("signins: both errands and agent_runs hold rows")
	}
	if _, err := tx.Exec(`DROP TABLE agent_runs; ALTER TABLE errands RENAME TO agent_runs`); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateToV20 drops the accounts' password column: passwords live in secretd. A fresh database
// never had the column.
func migrateToV20(db *sql.DB) error {
	var v, has int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v >= 20 {
		return err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('accounts') WHERE name = 'password'`).Scan(&has); err != nil {
		return err
	}
	if has == 1 {
		if _, err := db.Exec(`ALTER TABLE accounts DROP COLUMN password`); err != nil {
			return err
		}
	}
	_, err := db.Exec(`PRAGMA user_version = 20`)
	return err
}

// migrateToV19 gives each stored action its effect on the site's appointments, none at first (the
// appointments table is the schema's own).
func migrateToV19(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v >= 19 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT key, actions FROM sites`)
	if err != nil {
		return err
	}
	moved := map[string]string{}
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			rows.Close()
			return err
		}
		var actions []Action
		if err := json.Unmarshal([]byte(raw), &actions); err != nil {
			rows.Close()
			return err
		}
		for i := range actions {
			if actions[i].Effect == "" {
				actions[i].Effect = "none"
			}
		}
		if moved[key], err = actionsJSON(actions); err != nil {
			rows.Close()
			return err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for key, raw := range moved {
		if _, err := tx.Exec(`UPDATE sites SET actions = ? WHERE key = ?`, raw, key); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 19`); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateToV18 gives each stored action its details: v17 kept an action as its words alone; now it
// is {text, params}, with none at first.
func migrateToV18(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v >= 18 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT key, actions FROM sites`)
	if err != nil {
		return err
	}
	moved := map[string]string{}
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			rows.Close()
			return err
		}
		var words []string
		if json.Unmarshal([]byte(raw), &words) != nil {
			continue // already {text, params}
		}
		actions := make([]Action, 0, len(words))
		for _, w := range words {
			actions = append(actions, Action{Text: w, Params: []string{}})
		}
		if moved[key], err = actionsJSON(actions); err != nil {
			rows.Close()
			return err
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for key, raw := range moved {
		if _, err := tx.Exec(`UPDATE sites SET actions = ? WHERE key = ?`, raw, key); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 18`); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateToV17 gives each card its actions, none at first, and drops its source: every card is the
// owner's, grown through the API (the shipped catalog went). Every row is kept.
func migrateToV17(db *sql.DB) error {
	var v, actions, source int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v >= 17 {
		return err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sites') WHERE name = 'actions'`).Scan(&actions); err != nil {
		return err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sites') WHERE name = 'source'`).Scan(&source); err != nil {
		return err
	}
	if actions == 0 {
		if _, err := db.Exec(`ALTER TABLE sites ADD COLUMN actions TEXT NOT NULL DEFAULT '[]'`); err != nil {
			return err
		}
	}
	if source == 1 {
		if _, err := db.Exec(`ALTER TABLE sites DROP COLUMN source`); err != nil {
			return err
		}
	}
	_, err := db.Exec(`PRAGMA user_version = 17`)
	return err
}

// migrateToV15 gives each account a footprint, none at first.
func migrateToV15(db *sql.DB) error {
	return addAccountColumn(db, 15, "footprint", `TEXT NOT NULL DEFAULT ''`)
}

// addAccountColumn brings a database below version to it by adding column to accounts. A fresh
// database has the column from the schema already; an older one (v13 rebuilt by migrateToV14
// without it included) gains it here.
func addAccountColumn(db *sql.DB, version int, column, decl string) error {
	var v, has int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v >= version {
		return err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('accounts') WHERE name = ?`, column).Scan(&has); err != nil {
		return err
	}
	if has == 0 {
		if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN ` + column + ` ` + decl); err != nil {
			return err
		}
	}
	_, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version))
	return err
}

// migrateToV14 brings a database from the described-site sign-ins (v13) to the catalog of cards:
// each registered account moves to its site's key (its description's home_url's registrable
// domain), with its journal and errand runs; the description-era runs (register, check, sign_in) go
// with every row of theirs; versions, paths and points go. It runs once, in one transaction. A fresh
// database is simply marked v14. (The profiles and videos of the sites it renamed or the runs it
// dropped stay where they were: the one box is past it.)
func migrateToV14(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v >= 14 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var described int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'versions'`).Scan(&described); err != nil {
		return err
	}
	if described == 1 {
		if err := fromDescriptions(tx); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 14`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if described == 1 {
		slog.Info("signins: the site descriptions are gone", "from_version", v)
	}
	return nil
}

func fromDescriptions(tx *sql.Tx) error {
	keys := map[string]string{} // a described site's name → its key
	rows, err := tx.Query(`SELECT site, account_json FROM versions v WHERE version = (SELECT MAX(version) FROM versions WHERE site = v.site)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var site, raw string
		if err := rows.Scan(&site, &raw); err != nil {
			rows.Close()
			return err
		}
		var d struct {
			HomeURL string `json:"home_url"`
		}
		if json.Unmarshal([]byte(raw), &d) != nil {
			continue
		}
		if u, err := url.Parse(d.HomeURL); err == nil {
			if key, err := siteKey(u.Hostname()); err == nil && isKey(key) {
				keys[site] = key
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	const theirs = ` WHERE run IN (SELECT id FROM runs WHERE kind != 'errand');`
	if _, err := tx.Exec(`DELETE FROM run_parts` + theirs + `DELETE FROM run_clicks` + theirs + `DELETE FROM run_lines` + theirs +
		`DELETE FROM run_usage` + theirs + `DELETE FROM agent_runs` + theirs + `DELETE FROM runs WHERE kind != 'errand';
		DROP TABLE IF EXISTS run_points; DROP TABLE IF EXISTS paths;`); err != nil {
		return err
	}

	// An errand on a site with no description ran under its link's host (with "_" for "."): it moves
	// to the key of its own link.
	hosts, err := tx.Query(`SELECT DISTINCT r.site, e.url FROM runs r JOIN agent_runs e ON e.run = r.id`)
	if err != nil {
		return err
	}
	renamed := map[string]string{}
	for hosts.Next() {
		var site, link string
		if err := hosts.Scan(&site, &link); err != nil {
			hosts.Close()
			return err
		}
		if _, described := keys[site]; described {
			continue
		}
		if u, err := url.Parse(link); err == nil {
			if key, err := siteKey(u.Hostname()); err == nil && isKey(key) {
				renamed[site] = key
			}
		}
	}
	hosts.Close()
	for site, key := range keys {
		renamed[site] = key
	}
	for old, key := range renamed {
		if old == key {
			continue
		}
		if _, err := tx.Exec(`UPDATE runs SET site = ? WHERE site = ?`, key, old); err != nil {
			return err
		}
	}

	// Only a registered account moves; the rest knew nothing the box can use.
	if _, err := tx.Exec(`CREATE TABLE accounts_v14 (
		key TEXT PRIMARY KEY, email TEXT NOT NULL, password BLOB NOT NULL, owner_json TEXT NOT NULL,
		state TEXT NOT NULL, reason TEXT NOT NULL, run INTEGER NOT NULL, created_at TEXT NOT NULL)`); err != nil {
		return err
	}
	for site, key := range keys {
		if _, err := tx.Exec(`INSERT INTO accounts_v14 SELECT ?, email, password, owner_json, 'registered', '', 0, created_at
			FROM accounts WHERE site = ? AND registered = 1`, key, site); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DROP TABLE accounts; ALTER TABLE accounts_v14 RENAME TO accounts;
		CREATE TABLE journal_v14 (id INTEGER PRIMARY KEY, site TEXT NOT NULL, event TEXT NOT NULL,
			email TEXT NOT NULL, phone TEXT NOT NULL, at TEXT NOT NULL);
		INSERT INTO journal_v14 SELECT id, site, event, email, phone, at FROM journal;
		DROP TABLE journal; ALTER TABLE journal_v14 RENAME TO journal;
		DROP TABLE versions;`); err != nil {
		return err
	}
	for site, key := range keys {
		if _, err := tx.Exec(`UPDATE journal SET site = ? WHERE site = ?`, key, site); err != nil {
			return err
		}
	}
	return nil
}

// sweepRuns ends the runs left running, and fails the point each was at. Phases live in the engine's
// memory: a run still running here belonged to an engine that stopped.
func sweepRuns(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE runs SET state = 'ended', reason = 'the engine restarted', ended_at = ?
		WHERE ended_at = ''`, ts(time.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, v)
}

type scanner interface{ Scan(...any) error }

func (s *Store) Account(ctx context.Context, key string) (Account, bool, error) {
	var a Account
	var owner, created, clicks string
	err := s.db.QueryRowContext(ctx, `SELECT key, email, owner_json, state, reason, run, created_at, footprint,
		footprint_clicks FROM accounts WHERE key = ?`, key).Scan(&a.Key, &a.Email, &owner, &a.State, &a.Reason,
		&a.Run, &created, &a.Footprint, &clicks)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, err
	}
	if err := json.Unmarshal([]byte(owner), &a.Owner); err != nil {
		return Account{}, false, err
	}
	if err := json.Unmarshal([]byte(clicks), &a.FootprintClicks); err != nil {
		return Account{}, false, err
	}
	if a.FootprintClicks == nil {
		a.FootprintClicks = []string{}
	}
	if a.CreatedAt, err = parseTS(created); err != nil {
		return Account{}, false, err
	}
	return a, true, nil
}

func (s *Store) PutAccount(ctx context.Context, a Account) error {
	owner, err := json.Marshal(a.Owner)
	if err != nil {
		return err
	}
	clicks, err := clicksJSON(a.FootprintClicks)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO accounts (key, email, owner_json, state, reason, run, created_at,
			footprint, footprint_clicks)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET email = excluded.email,
			owner_json = excluded.owner_json, state = excluded.state, reason = excluded.reason,
			run = excluded.run, created_at = excluded.created_at, footprint = excluded.footprint,
			footprint_clicks = excluded.footprint_clicks`,
		a.Key, a.Email, string(owner), a.State, a.Reason, a.Run, ts(a.CreatedAt), a.Footprint, clicks)
	return err
}

// SetFootprint keeps url and the clicks after it as the account's footprint ("": none).
func (s *Store) SetFootprint(ctx context.Context, key, url string, clicks []string) error {
	raw, err := clicksJSON(clicks)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE accounts SET footprint = ?, footprint_clicks = ? WHERE key = ?`, url, raw, key)
	return err
}

func clicksJSON(clicks []string) (string, error) {
	if clicks == nil {
		clicks = []string{}
	}
	raw, err := json.Marshal(clicks)
	return string(raw), err
}

func (s *Store) DeleteAccount(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE key = ?`, key)
	return err
}

// Journal appends what happened on a site. Only identifiers go here, never a password or a code.
func (s *Store) Journal(ctx context.Context, key, event, email, phone string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO journal (site, event, email, phone, at) VALUES (?, ?, ?, ?, ?)`,
		key, event, email, phone, ts(time.Now()))
	return err
}

// Registrations counts the times the box started registering on the site, whatever came of each:
// every attempt is journaled before its run starts.
func (s *Store) Registrations(ctx context.Context, key string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal WHERE site = ? AND event = 'registering'`, key).Scan(&n)
	return n, err
}
