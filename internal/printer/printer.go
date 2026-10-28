// Package printer renders a syntax tree back to source text.
//
// It is the seed of the Phaenon → PHP emitter: given an ast.File it produces
// canonical, PSR-12-flavoured PHP. Two invariants matter more than beauty:
//
//   - Fidelity: the printed text lexes to the same non-trivia token stream as
//     the source it was parsed from (modulo spellings the tree does not keep,
//     such as modifier order or `<>` vs `!=`). The conformance suite checks
//     this over the whole php-src test corpus.
//   - Determinism: the same tree always prints the same bytes.
//
// Grouping parentheses are reproduced from ast.Paren nodes; the printer does
// not reason about precedence itself. Code that synthesises trees must wrap
// sub-expressions in Paren where PHP needs them.
package printer

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
)

// Printer accumulates output. Zero value is ready to use.
type Printer struct {
	sb        strings.Builder
	depth     int
	lineStart bool
	indentStr string
	// nextIsCloseTag is set by stmts() while printing a statement that is
	// directly followed by `?>`, so `<?= $x ?>` prints without a semicolon.
	nextIsCloseTag bool
}

// File renders a whole file.
func File(f *ast.File) string {
	p := &Printer{lineStart: true, indentStr: "    "}
	p.stmts(f.Stmts)
	return p.sb.String()
}

// Expr renders a single expression.
func Expr(e ast.Expr) string {
	p := &Printer{indentStr: "    "}
	p.expr(e)
	return p.sb.String()
}

// Stmt renders a single statement (without a trailing newline).
func Stmt(s ast.Stmt) string {
	p := &Printer{lineStart: true, indentStr: "    "}
	p.stmt(s)
	return strings.TrimRight(p.sb.String(), "\n")
}

// Type renders a type expression.
func Type(t ast.TypeExpr) string {
	p := &Printer{indentStr: "    "}
	p.typ(t)
	return p.sb.String()
}

// ------------------------------------------------------------------ output primitives

// w writes s, emitting indentation first when at the start of a line.
func (p *Printer) w(s string) {
	if s == "" {
		return
	}
	if p.lineStart {
		for i := 0; i < p.depth; i++ {
			p.sb.WriteString(p.indentStr)
		}
		p.lineStart = false
	}
	p.sb.WriteString(s)
}

// raw writes s verbatim: no indentation, and the line state follows s.
func (p *Printer) raw(s string) {
	if s == "" {
		return
	}
	p.sb.WriteString(s)
	p.lineStart = strings.HasSuffix(s, "\n")
}

func (p *Printer) nl() {
	if !p.lineStart {
		p.sb.WriteByte('\n')
		p.lineStart = true
	}
}

// blank emits an empty line (once, even when called repeatedly).
func (p *Printer) blank() {
	p.nl()
	if !strings.HasSuffix(p.sb.String(), "\n\n") {
		p.sb.WriteByte('\n')
	}
}

// ------------------------------------------------------------------ names and small helpers

func (p *Printer) name(n *ast.Name) {
	switch n.Kind {
	case ast.NameFullyQualified:
		p.w("\\")
	case ast.NameRelative:
		p.w("namespace\\")
	}
	p.w(strings.Join(n.Parts, "\\"))
}

func (p *Printer) names(ns []*ast.Name) {
	for i, n := range ns {
		if i > 0 {
			p.w(", ")
		}
		p.name(n)
	}
}

func (p *Printer) namedTypes(ts []*ast.NamedType) {
	for i, t := range ts {
		if i > 0 {
			p.w(", ")
		}
		p.typ(t)
	}
}

func (p *Printer) ident(id *ast.Ident) { p.w(id.Name) }

// modifiers prints a modifier set in canonical order, each followed by a space.
func (p *Printer) modifiers(m ast.Modifier) {
	order := []struct {
		flag ast.Modifier
		text string
	}{
		{ast.ModAbstract, "abstract"},
		{ast.ModFinal, "final"},
		{ast.ModPublic, "public"},
		{ast.ModProtected, "protected"},
		{ast.ModPrivate, "private"},
		{ast.ModPublicSet, "public(set)"},
		{ast.ModProtectedSet, "protected(set)"},
		{ast.ModPrivateSet, "private(set)"},
		{ast.ModStatic, "static"},
		{ast.ModReadonly, "readonly"},
		{ast.ModVar, "var"},
	}
	for _, o := range order {
		if m&o.flag != 0 {
			p.w(o.text)
			p.w(" ")
		}
	}
}

