package facts

import (
	"bytes"
	"strings"

	"github.com/VKCOM/php-parser/pkg/ast"
	"github.com/VKCOM/php-parser/pkg/token"

	agentlsp "github.com/hakastein/agent-lsp"
)

// What a name refers to, spelled as the alias types of nsresolver: a class-like, a function or a constant.
const (
	classRef    = ""
	functionRef = "function"
	constRef    = "const"
)

// reserved are the unqualified names that name no declaration: the types and self, static and parent where a class
// is named, true, false and null where a constant is.
var reserved = map[string]map[string]bool{
	classRef: {"self": true, "static": true, "parent": true, "int": true, "float": true, "bool": true,
		"string": true, "void": true, "iterable": true, "object": true, "mixed": true, "never": true, "null": true,
		"false": true, "true": true, "array": true, "callable": true},
	constRef:    {"true": true, "false": true, "null": true},
	functionRef: {},
}

// The keys of declared names. PHP compares names of classes, functions and methods case-insensitively; a constant
// by its namespace case-insensitively and by its name case-sensitively. Each key starts with a letter of its kind
// and a colon; Qualified drops them.

func classKey(fqn string) string    { return "c:" + strings.ToLower(fqn) }
func functionKey(fqn string) string { return "f:" + strings.ToLower(fqn) }

