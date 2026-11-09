package golang_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentlsp "github.com/hakastein/agent-lsp"
	"github.com/hakastein/agent-lsp/golang"
)

const (
	discountGo     = "cart/discount_windows.go"
	listGo         = "cart/list.go"
	restockGo      = "cart/restock_darwin.go"
	saveGo         = "cart/save.go"
	storeOtherGo   = "cart/store_other.go"
	storeWindowsGo = "cart/store_windows.go"
	namerGo        = "cart/namer.go"
	datedGo        = "cart/dated.go"
	pricersGo      = "cart/pricers.go"
	fillTestGo     = "cart/fill_test.go"
)

// Rename gives the edits of every platform: in files only one of them builds, in the methods of the types converted
// to the interface of a renamed interface method, in every declaration of a symbol declared for the platforms apart.
// The root with the edits made builds for linux, darwin and windows.
func TestRename(t *testing.T) {
	gocache := goEnv(t, "GOCACHE")
	cache(t)

	tests := []struct {
		name  string
		at    place
		to    string
		edits []place // in the order of files, lines and columns
	}{
		{"method, from a call", place{"main.go", "Add(cart.Item"}, "Put", []place{
			{cartGo, "Add puts"}, {cartGo, "Add(it Item)"}, {discountGo, "Add(it)"}, {restockGo, "Add(c.Items[0])"},
			{"main.go", "Add(cart.Item"},
		}},
		{"interface method and the implementation converted to it", place{cartGo, "Price(it Item)"}, "Cost", []place{
			{cartGo, "Price is the price"}, {cartGo, "Price(it Item)"}, {discountGo, "Price(it)"},
			{listGo, "Price is the price"}, {listGo, "Price(it Item)"}, {listGo, "Price(it)"},
		}},
		{"interface type, without the types that implement it", place{cartGo, "Pricer interface"}, "Quoter", []place{
			{cartGo, "Pricer prices"}, {cartGo, "Pricer interface"}, {discountGo, "Pricer, it"}, {listGo, "Pricer, it"},
			{pricersGo, "Pricer{List"},
		}},
		{"interface method, an implementation not converted to it", place{namerGo, "String() string"}, "Name", []place{
			{namerGo, "String() string"},
		}},
		{"function of the tests, from a file built for windows only", place{"cart/backup_windows_test.go", "fill()"}, "empty",
			[]place{{"cart/backup_windows_test.go", "fill()"}, {fillTestGo, "fill is"}, {fillTestGo, "fill() *Cart"},
				{"cart/save_other_test.go", "fill()"}}},
		{"whitespace", place{"main.go", "\tbasket.Add"}, "x", nil},
		{"function declared for the platforms apart", place{storeOtherGo, "store(c *Cart"}, "keep", []place{
			{saveGo, "store(c, c.path())"}, {storeOtherGo, "store keeps"}, {storeOtherGo, "store(c *Cart"},
			{storeWindowsGo, "store keeps"}, {storeWindowsGo, "store(c *Cart"}, {storeWindowsGo, "store(c, c.path()+"},
		}},
		{"method declared for the platforms apart", place{storeWindowsGo, "path() string"}, "file", []place{
			{saveGo, "path())"}, {storeOtherGo, "path is the file"}, {storeOtherGo, "path() string"},
			{storeWindowsGo, "path is the file"}, {storeWindowsGo, "path() string"}, {storeWindowsGo, "path()+"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRoot(t)
			var got agentlsp.Renaming
			var err error
			session(t, root, func(ix *golang.Index) {
				got, err = ix.Rename(tt.at.position(t, root), tt.to)
			})
			require.NoError(t, err)
			assert.Equal(t, agentlsp.Renaming{Edits: renames(t, root, tt.edits, tt.to)}, got)

			apply(t, got.Edits)

			assert.Equal(t, map[string]string{"linux": "", "darwin": "", "windows": ""}, vet(t, root, gocache))
		})
	}
}

// Rename refuses what it cannot rename whole: a name taken in the scope, a symbol declared outside the root, a type
// outside the root converted to the interface, a method fmt finds through any, a field another platform declares
// where Rename cannot find it.
func TestRenameRefusals(t *testing.T) {
	cache(t)
	root := newRoot(t)

	tests := []struct {
		name string
		at   place
		to   string
		want string
	}{
		{"name of another method", place{cartGo, "Add(it Item)"}, "Total",
			"shop/cart/cart.go:30:16:\twould conflict with this method"},
		{"standard library", place{"main.go", "Println"}, "Print", "Println is declared outside the root (std) at "},
		{"dependency", place{cartGo, "Plus(it.Price)"}, "Add", "Plus is declared outside the root (module) at "},
		{"interface method, a type of the standard library converted to it", place{datedGo, "Weekday() time"}, "DayOfWeek",
			"func (time.Time).Weekday() time.Weekday must be renamed too, but the renaming edits only example.com/shop/cart and the packages that import it"},
		{"method fmt looks for", place{cartGo, "String() string {"}, "Describe",
			"renaming this method \"String\" to \"Describe\" would make *example.com/shop/cart.Cart no longer implement fmt.Stringer"},
		{"field of a struct type without a name, from a file not every platform builds", place{storeWindowsGo, "count int"},
			"n", "count is a field or method of a type without a name at the package level, in a file not every platform builds"},
	}
	errs := make([]error, len(tests))
	session(t, root, func(ix *golang.Index) {
		for i, tt := range tests {
			_, errs[i] = ix.Rename(tt.at.position(t, root), tt.to)
		}
	})
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ErrorContains(t, errs[i], tt.want)
		})
	}
}

// A package is not renamed, at its clause or at its import name.
func TestRenameOfAPackageIsNotSupported(t *testing.T) {
	cache(t)
	root := newRoot(t)
	var clauseErr, importErr error
	session(t, root, func(ix *golang.Index) {
		_, clauseErr = ix.Rename(place{cartGo, "cart\n\nimport"}.position(t, root), "basket")
		_, importErr = ix.Rename(place{"main.go", "cart.Item"}.position(t, root), "basket")
	})
	assert.ErrorIs(t, clauseErr, agentlsp.ErrNotSupported)
	assert.ErrorIs(t, importErr, agentlsp.ErrNotSupported)
}

// A symbol of a vendored dependency is outside the root too, though its file is under the root.
func TestRenameInVendor(t *testing.T) {
	cache(t)
	root := filepath.Join(filepath.Dir(newRoot(t)), "vendored")
	var err error
	session(t, root, func(ix *golang.Index) {
		_, err = ix.Rename(place{"main.go", "Amount(300)"}.position(t, root), "Sum")
	})
	assert.ErrorContains(t, err, "Amount is declared outside the root (vendor) at "+
		filepath.Join(root, "vendor", "example.com", "money", "money.go")+":5:6")
}
