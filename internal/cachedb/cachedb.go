// Package cachedb opens the cache database of the user: one SQLite file under the user's cache directory that the
// golang package keeps its tables in (the php package keeps its index in a file of its own per root). A table whose rows carry a "used" column (unix seconds of the
// last use) is swept: rows not used for MaxAge are deleted along the way, at most once a day.
package cachedb

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
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
	gen  []byte // the content of the lock file at Open; it changes whenever the database file is removed
}

// Open opens the cache database, runs schema (statements that must be idempotent, such as CREATE TABLE IF NOT
// EXISTS) and sweeps the tables when the last sweep is older than a day. A file that is not a database or is
// damaged is made again: what the cache holds can be computed again. A damaged file that cannot be removed, as on
// Windows while another process holds it open, leaves this Open a database in memory, and the next Open removes it.
// Processes open the database one at a time, under the lock of the file <Path()>.lock.
func Open(schema string) (*DB, error) {
	file, err := Path()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, err
	}
	lock, gen, err := lockDB(file)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	var db *sql.DB
	if !bytes.Equal(gen, unremoved) {
		db, err = open(file, schema)
	}
	if bytes.Equal(gen, unremoved) || Damaged(err) {
		if gen, err = remove(lock, file); err == nil {
			db, err = open(file, schema)
		} else {
			db, err = open("", schema)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return &DB{DB: db, file: file, gen: gen}, nil
}

// CloseAfter closes db after err and returns what is left of err with the error of closing. An err that says the
// database is damaged removes the database file, so that the next Open makes a new one; an err after the file was
// made again since db was opened is of a file that no Open opens again. Both are dropped, and the damaged file is
// removed only when it was not made again; a file that cannot be removed is left to the next Open.
func (db *DB) CloseAfter(err error) error {
	closeErr := db.Close()
	if err == nil {
		return closeErr
	}
	lock, gen, lockErr := lockDB(db.file)
	if lockErr != nil {
		return errors.Join(err, closeErr, lockErr)
	}
	defer lock.Close()
	if !bytes.Equal(gen, db.gen) {
		return closeErr
	}
	if Damaged(err) {
		remove(lock, db.file)
		return closeErr
	}
	return errors.Join(err, closeErr)
}

// Damaged reports whether err says that the database file is not a database or is damaged.
func Damaged(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && (e.Code()&0xff == sqlite3.SQLITE_NOTADB || e.Code()&0xff == sqlite3.SQLITE_CORRUPT)
}

// lockDB waits for the lock of the database file and returns the lock file, which its Close releases, and the
// content of the lock file.
func lockDB(file string) (*os.File, []byte, error) {
	lock, err := os.OpenFile(file+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err := lockFile(lock); err != nil {
		lock.Close()
		return nil, nil, err
	}
	gen, err := io.ReadAll(lock)
	if err != nil {
		lock.Close()
		return nil, nil, err
	}
	return lock, gen, nil
}

// unremoved is the content of the lock file while the database file is damaged and could not be removed: the next
// Open removes it.
var unremoved = []byte("unremoved")

// remove removes the database file under the lock and writes a new content of the lock file, which it returns. A
// file it cannot remove, it marks unremoved and returns the error.
func remove(lock *os.File, file string) ([]byte, error) {
	gen, err := fmt.Appendf(nil, "%016x", time.Now().UnixNano()), error(nil)
	for _, f := range []string{file, file + "-wal", file + "-shm"} {
		if e := os.Remove(f); e != nil && !errors.Is(e, fs.ErrNotExist) {
			gen, err = unremoved, e
			break
		}
	}
	if e := lock.Truncate(0); e != nil {
		return gen, errors.Join(err, e)
	}
	_, e := lock.WriteAt(gen, 0)
	return gen, errors.Join(err, e)
}

// open opens the database file, or a database in memory for no file.
func open(file, schema string) (*sql.DB, error) {
	name := ":memory:"
	if file != "" {
		// A URI with an escaped path: a ? or % in the path is a part of the name, not the start of the query.
		uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(file), "/")}
		name = uri.String()
	}
	db, err := sql.Open("sqlite", name+
		"?_pragma=busy_timeout(60000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	if file == "" {
		db.SetMaxOpenConns(1) // every connection to :memory: is a database of its own
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
