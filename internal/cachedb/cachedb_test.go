package cachedb_test

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hakastein/agent-lsp/internal/cachedb"
)

const schema = `CREATE TABLE IF NOT EXISTS facts (key TEXT PRIMARY KEY, used INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS notes (key TEXT PRIMARY KEY, used INTEGER NOT NULL);`

// cache gives the rest of the test a fresh user cache directory.
func cache(t *testing.T) {
	t.Helper()
	for _, name := range []string{"HOME", "XDG_CACHE_HOME", "LOCALAPPDATA"} {
		t.Setenv(name, t.TempDir())
	}
}

// Open sweeps every table with a "used" column: a row not used for 14 days goes, a younger one stays.
func TestOpenSweepsUnusedRows(t *testing.T) {
	cache(t)
	now := time.Now()
	db, err := cachedb.Open(schema)
	require.NoError(t, err)
	for _, table := range []string{"facts", "notes"} {
		for key, age := range map[string]time.Duration{"used 15 days ago": 15 * 24 * time.Hour, "used 13 days ago": 13 * 24 * time.Hour} {
			_, err := db.Exec(`INSERT INTO `+table+` (key, used) VALUES (?, ?)`, key, now.Add(-age).Unix())
			require.NoError(t, err)
		}
	}
	_, err = db.Exec(`UPDATE meta SET value = '0' WHERE key = 'swept'`) // the last sweep was long ago
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = cachedb.Open(schema)
	require.NoError(t, err)
	defer db.Close()

	for _, table := range []string{"facts", "notes"} {
		t.Run(table, func(t *testing.T) {
			assert.Equal(t, []string{"used 13 days ago"}, keys(t, db, table))
		})
	}
}

// A cache file that is not a database holds nothing that cannot be computed again: Open makes a new one.
func TestOpenRecreatesBrokenCache(t *testing.T) {
	cache(t)
	garbage(t)

	db, err := cachedb.Open(schema)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`INSERT INTO facts (key, used) VALUES (?, ?)`, "fact", time.Now().Unix())

	assert.NoError(t, err)
	assert.Equal(t, []string{"fact"}, keys(t, db, "facts"))
}

// Any other error of opening the cache file is an error: a directory in its place is not removed.
func TestOpenFailsOnACacheFileItCannotOpen(t *testing.T) {
	cache(t)
	file, err := cachedb.Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(file, 0o755))

	_, err = cachedb.Open(schema)

	assert.Error(t, err)
	assert.DirExists(t, file)
}

// Processes that open the cache database at once all open it, whether its file is not a database or is not there.
func TestOpenAtOnce(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T)
	}{
		{"a file that is not a database", garbage},
		{"no file", func(*testing.T) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache(t)
			tc.prepare(t)

			assert.Equal(t, make([]error, 8), openAtOnce(8))
		})
	}
}

// keys is the keys of the rows of table.
func keys(t *testing.T, db *cachedb.DB, table string) []string {
	t.Helper()
	var out []string
	rows, err := db.Query(`SELECT key FROM ` + table)
	require.NoError(t, err)
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		out = append(out, key)
	}
	require.NoError(t, rows.Err())
	return out
}

// garbage puts a file that is not a database in place of the cache database.
func garbage(t *testing.T) {
	t.Helper()
	file, err := cachedb.Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte("garbage garbage garbage"), 0o600))
}

// openAtOnce opens the cache database n times at once, each with connections of its own, as n processes would; it
// writes a row through each and returns the errors.
func openAtOnce(n int) []error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			db, err := cachedb.Open(schema)
			if err != nil {
				errs[i] = err
				return
			}
			_, err = db.Exec(`INSERT OR REPLACE INTO facts (key, used) VALUES (?, ?)`, "fact", time.Now().Unix())
			errs[i] = errors.Join(err, db.Close())
		})
	}
	wg.Wait()
	return errs
}
