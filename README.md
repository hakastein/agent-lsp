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
	"github.com/hakastein/agent-lsp/php"
)

ix, err := php.Open(root, nil) // builds the index of root or reuses it
if err != nil {
	return err
}
defer ix.Close()

pos := agentlsp.Position{File: "/abs/path/src/app.php", Line: 12, Column: 9}
decls, err := ix.Definition(pos)
refs, err := ix.References(pos) // refs.Uses, refs.Implementations, refs.Unresolved
```

| Method | Answer |
|---|---|
| `Definition(Position)` | the declarations of the symbol at the position |
| `References(Position)` | its uses and implementations, in separate lists, and the places with its name that were not resolved |
| `Rename(Position, name)` | the edits of every file and the places that were not resolved; writes nothing |
| `Symbols(name)` | the declarations with that name |
| `Outline(file)` | the symbols of a file |

- A position is an absolute path, a 1-based line and a 1-based column in bytes. Locations in answers use the same
  units, with an exclusive end.
- A position that is not a place in a file of the root (a relative path, a file outside the root or one that does
  not exist, a line or a column outside the file) is an error that repeats the position and names the problem. A
  place in a file of the root where no symbol is, such as whitespace or a comment, gives an empty answer and no
  error. The file is read as it is on disk; a column may stand right after the last byte of its line, and the line
  break (`\n` or `\r\n`) is not a column.
- An operation a language does not implement fails with an error that `errors.Is(err, agentlsp.ErrNotSupported)`.
- `Open` takes an optional func for diagnostic lines (skipped files, syntax errors, whether the index was built or
  reused); they are not a contract.

## PHP

Package `php` indexes the `.php` files under the root. It answers `Definition` and `References` for methods;
`Rename`, `Symbols` and `Outline` are not supported.

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

[MIT](LICENSE)
