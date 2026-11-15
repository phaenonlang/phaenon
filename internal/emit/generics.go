package emit

import (
	"bytes"
	"regexp"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// This file lowers Phaenon's generics to PHP: type parameters and
// arguments are erased from the native signatures and restated in
// PHPStan docblocks; `is` / `as` become PHP's own checks, as the checker
// lowered them; `type` aliases are expanded. The
// output contains no identifier the programmer did not write (roadmap D9):
// a type parameter has no runtime value, a `class<T>` parameter does. See
// design/generics.md §4 and §11.2.

type lowering struct {
	e *emitter
	p *program.Program
	f *program.File
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
			if len(n.TypeArgs) > 0 || isAliasCandidate(n) || isBytes(n) {
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
	l := &lowering{e: e, p: e.opts.Program, f: e.opts.File}
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
		return l.classDecl(s, nil)
	case *ast.FunctionDecl:
		return l.funcDecl(s)
	case *ast.Block:
		return l.stmts(s.Stmts)
	}
	return l.walk(s, l.topScope(s.Span().Start))
}

// blank removes a statement (dropLines).
func (l *lowering) blank(s ast.Span) {
	l.e.drop = append(l.e.drop, s)
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
			// `T...` and `T=` are PHPStan's markers as well: the docblock
			// spells the parameter as the source did.
			s := l.docType(m.Type, sc)
			if m.Variadic {
				s += "..."
			}
			if m.Optional {
				s += "="
			}
			parts = append(parts, s)
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
		if nt, ok := n.(*ast.NamedType); ok && isBytes(nt) {
			found = true
		}
		return !found
	})
	return found
}

// isBytes: the type bytes, which PHP would read as a class of that name.
// It is Phaenon syntax (hasGenerics) and no alias candidate: its erasure,
// string, says all a docblock could.
func isBytes(n *ast.NamedType) bool {
	return len(n.Name.Parts) == 1 && strings.EqualFold(n.Name.Parts[0], "bytes")
}

// ------------------------------------------------------------------ docblocks

// addDoc gives the declaration at declStart (whose current doc comment
// text is doc) the given tags, merging into an existing docblock. Tags
// already present are not repeated.
func (l *lowering) addDoc(declStart int, doc string, tags []string) {
	l.addDocWith(declStart, doc, tags, "")
}

// addDocWith is addDoc with one more tag, extra: a declaration's
// `@throws`, which joins the generated tags whenever there are any (a
// declaration without them gets it from lowerFunctions). It reports
// whether extra was written. The docblock is laid out as a person writes
// one (design/line-map.md §2): a new one on lines of its own before the
// declaration, a one-line one rewritten as a multi-line block, tags added
// to a multi-line one on lines of their own.
func (l *lowering) addDocWith(declStart int, doc string, tags []string, extra string) (wroteExtra bool) {
	if len(tags) == 0 {
		return false
	}
	indent := l.e.outIndent(declStart)
	idx := -1
	if doc != "" {
		// A docblock between the attributes and the declaration
		// (`#[A] /** … */ function f()`) lies after declStart and is not the
		// one PHPStan reads: it reads the comment before the attributes. The
		// tags then go into a docblock of their own there.
		idx = bytes.LastIndex(l.e.src[:declStart], []byte(doc))
		if idx >= 0 && len(bytes.TrimSpace(l.e.src[idx+len(doc):declStart])) > 0 {
			idx = -1 // the same text above an earlier declaration
		}
	}
	if idx < 0 {
		if extra != "" {
			tags = append(tags, extra)
		}
		if len(tags) == 1 && !l.e.startsLine(declStart) {
			// A closure in an expression keeps its line: `$f = /** @return T
			// */ function () {…}`, as a person writes the one tag there.
			l.e.insert(declStart, "/** "+tags[0]+" */ ")
			return extra != ""
		}
		l.e.insertFree(declStart, docBlock(indent, "", tags)+"\n"+indent)
		return extra != ""
	}
	oneLine := !strings.Contains(doc, "\n")
	filled := doc // a one-line docblock with the types of its own tags filled in
	var inDoc *emitter
	if oneLine {
		// A one-line docblock that gains tags is rewritten as a whole, so
		// the types its tags get (`/** @param $x - prose */`) are filled
		// into its text rather than in place.
		saved := l.e
		l.e = &emitter{src: []byte(doc), opts: saved.opts}
		tags = l.fillTypes(doc, 0, tags)
		tags = l.replaceVar(doc, 0, tags)
		inDoc = l.e
		l.e = saved
		filled = string(inDoc.spliced())
	} else {
		tags = l.fillTypes(doc, idx, tags)
		tags = l.replaceVar(doc, idx, tags)
	}
	tags = missingTags(doc, tags)
	if extra != "" && len(missingTags(doc, []string{extra})) > 0 {
		tags = append(tags, extra)
		wroteExtra = true
	}
	if len(tags) == 0 {
		if oneLine {
			for _, ed := range inDoc.edits {
				l.e.replace(ast.Span{Start: idx + ed.Start, End: idx + ed.End}, ed.Text)
			}
		}
		return false
	}
	if oneLine {
		// The content moves into a new docblock: its written types erase
		// as translateWrittenDocs would have translated them in place.
		content := translateDoc(oneLineContent(filled))
		if len(tags) == 1 && strings.HasPrefix(tags[0], "@var ") && content != "" {
			// A property's `/** what it holds */` gets its type back on the
			// tag's line: `@var list<string> what it holds`, the PHP it
			// came from (roadmap D14).
			tags, content = []string{tags[0] + " " + content}, ""
		}
		l.e.rewriteDoc(idx, doc, content, tags)
		return wroteExtra
	}
	lastNL := strings.LastIndexByte(doc, '\n')
	closeIndent := l.indentAt(idx)
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
	l.e.insertFree(at, b.String())
	return wroteExtra
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
	case "@throws", "@phpstan-throws":
		// Several @throws tags add up; only the same type is the same tag.
		if len(fields) > 1 {
			return "@throws " + fields[1]
		}
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
	if l.addDocWith(d.S.Start, d.Doc, tags, l.e.throwsTag(d.Throws)) {
		l.e.throwsWritten[d] = true
	}
	if err := l.declHead(d.Attrs, d.Params, sc); err != nil {
		return err
	}
	return l.walkBody(d.Body, sc)
}

