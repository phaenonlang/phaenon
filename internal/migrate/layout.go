package migrate

// The canonical layout (design/functions.md §5).
//
// Layout rewrites a Phaenon file into the style the PhpStorm plugin, `phn
// migrate` and later `phn fmt` write:
//
//   - the opening brace of a function, a method, a closure or an anonymous
//     class moves up to the declaration's line (control structures have
//     theirs there already);
//   - the opening brace of a class, interface, trait or enum stands on a
//     line of its own (design/line-map.md §7), and moves back down where an
//     earlier run of migrate moved it up;
//   - an empty body — a function or method without statements, a class-like
//     without members, and no comment inside — is `{}` on the declaration's
//     line, as PER-CS 2.0 writes it;
//   - a docblock with one line of content becomes one line;
//   - a body that is exactly one `return expr;` — or one expression
//     statement under void or never — with no comment inside the braces
//     becomes `=> expr;`, wrapped after the arrow when the line would pass
//     120 columns.
//
// The brace and body rewrites remove lines, which is the point of them: the
// .phn is the new source, and nothing else in a file moves. A declaration
// with a comment where the edit would go keeps its layout, so nothing the
// programmer wrote is lost. Re-running is a no-op.

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/token"
)

// Stats counts what a migration rewrote, for phn migrate's summary.
type Stats struct {
	Throws    int // bare @throws tags lifted into `throws` clauses
	Arrows    int // block bodies written as `=> expr;`
	Braces    int // opening braces moved up to their declaration's line
	Down      int // class-like braces moved back down to a line of their own
	Empty     int // empty bodies (functions, methods, class-likes) written as {}
	Docblocks int // docblocks collapsed onto one line
}

// maxColumns is the width past which an expression body wraps after its
// arrow, and which a collapsed docblock may not pass.
const maxColumns = 120

// Layout applies the canonical layout to Phaenon source. stats, when not
// nil, is incremented by what it rewrote.
func Layout(src []byte, stats *Stats) ([]byte, error) {
	if stats == nil {
		stats = &Stats{}
	}
	// Braces and docblocks first, bodies on the result: a closure inside a
	// body that becomes an arrow moves its brace in the first pass, and the
	// arrow carries the moved text. A method of an anonymous class inside
	// a body that becomes an arrow is written as one in the next round.
	out, err := layoutPass(src, stats, false)
	if err != nil {
		return nil, err
	}
	for {
		before := stats.Arrows
		if out, err = layoutPass(out, stats, true); err != nil {
			return nil, err
		}
		if stats.Arrows == before {
			return out, nil
		}
	}
}

type layout struct {
	tokenIndex
	src   []byte
	edits []edit
	stats *Stats
	// arrows are the declarations written as arrows in this pass; one
	// nested in another waits for the next.
	arrows []ast.Span
}

func layoutPass(src []byte, stats *Stats, bodies bool) ([]byte, error) {
	f, err := parser.Parse(src, lexer.ModePhaenon)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	l := &layout{tokenIndex: tokenIndex{lexer.Tokenize(src, lexer.ModePhaenon)}, src: src, stats: stats}
	if !bodies {
		l.docblocks()
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ClassDecl:
			if !bodies {
				l.classBrace(n)
			}
		case *ast.FunctionDecl:
			l.function(n.Name, n.TypeParams, n.ReturnType, n.Throws, n.Body, n.Arrow == nil, n.S, bodies)
		case *ast.MethodDecl:
			l.function(n.Name, n.TypeParams, n.ReturnType, n.Throws, n.Body, n.HasBody && n.Arrow == nil, n.S, bodies)
		case *ast.Closure:
			if !bodies {
				l.closureBrace(n)
			}
		}
		return true
	})
	sort.SliceStable(l.edits, func(i, j int) bool { return l.edits[i].Start < l.edits[j].Start })
	var b bytes.Buffer
	pos := 0
	for _, e := range l.edits {
		if e.Start < pos {
			return nil, fmt.Errorf("layout: overlapping edits at offset %d", e.Start)
		}
		b.Write(src[pos:e.Start])
		b.WriteString(e.text)
		pos = e.End
	}
	b.Write(src[pos:])
	return b.Bytes(), nil
}

// ------------------------------------------------------------------ tokens

// tokenIndex finds positions in a file's token stream.
type tokenIndex struct{ toks []token.Token }

// tokenAt is the index of the first token starting at or after off.
func (t tokenIndex) tokenAt(off int) int {
	return sort.Search(len(t.toks), func(i int) bool { return t.toks[i].Start >= off })
}

