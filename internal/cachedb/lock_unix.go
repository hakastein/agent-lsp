//go:build unix

package cachedb

import (
	"os"
	"syscall"
)

// lockFile waits for the exclusive lock of f, which other processes take through files of their own; closing f
// releases it.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}
