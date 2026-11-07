//go:build unix

package php_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A named pipe and a symlink to one have .php names; reading either would wait for a writer forever.
func TestNonRegularFilesAreNotIndexed(t *testing.T) {
	cache(t)
	root, outside := newRoot(t, "methods"), t.TempDir()
	require.NoError(t, syscall.Mkfifo(filepath.Join(root, "src", "x.php"), 0o600))
	require.NoError(t, syscall.Mkfifo(filepath.Join(outside, "pipe.php"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(outside, "pipe.php"), filepath.Join(root, "src", "y.php")))
	ix, _ := open(t, root)

	assert.ElementsMatch(t, locations(t, root, cartAddCalls), uses(t, ix, root, cartAdd))
}

// unreadable is an entry under the root that holds a call to Shop\Cart::add and that the test makes unreadable and
// readable again.
type unreadable struct {
	name        string
	permissions bool // made unreadable by its mode bits, which do not stop a process running as root
	arrange     func(t *testing.T, root string) (spoil, mend func())
	path        string // the entry the index cannot read, relative to the root
	call        place
	osError     string
}

var unreadableEntries = []unreadable{
	{"unreadable .php file", true, unreadableFile, "src/file.php", place{"src/file.php", "add('linked')"},
		"permission denied"},
	{"unreadable directory", true, unreadableDirectory, "src/lib", place{"src/lib/deep.php", "add('linked')"},
		"permission denied"},
	{"unreadable symlinked directory", true, unreadableLinkedDirectory, "src/lib",
		place{"src/lib/deep.php", "add('linked')"}, "permission denied"},
	{"broken .php symlink", false, brokenPHPLink, "src/broken.php", place{"src/broken.php", "add('linked')"},
		"no such file or directory"},
	{"symlink through a regular file", false, linkThroughFile, "src/lib", place{"src/lib/deep.php", "add('linked')"},
		"not a directory"},
	{"symlink through a locked directory", true, linkThroughLockedDirectory, "src/lib",
		place{"src/lib/deep.php", "add('linked')"}, "permission denied"},
}

// An entry the index cannot read fails the index: an answer without its calls would look complete.
func TestUnreadableEntryFailsTheIndex(t *testing.T) {
	for _, tt := range unreadableEntries {
		t.Run(tt.name, func(t *testing.T) {
			skipAsRoot(t, tt.permissions)
			cache(t)
			root := newRoot(t, "methods")
			spoil, mend := tt.arrange(t, root)
			want := locations(t, root, append([]place{tt.call}, cartAddCalls...))
			spoil()

			_, failingLog, err := tryOpen(t, root)
			mend()
			mended, mendedLog := open(t, root)

			assertIndexFailed(t, err, root, tt.path, tt.osError)
			assert.NotContains(t, failingLog, rootLine)
			assert.ElementsMatch(t, want, uses(t, mended, root, cartAdd))
			assert.Contains(t, mendedLog, indexed(5, 5))
		})
	}
}

// A ready index is not reused when an entry cannot be read: without the entry the map of the root cannot be
// compared. The ready index is built before the entry exists, so skipping the entry would make the map match.
func TestUnreadableEntryFailsAReadyIndex(t *testing.T) {
	for _, tt := range unreadableEntries {
		t.Run(tt.name, func(t *testing.T) {
			skipAsRoot(t, tt.permissions)
			cache(t)
			root := newRoot(t, "methods")
			first, _ := open(t, root)
			before := uses(t, first, root, cartAdd)
			spoil, mend := tt.arrange(t, root)
			want := locations(t, root, append([]place{tt.call}, cartAddCalls...))
			spoil()
			_, failingLog, err := tryOpen(t, root)
			mend()
			mended, _ := open(t, root)

			assert.ElementsMatch(t, locations(t, root, cartAddCalls), before)
			assertIndexFailed(t, err, root, tt.path, tt.osError)
			assert.NotContains(t, failingLog, rootLine)
			assert.ElementsMatch(t, want, uses(t, mended, root, cartAdd))
		})
	}
}

func skipAsRoot(t *testing.T, permissions bool) {
	t.Helper()
	if permissions && os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not stop root from reading, so the entry cannot be made unreadable")
	}
}

