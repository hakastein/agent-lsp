package golang_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentlsp "github.com/hakastein/agent-lsp"
	"github.com/hakastein/agent-lsp/golang"
)

const cartGo = "cart/cart.go"

var (
	add = decl{
		name: place{cartGo, "Add(it Item)"}, from: "func (c *Cart) Add", to: "c.total.Plus(it.Price)\n}",
		signature: "func (c *Cart) Add(it Item)",
		doc:       "// Add puts an item into the cart\n// and adds its price to the total.",
	}
	cartType = decl{
		name: place{cartGo, "Cart struct {"}, from: "type Cart struct", to: "total money.Amount\n}",
		signature: "type Cart struct", doc: "// Cart is the items of one customer.",
	}
	items = decl{
		name: place{cartGo, "Items []Item"}, from: "Items []Item", to: "Items []Item", signature: "Items []Item",
	}
	maxItems = decl{
		name: place{cartGo, "MaxItems = 100"}, from: "MaxItems = 100", to: "MaxItems = 100",
		signature: "const MaxItems = 100", doc: "// MaxItems is the most items a cart holds.",
	}
	pricerPrice = decl{
		name: place{cartGo, "Price(it Item)"}, from: "Price(it Item)", to: "Price(it Item) money.Amount",
		signature: "Price(it Item) money.Amount", doc: "// Price is the price of it.",
	}
)

func TestDefinition(t *testing.T) {
	cache(t)
	root := newRoot(t)
	writeBroken(t, root)

	tests := []struct {
		name string
		at   place
		want []decl
	}{
		{"method call", place{"main.go", "Add(cart.Item"}, []decl{add}},
		{"method declaration", place{cartGo, "Add(it Item)"}, []decl{add}},
		{"call in a file built for windows only", place{"cart/discount_windows.go", "Add(it)"}, []decl{add}},
		{"type", place{"main.go", "Cart{}"}, []decl{cartType}},
		{"field", place{"main.go", "Items, basket"}, []decl{items}},
		{"constant in parentheses", place{cartGo, "MaxItems = 100"}, []decl{maxItems}},
		{"interface method", place{cartGo, "Price(it Item)"}, []decl{pricerPrice}},
		{"local variable", place{"main.go", "basket.Add"}, []decl{{
			name: place{"main.go", "basket := "}, from: "basket := ", to: "&cart.Cart{}",
			signature: "basket := &cart.Cart{}",
		}}},
		{"dependency outside the root", place{"main.go", "Amount(300)"}, []decl{{
			name: place{"../money/money.go", "Amount int64"}, from: "type Amount int64", to: "type Amount int64",
			signature: "type Amount int64", doc: "// Amount is a sum in cents.", origin: agentlsp.OriginModule,
		}}},
		{"whitespace", place{"main.go", "\tbasket.Add"}, nil},
		{"file with a syntax error", place{brokenGo, "Ready() {}"}, []decl{{
			name: place{brokenGo, "Ready() {}"}, from: "func Ready", to: "func Ready() {}", signature: "func Ready()",
			doc: "// Ready is complete.",
		}}},
	}
	var got [][]agentlsp.Declaration
	session(t, root, func(ix *golang.Index) {
		for _, tt := range tests {
			decls, err := ix.Definition(tt.at.position(t, root))
			require.NoError(t, err, tt.name)
			got = append(got, decls)
		}
	})
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ElementsMatch(t, declarations(t, root, tt.want), got[i])
		})
	}
}

// A declaration of the standard library lies under GOROOT, whose lines change with the Go version: the test reads
// its file name, signature and origin.
func TestDefinitionInStandardLibrary(t *testing.T) {
	cache(t)
	root := newRoot(t)
	var got []agentlsp.Declaration
	session(t, root, func(ix *golang.Index) {
		var err error
		got, err = ix.Definition(place{"main.go", "Println("}.position(t, root))
		require.NoError(t, err)
	})
	require.Len(t, got, 1)
	assert.Equal(t,
		[]string{"print.go", "func Println(a ...any) (n int, err error)", string(agentlsp.OriginStd)},
		[]string{filepath.Base(got[0].File), got[0].Signature, string(got[0].Origin)})
	assert.NotEmpty(t, got[0].Doc)
}