// nextCode is the index of the first non-trivia token at or after index i.
func (t tokenIndex) nextCode(i int) int {
	for i < len(t.toks) && t.toks[i].Kind.IsTrivia() {
		i++
	}
	return i
}

// matching is the index of the `)` closing the `(` at index open.
func (t tokenIndex) matching(open int) int {
	depth := 0
	for i := open; i < len(t.toks); i++ {
		switch t.toks[i].Kind {
		case token.Kind('('):
			depth++
		case token.Kind(')'):
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// parenList is the end of the parenthesised list that starts at the first
// `(` at or after off, -1 when there is none.
func (t tokenIndex) parenList(off int) int {
	i := t.nextCode(t.tokenAt(off))
	if i >= len(t.toks) || t.toks[i].Kind != token.Kind('(') {
		return -1
	}
	if c := t.matching(i); c >= 0 {
		return t.toks[c].End
	}
	return -1
}

// hasComment reports whether a comment lies in src[from:to).
func (l *layout) hasComment(from, to int) bool {
	for i := l.tokenAt(from); i < len(l.toks) && l.toks[i].Start < to; i++ {
		if k := l.toks[i].Kind; k == token.T_COMMENT || k == token.T_DOC_COMMENT {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ braces

// moveBrace puts the `{` that follows offset after on after's line, when
// only whitespace with a line break lies between them.
func (l *layout) moveBrace(after int) {
	i := l.tokenAt(after)
	j := l.nextCode(i)
	if j >= len(l.toks) || l.toks[j].Kind != token.Kind('{') {
		return
	}
	gap := l.src[after:l.toks[j].Start]
	if !bytes.ContainsAny(gap, "\n\r") || l.hasComment(after, l.toks[j].Start) {
		return
	}
	l.edits = append(l.edits, edit{ast.Span{Start: after, End: l.toks[j].Start}, " "})
	l.stats.Braces++
}

// classBrace puts a named class-like's opening brace on a line of its own,
// at the indentation of the line holding its name, and moves an anonymous
// class's up, as a closure's.
func (l *layout) classBrace(c *ast.ClassDecl) {
	from := c.S.Start
	switch {
	case c.Args != nil:
		from = c.Args.Span().End
	case c.Name != nil:
		from = c.Name.S.End
	}
	// The header — type parameters, extends, implements, a backing type —
	// holds no brace; the first `{` after the name opens the body.
	i := l.tokenAt(from)
	for i < len(l.toks) && l.toks[i].Kind != token.Kind('{') {
		i++
	}
	if i == len(l.toks) {
		return
	}
	prev := i - 1
	for prev >= 0 && l.toks[prev].Kind.IsTrivia() {
		prev--
	}
	if prev < 0 {
		return
	}
	if c.Name == nil {
		l.moveBrace(l.toks[prev].End)
		return
	}
	// An empty body — no member, no comment — is `{}` on the header's line.
	if close := l.nextCode(i + 1); len(c.Members) == 0 && close < len(l.toks) && l.toks[close].Kind == token.Kind('}') && !l.hasComment(l.toks[prev].End, l.toks[close].End) {
		if string(l.src[l.toks[prev].End:l.toks[close].End]) != " {}" {
			l.edits = append(l.edits, edit{ast.Span{Start: l.toks[prev].End, End: l.toks[close].End}, " {}"})
			l.stats.Empty++
		}
		return
	}
	gap := l.src[l.toks[prev].End:l.toks[i].Start]
	if bytes.ContainsAny(gap, "\n\r") || l.hasComment(l.toks[prev].End, l.toks[i].Start) {
		return
	}
	l.edits = append(l.edits, edit{ast.Span{Start: l.toks[prev].End, End: l.toks[i].Start}, "\n" + indentOf(l.src, c.Name.S.Start)})
	l.stats.Down++
}

func (l *layout) closureBrace(c *ast.Closure) {
	end := l.parenList(c.Kw.End)
	if end < 0 {
		return
	}
	if len(c.Uses) > 0 {
		if u := l.parenList(end); u > 0 {
			end = u
		}
	}
	if c.ReturnType != nil {
		end = c.ReturnType.Span().End
	}
	l.moveBrace(end)
}

// signatureEnd is the end of a named function's signature: the parameter
// list, the return type, the `throws` clause.
func (l *layout) signatureEnd(name *ast.Ident, tps []*ast.TypeParam, ret ast.TypeExpr, throws *ast.Throws) int {
	from := name.S.End
	if len(tps) > 0 {
		from = tps[len(tps)-1].S.End
		// Past the `>` closing the type parameters.
		if i := l.nextCode(l.tokenAt(from)); i < len(l.toks) {
			from = l.toks[i].End
		}
	}
	end := l.parenList(from)
	if ret != nil {
		end = ret.Span().End
	}
	if throws != nil {
		end = throws.S.End
	}
	return end
}

// ------------------------------------------------------------------ functions

// function moves a block body's brace up (first pass) or writes it as an
// expression body (second pass); a body that can become an arrow keeps
// its brace for the second pass.
func (l *layout) function(name *ast.Ident, tps []*ast.TypeParam, ret ast.TypeExpr, throws *ast.Throws, body []ast.Stmt, block bool, span ast.Span, bodies bool) {
	if !block {
		return
	}
	sigEnd := l.signatureEnd(name, tps, ret, throws)
	if sigEnd < 0 {
		return
	}
	open := l.nextCode(l.tokenAt(sigEnd))
	if open >= len(l.toks) || l.toks[open].Kind != token.Kind('{') {
		return
	}
	// A comment between the signature and the brace, or inside the braces,
	// keeps the block: the arrow would have nowhere to put it.
	expr := l.arrowCandidate(name, ret, body, sigEnd, span.End)
	switch {
	case expr != nil && bodies:
		for _, a := range l.arrows {
			if a.Start <= span.Start && span.End <= a.End {
				return
			}
		}
		l.arrows = append(l.arrows, span)
		l.arrow(name, sigEnd, expr, body[0].Span(), span)
	case expr == nil && !bodies:
		if !l.emptyBody(sigEnd, open, len(body)) {
			l.moveBrace(sigEnd)
		}
	}
}

// emptyBody writes a body with nothing in it — no statement, no comment —
// as `{}` on the declaration's line, and reports whether the body is one.
func (l *layout) emptyBody(sigEnd, open, statements int) bool {
	if statements != 0 {
		return false
	}
	close := l.nextCode(open + 1)
	if close >= len(l.toks) || l.toks[close].Kind != token.Kind('}') || l.hasComment(sigEnd, l.toks[close].End) {
		return false
	}
	if string(l.src[sigEnd:l.toks[close].End]) != " {}" {
		l.edits = append(l.edits, edit{ast.Span{Start: sigEnd, End: l.toks[close].End}, " {}"})
		l.stats.Empty++
	}
	return true
}

// arrowCandidate returns the expression a body may be written as, nil when
// it stays a block: exactly one `return expr;`, or one expression
// statement under void or never; no comment from the end of the signature
// (sigEnd) to the closing brace (end); not a constructor, a destructor or a
// generator (design/functions.md §3.1).
func (l *layout) arrowCandidate(name *ast.Ident, ret ast.TypeExpr, body []ast.Stmt, sigEnd, end int) ast.Expr {
	if len(body) != 1 || l.hasComment(sigEnd, end) {
		return nil
	}
	switch strings.ToLower(name.Name) {
	case "__construct", "__destruct":
		return nil
	}
	var e ast.Expr
	switch s := body[0].(type) {
	case *ast.Return:
		if isVoidOrNever(ret) {
			return nil
		}
		e = s.Expr
	case *ast.ExprStmt:
		if !isVoidOrNever(ret) {
			return nil
		}
		e = s.Expr
	}
	if e == nil || yields(e) {
		return nil
	}
	return e
}

// isVoidOrNever reports a return type under which an expression body is
// only evaluated.
func isVoidOrNever(t ast.TypeExpr) bool {
	nt, ok := t.(*ast.NamedType)
	if !ok || len(nt.TypeArgs) > 0 || len(nt.Name.Parts) != 1 || nt.Name.Kind != ast.NameUnqualified {
		return false
	}
	w := strings.ToLower(nt.Name.Parts[0])
	return w == "void" || w == "never"
}

// yields reports a yield that makes the enclosing function a generator.
func yields(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.Yield, *ast.YieldFrom:
			found = true
		case *ast.Closure, *ast.ArrowFunc, *ast.ClassDecl, *ast.Concurrently, *ast.Spawn:
			return false
		}
		return !found
	})
	return found
}

// arrow replaces everything from the end of the signature to the closing
// brace with ` => expr;`. On one line with the signature when that line
// fits in 120 columns, the expression's continuation lines moving one
// level out with it; otherwise wrapped after the arrow, which starts the
// next line one level in from the declaration and leaves the expression's
// lines where they were (they were already one level in, under `return`).
func (l *layout) arrow(name *ast.Ident, sigEnd int, e ast.Expr, stmt, decl ast.Span) {
	src := l.src
	// The expression runs to the last token before the statement's `;`
	// (the tree's span of a prefix expression such as `throw $e` ends at
	// its operator).
	semi := l.tokenAt(stmt.End - 1)
	end := semi - 1
	for end >= 0 && l.toks[end].Kind.IsTrivia() {
		end--
	}
	exprSpan := ast.Span{Start: e.Span().Start, End: l.toks[end].End}
	text := string(src[exprSpan.Start:exprSpan.End])
	declIndent := indentOf(src, name.S.Start)
	level := "    "
	if strings.Contains(declIndent, "\t") {
		level = "\t"
	}
	if stmtIndent := indentOf(src, stmt.Start); lineStart(src, stmt.Start) != lineStart(src, decl.Start) &&
		strings.HasPrefix(stmtIndent, declIndent) && len(stmtIndent) > len(declIndent) && isLineStart(src, stmt.Start) {
		level = stmtIndent[len(declIndent):]
	}
	lines := strings.Split(text, "\n")
	inline := " => " + text + ";"
	if len(lines) > 1 {
		dedented, ok := l.dedent(lines, declIndent, level, exprSpan)
		if ok {
			inline = " => " + dedented + ";"
		} else {
			inline = ""
		}
	}
	first := string(src[lineStart(src, sigEnd):sigEnd]) + strings.SplitN(inline, "\n", 2)[0]
	repl := inline
	if inline == "" || width(first) > maxColumns {
		repl = "\n" + declIndent + level + "=> " + text + ";"
	}
	l.edits = append(l.edits, edit{ast.Span{Start: sigEnd, End: decl.End}, repl})
	l.stats.Arrows++
}

// dedent moves an expression's continuation lines one level out, for an
// arrow that joins the signature's line. It refuses (ok false) when a line
// is indented less than the body was, or when a token of the expression
// spans lines (a string or heredoc, whose text would change).
func (l *layout) dedent(lines []string, declIndent, level string, span ast.Span) (string, bool) {
	for i := l.tokenAt(span.Start); i < len(l.toks) && l.toks[i].Start < span.End; i++ {
		t := l.toks[i]
		if t.Kind != token.T_WHITESPACE && bytes.ContainsAny(l.src[t.Start:t.End], "\n\r") {
			return "", false
		}
	}
	bodyIndent := declIndent + level
	out := []string{lines[0]}
	for _, line := range lines[1:] {
		switch {
		case strings.TrimSpace(line) == "":
			out = append(out, "")
		case strings.HasPrefix(line, bodyIndent):
			out = append(out, declIndent+line[len(bodyIndent):])
		default:
			return "", false
		}
	}
	return strings.Join(out, "\n"), true
}

// ------------------------------------------------------------------ docblocks

// docblocks collapses every docblock with exactly one line of content onto
// one line, unless the result would pass 120 columns.
func (l *layout) docblocks() {
	for _, t := range l.toks {
		if t.Kind != token.T_DOC_COMMENT {
			continue
		}
		text := string(l.src[t.Start:t.End])
		if !strings.Contains(text, "\n") {
			continue
		}
		var lines []string
		for _, line := range strings.Split(text, "\n") {
			if c := content(line); c != "" {
				lines = append(lines, c)
			}
		}
		if len(lines) != 1 {
			continue
		}
		one := "/** " + lines[0] + " */"
		if width(string(l.src[lineStart(l.src, t.Start):t.Start])+one) > maxColumns {
			continue
		}
		l.edits = append(l.edits, edit{ast.Span{Start: t.Start, End: t.End}, one})
		l.stats.Docblocks++
	}
}

// ------------------------------------------------------------------ text helpers

func lineStart(src []byte, off int) int { return bytes.LastIndexByte(src[:off], '\n') + 1 }

// isLineStart reports whether only whitespace precedes off on its line.
func isLineStart(src []byte, off int) bool {
	return len(bytes.TrimSpace(src[lineStart(src, off):off])) == 0
}

// indentOf is the whitespace that starts the line holding off.
func indentOf(src []byte, off int) string {
	start := lineStart(src, off)
	end := start
	for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	return string(src[start:end])
}

// width is the display width of a line: runes, a tab counting four.
func width(s string) int {
	n := 0
	for _, r := range s {
		if r == '\t' {
			n += 4
		} else {
			n++
		}
	}
	return n
}
