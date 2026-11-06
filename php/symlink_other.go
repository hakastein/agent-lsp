//go:build !windows

package php

import "syscall"

// errSymlinkLoop is the error of following a chain of symlinks that does not end.
var errSymlinkLoop error = syscall.ELOOP
