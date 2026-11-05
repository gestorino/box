// Package dbmigrate brings a SQLite archive to the schema its daemon expects.
package dbmigrate

import (
	"database/sql"
	"fmt"
)

// Run applies migrations to db: migrations[v] brings it from version v to v+1, each in a transaction
// together with its PRAGMA user_version. A database newer than migrations is an error.
func Run(db *sql.DB, migrations []string) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("database is version %d, newer than this build (%d)", v, len(migrations))
	}
	for ; v < len(migrations); v++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v] + fmt.Sprintf("PRAGMA user_version = %d;", v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
