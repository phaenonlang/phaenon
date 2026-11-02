package emit

import (
	"bytes"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
)

// lowerFunctions compiles the syntax of design/functions.md, none of which
// needs type information: a `fun` head becomes the PSR-12 `function` head
// with `public` written out, a class constant or a property or promoted
// parameter with an asymmetric visibility and no main one gets its implied
// `public`, a closure in a constant expression becomes the `static
// function` PHP 8.5 accepts there, an expression body becomes a block on
// the same lines and a `throws` clause a @throws tag. It runs after the
// generics lowering, so that a docblock that pass inserts at a
// declaration's start precedes the rewritten head, and so that the
// @throws tag joins that docblock when there is one.
func (e *emitter) lowerFunctions(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FunctionDecl:
			if n.Fun {
				e.replace(n.Head, "function")
			}
			e.lowerThrows(n, n.S.Start, n.Name.S.Start, n.Doc, n.Throws)
			e.lowerExprBody(n.Arrow, n.Body, n.Throws)
		case *ast.MethodDecl:
			if n.Fun {
				e.methodHead(n)
			}
			e.lowerThrows(n, n.S.Start, n.Name.S.Start, n.Doc, n.Throws)
			e.lowerExprBody(n.Arrow, n.Body, n.Throws)
		case *ast.PropertyDecl:
			e.impliedPublic(n.Modifiers, n.Mods)
		case *ast.ClassConstDecl:
			if n.Modifiers&(ast.ModPublic|ast.ModProtected|ast.ModPrivate) == 0 {
				e.insert(n.Kw.Start, "public ")
			}
		case *ast.Param:
			e.impliedPublic(n.Modifiers, n.Mods)
			if n.Default != nil {
				e.constClosures(n.Default)
			}
		case *ast.Attribute:
			if n.Args != nil {
				for _, a := range n.Args.Args {
					if a.Value != nil {
						e.constClosures(a.Value)
					}
				}
			}
		case *ast.PropertyItem:
			if n.Default != nil {
				e.constClosures(n.Default)
			}
		case *ast.ConstItem:
			e.constClosures(n.Value)
		case *ast.EnumCase:
			if n.Value != nil {
				e.constClosures(n.Value)
			}
		case *ast.StaticVar:
			if n.Default != nil {
				e.constClosures(n.Default)
			}
		}
		return true
	})
}

// lowerExprBody writes an expression body as the block it stands for
// (design/functions.md §3.2): the arrow becomes `{ return` (`{` when the
// parser made the body a statement, under void and never) and the final
// semicolon `; }`, each on its own line, so line N stays line N.
//
// An arrow that opens a continuation line, the long form, hands its brace
// to the end of the signature's line, after the return type (or the
// parameters), and becomes `return` itself, or vanishes under void and
// never: the statement then stands one level in from the declaration, as
// the expression's own continuation lines already do, and the output is
// the block a person writes, the brace placement aside.
func (e *emitter) lowerExprBody(b *ast.ExprBody, body []ast.Stmt, throws *ast.Throws) {
	if b == nil {
		return
	}
	ret := false
	if len(body) == 1 {
		_, ret = body[0].(*ast.Return)
	}
	lineStart := bytes.LastIndexByte(e.src[:b.Arrow.Start], '\n') + 1
	if len(bytes.TrimSpace(e.src[lineStart:b.Arrow.Start])) == 0 {
		before := b.Arrow.Start
		if throws != nil {
			before = throws.Kw.Start // the clause leaves the signature
		}
		if sig := e.codeEndBefore(before); sig >= 0 {
			e.insert(sig, " {")
			kw := ""
			if ret {
				kw = "return"
			}
			end := b.Arrow.End
			if !ret && end < len(e.src) && e.src[end] == ' ' {
				end++ // `=> expr` becomes `expr` where the arrow stood
			}
			e.replace(ast.Span{Start: b.Arrow.Start, End: end}, kw)
			e.replace(b.Semi, "; }")
			return
		}
	}
	open := "{"
	if ret {
		open = "{ return"
	}
	e.replace(b.Arrow, open)
	e.replace(b.Semi, "; }")
}

