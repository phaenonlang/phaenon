package emit

import (
	"bytes"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
)

// lowerFunctions compiles the syntax of design/functions.md, none of which
// needs type information: a `fun` head becomes the PER `function` head
// with `public` written out, a class constant or a property or promoted
// parameter with an asymmetric visibility and no main one gets its implied
// `public`, a closure in a constant expression becomes the `static
// function` PHP 8.5 accepts there, an expression body becomes a PER
// block and a `throws` clause a @throws tag. The opening braces of named
// functions, methods and class-likes go where PER Coding Style puts them
// (layout.go).
// It runs after the generics lowering, so that a docblock that pass inserts
// at a declaration's start precedes the rewritten head, and so that the
// @throws tag joins that docblock when there is one.
func (e *emitter) lowerFunctions(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FunctionDecl:
			if n.Fun {
				e.replace(n.Head, "function")
			}
			e.throwsDoc(n, n.S.Start, n.Name.S.Start, n.Doc, n.Throws)
			e.layoutFunction(function{kwAt: n.Head.Start, name: n.Name, tps: n.TypeParams, ret: n.ReturnType, throws: n.Throws,
				arrow: n.Arrow, body: n.Body, hasBody: true, end: n.S.End})
		case *ast.MethodDecl:
			if n.Fun {
				e.methodHead(n)
			}
			e.throwsDoc(n, n.S.Start, n.Name.S.Start, n.Doc, n.Throws)
			e.layoutFunction(function{kwAt: n.Head.End - 1, name: n.Name, tps: n.TypeParams,
				ret: n.ReturnType, throws: n.Throws, arrow: n.Arrow, body: n.Body, hasBody: n.HasBody, end: n.S.End})
		case *ast.ClassDecl:
			if n.Name != nil {
				e.classBrace(n)
			}
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

// throwsDoc writes the @throws tag of a `throws` clause (design/functions.md
// §4.2), unless the generics lowering already wrote it into the docblock
// it generated for the declaration. The clause itself leaves the signature
// with the body's layout (layoutFunction).
func (e *emitter) throwsDoc(decl ast.Node, declStart, nameStart int, doc string, t *ast.Throws) {
	if t != nil && !e.throwsWritten[decl] {
		e.mergeThrows(declStart, nameStart, doc, e.throwsTag(t))
	}
}

// removeThrows deletes a `throws` clause in place, keeping its line breaks:
// the fallback when a comment between the signature and the body keeps
// the layout as written. The space before the keyword goes with it (`): T
// throws X {` → `): T {`). A clause that opens a line hands its place to
// what follows on the line, which then stands at the declaration's
// indentation (indent), or leaves the line empty.
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

// docBlock renders a multi-line docblock at indent: the content of a
// one-line docblock it replaces (if any) and then the tags, one per line,
// without a line break after the closing `*/`.
func docBlock(indent, content string, tags []string) string {
	var b strings.Builder
	b.WriteString("/**\n")
	if content != "" {
		b.WriteString(indent + " * " + content + "\n")
	}
	for _, t := range tags {
		b.WriteString(indent + " * " + t + "\n")
	}
	b.WriteString(indent + " */")
	return b.String()
}

// rewriteDoc replaces the one-line docblock doc at idx with a multi-line
// block of its content and the tags. A declaration that stood on the
// docblock's line moves to the line after it.
func (e *emitter) rewriteDoc(idx int, doc, content string, tags []string) {
	indent := e.outIndent(idx)
	end := idx + len(doc)
	next := end
	for next < len(e.src) && (e.src[next] == ' ' || e.src[next] == '\t') {
		next++
	}
	text := docBlock(indent, content, tags)
	if next < len(e.src) && e.src[next] != '\n' && e.src[next] != '\r' {
		e.replaceFree(ast.Span{Start: idx, End: next}, text+"\n"+indent)
		return
	}
	e.replaceFree(ast.Span{Start: idx, End: end}, text)
}

// oneLineContent is what a one-line docblock says, without its delimiters.
func oneLineContent(doc string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(doc, "/**"), "*/"))
}

