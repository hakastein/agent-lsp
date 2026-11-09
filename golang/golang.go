// Package golang answers questions about the Go code of a root: a module or a workspace of a go.work file. The
// answers come from a fork of the internal packages of gopls, called directly, without the LSP protocol. The cache of
// gopls (export data, indexes of symbols, references and method sets) and the packages the go command lists for the
// root live in the cache database of the user, so a call in a new process reuses what an earlier one computed. The
// package is named golang, as the package of gopls that answers these questions: go is a keyword.
package golang

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
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
	"github.com/hakastein/agent-lsp/internal/gopls/cache/parsego"
	"github.com/hakastein/agent-lsp/internal/gopls/filecache"
	gopls "github.com/hakastein/agent-lsp/internal/gopls/golang"
	"github.com/hakastein/agent-lsp/internal/gopls/protocol"
	"github.com/hakastein/agent-lsp/internal/gopls/settings"
	"github.com/hakastein/agent-lsp/internal/position"
	"golang.org/x/sync/errgroup"
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

// platforms are the GOOS the packages of a root are loaded for, each in a view of its own: a file built for any of
// them is answered about, and References joins their answers.
var platforms = []string{"linux", "darwin", "windows"}

// Index answers questions about the Go code of one root.
type Index struct {
	root  string
	env   cache.GoEnv
	logf  func(string, ...any)
	views []view // one per platform, in their order; nil once closed
	db    *cachedb.DB
}

// view is the gopls view of the root for one GOOS: its session and its snapshot as Open loaded it.
type view struct {
	session  *cache.Session
	snapshot *cache.Snapshot
	release  func()
}

func (v view) close() {
	v.release()
	v.session.Shutdown(context.Background())
}

// Open opens the Go code of root, a directory with a go.mod or a go.work file, and loads its packages for each of
// linux, darwin and windows, whatever GOOS the environment sets: the stored list of packages when the files it depends
// on are the same, the go command otherwise. A process has one Go index
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
		if err != nil {
			db.Close()
		}
	}()
	if err := filecache.Open(db.DB, blobsTable, []byte(salt)); err != nil {
		return nil, db.Err(err)
	}
	ctx := context.Background()
	dir := protocol.URIFromPath(root)
	env, err := cache.FetchGoEnv(ctx, dir, settings.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("go: %w", err)
	}
	var views []view
	defer func() {
		if err != nil {
			for _, v := range views {
				v.close()
			}
		}
	}()
	shared := cache.New(nil) // what the views have in common, such as the files they read
	var loaders []*loader
	for _, goos := range platforms {
		opts := settings.DefaultOptions()
		opts.Env = map[string]string{"GOOS": goos} // the go commands of the view
		goEnv := *env
		goEnv.GOOS = goos // no other field of GoEnv depends on GOOS
		l := &loader{db: db, env: goEnv, logf: logf}
		session := cache.NewSession(ctx, shared)
		_, snapshot, release, err := session.NewView(ctx, &cache.Folder{
			Dir: dir, Name: filepath.Base(root), Options: opts, Env: goEnv, Load: l.load,
		})
		if err != nil {
			session.Shutdown(ctx)
			return nil, fmt.Errorf("go: %s: %w", goos, err)
		}
		views = append(views, view{session: session, snapshot: snapshot, release: release})
		loaders = append(loaders, l)
	}
	loaded, reused := 0, 0
	for i, v := range views { // the views load at once, each in the background
		v.snapshot.AwaitInitialized(ctx)
		if e := v.snapshot.InitializationError(); e != nil {
			return nil, fmt.Errorf("go: %s: %w", platforms[i], e.MainError)
		}
		loaded, reused = loaded+loaders[i].loaded, reused+loaders[i].reused
	}
	if loaded > 0 {
		logf("go: packages loaded: %d", loaded)
	} else {
		logf("go: packages reused: %d", reused)
	}
	return &Index{root: root, env: *env, logf: logf, views: views, db: db}, nil
}

// Close writes what the gopls cache computed to the cache database and closes it. A second Close does nothing.
func (ix *Index) Close() error {
	if ix.views == nil {
		return nil
	}
	defer open.Unlock()
	for _, v := range ix.views {
		v.close()
	}
	ix.views = nil
	hits, misses := filecache.Stats()
	written, err := filecache.Flush()
	ix.logf("go: cache: %d reused, %d missed, %d written", hits, misses, written)
	return errors.Join(ix.db.Err(err), ix.db.Close())
}