// codeEndBefore is the end of the last token before offset off that is no
// whitespace or comment, -1 when there is none.
func (e *emitter) codeEndBefore(off int) int {
	if e.toks == nil {
		e.toks = lexer.Tokenize(e.src, lexer.ModePhaenon)
	}
	i := sort.Search(len(e.toks), func(i int) bool { return e.toks[i].Start >= off }) - 1
	for i >= 0 && e.toks[i].Kind.IsTrivia() {
		i--
	}
	if i < 0 {
		return -1
	}
	return e.toks[i].End
}

// lineIndent is the whitespace that starts the line holding off.
func (e *emitter) lineIndent(off int) string {
	start := bytes.LastIndexByte(e.src[:off], '\n') + 1
	end := start
	for end < off && (e.src[end] == ' ' || e.src[end] == '\t') {
		end++
	}
	return string(e.src[start:end])
}

// throwsTag is the docblock tag a `throws A|B` clause compiles to, the
// names as written (a docblock resolves them through the file's imports,
// as the signature does); "" without a clause.
func (e *emitter) throwsTag(t *ast.Throws) string {
	if t == nil {
		return ""
	}
	parts := make([]string, len(t.Types))
	for i, ty := range t.Types {
		parts[i] = string(e.src[ty.S.Start:ty.S.End])
	}
	return "@throws " + strings.Join(parts, "|")
}

// lowerThrows takes a `throws` clause out of the signature and writes its
// @throws tag (design/functions.md §4.2), unless the generics lowering
// already wrote it into the docblock it generated for the declaration.
func (e *emitter) lowerThrows(decl ast.Node, declStart, nameStart int, doc string, t *ast.Throws) {
	if t == nil {
		return
	}
	e.removeThrows(t, e.lineIndent(nameStart))
	if !e.throwsWritten[decl] {
		e.mergeThrows(declStart, nameStart, doc, e.throwsTag(t))
	}
}

// removeThrows deletes a `throws` clause, keeping its line breaks. The
// space before the keyword goes with it (`): T throws X {` → `): T {`).
// A clause that opens a line hands its place to what follows on the line —
// the body's brace or arrow, which then stands at the declaration's
// indentation (indent) as lowerExprBody puts it — or leaves the line empty.
func (e *emitter) removeThrows(t *ast.Throws, indent string) {
	src := e.src
	start, end := t.Kw.Start, t.S.End
	i := start
	for i > 0 && (src[i-1] == ' ' || src[i-1] == '\t') {
		i--
	}
	text := ""
	switch {
	case i > 0 && src[i-1] != '\n' && src[i-1] != '\r':
		start = i
	default:
		j := end
		for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
			j++
		}
		start = i
		if j < len(src) && src[j] != '\n' && src[j] != '\r' {
			end = j
			text = indent
		}
	}
	e.replace(ast.Span{Start: start, End: end}, strings.Repeat("\n", bytes.Count(src[start:end], []byte("\n")))+text)
}

// mergeThrows writes a @throws tag into a declaration's docblock without
// adding a line:
//
//   - no docblock: `/** @throws A|B */ ` at the start of the declaration,
//     before its attributes and modifiers, where PHP, PHPStan and PhpStorm
//     attach it as they would on the line above;
//   - a docblock whose `*/` stands on a line of its own: the tag takes that
//     line, ` * @throws A|B */`;
//   - otherwise (a one-line docblock, or content beside the `*/`): the `*/`
//     moves to the next line with the tag, ahead of what stands there —
//     `/** Summary.` over ` * @throws A|B */ public function f()` — or onto
//     a blank line between the docblock and the declaration.
//
// A docblock on the declaration's own line has no next line to move to,
// and gains one: the single exception, recorded beside the generic
// docblocks' (design/functions.md, decisions made while building D18).
// One tag per line throughout, which is all PHPStan reads.
func (e *emitter) mergeThrows(declStart, nameStart int, doc, tag string) {
	if doc == "" {
		e.insert(declStart, "/** "+tag+" */ ")
		return
	}
	idx := bytes.LastIndex(e.src[:nameStart], []byte(doc))
	if idx < 0 || idx >= declStart {
		// A docblock between the attributes and the declaration is not the
		// one PHPStan reads (it reads the comment before the attributes):
		// the tag gets a docblock of its own there.
		e.insert(declStart, "/** "+tag+" */ ")
		return
	}
	if len(missingTags(doc, []string{tag})) == 0 {
		return // the docblock states it already
	}
	src := e.src
	end := idx + len(doc)
	closeAt := end - len("*/")
	lineStart := bytes.LastIndexByte(src[:closeAt], '\n') + 1
	if lineStart > idx && len(bytes.TrimSpace(src[lineStart:closeAt])) == 0 {
		e.replace(ast.Span{Start: closeAt, End: end}, "* "+tag+" */")
		return
	}
	// The `*/` and the spaces before it move to the next line.
	from := closeAt
	for from > idx+len("/**") && (src[from-1] == ' ' || src[from-1] == '\t') {
		from--
	}
	docLine := bytes.LastIndexByte(src[:idx], '\n') + 1
	indent := string(src[docLine:idx])
	if strings.TrimSpace(indent) != "" {
		indent = strings.Repeat(" ", len(indent)) // code before the docblock on its line
	}
	tagLine := "\n" + indent + " * " + tag + " */"
	next := end
	for next < len(src) && (src[next] == ' ' || src[next] == '\t' || src[next] == '\n' || src[next] == '\r') {
		next++
	}
	gap := string(src[end:next])
	switch strings.Count(gap, "\n") {
	case 0:
		e.replaceLines(ast.Span{Start: from, End: end}, tagLine)
	case 1:
		e.replace(ast.Span{Start: from, End: next}, tagLine+" ")
	default:
		// A blank line below the docblock takes the tag.
		lines := strings.SplitAfter(gap, "\n")
		e.replace(ast.Span{Start: from, End: next}, tagLine+"\n"+strings.Join(lines[2:], ""))
	}
}

