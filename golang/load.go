package golang

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/hakastein/agent-lsp/internal/cachedb"
	"github.com/hakastein/agent-lsp/internal/gopls/cache"
	"github.com/hakastein/agent-lsp/internal/gopls/xtools/go/packages"
	"github.com/hakastein/agent-lsp/internal/gopls/xtools/packagesinternal"
)

// loadSchema holds the stored loads of packages and the headers of the files they depend on.
//
// go1_files is the header hash (package name, imports, build constraints) of each .go file under a walked directory,
// with the size and the modification time it was read at: a file whose size and time are the same is not read again.
// go1_loads is the answer of a load of packages by the key of its configuration, with the state of the files it was
// computed from.
const loadSchema = `
CREATE TABLE IF NOT EXISTS go1_files (
	dir TEXT NOT NULL, path TEXT NOT NULL, size INTEGER NOT NULL, mtime INTEGER NOT NULL, header BLOB NOT NULL,
	used INTEGER NOT NULL, PRIMARY KEY (dir, path)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS go1_files_used ON go1_files (used);
CREATE TABLE IF NOT EXISTS go1_loads (
	key BLOB PRIMARY KEY, state BLOB NOT NULL, packages BLOB NOT NULL, used INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS go1_loads_used ON go1_loads (used);
`

// loader loads packages for a view, reusing the stored answer of a load whose configuration and files are the same.
type loader struct {
	db     *cachedb.DB
	env    cache.GoEnv
	logf   func(string, ...any)
	loaded int // packages a load returned
	reused int // packages a stored answer returned
}

// load is packages.Load that reuses a stored answer. The answer depends on the configuration (directory, patterns,
// the GO* and CGO_* environment and the file of go env -w, build flags, mode), the Go toolchain, and the files: the
// go.mod, go.sum, go.work and vendor/modules.txt files, and which .go files there are with their package names,
// imports and build constraints. A change of a function body is not a change of the answer. An error of the cache
// database names its file.
func (l *loader) load(cfg *packages.Config, patterns ...string) (_ []*packages.Package, err error) {
	defer func() { err = l.db.Err(err) }()
	if len(cfg.Overlay) > 0 {
		return l.fresh(cfg, patterns)
	}
	key := l.configKey(cfg, patterns)
	state, err := l.state(cfg.Dir)
	if err != nil {
		return nil, err
	}
	var stored, data []byte
	err = l.db.QueryRow(`SELECT state, packages FROM go1_loads WHERE key = ?`, key[:]).Scan(&stored, &data)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if bytes.Equal(stored, state[:]) {
		if pkgs, ok := l.decode(data); ok {
			l.reused += len(pkgs)
			_, err := l.db.Exec(`UPDATE go1_loads SET used = ? WHERE key = ? AND used < ?`,
				time.Now().Unix(), key[:], time.Now().Add(-24*time.Hour).Unix())
			return pkgs, err
		}
	}
	pkgs, err := l.fresh(cfg, patterns)
	if err != nil {
		return nil, err
	}
	data, err = encode(pkgs)
	if err != nil {
		return nil, err
	}
	_, err = l.db.Exec(`INSERT OR REPLACE INTO go1_loads (key, state, packages, used) VALUES (?, ?, ?, ?)`,
		key[:], state[:], data, time.Now().Unix())
	return pkgs, err
}

func (l *loader) fresh(cfg *packages.Config, patterns []string) ([]*packages.Package, error) {
	pkgs, err := packages.Load(cfg, patterns...)
	l.loaded += len(pkgs)
	return pkgs, err
}

// configKey names the configuration of a load and the toolchain that answers it.
func (l *loader) configKey(cfg *packages.Config, patterns []string) [sha256.Size]byte {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\x00%t\x00", salt, l.env.GoVersionOutput, l.env.GOROOT, cfg.Mode, cfg.Tests)
	fmt.Fprintf(h, "%s\x00%q\x00%q\x00", cfg.Dir, patterns, cfg.BuildFlags)
	var env []string
	for _, kv := range cfg.Env {
		// GOOS of the process is overridden by GOOS of the view, which l.env names below
		if (strings.HasPrefix(kv, "GO") || strings.HasPrefix(kv, "CGO_")) && !strings.HasPrefix(kv, "GOOS=") {
			env = append(env, kv)
		}
	}
	slices.Sort(env)
	fmt.Fprintf(h, "%q\x00%s/%s\x00", env, l.env.GOOS, l.env.GOARCH)
	if filepath.IsAbs(l.env.GOENV) { // not "off"
		goenv, _ := os.ReadFile(l.env.GOENV) // no file is no settings
		h.Write(goenv)
	}
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return sum
}

