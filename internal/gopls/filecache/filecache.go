// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The filecache package provides a shared durable blob cache.
//
// The cache is a mapping from (kind string, key [32]byte) to []byte,
// where kind is an identifier describing the namespace or purpose
// (e.g. "analysis"), and key is a SHA-256 digest of the recipe of the
// value. (It need not be the digest of the value itself, so you can
// query the cache without knowing what value the recipe would produce.)
//
// The Get and Set operations are concurrency-safe.
//
// In the agent-lsp fork (see ../FORK.md) this file is rewritten: the
// cache lives in a table of an SQLite database that [Open] names, not
// in files; [Set] buffers values in memory and [Flush] writes them.
// Without [Open] the cache lives in memory only.
package filecache

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hakastein/agent-lsp/internal/gopls/util/lru"
)

// memCache is a 100MB in-memory LRU cache in front of the database.
// It holds either raw bytes (written by [Set]) or decoded objects
// (written by [Get]).
var memCache = lru.New[memKey, any](100 * 1e6)

type memKey struct {
	kind string
	key  [32]byte
}

// ErrNotFound is the distinguished error
// returned by Get when the key is not found.
var ErrNotFound = fmt.Errorf("not found")

// Bytes is the identity decoder, for use with [Get] when the caller
// wants the raw bytes.
func Bytes(data []byte) []byte { return data }

// touchAfter is how old the last use of a row must be for a hit to record a new one.
const touchAfter = time.Hour

var (
	mu      sync.Mutex
	db      *sql.DB
	table   string
	salt    []byte
	pending = map[[32]byte][]byte{} // values Set since the last Flush, by row key
	touched = map[[32]byte]bool{}   // rows hit since the last Flush whose use is older than touchAfter

	hits, misses int
)

// Open makes the cache live in the table of db; the table is created if it does not exist. salt
// enters every row key, so values written under another salt are never read. Values buffered
// by Set and not flushed are dropped.
func Open(d *sql.DB, name string, s []byte) error {
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS "` + name + `" (
		key BLOB PRIMARY KEY, value BLOB NOT NULL, used INTEGER NOT NULL);
		CREATE INDEX IF NOT EXISTS "` + name + `_used" ON "` + name + `" (used);`); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	db, table, salt = d, name, s
	clear(pending)
	clear(touched)
	hits, misses = 0, 0
	memCache = lru.New[memKey, any](100 * 1e6)
	return nil
}

// Stats is the number of reads from the database that found a value and that did not, since Open.
func Stats() (hit, miss int) {
	mu.Lock()
	defer mu.Unlock()
	return hits, misses
}

// Flush writes the values buffered by Set and the times of use of the rows read since the last
// Flush in one transaction, and returns how many values it wrote.
func Flush() (n int, err error) {
	mu.Lock()
	d, values, uses := db, pending, touched
	pending, touched = map[[32]byte][]byte{}, map[[32]byte]bool{}
	mu.Unlock()
	if d == nil || len(values)+len(uses) == 0 {
		return 0, nil
	}
	tx, err := d.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		err = tx.Commit()
	}()
	now := time.Now().Unix()
	for k, v := range values {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO "`+table+`" (key, value, used) VALUES (?, ?, ?)`, k[:], v, now); err != nil {
			return 0, err
		}
	}
	for k := range uses {
		if _, err := tx.Exec(`UPDATE "`+table+`" SET used = ? WHERE key = ?`, now, k[:]); err != nil {
			return 0, err
		}
	}
	return len(values), nil
}

// rowKey is the key of the row of (kind, key): the salt enters it.
func rowKey(kind string, key [32]byte) [32]byte {
	h := sha256.New()
	h.Write(salt)
	fmt.Fprintf(h, "\x00%s\x00", kind)
	h.Write(key[:])
	var k [32]byte
	h.Sum(k[:0])
	return k
}

// Get retrieves from the cache the value most recently supplied to
// Set(kind, key), possibly by another process, and passes it through
// decode. The decoded result is cached in memory so that repeated
// reads avoid both I/O and deserialization.
//
// Get returns [ErrNotFound] if the value was not found.
//
// Each kind must be used with exactly one decoded type T; mixing types
// for the same kind is a programming error and will panic.
// The returned value may be shared with other callers and must not be
// mutated.
func Get[T any](kind string, key [32]byte, decode func([]byte) T) (T, error) {
	var zero T
	mk := memKey{kind, key}

	var data []byte
	if v, ok := memCache.Get(mk); ok {
		if t, ok := v.(T); ok {
			return t, nil
		}
		if data, ok = v.([]byte); !ok {
			panic(fmt.Sprintf("filecache: kind %q used with multiple decoded types (got %T, want %T)", kind, v, zero))
		}
	} else {
		var err error
		if data, err = get(kind, key); err != nil {
			return zero, err
		}
	}

	result := decode(data)
	memCache.Set(mk, result, len(data))
	return result, nil
}

// GetOrFatal retrieves from the cache like [Get]. Any error other than
// [ErrNotFound] is a miss as well: the value is computed again.
func GetOrFatal[T any](kind string, key [32]byte, decode func([]byte) T) (T, bool) {
	val, err := Get(kind, key, decode)
	if err == nil {
		return val, true // hit
	}
	return *new(T), false // miss
}

// get reads the value for (kind, key) from the database, bypassing
// the in-memory cache.
func get(kind string, key [32]byte) ([]byte, error) {
	mu.Lock()
	d, k := db, rowKey(kind, key)
	if v, ok := pending[k]; ok {
		mu.Unlock()
		return v, nil
	}
	mu.Unlock()
	if d == nil {
		return nil, ErrNotFound
	}
	var value []byte
	var used int64
	err := d.QueryRow(`SELECT value, used FROM "`+table+`" WHERE key = ?`, k[:]).Scan(&value, &used)
	mu.Lock()
	defer mu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		misses++
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	hits++
	if time.Since(time.Unix(used, 0)) > touchAfter {
		touched[k] = true
	}
	return value, nil
}

// Set updates the value in the cache. The value reaches the database
// on the next [Flush].
func Set(kind string, key [32]byte, value []byte) error {
	memCache.Set(memKey{kind, key}, value, len(value))
	mu.Lock()
	defer mu.Unlock()
	pending[rowKey(kind, key)] = value
	return nil
}
