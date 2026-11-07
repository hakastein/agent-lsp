// Package php answers questions about the .php files under a root: the declarations of classes, interfaces, traits,
// enums, functions, constants, methods and class constants, and the places that name them. The facts of a file live
// in the cache database of the user under the hash of its content, so roots and worktrees with the same files share
// them; a root keeps only its map «path → hash». The standard library is the phpstorm-stubs the binary embeds
// (stubs/STUBS.md). The rules are those of docs/adr/0001-workspace-index-on-disk.md.
package php

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	agentlsp "github.com/hakastein/agent-lsp"
	"github.com/hakastein/agent-lsp/internal/cachedb"
	"github.com/hakastein/agent-lsp/internal/position"
	"github.com/hakastein/agent-lsp/php/internal/facts"
)

// rules names the rules by which facts are extracted from a file and resolved. An incompatible change of them gets
// a new name; an incompatible change of the tables gets new tables (php2_…).
const rules = "php1 rules 1"

// parserModule is the module of the PHP parser; the binary may be linked with a module that replaces it.
const parserModule = "github.com/VKCOM/php-parser"

// salt enters the key of the facts of every file: the rules and the parser the binary is linked with. Facts that
// another build extracted are never read.
func salt() ([]byte, error) {
	sum, err := parserSum()
	if err != nil {
		return nil, err
	}
	s := sha256.Sum256(append([]byte(rules+"\x00"), sum[:]...))
	return s[:16], nil
}

// parserSum names the parser the binary is linked with: the sha256 of its module path and version. A binary that
// does not record a version of that module, as with a parser from a local directory or in a test binary, is named
// by the binary file itself.
func parserSum() ([sha256.Size]byte, error) {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path != parserModule {
				continue
			}
			if dep.Replace != nil {
				dep = dep.Replace
			}
			if dep.Version != "" && dep.Version != "(devel)" {
				return sha256.Sum256([]byte(dep.Path + "@" + dep.Version)), nil
			}
		}
	}
	return executableSum()
}

// executableSum is the sha256 of the running binary. On Linux it reads /proc/self/exe, the file the process runs even
// when another file has replaced it at its path since; elsewhere it reads the file at the path the process started
// from.
func executableSum() ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	exe := "/proc/self/exe"
	if runtime.GOOS != "linux" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return sum, err
		}
	}
	f, err := os.Open(exe)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// schema holds the PHP tables of the cache database.
//
// php1_files is the map «path → sha256 of the content» of each root, with the size and the modification time the
// file was hashed at: a file whose size and time are the same is not read again. The stubs are a root of their own.
// php1_facts names the facts of a content extracted by a salt; php1_decls and php1_links are those facts, deleted with
// their row of php1_facts.
const schema = `
CREATE TABLE IF NOT EXISTS php1_files (
	root TEXT NOT NULL, path TEXT NOT NULL, size INTEGER NOT NULL, mtime INTEGER NOT NULL, hash BLOB NOT NULL,
	used INTEGER NOT NULL, PRIMARY KEY (root, path)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS php1_files_hash ON php1_files (hash);
CREATE INDEX IF NOT EXISTS php1_files_used ON php1_files (used);
CREATE TABLE IF NOT EXISTS php1_facts (
	id INTEGER PRIMARY KEY, hash BLOB NOT NULL, salt BLOB NOT NULL, used INTEGER NOT NULL, UNIQUE (hash, salt));
CREATE INDEX IF NOT EXISTS php1_facts_used ON php1_facts (used);
CREATE TABLE IF NOT EXISTS php1_decls (
	facts INTEGER NOT NULL REFERENCES php1_facts (id) ON DELETE CASCADE,
	key TEXT NOT NULL, lname TEXT NOT NULL, kind TEXT NOT NULL, name TEXT NOT NULL, container TEXT NOT NULL,
	sl INTEGER NOT NULL, sc INTEGER NOT NULL, el INTEGER NOT NULL, ec INTEGER NOT NULL,
	rsl INTEGER NOT NULL, rsc INTEGER NOT NULL, rel INTEGER NOT NULL, rec INTEGER NOT NULL,
	signature TEXT NOT NULL, doc TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS php1_decls_facts ON php1_decls (facts);
CREATE INDEX IF NOT EXISTS php1_decls_key ON php1_decls (key);
CREATE INDEX IF NOT EXISTS php1_decls_lname ON php1_decls (lname);
CREATE TABLE IF NOT EXISTS php1_links (
	facts INTEGER NOT NULL REFERENCES php1_facts (id) ON DELETE CASCADE,
	key TEXT NOT NULL, fallback TEXT NOT NULL,
	sl INTEGER NOT NULL, sc INTEGER NOT NULL, el INTEGER NOT NULL, ec INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS php1_links_facts ON php1_links (facts, sl);
CREATE INDEX IF NOT EXISTS php1_links_key ON php1_links (key);
CREATE INDEX IF NOT EXISTS php1_links_fallback ON php1_links (fallback) WHERE fallback != '';
`

