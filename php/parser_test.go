package php_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentlsp "github.com/hakastein/agent-lsp"
)

// usesOf asks one build of the library for the uses at a place and returns them with its diagnostic lines.
type usesOf func(t *testing.T, root string, at place) ([]agentlsp.Location, string)

// fork is this build: the library linked with the parser go.mod replaces the parser with.
func fork(t *testing.T, root string, at place) ([]agentlsp.Location, string) {
	t.Helper()
	ix, log := open(t, root)
	return uses(t, ix, root, at), log
}

// upstream builds testdata/upstream from this module with the php-parser release instead of the fork go.mod
// replaces it with: the same library with another parser. The build needs the module proxy.
func upstream(t *testing.T) usesOf {
	t.Helper()
	src := t.TempDir()
	for _, name := range []string{"go.mod", "go.sum", "agentlsp.go"} {
		data, err := os.ReadFile(filepath.Join("..", name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(src, name), data, 0o600))
	}
	require.NoError(t, os.CopyFS(filepath.Join(src, "php"), os.DirFS(".")))
	require.NoError(t, os.CopyFS(filepath.Join(src, "internal", "position"), os.DirFS(filepath.Join("..", "internal", "position"))))
	require.NoError(t, os.CopyFS(filepath.Join(src, "upstream"), os.DirFS(filepath.Join("testdata", "upstream"))))
	bin := filepath.Join(t.TempDir(), "upstream")
	for _, args := range [][]string{
		{"mod", "edit", "-dropreplace=github.com/VKCOM/php-parser"},
		{"build", "-mod=mod", "-o", bin, "./upstream"},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = src
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "go %v:\n%s", args, out)
	}
	return func(t *testing.T, root string, at place) ([]agentlsp.Location, string) {
		t.Helper()
		pos := at.position(t, root)
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(bin, root, pos.File, strconv.Itoa(pos.Line), strconv.Itoa(pos.Column))
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		require.NoError(t, cmd.Run(), "stderr:\n%s", &stderr)
		var found []agentlsp.Location
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &found))
		return found, stderr.String()
	}
}

// An index built with another parser holds the facts that parser extracted; the snapshot of files alone cannot tell.
// Each build makes its own index whichever ran first, and answers by the facts of its own parser.
func TestIndexOfAnotherParserIsNotReused(t *testing.T) {
	upstream := upstream(t)

	tests := []struct {
		name          string
		first, second usesOf
		want          []place
	}{
		{"fork after upstream", upstream, fork, []place{enumCall}},
		{"upstream after fork, which loses the class named Enum", fork, upstream, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache(t)
			root := newRoot(t, "syntax")
			tt.first(t, root, enumLabel)
			got, log := tt.second(t, root, enumLabel)

			assert.ElementsMatch(t, locations(t, root, tt.want), got)
			assert.Contains(t, log, built)
			assert.NotContains(t, log, reused)
		})
	}
}
