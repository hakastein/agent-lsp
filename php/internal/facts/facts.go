// Package facts reads one PHP file into the facts the index stores: its declarations (classes, interfaces, traits,
// enums, functions, constants, methods and class constants) and its links (names of classes, functions and
// constants resolved by the namespace and the use statements, and method calls whose receiver class is known). The
// facts depend on the content of the file only. The rules are those of docs/adr/0001-workspace-index-on-disk.md; for
// method calls, in short:
// `(new X)->m()` anywhere; `$x->m()` only in a function, method or closure body where every write to `$x` is
// `new X` of one class, the first of them before the call, and nothing else can bind `$x`; a variable an arrow
// function or a `use` capture takes is known when the chain of captures, followed outward, comes from such a body.
// A top-level variable and a superglobal are never known.
package facts

import (
	"slices"
	"strings"

	"github.com/VKCOM/php-parser/pkg/ast"
	"github.com/VKCOM/php-parser/pkg/conf"
	phperrors "github.com/VKCOM/php-parser/pkg/errors"
	"github.com/VKCOM/php-parser/pkg/parser"
	"github.com/VKCOM/php-parser/pkg/version"
	"github.com/VKCOM/php-parser/pkg/visitor"
	"github.com/VKCOM/php-parser/pkg/visitor/nsresolver"
	"github.com/VKCOM/php-parser/pkg/visitor/traverser"

	agentlsp "github.com/hakastein/agent-lsp"
)

// Decl is a declaration of the file. Container is the class of a method or a class constant without its namespace,
// "" for the others. Signature is the text up to the body of a class-like or a function, the whole text of a method
// without a body, the first line of the others. Doc is the doc comment that ends on the line before Range or on its
// line, "" for none.
type Decl struct {
	Key       string // the key of the declared name
	Kind      agentlsp.Kind
	Name      string
	Container string
	NameRange Range
	Range     Range
	Signature string
	Doc       string
}

// Link is a place that names a declaration by its key: a name of a class, a function or a constant, or a method call
// whose receiver class is known. An unqualified name of a function or a constant in a namespace has a Fallback: the
// global name PHP takes when the namespace does not declare Key. A link whose Key and Fallback are declared
// nowhere names nothing.
type Link struct {
	Key      string
	Fallback string
	Name     Range
}

// Facts is what one file contributes to the index.
type Facts struct {
	Decls []Decl
	Links []Link
}

// Parse reads src. A file with syntax errors yields the facts of the tree the parser recovered; the errors are
// returned beside them.
func Parse(src []byte) (Facts, []string) {
	var syntax []string
	root, err := parser.Parse(src, conf.Config{
		Version:          &version.Version{Major: 8, Minor: 1},
		ErrorHandlerFunc: func(e *phperrors.Error) { syntax = append(syntax, e.String()) },
	})
	if err != nil {
		return Facts{}, append(syntax, err.Error())
	}
	if root == nil {
		return Facts{}, syntax
	}

	c := &collector{lines: newLines(src), ns: nsresolver.NewNamespace(""), skip: map[ast.Vertex]bool{},
		kinds: map[ast.Vertex]string{}}
	traverser.NewTraverser(c).Traverse(root)
	return c.facts(), syntax
}

// collector gathers what the traversal meets, each thing with its byte offset in the file. The order of the visits
// does not matter: afterwards every event goes to the innermost scope whose byte range contains it and every method
// to the innermost class-like, so nesting is the nesting of the source.
type collector struct {
	visitor.Null
	lines lines
	ns    *nsresolver.Namespace // the namespace and the use statements in effect where the traversal is
	skip  map[ast.Vertex]bool   // names that name no declaration: namespace names, names of use statements
	kinds map[ast.Vertex]string // names of functions and constants; any other name is of a class

	classes    []classDecl
	decls      []Decl
	members    []member
	links      []Link
	scopes     []*scope
	events     []event
	direct     []Link // calls on `new X`: known without a scope
	noPosition bool   // a scope or an event has no position: no variable is known
}

type classDecl struct {
	start, end int
	name       string // "" for an anonymous class
}

// member is a method or a class constant at a byte offset: its class is the innermost class-like there.
type member struct {
	at   int
	decl Decl
}

// event is something at a byte offset that changes or reads what the enclosing scope knows of its variables.
type event struct {
	kind  eventKind
	at    int
	end   int    // write: the end of the write
	name  string // the variable
	class string // write: the class of `new class`; call: the method name
	call  Range  // call: the method name at the call site
}

type eventKind int

