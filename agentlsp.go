// Package agentlsp holds what the language packages of agent-lsp share: positions, locations, the answers of the
// operations and the error of an operation a language does not support. Each language is a package of its own
// (php) with an Open function that returns an index of a root; the indexes of all languages have the same methods,
// so a consumer declares one interface for them and picks the package by the file extension.
package agentlsp

import "errors"

// ErrNotSupported is the error of an operation a language package does not implement; errors.Is finds it.
var ErrNotSupported = errors.New("not supported")

// Position is a place in a file: an absolute path, a 1-based line and a 1-based column in bytes.
type Position struct {
	File   string
	Line   int
	Column int
}

// Location is a range in a file: an absolute path, the 1-based line and column in bytes of its start and of the
// byte after its end.
type Location struct {
	File      string
	Line      int
	Column    int
	EndLine   int
	EndColumn int
}

// References is the answer of References: the places that use the symbol, the declarations that implement or
// override it, and the places with the symbol's name the analysis could not resolve.
type References struct {
	Uses            []Location
	Implementations []Location
	Unresolved      []Location
}

// Edit replaces the text at Location with NewText.
type Edit struct {
	Location
	NewText string
}

// Renaming is the answer of Rename: the edits of every file and the places with the symbol's name the analysis
// could not resolve. Rename writes nothing.
type Renaming struct {
	Edits      []Edit
	Unresolved []Location
}

// Symbol is a declaration: its name and where it is.
type Symbol struct {
	Name     string
	Location Location
}