// A declaration under the vendor directory of the root is of a vendored dependency.
func TestDefinitionInVendor(t *testing.T) {
	cache(t)
	root := filepath.Join(filepath.Dir(newRoot(t)), "vendored")
	var got []agentlsp.Declaration
	session(t, root, func(ix *golang.Index) {
		var err error
		got, err = ix.Definition(place{"main.go", "Amount(300)"}.position(t, root))
		require.NoError(t, err)
	})

	assert.Equal(t, declarations(t, root, []decl{{
		name: place{"vendor/example.com/money/money.go", "Amount int64"}, from: "type Amount int64", to: "type Amount int64",
		signature: "type Amount int64", doc: "// Amount is a sum in cents.", origin: agentlsp.OriginVendor,
	}}), got)
}

// A go.work in the working directory of the process is not of the root, though on Windows the drive-relative path
// C:go.work, on the drive of the root, names that file.
func TestDefinitionFromAnotherWorkingDirectory(t *testing.T) {
	cache(t)
	testdata := filepath.Dir(newRoot(t))
	t.Chdir(filepath.Join(testdata, "elsewhere")) // a module with a go.work of its own beside the roots

	tests := []struct {
		name string
		root string // under testdata
		at   place
		want decl
	}{
		{"vendor directory", "vendored", place{"main.go", "Amount(300)"}, decl{
			name: place{"vendor/example.com/money/money.go", "Amount int64"}, from: "type Amount int64", to: "type Amount int64",
			signature: "type Amount int64", doc: "// Amount is a sum in cents.", origin: agentlsp.OriginVendor,
		}},
		{"directory a replace names", "shop", place{cartGo, "Plus(it.Price)"}, decl{
			name: place{"../money/money.go", "Plus(b Amount)"}, from: "func (a Amount) Plus", to: "return a + b }",
			signature: "func (a Amount) Plus(b Amount) Amount", doc: "// Plus is the sum of a and b.", origin: agentlsp.OriginModule,
		}},
	}
	got := make([][]agentlsp.Declaration, len(tests))
	errs := make([]error, len(tests))
	for i, tt := range tests {
		root := filepath.Join(testdata, tt.root)
		session(t, root, func(ix *golang.Index) {
			got[i], errs[i] = ix.Definition(tt.at.position(t, root))
		})
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, errs[i])
			assert.Equal(t, declarations(t, filepath.Join(testdata, tt.root), []decl{tt.want}), got[i])
		})
	}
}

// A file changed on disk while the index is open is read as it is now: a byte that is not UTF-8 right after the
// package clause leaves the parser no declaration, and the place gopls gives is a declaration of itself.
func TestDefinitionInFileChangedWhileOpen(t *testing.T) {
	cache(t)
	root := newRoot(t)
	var got []agentlsp.Declaration
	session(t, root, func(ix *golang.Index) {
		edit(t, root, cartGo, "package cart\n\nimport", "package cart\n// \xd0\nimport")
		var err error
		got, err = ix.Definition(place{"main.go", "Add(cart.Item"}.position(t, root))
		require.NoError(t, err)
	})

	assert.Equal(t, declarations(t, root, []decl{{
		name: place{cartGo, "Add(it Item)"}, from: "Add(it Item)", to: "func (c *Cart) Add", signature: "func (c *Cart) Add(it Item) {",
	}}), got)
}

