package php_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	agentlsp "github.com/hakastein/agent-lsp"
)

// A position that is not a place in a file of the root is an error that repeats it; a place where no Symbol is, is
// an empty answer. src/app.php has 27 lines and a newline after the last; line 8, `    $basket->add('kiwi');`, has
// 25 bytes.
func TestPositionThatIsNotAPlaceIsAnError(t *testing.T) {
	cache(t)
	root := newRoot(t, "methods")
	ix, _ := open(t, root)
	app := filepath.Join(root, "src", "app.php")

	tests := []struct {
		name string
		at   agentlsp.Position
		want string // what the error says after the position; "" for an empty answer and no error
	}{
		{"relative path", agentlsp.Position{File: filepath.Join("src", "app.php"), Line: 8, Column: 5}, "the path is not absolute"},
		{"file outside the root", agentlsp.Position{File: filepath.Join(root, "..", "app.php"), Line: 8, Column: 5}, "the file is not under the root"},
		{"file that does not exist", agentlsp.Position{File: filepath.Join(root, "src", "gone.php"), Line: 8, Column: 5}, "the file does not exist"},
		{"line 0", agentlsp.Position{File: app, Line: 0, Column: 5}, "no line 0: the file has lines 1 to 28"},
		{"line past the end of the file", agentlsp.Position{File: app, Line: 29, Column: 1}, "no line 29: the file has lines 1 to 28"},
		{"column 0", agentlsp.Position{File: app, Line: 8, Column: 0}, "no column 0: line 8 has columns 1 to 26"},
		{"column past the end of the line", agentlsp.Position{File: app, Line: 8, Column: 27}, "no column 27: line 8 has columns 1 to 26"},
		{"whitespace", agentlsp.Position{File: app, Line: 8, Column: 1}, ""},
		{"comment", agentlsp.Position{File: app, Line: 11, Column: 6}, ""},
		{"end of the line", agentlsp.Position{File: app, Line: 8, Column: 26}, ""},
		{"empty line after the last newline", agentlsp.Position{File: app, Line: 28, Column: 1}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decls, declsErr := ix.Definition(tt.at)
			refs, refsErr := ix.References(tt.at)

			assert.Empty(t, decls)
			assert.Equal(t, agentlsp.References{}, refs)
			assert.Equal(t, tt.want, problem(tt.at, declsErr))
			assert.Equal(t, tt.want, problem(tt.at, refsErr))
		})
	}
}
