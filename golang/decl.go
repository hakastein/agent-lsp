package golang

import (
	"bytes"
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/ast/astutil"

	agentlsp "github.com/hakastein/agent-lsp"
)

// source is a parsed file and its text.
type source struct {
	path string
	src  []byte
	fset *token.FileSet
	file *ast.File
}

// location is the range of [pos, end) in the file.
func (s source) location(pos, end token.Pos) agentlsp.Location {
	p, e := s.fset.Position(pos), s.fset.Position(end)
	return agentlsp.Location{File: s.path, Line: p.Line, Column: p.Column, EndLine: e.Line, EndColumn: e.Column}
}

// text is the source text of [pos, end).
func (s source) text(pos, end token.Pos) string {
	return string(s.src[s.fset.Position(pos).Offset:s.fset.Position(end).Offset])
}

// firstLine is the first line of the source text of node, without trailing white space.
func (s source) firstLine(node ast.Node) string {
	text, _, _ := strings.Cut(s.text(node.Pos(), node.End()), "\n")
	return strings.TrimRight(text, " \t\r")
}

// declaration is the declaration of node, named by name, with the doc comment doc.
func (s source) declaration(name *ast.Ident, node ast.Node, signature string, doc *ast.CommentGroup) agentlsp.Declaration {
	d := agentlsp.Declaration{
		Location:  s.location(name.Pos(), name.End()),
		Range:     s.location(node.Pos(), node.End()),
		Signature: signature,
	}
	if doc != nil {
		d.Doc = s.text(doc.Pos(), doc.End())
	}
	return d
}

// symbols is every named declaration of the file in source order: functions, methods, types, the fields of struct
// types and the methods of interface types, variables and constants at the top level.
func (s source) symbols() []agentlsp.Symbol {
	var out []agentlsp.Symbol
	add := func(name *ast.Ident, kind agentlsp.Kind, container string, node ast.Node, signature string, doc *ast.CommentGroup) {
		if name.Name == "_" {
			return
		}
		out = append(out, agentlsp.Symbol{Name: name.Name, Kind: kind, Container: container,
			Declaration: s.declaration(name, node, signature, doc)})
	}
	// fields adds the fields of a struct type or the methods of an interface type named container, and those of the
	// struct and interface types nested in them.
	var fields func(typ ast.Expr, container string)
	fields = func(typ ast.Expr, container string) {
		var list *ast.FieldList
		kind := agentlsp.KindField
		switch t := typ.(type) {
		case *ast.StructType:
			list = t.Fields
		case *ast.InterfaceType:
			list, kind = t.Methods, agentlsp.KindMethod
		default:
			return
		}
		for _, f := range list.List {
			sig := s.firstLine(f)
			if len(f.Names) == 0 {
				if name := embedded(f.Type); name != nil && kind == agentlsp.KindField {
					add(name, kind, container, f, sig, f.Doc)
				}
				continue
			}
			for _, name := range f.Names {
				add(name, kind, container, f, sig, f.Doc)
				fields(f.Type, container+"."+name.Name)
			}
		}
	}
	for _, decl := range s.file.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			sig := s.funcSignature(decl)
			if decl.Recv.NumFields() == 0 {
				add(decl.Name, agentlsp.KindFunc, "", decl, sig, decl.Doc)
				continue
			}
			container := ""
			if recv := receiver(decl.Recv.List[0].Type); recv != nil {
				container = recv.Name
			}
			add(decl.Name, agentlsp.KindMethod, container, decl, sig, decl.Doc)
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				// A spec alone in a declaration without parentheses is the whole declaration with its doc.
				var node ast.Node = spec
				doc := specDoc(spec)
				if !decl.Lparen.IsValid() {
					node, doc = decl, decl.Doc
				}
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					add(spec.Name, agentlsp.KindType, "", node, s.typeSignature(spec), doc)
					fields(spec.Type, spec.Name.Name)
				case *ast.ValueSpec:
					kind := agentlsp.KindVar
					if decl.Tok == token.CONST {
						kind = agentlsp.KindConst
					}
					sig := decl.Tok.String() + " " + s.firstLine(spec)
					for _, name := range spec.Names {
						add(name, kind, "", node, sig, doc)
					}
				}
			}
		}
	}
	return out
}

// definition is the declaration whose name stands at pos: a symbol of the file or a local one, such as a parameter,
// a variable of a function body or a label. Its Origin is left to the caller.
func (s source) definition(pos token.Pos) (agentlsp.Declaration, bool) {
	for _, sym := range s.symbols() {
		if l := sym.Location; s.contains(l, pos) {
			return sym.Declaration, true
		}
	}
	path, _ := astutil.PathEnclosingInterval(s.file, pos, pos)
	if len(path) == 0 {
		return agentlsp.Declaration{}, false
	}
	name, ok := path[0].(*ast.Ident)
	if !ok {
		return agentlsp.Declaration{}, false
	}
	for _, node := range path[1:] {
		switch node := node.(type) {
		case *ast.Field, *ast.AssignStmt, *ast.DeclStmt, *ast.RangeStmt, *ast.LabeledStmt, *ast.ImportSpec,
			*ast.TypeSpec, *ast.ValueSpec, *ast.CaseClause, *ast.File:
			return s.declaration(name, node, s.firstLine(node), nil), true
		case *ast.TypeSwitchStmt:
			return s.declaration(name, node.Assign, s.firstLine(node.Assign), nil), true
		}
	}
	return agentlsp.Declaration{}, false
}

// contains reports whether the name at l covers pos.
func (s source) contains(l agentlsp.Location, pos token.Pos) bool {
	p := s.fset.Position(pos)
	return l.Line == p.Line && l.Column <= p.Column && p.Column <= l.EndColumn
}

// funcSignature is the text of a function declaration without its body.
func (s source) funcSignature(decl *ast.FuncDecl) string {
	if decl.Body == nil {
		return s.text(decl.Pos(), decl.End())
	}
	return string(bytes.TrimRight([]byte(s.text(decl.Pos(), decl.Body.Lbrace)), " \t\r\n"))
}

// typeSignature is "type T struct" or "type T interface" for a struct or interface type, with the type parameters,
// and the whole spec after "type" for other types.
func (s source) typeSignature(spec *ast.TypeSpec) string {
	end := spec.End()
	switch t := spec.Type.(type) {
	case *ast.StructType:
		end = t.Fields.Opening
	case *ast.InterfaceType:
		end = t.Methods.Opening
	}
	return "type " + strings.TrimRight(s.text(spec.Pos(), end), " \t")
}

// specDoc is the doc comment of a spec in parentheses.
func specDoc(spec ast.Spec) *ast.CommentGroup {
	switch spec := spec.(type) {
	case *ast.TypeSpec:
		return spec.Doc
	case *ast.ValueSpec:
		return spec.Doc
	}
	return nil
}

// receiver is the name of the type of a method receiver: T of T, *T, T[P] and *T[P].
func receiver(expr ast.Expr) *ast.Ident {
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		case *ast.Ident:
			return e
		default:
			return nil
		}
	}
}

// embedded is the name of an embedded field: T of T, *T, pkg.T, *pkg.T and their instances.
func embedded(expr ast.Expr) *ast.Ident {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	switch e := expr.(type) {
	case *ast.IndexExpr:
		expr = e.X
	case *ast.IndexListExpr:
		expr = e.X
	}
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return e.Sel
	case *ast.Ident:
		return e
	}
	return nil
}
