package php_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReopenReusesTheIndex(t *testing.T) {
	cache(t)
	root := newRoot(t, "methods")
	call := place{"src/Shop/Checkout.php", "add('apple')"}

	first, firstLog := open(t, root)
	wantDefinition, wantUses := definition(t, first, root, call), uses(t, first, root, call)
	second, secondLog := open(t, root)
	gotDefinition, gotUses := definition(t, second, root, call), uses(t, second, root, call)

	assert.Equal(t, wantDefinition, gotDefinition)
	assert.Equal(t, wantUses, gotUses)
	assert.NotEmpty(t, gotUses)
	assert.Contains(t, firstLog, indexed(4, 4))
	assert.Contains(t, secondLog, indexed(4, 0))
	assert.Regexp(t, `php: stubs: [1-9]\d* files, [1-9]\d* parsed`, firstLog)
	assert.Regexp(t, `php: stubs: [1-9]\d* files, 0 parsed`, secondLog)
}

// A second root with the same files, as a second worktree of a project, parses none of them and answers with its
// own paths.
func TestSecondRootReusesParsedFiles(t *testing.T) {
	cache(t)
	first, second := newRoot(t, "methods"), newRoot(t, "methods")
	open(t, first)
	ix, log := open(t, second)

	assert.ElementsMatch(t, locations(t, second, cartAddCalls), uses(t, ix, second, cartAdd))
	assert.Contains(t, log, indexed(4, 0))
}

// A root holds composer.json or, as the top of a git checkout or worktree, .git; Open does not look above it.
func TestOpenNeedsComposerJSONOrGit(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T, root string)
		want    string // the error after the root; "" for none
	}{
		{"composer.json", func(*testing.T, string) {}, ""},
		{".git directory", func(t *testing.T, root string) {
			require.NoError(t, os.Remove(filepath.Join(root, "composer.json")))
			require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
		}, ""},
		{".git file of a worktree", func(t *testing.T, root string) {
			require.NoError(t, os.Remove(filepath.Join(root, "composer.json")))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere\n"), 0o600))
		}, ""},
		{"neither", func(t *testing.T, root string) {
			require.NoError(t, os.Remove(filepath.Join(root, "composer.json")))
		}, "no composer.json or .git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache(t)
			root := newRoot(t, "methods")
			tt.arrange(t, root)

			_, _, err := tryOpen(t, root)

			assert.Equal(t, tt.want, after(err, "php: "+root+": "))
		})
	}
}

// Close may be called again; the second call does nothing.
func TestCloseTwice(t *testing.T) {
	cache(t)
	ix, _ := open(t, newRoot(t, "methods"))
	require.NoError(t, ix.Close())

	assert.NoError(t, ix.Close())
}

func TestReopenAfterAChangeAnswersFromTheDisk(t *testing.T) {
	tests := []struct {
		name   string
		change func(t *testing.T, root string)
		want   []place
		log    string
	}{
		{"file changed", appendToApp, append([]place{{"src/app.php", "add('fresh')"}}, cartAddCalls...), indexed(4, 1)},
		{"file added", addShop, append([]place{{"src/shop.php", "add('added')"}}, cartAddCalls...), indexed(5, 1)},
		{"file removed", removeCheckout, appCartAddCalls, indexed(3, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache(t)
			root := newRoot(t, "methods")
			first, _ := open(t, root)
			uses(t, first, root, cartAdd)

			tt.change(t, root)
			second, log := open(t, root)

			assert.ElementsMatch(t, locations(t, root, tt.want), uses(t, second, root, cartAdd))
			assert.Contains(t, log, tt.log)
		})
	}
}

// The user's cache directory is a regular file: no index can be opened under it.
func TestOpenFailsWithoutACacheDirectory(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	require.NoError(t, os.WriteFile(home, nil, 0o600))
	cacheAt(t, home)

	_, _, err := tryOpen(t, newRoot(t, "methods"))

	assert.Equal(t, &fs.PathError{Op: "mkdir", Path: home, Err: syscall.ENOTDIR}, err)
}

// The path of the index file goes into the SQLite DSN: a space, % and ? in the cache directory and the root are
// parts of the name. Were ? read as the start of the query, every root would get the one file before it, and the
// index of root would be built again after other. Windows does not allow ? in a name.
func TestOpenUnderPathsWithSpecialCharacters(t *testing.T) {
	name := "a %41 ?b"
	if runtime.GOOS == "windows" {
		name = "a %41 b"
	}
	cacheAt(t, filepath.Join(t.TempDir(), name))
	root, other := filepath.Join(t.TempDir(), name), filepath.Join(t.TempDir(), name)
	require.NoError(t, os.CopyFS(root, os.DirFS(filepath.Join("testdata", "methods"))))
	require.NoError(t, os.CopyFS(other, os.DirFS(filepath.Join("testdata", "syntax"))))
	open(t, root)
	open(t, other)
	ix, log := open(t, root)

	assert.ElementsMatch(t, locations(t, root, cartAddCalls), uses(t, ix, root, cartAdd))
	assert.Contains(t, log, indexed(4, 0))
}

func appendToApp(t *testing.T, root string) {
	t.Helper()
	app := filepath.Join(root, "src", "app.php")
	src, err := os.ReadFile(app)
	require.NoError(t, err)
	fresh := "\nfunction fresh(): void\n{\n    $basket = new Cart();\n    $basket->add('fresh');\n}\n"
	require.NoError(t, os.WriteFile(app, append(src, fresh...), 0o600))
}

func addShop(t *testing.T, root string) {
	t.Helper()
	src := "<?php\n\nfunction shop(): void\n{\n    $cart = new \\Shop\\Cart();\n    $cart->add('added');\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "shop.php"), []byte(src), 0o600))
}

func removeCheckout(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, os.Remove(filepath.Join(root, "src", "Shop", "Checkout.php")))
}
