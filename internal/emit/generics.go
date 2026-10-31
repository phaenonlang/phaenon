package emit

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// This file lowers Phaenon's generics to PHP: type parameters and
// arguments are erased from the native signatures and restated in
// PHPStan docblocks; `is` / `as` become native checks where PHP has them
// and \Phaenon\Type checks elsewhere; `type` aliases are expanded. The
// output contains no identifier the programmer did not write (roadmap D9):
// a type parameter has no runtime value, a `class<T>` parameter does. See
// design/generics.md §4 and §11.2.

// RuntimeType is the fully qualified runtime descriptor class.
const RuntimeType = "\\Phaenon\\Type"

type lowering struct {
	e *emitter
	p *program.Program
	f *program.File
	// bare holds the expressions that are a whole statement, return value
	// or assigned value: a guarded stdlib call there needs no parentheses.
	bare map[ast.Expr]bool
}

// scope is what a body needs to lower its expressions.
type scope struct {
	res    *program.Resolver
	class  *program.Class
	static bool
	// params are the type parameters visible by name, with their syntax
	// (for textual erasure of bounds).
	params map[string]*ast.TypeParam
}

func (s *scope) child() *scope {
	c := &scope{res: s.res, class: s.class, static: s.static, params: map[string]*ast.TypeParam{}}
	for k, v := range s.params {
		c.params[k] = v
	}
	return c
}

// hasGenerics reports whether a file uses any Phaenon-only type syntax.
func hasGenerics(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		switch n := n.(type) {
		case *ast.TypeParam, *ast.TypeAlias, *ast.IsExpr, *ast.AsExpr, *ast.ClosureType, *ast.TupleType:
			found = true
		case *ast.Cast:
			found = found || n.Op == token.T_BYTES_CAST
		case *ast.NamedType:
			if len(n.TypeArgs) > 0 || isAliasCandidate(n) {
				found = true
			}
		case *ast.Call:
			found = found || len(n.TypeArgs) > 0
		case *ast.MethodCall:
			found = found || len(n.TypeArgs) > 0
		case *ast.StaticCall:
			found = found || len(n.TypeArgs) > 0
		case *ast.New:
			found = found || len(n.TypeArgs) > 0
		}
		return !found
	})
	return found
}

// isAliasCandidate: a bare `list` / `map` is Phaenon syntax even without
// arguments (PHP has no such types); aliases are found by the lowering.
func isAliasCandidate(n *ast.NamedType) bool {
	if len(n.Name.Parts) != 1 {
		return false
	}
	switch strings.ToLower(n.Name.Parts[0]) {
	case "list", "map", "class":
		return true
	}
	return false
}

func (e *emitter) lowerGenerics(f *ast.File) error {
	if !hasGenerics(f) && e.opts.File == nil {
		return nil
	}
	if e.opts.Program == nil || e.opts.File == nil {
		if hasGenerics(f) {
			return &Error{File: e.opts.SourceName, Line: 1, Msg: "Phaenon syntax needs type information: compile with phn build"}
		}
		return nil
	}
	l := &lowering{e: e, p: e.opts.Program, f: e.opts.File, bare: map[ast.Expr]bool{}}
	return l.stmts(f.Stmts)
}

func (l *lowering) errorf(n ast.Node, format string, args ...any) error {
	return l.e.errorf(n, format, args...)
}

// topScope is the scope of a position outside any declaration.
func (l *lowering) topScope(off int) *scope {
	return &scope{res: l.p.NewResolver(l.f, l.f.ScopeAt(off), nil, ast.Span{}), params: map[string]*ast.TypeParam{}}
}

func (l *lowering) stmts(list []ast.Stmt) error {
	for _, s := range list {
		if err := l.stmt(s); err != nil {
			return err
		}
	}
	return nil
}

func (l *lowering) stmt(s ast.Stmt) error {
	switch s := s.(type) {
	case *ast.Namespace:
		if s.Braced {
			return l.stmts(s.Body)
		}
		return nil
	case *ast.TypeAlias:
		l.blank(s.S)
		return nil
	case *ast.ClassDecl:
		return l.classDecl(s)
	case *ast.FunctionDecl:
		return l.funcDecl(s)
	case *ast.Block:
		return l.stmts(s.Stmts)
	}
	return l.walk(s, l.topScope(s.Span().Start))
}

// blank removes a statement, keeping its line breaks.
func (l *lowering) blank(s ast.Span) {
	l.e.replace(s, strings.Repeat("\n", bytes.Count(l.e.src[s.Start:s.End], []byte("\n"))))
}

// ------------------------------------------------------------------ text helpers

func (l *lowering) text(s ast.Span) string { return string(l.e.src[s.Start:s.End]) }

// indentAt is the whitespace at the start of the line holding off.
func (l *lowering) indentAt(off int) string {
	src := l.e.src
	start := bytes.LastIndexByte(src[:off], '\n') + 1
	end := start
	for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	return string(src[start:end])
}

// find returns the offset of the first occurrence of b at or after from,
// skipping comments and strings is not needed for the delimiters we look
// for (they follow a token directly).
func (l *lowering) find(from int, b byte) int {
	i := bytes.IndexByte(l.e.src[from:], b)
	if i < 0 {
		return -1
	}
	return from + i
}

// angleSpan is the span of a `<…>` list that follows offset after: from
// the `<` to just past the closing `>`.
func (l *lowering) angleSpan(after, lastEnd int) ast.Span {
	open := l.find(after, '<')
	close := lastEnd
	for close < len(l.e.src) && l.e.src[close] != '>' {
		close++
	}
	return ast.Span{Start: open, End: close + 1}
}

// removeTypeParams deletes a `<T, U>` list.
func (l *lowering) removeTypeParams(after int, tps []*ast.TypeParam) {
	if len(tps) == 0 {
		return
	}
	s := l.angleSpan(after, tps[len(tps)-1].S.End)
	// Keep the whitespace before `<` out of the deletion so `function f <T>(`
	// does not become `function f (`.
	l.e.replace(s, strings.Repeat("\n", bytes.Count(l.e.src[s.Start:s.End], []byte("\n"))))
}

