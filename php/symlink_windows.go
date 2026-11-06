package php

import "golang.org/x/sys/windows"

// errSymlinkLoop is the error of following a chain of symlinks that does not end; Windows names it so.
var errSymlinkLoop error = windows.ERROR_CANT_RESOLVE_FILENAME