// Index is the index of the .php files under one root and of the stubs.
type Index struct {
	root  string
	db    *cachedb.DB
	salt  []byte
	stubs string // the root of the stubs in php1_files
}

// Open opens the index of root, a directory with a composer.json file or a .git entry, and brings it up to the
// .php files under root: a file whose size or modification time changed is hashed again, and a content whose facts
// the cache does not hold is parsed. The stubs are parsed once for a cache. Answers come from the files as they were
// at Open. logf, if not nil, receives diagnostic lines; they are not a contract.
func Open(root string, logf func(format string, args ...any)) (*Index, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if !exists(filepath.Join(root, "composer.json")) && !exists(filepath.Join(root, ".git")) {
		return nil, fmt.Errorf("php: %s: no composer.json or .git", root)
	}
	s, err := salt()
	if err != nil {
		return nil, err
	}
	stubs, err := embeddedStubs()
	if err != nil {
		return nil, err
	}
	db, err := cachedb.Open(schema)
	if err != nil {
		return nil, err
	}
	ix := &Index{root: root, db: db, salt: s, stubs: "stubs:" + stubs.digest}
	if err := ix.open(stubs, logf); err != nil {
		db.Close()
		return nil, db.Err(err)
	}
	return ix, nil
}

func (ix *Index) open(stubs stubSet, logf func(string, ...any)) error {
	n, parsed, err := ix.index(source{key: ix.stubs, paths: stubs.paths, stat: stubs.stat, read: stubs.read}, logf)
	if err != nil {
		return err
	}
	logf("php: stubs: %d files, %d parsed", n, parsed)
	paths, err := phpFiles(ix.root, logf)
	if err != nil {
		return err
	}
	n, parsed, err = ix.index(source{key: ix.root, paths: paths, stat: ix.stat, read: ix.read}, logf)
	if err != nil {
		return err
	}
	logf("php: root: %d files, %d parsed", n, parsed)
	return nil
}

// Close closes the cache database. A second Close does nothing.
func (ix *Index) Close() error {
	return ix.db.Close()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// phpFiles lists the .php files under root, relative, with forward slashes, sorted. Symlinks are followed, to files
// and to directories, also outside root; a file is named by its path through the symlink. A directory that is
// already on the way from root to it is not walked again. Only regular files are listed.
//
// An entry that cannot be walked is an error: the list would miss files without saying so. Only a broken symlink
// whose name does not end in .php is skipped: what it pointed at, a file or a directory, cannot be known. A symlink
// is broken when its target does not exist or its chain of symlinks does not end; any other error of following it
// is an error.
func phpFiles(root string, logf func(string, ...any)) ([]string, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var paths []string
	if err := walkDir(root, "", entries, []string{canonical}, &paths, logf); err != nil {
		return nil, err
	}
	slices.Sort(paths)
	return paths, nil
}

// walkDir lists the .php files of the directory dir, whose path relative to the root is rel; chain holds the
// canonical paths of the directories from the root to dir.
func walkDir(dir, rel string, entries []fs.DirEntry, chain []string, paths *[]string, logf func(string, ...any)) error {
	for _, e := range entries {
		p, r := filepath.Join(dir, e.Name()), path.Join(rel, e.Name())
		mode := e.Type()
		if mode&fs.ModeSymlink != 0 {
			info, err := os.Stat(p) // never opens the target: a named pipe does not block it
			switch {
			case err == nil:
				mode = info.Mode().Type()
			case broken(err) && !strings.HasSuffix(e.Name(), ".php"):
				logf("php: skip %s: %v", p, err)
				continue
			default:
				return err
			}
		}
		switch {
		case mode.IsDir():
			canonical, err := filepath.EvalSymlinks(p)
			if err != nil {
				return err
			}
			if slices.Contains(chain, canonical) {
				logf("php: skip %s: a symlink cycle", p)
				continue
			}
			sub, err := os.ReadDir(p)
			if err != nil {
				return err
			}
			if err := walkDir(p, r, sub, append(chain[:len(chain):len(chain)], canonical), paths, logf); err != nil {
				return err
			}
		case mode.IsRegular() && strings.HasSuffix(e.Name(), ".php"):
			*paths = append(*paths, r)
		}
	}
	return nil
}

// broken reports whether err, from following a symlink, says that the symlink leads nowhere: its target does not
// exist or the chain of symlinks does not end.
func broken(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, errSymlinkLoop)
}

