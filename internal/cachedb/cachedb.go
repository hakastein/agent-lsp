// Package cachedb opens the cache database of the user: one SQLite file under the user's cache directory that every
// language package of agent-lsp keeps its tables in. A table whose rows carry a "used" column (unix seconds of the
// last use) is swept: rows not used for MaxAge are deleted along the way, at most once a day.
package cachedb

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// MaxAge is how long a row of the cache lives without being used.
const MaxAge = 14 * 24 * time.Hour

// sweepEvery is how often Open sweeps the tables.
const sweepEvery = 24 * time.Hour

// Path is the file of the cache database: <os.UserCacheDir()>/agent-lsp/cache.sqlite.
func Path() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "agent-lsp", "cache.sqlite"), nil
}

// Open opens the cache database, runs schema (statements that must be idempotent, such as CREATE TABLE IF NOT
// EXISTS) and sweeps the tables when the last sweep is older than a day.
func Open(schema string) (*sql.DB, error) {
	file, err := Path()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, err
	}
	// A URI with an escaped path: a ? or % in the path is a part of the name, not the start of the query.
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(file), "/")}
	db, err := sql.Open("sqlite", uri.String()+
		"?_pragma=busy_timeout(60000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;` +
		schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if err := sweep(db, time.Now()); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return db, nil
}

// sweep deletes the rows not used for MaxAge from every table with a "used" column, unless the last sweep was less
// than a day before now. Two processes that sweep at once delete the same rows.
func sweep(db *sql.DB, now time.Time) (err error) {
	var last int64
	err = db.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key = 'swept'`).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if now.Sub(time.Unix(last, 0)) < sweepEvery {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		err = tx.Commit()
	}()
	rows, err := tx.Query(`SELECT m.name FROM sqlite_schema m JOIN pragma_table_info(m.name) c
		WHERE m.type = 'table' AND c.name = 'used'`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, t)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, t := range tables {
		if _, err := tx.Exec(`DELETE FROM "`+t+`" WHERE used < ?`, now.Add(-MaxAge).Unix()); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES ('swept', ?)`, fmt.Sprint(now.Unix()))
	return err
}
