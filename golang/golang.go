// Package golang answers questions about the Go code of a root: a module or a workspace of a go.work file. The
// answers come from a fork of the internal packages of gopls, called directly, without the LSP protocol. The cache of
// gopls (export data, indexes of symbols, references and method sets) and the packages the go command lists for the
// root live in the cache database of the user, so a call in a new process reuses what an earlier one computed. The
// package is named golang, as the package of gopls that answers these questions: go is a keyword.
package golang

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	agentlsp "github.com/hakastein/agent-lsp"
	"github.com/hakastein/agent-lsp/internal/cachedb"
	"github.com/hakastein/agent-lsp/internal/gopls/cache"
	"github.com/hakastein/agent-lsp/internal/gopls/filecache"
	gopls "github.com/hakastein/agent-lsp/internal/gopls/golang"
	"github.com/hakastein/agent-lsp/internal/gopls/protocol"
	"github.com/hakastein/agent-lsp/internal/gopls/settings"
	"github.com/hakastein/agent-lsp/internal/position"
)

// forkVersion names the gopls the fork is taken from and the revision of the fork's changes; see
// internal/gopls/FORK.md. A new revision is a new name.
const forkVersion = "gopls v0.23.0 fork 1"

// salt enters every key of the cache: the schema of the Go tables, the fork, and the Go version the binary is built
// with, whose go/types type-checks. A value written by another build of either is never read.
var salt = "go1 " + forkVersion + " " + runtime.Version()

// blobsTable is the table of the gopls cache.
const blobsTable = "go1_blobs"

// open guards the one Go index a process may have open: the gopls cache is global to the process.
var open sync.Mutex

// errCacheGone is an error of openIndex on a cache database that it found damaged and removed, or that another
// process made again meanwhile: openIndex may run again on the new one.
var errCacheGone = errors.New("go: cache database damaged or made again")

// Index answers questions about the Go code of one root.
type Index struct {
	root    string
	env     cache.GoEnv
	logf    func(string, ...any)
	session *cache.Session
	view    *cache.Snapshot // the gopls view of the root as Open loaded it
	release func()
	db      *cachedb.DB
	loader  *loader
}

// Open opens the Go code of root, a directory with a go.mod or a go.work file, and loads its packages: the stored
// list of packages when the files it depends on are the same, the go command otherwise. A process has one Go index
// open at a time; Open fails while another is. logf, if not nil, receives diagnostic lines; they are not a contract.
func Open(root string, logf func(format string, args ...any)) (*Index, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if !exists(filepath.Join(root, "go.mod")) && !exists(filepath.Join(root, "go.work")) {
		return nil, fmt.Errorf("go: %s: no go.mod or go.work", root)
	}
	if !open.TryLock() {
		return nil, errors.New("go: another Go index is open in this process")
	}
	ix, err := openIndex(root, logf)
	if errors.Is(err, errCacheGone) {
		logf("%v; opening again", err)
		ix, err = openIndex(root, logf)
	}
	if err != nil {
		open.Unlock()
		return nil, err
	}
	return ix, nil
}

func openIndex(root string, logf func(string, ...any)) (_ *Index, err error) {
	db, err := cachedb.Open(loadSchema)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil && db.CloseAfter(err) == nil {
			err = fmt.Errorf("%w: %w", errCacheGone, err)
		}
	}()
	if err := filecache.Open(db.DB, blobsTable, []byte(salt)); err != nil {
		return nil, err
	}
	ctx := context.Background()
	dir := protocol.URIFromPath(root)
	opts := settings.DefaultOptions()
	env, err := cache.FetchGoEnv(ctx, dir, opts)
	if err != nil {
		return nil, fmt.Errorf("go: %w", err)
	}
	l := &loader{db: db.DB, env: *env, logf: logf}
	session := cache.NewSession(ctx, cache.New(nil))
	_, view, release, err := session.NewView(ctx, &cache.Folder{
		Dir: dir, Name: filepath.Base(root), Options: opts, Env: *env, Load: l.load,
	})
	if err != nil {
		session.Shutdown(ctx)
		return nil, fmt.Errorf("go: %w", err)
	}
	view.AwaitInitialized(ctx)
	if e := view.InitializationError(); e != nil {
		release()
		session.Shutdown(ctx)
		return nil, fmt.Errorf("go: %w", e.MainError)
	}
	if l.loaded > 0 {
		logf("go: packages loaded: %d", l.loaded)
	} else {
		logf("go: packages reused: %d", l.reused)
	}
	return &Index{root: root, env: *env, logf: logf, session: session, view: view, release: release,
		db: db, loader: l}, nil
}

