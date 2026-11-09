package emit

import (
	"bytes"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/token"
)

// This file lays out what the compiler rewrites around declarations as
// PER Coding Style 3.0 and php-cs-fixer's @PER-CS3x0 want it
// (design/line-map.md §2, roadmap D22):
//
//   - the opening brace of a named function or method goes on the line
//     after its signature, at the indentation of the `function` keyword's
//     line, except after a multi-line parameter list (the `)` first on its
//     line), where `) {` share the line;
//   - the opening brace of a class, interface, trait or enum goes on the
//     line after its header;
//   - an empty body — no statement, member or comment — stays as written:
//     `{}` on the declaration's line is PER's abbreviation of it, and
//     Phaenon's;
//   - a body written on one line (`{ return 1; }`) gets its statements and
//     its closing brace on lines of their own;
//   - an expression body `=> expr;` becomes the block it stands for, the
//     expression's continuation lines moving in with it.
//
// Closures and anonymous classes keep their braces on their lines, as PER
// has them. A brace already on a line of its own stays as written, and a
// comment between a signature and its body keeps the layout as it is: it
// would have nowhere to go.

// function is what the layout needs of a named function or method.
type function struct {
	kwAt    int // an offset on the line of the `function` keyword
	name    *ast.Ident
	tps     []*ast.TypeParam
	ret     ast.TypeExpr
	throws  *ast.Throws
	arrow   *ast.ExprBody
	body    []ast.Stmt
	hasBody bool
	end     int // the end of the declaration, after its closing brace
}

// layoutFunction places the body of a named function or method and takes
// its `throws` clause out of the signature.
func (e *emitter) layoutFunction(fn function) {
	declIndent := e.outIndent(fn.kwAt)
	closeParen := e.paramsClose(fn.name, fn.tps)
	if closeParen < 0 {
		if fn.throws != nil {
			e.removeThrows(fn.throws, declIndent)
		}
		return
	}
	toks := e.tokens()
	sigEnd := toks[closeParen].End
	if fn.ret != nil {
		sigEnd = fn.ret.Span().End
	}
	tail := sigEnd
	if fn.throws != nil {
		tail = fn.throws.S.End
	}
	next := e.nextCode(e.tokenAt(tail))
	switch {
	case fn.arrow != nil:
		e.exprBody(fn, sigEnd, closeParen, declIndent)
	case !fn.hasBody:
		e.abstractTail(fn, sigEnd, tail, next, declIndent)
	default:
		e.blockBrace(fn, sigEnd, closeParen, next, declIndent)
	}
}

// blockBrace puts a block body's opening brace where PER wants it.
func (e *emitter) blockBrace(fn function, sigEnd, closeParen, open int, declIndent string) {
	toks := e.tokens()
	if open >= len(toks) || toks[open].Kind != token.Kind('{') || e.hasComment(sigEnd, toks[open].Start) {
		if fn.throws != nil {
			e.removeThrows(fn.throws, declIndent)
		}
		return
	}
	if e.emptyBody(open, fn.end) {
		if fn.throws != nil {
			e.removeThrows(fn.throws, declIndent)
		}
		return
	}
	brace := toks[open]
	gap := e.src[sigEnd:brace.Start]
	braceIndent := declIndent
	switch {
	case e.startsLine(toks[closeParen].Start):
		// After a multi-line parameter list: `) {` (or `): T {`) on one line.
		braceIndent = e.outIndent(toks[closeParen].Start)
		switch {
		case string(gap) == " ":
		case !bytes.ContainsRune(gap, '\n'):
			e.replace(ast.Span{Start: sigEnd, End: brace.Start}, " ")
		default:
			// The brace joins the signature's line: written there, so no
			// source token lands on a line another source line maps to.
			e.replaceFree(ast.Span{Start: sigEnd, End: brace.End}, " {")
		}
	case !bytes.ContainsRune(gap, '\n') || fn.throws != nil:
		e.replaceFree(ast.Span{Start: sigEnd, End: brace.Start}, "\n"+declIndent)
	default:
		braceIndent = e.outIndent(brace.Start) // on a line of its own already
	}
	e.oneLineBody(open, fn.end, braceIndent)
}

// abstractTail takes the `throws` clause out of a signature without a body.
func (e *emitter) abstractTail(fn function, sigEnd, tail, semi int, declIndent string) {
	if fn.throws == nil {
		return
	}
	toks := e.tokens()
	if semi >= len(toks) || toks[semi].Kind != token.Kind(';') || e.hasComment(sigEnd, toks[semi].Start) {
		e.removeThrows(fn.throws, declIndent)
		return
	}
	if bytes.ContainsRune(e.src[sigEnd:toks[semi].Start], '\n') {
		// The clause had a line of its own: the `;` closes the signature's.
		e.replaceFree(ast.Span{Start: sigEnd, End: toks[semi].End}, ";")
		return
	}
	e.replace(ast.Span{Start: sigEnd, End: tail}, "")
}

