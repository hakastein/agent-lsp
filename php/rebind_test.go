package php_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

var (
	rebindCartAdd   = place{"src/Shop/Cart.php", "add(string $sku): void"}
	rebindResolving = []place{
		{"src/functions.php", "add('plain')"},
		{"src/functions.php", "add('mutated')"},
	}
)

// A variable holds `new X` only when nothing in the program can bind it to another value: a call on it that
// something else may rebind has no definition.
func TestRebindableVariableIsUnknown(t *testing.T) {
	cache(t)
	root := newRoot(t, "rebind")
	ix, _ := open(t, root)

	tests := []struct {
		name string
		at   place
		want []place
	}{
		{"global in another function", place{"src/top.php", "add('global')"}, nil},
		{"$GLOBALS in another function", place{"src/top.php", "add('globals')"}, nil},
		{"by-reference parameter", place{"src/functions.php", "add('by-reference')"}, nil},
		{"by-reference parameter of a builtin", place{"src/functions.php", "add('builtin')"}, nil},
		{"argument of a function", place{"src/functions.php", "add('function-argument')"}, nil},
		{"argument of a method", place{"src/functions.php", "add('method-argument')"}, nil},
		{"argument of new", place{"src/functions.php", "add('new-argument')"}, nil},
		{"variable variable", place{"src/functions.php", "add('variable-variable')"}, nil},
		{"extract", place{"src/functions.php", "add('extract')"}, nil},
		{"include", place{"src/functions.php", "add('include')"}, nil},
		{"eval", place{"src/functions.php", "add('eval')"}, nil},
		{"parse_str with one argument", place{"src/functions.php", "add('parse-str')"}, nil},
		{"reference assignment", place{"src/functions.php", "add('reference-assign')"}, nil},
		{"foreach by reference", place{"src/functions.php", "add('foreach-by-reference')"}, nil},
		{"short list", place{"src/functions.php", "add('short-list')"}, nil},
		{"keyed list", place{"src/functions.php", "add('keyed-list')"}, nil},
		{"catch", place{"src/functions.php", "add('catch')"}, nil},
		{"use by reference", place{"src/functions.php", "add('use-by-reference')"}, nil},
		{"unset", place{"src/functions.php", "add('unset')"}, nil},
		{"braced variable variable", place{"src/functions.php", "add('braced-variable')"}, nil},
		{"mb_parse_str with one argument", place{"src/functions.php", "add('mb-parse-str')"}, nil},
		{"global variable variable", place{"src/functions.php", "add('global-variable-variable')"}, nil},
		{"function body", place{"src/functions.php", "add('plain')"}, []place{rebindCartAdd}},
		{"object passed to its method", place{"src/functions.php", "add('mutated')"}, []place{rebindCartAdd}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ElementsMatch(t, locations(t, root, tt.want), definition(t, ix, root, tt.at))
		})
	}
	t.Run("uses", func(t *testing.T) {
		assert.ElementsMatch(t, locations(t, root, rebindResolving), uses(t, ix, root, rebindCartAdd))
	})
}