// Close writes what the gopls cache computed to the cache database and closes it. A cache database found damaged
// is removed instead, and the next Open makes it again; one that cannot be removed now, the next Open removes. Nor
// is it an error that what was computed misses a cache database another process has made again meanwhile. A second
// Close does nothing.
func (ix *Index) Close() error {
	if ix.session == nil {
		return nil
	}
	defer open.Unlock()
	ix.release()
	ix.session.Shutdown(context.Background())
	ix.session = nil
	hits, misses := filecache.Stats()
	written, err := filecache.Flush()
	ix.logf("go: cache: %d reused, %d missed, %d written", hits, misses, written)
	if !cachedb.Damaged(err) {
		err = errors.Join(err, ix.loader.damaged) // one damage is enough: CloseAfter removes the database
	}
	if cachedb.Damaged(err) {
		ix.logf("go: cache database damaged: %v", err)
	}
	return ix.db.CloseAfter(err)
}

// Definition is the declarations of the symbol at pos, which may be in the standard library or a dependency. On a
// declaration, it is that declaration. A place in a file of the root where no symbol is has none. A position that is
// not a place in a file of the root is an error that repeats pos and names the problem, as in every language.
func (ix *Index) Definition(pos agentlsp.Position) ([]agentlsp.Declaration, error) {
	src, err := position.Check(ix.root, pos)
	if err != nil {
		return nil, fmt.Errorf("go: position %s:%d:%d: %w", pos.File, pos.Line, pos.Column, err)
	}
	if !strings.HasSuffix(pos.File, ".go") {
		return nil, nil
	}
	ctx := context.Background()
	uri := protocol.URIFromPath(pos.File)
	fh, err := ix.view.ReadFile(ctx, uri)
	if err != nil {
		return nil, fmt.Errorf("go: %w", err)
	}
	at, err := protocol.NewMapper(uri, src).OffsetPosition(position.Offset(src, pos))
	if err != nil {
		return nil, fmt.Errorf("go: %w", err)
	}
	locs, err := gopls.Definition(ctx, ix.view, fh, protocol.Range{Start: at, End: at})
	if err != nil {
		return nil, fmt.Errorf("go: definition at %s:%d:%d: %w", pos.File, pos.Line, pos.Column, err)
	}
	var out []agentlsp.Declaration
	for _, loc := range locs {
		d, err := ix.declarationAt(loc)
		if err != nil {
			return nil, fmt.Errorf("go: %w", err)
		}
		out = append(out, d)
	}
	return out, nil
}

// declarationAt is the declaration whose name is at loc. A place that names no declaration, such as the results of
// a function a return statement leads to, is a declaration of itself with the text of its line as the signature.
func (ix *Index) declarationAt(loc protocol.Location) (agentlsp.Declaration, error) {
	s, err := parse(loc.URI.Path())
	if err != nil {
		return agentlsp.Declaration{}, err
	}
	start, end, err := protocol.NewMapper(loc.URI, s.src).RangeOffsets(loc.Range)
	if err != nil {
		return agentlsp.Declaration{}, err
	}
	tok := s.fset.File(s.file.Pos())
	d, ok := s.definition(tok.Pos(start))
	if !ok {
		l := s.location(tok.Pos(start), tok.Pos(end))
		line := s.src[tok.LineStart(l.Line)-token.Pos(tok.Base()):]
		text, _, _ := strings.Cut(string(line), "\n")
		d = agentlsp.Declaration{Location: l, Range: l, Signature: strings.TrimSpace(text)}
	}
	d.Origin = ix.origin(s.path)
	return d, nil
}