// A module the go.work of the root uses outside its directory is a part of the root, as a module under it is: a
// position in it is asked about, its declarations are of the root, Rename edits it. A directory beside a module whose
// name starts with the name of the module is not a part of the module.
func TestModuleOfGoWorkOutsideTheRoot(t *testing.T) {
	cache(t)
	root := filepath.Join(filepath.Dir(newRoot(t)), "work")
	const moneyGo = "../money/money.go"
	amount := decl{
		name: place{moneyGo, "Amount int64"}, from: "type Amount int64", to: "type Amount int64",
		signature: "type Amount int64", doc: "// Amount is a sum in cents.",
	}
	plus := decl{
		name: place{moneyGo, "Plus(b Amount)"}, from: "func (a Amount) Plus", to: "return a + b }",
		signature: "func (a Amount) Plus(b Amount) Amount", doc: "// Plus is the sum of a and b.",
	}
	call := place{"app/main.go", "Plus(1)"}
	const appsGo = "apps/apps.go" // in no module
	var defs []agentlsp.Declaration
	var refs agentlsp.References
	var renaming agentlsp.Renaming
	var syms, outline, apps []agentlsp.Symbol
	session(t, root, func(ix *golang.Index) {
		var err error
		defs, err = ix.Definition(call.position(t, root))
		require.NoError(t, err)
		refs, err = ix.References(plus.name.position(t, root))
		require.NoError(t, err)
		renaming, err = ix.Rename(plus.name.position(t, root), "Add")
		require.NoError(t, err)
		syms, err = ix.Symbols("money.Amount.Plus")
		require.NoError(t, err)
		outline, err = ix.Outline(filepath.Join(root, filepath.FromSlash(moneyGo)))
		require.NoError(t, err)
		apps, err = ix.Outline(filepath.Join(root, filepath.FromSlash(appsGo)))
		require.NoError(t, err)
	})

	assert.Equal(t, declarations(t, root, []decl{plus}), defs)
	assert.Equal(t, agentlsp.References{Uses: locations(t, root, []place{call})}, refs)
	assert.Equal(t, agentlsp.Renaming{Edits: renames(t, root, []place{{moneyGo, "Plus is"}, plus.name, call}, "Add")}, renaming)
	assert.Equal(t, symbols(t, root, []symbol{{"Plus", "method", "Amount", plus}}), syms)
	assert.Equal(t, symbols(t, root, []symbol{{"Amount", "type", "", amount}, {"Plus", "method", "Amount", plus}}), outline)
	assert.Equal(t, symbols(t, root, []symbol{{"Run", "func", "", decl{
		name: place{appsGo, "Run() {}"}, from: "func Run", to: "func Run() {}", signature: "func Run()",
	}}}), apps)
}

// A go.work of a directory above a root with a go.mod adds nothing to the root: the other modules it uses are
// dependencies.
func TestModuleRootUnderGoWorkAbove(t *testing.T) {
	cache(t)
	root := filepath.Join(filepath.Dir(newRoot(t)), "work", "app")
	var got []agentlsp.Declaration
	session(t, root, func(ix *golang.Index) {
		var err error
		got, err = ix.Definition(place{"main.go", "Plus(1)"}.position(t, root))
		require.NoError(t, err)
	})

	assert.Equal(t, declarations(t, root, []decl{{
		name: place{"../../money/money.go", "Plus(b Amount)"}, from: "func (a Amount) Plus", to: "return a + b }",
		signature: "func (a Amount) Plus(b Amount) Amount", doc: "// Plus is the sum of a and b.", origin: agentlsp.OriginModule,
	}}), got)
}

// Origin of a file follows the rule of the Origin of a Declaration; the file need not exist.
func TestOrigin(t *testing.T) {
	cache(t)
	testdata := filepath.Dir(newRoot(t))
	goroot, modcache := goEnv(t, "GOROOT"), goEnv(t, "GOMODCACHE")

	tests := []struct {
		name string
		root string // under testdata
		path string
		want agentlsp.Origin
	}{
		{"file of the root", "shop", filepath.Join(testdata, "shop", "cart", "cart.go"), agentlsp.OriginRoot},
		{"module the go.work of the root uses outside it", "work", filepath.Join(testdata, "money", "money.go"), agentlsp.OriginRoot},
		{"module a go.work above the root uses", "work/app", filepath.Join(testdata, "money", "money.go"), agentlsp.OriginModule},
		{"directory a replace names", "shop", filepath.Join(testdata, "money", "money.go"), agentlsp.OriginModule},
		{"vendor directory of the root", "vendored", filepath.Join(testdata, "vendored", "vendor", "example.com", "money", "money.go"),
			agentlsp.OriginVendor},
		{"standard library", "shop", filepath.Join(goroot, "src", "fmt", "print.go"), agentlsp.OriginStd},
		{"module cache", "shop", filepath.Join(modcache, "example.com", "money@v1.0.0", "money.go"), agentlsp.OriginModule},
	}
	var got []agentlsp.Origin
	for _, tt := range tests {
		session(t, filepath.Join(testdata, filepath.FromSlash(tt.root)), func(ix *golang.Index) {
			got = append(got, ix.Origin(tt.path))
		})
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, got[i])
		})
	}
}