// removeTypeArgs deletes the `<…>` of a named type.
func (l *lowering) removeTypeArgs(t *ast.NamedType) {
	if len(t.TypeArgs) == 0 {
		return
	}
	s := ast.Span{Start: t.Name.S.End, End: t.S.End}
	l.e.replace(s, strings.Repeat("\n", bytes.Count(l.e.src[s.Start:s.End], []byte("\n"))))
}

// ------------------------------------------------------------------ types: erasure and docblock text

// erase returns the native PHP spelling of a Phaenon type.
func (l *lowering) erase(t ast.TypeExpr, sc *scope) string {
	switch t := t.(type) {
	case nil:
		return ""
	case *ast.NamedType:
		if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified {
			name := t.Name.Parts[0]
			if tp, ok := sc.params[name]; ok {
				if tp.Bound == nil {
					return "mixed"
				}
				return l.erase(tp.Bound, sc)
			}
			if sc.class != nil {
				for _, tp := range sc.class.TypeParams {
					if tp.Name == name {
						return nativeOf(tp.Bound)
					}
				}
			}
			switch strings.ToLower(name) {
			case "list", "map", "array":
				return "array"
			case "class", "bytes":
				return "string"
			case "iterable":
				return "iterable"
			}
			if sc.res.FileAlias(name) != nil {
				// A file-level alias: the resolved type knows its native spelling.
				return nativeOf(sc.res.Native(t))
			}
		}
		return l.text(t.Name.S)
	case *ast.NullableType:
		inner := l.erase(t.Type, sc)
		switch {
		case inner == "mixed" || inner == "null":
			return "mixed"
		case strings.Contains(inner, "|"):
			return inner + "|null"
		}
		return "?" + inner
	case *ast.UnionType:
		var parts []string
		for _, m := range t.Types {
			parts = append(parts, l.erase(m, sc))
		}
		return joinUnion(parts)
	case *ast.IntersectionType:
		var parts []string
		for _, m := range t.Types {
			parts = append(parts, l.erase(m, sc))
		}
		s := strings.Join(parts, "&")
		if t.Parenthesised {
			return "(" + s + ")"
		}
		return s
	case *ast.ClosureType:
		return "\\Closure"
	case *ast.TupleType:
		return "array"
	}
	return "mixed"
}