// methodHead rewrites `[modifiers] fun` as `[modifiers] function` in PSR-12
// order with `public` written out. A head broken over lines or holding a
// comment keeps its modifiers as written and has only the keyword replaced,
// so the line count holds and nothing the programmer wrote is lost.
func (e *emitter) methodHead(n *ast.MethodDecl) {
	head := e.src[n.Head.Start:n.Head.End]
	if bytes.ContainsAny(head, "\n\r/#") {
		kw := ast.Span{Start: n.Head.End - len("fun"), End: n.Head.End}
		if n.Modifiers&(ast.ModPublic|ast.ModProtected|ast.ModPrivate) == 0 {
			e.replace(kw, "public function")
		} else {
			e.replace(kw, "function")
		}
		return
	}
	e.replace(n.Head, methodModifiers(n.Modifiers)+"function")
}

// methodModifiers renders a method's modifiers in PSR-12 order, `public`
// written out, each followed by a space.
func methodModifiers(m ast.Modifier) string {
	var parts []string
	if m&ast.ModAbstract != 0 {
		parts = append(parts, "abstract")
	}
	if m&ast.ModFinal != 0 {
		parts = append(parts, "final")
	}
	switch {
	case m&ast.ModProtected != 0:
		parts = append(parts, "protected")
	case m&ast.ModPrivate != 0:
		parts = append(parts, "private")
	default:
		parts = append(parts, "public")
	}
	if m&ast.ModStatic != 0 {
		parts = append(parts, "static")
	}
	return strings.Join(parts, " ") + " "
}

// impliedPublic writes the `public` a `private(set)` property or promoted
// parameter leaves implied (PHP 8.4 allows the omission; PSR-12 tooling does
// not): `private(set) int $x` → `public private(set) int $x`.
func (e *emitter) impliedPublic(m ast.Modifier, mods ast.Span) {
	if m&(ast.ModPublicSet|ast.ModProtectedSet|ast.ModPrivateSet) == 0 || m&(ast.ModPublic|ast.ModProtected|ast.ModPrivate) != 0 {
		return
	}
	e.insert(mods.Start, "public ")
}

// constClosures rewrites the closures of a constant expression — an
// attribute argument, a constant, a default — as the `static function`
// closures PHP accepts there: `fn (…) => expr` becomes
// `static function (…) { return expr; }` and a `function` closure gets
// `static`. The closures' own bodies are ordinary code and are left alone.
func (e *emitter) constClosures(root ast.Expr) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ArrowFunc:
			kw := "static function"
			if n.Static {
				kw = "function"
			}
			e.replace(n.Kw, kw)
			e.replace(n.Arrow, "{ return")
			e.insert(n.Body.Span().End, "; }")
			return false
		case *ast.Closure:
			if !n.Static {
				e.insert(n.Kw.Start, "static ")
			}
			return false
		case *ast.ClassDecl:
			return false
		}
		return true
	})
}
