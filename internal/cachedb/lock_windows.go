package cachedb

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile waits for the exclusive lock of f, which other processes take through files of their own; closing f
// releases it.
func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, new(windows.Overlapped))
}