// joinUnion merges erased members into one native union: nested unions
// flatten, `?X` splits, duplicates drop, mixed absorbs everything.
func joinUnion(parts []string) string {
	seen := map[string]bool{}
	var out []string
	var add func(p string)
	add = func(p string) {
		if strings.HasPrefix(p, "?") {
			add(p[1:])
			add("null")
			return
		}
		if strings.Contains(p, "|") && !strings.HasPrefix(p, "(") {
			for _, q := range strings.Split(p, "|") {
				add(q)
			}
			return
		}
		key := strings.ToLower(p)
		if !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	for _, p := range parts {
		add(p)
	}
	if seen["mixed"] {
		return "mixed"
	}
	if len(out) == 2 && seen["null"] && !strings.HasPrefix(out[0], "(") && !strings.HasPrefix(out[1], "(") {
		for _, o := range out {
			if strings.ToLower(o) != "null" {
				return "?" + o
			}
		}
	}
	return strings.Join(out, "|")
}

// nativeOf is the native spelling of a resolved type (mixed when it has
// none).
func nativeOf(t types.Type) string {
	if t == nil {
		return "mixed"
	}
	s, ok := types.Native(t)
	if !ok {
		return "mixed"
	}
	return s
}

// docType prints a Phaenon type in PHPStan vocabulary for a docblock.
func (l *lowering) docType(t ast.TypeExpr, sc *scope) string {
	switch t := t.(type) {
	case nil:
		return ""
	case *ast.NamedType:
		name := l.text(t.Name.S)
		if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified {
			if _, ok := sc.params[t.Name.Parts[0]]; !ok {
				switch strings.ToLower(t.Name.Parts[0]) {
				case "map":
					name = "array"
				case "bytes":
					name = "string" // PHPStan has no binary string (strings.md §5)
				case "class":
					// A bare `class` is any class name; PHPStan spells both class-string.
					name = "class-string"
				case "closure":
					name = "\\Closure"
				default:
					if a := sc.res.FileAlias(t.Name.Parts[0]); a != nil {
						return types.PrintQualified(sc.res.Native(t))
					}
				}
			}
		}
		if len(t.TypeArgs) == 0 {
			return name
		}
		var args []string
		for _, a := range t.TypeArgs {
			args = append(args, l.docType(a, sc))
		}
		return name + "<" + strings.Join(args, ", ") + ">"
	case *ast.NullableType:
		return "?" + l.docType(t.Type, sc)
	case *ast.UnionType:
		var parts []string
		for _, m := range t.Types {
			parts = append(parts, l.docType(m, sc))
		}
		return strings.Join(parts, "|")
	case *ast.IntersectionType:
		var parts []string
		for _, m := range t.Types {
			parts = append(parts, l.docType(m, sc))
		}
		s := strings.Join(parts, "&")
		if t.Parenthesised {
			return "(" + s + ")"
		}
		return s
	case *ast.TupleType:
		var parts []string
		for _, m := range t.Elems {
			parts = append(parts, l.docType(m, sc))
		}
		return "array{" + strings.Join(parts, ", ") + "}"
	case *ast.ClosureType:
		var parts []string
		for _, m := range t.Params {
			parts = append(parts, l.docType(m, sc))
		}
		s := "\\Closure(" + strings.Join(parts, ", ") + ")"
		if t.Return != nil {
			ret := l.docType(t.Return, sc)
			if _, union := t.Return.(*ast.UnionType); union {
				ret = "(" + ret + ")" // `Closure(U): (A|B)`: without the parentheses PHPStan reads `(Closure(U): A)|B`
			}
			s += ": " + ret
		}
		return s
	}
	return "mixed"
}

// needsDoc reports whether erasing t loses information a docblock must
// keep.
func (l *lowering) needsDoc(t ast.TypeExpr, sc *scope) bool {
	switch t := t.(type) {
	case nil:
		return false
	case *ast.NamedType:
		if len(t.TypeArgs) > 0 {
			return true
		}
		if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified {
			name := t.Name.Parts[0]
			if _, ok := sc.params[name]; ok {
				return true
			}
			if sc.class != nil {
				for _, tp := range sc.class.TypeParams {
					if tp.Name == name {
						return true
					}
				}
			}
			if isAliasCandidate(t) || sc.res.FileAlias(name) != nil || strings.Contains(name, "-") {
				return true
			}
		}
		return false
	case *ast.NullableType:
		return l.needsDoc(t.Type, sc)
	case *ast.UnionType:
		for _, m := range t.Types {
			if l.needsDoc(m, sc) {
				return true
			}
		}
		return false
	case *ast.IntersectionType:
		for _, m := range t.Types {
			if l.needsDoc(m, sc) {
				return true
			}
		}
		return false
	case *ast.ClosureType, *ast.TupleType:
		return true
	}
	return false
}

// eraseType rewrites a written type to its erasure, and reports whether a
// docblock is needed for it.
func (l *lowering) eraseType(t ast.TypeExpr, sc *scope, override string) bool {
	if t == nil {
		return false
	}
	doc := override != "" || l.needsDoc(t, sc)
	if !doc && !mentionsBytes(t) {
		return false // plain PHP stays byte for byte
	}
	erased := l.erase(t, sc)
	if override != "" {
		erased = override
	}
	if erased != l.text(t.Span()) {
		l.e.replace(t.Span(), erased)
	}
	return doc
}

// mentionsBytes reports a type that spells bytes: it erases to string
// without a docblock saying more (strings.md §5).
func mentionsBytes(t ast.TypeExpr) bool {
	found := false
	ast.Inspect(t, func(n ast.Node) bool {
		if nt, ok := n.(*ast.NamedType); ok && len(nt.Name.Parts) == 1 && strings.EqualFold(nt.Name.Parts[0], "bytes") {
			found = true
		}
		return !found
	})
	return found
}

// ------------------------------------------------------------------ docblocks

// addDoc gives the declaration at declStart (whose current doc comment
// text is doc) the given tags, merging into an existing docblock. Tags
// already present are not repeated.
func (l *lowering) addDoc(declStart int, doc string, tags []string) {
	if len(tags) == 0 {
		return
	}
	indent := l.indentAt(declStart)
	if doc == "" {
		if len(tags) == 1 {
			l.e.insertLines(declStart, "/** "+tags[0]+" */\n"+indent)
			return
		}
		var b strings.Builder
		b.WriteString("/**\n")
		for _, t := range tags {
			b.WriteString(indent + " * " + t + "\n")
		}
		b.WriteString(indent + " */\n" + indent)
		l.e.insertLines(declStart, b.String())
		return
	}
	idx := bytes.LastIndex(l.e.src[:declStart], []byte(doc))
	if idx < 0 {
		return
	}
	tags = l.fillTypes(doc, idx, tags)
	tags = l.replaceVar(doc, idx, tags)
	tags = missingTags(doc, tags)
	if len(tags) == 0 {
		return
	}
	span := ast.Span{Start: idx, End: idx + len(doc)}
	indent = l.indentAt(idx)
	if !strings.Contains(doc, "\n") {
		content := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(doc, "/**"), "*/"))
		if len(tags) == 1 && strings.HasPrefix(tags[0], "@var ") {
			// A property's one-line `/** what it holds */` keeps its line:
			// `/** @var list<string> what it holds */` is the PHP it came from.
			text := "/** " + tags[0]
			if content != "" {
				text += " " + content
			}
			l.e.replace(span, text+" */")
			return
		}
		// `/** Summary. */` becomes a multi-line block.
		var b strings.Builder
		b.WriteString("/**\n")
		if content != "" {
			b.WriteString(indent + " * " + content + "\n")
		}
		for _, t := range tags {
			b.WriteString(indent + " * " + t + "\n")
		}
		b.WriteString(indent + " */")
		l.e.replaceLines(span, b.String())
		return
	}
	lastNL := strings.LastIndexByte(doc, '\n')
	closeIndent := indent
	if ws := strings.TrimRight(doc[lastNL+1:], "*/ "); ws != "" {
		closeIndent = ws
	}
	// The generated tags go before the first tag already there, so that
	// `@throws`, `@see` and the like keep their place after the signature
	// tags; with no tags they go at the end, after the prose.
	at := idx + lastNL + 1
	off := 0
	for _, line := range strings.SplitAfter(doc, "\n") {
		if strings.HasPrefix(strings.TrimLeft(strings.TrimSpace(line), "/* "), "@") {
			at = idx + off
			break
		}
		off += len(line)
	}
	var b strings.Builder
	for _, t := range tags {
		b.WriteString(closeIndent + " * " + t + "\n")
	}
	l.e.insertLines(at, b.String())
}

// fillTypes puts the type back into `@param $x description` lines, which
// Phaenon source writes without one (the type is in the signature), and
// returns the tags that found no such line.
func (l *lowering) fillTypes(doc string, idx int, tags []string) []string {
	var rest []string
	for _, tag := range tags {
		fields := strings.Fields(tag)
		if len(fields) < 3 || fields[0] != "@param" {
			rest = append(rest, tag)
			continue
		}
		key := tagKey(tag)
		off := 0
		found := false
		for _, line := range strings.SplitAfter(doc, "\n") {
			text := strings.TrimLeft(strings.TrimSpace(line), "/* ")
			if strings.HasPrefix(text, "@param") && tagKey(text) == key {
				lf := strings.Fields(text)
				if len(lf) >= 2 && strings.HasPrefix(strings.TrimLeft(lf[1], "&."), "$") {
					at := idx + off + strings.Index(line, "@param") + len("@param")
					l.e.replace(ast.Span{Start: at, End: at}, " "+strings.Join(fields[1:len(fields)-1], " "))
					// `@param $x - prose` spells a typeless description; with
					// the type back, the dash goes: `@param T $x prose`.
					afterVar := strings.Index(line, lf[1]) + len(lf[1])
					if dash := afterVar + len(line[afterVar:]) - len(strings.TrimLeft(line[afterVar:], " \t")); strings.HasPrefix(line[dash:], "- ") || strings.TrimRight(line[dash:], " \t\r\n") == "-" {
						l.e.replace(ast.Span{Start: idx + off + afterVar, End: idx + off + dash + 1}, "")
					}
					found = true
				}
				break
			}
			off += len(line)
		}
		if !found {
			rest = append(rest, tag)
		}
	}
	return rest
}

