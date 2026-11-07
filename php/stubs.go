package php

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hakastein/agent-lsp/internal/cachedb"
)

// stubsFS is the phpstorm-stubs; stubs/STUBS.md names their version. Some stubs start with _, which go:embed leaves
// out without all:.
//
//go:embed all:stubs
var stubsFS embed.FS

// stubSet is the .php files of the stubs, relative to stubs/, and the name of their content.
type stubSet struct {
	paths  []string
	digest string
}

// embeddedStubs lists the stubs and names them by the hash of their paths and contents: other stubs are another
// root in the cache and another directory on disk.
var embeddedStubs = sync.OnceValues(func() (stubSet, error) {
	var s stubSet
	h := sha256.New()
	err := fs.WalkDir(stubsFS, "stubs", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".php") {
			return err
		}
		src, err := stubsFS.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, "stubs/")
		fmt.Fprintf(h, "%s\x00%d\x00", rel, len(src))
		h.Write(src)
		s.paths = append(s.paths, rel)
		return nil
	})
	s.digest = hex.EncodeToString(h.Sum(nil)[:8])
	return s, err
})

// stat is the size of a stub; the stubs have no modification time, their digest names their content.
func (s stubSet) stat(p string) (int64, int64, error) {
	info, err := fs.Stat(stubsFS, "stubs/"+p)
	if err != nil {
		return 0, 0, err
	}
	return info.Size(), 0, nil
}

func (s stubSet) read(p string) ([]byte, bool, error) {
	src, err := stubsFS.ReadFile("stubs/" + p)
	return src, err == nil, err
}

// stubFile is the path of the stub p on disk, <cache dir>/agent-lsp/php-stubs/<digest>/<p>, so that a declaration of
// the standard library is in a file a person or a program can open. The file is written when it is missing.
//
// shortcut: directories of earlier stubs stay on disk; remove them by hand after an update of the stubs.
func stubFile(digest, p string) (string, error) {
	db, err := cachedb.Path()
	if err != nil {
		return "", err
	}
	file := filepath.Join(filepath.Dir(db), "php-stubs", digest, filepath.FromSlash(p))
	if _, err := os.Stat(file); !errors.Is(err, fs.ErrNotExist) {
		return file, err
	}
	src, err := stubsFS.ReadFile("stubs/" + p)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", err
	}
	// A temporary file renamed into place: a process that reads the file never sees a part of it.
	tmp, err := os.CreateTemp(filepath.Dir(file), ".stub-*")
	if err != nil {
		return "", err
	}
	_, err = tmp.Write(src)
	if err = errors.Join(err, tmp.Close()); err == nil {
		err = os.Rename(tmp.Name(), file)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return file, nil
}