// A position that is not a place in a file of the root is an error of Definition and References alike.
func TestPositionErrors(t *testing.T) {
	cache(t)
	root := newRoot(t)
	main := filepath.Join(root, "main.go")

	tests := []struct {
		name string
		at   agentlsp.Position
		want string
	}{
		{"relative path", agentlsp.Position{File: "main.go", Line: 12, Column: 2}, "the path is not absolute"},
		{"file outside the root", agentlsp.Position{File: filepath.Join(root, "..", "money", "money.go"), Line: 5, Column: 6}, "the file is not under the root"},
		{"file that does not exist", agentlsp.Position{File: filepath.Join(root, "gone.go"), Line: 1, Column: 1}, "the file does not exist"},
		{"line 0", agentlsp.Position{File: main, Line: 0, Column: 1}, "no line 0: the file has lines 1 to 15"},
		{"line past the end of the file", agentlsp.Position{File: main, Line: 16, Column: 1}, "no line 16: the file has lines 1 to 15"},
		{"column 0", agentlsp.Position{File: main, Line: 12, Column: 0}, "no column 0: line 12 has columns 1 to 62"},
		{"column past the end of the line", agentlsp.Position{File: main, Line: 12, Column: 63}, "no column 63: line 12 has columns 1 to 62"},
		{"end of the line", agentlsp.Position{File: main, Line: 12, Column: 62}, ""},
	}
	var defs, refs []error
	session(t, root, func(ix *golang.Index) {
		for _, tt := range tests {
			_, err := ix.Definition(tt.at)
			defs = append(defs, err)
			_, err = ix.References(tt.at)
			refs = append(refs, err)
		}
	})
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, []string{tt.want, tt.want}, []string{problem(tt.at, defs[i]), problem(tt.at, refs[i])})
		})
	}
}

// A .go file that none of linux, darwin and windows builds is in no package: a question about it is an error.
func TestFileOfNoPlatform(t *testing.T) {
	cache(t)
	root := newRoot(t)
	at := place{"cart/cart_plan9.go", "plan9() {}"}.position(t, root)
	var defErr, refsErr error
	session(t, root, func(ix *golang.Index) {
		_, defErr = ix.Definition(at)
		_, refsErr = ix.References(at)
	})
	assert.ErrorContains(t, defErr, "cart_plan9.go is in no package for linux, darwin, windows")
	assert.ErrorContains(t, refsErr, "cart_plan9.go is in no package for linux, darwin, windows")
}

// References joins the answers of linux, darwin and windows: a use or an implementation in a file one of them builds
// is in it, a place all of them see is in it once. The answer is the same from a use and from the declaration. A
// symbol declared for the platforms apart is one symbol: the uses of every declaration are in the answer.
func TestReferences(t *testing.T) {
	cache(t)
	root := newRoot(t)
	addCalls := []place{{discountGo, "Add(it)"}, {restockGo, "Add(c.Items[0])"}, {"main.go", "Add(cart.Item"}}
	priceCalls := []place{{discountGo, "Price(it)"}, {listGo, "Price(it)"}}
	itemsUses := []place{{cartGo, "Items = append"}, {cartGo, "Items, it)"}, {cartGo, "Items))"}, {restockGo, "Items[0]"},
		{"main.go", "Items, basket"}}

	tests := []struct {
		name  string
		at    place
		uses  []place
		impls []place
	}{
		{"method call", place{"main.go", "Add(cart.Item"}, addCalls, nil},
		{"method declaration", place{cartGo, "Add(it Item)"}, addCalls, nil},
		{"call in a file built for windows only", place{discountGo, "Add(it)"}, addCalls, nil},
		{"call in a file built for darwin only", place{restockGo, "Add(c.Items[0])"}, addCalls, nil},
		{"interface method", place{cartGo, "Price(it Item)"}, priceCalls,
			[]place{{discountGo, "Price(it Item)"}, {listGo, "Price(it Item)"}}},
		{"interface type", place{cartGo, "Pricer interface"}, []place{{discountGo, "Pricer, it"}, {listGo, "Pricer, it"}, {pricersGo, "Pricer{List"}},
			[]place{{discountGo, "Discount struct{}"}, {listGo, "List struct{}"}}},
		{"method of a concrete type", place{listGo, "Price(it Item)"}, priceCalls, nil},
		{"field", place{cartGo, "Items []Item"}, itemsUses, nil},
		{"use after text that is not ASCII on its line", place{cartGo, "Items))"}, itemsUses, nil},
		{"function declared for the platforms apart", place{storeOtherGo, "store(c *Cart"},
			[]place{{saveGo, "store(c, c.path())"}, {storeWindowsGo, "store(c, c.path()+"}}, nil},
		{"whitespace", place{"main.go", "\tbasket.Add"}, nil, nil},
	}
	var got []agentlsp.References
	var errs []error
	session(t, root, func(ix *golang.Index) {
		for _, tt := range tests {
			refs, err := ix.References(tt.at.position(t, root))
			got, errs = append(got, refs), append(errs, err)
		}
	})
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, errs[i])
			assert.Equal(t, agentlsp.References{Uses: locations(t, root, tt.uses),
				Implementations: locations(t, root, tt.impls)}, got[i])
		})
	}
}