// replaceVar rewrites the type of a `@var` the docblock already has with the
// one the declaration's type arguments say: `private \ArrayObject<int,
// Metric> $exported` is the type, a `@var \ArrayObject<int, mixed>` left
// above it by an earlier edit is not (the checker reads the syntax too).
func (l *lowering) replaceVar(doc string, idx int, tags []string) []string {
	var rest []string
	for _, tag := range tags {
		fields := strings.Fields(tag)
		if len(fields) < 2 || fields[0] != "@var" {
			rest = append(rest, tag)
			continue
		}
		off := 0
		found := false
		for _, line := range strings.SplitAfter(doc, "\n") {
			text := strings.TrimLeft(strings.TrimSpace(line), "/* ")
			if strings.HasPrefix(text, "@var") {
				// The type runs from the first word after @var to the
				// variable name or the end of the line (`*/` excluded); it may
				// hold spaces of its own (`array<int, string>`).
				tagAt := strings.Index(line, "@var")
				after := line[tagAt+len("@var"):]
				typeStart := len(after) - len(strings.TrimLeft(after, " \t"))
				rest := strings.TrimRight(strings.TrimSuffix(strings.TrimRight(after[typeStart:], " \t\r\n"), "*/"), " \t")
				if i := strings.Index(rest, " $"); i >= 0 {
					rest = rest[:i]
				}
				if rest != "" && !strings.HasPrefix(rest, "$") {
					typeAt := idx + off + tagAt + len("@var") + typeStart
					l.e.replace(ast.Span{Start: typeAt, End: typeAt + len(rest)}, strings.Join(fields[1:], " "))
					found = true
				}
				break
			}
			off += len(line)
		}
		if !found {
			rest = append(rest, tag)
		}
	}
	return rest
}

// missingTags drops the tags a docblock already states (same tag and, for
// @param / @template, the same name).
func missingTags(doc string, tags []string) []string {
	have := map[string]bool{}
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimLeft(strings.TrimSpace(line), "/* ")
		if strings.HasPrefix(line, "@") {
			have[tagKey(line)] = true
		}
	}
	var out []string
	for _, t := range tags {
		if !have[tagKey(t)] {
			out = append(out, t)
		}
	}
	return out
}

// tagKey is the part of a tag that identifies it: `@return`, `@param … $x`
// → "$x", `@template T` → "@template T".
func tagKey(tag string) string {
	fields := strings.Fields(tag)
	if len(fields) == 0 {
		return ""
	}
	switch fields[0] {
	case "@param", "@phpstan-param", "@psalm-param":
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "$") || strings.HasPrefix(f, "...$") || strings.HasPrefix(f, "&$") {
				return "@param " + strings.TrimLeft(f, ".&")
			}
		}
		return "@param"
	case "@template", "@template-covariant", "@template-contravariant", "@phpstan-template":
		if len(fields) > 1 {
			return "@template " + fields[1]
		}
	case "@return", "@phpstan-return", "@psalm-return":
		return "@return"
	case "@var", "@phpstan-var", "@psalm-var":
		return "@var"
	case "@extends", "@implements", "@phpstan-extends", "@phpstan-implements", "@template-extends", "@template-implements":
		if len(fields) > 1 {
			name := fields[1]
			if i := strings.IndexByte(name, '<'); i >= 0 {
				name = name[:i]
			}
			return strings.TrimPrefix(fields[0], "@phpstan-") + " " + name
		}
	}
	return fields[0]
}

func (l *lowering) templateTags(tps []*ast.TypeParam, sc *scope) []string {
	var tags []string
	for _, tp := range tps {
		tag := "@template"
		switch tp.Variance {
		case ast.Covariant:
			tag = "@template-covariant"
		case ast.Contravariant:
			tag = "@template-contravariant"
		}
		tag += " " + tp.Name.Name
		if tp.Bound != nil {
			tag += " of " + l.docType(tp.Bound, sc)
		}
		if tp.Default != nil {
			tag += " = " + l.docType(tp.Default, sc)
		}
		tags = append(tags, tag)
	}
	return tags
}

// ------------------------------------------------------------------ signatures

// signature erases the parameters and return type of a function-like
// declaration and returns the docblock tags that keep their meaning.
// overrides gives, per parameter index and for the return (key -1), an
// erased type to use instead (the override-erasure rule).
func (l *lowering) signature(params []*ast.Param, ret ast.TypeExpr, sc *scope, overrides map[int]string) []string {
	var tags []string
	for i, p := range params {
		if l.eraseType(p.Type, sc, overrides[i]) {
			name := "$" + p.Var.Name
			if p.Variadic {
				name = "..." + name
			}
			tags = append(tags, "@param "+l.docType(p.Type, sc)+" "+name)
		}
	}
	if l.eraseType(ret, sc, overrides[-1]) {
		tags = append(tags, "@return "+l.docType(ret, sc))
	}
	return tags
}

// paramsOpen is the offset of the `(` opening a parameter list declared
// after offset after.
func (l *lowering) paramsOpen(after int) int { return l.find(after, '(') }

// bodyOpen is the offset of the `{` opening a body after offset after.
func (l *lowering) bodyOpen(after int) int { return l.find(after, '{') }

// ------------------------------------------------------------------ functions

func (l *lowering) funcDecl(d *ast.FunctionDecl) error {
	sc := l.topScope(d.S.Start)
	fn := l.p.Function(sc.res.Scope().Prefix() + d.Name.Name)
	if fn != nil {
		fn.Signature()
		sc.res = sc.res.WithParams(fn.TypeParams)
	}
	for _, tp := range d.TypeParams {
		sc.params[tp.Name.Name] = tp
	}
	tags := l.templateTags(d.TypeParams, sc)
	l.removeTypeParams(d.Name.S.End, d.TypeParams)
	tags = append(tags, l.signature(d.Params, d.ReturnType, sc, nil)...)
	l.addDoc(d.S.Start, d.Doc, tags)
	return l.walkBody(d.Body, sc)
}

