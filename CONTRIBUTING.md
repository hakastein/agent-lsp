# Contributing

Bug reports and pull requests are welcome on [GitHub](https://github.com/hakastein/agent-lsp).

## Before you start

- agent-lsp is a library with its own contract; behaviour is not shaped for a particular program that uses it.
- Open an issue before a change of behaviour. A change of a recorded decision updates its ADR in the same pull
  request.
- Design records ([`docs/adr/`](docs/adr/)) and the glossary ([`CONTEXT.md`](CONTEXT.md)) are in Russian.
- A bug found on a private code base comes as a small self-written fixture, never as that code.

## Development

```bash
make go   # gofmt check, go vet for linux, darwin and windows, go test
```

Everything builds with `CGO_ENABLED=0`. CI runs the same target on every push and pull request.

## Tests

- The seam is the exported API: a test opens the index of a root with the language package's `Open`, asks a
  question and checks the places in the answer or the error. Tests do not read the SQLite tables of the index;
  only the test of `internal/cachedb`, which sweeps the tables, does.
- Every test gets its own cache directory, so indexes of tests never meet.
- Fixtures are small files in the language package's `testdata/`; a test finds a place in a file by a unique
  substring.
- Tests live in the external package (`php_test`) and use testify with table cases; a test body has no logic.
- Diagnostic lines of `Open` are not a contract. A test reads them only where nothing else shows the behaviour.

## Pull requests

- One change per pull request, against `main`, from a branch `issue-<N>`.
- Borrowed code keeps its authors' notice. `internal/gopls` is a fork of gopls: a change there is listed in
  [`internal/gopls/FORK.md`](internal/gopls/FORK.md) in the same pull request.
- `make go` passes.
- A release is a tag `vX.Y.Z` with a section `## vX.Y.Z` in `CHANGELOG.md`.