// stat is the size and the modification time of the file p under the root, through symlinks.
func (ix *Index) stat(p string) (int64, int64, error) {
	info, err := os.Stat(filepath.Join(ix.root, filepath.FromSlash(p)))
	if err != nil {
		return 0, 0, err
	}
	return info.Size(), info.ModTime().UnixNano(), nil
}

// read reads the file p under the root. A name that is no longer a regular file is not part of the root: ok is
// false. Any other error is returned: the root cannot be known without the file.
func (ix *Index) read(p string) (src []byte, ok bool, err error) {
	src, err = position.ReadFile(filepath.Join(ix.root, filepath.FromSlash(p)))
	if errors.Is(err, position.ErrNotRegular) {
		return nil, false, nil
	}
	return src, err == nil, err
}

// source is the files of a root of php1_files: the .php files under a root or the stubs.
type source struct {
	key   string
	paths []string
	stat  func(p string) (size, mtime int64, err error)
	read  func(p string) (src []byte, ok bool, err error)
}

// stored is a row of php1_files; missing is set when the cache holds no facts of its hash for the salt.
type stored struct {
	size, mtime int64
	hash        []byte
	missing     bool
}

// entry is what index learns of a file of a source.
type entry struct {
	size, mtime int64
	hash        []byte
	skip        bool         // not a regular file
	write       bool         // the row of php1_files is new or changed
	facts       *facts.Facts // facts to store, nil when the cache holds them
	syntax      []string     // syntax errors of the parsed file
}