func constKey(fqn string) string {
	i := strings.LastIndex(fqn, `\`)
	return "k:" + strings.ToLower(fqn[:i+1]) + fqn[i+1:]
}

func methodKey(class, method string) string {
	return "m:" + strings.ToLower(class) + "::" + strings.ToLower(method)
}

func classConstKey(class, name string) string { return "q:" + strings.ToLower(class) + "::" + name }

// Qualified is the qualified name of a key: `Ns\Class`, `Ns\Class::method`, `Ns\func`, `Ns\CONST` or
// `Ns\Class::CONST`, in lower case but for the names of constants.
func Qualified(key string) string { return key[2:] }

func keyOf(kind, fqn string) string {
	switch kind {
	case functionRef:
		return functionKey(fqn)
	case constRef:
		return constKey(fqn)
	}
	return classKey(fqn)
}

// qualify is name in the namespace in effect.
func (c *collector) qualify(name string) string {
	if c.ns.Namespace == "" {
		return name
	}
	return c.ns.Namespace + `\` + name
}

// declaration is the Decl of node named by the identifier name, without Key: its signature ends at the byte offset
// body, or is the first line of node when body is -1.
func (c *collector) declaration(kind agentlsp.Kind, name, node ast.Vertex, body int) (Decl, bool) {
	id, ok := name.(*ast.Identifier)
	p := node.GetPosition()
	if !ok || p == nil || id.Position == nil {
		return Decl{}, false
	}
	return Decl{Kind: kind, Name: string(id.Value), NameRange: c.lines.span(id.Position), Range: c.lines.span(p),
		Signature: c.signature(p.StartPos, p.EndPos, body), Doc: c.doc(p.StartPos)}, true
}

// startOf is the byte offset of the token t, -1 for none.
func startOf(t *token.Token) int {
	if t == nil || t.Position == nil {
		return -1
	}
	return t.Position.StartPos
}

// member records a method or a class constant; its key and container come from its class after the traversal.
func (c *collector) member(kind agentlsp.Kind, name, node ast.Vertex, body int) {
	if d, ok := c.declaration(kind, name, node, body); ok {
		c.members = append(c.members, member{at: node.GetPosition().StartPos, decl: d})
	}
}

// signature is the text of [start, body) without trailing white space, or of the first line of [start, end) when
// body is -1.
func (c *collector) signature(start, end, body int) string {
	text := c.lines.src[start:end]
	if body >= 0 {
		text = c.lines.src[start:body]
	} else {
		text, _, _ = bytes.Cut(text, []byte("\n"))
	}
	return string(bytes.TrimRight(text, " \t\r\n"))
}

// doc is the doc comment `/** … */` that ends right before start, with white space and at most one line break
// between them; "" for none.
func (c *collector) doc(start int) string {
	src := c.lines.src
	before := bytes.TrimRight(src[:start], " \t\r\n")
	if !bytes.HasSuffix(before, []byte("*/")) || bytes.Count(src[len(before):start], []byte("\n")) > 1 {
		return ""
	}
	open := bytes.LastIndex(before, []byte("/**"))
	if open < 0 || len(before)-open < 5 || bytes.Contains(before[open+3:len(before)-2], []byte("*/")) {
		return ""
	}
	return string(before[open:])
}

func (c *collector) StmtConstList(n *ast.StmtConstList) {
	for _, k := range n.Consts {
		if k, ok := k.(*ast.StmtConstant); ok {
			if d, ok := c.declaration(agentlsp.KindConst, k.Name, n, -1); ok {
				d.Key = constKey(c.qualify(d.Name))
				c.decls = append(c.decls, d)
			}
		}
	}
}

func (c *collector) StmtClassConstList(n *ast.StmtClassConstList) {
	for _, k := range n.Consts {
		if k, ok := k.(*ast.StmtConstant); ok {
			c.member(agentlsp.KindClassConst, k.Name, n, -1)
		}
	}
}

func (c *collector) EnumCase(n *ast.EnumCase) { c.member(agentlsp.KindClassConst, n.Name, n, -1) }

// define declares the constant its first argument names when that is a string literal.
func (c *collector) define(n *ast.ExprFunctionCall) {
	if len(n.Args) == 0 || n.Position == nil {
		return
	}
	arg, ok := n.Args[0].(*ast.Argument)
	if !ok {
		return
	}
	s, ok := arg.Expr.(*ast.ScalarString)
	if !ok || s.Position == nil {
		return
	}
	text := string(c.lines.src[s.Position.StartPos:s.Position.EndPos])
	open := strings.IndexAny(text, `'"`)
	if open < 0 || len(text)-open < 3 {
		return
	}
	fqn := strings.TrimPrefix(strings.ReplaceAll(text[open+1:len(text)-1], `\\`, `\`), `\`)
	p := n.Position
	c.decls = append(c.decls, Decl{
		Key: constKey(fqn), Kind: agentlsp.KindConst, Name: fqn[strings.LastIndex(fqn, `\`)+1:],
		NameRange: Range{Start: c.lines.at(s.Position.StartPos + open + 1), End: c.lines.at(s.Position.EndPos - 1)},
		Range:     c.lines.span(p), Signature: c.signature(p.StartPos, p.EndPos, -1), Doc: c.doc(p.StartPos),
	})
}

// The use statements come before the code they cover, which the traversal visits after them.
func (c *collector) StmtUse(n *ast.StmtUseList) { c.use(n.Type, nil, n.Uses) }

func (c *collector) StmtGroupUse(n *ast.StmtGroupUseList) {
	prefix, ok := n.Prefix.(*ast.Name)
	if !ok {
		return
	}
	c.skip[prefix] = true
	c.use(n.Type, prefix.Parts, n.Uses)
}

// use adds the aliases of a use statement and records each imported name as a link.
func (c *collector) use(typ ast.Vertex, prefix []ast.Vertex, uses []ast.Vertex) {
	for _, u := range uses {
		use, ok := u.(*ast.StmtUse)
		if !ok {
			continue
		}
		name, ok := use.Use.(*ast.Name)
		if !ok || len(name.Parts) == 0 {
			continue
		}
		c.skip[name] = true
		kind := useKind(typ)
		if use.Type != nil {
			kind = useKind(use.Type)
		}
		fqn := joinParts(append(prefix[:len(prefix):len(prefix)], name.Parts...))
		alias := fqn[strings.LastIndex(fqn, `\`)+1:]
		if id, ok := use.Alias.(*ast.Identifier); ok {
			alias = string(id.Value)
		}
		c.ns.AddAlias(kind, fqn, alias)
		if name.Position != nil {
			c.links = append(c.links, Link{Key: keyOf(kind, fqn), Name: c.lines.span(name.Position)})
		}
	}
}

// useKind is the kind of names a use statement of type typ imports: `use function`, `use const` or classes.
func useKind(typ ast.Vertex) string {
	if id, ok := typ.(*ast.Identifier); ok {
		return strings.ToLower(string(id.Value))
	}
	return classRef
}

func (c *collector) NameName(n *ast.Name)                         { c.name(n, n.Parts) }
func (c *collector) NameFullyQualified(n *ast.NameFullyQualified) { c.name(n, n.Parts) }
func (c *collector) NameRelative(n *ast.NameRelative)             { c.name(n, n.Parts) }

// name records a name in the code as a link to a class, or to a function or a constant where a call or a
// constant fetch has marked it so. An unqualified name of a function or a constant that no use statement imports
// falls back to the global name.
func (c *collector) name(n ast.Vertex, parts []ast.Vertex) {
	p := n.GetPosition()
	if c.skip[n] || p == nil || len(parts) == 0 {
		return
	}
	kind := c.kinds[n]
	_, plain := n.(*ast.Name)
	single := ""
	if part, ok := parts[0].(*ast.NamePart); ok && plain && len(parts) == 1 {
		single = string(part.Value)
	}
	if reserved[kind][strings.ToLower(single)] {
		return
	}
	fqn, err := c.ns.ResolveName(n, kind)
	if err != nil {
		return
	}
	link := Link{Key: keyOf(kind, fqn), Name: c.lines.span(p)}
	if single != "" && kind != classRef && c.ns.Namespace != "" {
		if _, err := c.ns.ResolveAlias(n, kind); err != nil {
			link.Fallback = keyOf(kind, single)
		}
	}
	c.links = append(c.links, link)
}