const (
	write   eventKind = iota // the variable gets `new class`
	spoil                    // the variable gets something else or may be rebound
	dynamic                  // any variable of the scope may be rebound
	call                     // a method call on the variable
)

func (c *collector) facts() Facts {
	f := Facts{Decls: c.decls, Links: append(c.links, c.direct...)}
	for _, m := range c.members {
		class := c.classAt(m.at)
		if class == "" {
			continue
		}
		d := m.decl
		d.Container = class[strings.LastIndex(class, `\`)+1:]
		if d.Kind == agentlsp.KindMethod {
			d.Key = methodKey(class, d.Name)
		} else {
			d.Key = classConstKey(class, d.Name)
		}
		f.Decls = append(f.Decls, d)
	}
	if !c.noPosition {
		f.Links = append(f.Links, c.variableCalls()...)
	}
	return f
}

// variableCalls places the events in their scopes and returns the calls on variables whose class is known.
func (c *collector) variableCalls() []Link {
	top := &scope{start: 0, end: len(c.lines.src) + 1, top: true, vars: map[string]*binding{}}
	scopes := append([]*scope{top}, c.scopes...)
	slices.SortStableFunc(scopes, func(a, b *scope) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return b.end - a.end
	})
	for i, s := range scopes[1:] {
		s.parent = innermost(scopes[:i+1], s.start)
		for _, u := range s.uses {
			if u.byRef {
				s.parent.spoil(u.name)
				s.spoil(u.name)
			} else {
				s.imports[u.name] = true
			}
		}
	}
	var calls []event
	for _, e := range c.events {
		s := innermost(scopes, e.at)
		switch e.kind {
		case write:
			s.assign(e.name, e.class, e.at, e.end)
		case spoil:
			s.spoil(e.name)
		case dynamic:
			s.dynamic = true
		case call:
			calls = append(calls, e)
		}
	}
	slices.SortFunc(calls, func(a, b event) int { return a.at - b.at })
	var out []Link
	for _, e := range calls {
		if class := innermost(scopes, e.at).classOf(e.name, e.at); class != "" {
			out = append(out, Link{Key: methodKey(class, e.class), Name: e.call})
		}
	}
	return out
}

// innermost is the scope of scopes, sorted by start, that most closely contains offset at; scopes[0] contains
// every offset.
func innermost(scopes []*scope, at int) *scope {
	for i := len(scopes) - 1; i > 0; i-- {
		if s := scopes[i]; s.start <= at && at < s.end {
			return s
		}
	}
	return scopes[0]
}

// classAt is the name of the innermost class-like that contains offset at; "" outside one or in an anonymous class.
func (c *collector) classAt(at int) string {
	best := -1
	for i, k := range c.classes {
		if k.start <= at && at < k.end && (best < 0 || k.start >= c.classes[best].start) {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return c.classes[best].name
}

// event records e at the start of n; a write ends where n ends.
func (c *collector) event(n ast.Vertex, e event) {
	p := n.GetPosition()
	if p == nil {
		c.noPosition = true
		return
	}
	e.at, e.end = p.StartPos, p.EndPos
	c.events = append(c.events, e)
}

// The traversal visits a node before its children: a namespace statement before the code it covers.
func (c *collector) StmtNamespace(n *ast.StmtNamespace) {
	name := ""
	if nm, ok := n.Name.(*ast.Name); ok {
		name = joinParts(nm.Parts)
		c.skip[nm] = true
	}
	c.ns = nsresolver.NewNamespace(name)
}

func (c *collector) StmtClass(n *ast.StmtClass) {
	c.class(n, n.Name, agentlsp.KindClass, startOf(n.OpenCurlyBracketTkn))
}

func (c *collector) StmtInterface(n *ast.StmtInterface) {
	c.class(n, n.Name, agentlsp.KindInterface, startOf(n.OpenCurlyBracketTkn))
}

func (c *collector) StmtTrait(n *ast.StmtTrait) {
	c.class(n, n.Name, agentlsp.KindTrait, startOf(n.OpenCurlyBracketTkn))
}

func (c *collector) StmtEnum(n *ast.StmtEnum) {
	c.class(n, n.Name, agentlsp.KindEnum, startOf(n.OpenCurlyBracketTkn))
}

func (c *collector) class(n ast.Vertex, name ast.Vertex, kind agentlsp.Kind, body int) {
	p := n.GetPosition()
	if p == nil {
		return
	}
	decl := classDecl{start: p.StartPos, end: p.EndPos}
	if d, ok := c.declaration(kind, name, n, body); ok {
		decl.name = c.qualify(d.Name)
		d.Key = classKey(decl.name)
		c.decls = append(c.decls, d)
	}
	c.classes = append(c.classes, decl)
}

func (c *collector) StmtClassMethod(n *ast.StmtClassMethod) {
	body := -1 // a method without a body: its signature is its whole text
	if n.Position != nil {
		body = n.Position.EndPos
	}
	if list, ok := n.Stmt.(*ast.StmtStmtList); ok {
		body = startOf(list.OpenCurlyBracketTkn)
	}
	c.member(agentlsp.KindMethod, n.Name, n, body)
	c.scope(n, &scope{})
}

func (c *collector) StmtFunction(n *ast.StmtFunction) {
	if d, ok := c.declaration(agentlsp.KindFunc, n.Name, n, startOf(n.OpenCurlyBracketTkn)); ok {
		d.Key = functionKey(c.qualify(d.Name))
		c.decls = append(c.decls, d)
	}
	c.scope(n, &scope{})
}

func (c *collector) ExprArrowFunction(n *ast.ExprArrowFunction) { c.scope(n, &scope{arrow: true}) }

func (c *collector) ExprClosure(n *ast.ExprClosure) {
	s := &scope{imports: map[string]bool{}}
	for _, u := range n.Uses {
		if use, ok := u.(*ast.ExprClosureUse); ok {
			s.uses = append(s.uses, closureUse{name: variable(use.Var), byRef: use.AmpersandTkn != nil})
		}
	}
	c.scope(n, s)
}

func (c *collector) scope(n ast.Vertex, s *scope) {
	p := n.GetPosition()
	if p == nil {
		c.noPosition = true
		return
	}
	s.start, s.end, s.vars = p.StartPos, p.EndPos, map[string]*binding{}
	c.scopes = append(c.scopes, s)
}

func (c *collector) ExprAssign(n *ast.ExprAssign) {
	name, class := variable(n.Var), c.newClass(n.Expr)
	switch {
	case name == "":
		c.spoilTarget(n.Var)
	case class != "":
		c.event(n, event{kind: write, name: name, class: class})
	default:
		c.event(n, event{kind: spoil, name: name})
	}
}

// `$a = &$b` binds both names to one value.
func (c *collector) ExprAssignReference(n *ast.ExprAssignReference) {
	c.spoilTarget(n.Var)
	c.spoilTarget(n.Expr)
}

func (c *collector) ExprAssignBitwiseAnd(n *ast.ExprAssignBitwiseAnd)     { c.spoilTarget(n.Var) }
func (c *collector) ExprAssignBitwiseOr(n *ast.ExprAssignBitwiseOr)       { c.spoilTarget(n.Var) }
func (c *collector) ExprAssignBitwiseXor(n *ast.ExprAssignBitwiseXor)     { c.spoilTarget(n.Var) }
func (c *collector) ExprAssignCoalesce(n *ast.ExprAssignCoalesce)         { c.spoilTarget(n.Var) }
func (c *collector) ExprAssignConcat(n *ast.ExprAssignConcat)             { c.spoilTarget(n.Var) }
func (c *collector) ExprAssignDiv(n *ast.ExprAssignDiv)                   { c.spoilTarget(n.Var) }
func (c *collector) ExprAssignMinus(n *ast.ExprAssignMinus)               { c.spoilTarget(n.Var) }
func (c *collector) ExprAssignMod(n *ast.ExprAssignMod)                   { c.spoilTarget(n.Var) }
func (c *collector) ExprAssignMul(n *ast.ExprAssignMul)                   { c.spoilTarget(n.Var) }
func (c *collector) ExprAssignPlus(n *ast.ExprAssignPlus)                 { c.spoilTarget(n.Var) }
func (c *collector) ExprAssignPow(n *ast.ExprAssignPow)                   { c.spoilTarget(n.Var) }
func (c *collector) ExprAssignShiftLeft(n *ast.ExprAssignShiftLeft)       { c.spoilTarget(n.Var) }
func (c *collector) ExprAssignShiftRight(n *ast.ExprAssignShiftRight)     { c.spoilTarget(n.Var) }
func (c *collector) ExprPreInc(n *ast.ExprPreInc)                         { c.spoilTarget(n.Var) }
func (c *collector) ExprPreDec(n *ast.ExprPreDec)                         { c.spoilTarget(n.Var) }
func (c *collector) ExprPostInc(n *ast.ExprPostInc)                       { c.spoilTarget(n.Var) }
func (c *collector) ExprPostDec(n *ast.ExprPostDec)                       { c.spoilTarget(n.Var) }
func (c *collector) Parameter(n *ast.Parameter)                           { c.spoilTarget(n.Var) }
func (c *collector) StmtCatch(n *ast.StmtCatch)                           { c.spoilTarget(n.Var) }
func (c *collector) StmtStaticVar(n *ast.StmtStaticVar)                   { c.spoilTarget(n.Var) }
func (c *collector) ExprMethodCall(n *ast.ExprMethodCall)                 { c.call(n.Var, n.Method) }
func (c *collector) ExprNullsafeMethodCall(n *ast.ExprNullsafeMethodCall) { c.call(n.Var, n.Method) }

// A variable passed as an argument may be passed by reference: without the callee's signature it may be rebound.
func (c *collector) Argument(n *ast.Argument) { c.spoilTarget(unbracket(n.Expr)) }

func (c *collector) ExprArrayItem(n *ast.ExprArrayItem) {
	if n.AmpersandTkn != nil {
		c.spoilTarget(n.Val)
	}
}

func (c *collector) StmtForeach(n *ast.StmtForeach) {
	c.spoilTarget(n.Key)
	c.spoilTarget(n.Var)
}

func (c *collector) StmtGlobal(n *ast.StmtGlobal) {
	for _, v := range n.Vars {
		c.spoilTarget(v)
	}
}

func (c *collector) StmtUnset(n *ast.StmtUnset) {
	for _, v := range n.Vars {
		c.spoilTarget(v)
	}
}

// A variable variable `$$n` may name any variable of its scope.
func (c *collector) ExprVariable(n *ast.ExprVariable) {
	if _, ok := n.Name.(*ast.Identifier); !ok {
		c.event(n, event{kind: dynamic})
	}
}

// eval and include run code in the current scope: it may write any of its variables.
func (c *collector) ExprEval(n *ast.ExprEval)               { c.event(n, event{kind: dynamic}) }
func (c *collector) ExprInclude(n *ast.ExprInclude)         { c.event(n, event{kind: dynamic}) }
func (c *collector) ExprIncludeOnce(n *ast.ExprIncludeOnce) { c.event(n, event{kind: dynamic}) }
func (c *collector) ExprRequire(n *ast.ExprRequire)         { c.event(n, event{kind: dynamic}) }
func (c *collector) ExprRequireOnce(n *ast.ExprRequireOnce) { c.event(n, event{kind: dynamic}) }

// extract, and parse_str or mb_parse_str with one argument, write variables of the current scope by names taken
// from data. define declares a constant.
func (c *collector) ExprFunctionCall(n *ast.ExprFunctionCall) {
	c.kinds[n.Function] = functionRef
	var parts []ast.Vertex
	switch f := n.Function.(type) {
	case *ast.Name:
		parts = f.Parts
	case *ast.NameFullyQualified:
		parts = f.Parts
	case *ast.NameRelative:
		parts = f.Parts
	default:
		return // PHP refuses to call these functions dynamically
	}
	if len(parts) == 0 {
		return
	}
	part, ok := parts[len(parts)-1].(*ast.NamePart)
	if !ok {
		return
	}
	switch strings.ToLower(string(part.Value)) {
	case "extract":
		c.event(n, event{kind: dynamic})
	case "parse_str", "mb_parse_str":
		if len(n.Args) < 2 || spreads(n.Args) {
			c.event(n, event{kind: dynamic})
		}
	case "define":
		if len(parts) == 1 {
			c.define(n)
		}
	}
}

func (c *collector) ExprConstFetch(n *ast.ExprConstFetch) { c.kinds[n.Const] = constRef }

// call records a method call whose receiver is `new X` or a variable.
func (c *collector) call(receiver, method ast.Vertex) {
	id, ok := method.(*ast.Identifier)
	if !ok {
		return
	}
	name := c.lines.span(id.Position)
	if class := c.newClass(receiver); class != "" {
		c.direct = append(c.direct, Link{Key: methodKey(class, string(id.Value)), Name: name})
		return
	}
	if v := variable(receiver); v != "" && !superglobals[v] {
		c.event(id, event{kind: call, name: v, class: string(id.Value), call: name})
	}
}

// superglobals are visible in every scope of the program without `global`: any function may write them, so their
// class is never known. PHP spells variable names case-sensitively; `$_session` is an ordinary variable.
var superglobals = map[string]bool{
	"GLOBALS": true, "_SERVER": true, "_GET": true, "_POST": true, "_FILES": true, "_COOKIE": true,
	"_SESSION": true, "_REQUEST": true, "_ENV": true,
}

// newClass is the class X of `new X(...)`, also in parentheses; "" for any other expression and for
// `new self`, `new static`, `new parent`, an anonymous or a dynamic class.
func (c *collector) newClass(e ast.Vertex) string {
	n, ok := unbracket(e).(*ast.ExprNew)
	if !ok {
		return ""
	}
	switch n.Class.(type) {
	case *ast.Name, *ast.NameFullyQualified, *ast.NameRelative:
	default:
		return ""
	}
	class, err := c.ns.ResolveName(n.Class, classRef)
	if err != nil {
		return ""
	}
	switch strings.ToLower(class) {
	case "", "self", "static", "parent":
		return ""
	}
	return class
}

// spoilTarget makes every variable written by target unknown: a variable, every variable of the scope for a
// variable variable, each variable inside a list or array destructuring.
func (c *collector) spoilTarget(target ast.Vertex) {
	switch t := target.(type) {
	case *ast.ExprVariable:
		if _, ok := t.Name.(*ast.Identifier); !ok {
			c.event(t, event{kind: dynamic})
		} else if name := variable(t); name != "" {
			c.event(t, event{kind: spoil, name: name})
		}
	case *ast.ExprList:
		c.spoilItems(t.Items)
	case *ast.ExprArray:
		c.spoilItems(t.Items)
	}
}

func (c *collector) spoilItems(items []ast.Vertex) {
	for _, item := range items {
		if it, ok := item.(*ast.ExprArrayItem); ok {
			c.spoilTarget(it.Val)
		}
	}
}

func unbracket(e ast.Vertex) ast.Vertex {
	for {
		b, ok := e.(*ast.ExprBrackets)
		if !ok {
			return e
		}
		e = b.Expr
	}
}

func spreads(args []ast.Vertex) bool {
	for _, a := range args {
		if arg, ok := a.(*ast.Argument); ok && arg.VariadicTkn != nil {
			return true
		}
	}
	return false
}

func joinParts(parts []ast.Vertex) string {
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		if part, ok := p.(*ast.NamePart); ok {
			names = append(names, string(part.Value))
		}
	}
	return strings.Join(names, `\`)
}

// variable is the name of a plain variable `$x` without the dollar; "" for `$this`, `$$x` and anything else.
func variable(e ast.Vertex) string {
	v, ok := e.(*ast.ExprVariable)
	if !ok {
		return ""
	}
	id, ok := v.Name.(*ast.Identifier)
	if !ok {
		return ""
	}
	name := strings.TrimPrefix(string(id.Value), "$")
	if name == "this" {
		return ""
	}
	return name
}

// scope is the variables of a function body, a method body, a closure, an arrow function or a file's top level.
type scope struct {
	start, end int // the byte range of the scope's node
	parent     *scope
	top        bool            // the file's top level: any file may reach its variables, so none is known
	arrow      bool            // an arrow function sees every variable of its parent it does not write
	uses       []closureUse    // a closure's `use` list
	imports    map[string]bool // a closure sees the variables of its parent it imports by value with `use`
	dynamic    bool            // something in the scope may write any of its variables
	vars       map[string]*binding
}

type closureUse struct {
	name  string
	byRef bool
}

// binding is what the scope's writes say about a variable.
type binding struct {
	class string
	first int  // the start of the first write
	after int  // the variable holds `new class` only after this byte offset: the end of the first write
	bad   bool // some write is not `new class` of that one class
}

func (s *scope) assign(name, class string, at, end int) {
	b, ok := s.vars[name]
	if !ok {
		s.vars[name] = &binding{class: class, first: at, after: end}
		return
	}
	if !strings.EqualFold(b.class, class) {
		b.bad = true
	}
	if at < b.first {
		b.first, b.after = at, end
	}
}

func (s *scope) spoil(name string) {
	if name == "" {
		return
	}
	b, ok := s.vars[name]
	if !ok {
		b = &binding{}
		s.vars[name] = b
	}
	b.bad = true
}

// classOf is the class of variable name at byte offset at; "" when the class is not known. A variable of the
// top level is never known: `include`, `global` and `$GLOBALS` in any file, also one the index does not hold,
// may write it.
func (s *scope) classOf(name string, at int) string {
	if s.top || s.dynamic {
		return ""
	}
	b, ok := s.vars[name]
	if !ok {
		if s.parent != nil && (s.arrow || s.imports[name]) {
			return s.parent.classOf(name, at)
		}
		return ""
	}
	if b.bad || at < b.after {
		return ""
	}
	return b.class
}