func assertIndexFailed(t *testing.T, err error, root, path, osError string) {
	t.Helper()
	require.Error(t, err, "opened an index that misses %s", path)
	assert.Contains(t, err.Error(), filepath.Join(root, filepath.FromSlash(path))+": "+osError)
}

// chmod sets the mode of path and puts it back to restore when the test ends, so the test can remove its files.
func chmod(t *testing.T, path string, mode, restore os.FileMode) (spoil, mend func()) {
	t.Helper()
	mend = func() { require.NoError(t, os.Chmod(path, restore)) }
	t.Cleanup(mend)
	return func() { require.NoError(t, os.Chmod(path, mode)) }, mend
}

func unreadableFile(t *testing.T, root string) (spoil, mend func()) {
	t.Helper()
	file := filepath.Join(root, "src", "file.php")
	require.NoError(t, os.WriteFile(file, []byte(linkedCall), 0o600))
	return chmod(t, file, 0, 0o600)
}

func unreadableDirectory(t *testing.T, root string) (spoil, mend func()) {
	t.Helper()
	lib := filepath.Join(root, "src", "lib")
	require.NoError(t, os.Mkdir(lib, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(lib, "deep.php"), []byte(linkedCall), 0o600))
	return chmod(t, lib, 0, 0o755)
}

func unreadableLinkedDirectory(t *testing.T, root string) (spoil, mend func()) {
	t.Helper()
	lib := filepath.Join(t.TempDir(), "lib")
	require.NoError(t, os.Mkdir(lib, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(lib, "deep.php"), []byte(linkedCall), 0o600))
	require.NoError(t, os.Symlink(lib, filepath.Join(root, "src", "lib")))
	return chmod(t, lib, 0, 0o755)
}

func brokenPHPLink(t *testing.T, root string) (spoil, mend func()) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "linked.php")
	write := func() { require.NoError(t, os.WriteFile(target, []byte(linkedCall), 0o600)) }
	write()
	require.NoError(t, os.Symlink(target, filepath.Join(root, "src", "broken.php")))
	return func() { require.NoError(t, os.Remove(target)) }, write
}

// linkThroughFile makes src/lib a symlink to afile/inner; spoiled, afile is a regular file, so the target is
// neither there nor missing.
func linkThroughFile(t *testing.T, root string) (spoil, mend func()) {
	t.Helper()
	afile := filepath.Join(t.TempDir(), "afile")
	mend = func() {
		require.NoError(t, os.RemoveAll(afile))
		require.NoError(t, os.MkdirAll(filepath.Join(afile, "inner"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(afile, "inner", "deep.php"), []byte(linkedCall), 0o600))
	}
	mend()
	require.NoError(t, os.Symlink(filepath.Join(afile, "inner"), filepath.Join(root, "src", "lib")))
	return func() {
		require.NoError(t, os.RemoveAll(afile))
		require.NoError(t, os.WriteFile(afile, nil, 0o600))
	}, mend
}

// linkThroughLockedDirectory makes src/lib a symlink to locked/inner; spoiled, locked has mode 000, so the target
// cannot be stat-ed.
func linkThroughLockedDirectory(t *testing.T, root string) (spoil, mend func()) {
	t.Helper()
	locked := filepath.Join(t.TempDir(), "locked")
	require.NoError(t, os.MkdirAll(filepath.Join(locked, "inner"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(locked, "inner", "deep.php"), []byte(linkedCall), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(locked, "inner"), filepath.Join(root, "src", "lib")))
	return chmod(t, locked, 0, 0o755)
}