func TestSymbols(t *testing.T) {
	cache(t)
	root := newRoot(t)
	writeBroken(t, root)
	addMethod := symbol{"Add", "method", "Cart", add}
	itemPrice := symbol{"Price", "field", "Item", decl{
		name: place{cartGo, "Price money.Amount"}, from: "Price money.Amount", to: "Price money.Amount",
		signature: "Price money.Amount",
	}}

	tests := []struct {
		name  string
		query string
		want  []symbol
	}{
		{"bare name", "Add", []symbol{addMethod}},
		{"type and method", "Cart.Add", []symbol{addMethod}},
		{"package, type and method", "cart.Cart.Add", []symbol{addMethod}},
		{"package path, type and method", "example.com/shop/cart.Cart.Add", []symbol{addMethod}},
		{"package and constant", "cart.MaxItems", []symbol{{"MaxItems", "const", "", maxItems}}},
		{"bare name of fields and methods", "Price", []symbol{itemPrice, {"Price", "method", "Pricer", pricerPrice},
			{"Price", "method", "Discount", decl{
				name: place{"cart/discount_windows.go", "Price(it Item)"}, from: "func (Discount) Price", to: "it.Price / 2 }",
				signature: "func (Discount) Price(it Item) money.Amount", doc: "// Price is half the price of it.",
			}},
			{"Price", "method", "List", decl{
				name: place{"cart/list.go", "Price(it Item)"}, from: "func (List) Price", to: "return it.Price }",
				signature: "func (List) Price(it Item) money.Amount", doc: "// Price is the price of it.",
			}},
		}},
		{"type built for windows only", "Discount", []symbol{{"Discount", "type", "", decl{
			name: place{"cart/discount_windows.go", "Discount struct{}"}, from: "type Discount struct{}",
			to: "type Discount struct{}", signature: "type Discount struct", doc: "// Discount prices an item at half its price.",
		}}}},
		{"field", "Cart.Items", []symbol{{"Items", "field", "Cart", items}}},
		{"function of package main", "main.main", []symbol{{"main", "func", "", decl{
			name: place{"main.go", "main() {"}, from: "func main() {", to: "basket.Total())\n}", signature: "func main()",
		}}}},
		{"dependency is not searched", "Plus", nil},
		{"wrong type", "Item.Add", nil},
		{"function cut short at the end of the file", "Broken", []symbol{{"Broken", "func", "", decl{
			name: place{brokenGo, "Broken("}, from: "func Broken", to: "( {\n", signature: "func Broken( {",
		}}}},
	}
	var got [][]agentlsp.Symbol
	session(t, root, func(ix *golang.Index) {
		for _, tt := range tests {
			syms, err := ix.Symbols(tt.query)
			require.NoError(t, err, tt.name)
			got = append(got, syms)
		}
	})
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, symbols(t, root, tt.want), append([]agentlsp.Symbol{}, got[i]...))
		})
	}
}