// ------------------------------------------------------------------ classes

func (l *lowering) classDecl(d *ast.ClassDecl) error {
	sc := l.topScope(d.S.Start)
	var cl *program.Class
	if d.Name != nil {
		cl = l.p.Class(sc.res.Scope().Prefix() + d.Name.Name)
	}
	if cl == nil {
		return l.errorf(d, "class not found in the program (internal error)")
	}
	cl.Signature()
	sc.class = cl
	sc.res = l.p.NewResolver(l.f, cl.Scope, cl, d.S)
	for _, tp := range d.TypeParams {
		sc.params[tp.Name.Name] = tp
	}
	// Header: templates, extends / implements arguments.
	tags := l.templateTags(d.TypeParams, sc)
	if d.Name != nil {
		l.removeTypeParams(d.Name.S.End, d.TypeParams)
	}
	for i, e := range d.Extends {
		if len(e.TypeArgs) > 0 {
			tag := "@extends"
			if d.Kind == ast.KindInterface && i >= 0 {
				tag = "@extends"
			}
			tags = append(tags, tag+" "+l.docType(e, sc))
			l.removeTypeArgs(e)
		}
	}
	for _, e := range d.Implements {
		if len(e.TypeArgs) > 0 {
			tags = append(tags, "@implements "+l.docType(e, sc))
			l.removeTypeArgs(e)
		}
	}
	l.addDoc(d.S.Start, d.Doc, tags)

	for _, m := range d.Members {
		switch m := m.(type) {
		case *ast.MethodDecl:
			if err := l.methodDecl(cl, m, sc); err != nil {
				return err
			}
		case *ast.PropertyDecl:
			psc := sc.child()
			if l.eraseType(m.Type, psc, "") {
				l.addDoc(m.S.Start, m.Doc, []string{"@var " + l.docType(m.Type, psc)})
			}
			for _, it := range m.Props {
				if it.Default != nil {
					if err := l.walk(it.Default, psc); err != nil {
						return err
					}
				}
			}
			for _, h := range m.Hooks {
				if err := l.walk(h, psc); err != nil {
					return err
				}
			}
		case *ast.ClassConstDecl:
			if err := l.walk(m, sc.child()); err != nil {
				return err
			}
		case *ast.EnumCase:
			if err := l.walk(m, sc.child()); err != nil {
				return err
			}
		}
	}
	return nil
}

// ------------------------------------------------------------------ methods

func (l *lowering) methodDecl(cl *program.Class, d *ast.MethodDecl, csc *scope) error {
	sc := csc.child()
	sc.static = d.Modifiers&ast.ModStatic != 0
	m := cl.FindMethod(d.Name.Name)
	if m != nil && m.Class == cl {
		sc.res = sc.res.WithParams(m.TypeParams)
	}
	for _, tp := range d.TypeParams {
		sc.params[tp.Name.Name] = tp
	}
	tags := l.templateTags(d.TypeParams, sc)
	l.removeTypeParams(d.Name.S.End, d.TypeParams)
	var overrides map[int]string
	if m != nil && m.Class == cl {
		overrides = l.overrideErasure(cl, m)
	}
	tags = append(tags, l.signature(d.Params, d.ReturnType, sc, overrides)...)
	l.addDoc(d.S.Start, d.Doc, tags)
	if !d.HasBody {
		return nil
	}
	return l.walkBody(d.Body, sc)
}

// overrideErasure applies the override-erasure rule: a method that
// overrides keeps its parameters' erased types at least as wide as the
// parent's, and its return type no wider.
func (l *lowering) overrideErasure(cl *program.Class, m *program.Method) map[int]string {
	var parent *program.Method
	key := strings.ToLower(m.Name)
	for _, a := range cl.Ancestors() {
		if a == cl {
			continue
		}
		a.Signature()
		if pm := a.FindMethod(key); pm != nil && pm.Class != cl {
			if pm.Vis == program.Private && pm.Class.Kind != ast.KindInterface {
				continue
			}
			parent = pm
			break
		}
	}
	if parent == nil {
		return nil
	}
	if strings.EqualFold(m.Name, "__construct") && !parent.Abstract && parent.Class.Kind != ast.KindInterface {
		return nil
	}
	out := map[int]string{}
	for i, pp := range parent.Params {
		if i >= len(m.Params) {
			break
		}
		pe, ce := erasedOrMixed(pp.NativeType), erasedOrMixed(m.Params[i].NativeType)
		if !types.IsSubtype(pe, ce, l.p) {
			if s, ok := types.Native(pe); ok {
				out[i] = s
			}
		}
	}
	if parent.NativeReturn != nil && m.NativeReturn != nil {
		pe, ce := types.Erase(parent.NativeReturn), types.Erase(m.NativeReturn)
		if !types.IsSubtype(ce, pe, l.p) {
			if s, ok := types.Native(pe); ok {
				out[-1] = s
			}
		}
	}
	return out
}

func erasedOrMixed(t types.Type) types.Type {
	if t == nil {
		return types.TMixed
	}
	return types.Erase(t)
}

// ------------------------------------------------------------------ bodies

func (l *lowering) walkBody(body []ast.Stmt, sc *scope) error {
	for _, s := range body {
		if err := l.walk(s, sc); err != nil {
			return err
		}
	}
	return nil
}

