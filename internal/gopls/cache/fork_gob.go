// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file is part of the agent-lsp fork (see ../FORK.md): the serialization types of
// diagnostics, copied verbatim from upstream cache/analysis.go, which the fork drops.

package cache

import (
	"encoding/json"

	"github.com/hakastein/agent-lsp/internal/gopls/protocol"
	"github.com/hakastein/agent-lsp/internal/gopls/util/frob"
)

// -- data types for serialization of analysis.Diagnostic and golang.Diagnostic --

// (The name says gob but we use frob.)
var diagnosticsCodec = frob.CodecFor[[]gobDiagnostic]()

type gobDiagnostic struct {
	Location       protocol.Location
	Severity       protocol.DiagnosticSeverity
	Code           string
	CodeHref       string
	Source         string
	Message        string
	SuggestedFixes []gobSuggestedFix
	Related        []gobRelatedInformation
	Tags           []protocol.DiagnosticTag
}

type gobRelatedInformation struct {
	Location protocol.Location
	Message  string
}

type gobSuggestedFix struct {
	Message    string
	TextEdits  []gobTextEdit
	Command    *gobCommand
	ActionKind protocol.CodeActionKind
}

type gobCommand struct {
	Title     string
	Command   string
	Arguments []json.RawMessage
}

type gobTextEdit struct {
	Location protocol.Location
	NewText  []byte
}