func TestOutline(t *testing.T) {
	cache(t)
	root := newRoot(t)
	field := func(name, container, text string) symbol {
		return symbol{name, "field", container, decl{name: place{cartGo, text}, from: text, to: text, signature: text}}
	}
	want := []symbol{
		{"Cart", "type", "", cartType},
		{"Items", "field", "Cart", items},
		field("total", "Cart", "total money.Amount"),
		{"Item", "type", "", decl{
			name: place{cartGo, "Item struct {"}, from: "type Item struct", to: "Price money.Amount\n}",
			signature: "type Item struct", doc: "// Item is one line of a cart.",
		}},
		field("Name", "Item", "Name  string"),
		field("Price", "Item", "Price money.Amount"),
		{"Add", "method", "Cart", add},
		{"Total", "method", "Cart", decl{
			name: place{cartGo, "Total() money"}, from: "func (c *Cart) Total()", to: "return c.total }",
			signature: "func (c *Cart) Total() money.Amount", doc: "// Total is the sum of the prices.",
		}},
		{"String", "method", "Cart", decl{
			name: place{cartGo, "String() string"}, from: "func (c *Cart) String()", to: "len(c.Items))\n}",
			signature: "func (c *Cart) String() string",
		}},
		{"MaxItems", "const", "", maxItems},
		{"MinItems", "const", "", decl{
			name: place{cartGo, "MinItems = 0"}, from: "MinItems = 0", to: "MinItems = 0", signature: "const MinItems = 0",
		}},
		{"Pricer", "type", "", decl{
			name: place{cartGo, "Pricer interface"}, from: "type Pricer interface", to: "Price(it Item) money.Amount\n}",
			signature: "type Pricer interface", doc: "// Pricer prices an item.",
		}},
		{"Price", "method", "Pricer", pricerPrice},
	}
	var got []agentlsp.Symbol
	var outside error
	session(t, root, func(ix *golang.Index) {
		var err error
		got, err = ix.Outline(filepath.Join(root, "cart", "cart.go"))
		require.NoError(t, err)
		_, outside = ix.Outline(filepath.Join(root, "..", "money", "money.go"))
	})
	assert.Equal(t, symbols(t, root, want), got)
	assert.ErrorContains(t, outside, "the file is not under the root")
}

// Each Open is a new process as far as the index goes: a new session of gopls, and the cache database. The stored
// load of packages serves while the files keep their package names and imports; any other edit is still seen. The
// question is in a file built for windows only: GOOS of the environment changes no answer.
func TestReopen(t *testing.T) {
	cache(t)
	root := newRoot(t)
	call := place{"cart/discount_windows.go", "Add(it)"}

	steps := []struct {
		name    string
		change  func()
		wantLog string
	}{
		{"first open loads the packages", func() {}, loaded},
		{"same files reuse the load", func() {}, reused},
		{"an edit that keeps the imports reuses the load", func() {
			edit(t, root, cartGo, "// Add puts", "// Item can be added twice.\n\n// Add puts")
		}, reused},
		{"an edit of the imports loads again", func() {
			edit(t, root, cartGo, `"fmt"`, `"fmt"
	_ "strings"`)
		}, loaded},
		{"an edit of go.mod loads again", func() {
			edit(t, root, "go.mod", "go 1.26", "go 1.26\n\n// A module of a shop.")
		}, loaded},
		{"a new file loads again", func() {
			require.NoError(t, os.WriteFile(filepath.Join(root, "cart", "empty.go"), []byte("package cart\n"), 0o600))
		}, loaded},
		{"GOOS of the environment reuses the load", func() { t.Setenv("GOOS", "darwin") }, reused},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			step.change()
			var got []agentlsp.Declaration
			log := session(t, root, func(ix *golang.Index) {
				var err error
				got, err = ix.Definition(call.position(t, root))
				require.NoError(t, err)
			})
			assert.Contains(t, log, step.wantLog)
			assert.Equal(t, declarations(t, root, []decl{add}), got)
		})
	}
}

// Two worktrees of a module share the cache entries of what lies outside them (the standard library, the module
// cache); each answers with its own files.
func TestSecondWorktree(t *testing.T) {
	cache(t)
	first, second := newRoot(t), newRoot(t)
	call := place{"main.go", "Add(cart.Item"}
	var got []agentlsp.Declaration
	ask := func(root string) func(ix *golang.Index) {
		return func(ix *golang.Index) {
			var err error
			got, err = ix.Definition(call.position(t, root))
			require.NoError(t, err)
		}
	}

	firstLog := session(t, first, ask(first))
	secondLog := session(t, second, ask(second))

	assert.Contains(t, firstLog, cacheUnused)
	assert.NotContains(t, secondLog, cacheUnused)
	assert.Equal(t, declarations(t, second, []decl{add}), got)
}