// question is a position in the terms of gopls: the file, the place in it, and the snapshots of the views whose
// packages hold the file, in the order of platforms.
type question struct {
	uri   protocol.DocumentURI
	at    protocol.Range
	views []*cache.Snapshot
}

// ask checks pos, as every language does, and finds the platforms that build its file. A file that is not a .go file
// gives no views: no symbol is there. A .go file no platform builds is an error.
func (ix *Index) ask(ctx context.Context, pos agentlsp.Position) (question, error) {
	src, err := position.Check(ix.root, pos)
	if err != nil {
		return question{}, fmt.Errorf("go: position %s:%d:%d: %w", pos.File, pos.Line, pos.Column, err)
	}
	if !strings.HasSuffix(pos.File, ".go") {
		return question{}, nil
	}
	q := question{uri: protocol.URIFromPath(pos.File)}
	at, err := protocol.NewMapper(q.uri, src).OffsetPosition(position.Offset(src, pos))
	if err != nil {
		return question{}, fmt.Errorf("go: %w", err)
	}
	q.at = protocol.Range{Start: at, End: at}
	if q.views, err = ix.building(ctx, q.uri); err != nil {
		return question{}, fmt.Errorf("go: %w", err)
	}
	if q.views == nil {
		return question{}, fmt.Errorf("go: %s is in no package for %s", pos.File, strings.Join(platforms, ", "))
	}
	return q, nil
}

// building is the snapshots of the views whose packages hold the file at uri, in the order of platforms: the
// platforms that build the file.
func (ix *Index) building(ctx context.Context, uri protocol.DocumentURI) ([]*cache.Snapshot, error) {
	var out []*cache.Snapshot
	for _, v := range ix.views {
		mps, err := v.snapshot.MetadataForFile(ctx, uri, true)
		if err != nil {
			return nil, err
		}
		if len(mps) > 0 {
			out = append(out, v.snapshot)
		}
	}
	return out, nil
}

