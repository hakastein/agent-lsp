package php_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const linkedCall = "<?php\n\nfunction linked(): void\n{\n" +
	"    $linked = new \\Shop\\Cart();\n    $linked->add('linked');\n}\n"

func TestSymlinksAreFollowed(t *testing.T) {
	cache(t)
	root, outside := workspace(t, "methods"), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "linked.php"), []byte(linkedCall), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(outside, "lib"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "lib", "deep.php"), []byte(linkedCall), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(outside, "linked.php"), filepath.Join(root, "src", "file.php")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "lib"), filepath.Join(root, "src", "lib")))
	ix, _ := open(t, root)

	want := append([]place{{"src/file.php", "add('linked')"}, {"src/lib/deep.php", "add('linked')"}}, cartAddCalls...)
	assert.ElementsMatch(t, locations(t, root, want), uses(t, ix, root, cartAdd))
}

// src/loop/ is the root again; walking into it would never end.
func TestSymlinkCycleIsWalkedOnce(t *testing.T) {
	cache(t)
	root := workspace(t, "methods")
	require.NoError(t, os.Symlink(root, filepath.Join(root, "src", "loop")))
	ix, _ := open(t, root)

	assert.ElementsMatch(t, locations(t, root, cartAddCalls), uses(t, ix, root, cartAdd))
}

// A broken symlink without the .php suffix may have pointed at a directory or at anything else; it is skipped. It
// is broken when its target is missing or its chain of symlinks never ends.
func TestBrokenSymlinkWithAnotherNameIsSkipped(t *testing.T) {
	tests := []struct {
		name   string
		target func(t *testing.T, link string) string
	}{
		{"missing target", func(t *testing.T, _ string) string { return filepath.Join(t.TempDir(), "gone") }},
		{"chain that never ends", func(_ *testing.T, link string) string { return link }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache(t)
			root := workspace(t, "methods")
			link := filepath.Join(root, "src", "gone")
			require.NoError(t, os.Symlink(tt.target(t, link), link))
			ix, log := open(t, root)

			assert.ElementsMatch(t, locations(t, root, cartAddCalls), uses(t, ix, root, cartAdd))
			assert.Contains(t, log, "php: skip "+link)
		})
	}
}