// state hashes the files a load of dir depends on: those under dir and under the directories its go.mod and go.work
// files use or replace modules with.
func (l *loader) state(dir string) ([sha256.Size]byte, error) {
	h := sha256.New()
	dirs := []string{dir}
	for i := 0; i < len(dirs); i++ {
		files, err := l.walk(dirs[i])
		if err != nil {
			return [sha256.Size]byte{}, err
		}
		fmt.Fprintf(h, "dir %s\n", dirs[i])
		for _, f := range files {
			fmt.Fprintf(h, "%s %x\n", f.path, f.header)
			if i == 0 {
				dirs = append(dirs, localModules(dirs[i], f)...)
			}
		}
	}
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return sum, nil
}

// localModules is the directories a go.mod or go.work file at the top of dir names: modules replaced by a directory
// and modules a workspace uses.
func localModules(dir string, f header) []string {
	var out []string
	add := func(p string) {
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if rel, err := filepath.Rel(dir, p); err != nil || !filepath.IsLocal(rel) {
			out = append(out, filepath.Clean(p))
		}
	}
	switch f.path {
	case "go.mod":
		if mf, err := modfile.ParseLax(f.path, f.content, nil); err == nil {
			for _, r := range mf.Replace {
				if r.New.Version == "" {
					add(r.New.Path)
				}
			}
		}
	case "go.work":
		if wf, err := modfile.ParseWork(f.path, f.content, nil); err == nil {
			for _, u := range wf.Use {
				add(u.Path)
			}
			for _, r := range wf.Replace {
				if r.New.Version == "" {
					add(r.New.Path)
				}
			}
		}
	}
	return out
}

// header is a file a load depends on: its path relative to the walked directory, with forward slashes, and the hash
// of what the load reads from it. content is set for go.mod and go.work.
type header struct {
	path    string
	header  []byte
	content []byte
}

