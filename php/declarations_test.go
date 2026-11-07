package php_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentlsp "github.com/hakastein/agent-lsp"
	"github.com/hakastein/agent-lsp/php"
)

// shown is a declaration as the tests compare it: its origin ("root" for the root), its file relative to the root
// or to the directory of the stubs, the line and column of its name and the text there, read from the file on disk.
func shown(t *testing.T, root string, d agentlsp.Declaration) string {
	t.Helper()
	origin, file := string(d.Origin), d.File
	if origin == "" {
		origin = "root"
	}
	if rel, err := filepath.Rel(root, file); err == nil && filepath.IsLocal(rel) {
		file = filepath.ToSlash(rel)
	} else {
		_, after, found := strings.Cut(filepath.ToSlash(file), "/php-stubs/")
		require.True(t, found, "a file outside the root that is not a stub: %s", file)
		_, file, _ = strings.Cut(after, "/") // the directory named by the digest of the stubs
	}
	src, err := os.ReadFile(d.File)
	require.NoError(t, err)
	line := strings.Split(string(src), "\n")[d.Line-1]
	require.Equal(t, d.Line, d.EndLine)
	return fmt.Sprintf("%s %s:%d:%d %s", origin, file, d.Line, d.Column, line[d.Column-1:d.EndColumn-1])
}

func shownDefinition(t *testing.T, ix *php.Index, root string, at place) []string {
	t.Helper()
	found, err := ix.Definition(at.position(t, root))
	require.NoError(t, err)
	var out []string
	for _, d := range found {
		out = append(out, shown(t, root, d))
	}
	return out
}

// shownSymbols is symbols as the tests compare them: the kind, the container and the name, then the declaration.
func shownSymbols(t *testing.T, root string, syms []agentlsp.Symbol) []string {
	t.Helper()
	var out []string
	for _, s := range syms {
		name := s.Name
		if s.Container != "" {
			name = s.Container + "::" + name
		}
		out = append(out, fmt.Sprintf("%s %s: %s", s.Kind, name, shown(t, root, s.Declaration)))
	}
	return out
}

// Names of classes, functions and constants resolve by the namespace and the use statements of their file into
// the root, its vendor directory and the stubs; an unqualified function or constant in a namespace falls back to
// the global one.
func TestDefinitionOfNames(t *testing.T) {
	cache(t)
	root := newRoot(t, "decls")
	ix, _ := open(t, root)

	tests := []struct {
		name string
		at   place
		want []string
	}{
		{"class imported by use", place{"src/app.php", "Cart())"}, []string{"root src/Shop/Cart.php:13:13 Cart"}},
		{"name in a use statement", place{"src/Shop/Cart.php", `Shop\Contracts\Priced;`},
			[]string{"root src/Shop/Contracts/Priced.php:5:11 Priced"}},
		{"interface in implements", place{"src/Shop/Cart.php", "Priced, "},
			[]string{"root src/Shop/Contracts/Priced.php:5:11 Priced"}},
		{"fully qualified class of the stubs", place{"src/Shop/Cart.php", `\Countable`},
			[]string{"std Core/Core_c.php:736:11 Countable"}},
		{"class of the stubs", place{"src/app.php", `\DateTime`}, []string{"std date/date_c.php:539:7 DateTime"}},
		{"class aliased by use, in vendor", place{"src/app.php", "LibClient()"},
			[]string{"vendor vendor/acme/lib/src/Client.php:5:7 Client"}},
		{"function imported by use function", place{"src/Shop/Cart.php", "money(1)"},
			[]string{"root src/Shop/Util/functions.php:6:10 money"}},
		{"fully qualified function", place{"src/app.php", `\Shop\Util\money(2)`},
			[]string{"root src/Shop/Util/functions.php:6:10 money"}},
		{"qualified function", place{"src/app.php", `Shop\Util\money(3)`},
			[]string{"root src/Shop/Util/functions.php:6:10 money"}},
		{"function of the stubs, falling back from a namespace", place{"src/Shop/Cart.php", "strlen(PHP_EOL)"},
			[]string{"std Core/Core.php:58:10 strlen"}},
		{"function of a stub file named with _", place{"src/app.php", "hex2bin("},
			[]string{"std standard/_standard_manual.php:34:10 hex2bin"}},
		{"constant of the stubs, falling back from a namespace", place{"src/Shop/Cart.php", "PHP_EOL)"},
			[]string{"std Core/Core_d.php:208:9 PHP_EOL"}},
		{"constant imported by use const", place{"src/Shop/Cart.php", "CURRENCY + "},
			[]string{"root src/Shop/Util/functions.php:13:7 CURRENCY"}},
		{"constant of define, falling back from a namespace", place{"src/Shop/Cart.php", "LOCAL;"},
			[]string{"root src/Shop/Util/functions.php:15:9 LOCAL"}},
		{"method of a class known by new", place{"src/app.php", "add('apple')"},
			[]string{"root src/Shop/Cart.php:21:21 add"}},
		{"class at its declaration", place{"src/Shop/Cart.php", "Cart implements"},
			[]string{"root src/Shop/Cart.php:13:13 Cart"}},
		{"class declared nowhere", place{"src/app.php", "Missing()"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shownDefinition(t, ix, root, tt.at))
		})
	}
}

