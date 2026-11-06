package php_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// knownScopeCalls is every call in testdata/scopes whose receiver class is known to be Shop\Cart.
var knownScopeCalls = []place{
	{"src/top.php", "add('top-level-new')"},
	{"src/top.php", "add('closure-own')"},
	{"src/top.php", "add('function-body')"},
	{"src/top.php", "add('function-new')"},
	{"src/top.php", "add('function-arrow')"},
	{"src/top.php", "add('function-closure')"},
	{"src/top.php", "add('nested-arrows')"},
	{"src/top.php", "add('closure-in-arrow')"},
	{"src/top.php", "add('method-body')"},
	{"src/superglobals.php", "add('local-session')"},
	{"src/superglobals.php", "add('lower-case-session')"},
}

// A variable's class is known only inside a function, method or closure body, from the writes in that body: any
// file may reach a top-level variable, and any function a superglobal.
func TestReceiverScopes(t *testing.T) {
	cache(t)
	root := workspace(t, "scopes")
	ix, _ := open(t, root)

	tests := []struct {
		name string
		at   place
		want []place
	}{
		{"top-level variable", place{"src/top.php", "add('top-level')"}, nil},
		{"new at top level", place{"src/top.php", "add('top-level-new')"}, []place{cartAdd}},
		{"top-level variable in an arrow function", place{"src/top.php", "add('top-level-arrow')"}, nil},
		{"top-level variable imported by a closure", place{"src/top.php", "add('top-level-closure')"}, nil},
		{"top-level variable through nested arrow functions", place{"src/top.php", "add('top-level-nested')"}, nil},
		{"closure's own variable at top level", place{"src/top.php", "add('closure-own')"}, []place{cartAdd}},
		{"variable of a function body", place{"src/top.php", "add('function-body')"}, []place{cartAdd}},
		{"new in a function body", place{"src/top.php", "add('function-new')"}, []place{cartAdd}},
		{"function variable in an arrow function", place{"src/top.php", "add('function-arrow')"}, []place{cartAdd}},
		{"function variable imported by a closure", place{"src/top.php", "add('function-closure')"}, []place{cartAdd}},
		{"function variable through nested arrow functions", place{"src/top.php", "add('nested-arrows')"},
			[]place{cartAdd}},
		{"function variable imported by a closure in an arrow function", place{"src/top.php", "add('closure-in-arrow')"},
			[]place{cartAdd}},
		{"variable of a method body", place{"src/top.php", "add('method-body')"}, []place{cartAdd}},
		{"alias made by another file", place{"src/aliased.php", "add('alias-from-another-file')"}, nil},
		{"global in eval", place{"src/evaluated.php", "add('global-in-eval')"}, nil},
		{"global in a non-.php file", place{"src/included.php", "add('global-in-a-non-php-file')"}, nil},
		{"$_SESSION at top level", place{"src/superglobals.php", "add('session-top-level')"}, nil},
		{"$_SESSION in a function", place{"src/superglobals.php", "add('session-function')"}, nil},
		{"$_SERVER", place{"src/superglobals.php", "add('server')"}, nil},
		{"$_GET", place{"src/superglobals.php", "add('get')"}, nil},
		{"$_POST", place{"src/superglobals.php", "add('post')"}, nil},
		{"$_FILES", place{"src/superglobals.php", "add('files')"}, nil},
		{"$_COOKIE", place{"src/superglobals.php", "add('cookie')"}, nil},
		{"$_REQUEST", place{"src/superglobals.php", "add('request')"}, nil},
		{"$_ENV", place{"src/superglobals.php", "add('env')"}, nil},
		{"local named like a superglobal", place{"src/superglobals.php", "add('local-session')"}, []place{cartAdd}},
		{"superglobal name in another case", place{"src/superglobals.php", "add('lower-case-session')"}, []place{cartAdd}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ElementsMatch(t, locations(t, root, tt.want), definition(t, ix, root, tt.at))
		})
	}
	t.Run("references from the declaration", func(t *testing.T) {
		assert.ElementsMatch(t, locations(t, root, knownScopeCalls), uses(t, ix, root, cartAdd))
	})
}
