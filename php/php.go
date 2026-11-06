// Package php answers questions about the .php files under a root from an index in SQLite on disk: the files it
// was built from, the method Symbols and the resolved links «place → Symbol». Links are resolved while indexing; a
// question only reads them. The rules are those of docs/adr/0001-workspace-index-on-disk.md.
package php

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	"syscall"

	_ "modernc.org/sqlite"

	agentlsp "github.com/hakastein/agent-lsp"
	"github.com/hakastein/agent-lsp/php/internal/facts"
)

// schemaDir names the schema and the link semantics of the index. An incompatible change of either gets a new name,
// so an index of another schema or with links resolved by other rules is never opened.
const schemaDir = "index-v3"

// parserModule is the module of the PHP parser; the binary may be linked with a module that replaces it.
const parserModule = "github.com/VKCOM/php-parser"

// indexDir is the directory of every index this binary builds: schemaDir and the parser module the binary is linked
// with. An index whose facts another parser extracted has the same snapshot of files, so it lives elsewhere and is
// never opened.
func indexDir() (string, error) {
	sum, err := parserSum()
	if err != nil {
		return "", err
	}
	return schemaDir + "-" + hex.EncodeToString(sum[:6]), nil
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

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS files (id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, hash BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS symbols (
	key TEXT NOT NULL, file INTEGER NOT NULL,
	sl INTEGER NOT NULL, sc INTEGER NOT NULL, el INTEGER NOT NULL, ec INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS symbols_key ON symbols (key);
CREATE INDEX IF NOT EXISTS symbols_place ON symbols (file, sl);
CREATE TABLE IF NOT EXISTS links (
	key TEXT NOT NULL, file INTEGER NOT NULL,
	sl INTEGER NOT NULL, sc INTEGER NOT NULL, el INTEGER NOT NULL, ec INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS links_key ON links (key);
CREATE INDEX IF NOT EXISTS links_place ON links (file, sl);
`

// Index is the index of the .php files under one root.
type Index struct {
	root string
	db   *sql.DB
}

// indexPath is the index file of root. The package chooses it: a directory per root under the user's cache
// directory.
func indexPath(root string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir, err := indexDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(cache, "agent-lsp", "php", dir, hex.EncodeToString(sum[:16]), "index.sqlite"), nil
}

// Open opens the index of root and makes it describe the .php files under root. An index whose snapshot of files
// («path → hash») equals the files on disk is used as it is; otherwise the whole index is built again. Answers come
// from the files as they were at Open. logf, if not nil, receives diagnostic lines; they are not a contract.
func Open(root string, logf func(format string, args ...any)) (*Index, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	file, err := indexPath(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", file+"?_pragma=busy_timeout(60000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // a build transaction and its temporary table live on one connection
	ix := &Index{root: root, db: db}
	if err := ix.prepare(logf); err != nil {
		db.Close()
		return nil, err
	}
	return ix, nil
}

// Close closes the index file.
func (ix *Index) Close() error {
	return ix.db.Close()
}

func (ix *Index) prepare(logf func(string, ...any)) error {
	if _, err := ix.db.Exec(schema); err != nil {
		return err
	}
	paths, err := phpFiles(ix.root, logf)
	if err != nil {
		return err
	}
	complete, err := ix.complete()
	if err != nil {
		return err
	}
	if complete {
		same, n, err := ix.sameFiles(paths, logf)
		if err != nil {
			return err
		}
		if same {
			logf("php: index reused: %d files", n)
			return nil
		}
	}
	n, err := ix.build(paths, logf)
	if err != nil {
		return err
	}
	logf("php: index built: %d files", n)
	return nil
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

// errNotRegular is the error of reading a name that is not a regular file.
var errNotRegular = errors.New("not a regular file")

// readFile reads a regular file. The file is opened without blocking and checked after opening, so a name that
// has become a named pipe, a socket or a device by then is not read: the error is errNotRegular.
func readFile(name string) ([]byte, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", name, errNotRegular)
	}
	return io.ReadAll(f)
}

// snapshotFile reads the file p under root and hashes it for the snapshot. A name that is no longer a regular file
// is logged and is not part of the snapshot: ok is false. Any other error is returned: the snapshot cannot be known
// without the file.
func snapshotFile(root, p string, logf func(string, ...any)) (src []byte, sum [sha256.Size]byte, ok bool, err error) {
	src, err = readFile(filepath.Join(root, filepath.FromSlash(p)))
	if errors.Is(err, errNotRegular) {
		logf("php: skip %s: %v", p, err)
		return nil, sum, false, nil
	}
	if err != nil {
		return nil, sum, false, err
	}
	return src, sha256.Sum256(src), true, nil
}

func (ix *Index) complete() (bool, error) {
	err := ix.db.QueryRow(`SELECT 1 FROM meta WHERE key = 'complete'`).Scan(new(int))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// sameFiles reports whether the stored snapshot equals the files on disk and how many files it holds. A file that
// cannot be read is an error, never a match.
func (ix *Index) sameFiles(paths []string, logf func(string, ...any)) (bool, int, error) {
	stored := map[string]string{}
	rows, err := ix.db.Query(`SELECT path, lower(hex(hash)) FROM files`)
	if err != nil {
		return false, 0, err
	}
	for rows.Next() {
		var p, sum string
		if err := rows.Scan(&p, &sum); err != nil {
			rows.Close()
			return false, 0, err
		}
		stored[p] = sum
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, 0, err
	}
	read := 0
	for _, p := range paths {
		_, sum, ok, err := snapshotFile(ix.root, p, logf)
		if err != nil {
			return false, 0, err
		}
		if !ok {
			continue
		}
		read++
		if stored[p] != hex.EncodeToString(sum[:]) {
			return false, 0, nil
		}
	}
	return read == len(stored), read, nil
}

// build replaces the whole index in one transaction and returns the number of files indexed. A build that does
// not finish, also on a file that cannot be read, leaves the previous state.
func (ix *Index) build(paths []string, logf func(string, ...any)) (n int, err error) {
	tx, err := ix.db.Begin()
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

	if _, err := tx.Exec(`
		DELETE FROM meta; DELETE FROM files; DELETE FROM symbols; DELETE FROM links;
		CREATE TEMP TABLE calls (key TEXT NOT NULL, file INTEGER NOT NULL,
			sl INTEGER NOT NULL, sc INTEGER NOT NULL, el INTEGER NOT NULL, ec INTEGER NOT NULL);`); err != nil {
		return 0, err
	}
	insFile, err := tx.Prepare(`INSERT INTO files (path, hash) VALUES (?, ?)`)
	if err != nil {
		return 0, err
	}
	defer insFile.Close()
	insSymbol, err := tx.Prepare(`INSERT INTO symbols (key, file, sl, sc, el, ec) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer insSymbol.Close()
	insCall, err := tx.Prepare(`INSERT INTO temp.calls (key, file, sl, sc, el, ec) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer insCall.Close()

	for _, p := range paths {
		src, sum, ok, err := snapshotFile(ix.root, p, logf)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		n++
		res, err := insFile.Exec(p, sum[:])
		if err != nil {
			return 0, fmt.Errorf("%s: %w", p, err)
		}
		file, err := res.LastInsertId()
		if err != nil {
			return 0, fmt.Errorf("%s: %w", p, err)
		}
		found, syntax := facts.Parse(src)
		for _, e := range syntax {
			logf("php: %s: %s", p, e)
		}
		for _, m := range found.Methods {
			if err := insert(insSymbol, m.Key, file, m.Name); err != nil {
				return 0, fmt.Errorf("%s: %w", p, err)
			}
		}
		for _, c := range found.Calls {
			if err := insert(insCall, c.Key, file, c.Name); err != nil {
				return 0, fmt.Errorf("%s: %w", p, err)
			}
		}
	}
	// A call becomes a link only when its target Symbol is declared somewhere under the root.
	_, err = tx.Exec(`
		INSERT INTO links (key, file, sl, sc, el, ec)
			SELECT key, file, sl, sc, el, ec FROM temp.calls c
			WHERE EXISTS (SELECT 1 FROM symbols s WHERE s.key = c.key);
		DROP TABLE temp.calls;
		INSERT INTO meta (key, value) VALUES ('complete', '1');`)
	return n, err
}

// insert runs stmt with a key, a file id and a range.
func insert(stmt *sql.Stmt, key string, file int64, r facts.Range) error {
	_, err := stmt.Exec(key, file, r.Start.Line, r.Start.Column, r.End.Line, r.End.Column)
	return err
}

// Definition is the declarations of the Symbol at pos: the one a link there points to, or the one declared there.
// A position outside the root or on no Symbol has none.
func (ix *Index) Definition(pos agentlsp.Position) ([]agentlsp.Location, error) {
	key, err := ix.symbolAt(pos)
	if err != nil || key == "" {
		return nil, err
	}
	return ix.locations(`SELECT f.path, s.sl, s.sc, s.el, s.ec FROM symbols s JOIN files f ON f.id = s.file
		WHERE s.key = ? ORDER BY f.path, s.sl, s.sc`, key)
}

// References is every link to the Symbol at pos, in Uses. There is no hierarchy, so Implementations is empty, and
// calls whose receiver class is unknown are not collected, so Unresolved is empty too.
func (ix *Index) References(pos agentlsp.Position) (agentlsp.References, error) {
	key, err := ix.symbolAt(pos)
	if err != nil || key == "" {
		return agentlsp.References{}, err
	}
	uses, err := ix.locations(`SELECT f.path, l.sl, l.sc, l.el, l.ec FROM links l JOIN files f ON f.id = l.file
		WHERE l.key = ? ORDER BY f.path, l.sl, l.sc`, key)
	return agentlsp.References{Uses: uses}, err
}

// Rename is not supported.
func (ix *Index) Rename(pos agentlsp.Position, name string) (agentlsp.Renaming, error) {
	return agentlsp.Renaming{}, fmt.Errorf("php: rename: %w", agentlsp.ErrNotSupported)
}

// Symbols is not supported.
func (ix *Index) Symbols(name string) ([]agentlsp.Symbol, error) {
	return nil, fmt.Errorf("php: symbols: %w", agentlsp.ErrNotSupported)
}

// Outline is not supported.
func (ix *Index) Outline(file string) ([]agentlsp.Symbol, error) {
	return nil, fmt.Errorf("php: outline: %w", agentlsp.ErrNotSupported)
}

// symbolAt is the key of the Symbol a link at pos points to or that is declared at pos; "" when there is none.
func (ix *Index) symbolAt(pos agentlsp.Position) (string, error) {
	rel, err := filepath.Rel(ix.root, pos.File)
	if err != nil || !filepath.IsLocal(rel) {
		return "", nil
	}
	for _, q := range []string{
		`SELECT l.key FROM links l JOIN files f ON f.id = l.file
			WHERE f.path = ?1 AND (l.sl, l.sc) <= (?2, ?3) AND (?2, ?3) <= (l.el, l.ec) LIMIT 1`,
		`SELECT s.key FROM symbols s JOIN files f ON f.id = s.file
			WHERE f.path = ?1 AND (s.sl, s.sc) <= (?2, ?3) AND (?2, ?3) <= (s.el, s.ec) LIMIT 1`,
	} {
		var key string
		err := ix.db.QueryRow(q, filepath.ToSlash(rel), pos.Line, pos.Column).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		return key, err
	}
	return "", nil
}

func (ix *Index) locations(q, key string) ([]agentlsp.Location, error) {
	rows, err := ix.db.Query(q, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []agentlsp.Location
	for rows.Next() {
		var p string
		var l agentlsp.Location
		if err := rows.Scan(&p, &l.Line, &l.Column, &l.EndLine, &l.EndColumn); err != nil {
			return nil, err
		}
		l.File = filepath.Join(ix.root, filepath.FromSlash(p))
		out = append(out, l)
	}
	return out, rows.Err()
}
