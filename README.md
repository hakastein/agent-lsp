# agent-lsp

[![CI](https://github.com/hakastein/agent-lsp/actions/workflows/ci.yml/badge.svg)](https://github.com/hakastein/agent-lsp/actions/workflows/ci.yml)

agent-lsp is a Go library that answers questions about code: where a symbol is declared, where it is used, how to
rename it. A program imports it and calls it directly: no server, no daemon, no subprocess. The index lives in SQLite
on disk (`modernc.org/sqlite`, no CGO).

## API

The root package `github.com/hakastein/agent-lsp` holds the types every language shares. Each language is a package
of its own with an `Open` function; the index it returns has the same methods in every language, so a consumer
declares one interface for them and picks the package by the file extension.

```go
import (
	agentlsp "github.com/hakastein/agent-lsp"
	"github.com/hakastein/agent-lsp/golang"
)

ix, err := golang.Open(root, nil) // root holds go.mod or go.work; php.Open takes any directory
if err != nil {
	return err
}
defer ix.Close()

pos := agentlsp.Position{File: "/abs/path/cart/cart.go", Line: 12, Column: 9}
decls, err := ix.Definition(pos)          // []agentlsp.Declaration
syms, err := ix.Symbols("cart.Cart.Add")  // []agentlsp.Symbol
refs, err := ix.References(pos)           // refs.Uses, refs.Implementations, refs.Unresolved
```

| Method | Answer |
|---|---|
| `Definition(Position)` | the declarations of the symbol at the position |
| `References(Position)` | its uses and implementations, in separate lists, and the places with its name that were not resolved |
| `Rename(Position, name)` | the edits of every file and the places that were not resolved; writes nothing |
| `Symbols(name)` | the declarations with that qualified name |
| `Outline(file)` | the symbols of a file |

- A `Declaration` is the location of the name, the `Range` of the whole declaration without its doc comment (to print
  its source), the `Signature` (the text without the body: the header of a function,
  `type T struct` of a struct or interface type, the first line of other declarations), the `Doc` comment as it
  stands in the file, comment markers included (its lines come right before `Range.Line`), and the `Origin`:
  `""` for the root, `"std"` for the standard library, `"module"` for a dependency outside the root, `"vendor"` for
  a vendor directory of the root. A question on a declaration answers that declaration.
- A `Symbol` is a declaration with its `Name`, `Kind` and `Container` (the type of a method or a field; `""` at the
  top level). The kinds are constants shared by the languages: `agentlsp.KindFunc`, `KindMethod`, `KindType`,
  `KindField`, `KindVar`, `KindConst`; a kind prints as `func`, `method`, `type`, `field`, `var`, `const`.
- The name for `Symbols` is qualified with dots: `Func`, `Type.Method`, `pkg.Func`, `pkg.Type.Method`; the package
  is its name or its path. A bare name matches the last segment of every qualified name. In PHP the form is
  `Class::method`.

- A position is an absolute path, a 1-based line and a 1-based column in bytes. Locations in answers use the same
  units, with an exclusive end.
- A path of a file of the root, in a position and in an answer, starts with the root as `Open` got it, made absolute
  but with symlinks not resolved: a root opened through a symlink is walked through it and answers with paths through
  it. Files outside the root (the standard library, dependencies) have the paths the language tools report.
- A position that is not a place in a file of the root (a relative path, a file outside the root or one that does
  not exist, a line or a column outside the file) is an error that repeats the position and names the problem. A
  place in a file of the root where no symbol is, such as whitespace or a comment, gives an empty answer and no
  error. The file is read as it is on disk; a column may stand right after the last byte of its line, and the line
  break (`\n` or `\r\n`) is not a column.
- An operation a language does not implement fails with an error that `errors.Is(err, agentlsp.ErrNotSupported)`.
- `Open` takes an optional func for diagnostic lines (skipped files, syntax errors, whether the index was built or
  reused); they are not a contract. `Close` may be called again; the second call does nothing.

## Go

Package `golang` answers about the Go code of a root: a directory with `go.mod` or `go.work`. It calls a fork of the
internal packages of gopls directly, with no LSP server ([`internal/gopls/FORK.md`](internal/gopls/FORK.md)).

- `Definition` resolves by types, also into the standard library and dependencies; locals, parameters, fields,
  interface methods and labels are declarations too.
- `Symbols` searches the packages of the root, test files included; dependencies are not searched. It gives all
  six kinds.
- `Outline` lists the declarations of a file in order: functions, methods, types, the fields of struct types and the
  methods of interface types, top-level variables and constants. It reads the syntax of the file only.
- `References` and `Rename` are not supported.
- A process has one Go index open at a time: `Open` fails until the open one is closed.

### The cache

- What gopls computes (export data, indexes of symbols, references and method sets) and the packages `go list`
  reports for the root live in one SQLite file under the user's cache directory: `agent-lsp/cache.sqlite`. Every
  call is a new process, so a call reuses what an earlier one computed.
- The packages of the root are listed again only when `go.mod`, `go.sum`, `go.work`, the set of `.go` files, or the
  package name, imports or build constraints of a file change, or the toolchain, `GOOS`, `GOARCH`, build flags or
  the settings of `go env -w` differ. An edit of a function body is seen by the next call without listing again.
- Keys hold the schema, the fork and the Go version the binary is built with: entries of another build are not read.
- Two worktrees of one module share the entries of the standard library and the module cache; each computes the
  packages of its own files once, since gopls keys and export data hold file paths.
- An entry not used for 14 days is deleted along the way. A cache file that is not an SQLite database or is damaged
  is made again: what it holds can be computed again. Damage that `Open` of the file does not see is found later by
  a read or a write. When `Open` finds it, `Open` makes the file again and goes on. When the gopls cache finds it,
  the read is a miss; when a later load of packages does, that question fails. Either way `Close` removes the file,
  and the next call makes it again. A file that another process has already made again is not removed, and a
  read or a write that failed on the old file is not an error: `Open` opens again, `Close` drops what was not
  written. A damaged file that cannot be removed, as on Windows while another process holds it open, fails no call
  either: when `Open` finds the damage, the call goes on with a cache in memory and computes its answer again; when
  `Close` does, it leaves the file. The next call removes the file, or goes on in memory while it still cannot. Any
  other error of opening the file fails `Open`.
- Processes open, make and remove the cache file one at a time, under the lock of `agent-lsp/cache.sqlite.lock` next
  to it: calls that start at once on a missing or damaged file all succeed.
- The diagnostic lines say whether the packages were loaded or reused and how many cache entries were reused,
  missed and written.

Timings of `Open`, `Definition` and `Close` as new processes, 16 cores, Linux, a warm Go build cache, on
`golang.org/x/tools` v0.47.0:

| Call | Module `golang.org/x/tools` (`go/packages/packages.go` → `goListDriver`) | Module `golang.org/x/tools/gopls` (`internal/golang/definition.go` → `NarrowestPackageForFile`) |
|---|---|---|
| first call, empty cache | 2.80 s (`go list` 2.5 s) | 1.65 s (`go list` 1.1 s, `Definition` 0.46 s) |
| next call | 0.11 s | 0.16 s |
| after an edit outside the imports | 0.10 s | 0.20 s |
| after an edit of the imports | 0.39 s | 0.51 s |
| first call in a second worktree | 0.50 s | 0.66 s |
| next call in the second worktree | 0.10 s | 0.17 s |

The cache file grows to 4.7 MB with one worktree of the gopls module and 8.7 MB with two.

## PHP

Package `php` indexes the `.php` files under the root. It answers `Definition` and `References` for methods;
`Rename`, `Symbols` and `Outline` are not supported. A declaration of `Definition` holds the location of the method
name, which is also its range; it has no signature or doc comment.

- A method call is resolved when its receiver class is known from the code around it:
  - `(new Cart())->add()` and `(new Cart())?->add()`, anywhere, also at the top level of a file;
  - `$cart->add()` inside a function, method or closure body where every assignment to `$cart` in that body is
    `new Cart(...)` of one class, the first of them comes before the call and nothing can bind `$cart` to another
    value. An arrow function sees the variables of its parent; a closure sees those it imports with `use` by value.
    Such a variable is known when, following the chain of arrow functions and `use` captures outward, it comes
    from a function, method or closure body where it is known; a chain that reaches the top level of the file
    gives an unknown variable.
  - A variable at the top level of a file is never known, also inside an arrow function or a closure that takes it
    from there. A superglobal (`$GLOBALS`, `$_SERVER`, `$_GET`, `$_POST`, `$_FILES`, `$_COOKIE`, `$_SESSION`,
    `$_REQUEST`, `$_ENV`, case-sensitive) is never known in any scope.
  - Any other write makes the variable unknown: another assignment, a compound assignment, `++`/`--`, a parameter,
    `foreach` (also by reference), `list(...)` and `[...]` destructuring (also with keys), `catch`, `global`,
    `static`, `unset`, a reference (`$y = &$x`, `use (&$x)`), and passing it as an argument (the parameter may be
    by reference). A method call on the variable itself does not.
  - Every variable of the body is unknown when the body has `$$name` or `${...}` (also in `global $$name`),
    `extract`, `eval`, `include`, `include_once`, `require`, `require_once`, or `parse_str` or `mb_parse_str` with
    one argument.
  - `new self`, `new static`, `new parent`, an anonymous class and a dynamic class name give no receiver class.
  - These rules are recorded in [ADR 0001](docs/adr/0001-workspace-index-on-disk.md) (in Russian).
- `Definition` lists the method declared in that class. A call becomes a use only when that class declares the
  method; a call that is not resolved this way is not a use, whatever its method name.
- `References` from a call site or from the method declaration lists every resolved call site of the method in
  `Uses`. `Implementations` and `Unresolved` are empty: there is no hierarchy, and calls whose receiver is unknown
  are not collected.
- Class and method names compare case-insensitively, as in PHP.

Nothing else is resolved: no inheritance, interfaces, traits, `$this`, `self`, `static`, parameters, properties,
return types or PHPDoc.

### The index

- `Open` builds the index of the root or reuses it, and answers come from the files as they were at `Open`. It
  compares the files under the root (path and content hash of every `.php` file) with the ones the index was built
  from: if they are the same, the index is used as it is; otherwise it is built again as a whole.
- The index is one SQLite file per root under the user's cache directory (`os.UserCacheDir()`) in `agent-lsp/php/`.
  It is not `agent-lsp/cache.sqlite`, the file Go keeps its cache in.
- Symbolic links to files and directories are followed, also outside the root; a file is named by its path through
  the link, and a directory already on the path from the root is not walked again. Only regular files are read.
- Skipped on purpose, and only these: an entry that is not a regular file (a named pipe, a socket, a device), a
  directory already on the path from the root, and a broken symlink whose name does not end in `.php`. A symlink is
  broken only when its target is missing or its chain of links never ends. Any other entry that cannot be read makes
  `Open` fail with the path and the OS error, also when a ready index exists: a directory that cannot be listed, an
  entry that cannot be stat-ed (also a symlink whose target path runs through a regular file or a directory without
  permission), a `.php` file that cannot be opened or read, a broken `.php` symlink.

### The PHP parser

PHP is parsed by a fork of `github.com/VKCOM/php-parser` that `go.mod` puts in place with `replace`. Go applies
`replace` only in the main module, so a program that imports agent-lsp copies that `replace` line into its own
`go.mod`; without it the program is built with the upstream parser, which loses a class named `Enum` and strings
with the `b` prefix.

## Development

Requires Go 1.26 or later. The build does not use CGO; the library builds for Linux, macOS and Windows.

```bash
make go   # gofmt, go vet for linux, darwin and windows, go test
```

## Documentation

- Decisions: [docs/adr/](docs/adr/) (in Russian).
- Glossary: [CONTEXT.md](CONTEXT.md) (in Russian).
- Changes: [CHANGELOG.md](CHANGELOG.md) (in Russian).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE). The fork of gopls in `internal/gopls` keeps the BSD-3 license of the Go Authors
([`internal/gopls/LICENSE`](internal/gopls/LICENSE)).