// ------------------------------------------------------------------ classes

// classDecl lowers a class-like declaration. An anonymous class is one as
// well: its `extends Box<int>` and the Phaenon types of its members are no
// more PHP than a named class's, and a method of it that overrides a
// generic one is held to the override-erasure rule. The program keeps no
// anonymous class by its declaration, so it is declared here as the
// checker declared it; outer is the scope of the expression it stands in,
// which its constructor arguments belong to.
func (l *lowering) classDecl(d *ast.ClassDecl, outer *scope) error {
	sc := l.topScope(d.S.Start)
	var cl *program.Class
	doc := d.Doc
	if d.Name != nil {
		cl = l.p.Class(sc.res.Scope().Prefix() + d.Name.Name)
	} else {
		cl = l.p.DeclareAnonymous(l.f, sc.res.Scope(), d)
		doc = l.e.docBefore(d.S.Start)
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
	l.addDoc(d.S.Start, doc, tags)
	if err := l.declHead(d.Attrs, nil, sc); err != nil {
		return err
	}
	if d.Args != nil && outer != nil {
		if err := l.walk(d.Args, outer); err != nil {
			return err
		}
	}

	for _, m := range d.Members {
		switch m := m.(type) {
		case *ast.MethodDecl:
			if err := l.methodDecl(cl, m, sc); err != nil {
				return err
			}
		case *ast.PropertyDecl:
			psc := sc.child()
			if err := l.declHead(m.Attrs, nil, psc); err != nil {
				return err
			}
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
			if err := l.hooks(m.Hooks, psc); err != nil {
				return err
			}
		case *ast.ClassConstDecl:
			// A typed constant's type is native PHP like a property's.
			csc := sc.child()
			if l.eraseType(m.Type, csc, "") {
				l.addDoc(m.S.Start, m.Doc, []string{"@var " + l.docType(m.Type, csc)})
			}
			if err := l.declHead(m.Attrs, nil, csc); err != nil {
				return err
			}
			for _, it := range m.Consts {
				if err := l.walk(it.Value, csc); err != nil {
					return err
				}
			}
		case *ast.EnumCase:
			if err := l.walk(m, sc.child()); err != nil {
				return err
			}
		}
	}
	return nil
}

// hooks lowers the hooks of a property or of a promoted constructor
// parameter. A set hook's parameter is typed like any other: its Phaenon
// type is erased and kept in a @param tag before the hook, where PHPStan
// reads it (the tag matters when the hook takes more than the property
// holds). The tree keeps no docblock for a hook, so the one before it is
// looked up in the source.
func (l *lowering) hooks(hooks []*ast.PropertyHook, sc *scope) error {
	for _, h := range hooks {
		tags := l.signature(h.Params, nil, sc, nil)
		l.addDoc(h.S.Start, l.e.docBefore(h.S.Start), tags)
		if err := l.declHead(h.Attrs, h.Params, sc); err != nil {
			return err
		}
		if err := l.walkBody(h.Body, sc); err != nil {
			return err
		}
		if h.Expr != nil {
			if err := l.walk(h.Expr, sc); err != nil {
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
		overrides = l.overrideErasure(m, sc)
	}
	tags = append(tags, l.signature(d.Params, d.ReturnType, sc, overrides)...)
	if l.addDocWith(d.S.Start, d.Doc, tags, l.e.throwsTag(d.Throws)) {
		l.e.throwsWritten[d] = true
	}
	if err := l.declHead(d.Attrs, d.Params, sc); err != nil {
		return err
	}
	if !d.HasBody {
		return nil
	}
	return l.walkBody(d.Body, sc)
}

// overrideErasure applies the override-erasure rule (program's
// ErasureOverrides): a method that overrides keeps its parameters' erased
// types at least as wide as the parent's, and its return type no wider.
// A widened class is named as the file knows it, or imported (roadmap
// D11): it is the compiler's word, where the source wrote `T`.
func (l *lowering) overrideErasure(m *program.Method, sc *scope) map[int]string {
	widened := m.ErasureOverrides()
	// In slot order: the first class to claim a short name gets it.
	slots := make([]int, 0, len(widened))
	for k := range widened {
		slots = append(slots, k)
	}
	sort.Ints(slots)
	var out map[int]string
	for _, k := range slots {
		s, ok := types.Native(widened[k])
		if !ok {
			continue
		}
		if out == nil {
			out = map[int]string{}
		}
		out[k] = nativeClassName.ReplaceAllStringFunc(s, func(name string) string {
			return l.e.importShort(name[1:], sc.res)
		})
	}
	return out
}

// nativeClassName matches a fully qualified class name as types.Native
// prints it.
var nativeClassName = regexp.MustCompile(`\\[^|&?()\s]+`)

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
			err = l.classDecl(n, sc)
			return false
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
		case *ast.Call:
			err = l.callSite(n.Callee.Span().End, n.Args, n.TypeArgs)
			if _, isName := n.Callee.(*ast.Name); !isName {
				err = l.walkOr(err, n.Callee, sc)
			}
			err = l.walkOr(err, n.Args, sc)
			return false
		case *ast.MethodCall:
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
		case *ast.AttrGroup:
			err = l.attrGroup(n, sc)
			return false
		case *ast.Use, *ast.Namespace:
			return false
		}
		return true
	})
	return err
}

