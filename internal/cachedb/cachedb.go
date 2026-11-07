// Package cachedb opens the cache database of the user: one SQLite file under the user's cache directory that the
// golang and php packages keep their tables in. A table whose rows carry a "used" column (unix seconds of the last
// use) is swept: rows not used for MaxAge are deleted along the way, at most once a day. Foreign keys are enforced:
// a row that references a swept row with ON DELETE CASCADE goes with it.
package cachedb

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
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

// DB is the cache database Open opened.
type DB struct {
	*sql.DB
	file string
}

// Open opens the cache database, runs schema (statements that must be idempotent, such as CREATE TABLE IF NOT
// EXISTS) and sweeps the tables when the last sweep is older than a day. A file that is not a database or is
// damaged is removed and made again empty: what the cache holds is computed again. A damaged file that cannot be
// removed is an error that names it. Processes open the database one at a time, under the lock of the file
// <Path()>.lock.
func Open(schema string) (*DB, error) {
	file, err := Path()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, err
	}
	lock, err := lockDB(file)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	db, err := open(file, schema)
	if damaged(err) {
		if err = remove(file); err == nil {
			db, err = open(file, schema)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return &DB{DB: db, file: file}, nil
}

// Err is err with the path of the database file before it when err is an error of SQLite, so that a person can
// remove a file damaged after Open; any other err is returned as it is.
func (db *DB) Err(err error) error {
	var e *sqlite.Error
	if errors.As(err, &e) {
		return fmt.Errorf("%s: %w", db.file, err)
	}
	return err
}

// damaged reports whether err says that the database file is not a database or is damaged.
func damaged(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && (e.Code()&0xff == sqlite3.SQLITE_NOTADB || e.Code()&0xff == sqlite3.SQLITE_CORRUPT)
}

// lockDB waits for the lock of the database file and returns the lock file, whose Close releases it.
func lockDB(file string) (*os.File, error) {
	lock, err := os.OpenFile(file+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(lock); err != nil {
		lock.Close()
		return nil, err
	}
	return lock, nil
}

// remove removes the database file and the files of its write-ahead log.
func remove(file string) error {
	for _, f := range []string{file, file + "-wal", file + "-shm"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// open opens the database file.
func open(file, schema string) (*sql.DB, error) {
	// A URI with an escaped path: a ? or % in the path is a part of the name, not the start of the query.
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(file), "/")}
	db, err := sql.Open("sqlite", uri.String()+
		"?_pragma=busy_timeout(60000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"+
		"&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;` +
		schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := sweep(db, time.Now()); err != nil {
		db.Close()
		return nil, err
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