// A declaration holds the range of the whole declaration, its signature and its doc comment.
func TestDeclarations(t *testing.T) {
	cache(t)
	root := newRoot(t, "decls")
	ix, _ := open(t, root)
	cart := filepath.Join(root, "src", "Shop", "Cart.php")
	functions := filepath.Join(root, "src", "Shop", "Util", "functions.php")
	priced := filepath.Join(root, "src", "Shop", "Contracts", "Priced.php")
	at := func(file string, line, column, endLine, endColumn int) agentlsp.Location {
		return agentlsp.Location{File: file, Line: line, Column: column, EndLine: endLine, EndColumn: endColumn}
	}

	tests := []struct {
		name string
		at   place
		want agentlsp.Declaration
	}{
		{"class with an attribute", place{"src/app.php", "Cart())"}, agentlsp.Declaration{
			Location: at(cart, 13, 13, 13, 17), Range: at(cart, 12, 1, 34, 2),
			Signature: "#[\\Attribute]\nfinal class Cart implements Priced, \\Countable",
			Doc:       "/**\n * A cart of goods.\n */"}},
		{"method", place{"src/app.php", "add('apple')"}, agentlsp.Declaration{
			Location: at(cart, 21, 21, 21, 24), Range: at(cart, 21, 5, 23, 6),
			Signature: "public function add(string $sku): void", Doc: "/**\n     * Adds a good.\n     */"}},
		{"method without a body", place{"src/Shop/Contracts/Priced.php", "discount("}, agentlsp.Declaration{
			Location: at(priced, 9, 21, 9, 29), Range: at(priced, 9, 5, 11, 12),
			Signature: "public function discount(\n        int $percent\n    ): int;"}},
		{"class constant", place{"src/Shop/Cart.php", "TAX = 20"}, agentlsp.Declaration{
			Location: at(cart, 16, 18, 16, 21), Range: at(cart, 16, 5, 16, 38),
			Signature: "public const TAX = 20, ROUND = 2;", Doc: "/** The tax rate. */"}},
		{"function", place{"src/Shop/Cart.php", "money(1)"}, agentlsp.Declaration{
			Location: at(functions, 6, 10, 6, 15), Range: at(functions, 6, 1, 9, 2),
			Signature: "function money(int $cents): string", Doc: "/** Formats money. */"}},
		{"constant after a comment and a blank line", place{"src/Shop/Cart.php", "CURRENCY + "}, agentlsp.Declaration{
			Location: at(functions, 13, 7, 13, 15), Range: at(functions, 13, 1, 13, 24),
			Signature: "const CURRENCY = 'EUR';"}},
		{"constant of define", place{"src/Shop/Cart.php", "LOCAL;"}, agentlsp.Declaration{
			Location: at(functions, 15, 9, 15, 14), Range: at(functions, 15, 1, 15, 19),
			Signature: "define('LOCAL', 1)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found, err := ix.Definition(tt.at.position(t, root))

			require.NoError(t, err)
			assert.Equal(t, []agentlsp.Declaration{tt.want}, found)
		})
	}
}

// A declaration of the stubs is in a file on disk, with its signature and doc comment.
func TestDeclarationOfTheStubs(t *testing.T) {
	cache(t)
	root := newRoot(t, "decls")
	ix, _ := open(t, root)
	found, err := ix.Definition(place{"src/Shop/Cart.php", "strlen(PHP_EOL)"}.position(t, root))
	require.NoError(t, err)
	require.Len(t, found, 1)
	src, err := os.ReadFile(found[0].File)
	require.NoError(t, err)

	assert.Equal(t, "#[Pure]\nfunction strlen(string $string): int", found[0].Signature)
	assert.True(t, strings.HasPrefix(found[0].Doc, "/**\n * Get string length\n"), found[0].Doc)
	assert.Contains(t, string(src), found[0].Doc+"\n#[Pure]\nfunction strlen(string $string): int {}")
}

