package cachedb_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hakastein/agent-lsp/internal/cachedb"
)

const schema = `CREATE TABLE IF NOT EXISTS facts (key TEXT PRIMARY KEY, used INTEGER NOT NULL);`

// Open sweeps every table with a "used" column: a row not used for 14 days goes, a younger one stays.
func TestOpenSweepsUnusedRows(t *testing.T) {
	for _, name := range []string{"HOME", "XDG_CACHE_HOME", "LOCALAPPDATA"} {
		t.Setenv(name, t.TempDir())
	}
	now := time.Now()
	db, err := cachedb.Open(schema)
	require.NoError(t, err)
	for key, age := range map[string]time.Duration{"used 15 days ago": 15 * 24 * time.Hour, "used 13 days ago": 13 * 24 * time.Hour} {
		_, err := db.Exec(`INSERT INTO facts (key, used) VALUES (?, ?)`, key, now.Add(-age).Unix())
		require.NoError(t, err)
	}
	_, err = db.Exec(`UPDATE meta SET value = '0' WHERE key = 'swept'`) // the last sweep was long ago
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = cachedb.Open(schema)
	require.NoError(t, err)
	defer db.Close()
	var kept []string
	rows, err := db.Query(`SELECT key FROM facts`)
	require.NoError(t, err)
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		kept = append(kept, key)
	}
	require.NoError(t, rows.Err())

	assert.Equal(t, []string{"used 13 days ago"}, kept)
}
