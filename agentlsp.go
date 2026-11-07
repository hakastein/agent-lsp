// Package agentlsp holds what the language packages of agent-lsp share: positions, locations, the answers of the
// operations, the kinds of symbols and the error of an operation a language does not support. Each language is a package of its own
// (php, golang) with an Open function that returns an index of a root; the indexes of all languages have the same methods,
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

// Origin says where a declaration lives: under the root, or outside the code of the root (the standard library,
// a dependency, a vendored copy). A consumer that marks declarations outside the root reads it instead of guessing
// from the path.
type Origin string

const (
	// OriginRoot is a declaration in a file of the root.
	OriginRoot Origin = ""
	// OriginStd is a declaration of the standard library of the language (Go: under GOROOT).
	OriginStd Origin = "std"
	// OriginModule is a declaration of a dependency outside the root (Go: the module cache or a module replaced
	// by a directory outside the root).
	OriginModule Origin = "module"
	// OriginVendor is a declaration of a vendored dependency: a file under a vendor directory of the root.
	OriginVendor Origin = "vendor"
)

// Declaration is a declaration of a symbol. Location is its name; Range is the whole declaration, from its first
// keyword or name to its end, without the doc comment. Signature is its text without the body: the header of a
// function, "type T struct" of a struct or interface type, the first line of other declarations. Doc is its doc
// comment as it stands in the file, comment markers included, or "" for none; its lines come right before
// Range.Line.
type Declaration struct {
	Location
	Range     Location
	Signature string
	Doc       string
	Origin    Origin
}

// Kind is the kind of a symbol; its value is the word a consumer prints. The kinds are shared by the languages.
type Kind string

const (
	// KindFunc is a function.
	KindFunc Kind = "func"
	// KindMethod is a method of a type, also of an interface type.
	KindMethod Kind = "method"
	// KindType is a type.
	KindType Kind = "type"
	// KindField is a field of a struct type, also an embedded one.
	KindField Kind = "field"
	// KindVar is a variable at the top level.
	KindVar Kind = "var"
	// KindConst is a constant at the top level.
	KindConst Kind = "const"
	// KindClass is a class.
	KindClass Kind = "class"
	// KindInterface is an interface.
	KindInterface Kind = "interface"
	// KindTrait is a trait.
	KindTrait Kind = "trait"
	// KindEnum is an enum.
	KindEnum Kind = "enum"
	// KindClassConst is a constant of a class, an interface or an enum, also a case of an enum.
	KindClassConst Kind = "classconst"
)

// Symbol is a named declaration: its name, its kind and the declaration that contains it (Container: the type of a
// method or a field, "" at the top level).
type Symbol struct {
	Name      string
	Kind      Kind
	Container string
	Declaration
}