// walk lowers the expressions of a node.
func (l *lowering) walk(root ast.Node, sc *scope) error {
	var err error
	ast.Inspect(root, func(n ast.Node) bool {
		if err != nil {
			return false
		}
		switch n := n.(type) {
		case *ast.ClassDecl:
			if n.Name != nil {
				err = l.classDecl(n)
				return false
			}
			return true // anonymous classes are walked as code
		case *ast.FunctionDecl:
			err = l.funcDecl(n)
			return false
		case *ast.TypeAlias:
			l.blank(n.S)
			return false
		case *ast.Closure:
			err = l.closure(n, sc)
			return false
		case *ast.ArrowFunc:
			err = l.arrowFunc(n, sc)
			return false
		case *ast.Cast:
			if n.Op == token.T_BYTES_CAST {
				// bytes is a string at runtime: the cast has nothing to do.
				l.e.replace(ast.Span{Start: n.S.Start, End: n.Expr.Span().Start}, "")
			}
			return true
		case *ast.Return:
			if n.Expr != nil {
				l.bare[n.Expr] = true
			}
			return true
		case *ast.ExprStmt:
			l.bare[n.Expr] = true
			return true
		case *ast.Assign:
			l.bare[n.Value] = true
			return true
		case *ast.IsExpr:
			err = l.isExpr(n, sc)
			return false
		case *ast.AsExpr:
			err = l.asExpr(n, sc)
			return false
		case *ast.Call:
			err = l.callSite(n.Callee.Span().End, n.Args, n.TypeArgs)
			if _, isName := n.Callee.(*ast.Name); !isName {
				err = l.walkOr(err, n.Callee, sc)
			}
			err = l.walkOr(err, n.Args, sc)
			return false
		case *ast.MethodCall:
			if target, ok := l.f.MethodTargets[n]; ok {
				err = l.stdlibCall(n, target, sc)
				return false
			}
			err = l.walkOr(err, n.Object, sc)
			if _, isIdent := n.Name.(*ast.Ident); !isIdent {
				err = l.walkOr(err, n.Name, sc)
			}
			err = l.callSiteOr(err, n.Name.Span().End, n.Args, n.TypeArgs)
			err = l.walkOr(err, n.Args, sc)
			return false
		case *ast.StaticCall:
			err = l.classRef(n.Class, sc)
			if _, isIdent := n.Name.(*ast.Ident); !isIdent {
				err = l.walkOr(err, n.Name, sc)
			}
			err = l.callSiteOr(err, n.Name.Span().End, n.Args, n.TypeArgs)
			err = l.walkOr(err, n.Args, sc)
			return false
		case *ast.New:
			err = l.newExpr(n, sc)
			return false
		case *ast.ClassConstFetch:
			if _, ok := n.Class.(*ast.Name); ok {
				return false
			}
			return true
		case *ast.StaticPropertyFetch:
			err = l.classRef(n.Class, sc)
			return false
		case *ast.Instanceof:
			if _, ok := n.Class.(*ast.Name); ok {
				err = l.walk(n.Expr, sc)
				return false
			}
			return true
		case *ast.Name:
			return false
		case *ast.AttrGroup, *ast.Use, *ast.Namespace:
			return false
		}
		return true
	})
	return err
}

func (l *lowering) walkOr(err error, n ast.Node, sc *scope) error {
	if err != nil || n == nil {
		return err
	}
	return l.walk(n, sc)
}

func (l *lowering) callSiteOr(err error, argsAfter int, args *ast.ArgList, typeArgs []ast.TypeExpr) error {
	if err != nil {
		return err
	}
	return l.callSite(argsAfter, args, typeArgs)
}

// classRef lowers the class position of `X::…`: a name needs nothing;
// anything else is walked as an expression.
func (l *lowering) classRef(class ast.Expr, sc *scope) error {
	if _, ok := class.(*ast.Name); ok {
		return nil
	}
	return l.walk(class, sc)
}

// callSite drops the type arguments written at a call site; the
// parentheses and the arguments stay where they were.
func (l *lowering) callSite(argsAfter int, args *ast.ArgList, typeArgs []ast.TypeExpr) error {
	if len(typeArgs) > 0 && args != nil {
		s := ast.Span{Start: argsAfter, End: args.S.Start}
		l.e.replace(s, strings.Repeat("\n", bytes.Count(l.e.src[s.Start:s.End], []byte("\n"))))
	}
	return nil
}

func (l *lowering) newExpr(n *ast.New, sc *scope) error {
	if n.Anon != nil {
		return l.walk(n.Anon, sc)
	}
	if _, ok := n.Class.(*ast.Name); !ok {
		if err := l.walk(n.Class, sc); err != nil {
			return err
		}
	}
	if n.Args == nil {
		// `new Foo<User>` without parentheses: drop the type arguments.
		if len(n.TypeArgs) > 0 {
			l.e.replace(ast.Span{Start: n.Class.Span().End, End: n.S.End}, "")
		}
		return nil
	}
	if err := l.callSite(n.Class.Span().End, n.Args, n.TypeArgs); err != nil {
		return err
	}
	return l.walk(n.Args, sc)
}

// ------------------------------------------------------------------ closures

func (l *lowering) closure(n *ast.Closure, sc *scope) error {
	c := sc.child()
	for _, tp := range n.TypeParams {
		c.params[tp.Name.Name] = tp
	}
	c.res = c.res.WithParams(c.res.NativeParams(n.TypeParams))
	l.removeTypeParams(n.S.Start, n.TypeParams)
	tags := append(l.templateTags(n.TypeParams, c), l.signature(n.Params, n.ReturnType, c, nil)...)
	l.addDoc(n.S.Start, "", tags)
	for _, u := range n.Uses {
		if err := l.walk(u, c); err != nil {
			return err
		}
	}
	return l.walkBody(n.Body, c)
}

func (l *lowering) arrowFunc(n *ast.ArrowFunc, sc *scope) error {
	c := sc.child()
	for _, tp := range n.TypeParams {
		c.params[tp.Name.Name] = tp
	}
	c.res = c.res.WithParams(c.res.NativeParams(n.TypeParams))
	l.removeTypeParams(n.S.Start, n.TypeParams)
	tags := append(l.templateTags(n.TypeParams, c), l.signature(n.Params, n.ReturnType, c, nil)...)
	l.addDoc(n.S.Start, "", tags)
	return l.walk(n.Body, c)
}

// ------------------------------------------------------------------ is / as