// exprBody writes an expression body as the PER block it stands for
// (design/functions.md §3, line-map.md §2):
//
//	public function name(): string
//	{
//	    return $this->name;
//	}
//
// `return` is left out under void and never; after a multi-line parameter
// list the brace shares the `)` line, as a block's does. The arrow's line —
// the signature's, or a continuation line of its own — ends up as the
// statement's; a multi-line expression keeps its line breaks, and its
// continuation lines move one level in when the expression started on the
// signature's line (planIndents). A comment between the signature and the
// expression keeps the old in-place form, `{ return expr; }`.
func (e *emitter) exprBody(fn function, sigEnd, closeParen int, declIndent string) {
	b := fn.arrow
	ret := false
	if len(fn.body) == 1 {
		_, ret = fn.body[0].(*ast.Return)
	}
	toks := e.tokens()
	first := e.nextCode(e.tokenAt(b.Arrow.End))
	if first >= len(toks) || e.hasComment(sigEnd, toks[first].Start) {
		if fn.throws != nil {
			e.removeThrows(fn.throws, declIndent)
		}
		open := "{"
		if ret {
			open = "{ return"
		}
		e.replace(b.Arrow, open)
		e.replace(b.Semi, "; }")
		return
	}
	exprStart := toks[first].Start
	level := "    "
	if strings.Contains(declIndent, "\t") {
		level = "\t"
	}
	bodyIndent := declIndent + level
	kw := ""
	if ret {
		kw = "return "
	}
	open := "\n" + declIndent + "{"
	if e.startsLine(toks[closeParen].Start) {
		open = " {" // `): T {` after a multi-line parameter list
	}
	e.replaceFree(ast.Span{Start: sigEnd, End: exprStart}, open+"\n"+bodyIndent+kw)
	e.replaceFree(b.Semi, ";\n"+declIndent+"}")
}

// planIndents registers, before any lowering writes text, the indentation
// the continuation lines of expression bodies gain, so that what the
// lowerings lay out inside them (a nested method's braces, a docblock)
// lands where those lines do (outIndent). A body's request depends on its
// source alone: the expression's continuation lines move in by what the
// block's statement stands in from the line the expression starts on.
func (e *emitter) planIndents(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		var fn function
		switch n := n.(type) {
		case *ast.FunctionDecl:
			fn = function{kwAt: n.Kw.Start, name: n.Name, tps: n.TypeParams, ret: n.ReturnType, arrow: n.Arrow}
		case *ast.MethodDecl:
			fn = function{kwAt: n.Kw.Start, name: n.Name, tps: n.TypeParams, ret: n.ReturnType, arrow: n.Arrow}
		default:
			return true
		}
		if fn.arrow == nil {
			return true
		}
		closeParen := e.paramsClose(fn.name, fn.tps)
		if closeParen < 0 {
			return true
		}
		toks := e.tokens()
		sigEnd := toks[closeParen].End
		if fn.ret != nil {
			sigEnd = fn.ret.Span().End
		}
		first := e.nextCode(e.tokenAt(fn.arrow.Arrow.End))
		if first >= len(toks) || e.hasComment(sigEnd, toks[first].Start) {
			return true
		}
		exprStart, semi := toks[first].Start, fn.arrow.Semi.Start
		if e.lineOf(exprStart) == e.lineOf(semi) || e.multiLineToken(exprStart, semi) {
			return true
		}
		declIndent := e.lineIndent(fn.kwAt)
		level := "    "
		if strings.Contains(declIndent, "\t") {
			level = "\t"
		}
		bodyIndent := declIndent + level
		if exprIndent := e.lineIndent(exprStart); len(bodyIndent) > len(exprIndent) && strings.HasPrefix(bodyIndent, exprIndent) {
			e.indents = append(e.indents, indentReq{Span: ast.Span{Start: exprStart, End: semi}, Text: bodyIndent[len(exprIndent):]})
		}
		return true
	})
}

// outIndent is the indentation the line holding off has in the output: its
// own, and what the expression bodies around it move it in by.
func (e *emitter) outIndent(off int) string {
	indent := e.lineIndent(off)
	start := bytes.LastIndexByte(e.src[:off], '\n')
	if start < 0 {
		return indent
	}
	for _, r := range e.indents {
		if r.Start <= start && start < r.End {
			indent += r.Text
		}
	}
	return indent
}

// classBrace puts the opening brace of a class, interface, trait or enum
// on the line after its header; an empty body stays as written.
func (e *emitter) classBrace(d *ast.ClassDecl) {
	toks := e.tokens()
	headerEnd := d.Name.S.End
	if n := len(d.TypeParams); n > 0 {
		if i := e.nextCode(e.tokenAt(d.TypeParams[n-1].S.End)); i < len(toks) {
			headerEnd = toks[i].End // the closing `>`
		}
	}
	for _, t := range append(append([]*ast.NamedType{}, d.Extends...), d.Implements...) {
		headerEnd = max(headerEnd, t.S.End)
	}
	if d.BackingType != nil {
		headerEnd = max(headerEnd, d.BackingType.Span().End)
	}
	open := e.nextCode(e.tokenAt(headerEnd))
	if open >= len(toks) || toks[open].Kind != token.Kind('{') || e.hasComment(headerEnd, toks[open].Start) {
		return
	}
	if e.emptyBody(open, d.S.End) {
		return
	}
	indent := e.outIndent(d.Name.S.Start)
	braceIndent := e.outIndent(toks[open].Start)
	if !bytes.ContainsRune(e.src[headerEnd:toks[open].Start], '\n') {
		e.replaceFree(ast.Span{Start: headerEnd, End: toks[open].Start}, "\n"+indent)
		braceIndent = indent
	}
	e.oneLineBody(open, d.S.End, braceIndent)
}

