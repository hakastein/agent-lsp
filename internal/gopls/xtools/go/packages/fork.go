// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file is part of the agent-lsp fork (see ../../../FORK.md).

package packages

import "github.com/hakastein/agent-lsp/internal/gopls/xtools/packagesinternal"

// SetDepsErrors sets the errors of the dependencies of p, as the go command reported them; a
// package restored from a stored load gets them back this way.
func SetDepsErrors(p *Package, errs []*packagesinternal.PackageError) {
	p.depsErrors = errs
}