// classTest tests operand against `class` or `class<Foo>` the way PHP code
// does: a string that names a class, or one that is_a() the argument. The
// operand is repeated, so it must be simple; a type argument that is not a
// plain class (object, a type parameter, a union) is left to the runtime.
func (l *lowering) classTest(t *ast.NamedType, operand string, simple bool, sc *scope) (string, bool) {
	if !simple {
		return "", false
	}
	if len(t.TypeArgs) == 0 {
		return "\\is_string(" + operand + ") && (\\class_exists(" + operand + ") || \\interface_exists(" + operand + "))", true
	}
	if c, ok := sc.res.Native(t.TypeArgs[0]).(types.Class); ok && len(c.Args) == 0 {
		return "\\is_string(" + operand + ") && \\is_a(" + operand + ", \\" + c.Name + "::class, true)", true
	}
	return "", false
}

// simpleOperand reports whether an expression can be repeated in the
// output without changing behaviour.
func simpleOperand(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Variable:
		return true
	case *ast.PropertyFetch:
		return !e.NullSafe && simpleOperand(e.Object) && isIdent(e.Name)
	case *ast.StaticPropertyFetch:
		_, isName := e.Class.(*ast.Name)
		return isName
	case *ast.ConstFetch, *ast.Name, *ast.ClassConstFetch:
		return true
	case *ast.Paren:
		return simpleOperand(e.Expr)
	}
	return false
}

func isIdent(e ast.Expr) bool {
	_, ok := e.(*ast.Ident)
	return ok
}

// nativeTest returns a PHP expression testing operand against a type, when
// PHP has a native way: `instanceof`, `is_int()`, `=== null`.
func (l *lowering) nativeTest(t ast.TypeExpr, operand string, simple bool, sc *scope) (string, bool) {
	switch t := t.(type) {
	case *ast.NamedType:
		if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified && strings.EqualFold(t.Name.Parts[0], "class") {
			return l.classTest(t, operand, simple, sc)
		}
		if len(t.TypeArgs) > 0 {
			return "", false
		}
		if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified {
			name := t.Name.Parts[0]
			if _, isParam := sc.params[name]; isParam {
				return "", false
			}
			if sc.class != nil {
				for _, tp := range sc.class.TypeParams {
					if tp.Name == name {
						return "", false
					}
				}
			}
			if sc.res.FileAlias(name) != nil {
				return "", false
			}
			switch strings.ToLower(name) {
			case "int":
				return "\\is_int(" + operand + ")", true
			case "float":
				return "\\is_float(" + operand + ")", true
			case "string":
				return "\\is_string(" + operand + ")", true
			case "bool":
				return "\\is_bool(" + operand + ")", true
			case "array":
				return "\\is_array(" + operand + ")", true
			case "object":
				return "\\is_object(" + operand + ")", true
			case "iterable":
				return "\\is_iterable(" + operand + ")", true
			case "callable":
				return "\\is_callable(" + operand + ")", true
			case "null":
				return operand + " === null", true
			case "true":
				return operand + " === true", true
			case "false":
				return operand + " === false", true
			case "mixed":
				return "true", true
			case "void", "never", "list", "map", "class", "closure", "scalar", "numeric":
				return "", false
			}
		}
		return operand + " instanceof " + l.text(t.Name.S), true
	case *ast.NullableType:
		if !simple {
			return "", false
		}
		inner, ok := l.nativeTest(t.Type, operand, simple, sc)
		if !ok {
			return "", false
		}
		return "(" + operand + " === null || " + inner + ")", true
	case *ast.UnionType:
		if !simple {
			return "", false
		}
		var parts []string
		for _, m := range t.Types {
			p, ok := l.nativeTest(m, operand, simple, sc)
			if !ok {
				return "", false
			}
			parts = append(parts, p)
		}
		return "(" + strings.Join(parts, " || ") + ")", true
	}
	return "", false
}

func (l *lowering) isExpr(n *ast.IsExpr, sc *scope) error {
	operand := l.text(n.Expr.Span())
	simple := simpleOperand(n.Expr)
	if test, ok := l.nativeTest(n.Type, operand, simple, sc); ok {
		// `if ($x is ?Foo)` needs no second pair of parentheses.
		if strings.HasPrefix(test, "(") && n.S.Start > 0 && l.e.src[n.S.Start-1] == '(' && n.S.End < len(l.e.src) && l.e.src[n.S.End] == ')' {
			test = test[1 : len(test)-1]
		}
		// Keep the operand's own edits by wrapping instead of replacing
		// when the test starts with it.
		if strings.HasPrefix(test, operand+" ") && !strings.HasPrefix(test, "(") {
			l.e.replace(ast.Span{Start: n.Expr.Span().End, End: n.S.End}, test[len(operand):])
			return l.walk(n.Expr, sc)
		}
		if strings.HasPrefix(test, "\\is_") || strings.HasPrefix(test, "(") {
			if idx := strings.Index(test, operand); idx >= 0 && !simple {
				l.e.insert(n.Expr.Span().Start, test[:idx])
				l.e.replace(ast.Span{Start: n.Expr.Span().End, End: n.S.End}, test[idx+len(operand):])
				return l.walk(n.Expr, sc)
			}
		}
		l.e.replace(n.S, test)
		return nil
	}
	desc, err := l.typeValue(sc.res.Native(n.Type), sc, n)
	if err != nil {
		return err
	}
	l.e.insert(n.Expr.Span().Start, desc+"->check(")
	l.e.replace(ast.Span{Start: n.Expr.Span().End, End: n.S.End}, ")")
	return l.walk(n.Expr, sc)
}

func (l *lowering) asExpr(n *ast.AsExpr, sc *scope) error {
	desc, err := l.typeValue(sc.res.Native(n.Type), sc, n)
	if err != nil {
		return err
	}
	method := "assert"
	if n.Nullable {
		method = "orNull"
	}
	l.e.insert(n.Expr.Span().Start, desc+"->"+method+"(")
	l.e.replace(ast.Span{Start: n.Expr.Span().End, End: n.S.End}, ")")
	return l.walk(n.Expr, sc)
}

// ------------------------------------------------------------------ runtime descriptors