// walk lists the files under dir a load depends on, as the go command walks a pattern "dir/...": it skips the
// directories named testdata or starting with . or _. Each .go file is named by the hash of its header; go.mod, go.sum,
// go.work, go.work.sum and vendor/modules.txt by the hash of their content. A .go file whose size and modification
// time are those stored is not read again. A dir that is a symlink is walked through it, as the go command does.
func (l *loader) walk(dir string) ([]header, error) {
	type row struct {
		size, mtime int64
		header      []byte
	}
	stored := map[string]row{}
	rows, err := l.db.Query(`SELECT path, size, mtime, header FROM go1_files WHERE dir = ?`, dir)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		var r row
		if err := rows.Scan(&p, &r.size, &r.mtime, &r.header); err != nil {
			rows.Close()
			return nil, err
		}
		stored[p] = r
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}

	top, err := filepath.EvalSymlinks(dir) // WalkDir does not follow a root that is a symlink
	if err != nil {
		return nil, err
	}
	var out []header
	changed := map[string]row{}
	err = filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == top {
				return err
			}
			return nil // the go command does not list what it cannot read either
		}
		name := d.Name()
		if d.IsDir() {
			if p != top && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(top, p)
		rel = filepath.ToSlash(rel)
		switch {
		case strings.HasSuffix(name, ".go"):
			info, err := d.Info()
			if err != nil {
				return nil
			}
			r, ok := stored[rel]
			if !ok || r.size != info.Size() || r.mtime != info.ModTime().UnixNano() {
				src, err := os.ReadFile(p)
				if err != nil {
					return nil
				}
				r = row{info.Size(), info.ModTime().UnixNano(), goHeader(src)}
				changed[rel] = r
			}
			delete(stored, rel)
			out = append(out, header{path: rel, header: r.header})
		case name == "go.mod" || name == "go.sum" || name == "go.work" || name == "go.work.sum" ||
			rel == "vendor/modules.txt":
			src, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			sum := sha256.Sum256(src)
			out = append(out, header{path: rel, header: sum[:], content: src})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	tx, err := l.db.Begin()
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	for p := range stored {
		if _, err := tx.Exec(`DELETE FROM go1_files WHERE dir = ? AND path = ?`, dir, p); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	for p, r := range changed {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO go1_files (dir, path, size, mtime, header, used) VALUES (?, ?, ?, ?, ?, ?)`,
			dir, p, r.size, r.mtime, r.header, now); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if _, err := tx.Exec(`UPDATE go1_files SET used = ? WHERE dir = ? AND used < ?`,
		now, dir, time.Now().Add(-24*time.Hour).Unix()); err != nil {
		tx.Rollback()
		return nil, err
	}
	return out, tx.Commit()
}

// goHeader hashes what a load reads from a .go file: its build constraints, its package name and its imports. A
// file whose header does not parse is hashed whole.
func goHeader(src []byte) []byte {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		sum := sha256.Sum256(src)
		return sum[:]
	}
	h := sha256.New()
	for _, g := range f.Comments {
		if g.Pos() > f.Package {
			break
		}
		for _, c := range g.List {
			if strings.HasPrefix(c.Text, "//go:build") || strings.HasPrefix(c.Text, "// +build") {
				fmt.Fprintln(h, c.Text)
			}
		}
	}
	fmt.Fprintln(h, "package", f.Name.Name)
	for _, imp := range f.Imports {
		fmt.Fprintln(h, "import", imp.Path.Value)
	}
	return h.Sum(nil)
}

// storedPackage is a package of a load as it is stored: the fields of packages.Package gopls asks for, with the
// imports named by package ID.
type storedPackage struct {
	ID, Name, PkgPath, Dir, ExportFile, Target, ForTest string
	Errors                                              []packages.Error
	GoFiles, CompiledGoFiles, OtherFiles, IgnoredFiles  []string
	EmbedFiles, EmbedPatterns                           []string
	Imports                                             map[string]string
	Module                                              *packages.Module
	DepsErrors                                          []*packagesinternal.PackageError
	Root                                                bool
}

// encode stores the packages a load returned and every package they import.
func encode(roots []*packages.Package) ([]byte, error) {
	var out []storedPackage
	seen := map[string]bool{}
	isRoot := map[*packages.Package]bool{}
	for _, p := range roots {
		isRoot[p] = true
	}
	packages.Visit(roots, nil, func(p *packages.Package) {
		if seen[p.ID] {
			return
		}
		seen[p.ID] = true
		imports := map[string]string{}
		for path, imp := range p.Imports {
			imports[path] = imp.ID
		}
		out = append(out, storedPackage{
			ID: p.ID, Name: p.Name, PkgPath: p.PkgPath, Dir: p.Dir, ExportFile: p.ExportFile, Target: p.Target,
			ForTest: p.ForTest, Errors: p.Errors, GoFiles: p.GoFiles, CompiledGoFiles: p.CompiledGoFiles,
			OtherFiles: p.OtherFiles, IgnoredFiles: p.IgnoredFiles, EmbedFiles: p.EmbedFiles,
			EmbedPatterns: p.EmbedPatterns, Imports: imports, Module: p.Module,
			DepsErrors: packagesinternal.GetDepsErrors(p), Root: isRoot[p],
		})
	})
	return json.Marshal(out)
}

// decode restores the packages of a stored load; ok is false when they cannot serve: the data does not decode, or a
// file of a package the go command placed in its build cache is gone.
func (l *loader) decode(data []byte) (roots []*packages.Package, ok bool) {
	var stored []storedPackage
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, false
	}
	sizes := types.SizesFor("gc", l.env.GOARCH)
	if sizes == nil {
		return nil, false
	}
	byID := map[string]*packages.Package{}
	for _, s := range stored {
		if l.env.GOCACHE != "" {
			for _, f := range s.CompiledGoFiles {
				if strings.HasPrefix(f, l.env.GOCACHE) {
					if _, err := os.Stat(f); err != nil {
						l.logf("go: stored packages not used: %v", err)
						return nil, false
					}
				}
			}
		}
		p := &packages.Package{
			ID: s.ID, Name: s.Name, PkgPath: s.PkgPath, Dir: s.Dir, ExportFile: s.ExportFile, Target: s.Target,
			ForTest: s.ForTest, Errors: s.Errors, GoFiles: s.GoFiles, CompiledGoFiles: s.CompiledGoFiles,
			OtherFiles: s.OtherFiles, IgnoredFiles: s.IgnoredFiles, EmbedFiles: s.EmbedFiles,
			EmbedPatterns: s.EmbedPatterns, Module: s.Module, TypesSizes: sizes,
		}
		packages.SetDepsErrors(p, s.DepsErrors)
		byID[s.ID] = p
		if s.Root {
			roots = append(roots, p)
		}
	}
	for _, s := range stored {
		p := byID[s.ID]
		p.Imports = map[string]*packages.Package{}
		for path, id := range s.Imports {
			imp, found := byID[id]
			if !found {
				return nil, false
			}
			p.Imports[path] = imp
		}
	}
	return roots, true
}