// mergeThrows writes a @throws tag into a declaration's docblock, one tag
// per line, which is all PHPStan reads:
//
//   - no docblock: a new one on lines of its own before the declaration and
//     its attributes, where PHP, PHPStan and PhpStorm attach it;
//   - a docblock whose `*/` stands on a line of its own: the tag takes a
//     new line before it;
//   - a multi-line docblock with content beside its `*/`: the `*/` moves to
//     a line of its own after the tag's;
//   - a one-line docblock: it becomes a multi-line block, and a
//     declaration on its line moves below it.
func (e *emitter) mergeThrows(declStart, nameStart int, doc, tag string) {
	indent := e.outIndent(declStart)
	newDoc := func() {
		e.insertFree(declStart, docBlock(indent, "", []string{tag})+"\n"+indent)
	}
	if doc == "" {
		newDoc()
		return
	}
	idx := bytes.LastIndex(e.src[:nameStart], []byte(doc))
	if idx < 0 || idx >= declStart {
		// A docblock between the attributes and the declaration is not the
		// one PHPStan reads (it reads the comment before the attributes):
		// the tag gets a docblock of its own there.
		newDoc()
		return
	}
	if len(missingTags(doc, []string{tag})) == 0 {
		return // the docblock states it already
	}
	if !strings.Contains(doc, "\n") {
		e.rewriteDoc(idx, doc, eraseBytesInDoc(oneLineContent(doc)), []string{tag})
		return
	}
	src := e.src
	end := idx + len(doc)
	closeAt := end - len("*/")
	lineStart := bytes.LastIndexByte(src[:closeAt], '\n') + 1
	if len(bytes.TrimSpace(src[lineStart:closeAt])) == 0 {
		e.insertFree(lineStart, string(src[lineStart:closeAt])+"* "+tag+"\n")
		return
	}
	// The `*/` and the spaces before it move to a line after the tag's.
	from := closeAt
	for from > idx+len("/**") && (src[from-1] == ' ' || src[from-1] == '\t') {
		from--
	}
	docIndent := e.outIndent(idx)
	text := "\n" + docIndent + " * " + tag + "\n" + docIndent + " */"
	next := end
	for next < len(src) && (src[next] == ' ' || src[next] == '\t') {
		next++
	}
	if next < len(src) && src[next] != '\n' && src[next] != '\r' {
		e.replaceFree(ast.Span{Start: from, End: next}, text+"\n"+docIndent)
		return
	}
	e.replaceFree(ast.Span{Start: from, End: end}, text)
}

// methodHead rewrites `[modifiers] fun` as `[modifiers] function` in PER
// order with `public` written out. A head broken over lines or holding a
// comment keeps its modifiers as written and has only the keyword replaced,
// so nothing the programmer wrote is lost. Modifiers already written so
// stay the source's text and only the keyword is the compiler's, so that a
// tool's change of a modifier is carried back (phn fix).
func (e *emitter) methodHead(n *ast.MethodDecl) {
	head := e.src[n.Head.Start:n.Head.End]
	kw := ast.Span{Start: n.Head.End - len("fun"), End: n.Head.End}
	if bytes.ContainsAny(head, "\n\r/#") {
		if n.Modifiers&(ast.ModPublic|ast.ModProtected|ast.ModPrivate) == 0 {
			e.replace(kw, "public function")
		} else {
			e.replace(kw, "function")
		}
		return
	}
	mods := string(head[:len(head)-len("fun")])
	canonical := methodModifiers(n.Modifiers)
	if mods == canonical {
		e.replace(kw, "function")
		return
	}
	e.replace(n.Head, canonical+"function")
}

// methodModifiers renders a method's modifiers in PER order (§4.6), `public`
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
// parameter leaves implied (PHP 8.4 allows the omission; php-cs-fixer's
// modifier_keywords does not): `private(set) int $x` → `public private(set) int $x`.
func (e *emitter) impliedPublic(m ast.Modifier, mods ast.Span) {
	if m&(ast.ModPublicSet|ast.ModProtectedSet|ast.ModPrivateSet) == 0 || m&(ast.ModPublic|ast.ModProtected|ast.ModPrivate) != 0 {
		return
	}
	e.insert(mods.Start, "public ")
}

// constClosures rewrites the closures of a constant expression — an
// attribute argument, a constant, a default — as the `static function`
// closures PHP accepts there: `fn(…) => expr` becomes `static function
// (…) {` with `return expr;` on a line of its own (closureBody), and a
// `function` closure gets `static`. The closures' own bodies are ordinary
// code and are left alone; a closure keeps its brace on its line, as PER
// has it.
func (e *emitter) constClosures(root ast.Expr) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ArrowFunc:
			kw := "static function"
			if n.Static {
				kw = "function"
			}
			if n.Kw.End < len(e.src) && e.src[n.Kw.End] == '(' {
				kw += " " // PER: `fn(`, but `function (`
			}
			e.replace(n.Kw, kw)
			e.closureBody(n)
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

// closureBody writes the body of an arrow function lowered to a closure as
// the block PER gives a closure (§7): the brace on the closure's line, the
// statement one level in on a line of its own, the closing brace at the
// indentation of the closure's line. A multi-line body moves in with it; a
// comment after the arrow keeps the one-line form, `{ return expr; }`.
func (e *emitter) closureBody(n *ast.ArrowFunc) {
	toks := e.tokens()
	first := e.nextCode(e.tokenAt(n.Arrow.End))
	end := n.Body.Span().End
	if first >= len(toks) || e.hasComment(n.Arrow.End, toks[first].Start) {
		e.replace(n.Arrow, "{ return")
		e.insert(end, "; }")
		return
	}
	indent := e.outIndent(n.Kw.Start)
	level := "    "
	if strings.Contains(indent, "\t") {
		level = "\t"
	}
	bodyStart := toks[first].Start
	e.replaceFree(ast.Span{Start: n.Arrow.Start, End: bodyStart}, "{\n"+indent+level+"return ")
	e.insertFree(end, ";\n"+indent+"}")
	if e.lineOf(bodyStart) != e.lineOf(end) && !e.multiLineToken(bodyStart, end) {
		e.indents = append(e.indents, indentReq{Span: ast.Span{Start: bodyStart, End: end}, Text: level})
	}
}