// typeValue renders a type as a \Phaenon\Type expression.
func (l *lowering) typeValue(t types.Type, sc *scope, at ast.Node) (string, error) {
	rt := l.e.importShort(strings.TrimPrefix(RuntimeType, "\\"), sc.res)
	switch v := t.(type) {
	case nil:
		return rt + "::mixed()", nil
	case types.Param:
		name := types.ParamName(v.Name)
		return "", l.errorf(at, "type parameter %s is erased and has no runtime value; test against a class<%s> value instead", name, name)
	case types.Class:
		if strings.EqualFold(v.Name, "Closure") {
			return rt + "::of(\\Closure::class)", nil
		}
		var args []string
		for _, a := range v.Args {
			s, err := l.typeValue(a, sc, at)
			if err != nil {
				return "", err
			}
			args = append(args, s)
		}
		s := rt + "::of(\\" + v.Name + "::class"
		if len(args) > 0 {
			s += ", " + strings.Join(args, ", ")
		}
		return s + ")", nil
	case types.Static:
		return rt + "::of(static::class)", nil
	case types.This:
		return rt + "::of(static::class)", nil
	case types.Mixed:
		return rt + "::mixed()", nil
	case types.Null:
		return rt + "::null()", nil
	case types.Bool:
		return rt + "::bool()", nil
	case types.BoolLit:
		if v.V {
			return rt + "::literal(true)", nil
		}
		return rt + "::literal(false)", nil
	case types.Int:
		return rt + "::int()", nil
	case types.Float, types.FloatLit:
		return rt + "::float()", nil
	case types.String:
		return rt + "::string()", nil
	case types.StringLit:
		return rt + "::literal(" + phpString(v.V) + ")", nil
	case types.ClassString:
		if v.Of == nil {
			return rt + "::classString(" + rt + "::object())", nil
		}
		of, err := l.typeValue(v.Of, sc, at)
		if err != nil {
			return "", err
		}
		return rt + "::classString(" + of + ")", nil
	case types.Object:
		return rt + "::object()", nil
	case types.Resource:
		return rt + "::resource()", nil
	case types.Callable:
		if v.Closure {
			return rt + "::of(\\Closure::class)", nil
		}
		return rt + "::callable()", nil
	case types.Iterable:
		return rt + "::iterable()", nil
	case types.EnumCase:
		return rt + "::literal(\\" + v.Class + "::" + v.Case + ")", nil
	case types.Array:
		val, err := l.typeValue(v.Value, sc, at)
		if err != nil {
			return "", err
		}
		if v.List {
			return rt + "::list(" + val + ")", nil
		}
		key, err := l.typeValue(v.Key, sc, at)
		if err != nil {
			return "", err
		}
		return rt + "::map(" + key + ", " + val + ")", nil
	case types.Shape:
		return "", l.errorf(at, "an array shape cannot be tested at runtime; test its entries, or use array")
	case types.Union:
		if len(v.Types) == 2 {
			for i, m := range v.Types {
				if _, isNull := m.(types.Null); isNull {
					other, err := l.typeValue(v.Types[1-i], sc, at)
					if err != nil {
						return "", err
					}
					return rt + "::nullable(" + other + ")", nil
				}
			}
		}
		var parts []string
		for _, m := range v.Types {
			s, err := l.typeValue(m, sc, at)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return rt + "::union(" + strings.Join(parts, ", ") + ")", nil
	case types.Intersection:
		var parts []string
		for _, m := range v.Types {
			s, err := l.typeValue(m, sc, at)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return rt + "::intersection(" + strings.Join(parts, ", ") + ")", nil
	}
	return "", l.errorf(at, "%s cannot be tested at runtime", types.Print(t))
}

func phpString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

var _ = fmt.Sprintf

// ------------------------------------------------------------------ stdlib method syntax

// stdlibCall rewrites `$xs->map($f)` as `Seq::map($xs, $f)` (design/collections.md
// §3.1): the receiver becomes the first argument and the class is imported
// (roadmap D11). The receiver's text stays where it is, so a chain written
// over several lines keeps its lines.
func (l *lowering) stdlibCall(n *ast.MethodCall, target program.MethodTarget, sc *scope) error {
	cls := target.Class
	id, ok := n.Name.(*ast.Ident)
	if !ok || n.Args == nil {
		return l.errorf(n, "cannot compile this method call on a %s value", strings.ToLower(cls[strings.LastIndex(cls, "\\")+1:]))
	}
	obj := n.Object.Span()
	arrowText := "->"
	if n.NullSafe {
		arrowText = "?->"
	}
	rel := bytes.Index(l.e.src[obj.End:], []byte(arrowText))
	if rel < 0 {
		return l.errorf(n, "cannot find the %s of this call", arrowText)
	}
	arrow := obj.End + rel
	call := l.e.importShort(cls, sc.res) + "::" + id.Name + "("
	closing := ""
	if target.Guard {
		// `$x?->f()` and `$a?->b->f()`: PHP would skip the call, the static
		// call would not. The receiver is a path without side effects
		// (checker: purePath), so it may be read twice.
		recv := strings.ReplaceAll(string(l.e.src[obj.Start:obj.End]), "\n", " ")
		// In the else branch the chain is known non-null, so its ?-> are
		// plain -> there (PHPStan would otherwise flag them as never null).
		call = "null === " + recv + " ? null : " + call
		elseRecv := strings.ReplaceAll(string(l.e.src[obj.Start:obj.End]), "?->", "->")
		defer l.e.replace(obj, elseRecv) // after the inserts at the span's ends
		if !l.bare[n] {
			call, closing = "("+call, ")"
		}
	}
	l.e.insert(obj.Start, call)
	if len(n.Args.Args) > 0 {
		comma := ","
		if arrow == obj.End {
			comma = ", "
		}
		l.e.insert(obj.End, comma)
	}
	gone := ast.Span{Start: arrow, End: n.Args.S.Start + 1} // `->name<…>(`
	l.e.replace(gone, strings.Repeat("\n", bytes.Count(l.e.src[gone.Start:gone.End], []byte("\n"))))
	if closing != "" {
		l.e.insert(n.S.End, closing)
	}
	err := l.walk(n.Object, sc)
	return l.walkOr(err, n.Args, sc)
}