// attrGroup lowers the arguments of an attribute group: a closure there
// (functions.md §2) carries Phaenon types like any other and is erased and
// documented the same way; the attribute's own name is left alone.
func (l *lowering) attrGroup(g *ast.AttrGroup, sc *scope) error {
	for _, a := range g.Attrs {
		if a.Args == nil {
			continue
		}
		if err := l.walk(a.Args, sc); err != nil {
			return err
		}
	}
	return nil
}

// declHead lowers what a declaration holds outside its body and signature:
// its attributes, and its parameters' attributes, defaults and, on a
// promoted constructor parameter, hooks. The walk does not descend into
// declarations, so each kind calls this itself.
func (l *lowering) declHead(attrs []*ast.AttrGroup, params []*ast.Param, sc *scope) error {
	for _, g := range attrs {
		if err := l.attrGroup(g, sc); err != nil {
			return err
		}
	}
	for _, p := range params {
		for _, g := range p.Attrs {
			if err := l.attrGroup(g, sc); err != nil {
				return err
			}
		}
		if p.Default != nil {
			if err := l.walk(p.Default, sc); err != nil {
				return err
			}
		}
		if err := l.hooks(p.Hooks, sc); err != nil {
			return err
		}
	}
	return nil
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
		return l.classDecl(n.Anon, sc)
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
	if err := l.declHead(n.Attrs, n.Params, c); err != nil {
		return err
	}
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
	if err := l.declHead(n.Attrs, n.Params, c); err != nil {
		return err
	}
	return l.walk(n.Body, c)
}