// Symbols is the declarations of the packages of the root with the name: a bare name (Symbols), a name in its type
// (Snapshot.Symbols), a name in its package (cache.New, cache.Snapshot.Symbols); a package is named by its name or
// its path. A bare name matches the last segment of every qualified name. Declarations come in the order of their
// files and lines.
func (ix *Index) Symbols(name string) ([]agentlsp.Symbol, error) {
	ctx := context.Background()
	last := name[strings.LastIndex(name, ".")+1:]
	var ids []cache.PackageID
	for id := range ix.view.WorkspacePackages().All() {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	index, err := ix.view.Symbols(ctx, ids...)
	if err != nil {
		return nil, fmt.Errorf("go: symbols: %w", err)
	}
	type candidate struct{ pkgName, pkgPath string }
	files := map[string]candidate{}
	for i, pkg := range index {
		mp := ix.view.Metadata(ids[i])
		for j, syms := range pkg.Symbols {
			for _, sym := range syms {
				if sym.Name[strings.LastIndex(sym.Name, ".")+1:] == last {
					files[pkg.Files[j].Path()] = candidate{string(mp.Name), string(mp.PkgPath)}
				}
			}
		}
	}
	var out []agentlsp.Symbol
	for _, path := range slices.Sorted(maps.Keys(files)) {
		s, err := parse(path)
		if err != nil {
			return nil, fmt.Errorf("go: symbols: %w", err)
		}
		pkg := files[path]
		for _, sym := range s.symbols() {
			full := sym.Name
			if sym.Container != "" {
				full = sym.Container + "." + full
			}
			if matches(pkg.pkgName+"."+full, name) || matches(pkg.pkgPath+"."+full, name) {
				sym.Origin = ix.origin(path)
				out = append(out, sym)
			}
		}
	}
	return out, nil
}

// matches reports whether name is qualified by its last segments or is the whole qualified name.
func matches(qualified, name string) bool {
	return qualified == name || strings.HasSuffix(qualified, "."+name) || strings.HasSuffix(qualified, "/"+name)
}

// Outline is the symbols of a .go file of the root in the order of their declarations: functions, methods, types,
// the fields of struct types and the methods of interface types, variables and constants. A file that is not a file
// of the root is an error.
func (ix *Index) Outline(file string) ([]agentlsp.Symbol, error) {
	if _, err := position.Check(ix.root, agentlsp.Position{File: file, Line: 1, Column: 1}); err != nil {
		return nil, fmt.Errorf("go: outline %s: %w", file, err)
	}
	s, err := parse(file)
	if err != nil {
		return nil, fmt.Errorf("go: outline %s: %w", file, err)
	}
	syms := s.symbols()
	for i := range syms {
		syms[i].Origin = ix.origin(file)
	}
	return syms, nil
}

// References is not supported.
func (ix *Index) References(pos agentlsp.Position) (agentlsp.References, error) {
	return agentlsp.References{}, fmt.Errorf("go: references: %w", agentlsp.ErrNotSupported)
}

// Rename is not supported.
func (ix *Index) Rename(pos agentlsp.Position, name string) (agentlsp.Renaming, error) {
	return agentlsp.Renaming{}, fmt.Errorf("go: rename: %w", agentlsp.ErrNotSupported)
}

// origin says where the file at path lives.
func (ix *Index) origin(path string) agentlsp.Origin {
	if under(ix.root, path) {
		if slices.Contains(strings.Split(filepath.ToSlash(path[len(ix.root):]), "/"), "vendor") {
			return agentlsp.OriginVendor
		}
		return agentlsp.OriginRoot
	}
	if ix.env.GOROOT != "" && under(ix.env.GOROOT, path) {
		return agentlsp.OriginStd
	}
	return agentlsp.OriginModule
}

// under reports whether path is under dir.
func under(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && filepath.IsLocal(rel)
}

// parse reads and parses a Go file; a file with syntax errors gives the tree the parser recovered.
func parse(path string) (source, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return source{}, err
	}
	fset := token.NewFileSet()
	file, _ := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	return source{path: path, src: src, fset: fset, file: file}, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