// Definition is the declarations of the symbol at pos, which may be in the standard library or a dependency. On a
// declaration, it is that declaration. A place in a file of the root where no symbol is has none. A position that is
// not a place in a file of the root is an error that repeats pos and names the problem, as in every language. The
// answer is that of the first platform that builds the file; a .go file no platform builds is an error.
func (ix *Index) Definition(pos agentlsp.Position) ([]agentlsp.Declaration, error) {
	ctx := context.Background()
	q, err := ix.ask(ctx, pos)
	if err != nil || q.views == nil {
		return nil, err
	}
	fh, err := q.views[0].ReadFile(ctx, q.uri)
	if err != nil {
		return nil, fmt.Errorf("go: %w", err)
	}
	locs, err := gopls.Definition(ctx, q.views[0], fh, q.at)
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
// its path. A bare name matches the last segment of every qualified name. The packages are those of every platform.
// Declarations come in the order of their files and lines.
func (ix *Index) Symbols(name string) ([]agentlsp.Symbol, error) {
	ctx := context.Background()
	last := name[strings.LastIndex(name, ".")+1:]
	type candidate struct{ pkgName, pkgPath string }
	files := map[string]candidate{}
	for _, v := range ix.views {
		var ids []cache.PackageID
		for id := range v.snapshot.WorkspacePackages().All() {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		index, err := v.snapshot.Symbols(ctx, ids...)
		if err != nil {
			return nil, fmt.Errorf("go: symbols: %w", err)
		}
		for i, pkg := range index {
			mp := v.snapshot.Metadata(ids[i])
			for j, syms := range pkg.Symbols {
				for _, sym := range syms {
					if sym.Name[strings.LastIndex(sym.Name, ".")+1:] == last {
						files[pkg.Files[j].Path()] = candidate{string(mp.Name), string(mp.PkgPath)}
					}
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

// References is the uses of the symbol at pos in the packages of the root and, for an interface type or a method of
// one, the types and methods that implement it, joined over the platforms as targets says. A place seen under more
// than one platform is in the answer once. Locations come in the order of files, lines and columns. The declaration
// itself is not a use. Unresolved is empty: gopls resolves every identifier of the packages by types. Its answer to a
// place where no symbol is and its errors on a position are those of Definition.
func (ix *Index) References(pos agentlsp.Position) (agentlsp.References, error) {
	ctx := context.Background()
	q, err := ix.ask(ctx, pos)
	if err != nil || q.views == nil {
		return agentlsp.References{}, err
	}
	uses, impls, err := ix.references(ctx, q)
	if errors.Is(err, gopls.ErrNoIdentFound) {
		return agentlsp.References{}, nil
	}
	if err != nil {
		return agentlsp.References{}, fmt.Errorf("go: references at %s:%d:%d: %w", pos.File, pos.Line, pos.Column, err)
	}
	var refs agentlsp.References
	if refs.Uses, err = locations(uses); err != nil {
		return agentlsp.References{}, fmt.Errorf("go: %w", err)
	}
	if refs.Implementations, err = locations(impls); err != nil {
		return agentlsp.References{}, fmt.Errorf("go: %w", err)
	}
	return refs, nil
}

// references is what the views find for the question, as gopls gives it: the uses and the implementations. The
// views are asked at once.
func (ix *Index) references(ctx context.Context, q question) (uses, impls []protocol.Location, err error) {
	targets, err := ix.targets(ctx, q)
	if err != nil {
		return nil, nil, err
	}
	var mu sync.Mutex
	var g errgroup.Group
	for _, t := range targets {
		g.Go(func() error {
			fh, err := t.snapshot.ReadFile(ctx, t.uri)
			if err != nil {
				return err
			}
			found, err := gopls.References(ctx, t.snapshot, fh, t.at, false)
			if err != nil {
				return err
			}
			abstract, err := isInterface(ctx, t.snapshot, t.uri, t.at)
			if err != nil {
				return err
			}
			var implemented []protocol.Location
			if abstract {
				if implemented, err = gopls.Implementation(ctx, t.snapshot, fh, t.at); err != nil {
					return err
				}
			}
			mu.Lock()
			defer mu.Unlock()
			uses, impls = append(uses, found...), append(impls, implemented...)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, nil, err
	}
	return uses, impls, nil
}

// target is a place a view is asked about the symbol of a question.
type target struct {
	snapshot *cache.Snapshot
	uri      protocol.DocumentURI
	at       protocol.Range
}

// targets is where the views are asked about the symbol of the question: a view that builds the file of the question
// at the question, another one at its own declaration of the member the symbol is, if it has one. A symbol that is
// no member, such as a local variable, lives in the file of the question only.
func (ix *Index) targets(ctx context.Context, q question) ([]target, error) {
	var out []target
	for _, snapshot := range q.views {
		out = append(out, target{snapshot, q.uri, q.at})
	}
	if len(q.views) == len(ix.views) {
		return out, nil
	}
	pkg, _, id, err := identAt(ctx, q.views[0], q.uri, q.at)
	if err != nil || id == nil {
		return out, err
	}
	m, ok := memberOf(pkg, pkg.TypesInfo().ObjectOf(id))
	if !ok {
		return out, nil
	}
	for _, v := range ix.views {
		if slices.Contains(q.views, v.snapshot) {
			continue
		}
		decl, ok, err := m.declarationIn(ctx, v.snapshot)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, target{v.snapshot, decl.URI, protocol.Range{Start: decl.Range.Start, End: decl.Range.Start}})
		}
	}
	return out, nil
}

// member names a symbol of a package alike in every view: by the package, the type it is declared in ("" at the top
// level) and its name. A symbol declared for the platforms apart (f in f_unix.go and in f_windows.go, a method or a
// field of such a type) is one member: each view finds its own declaration.
type member struct {
	pkg             cache.PackageID
	container, name string
}

// memberOf is the member obj is; a symbol inside a function or of a type without a name is none.
func memberOf(pkg *cache.Package, obj types.Object) (member, bool) {
	switch o := obj.(type) {
	case *types.Func:
		obj = o.Origin()
	case *types.Var:
		obj = o.Origin()
	}
	if obj == nil || obj.Pkg() == nil {
		return member{}, false
	}
	m := member{pkg: cache.PackageID(obj.Pkg().Path()), name: obj.Name()} // the ID of a package that is no test variant
	if obj.Pkg() == pkg.Types() {
		m.pkg = pkg.Metadata().ID
	}
	scope := obj.Pkg().Scope()
	if obj.Parent() != nil { // methods and fields have none
		return m, obj.Parent() == scope
	}
	for _, name := range scope.Names() {
		if m.container = name; m.lookup(obj.Pkg()) == obj {
			return m, true
		}
	}
	return member{}, false
}

// lookup is the declaration of the member in the package p, nil if it has none; a method or a field may be promoted
// from an embedded field.
func (m member) lookup(p *types.Package) types.Object {
	if m.container == "" {
		return p.Scope().Lookup(m.name)
	}
	tn, ok := p.Scope().Lookup(m.container).(*types.TypeName)
	if !ok {
		return nil
	}
	obj, _, _ := types.LookupFieldOrMethod(tn.Type(), true, p, m.name)
	return obj
}

// declarationIn is the place of the declaration of the member in the view of snapshot, if its package has one there.
func (m member) declarationIn(ctx context.Context, snapshot *cache.Snapshot) (protocol.Location, bool, error) {
	if snapshot.Metadata(m.pkg) == nil {
		return protocol.Location{}, false, nil
	}
	pkgs, err := snapshot.TypeCheck(ctx, m.pkg)
	if err != nil {
		return protocol.Location{}, false, err
	}
	obj := m.lookup(pkgs[0].Types())
	if obj == nil {
		return protocol.Location{}, false, nil
	}
	loc, err := gopls.ObjectLocation(ctx, pkgs[0].FileSet(), snapshot, obj)
	return loc, err == nil, err
}

// identAt is the package of the file at uri in the view of snapshot, the file, and the identifier at the place, nil
// for none.
func identAt(ctx context.Context, snapshot *cache.Snapshot, uri protocol.DocumentURI, at protocol.Range) (*cache.Package, *parsego.File, *ast.Ident, error) {
	pkg, pgf, err := gopls.NarrowestPackageForFile(ctx, snapshot, uri)
	if err != nil {
		return nil, nil, nil, err
	}
	start, end, err := pgf.RangePos(at)
	if err != nil {
		return nil, nil, nil, err
	}
	cur, _ := pgf.Cursor().FindByPos(start, end)
	id, _ := cur.Node().(*ast.Ident)
	return pkg, pgf, id, nil
}

// isInterface reports whether the identifier at the place names an interface type or a method of one. gopls finds
// the implementations of those; for a concrete type or method it finds the interfaces they implement instead.
func isInterface(ctx context.Context, snapshot *cache.Snapshot, uri protocol.DocumentURI, at protocol.Range) (bool, error) {
	pkg, _, id, err := identAt(ctx, snapshot, uri, at)
	if err != nil || id == nil {
		return false, err
	}
	switch obj := pkg.TypesInfo().ObjectOf(id).(type) {
	case *types.TypeName:
		return types.IsInterface(obj.Type()), nil
	case *types.Func:
		recv := obj.Signature().Recv()
		return recv != nil && types.IsInterface(recv.Type()), nil
	}
	return false, nil
}

// mappers turn the places gopls gives into locations; they read each file once.
type mappers map[protocol.DocumentURI]*protocol.Mapper

func (ms mappers) location(loc protocol.Location) (agentlsp.Location, error) {
	m := ms[loc.URI]
	if m == nil {
		src, err := os.ReadFile(loc.URI.Path())
		if err != nil {
			return agentlsp.Location{}, err
		}
		m = protocol.NewMapper(loc.URI, src)
		ms[loc.URI] = m
	}
	start, end, err := m.RangeOffsets(loc.Range)
	if err != nil {
		return agentlsp.Location{}, err
	}
	l := agentlsp.Location{File: loc.URI.Path()}
	l.Line, l.Column = m.OffsetLineCol8(start)
	l.EndLine, l.EndColumn = m.OffsetLineCol8(end)
	return l, nil
}

// locations is the places gopls found as locations, each once, in the order of files, lines and columns.
func locations(locs []protocol.Location) ([]agentlsp.Location, error) {
	ms := mappers{}
	var out []agentlsp.Location
	for _, loc := range locs {
		l, err := ms.location(loc)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	slices.SortFunc(out, compareLocations)
	return slices.Compact(out), nil
}

func compareLocations(a, b agentlsp.Location) int {
	return cmp.Or(strings.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column),
		cmp.Compare(a.EndLine, b.EndLine), cmp.Compare(a.EndColumn, b.EndColumn))
}

// Rename is the edits that rename the symbol at pos to name in the packages of the root, joined over the platforms
// as targets says: a symbol declared for the platforms apart is renamed in every declaration. An interface method is
// renamed with the methods gopls couples to it, those of the types converted to the interface. Rename writes nothing.
// An edit made under more than one platform is in the answer once. Edits come in the order of files, lines and
// columns. A name that conflicts with another symbol is an error that names the place, as is a symbol declared outside
// the root (the standard library, a dependency, a vendored copy), a method coupled to the renamed one in a package that
// does not import the package of the declaration, a method fmt finds through any (String, Error, Format), and, asked from a file not every
// platform builds, a field or method of a type without a name at the package level; a package is not supported.
// Unresolved is empty, as in References. Its answer to a place where no symbol is and its errors on a position are
// those of Definition.
func (ix *Index) Rename(pos agentlsp.Position, name string) (agentlsp.Renaming, error) {
	ctx := context.Background()
	q, err := ix.ask(ctx, pos)
	if err != nil {
		return agentlsp.Renaming{}, err
	}
	edits, err := ix.rename(ctx, q, name)
	if err != nil {
		return agentlsp.Renaming{}, fmt.Errorf("go: rename at %s:%d:%d: %w", pos.File, pos.Line, pos.Column, err)
	}
	return agentlsp.Renaming{Edits: edits}, nil
}

func (ix *Index) rename(ctx context.Context, q question, name string) ([]agentlsp.Edit, error) {
	if q.views == nil {
		return nil, nil
	}
	pkg, pgf, id, err := identAt(ctx, q.views[0], q.uri, q.at)
	if err != nil || id == nil {
		return nil, err
	}
	obj := pkg.TypesInfo().ObjectOf(id)
	if _, imported := obj.(*types.PkgName); imported || id == pgf.File.Name {
		return nil, fmt.Errorf("package %s: %w", id.Name, agentlsp.ErrNotSupported)
	}
	if _, ok := memberOf(pkg, obj); !ok && obj != nil && obj.Parent() == nil && len(q.views) < len(ix.views) {
		// targets cannot find it in the views that do not build the file: they would keep the old name.
		return nil, fmt.Errorf("%s is a field or method of a type without a name at the package level, in a file not every platform builds", id.Name)
	}
	fh, err := q.views[0].ReadFile(ctx, q.uri)
	if err != nil {
		return nil, err
	}
	found, err := gopls.Definition(ctx, q.views[0], fh, q.at)
	if err != nil {
		return nil, err
	}
	decls, err := locations(found)
	if err != nil {
		return nil, err
	}
	for _, d := range decls {
		if origin := ix.origin(d.File); origin != agentlsp.OriginRoot {
			return nil, fmt.Errorf("%s is declared outside the root (%s) at %s:%d:%d", id.Name, origin, d.File, d.Line, d.Column)
		}
	}
	targets, err := ix.targets(ctx, q)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var changes []protocol.DocumentChange
	var g errgroup.Group
	for _, t := range targets {
		g.Go(func() error {
			fh, err := t.snapshot.ReadFile(ctx, t.uri)
			if err != nil {
				return err
			}
			renamed, err := gopls.Rename(ctx, t.snapshot, fh, t.at, name)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			changes = append(changes, renamed...)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return edits(changes)
}

// edits is the text edits of the changes gopls made, each once, in the order of files, lines and columns.
func edits(changes []protocol.DocumentChange) ([]agentlsp.Edit, error) {
	ms := mappers{}
	var out []agentlsp.Edit
	for _, c := range changes {
		for _, e := range protocol.AsTextEdits(c.TextDocumentEdit.Edits) {
			l, err := ms.location(protocol.Location{URI: c.TextDocumentEdit.TextDocument.URI, Range: e.Range})
			if err != nil {
				return nil, err
			}
			out = append(out, agentlsp.Edit{Location: l, NewText: e.NewText})
		}
	}
	slices.SortFunc(out, func(a, b agentlsp.Edit) int {
		return cmp.Or(compareLocations(a.Location, b.Location), strings.Compare(a.NewText, b.NewText))
	})
	return slices.Compact(out), nil
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
