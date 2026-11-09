package golang_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	agentlsp "github.com/hakastein/agent-lsp"
	"github.com/hakastein/agent-lsp/golang"
)

// newRoot copies testdata (the module shop and the module money it replaces with a directory) into a fresh
// directory and returns the module shop as the root.
func newRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.CopyFS(dir, os.DirFS("testdata")))
	return filepath.Join(dir, "shop")
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

// cache gives the rest of the test a fresh user cache directory, so the caches of tests never meet and never touch
// the user's cache.
func cache(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CACHE_HOME", "LOCALAPPDATA"} {
		t.Setenv(name, home)
	}
}

// session opens root, passes the index to ask and closes it; it returns the diagnostic lines of the index. A
// process has one Go index open at a time.
func session(t *testing.T, root string, ask func(ix *golang.Index)) string {
	t.Helper()
	var log strings.Builder
	ix, err := golang.Open(root, func(format string, args ...any) { fmt.Fprintf(&log, format+"\n", args...) })
	require.NoError(t, err)
	ask(ix)
	require.NoError(t, ix.Close())
	return log.String()
}

// Whether a load of packages or the gopls cache was reused is seen only through the diagnostic lines; these tests
// are their only reader.
const (
	loaded      = "go: packages loaded: "
	reused      = "go: packages reused: "
	cacheUnused = "go: cache: 0 reused,"
)

// place is where a needle stands in a file under the root.
type place struct {
	file   string // relative to the root, with forward slashes
	needle string // unique in the file; the place is the identifier it starts with
}

// at is the offset of the needle in its file and the file's text.
func (p place) at(t *testing.T, root string) (string, string, int) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(p.file))
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(src)
	require.Equal(t, 1, strings.Count(text, p.needle), "needle %q in %s", p.needle, p.file)
	return path, text, strings.Index(text, p.needle)
}

// location is the location of p's identifier.
func (p place) location(t *testing.T, root string) agentlsp.Location {
	t.Helper()
	path, text, at := p.at(t, root)
	word := len(p.needle) - len(strings.TrimLeft(p.needle,
		"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_"))
	line, column := lineColumn(text, at)
	return agentlsp.Location{File: path, Line: line, Column: column, EndLine: line, EndColumn: column + word}
}

// locations is the locations of the identifiers of places; nil for none.
func locations(t *testing.T, root string, places []place) []agentlsp.Location {
	t.Helper()
	var out []agentlsp.Location
	for _, p := range places {
		out = append(out, p.location(t, root))
	}
	return out
}

func (p place) position(t *testing.T, root string) agentlsp.Position {
	t.Helper()
	l := p.location(t, root)
	return agentlsp.Position{File: l.File, Line: l.Line, Column: l.Column}
}

// span is the range of a file from the start of the needle from to the end of the needle to; both are unique.
func span(t *testing.T, root, file, from, to string) agentlsp.Location {
	t.Helper()
	path, text, start := place{file, from}.at(t, root)
	_, _, end := place{file, to}.at(t, root)
	l := agentlsp.Location{File: path}
	l.Line, l.Column = lineColumn(text, start)
	l.EndLine, l.EndColumn = lineColumn(text, end+len(to))
	return l
}

func lineColumn(text string, offset int) (int, int) {
	return strings.Count(text[:offset], "\n") + 1, offset - strings.LastIndex(text[:offset], "\n")
}

// decl is a declaration under the root: its name, the needles that start and end it, its signature and doc.
type decl struct {
	name      place
	from, to  string
	signature string
	doc       string
	origin    agentlsp.Origin
}

func (d decl) declaration(t *testing.T, root string) agentlsp.Declaration {
	t.Helper()
	return agentlsp.Declaration{
		Location:  d.name.location(t, root),
		Range:     span(t, root, d.name.file, d.from, d.to),
		Signature: d.signature,
		Doc:       d.doc,
		Origin:    d.origin,
	}
}

func declarations(t *testing.T, root string, decls []decl) []agentlsp.Declaration {
	t.Helper()
	out := []agentlsp.Declaration{}
	for _, d := range decls {
		out = append(out, d.declaration(t, root))
	}
	return out
}

// symbol is a symbol under the root.
type symbol struct {
	name      string
	kind      agentlsp.Kind
	container string
	decl
}

func symbols(t *testing.T, root string, syms []symbol) []agentlsp.Symbol {
	t.Helper()
	out := []agentlsp.Symbol{}
	for _, s := range syms {
		out = append(out, agentlsp.Symbol{Name: s.name, Kind: s.kind,
			Container: s.container, Declaration: s.declaration(t, root)})
	}
	return out
}

// brokenGo is a file of package cart an edit has left with a syntax error: a function cut short at the end of the file.
// It is not in testdata, since go vet of the root fails on it: writeBroken writes it.
const brokenGo = "cart/broken.go"

func writeBroken(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(brokenGo)),
		[]byte("package cart\n\n// Ready is complete.\nfunc Ready() {}\n\nfunc Broken( {\n"), 0o600))
}

// edit replaces old, which must stand once in the file, with new.
func edit(t *testing.T, root, file, old, new string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(file))
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(src), old))
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(src), old, new, 1)), 0o600))
}

// renames is the edits that rename the identifiers of places to name.
func renames(t *testing.T, root string, places []place, name string) []agentlsp.Edit {
	t.Helper()
	var out []agentlsp.Edit
	for _, p := range places {
		out = append(out, agentlsp.Edit{Location: p.location(t, root), NewText: name})
	}
	return out
}

// apply makes the edits in their files. The edits come in the order of files, lines and columns, so making them from
// the last keeps the places of the earlier ones.
func apply(t *testing.T, edits []agentlsp.Edit) {
	t.Helper()
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		src, err := os.ReadFile(e.File)
		require.NoError(t, err)
		text := string(src)
		text = text[:offset(text, e.Line, e.Column)] + e.NewText + text[offset(text, e.EndLine, e.EndColumn):]
		require.NoError(t, os.WriteFile(e.File, []byte(text), 0o600))
	}
}

// offset is the offset of the 1-based line and column in bytes in text.
func offset(text string, line, column int) int {
	start := 0
	for range line - 1 {
		start += strings.Index(text[start:], "\n") + 1
	}
	return start + column - 1
}

// vet is the output of go vet ./... in root for each of linux, darwin and windows; "" for none. gocache is the build
// cache of the go command: the user cache directory of the test is fresh.
func vet(t *testing.T, root, gocache string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		cmd := exec.Command("go", "vet", "./...")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0", "GOPROXY=off", "GOFLAGS=", "GOCACHE="+gocache)
		text, _ := cmd.CombinedOutput()
		out[goos] = string(text)
	}
	return out
}

// goEnv is the value of a variable of the go command.
func goEnv(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("go", "env", name).Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

// problem is what err says is wrong with the position at: its text after "go: position <at>: ", all of it when it
// does not start so, and "" for no error.
func problem(at agentlsp.Position, err error) string {
	if err == nil {
		return ""
	}
	text, _ := strings.CutPrefix(err.Error(), fmt.Sprintf("go: position %s:%d:%d: ", at.File, at.Line, at.Column))
	return text
}