// attrsInline prints attribute groups separated by spaces, with a trailing space.
func (p *Printer) attrsInline(groups []*ast.AttrGroup) {
	for _, g := range groups {
		p.attrGroup(g)
		p.w(" ")
	}
}

// attrsLines prints each attribute group on its own line.
func (p *Printer) attrsLines(groups []*ast.AttrGroup) {
	for _, g := range groups {
		p.attrGroup(g)
		p.nl()
	}
}

func (p *Printer) attrGroup(g *ast.AttrGroup) {
	p.w("#[")
	for i, a := range g.Attrs {
		if i > 0 {
			p.w(", ")
		}
		p.name(a.Name)
		if a.Args != nil {
			p.argList(a.Args)
		}
	}
	p.w("]")
}

func (p *Printer) doc(d string) {
	if d == "" {
		return
	}
	p.w(d)
	p.nl()
}

// ------------------------------------------------------------------ types

func (p *Printer) typ(t ast.TypeExpr) {
	switch t := t.(type) {
	case *ast.NamedType:
		p.name(t.Name)
		p.typeArgs(t.TypeArgs)
	case *ast.NullableType:
		p.w("?")
		p.typ(t.Type)
	case *ast.UnionType:
		for i, m := range t.Types {
			if i > 0 {
				p.w("|")
			}
			p.typ(m)
		}
	case *ast.IntersectionType:
		if t.Parenthesised {
			p.w("(")
		}
		for i, m := range t.Types {
			if i > 0 {
				p.w("&")
			}
			p.typ(m)
		}
		if t.Parenthesised {
			p.w(")")
		}
	case *ast.ClosureType:
		p.w("Closure(")
		for i, m := range t.Params {
			if i > 0 {
				p.w(", ")
			}
			p.typ(m)
		}
		p.w(")")
		if t.Return != nil {
			p.w(": ")
			p.typ(t.Return)
		}
	default:
		panic("printer: unknown type node")
	}
}

func (p *Printer) typeArgs(args []ast.TypeExpr) {
	if len(args) == 0 {
		return
	}
	p.w("<")
	for i, a := range args {
		if i > 0 {
			p.w(", ")
		}
		p.typ(a)
	}
	p.w(">")
}

func (p *Printer) typeParams(params []*ast.TypeParam) {
	if len(params) == 0 {
		return
	}
	p.w("<")
	for i, tp := range params {
		if i > 0 {
			p.w(", ")
		}
		if tp.Reified {
			p.w("reified ")
		}
		switch tp.Variance {
		case ast.Covariant:
			p.w("out ")
		case ast.Contravariant:
			p.w("in ")
		}
		p.ident(tp.Name)
		if tp.Bound != nil {
			p.w(" of ")
			p.typ(tp.Bound)
		}
		if tp.Default != nil {
			p.w(" = ")
			p.typ(tp.Default)
		}
	}
	p.w(">")
}

func (p *Printer) returnType(t ast.TypeExpr) {
	if t != nil {
		p.w(": ")
		p.typ(t)
	}
}

// ------------------------------------------------------------------ parameters and hooks

func (p *Printer) params(params []*ast.Param) {
	p.w("(")
	for i, pr := range params {
		if i > 0 {
			p.w(", ")
		}
		p.param(pr)
	}
	p.w(")")
}

func (p *Printer) param(pr *ast.Param) {
	p.attrsInline(pr.Attrs)
	p.modifiers(pr.Modifiers)
	if pr.Type != nil {
		p.typ(pr.Type)
		p.w(" ")
	}
	if pr.ByRef {
		p.w("&")
	}
	if pr.Variadic {
		p.w("...")
	}
	p.expr(pr.Var)
	if pr.Default != nil {
		p.w(" = ")
		p.expr(pr.Default)
	}
	if pr.Hooks != nil {
		p.w(" ")
		p.hooks(pr.Hooks)
	}
}

func (p *Printer) hooks(hooks []*ast.PropertyHook) {
	p.w("{")
	p.nl()
	p.depth++
	for _, h := range hooks {
		p.attrsInline(h.Attrs)
		p.modifiers(h.Modifiers)
		if h.ByRef {
			p.w("&")
		}
		p.ident(h.Name)
		if h.Params != nil {
			p.params(h.Params)
		}
		switch {
		case h.Abstract:
			p.w(";")
		case h.Expr != nil:
			p.w(" => ")
			p.expr(h.Expr)
			p.w(";")
		default:
			p.w(" ")
			p.block(h.Body)
		}
		p.nl()
	}
	p.depth--
	p.w("}")
}
