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

ix, err := golang.Open(root, nil) // root holds go.mod or go.work; for php.Open, composer.json or .git
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
  `""` for the root, `"std"` for the standard library (the stubs in PHP), `"module"` for a dependency outside the root, `"vendor"` for
  a vendor directory of the root. A question on a declaration answers that declaration.
- A `Symbol` is a declaration with its `Name`, `Kind` and `Container` (the type of a method or a field; `""` at the
  top level). The kinds are constants shared by the languages: `agentlsp.KindFunc`, `KindMethod`, `KindType`,
  `KindField`, `KindVar`, `KindConst`, `KindClass`, `KindInterface`, `KindTrait`, `KindEnum`, `KindClassConst`; a
  kind prints as `func`, `method`, `type`, `field`, `var`, `const`, `class`, `interface`, `trait`, `enum`,
  `classconst`.
- The name for `Symbols` is qualified with dots: `Func`, `Type.Method`, `pkg.Func`, `pkg.Type.Method`; the package
  is its name or its path. A bare name matches the last segment of every qualified name. In PHP the segments are
  joined with `\` and `::`: `Cart::add`, `Shop\Cart`, `\Shop\Cart::add`.

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
- An entry not used for 14 days is deleted along the way. A cache file that is not an SQLite database, or whose
  damage shows when it is opened (making its tables, sweeping them), is removed and made again empty, and the call
  goes on: what the file held is computed again. A damaged file that cannot be removed fails `Open` with an error
  that names it. Damage found later, by a load of packages (also the one of `Open`) or by `Close` writing the gopls
  cache, is an SQLite error of that call with the path of the file (a failed read of the gopls cache is a miss);
  nothing repairs it: remove the file, and the next call indexes again. SQLite keeps the file whole when a process
  dies (WAL, a transaction per write), so damage comes from outside.
- Processes open and make the cache file one at a time, under the lock of `agent-lsp/cache.sqlite.lock` next to
  it: calls that start at once on a missing or damaged file all succeed.
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

Package `php` answers about the `.php` files of a root: a directory with `composer.json` or `.git` (a checkout or a
worktree); `Open` does not look above it. Finding the root of a file — the nearest `composer.json` above it, without
one the top of the git checkout — is left to the program, as the module root of Go is.

- `Definition`, `Symbols` and `Outline` know classes, interfaces, traits, enums, functions, constants (`const` and
  `define('NAME', …)` with a string literal), methods and class constants (also enum cases): kinds `class`,
  `interface`, `trait`, `enum`, `func`, `const`, `method`, `classconst`. The container of a method or a class
  constant is its class without the namespace. Properties are not indexed.
- A name of a class, a function or a constant resolves by the namespace and the `use` statements of its file, as in
  PHP: in `new`, `extends`, `implements`, trait `use`, `instanceof`, `catch`, static calls, class constants and
  `::class`, types of parameters, returns and properties (also unions and intersections), attributes, function
  calls, constants, and the names of `use` statements. An unqualified function or constant in a namespace that no
  `use function` or `use const` imports falls back to the global one: `strlen()` in `namespace Shop` is
  `Shop\strlen` when that is declared, `strlen` otherwise.
- Declarations come from the root, its `vendor/` directory and the stubs of the standard library (below); `Origin`
  is `""`, `"vendor"` or `"std"`. A declaration holds its range from its first attribute, modifier or keyword, its
  signature (the text up to the body of a class-like or a function, the whole text of a method without a body, the
  first line of a constant) and its doc comment `/** … */` when it ends right before the declaration.
- `References` lists in `Uses` every place under the root, `vendor/` included, that names the symbol: the names
  above (also through the fallback) and the resolved method calls. `Implementations` and `Unresolved` are empty.
- `Symbols` takes a bare name (`Cart`, `add`), a name in its class (`Cart::add`) or in its namespace (`Shop\Cart`,
  `\Shop\Cart::add`); a bare name matches the last segment of every qualified name. Names compare as PHP compares
  them: case-insensitively but for constants. It searches the root, `vendor/` and the stubs; the root comes first.
- `Outline` lists the declarations of a file of the root in the order of their ranges.
- `Rename` is not supported.

A method call is resolved when its receiver class is known from the code around it:

- `(new Cart())->add()` and `(new Cart())?->add()`, anywhere, also at the top level of a file;
- `$cart->add()` inside a function, method or closure body where every assignment to `$cart` in that body is
  `new Cart(...)` of one class, the first of them comes before the call and nothing can bind `$cart` to another
  value. An arrow function sees the variables of its parent; a closure sees those it imports with `use` by value.
  Such a variable is known when, following the chain of arrow functions and `use` captures outward, it comes from a
  function, method or closure body where it is known; a chain that reaches the top level of the file gives an
  unknown variable.
- A variable at the top level of a file is never known, also inside an arrow function or a closure that takes it
  from there. A superglobal (`$GLOBALS`, `$_SERVER`, `$_GET`, `$_POST`, `$_FILES`, `$_COOKIE`, `$_SESSION`,
  `$_REQUEST`, `$_ENV`, case-sensitive) is never known in any scope.
- Any other write makes the variable unknown: another assignment, a compound assignment, `++`/`--`, a parameter,
  `foreach` (also by reference), `list(...)` and `[...]` destructuring (also with keys), `catch`, `global`,
  `static`, `unset`, a reference (`$y = &$x`, `use (&$x)`), and passing it as an argument (the parameter may be by
  reference). A method call on the variable itself does not.
- Every variable of the body is unknown when the body has `$$name` or `${...}` (also in `global $$name`),
  `extract`, `eval`, `include`, `include_once`, `require`, `require_once`, or `parse_str` or `mb_parse_str` with one
  argument.
- `new self`, `new static`, `new parent`, an anonymous class and a dynamic class name give no receiver class.
- The call names the method declared in that class only: there is no inheritance, interfaces, traits, `$this`,
  `self`, `static`, parameters, properties, return types or PHPDoc yet.

These rules are recorded in [ADR 0001](docs/adr/0001-workspace-index-on-disk.md) (in Russian).

### The index

- The facts of a file — its declarations and the places that name them — depend on its content only. They live in
  the cache database of the user, `agent-lsp/cache.sqlite` under `os.UserCacheDir()`, the file the Go cache is in,
  keyed by the sha256 of the content and by the build: the rules of the package and the PHP parser the binary is
  linked with. Facts of another build are not read.
- A root keeps only its map «path → content hash», with the size and modification time each file was hashed at.
  `Open` walks the root: a file whose size and time are the same is not read; a changed one is hashed again; a
  content whose facts the cache holds is not parsed. An edit is seen by the next call, which parses only the edited
  file. A second worktree or copy of a project reads and hashes its files but parses none that the first had.
- Answers come from the files as they were at `Open`.
- Entries not used for 14 days are deleted along the way; damage of the cache file is handled as for Go (above).
- Symbolic links to files and directories are followed, also outside the root; a file is named by its path through
  the link, and a directory already on the path from the root is not walked again. Only regular files are read.
- Skipped on purpose, and only these: an entry that is not a regular file (a named pipe, a socket, a device), a
  directory already on the path from the root, and a broken symlink whose name does not end in `.php`. A symlink is
  broken only when its target is missing or its chain of links never ends. Any other entry that cannot be read makes
  `Open` fail with the path and the OS error: a directory that cannot be listed, an entry that cannot be stat-ed
  (also a symlink whose target path runs through a regular file or a directory without permission), a `.php` file
  that cannot be opened or read, a broken `.php` symlink.
- The diagnostic lines say how many files of the stubs and of the root there are and how many were parsed.

### The stubs

The declarations of the standard library and the extensions of PHP are the JetBrains
[phpstorm-stubs](https://github.com/JetBrains/phpstorm-stubs) (Apache-2.0), embedded in the binary from `php/stubs`;
[`php/stubs/STUBS.md`](php/stubs/STUBS.md) records their version and how to update them. They add about 8.8 MB to a
program. They are parsed once for a cache, named by the hash of their content. A file of the stubs that an answer
points to is written on first need to `agent-lsp/php-stubs/<hash>/` under the user's cache directory, so that the
place can be opened.

Timings of `Open` and `Definition` as new processes, 16 cores, Linux, on `laravel/laravel` with its `vendor/`
directory (8215 `.php` files), median of three runs:

| Call | Time |
|---|---|
| first call, empty cache (the stubs and every file parsed) | 2.6 s |
| next call | 0.10 s |
| after an edit of one file (that file parsed) | 0.12 s |
| first call in a second worktree (every file hashed, none parsed) | 0.74 s |
| next call in the second worktree | 0.10 s |

The cache file grows to 64 MB with two worktrees of the project, mostly declarations with their doc comments.

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
([`internal/gopls/LICENSE`](internal/gopls/LICENSE)). The phpstorm-stubs in `php/stubs` keep the Apache-2.0 license
of JetBrains ([`php/stubs/LICENSE`](php/stubs/LICENSE)).