// A stored load of packages depends on the target architecture and the build flags, as the go command sees them: from
// the environment of the process and from the file of go env -w (GOENV). A load for another one is never reused.
func TestLoadOfAnotherPlatformIsNotReused(t *testing.T) {
	setenv := func(name, value string) func(t *testing.T, goenv string) {
		return func(t *testing.T, _ string) { t.Setenv(name, value) }
	}
	tests := []struct {
		name   string
		change func(t *testing.T, goenv string)
	}{
		{"GOARCH", setenv("GOARCH", "arm64")},
		{"build tags", setenv("GOFLAGS", "-tags=premium")},
		{"build tags of go env -w", func(t *testing.T, goenv string) {
			require.NoError(t, os.WriteFile(goenv, []byte("GOFLAGS=-tags=premium\n"), 0o600))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache(t)
			root := newRoot(t)
			goenv := filepath.Join(t.TempDir(), "env")
			t.Setenv("GOENV", goenv)
			session(t, root, func(*golang.Index) {})
			tt.change(t, goenv)

			log := session(t, root, func(*golang.Index) {})

			assert.Contains(t, log, loaded)
			assert.NotContains(t, log, reused)
		})
	}
}

// A root opened through a symlink is walked through it: a file added under it is seen by the next Open, and Rename
// finds the files no platform builds under it. Answers name
// files by their paths through the root as Open got it, the paths the positions use.
func TestSymlinkedRoot(t *testing.T) {
	cache(t)
	root := filepath.Join(tempDir(t), "shop")
	symlink(t, newRoot(t), root)
	session(t, root, func(*golang.Index) {})
	require.NoError(t, os.WriteFile(filepath.Join(root, "cart", "extra.go"), []byte("package cart\n\nfunc Extra() {}\n"), 0o600))

	var defs []agentlsp.Declaration
	var syms []agentlsp.Symbol
	var renameErr error
	log := session(t, root, func(ix *golang.Index) {
		var err error
		defs, err = ix.Definition(place{"main.go", "Add(cart.Item"}.position(t, root))
		require.NoError(t, err)
		syms, err = ix.Symbols("Extra")
		require.NoError(t, err)
		_, renameErr = ix.Rename(place{cartGo, "MaxItems = 100"}.position(t, root), "Most")
	})

	assert.Contains(t, log, loaded)
	assert.Equal(t, declarations(t, root, []decl{add}), defs)
	assert.Equal(t, symbols(t, root, []symbol{{"Extra", "func", "", decl{
		name: place{"cart/extra.go", "Extra() {}"}, from: "func Extra", to: "func Extra() {}", signature: "func Extra()",
	}}}), syms)
	assert.ErrorContains(t, renameErr, "MaxItems is in files none of linux, darwin, windows builds, which the renaming would not edit: "+
		filepath.Join(root, "cart", "limits_integration_test.go")+":9:5, "+filepath.Join(root, "report", "report.go")+":10:15")
}

// Close may be called again; the second call does nothing.
func TestCloseTwice(t *testing.T) {
	cache(t)
	ix, err := golang.Open(newRoot(t), nil)
	require.NoError(t, err)
	require.NoError(t, ix.Close())

	assert.NoError(t, ix.Close())
}

// A process of a program that imports the library makes no directory in the user cache directory but its own cache:
// gopls would make goimports there on start. A test binary keeps it under the temporary directory instead, so the
// test runs a program built from testdata/open.
func TestOpenMakesNoGoimportsDirectory(t *testing.T) {
	// .exe: Windows starts a file only by an executable extension; elsewhere the name does not matter.
	bin := filepath.Join(t.TempDir(), "open.exe")
	build := exec.Command("go", "build", "-o", bin, "./testdata/open")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "go build:\n%s", out)
	cache(t)
	dir, err := os.UserCacheDir()
	require.NoError(t, err)

	out, err = exec.Command(bin, newRoot(t)).CombinedOutput()

	require.NoError(t, err, "%s", out)
	assert.DirExists(t, filepath.Join(dir, "agent-lsp"))
	assert.NoDirExists(t, filepath.Join(dir, "goimports"))
}

// The gopls cache is global to the process: a second Open while an index is open fails.
func TestOpenWhileAnotherIsOpen(t *testing.T) {
	cache(t)
	root := newRoot(t)
	first, err := golang.Open(root, nil)
	require.NoError(t, err)
	defer first.Close()

	_, err = golang.Open(root, nil)

	assert.ErrorContains(t, err, "another Go index is open")
}

func TestOpenErrors(t *testing.T) {
	cache(t)
	root := newRoot(t)

	tests := []struct {
		name string
		root string
		want string
	}{
		{"directory without go.mod or go.work", filepath.Join(root, "cart"), "no go.mod or go.work"},
		{"directory that does not exist", filepath.Join(root, "gone"), "no go.mod or go.work"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := golang.Open(tt.root, nil)
			assert.ErrorContains(t, err, tt.want)
		})
	}
}
