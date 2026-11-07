// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file is part of the agent-lsp fork (see ../FORK.md): helpers that the kept files of this
// package use, copied verbatim from upstream files the fork drops (the file of each is named
// above it).

package golang

import (
	"context"
	pathpkg "path"
	"regexp"
	"slices"
	"strings"

	"github.com/hakastein/agent-lsp/internal/gopls/cache"
	"github.com/hakastein/agent-lsp/internal/gopls/cache/parsego"
	"github.com/hakastein/agent-lsp/internal/gopls/file"
	"github.com/hakastein/agent-lsp/internal/gopls/protocol"
	"github.com/hakastein/agent-lsp/internal/gopls/xtools/astutil"
)

// rename.go

func parsePackageNameDecl(ctx context.Context, snapshot *cache.Snapshot, fh file.Handle, rng protocol.Range) (*parsego.File, bool, error) {
	pgf, err := snapshot.ParseGo(ctx, fh, parsego.Header)
	if err != nil {
		return nil, false, err
	}
	// Careful: because we used parsego.Header,
	// pgf.Pos(ppos) may be beyond EOF => (0, err).
	start, end, _ := pgf.RangePos(rng)
	return pgf, astutil.NodeContains(pgf.File.Name, astutil.RangeOf(start, end)), nil
}

// semtok.go

var docLinkRegex = regexp.MustCompile(`\[\*?([\pL_][\pL_0-9]*(\.[\pL_][\pL_0-9]*){0,2})]`)

// findDocLinkIndices finds the indices of the doc links in the line.
// It will ignore the url link, e.g. "[go]: https://go.dev".
// For a invalid url link, it would be considered as a doc link, e.g. "[go]: h://go.dev".
func findDocLinkIndices(line string) [][]int {
	indices := docLinkRegex.FindAllStringSubmatchIndex(line, -1)

	ret := slices.DeleteFunc(
		indices,
		func(index []int) bool {
			// end is the end index of the first submatch, which is the next pos of ']'
			end := index[1]

			if end < len(line) && line[end] == ':' {
				url := strings.TrimSpace(line[end+1:])
				before, _, found := strings.Cut(url, "://")
				if found && isScheme(before) {
					// Valid URL, skip this match
					return true
				}
			}

			return false
		},
	)

	return ret
}

func isScheme(s string) bool {
	switch s {
	case "file",
		"ftp",
		"gopher",
		"http",
		"https",
		"mailto",
		"nntp":
		return true
	}
	return false
}

func is[T any](x any) bool {
	_, ok := x.(T)
	return ok
}

// stub.go

func trimVersionSuffix(path string) string {
	dir, base := pathpkg.Split(path)
	if len(base) > 1 && base[0] == 'v' && strings.Trim(base[1:], "0123456789") == "" {
		return dir // sans "/v2"
	}
	return path
}

// extract.go

func cond[T any](cond bool, t, f T) T {
	if cond {
		return t
	} else {
		return f
	}
}