func TestSymbols(t *testing.T) {
	cache(t)
	root := newRoot(t, "decls")
	ix, _ := open(t, root)

	tests := []struct {
		name string
		want []string
	}{
		{"Cart", []string{"class Cart: root src/Shop/Cart.php:13:13 Cart"}},
		{`Shop\Cart`, []string{"class Cart: root src/Shop/Cart.php:13:13 Cart"}},
		{`\Shop\Cart`, []string{"class Cart: root src/Shop/Cart.php:13:13 Cart"}},
		{`shop\CART`, []string{"class Cart: root src/Shop/Cart.php:13:13 Cart"}},
		{"Cart::add", []string{"method Cart::add: root src/Shop/Cart.php:21:21 add"}},
		{`Shop\Cart::add`, []string{"method Cart::add: root src/Shop/Cart.php:21:21 add"}},
		{"price", []string{
			"method Cart::price: root src/Shop/Cart.php:25:21 price",
			"method Priced::price: root src/Shop/Contracts/Priced.php:7:21 price",
		}},
		{"CART::ADD", []string{"method Cart::add: root src/Shop/Cart.php:21:21 add"}},
		{"Cart::TAX", []string{"classconst Cart::TAX: root src/Shop/Cart.php:16:18 TAX"}},
		{"Cart::tax", nil},
		{"Suit::Hearts", []string{"classconst Suit::Hearts: root src/Shop/Util/functions.php:26:10 Hearts"}},
		{"Suit", []string{"enum Suit: root src/Shop/Util/functions.php:24:6 Suit"}},
		{"Loggable", []string{"trait Loggable: root src/Shop/Util/functions.php:17:7 Loggable"}},
		{"money", []string{"func money: root src/Shop/Util/functions.php:6:10 money"}},
		{`Util\CURRENCY`, []string{"const CURRENCY: root src/Shop/Util/functions.php:13:7 CURRENCY"}},
		{`UTIL\CURRENCY`, []string{"const CURRENCY: root src/Shop/Util/functions.php:13:7 CURRENCY"}},
		{"currency", nil},
		{`Lib\Client`, []string{ // the root first, also from a directory that sorts after vendor
			"class Client: root web/Client.php:5:7 Client",
			"class Client: vendor vendor/acme/lib/src/Client.php:5:7 Client",
		}},
		{"Countable", []string{ // the stubs last
			"interface Countable: root web/Client.php:9:11 Countable",
			"interface Countable: std Core/Core_c.php:736:11 Countable",
		}},
		{"Contracts", nil},
		{"Nowhere", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			syms, err := ix.Symbols(tt.name)

			require.NoError(t, err)
			assert.Equal(t, tt.want, shownSymbols(t, root, syms))
		})
	}
}

func TestOutline(t *testing.T) {
	cache(t)
	root := newRoot(t, "decls")
	ix, _ := open(t, root)

	tests := []struct {
		file string
		want []string
	}{
		{"src/Shop/Cart.php", []string{
			"class Cart: root src/Shop/Cart.php:13:13 Cart",
			"classconst Cart::TAX: root src/Shop/Cart.php:16:18 TAX",
			"classconst Cart::ROUND: root src/Shop/Cart.php:16:28 ROUND",
			"method Cart::add: root src/Shop/Cart.php:21:21 add",
			"method Cart::price: root src/Shop/Cart.php:25:21 price",
			"method Cart::count: root src/Shop/Cart.php:30:21 count",
		}},
		{"src/Shop/Util/functions.php", []string{
			"func money: root src/Shop/Util/functions.php:6:10 money",
			"const CURRENCY: root src/Shop/Util/functions.php:13:7 CURRENCY",
			"const LOCAL: root src/Shop/Util/functions.php:15:9 LOCAL",
			"trait Loggable: root src/Shop/Util/functions.php:17:7 Loggable",
			"method Loggable::log: root src/Shop/Util/functions.php:19:21 log",
			"enum Suit: root src/Shop/Util/functions.php:24:6 Suit",
			"classconst Suit::Hearts: root src/Shop/Util/functions.php:26:10 Hearts",
		}},
		{"vendor/acme/lib/src/Client.php", []string{"class Client: vendor vendor/acme/lib/src/Client.php:5:7 Client"}},
		{"composer.json", nil},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			syms, err := ix.Outline(filepath.Join(root, filepath.FromSlash(tt.file)))

			require.NoError(t, err)
			assert.Equal(t, tt.want, shownSymbols(t, root, syms))
		})
	}
}

// A class is used where its name stands: a use statement, new, implements; a function where it is called, also
// through the fallback from a namespace.
func TestReferencesOfNames(t *testing.T) {
	cache(t)
	root := newRoot(t, "decls")
	ix, _ := open(t, root)

	tests := []struct {
		name string
		at   place
		want []place
	}{
		{"class", place{"src/Shop/Cart.php", "Cart implements"},
			[]place{{"src/app.php", `Shop\Cart;`}, {"src/app.php", "Cart())"}}},
		{"interface", place{"src/Shop/Contracts/Priced.php", "Priced"},
			[]place{{"src/Shop/Cart.php", `Shop\Contracts\Priced;`}, {"src/Shop/Cart.php", "Priced, "}}},
		{"function", place{"src/Shop/Util/functions.php", "money(int"}, []place{
			{"src/Shop/Cart.php", `Shop\Util\money;`}, {"src/Shop/Cart.php", "money(1)"},
			{"src/app.php", `\Shop\Util\money(2)`}, {"src/app.php", `Shop\Util\money(3)`},
		}},
		{"constant of define, through the fallback", place{"src/Shop/Util/functions.php", "LOCAL'"},
			[]place{{"src/Shop/Cart.php", "LOCAL;"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ElementsMatch(t, locations(t, root, tt.want), uses(t, ix, root, tt.at))
		})
	}
}