// index brings the rows of s.key in php1_files up to the files of s and stores the facts the cache does not hold,
// in one transaction; it returns the number of files and of files parsed. A file that cannot be read fails it and
// leaves the previous state.
func (ix *Index) index(s source, logf func(string, ...any)) (files, parsed int, err error) {
	rows, err := ix.storedFiles(s.key)
	if err != nil {
		return 0, 0, err
	}
	entries := make([]entry, len(s.paths))
	var g errgroup.Group
	g.SetLimit(runtime.GOMAXPROCS(0))
	for i, p := range s.paths {
		g.Go(func() error {
			if err := ix.prepare(s, p, rows[p], &entries[i]); err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return 0, 0, err
	}

	tx, err := ix.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			return
		}
		err = tx.Commit()
	}()
	now := time.Now().Unix()
	for i, p := range s.paths {
		e := &entries[i]
		if e.skip {
			logf("php: skip %s: %v", p, position.ErrNotRegular)
			continue
		}
		files++
		delete(rows, p)
		for _, msg := range e.syntax {
			logf("php: %s: %s", p, msg)
		}
		if e.facts != nil {
			parsed++
			if err := ix.storeFacts(tx, e.hash, *e.facts, now); err != nil {
				return 0, 0, fmt.Errorf("%s: %w", p, err)
			}
		}
		if e.write {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO php1_files (root, path, size, mtime, hash, used)
				VALUES (?, ?, ?, ?, ?, ?)`, s.key, p, e.size, e.mtime, e.hash, now); err != nil {
				return 0, 0, err
			}
			if _, err := tx.Exec(`UPDATE php1_facts SET used = ? WHERE hash = ? AND salt = ?`,
				now, e.hash, ix.salt); err != nil {
				return 0, 0, err
			}
		}
	}
	for p := range rows {
		if _, err := tx.Exec(`DELETE FROM php1_files WHERE root = ? AND path = ?`, s.key, p); err != nil {
			return 0, 0, err
		}
	}
	return files, parsed, ix.touch(tx, s.key, now)
}

// storedFiles is the rows of php1_files of root by path.
func (ix *Index) storedFiles(root string) (map[string]stored, error) {
	rows, err := ix.db.Query(`SELECT f.path, f.size, f.mtime, f.hash, x.id IS NULL FROM php1_files f
		LEFT JOIN php1_facts x ON x.hash = f.hash AND x.salt = ? WHERE f.root = ?`, ix.salt, root)
	if err != nil {
		return nil, err
	}
	out := map[string]stored{}
	for rows.Next() {
		var p string
		var r stored
		if err := rows.Scan(&p, &r.size, &r.mtime, &r.hash, &r.missing); err != nil {
			rows.Close()
			return nil, err
		}
		out[p] = r
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

// prepare fills e for the file p of s whose row is r: the hash from r when the size and the time are those of r and
// its facts are stored, otherwise from the content, which is parsed when the cache holds no facts of it.
func (ix *Index) prepare(s source, p string, r stored, e *entry) error {
	var err error
	if e.size, e.mtime, err = s.stat(p); err != nil {
		return err
	}
	if r.hash != nil && r.size == e.size && r.mtime == e.mtime && !r.missing {
		e.hash = r.hash
		return nil
	}
	src, ok, err := s.read(p)
	if err != nil {
		return err
	}
	if !ok {
		e.skip = true
		return nil
	}
	sum := sha256.Sum256(src)
	e.hash = sum[:]
	e.write = r.hash == nil || r.size != e.size || r.mtime != e.mtime || string(r.hash) != string(e.hash)
	var held bool
	err = ix.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM php1_facts WHERE hash = ? AND salt = ?)`, e.hash, ix.salt).
		Scan(&held)
	if err != nil || held {
		return err
	}
	f, syntax := facts.Parse(src)
	e.facts, e.syntax = &f, syntax
	return nil
}

// storeFacts stores the facts of a content unless another process has just stored them.
func (ix *Index) storeFacts(tx *sql.Tx, hash []byte, f facts.Facts, now int64) error {
	res, err := tx.Exec(`INSERT OR IGNORE INTO php1_facts (hash, salt, used) VALUES (?, ?, ?)`, hash, ix.salt, now)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	decl, err := tx.Prepare(`INSERT INTO php1_decls (facts, key, lname, kind, name, container, sl, sc, el, ec,
		rsl, rsc, rel, rec, signature, doc) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer decl.Close()
	for _, d := range f.Decls {
		n, r := d.NameRange, d.Range
		if _, err := decl.Exec(id, d.Key, strings.ToLower(d.Name), string(d.Kind), d.Name, d.Container,
			n.Start.Line, n.Start.Column, n.End.Line, n.End.Column, r.Start.Line, r.Start.Column, r.End.Line,
			r.End.Column, d.Signature, d.Doc); err != nil {
			return err
		}
	}
	link, err := tx.Prepare(`INSERT INTO php1_links (facts, key, fallback, sl, sc, el, ec) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer link.Close()
	for _, r := range f.Links {
		if _, err := link.Exec(id, r.Key, r.Fallback, r.Name.Start.Line, r.Name.Start.Column, r.Name.End.Line,
			r.Name.End.Column); err != nil {
			return err
		}
	}
	return nil
}

// touch marks the rows of root and their facts used, at most once a day.
func (ix *Index) touch(tx *sql.Tx, root string, now int64) error {
	day := now - int64(24*time.Hour/time.Second)
	res, err := tx.Exec(`UPDATE php1_files SET used = ? WHERE root = ? AND used < ?`, now, root, day)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	_, err = tx.Exec(`UPDATE php1_facts SET used = ?1 WHERE salt = ?2 AND used < ?3
		AND hash IN (SELECT hash FROM php1_files WHERE root = ?4)`, now, ix.salt, day, root)
	return err
}

// Definition is the declarations of the symbol at pos: the one the name or the method call at pos names, or the
// one declared at pos; they may be under the root, in its vendor directory or in the stubs. A place in a file of
// the root that names nothing declared has none. A position that is not such a place is an error that repeats pos and
// names the problem: a relative path, a file outside the root or one that does not exist, a line or a column
// outside the file. The file is read as it is on disk; a column may stand right after the last byte of its line.
func (ix *Index) Definition(pos agentlsp.Position) ([]agentlsp.Declaration, error) {
	key, err := ix.symbolAt(pos)
	if err != nil || key == "" {
		return nil, err
	}
	syms, err := ix.symbols(`d.key = ?`, key)
	var out []agentlsp.Declaration
	for _, s := range syms {
		out = append(out, s.Declaration)
	}
	return out, err
}

// References is every place under the root that names the symbol at pos, in Uses. There is no hierarchy, so
// Implementations is empty, and calls whose receiver class is unknown are not collected, so Unresolved is empty
// too. Its answer to a place where no symbol is and its errors on a position are those of Definition.
func (ix *Index) References(pos agentlsp.Position) (agentlsp.References, error) {
	key, err := ix.symbolAt(pos)
	if err != nil || key == "" {
		return agentlsp.References{}, err
	}
	rows, err := ix.db.Query(`SELECT f.path, r.sl, r.sc, r.el, r.ec FROM php1_links r
		JOIN php1_facts x ON x.id = r.facts JOIN php1_files f ON f.hash = x.hash
		WHERE x.salt = ?1 AND f.root = ?2 AND (r.key = ?3 OR r.fallback = ?3 AND NOT `+declared(`r.key`)+`)
		ORDER BY f.path, r.sl, r.sc`, ix.salt, ix.root, key, ix.stubs)
	if err != nil {
		return agentlsp.References{}, ix.db.Err(err)
	}
	defer rows.Close()
	var uses []agentlsp.Location
	for rows.Next() {
		var p string
		var l agentlsp.Location
		if err := rows.Scan(&p, &l.Line, &l.Column, &l.EndLine, &l.EndColumn); err != nil {
			return agentlsp.References{}, ix.db.Err(err)
		}
		l.File = filepath.Join(ix.root, filepath.FromSlash(p))
		uses = append(uses, l)
	}
	return agentlsp.References{Uses: uses}, ix.db.Err(rows.Err())
}

// declared is an SQL condition: a declaration with the key the expression key gives is in the root ?2 or in the
// stubs ?4 for the salt ?1.
func declared(key string) string {
	return `EXISTS (SELECT 1 FROM php1_decls d2 JOIN php1_facts x2 ON x2.id = d2.facts
		JOIN php1_files f2 ON f2.hash = x2.hash WHERE d2.key = ` + key + ` AND x2.salt = ?1 AND f2.root IN (?2, ?4))`
}

// Rename is not supported.
func (ix *Index) Rename(pos agentlsp.Position, name string) (agentlsp.Renaming, error) {
	return agentlsp.Renaming{}, fmt.Errorf("php: rename: %w", agentlsp.ErrNotSupported)
}

// Symbols is the declarations with the name: a bare name (Cart, add), a name in its class (Cart::add), a name in
// its namespace (Shop\Cart, \Shop\Cart, Shop\Cart::add). A bare name matches the last segment of every qualified
// name. Names compare as PHP compares them: case-insensitively, but for the names of constants and class constants.
// Declarations of the root, its vendor directory and the stubs are searched; those of the root and its vendor
// directory come first, in the order of their files and lines.
func (ix *Index) Symbols(name string) ([]agentlsp.Symbol, error) {
	q := strings.TrimPrefix(name, `\`)
	i := strings.LastIndexAny(q, `\:`) + 1
	lower, constant := strings.ToLower(q), strings.ToLower(q[:i])+q[i:]
	syms, err := ix.symbols(`d.lname = ?`, lower[i:])
	var out []agentlsp.Symbol
	for _, s := range syms {
		want, qualified := lower, facts.Qualified(s.key) // in lower case but for the names of constants
		if s.Kind == agentlsp.KindConst || s.Kind == agentlsp.KindClassConst {
			want = constant
		}
		if qualified == want || strings.HasSuffix(qualified, `\`+want) || strings.HasSuffix(qualified, "::"+want) {
			out = append(out, s.Symbol)
		}
	}
	return out, err
}

// Outline is the declarations of a .php file of the root in the order of their ranges: classes, interfaces, traits
// and enums with their methods and constants, functions and constants. A file that is not a file of the root is an
// error; a file of the root that is not indexed has none.
func (ix *Index) Outline(file string) ([]agentlsp.Symbol, error) {
	rel, err := ix.place(agentlsp.Position{File: file, Line: 1, Column: 1})
	if err != nil {
		return nil, fmt.Errorf("php: outline %s: %w", file, err)
	}
	rows, err := ix.db.Query(`SELECT f.root, f.path, d.key, d.kind, d.name, d.container, d.sl, d.sc, d.el, d.ec,
		d.rsl, d.rsc, d.rel, d.rec, d.signature, d.doc FROM php1_files f
		JOIN php1_facts x ON x.hash = f.hash AND x.salt = ? JOIN php1_decls d ON d.facts = x.id
		WHERE f.root = ? AND f.path = ? ORDER BY d.rsl, d.rsc, d.sl, d.sc`, ix.salt, ix.root, rel)
	syms, err := ix.scanSymbols(rows, err)
	var out []agentlsp.Symbol
	for _, s := range syms {
		out = append(out, s.Symbol)
	}
	return out, err
}

// keyedSymbol is a Symbol with its key.
type keyedSymbol struct {
	agentlsp.Symbol
	key string
}

// symbols is the declarations in the root and the stubs that satisfy cond with arg, those of the root first.
func (ix *Index) symbols(cond, arg string) ([]keyedSymbol, error) {
	rows, err := ix.db.Query(`SELECT f.root, f.path, d.key, d.kind, d.name, d.container, d.sl, d.sc, d.el, d.ec,
		d.rsl, d.rsc, d.rel, d.rec, d.signature, d.doc FROM php1_decls d
		JOIN php1_facts x ON x.id = d.facts JOIN php1_files f ON f.hash = x.hash
		WHERE `+cond+` AND x.salt = ? AND f.root IN (?, ?) ORDER BY f.root = ?, f.path, d.sl, d.sc`,
		arg, ix.salt, ix.root, ix.stubs, ix.stubs)
	return ix.scanSymbols(rows, err)
}

func (ix *Index) scanSymbols(rows *sql.Rows, err error) ([]keyedSymbol, error) {
	if err != nil {
		return nil, ix.db.Err(err)
	}
	defer rows.Close()
	var out []keyedSymbol
	for rows.Next() {
		var root, p string
		var s keyedSymbol
		n, r := &s.Location, &s.Range
		if err := rows.Scan(&root, &p, &s.key, &s.Kind, &s.Name, &s.Container, &n.Line, &n.Column, &n.EndLine,
			&n.EndColumn, &r.Line, &r.Column, &r.EndLine, &r.EndColumn, &s.Signature, &s.Doc); err != nil {
			return nil, ix.db.Err(err)
		}
		switch {
		case root == ix.stubs:
			s.Origin = agentlsp.OriginStd
			if n.File, err = stubFile(strings.TrimPrefix(ix.stubs, "stubs:"), p); err != nil {
				return nil, fmt.Errorf("php: %w", err)
			}
		case slices.Contains(strings.Split(p, "/"), "vendor"):
			s.Origin = agentlsp.OriginVendor
			n.File = filepath.Join(ix.root, filepath.FromSlash(p))
		default:
			n.File = filepath.Join(ix.root, filepath.FromSlash(p))
		}
		r.File = n.File
		out = append(out, s)
	}
	return out, ix.db.Err(rows.Err())
}

// symbolAt is the key of the declaration at pos: the one a link there names, or the one whose name is there;
// "" when there is none. A link names its key when a declaration has it, otherwise its fallback when one has
// that.
func (ix *Index) symbolAt(pos agentlsp.Position) (string, error) {
	rel, err := ix.place(pos)
	if err != nil {
		return "", fmt.Errorf("php: position %s:%d:%d: %w", pos.File, pos.Line, pos.Column, err)
	}
	const file = `FROM php1_files f JOIN php1_facts x ON x.hash = f.hash AND x.salt = ?1`
	const at = `f.root = ?2 AND f.path = ?3 AND (t.sl, t.sc) <= (?5, ?6) AND (?5, ?6) <= (t.el, t.ec)`
	rows, err := ix.db.Query(`SELECT t.key, t.fallback `+file+` JOIN php1_links t ON t.facts = x.id WHERE `+at,
		ix.salt, ix.root, rel, ix.stubs, pos.Line, pos.Column)
	if err != nil {
		return "", ix.db.Err(err)
	}
	var candidates []string
	for rows.Next() {
		var key, fallback string
		if err := rows.Scan(&key, &fallback); err != nil {
			rows.Close()
			return "", ix.db.Err(err)
		}
		candidates = append(candidates, key, fallback)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return "", ix.db.Err(err)
	}
	for _, key := range candidates {
		var ok bool
		if key == "" {
			continue
		}
		if err := ix.db.QueryRow(`SELECT `+declared(`?3`), ix.salt, ix.root, key, ix.stubs).Scan(&ok); err != nil {
			return "", ix.db.Err(err)
		}
		if ok {
			return key, nil
		}
	}
	var key string
	err = ix.db.QueryRow(`SELECT t.key `+file+` JOIN php1_decls t ON t.facts = x.id WHERE `+at+` LIMIT 1`,
		ix.salt, ix.root, rel, ix.stubs, pos.Line, pos.Column).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return key, ix.db.Err(err)
}

// place is the path of the file of pos relative to the root, with forward slashes, or what makes pos no place in a
// file of the root (position.Check).
func (ix *Index) place(pos agentlsp.Position) (string, error) {
	if _, err := position.Check(ix.root, pos); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(ix.root, pos.File)
	return filepath.ToSlash(rel), err
}
