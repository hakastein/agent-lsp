package php_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	agentlsp "github.com/hakastein/agent-lsp"
)

var (
	cartAdd = place{"src/Shop/Cart.php", "add(string $sku): void"}
	wishAdd = place{"src/Shop/Wishlist.php", "add(string $sku): void"}

	checkoutCartAddCalls = []place{
		{"src/Shop/Checkout.php", "add('apple')"},
		{"src/Shop/Checkout.php", "ADD('pear')"},
	}
	appCartAddCalls = []place{
		{"src/app.php", "add('kiwi')"},
		{"src/app.php", "add('lime')"},
		{"src/app.php", "add('date')"},
		{"src/app.php", "add('emoji')"},
		{"src/app.php", "add('pea')"},
		{"src/app.php", "add('oat')"},
	}
	cartAddCalls = append(append([]place{}, checkoutCartAddCalls...), appCartAddCalls...)
)

func TestMethodDefinition(t *testing.T) {
	cache(t)
	root := newRoot(t, "methods")
	ix, _ := open(t, root)

	tests := []struct {
		name string
		at   place
		want []place
	}{
		{"call on a variable assigned new", place{"src/Shop/Checkout.php", "add('apple')"}, []place{cartAdd}},
		{"method name in another case", place{"src/Shop/Checkout.php", "ADD('pear')"}, []place{cartAdd}},
		{"call on new in parentheses", place{"src/app.php", "add('lime')"}, []place{cartAdd}},
		{"nullsafe call", place{"src/app.php", "add('date')"}, []place{cartAdd}},
		{"call after Cyrillic and an emoji on the line", place{"src/app.php", "add('emoji')"}, []place{cartAdd}},
		{"variable of the arrow function's parent", place{"src/app.php", "add('pea')"}, []place{cartAdd}},
		{"variable imported by a closure", place{"src/app.php", "add('oat')"}, []place{cartAdd}},
		{"same method name of another class", place{"src/Shop/Checkout.php", "add('plum')"}, []place{wishAdd}},
		{"declaration itself", cartAdd, []place{cartAdd}},
		{"parameter of unknown class", place{"src/Shop/Checkout.php", "add('fig')"}, nil},
		{"variable assigned two classes", place{"src/app.php", "add('nut')"}, nil},
		{"call before the assignment", place{"src/app.php", "add('yam')"}, nil},
		{"method the class does not declare", place{"src/app.php", "remove('rye')"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ElementsMatch(t, locations(t, root, tt.want), definition(t, ix, root, tt.at))
		})
	}
}

func TestMethodReferences(t *testing.T) {
	cache(t)
	root := newRoot(t, "methods")
	ix, _ := open(t, root)

	tests := []struct {
		name string
		at   place
		want []place
	}{
		{"from a call site", place{"src/app.php", "add('kiwi')"}, cartAddCalls},
		{"from the declaration", cartAdd, cartAddCalls},
		{"same method name of another class", wishAdd, []place{{"src/Shop/Checkout.php", "add('plum')"}}},
		{"method with one call", place{"src/Shop/Cart.php", "total(): int"}, []place{{"src/Shop/Checkout.php", "total();"}}},
		{"unresolved call", place{"src/Shop/Checkout.php", "add('fig')"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ElementsMatch(t, locations(t, root, tt.want), uses(t, ix, root, tt.at))
		})
	}
}

// A caller tells an operation PHP does not answer yet from a failed one by agentlsp.ErrNotSupported.
func TestOperationsWithoutAnAnswerAreNotSupported(t *testing.T) {
	cache(t)
	root := newRoot(t, "methods")
	ix, _ := open(t, root)
	at := cartAdd.position(t, root)
	_, rename := ix.Rename(at, "put")
	_, symbols := ix.Symbols("add")
	_, outline := ix.Outline(at.File)

	assert.ErrorIs(t, rename, agentlsp.ErrNotSupported)
	assert.ErrorIs(t, symbols, agentlsp.ErrNotSupported)
	assert.ErrorIs(t, outline, agentlsp.ErrNotSupported)
}
