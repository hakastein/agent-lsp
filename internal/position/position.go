// Package position checks that a position of a question is a place in a file of a root, the same way for every
// language package.
package position

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	agentlsp "github.com/hakastein/agent-lsp"
)

// ErrNotRegular is the error of reading a name that is not a regular file.
var ErrNotRegular = errors.New("not a regular file")

// ReadFile reads a regular file. The file is opened without blocking and checked after opening, so a name that
// has become a named pipe, a socket or a device by then is not read: the error is ErrNotRegular.
func ReadFile(name string) ([]byte, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", name, ErrNotRegular)
	}
	return io.ReadAll(f)
}

// Check reads the file of pos, which must be a place in a file under root, and returns its content; otherwise the
// error names the problem: a relative path, a file outside the root or one that does not exist, a line or a column
// outside the file. The line break of a line, \n or \r\n, is not a column of it; the column right after its last
// byte is.
func Check(root string, pos agentlsp.Position) ([]byte, error) {
	if !filepath.IsAbs(pos.File) {
		return nil, errors.New("the path is not absolute")
	}
	rel, err := filepath.Rel(root, pos.File)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, errors.New("the file is not under the root")
	}
	src, err := ReadFile(pos.File)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("the file does not exist")
	}
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(src, []byte("\n"))
	if pos.Line < 1 || pos.Line > len(lines) {
		return nil, fmt.Errorf("no line %d: the file has lines 1 to %d", pos.Line, len(lines))
	}
	end := len(bytes.TrimSuffix(lines[pos.Line-1], []byte("\r"))) + 1
	if pos.Column < 1 || pos.Column > end {
		return nil, fmt.Errorf("no column %d: line %d has columns 1 to %d", pos.Column, pos.Line, end)
	}
	return src, nil
}

// Offset is the byte offset of pos in src, a position Check accepted.
func Offset(src []byte, pos agentlsp.Position) int {
	offset := 0
	for range pos.Line - 1 {
		offset += bytes.IndexByte(src[offset:], '\n') + 1
	}
	return offset + pos.Column - 1
}