// oneLineBody lays out a body with statements whose braces share a line —
// the token at open and the last one before end: `{ stmts }` gets its
// statements on a line of their own one level in, and the closing brace on
// the next at indent. A comment inside leaves the body as written.
func (e *emitter) oneLineBody(open, end int, indent string) {
	toks := e.tokens()
	closeBrace := e.closeBrace(open, end)
	if closeBrace < 0 {
		return
	}
	inside := ast.Span{Start: toks[open].End, End: toks[closeBrace].Start}
	if bytes.ContainsRune(e.src[inside.Start:inside.End], '\n') || e.hasComment(inside.Start, inside.End) {
		return
	}
	first := e.nextCode(open + 1)
	if first == closeBrace {
		return
	}
	last := closeBrace - 1
	for toks[last].Kind.IsTrivia() {
		last--
	}
	level := "    "
	if strings.Contains(indent, "\t") {
		level = "\t"
	}
	e.replaceFree(ast.Span{Start: inside.Start, End: toks[first].Start}, "\n"+indent+level)
	e.replaceFree(ast.Span{Start: toks[last].End, End: inside.End}, "\n"+indent)
}

// closeBrace is the index of the `}` closing the body whose `{` is the
// token at open, the declaration ending at end; -1 when there is none.
func (e *emitter) closeBrace(open, end int) int {
	toks := e.tokens()
	i := e.tokenAt(end) - 1
	for i > open && toks[i].Kind.IsTrivia() {
		i--
	}
	if i <= open || toks[i].Kind != token.Kind('}') {
		return -1
	}
	return i
}

// emptyBody reports whether the body whose `{` is the token at open, the
// declaration ending at end, holds nothing but whitespace: PER Coding Style
// abbreviates such a body as `{}` on the declaration's line (§4.4 for
// functions and methods, §4 for class-likes), and `phn migrate` and
// `phn fmt` write it so.
func (e *emitter) emptyBody(open, end int) bool {
	closeBrace := e.closeBrace(open, end)
	return closeBrace > open && e.nextCode(open+1) == closeBrace && !e.hasComment(e.tokens()[open].End, e.tokens()[closeBrace].Start)
}

// paramsClose is the index of the `)` closing a declaration's parameter
// list, -1 when there is none.
func (e *emitter) paramsClose(name *ast.Ident, tps []*ast.TypeParam) int {
	toks := e.tokens()
	from := name.S.End
	if n := len(tps); n > 0 {
		from = tps[n-1].S.End
	}
	i := e.tokenAt(from)
	for i < len(toks) && toks[i].Kind != token.Kind('(') {
		i++
	}
	depth := 0
	for ; i < len(toks); i++ {
		switch toks[i].Kind {
		case token.Kind('('):
			depth++
		case token.Kind(')'):
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// tokenAt is the index of the first token starting at or after off.
func (e *emitter) tokenAt(off int) int {
	toks := e.tokens()
	return sort.Search(len(toks), func(i int) bool { return toks[i].Start >= off })
}

// nextCode is the index of the first token at or after index i that is no
// whitespace or comment.
func (e *emitter) nextCode(i int) int {
	toks := e.tokens()
	for i < len(toks) && toks[i].Kind.IsTrivia() {
		i++
	}
	return i
}

// hasComment reports whether a comment lies in src[from:to).
func (e *emitter) hasComment(from, to int) bool {
	toks := e.tokens()
	for i := e.tokenAt(from); i < len(toks) && toks[i].Start < to; i++ {
		if k := toks[i].Kind; k == token.T_COMMENT || k == token.T_DOC_COMMENT {
			return true
		}
	}
	return false
}

// multiLineToken reports whether a token other than whitespace in
// src[from:to) spans lines (a string, a heredoc, a comment), whose text
// moving its lines in would change.
func (e *emitter) multiLineToken(from, to int) bool {
	toks := e.tokens()
	for i := e.tokenAt(from); i < len(toks) && toks[i].Start < to; i++ {
		if t := toks[i]; t.Kind != token.T_WHITESPACE && bytes.ContainsRune(e.src[t.Start:t.End], '\n') {
			return true
		}
	}
	return false
}

// startsLine reports whether only whitespace precedes off on its line.
func (e *emitter) startsLine(off int) bool {
	start := bytes.LastIndexByte(e.src[:off], '\n') + 1
	return len(bytes.TrimSpace(e.src[start:off])) == 0
}
