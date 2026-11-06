package php_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	agentlsp "github.com/hakastein/agent-lsp"
	"github.com/hakastein/agent-lsp/php"
)

// newRoot copies testdata/<name> into a fresh directory and returns it as the root.
func newRoot(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	require.NoError(t, os.CopyFS(root, os.DirFS(filepath.Join("testdata", name))))
	return root
}

// symlink makes link point at target. Where the OS does not let the user make symlinks, as on Windows without the
// privilege, the test is skipped.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if err != nil && runtime.GOOS == "windows" {
		t.Skipf("no symlinks on this system: %v", err)
	}
	require.NoError(t, err)
}

// cache gives the rest of the test a fresh user cache directory, so indexes of tests never meet and never touch the
// user's cache.
func cache(t *testing.T) {
	t.Helper()
	cacheAt(t, t.TempDir())
}

// cacheAt makes home the user's home and cache directory for the rest of the test.
func cacheAt(t *testing.T, home string) {
	t.Helper()
	for _, name := range []string{"HOME", "XDG_CACHE_HOME", "LOCALAPPDATA"} {
		t.Setenv(name, home)
	}
}

// tryOpen opens the index of root and returns it with the diagnostic lines Open wrote. The index is closed when
// the test ends.
func tryOpen(t *testing.T, root string) (*php.Index, string, error) {
	t.Helper()
	var log strings.Builder
	ix, err := php.Open(root, func(format string, args ...any) { fmt.Fprintf(&log, format+"\n", args...) })
	if err == nil {
		t.Cleanup(func() { require.NoError(t, ix.Close()) })
	}
	return ix, log.String(), err
}

// open is tryOpen of an index that must open.
func open(t *testing.T, root string) (*php.Index, string) {
	t.Helper()
	ix, log, err := tryOpen(t, root)
	require.NoError(t, err)
	return ix, log
}

// Whether Open built the index or reused it is seen only through its diagnostic lines; these tests are their only
// reader.
const (
	built  = "php: index built: "
	reused = "php: index reused: "
)

// place is where a needle stands in a file under the root.
type place struct {
	file   string // relative to the root
	needle string // unique in the file; the place is the identifier it starts with
}

// location is the location of p's identifier.
func (p place) location(t *testing.T, root string) agentlsp.Location {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(p.file))
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(src)
	require.Equal(t, 1, strings.Count(text, p.needle), "needle %q in %s", p.needle, p.file)
	at := strings.Index(text, p.needle)
	word := len(p.needle) - len(strings.TrimLeft(p.needle,
		"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"))
	line := strings.Count(text[:at], "\n") + 1
	column := at - strings.LastIndex(text[:at], "\n")
	return agentlsp.Location{File: path, Line: line, Column: column, EndLine: line, EndColumn: column + word}
}

func (p place) position(t *testing.T, root string) agentlsp.Position {
	t.Helper()
	l := p.location(t, root)
	return agentlsp.Position{File: l.File, Line: l.Line, Column: l.Column}
}

func locations(t *testing.T, root string, places []place) []agentlsp.Location {
	t.Helper()
	out := []agentlsp.Location{}
	for _, p := range places {
		out = append(out, p.location(t, root))
	}
	return out
}

func definition(t *testing.T, ix *php.Index, root string, at place) []agentlsp.Location {
	t.Helper()
	found, err := ix.Definition(at.position(t, root))
	require.NoError(t, err)
	return found
}

// uses is References(at).Uses; the test fails when References answers anything else.
func uses(t *testing.T, ix *php.Index, root string, at place) []agentlsp.Location {
	t.Helper()
	refs, err := ix.References(at.position(t, root))
	require.NoError(t, err)
	require.Empty(t, refs.Implementations)
	require.Empty(t, refs.Unresolved)
	return refs.Uses
}

// problem is what err says is wrong with the position at: its text after "php: position <at>: ", all of it when it
// does not start so, and "" for no error.
func problem(at agentlsp.Position, err error) string {
	if err == nil {
		return ""
	}
	text, _ := strings.CutPrefix(err.Error(), fmt.Sprintf("php: position %s:%d:%d: ", at.File, at.Line, at.Column))
	return text
}
