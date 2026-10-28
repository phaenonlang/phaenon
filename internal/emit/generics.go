package emit

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/printer"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// This file lowers Phaenon's generics to PHP: type parameters and
// arguments are erased from the native signatures and restated in
// PHPStan docblocks; reified parameters become leading `string|\Phaenon\Type`
// parameters (or a `$__T` property for a class) and their value uses
// become calls on that descriptor; `is` / `as` become native checks where
// PHP has them and \Phaenon\Type checks elsewhere; `type` aliases are
// expanded. See design/generics.md §4–§5.

// RuntimeType is the fully qualified runtime descriptor class.
const RuntimeType = "\\Phaenon\\Type"

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
	// reified maps a reified parameter to the PHP expression holding its
	// descriptor: "$T" in a function or method, "$this->__T" in a class.
	reified map[string]string
	// used records the function-level descriptors a body referenced, so
	// an enclosing closure can capture them.
	used map[string]bool
}

func (s *scope) child() *scope {
	c := &scope{res: s.res, class: s.class, static: s.static, params: map[string]*ast.TypeParam{}, reified: map[string]string{}, used: map[string]bool{}}
	for k, v := range s.params {
		c.params[k] = v
	}
	for k, v := range s.reified {
		c.reified[k] = v
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
		case *ast.TypeParam, *ast.TypeAlias, *ast.IsExpr, *ast.AsExpr, *ast.ClosureType:
			found = true
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
	case "list", "map", "non-empty-list", "non-empty-map", "class-string":
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
			return &Error{File: e.opts.SourceName, Line: 1, Msg: "generics need type information: compile with phn build (without --no-types)"}
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
	return &scope{res: l.p.NewResolver(l.f, l.f.ScopeAt(off), nil, ast.Span{}), params: map[string]*ast.TypeParam{}, reified: map[string]string{}, used: map[string]bool{}}
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
			case "list", "map", "non-empty-list", "non-empty-map", "array", "non-empty-array":
				return "array"
			case "iterable":
				return "iterable"
			case "class-string":
				return "string"
			}
			if a := sc.res.FileAlias(name); a != nil {
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
				case "non-empty-map":
					name = "non-empty-array"
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
	case *ast.ClosureType:
		var parts []string
		for _, m := range t.Params {
			parts = append(parts, l.docType(m, sc))
		}
		s := "\\Closure(" + strings.Join(parts, ", ") + ")"
		if t.Return != nil {
			s += ": " + l.docType(t.Return, sc)
		}
		return s
	}
	return "mixed"
}

// needsDoc reports whether erasing t loses information a docblock must
// keep.
func (l *lowering) needsDoc(t ast.TypeExpr, sc *scope) bool {
	if t == nil {
		return false
	}
	return l.docType(t, sc) != l.erase(t, sc)
}

// eraseType rewrites a written type to its erasure, and reports whether a
// docblock is needed for it.
func (l *lowering) eraseType(t ast.TypeExpr, sc *scope, override string) bool {
	if t == nil {
		return false
	}
	erased := l.erase(t, sc)
	if override != "" {
		erased = override
	}
	if erased != l.text(t.Span()) {
		l.e.replace(t.Span(), erased)
	}
	return override != "" || l.needsDoc(t, sc)
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
	tags = missingTags(doc, tags)
	if len(tags) == 0 {
		return
	}
	span := ast.Span{Start: idx, End: idx + len(doc)}
	indent = l.indentAt(idx)
	if !strings.Contains(doc, "\n") {
		// `/** Summary. */` becomes a multi-line block.
		content := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(doc, "/**"), "*/"))
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
	var b strings.Builder
	for _, t := range tags {
		b.WriteString(closeIndent + " * " + t + "\n")
	}
	l.e.insertLines(idx+lastNL+1, b.String())
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

// reifiedParams inserts the leading `string|\Phaenon\Type $T` parameters
// and the normalising statements for the reified parameters of a function
// or method, and returns their @param tags.
func (l *lowering) reifiedParams(tps []*ast.TypeParam, params []*ast.Param, nameEnd int, body []ast.Stmt, hasBody bool, sc *scope) []string {
	var decls, inits, tags []string
	for _, tp := range tps {
		if !tp.Reified {
			continue
		}
		decls = append(decls, l.reifiedParamDecl(tp, sc))
		inits = append(inits, "$"+tp.Name.Name+" = "+RuntimeType+"::from($"+tp.Name.Name+");")
		tags = append(tags, "@param "+l.reifiedParamDoc(tp, sc))
		sc.reified[tp.Name.Name] = "$" + tp.Name.Name
	}
	if len(decls) == 0 {
		return nil
	}
	open := l.paramsOpen(nameEnd)
	text := strings.Join(decls, ", ")
	if len(params) > 0 {
		text += ", "
	}
	l.e.insert(open+1, text)
	if hasBody {
		var after int
		if len(params) > 0 {
			after = params[len(params)-1].S.End
		} else {
			after = open
		}
		brace := l.bodyOpen(after)
		l.e.insertLines(brace+1, l.firstStatements(brace, body, inits))
	}
	return tags
}

// firstStatements renders statements to put right after a body's `{`,
// on their own lines when the body spans lines.
func (l *lowering) firstStatements(brace int, body []ast.Stmt, stmts []string) string {
	src := l.e.src
	i := brace + 1
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	if i < len(src) && src[i] == '\n' {
		indent := l.indentAt(brace) + "    "
		if len(body) > 0 {
			indent = l.indentAt(body[0].Span().Start)
		}
		var b strings.Builder
		for _, s := range stmts {
			b.WriteString("\n" + indent + s)
		}
		return b.String()
	}
	return " " + strings.Join(stmts, " ")
}

func (l *lowering) reifiedParamDecl(tp *ast.TypeParam, sc *scope) string {
	t := RuntimeType
	if tp.Bound != nil && l.boundIsClass(tp.Bound, sc) {
		t = "string|" + RuntimeType
	}
	return t + " $" + tp.Name.Name
}

func (l *lowering) reifiedParamDoc(tp *ast.TypeParam, sc *scope) string {
	n := tp.Name.Name
	if tp.Bound != nil && l.boundIsClass(tp.Bound, sc) {
		return "class-string<" + n + ">|" + RuntimeType + "<" + n + "> $" + n
	}
	return RuntimeType + "<" + n + "> $" + n
}

func (l *lowering) boundIsClass(t ast.TypeExpr, sc *scope) bool {
	switch r := sc.res.Native(t).(type) {
	case types.Class:
		return true
	case types.Object:
		return true
	default:
		_ = r
	}
	return false
}

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
	tags = append(tags, l.reifiedParams(d.TypeParams, d.Params, d.Name.S.End, d.Body, true, sc)...)
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

	// Reified class parameters live in private properties.
	var reified []*ast.TypeParam
	for _, tp := range d.TypeParams {
		if tp.Reified {
			reified = append(reified, tp)
			sc.reified[tp.Name.Name] = "$this->__" + tp.Name.Name
		}
	}
	// The arguments a reified parent needs from this class.
	parentVals, err := l.parentReifiedValues(cl, d, sc)
	if err != nil {
		return err
	}
	var ctor *ast.MethodDecl
	for _, m := range d.Members {
		if md, ok := m.(*ast.MethodDecl); ok && strings.EqualFold(md.Name.Name, "__construct") {
			ctor = md
		}
	}
	brace := l.bodyOpen(l.headerEnd(d))
	memberIndent := l.indentAt(brace) + "    "
	if len(d.Members) > 0 {
		memberIndent = l.indentAt(d.Members[0].Span().Start)
	}
	if len(reified) > 0 {
		var b strings.Builder
		for _, tp := range reified {
			b.WriteString("\n" + memberIndent + "/** @var " + RuntimeType + "<" + tp.Name.Name + "> */")
			b.WriteString("\n" + memberIndent + "private readonly " + RuntimeType + " $__" + tp.Name.Name + ";")
		}
		if ctor == nil {
			b.WriteString("\n")
			b.WriteString(l.synthesizedCtor(cl, reified, parentVals, memberIndent, sc))
		}
		text := b.String()
		if len(d.Members) == 0 {
			text = strings.TrimSuffix(text, "\n")
		} else if ctor != nil {
			text += "\n"
		}
		l.e.insertLines(brace+1, text)
	} else if len(parentVals) > 0 && ctor == nil {
		text := "\n" + l.synthesizedCtor(cl, nil, parentVals, memberIndent, sc)
		if len(d.Members) == 0 {
			text = strings.TrimSuffix(text, "\n")
		}
		l.e.insertLines(brace+1, text)
	}

	for _, m := range d.Members {
		switch m := m.(type) {
		case *ast.MethodDecl:
			if err := l.methodDecl(cl, d, m, sc, reified, parentVals); err != nil {
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

// headerEnd is the offset after the last token of a class header.
func (l *lowering) headerEnd(d *ast.ClassDecl) int {
	end := d.S.Start
	if d.Name != nil {
		end = d.Name.S.End
	}
	for _, tp := range d.TypeParams {
		end = max(end, tp.S.End)
	}
	if d.BackingType != nil {
		end = max(end, d.BackingType.Span().End)
	}
	for _, e := range d.Extends {
		end = max(end, e.S.End)
	}
	for _, e := range d.Implements {
		end = max(end, e.S.End)
	}
	if d.Args != nil {
		end = max(end, d.Args.S.End)
	}
	// Past a closing `>` the span may not include.
	for end < len(l.e.src) && l.e.src[end] == '>' {
		end++
	}
	return end
}

// parentReifiedValues returns the runtime expressions this class must
// pass to its parent's constructor for the parent's reified parameters.
func (l *lowering) parentReifiedValues(cl *program.Class, d *ast.ClassDecl, sc *scope) ([]string, error) {
	parent := cl.ParentClass()
	if parent == nil || d.Kind != ast.KindClass {
		return nil, nil
	}
	parent.Signature()
	var vals []string
	for i, tp := range parent.TypeParams {
		if !tp.Reified {
			continue
		}
		if i >= len(cl.ParentArgs) {
			return nil, l.errorf(d, "%s needs a type argument for reified %s", parent.Name, tp.Name)
		}
		v, err := l.argValue(cl.ParentArgs[i], sc, d)
		if err != nil {
			return nil, err
		}
		vals = append(vals, v)
	}
	return vals, nil
}

// synthesizedCtor writes a constructor for a class that declares reified
// parameters or binds a parent's, forwarding the parent's parameters.
func (l *lowering) synthesizedCtor(cl *program.Class, reified []*ast.TypeParam, parentVals []string, indent string, sc *scope) string {
	var params, docs, body []string
	for _, tp := range reified {
		params = append(params, l.reifiedParamDecl(tp, sc))
		docs = append(docs, "@param "+l.reifiedParamDoc(tp, sc))
		body = append(body, "$this->__"+tp.Name.Name+" = "+RuntimeType+"::from($"+tp.Name.Name+");")
	}
	var parentCtor *program.Method
	if parent := cl.ParentClass(); parent != nil {
		parentCtor = parent.FindMethod("__construct")
	}
	if parentCtor != nil || len(parentVals) > 0 {
		args := append([]string{}, parentVals...)
		if parentCtor != nil {
			for _, p := range parentCtor.Params {
				params = append(params, l.paramText(p))
				if p.Variadic {
					args = append(args, "...$"+p.Name)
				} else {
					args = append(args, "$"+p.Name)
				}
				if doc, ok := paramDoc(p); ok {
					docs = append(docs, doc)
				}
			}
		}
		body = append(body, "parent::__construct("+strings.Join(args, ", ")+");")
	}
	var b strings.Builder
	if len(docs) == 1 {
		b.WriteString(indent + "/** " + docs[0] + " */\n")
	} else if len(docs) > 1 {
		b.WriteString(indent + "/**\n")
		for _, d := range docs {
			b.WriteString(indent + " * " + d + "\n")
		}
		b.WriteString(indent + " */\n")
	}
	b.WriteString(indent + "public function __construct(" + strings.Join(params, ", ") + ")\n")
	b.WriteString(indent + "{\n")
	for _, s := range body {
		b.WriteString(indent + "    " + s + "\n")
	}
	b.WriteString(indent + "}\n")
	return b.String()
}

// paramText renders a parameter of a parent constructor for a forwarding
// constructor: native type, name, default.
func (l *lowering) paramText(p *program.Param) string {
	var b strings.Builder
	if p.NativeType != nil {
		b.WriteString(nativeOf(p.NativeType) + " ")
	}
	if p.ByRef {
		b.WriteString("&")
	}
	if p.Variadic {
		b.WriteString("...")
	}
	b.WriteString("$" + p.Name)
	if p.HasDefault && p.Default != nil {
		b.WriteString(" = " + printer.Expr(p.Default))
	} else if p.HasDefault {
		b.WriteString(" = null")
	}
	return b.String()
}

// paramDoc restates a parent parameter's docblock type when it is more
// precise than the native one.
func paramDoc(p *program.Param) (string, bool) {
	if p.Type == nil || p.NativeType == nil {
		return "", false
	}
	if types.Print(p.Type) == types.Print(p.NativeType) {
		return "", false
	}
	return "@param " + types.PrintQualified(p.Type) + " $" + p.Name, true
}

// ------------------------------------------------------------------ methods

func (l *lowering) methodDecl(cl *program.Class, cd *ast.ClassDecl, d *ast.MethodDecl, csc *scope, reified []*ast.TypeParam, parentVals []string) error {
	sc := csc.child()
	sc.static = d.Modifiers&ast.ModStatic != 0
	m := cl.FindMethod(d.Name.Name)
	if m != nil && m.Class == cl {
		sc.res = sc.res.WithParams(m.TypeParams)
	}
	for _, tp := range d.TypeParams {
		sc.params[tp.Name.Name] = tp
		delete(sc.reified, tp.Name.Name) // shadowed until declared reified below
	}
	if sc.static {
		for k, v := range sc.reified {
			if strings.HasPrefix(v, "$this") {
				delete(sc.reified, k)
			}
		}
	}
	tags := l.templateTags(d.TypeParams, sc)
	isCtor := strings.EqualFold(d.Name.Name, "__construct")
	// Leading parameters: the class's reified parameters on the
	// constructor, the method's own on itself.
	var leading []*ast.TypeParam
	if isCtor {
		leading = append(leading, reified...)
	}
	leading = append(leading, d.TypeParams...)
	var decls, inits []string
	for _, tp := range leading {
		if !tp.Reified {
			continue
		}
		decls = append(decls, l.reifiedParamDecl(tp, sc))
		tags = append(tags, "@param "+l.reifiedParamDoc(tp, sc))
		if isCtor && containsTP(reified, tp) {
			inits = append(inits, "$this->__"+tp.Name.Name+" = "+RuntimeType+"::from($"+tp.Name.Name+");")
		} else {
			inits = append(inits, "$"+tp.Name.Name+" = "+RuntimeType+"::from($"+tp.Name.Name+");")
			sc.reified[tp.Name.Name] = "$" + tp.Name.Name
		}
	}
	if len(decls) > 0 {
		open := l.paramsOpen(d.Name.S.End)
		text := strings.Join(decls, ", ")
		if len(d.Params) > 0 {
			text += ", "
		}
		l.e.insert(open+1, text)
		if d.HasBody {
			after := open
			if len(d.Params) > 0 {
				after = d.Params[len(d.Params)-1].S.End
			}
			if d.ReturnType != nil {
				after = d.ReturnType.Span().End
			}
			brace := l.bodyOpen(after)
			l.e.insertLines(brace+1, l.firstStatements(brace, d.Body, inits))
		}
	}
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
	// A binding subclass passes the parent's reified arguments first.
	if isCtor && len(parentVals) > 0 {
		found := false
		ast.Inspect(&ast.Block{Stmts: d.Body}, func(n ast.Node) bool {
			if found {
				return false
			}
			if sc, ok := n.(*ast.StaticCall); ok {
				if cn, ok := sc.Class.(*ast.Name); ok && len(cn.Parts) == 1 && strings.EqualFold(cn.Parts[0], "parent") {
					if id, ok := sc.Name.(*ast.Ident); ok && strings.EqualFold(id.Name, "__construct") && sc.Args != nil {
						text := strings.Join(parentVals, ", ")
						if len(sc.Args.Args) > 0 {
							text += ", "
						}
						l.e.insert(sc.Args.S.Start+1, text)
						found = true
					}
				}
			}
			return !found
		})
		if !found {
			return l.errorf(d, "constructor of %s must call parent::__construct() so the parent's reified type parameters are bound", cl.Name)
		}
	}
	return l.walkBody(d.Body, sc)
}

func containsTP(list []*ast.TypeParam, tp *ast.TypeParam) bool {
	for _, x := range list {
		if x == tp {
			return true
		}
	}
	return false
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
		case *ast.IsExpr:
			err = l.isExpr(n, sc)
			return false
		case *ast.AsExpr:
			err = l.asExpr(n, sc)
			return false
		case *ast.Call:
			err = l.callSite(n, n.Callee.Span().End, n.Args, n.TypeArgs, sc)
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
			err = l.callSiteOr(err, n, n.Name.Span().End, n.Args, n.TypeArgs, sc)
			err = l.walkOr(err, n.Args, sc)
			return false
		case *ast.StaticCall:
			err = l.classRef(n.Class, sc)
			if _, isIdent := n.Name.(*ast.Ident); !isIdent {
				err = l.walkOr(err, n.Name, sc)
			}
			err = l.callSiteOr(err, n, n.Name.Span().End, n.Args, n.TypeArgs, sc)
			err = l.walkOr(err, n.Args, sc)
			return false
		case *ast.New:
			err = l.newExpr(n, sc)
			return false
		case *ast.ClassConstFetch:
			if cn, ok := n.Class.(*ast.Name); ok {
				if r, ok := l.reifiedRef(cn, sc); ok {
					if id, ok := n.Name.(*ast.Ident); ok && strings.EqualFold(id.Name, "class") {
						l.e.replace(n.S, r+"->className()")
					} else {
						l.e.replace(cn.S, "("+r+"->className())")
					}
					return false
				}
				return false
			}
			return true
		case *ast.StaticPropertyFetch:
			err = l.classRef(n.Class, sc)
			return false
		case *ast.Instanceof:
			if cn, ok := n.Class.(*ast.Name); ok {
				if r, ok := l.reifiedRef(cn, sc); ok {
					l.e.insert(n.Expr.Span().Start, r+"->check(")
					l.e.replace(ast.Span{Start: n.Expr.Span().End, End: n.S.End}, ")")
					err = l.walk(n.Expr, sc)
					return false
				}
				err = l.walk(n.Expr, sc)
				return false
			}
			return true
		case *ast.Name:
			if r, ok := l.reifiedRef(n, sc); ok {
				l.e.replace(n.S, r)
			}
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

func (l *lowering) callSiteOr(err error, n ast.Node, argsAfter int, args *ast.ArgList, typeArgs []ast.TypeExpr, sc *scope) error {
	if err != nil {
		return err
	}
	return l.callSite(n, argsAfter, args, typeArgs, sc)
}

// classRef lowers the class position of `X::…`: a reified parameter
// becomes its class name; anything else is walked as an expression.
func (l *lowering) classRef(class ast.Expr, sc *scope) error {
	if cn, ok := class.(*ast.Name); ok {
		if r, ok := l.reifiedRef(cn, sc); ok {
			l.e.replace(cn.S, "("+r+"->className())")
		}
		return nil
	}
	return l.walk(class, sc)
}

// reifiedRef returns the descriptor expression for a name that denotes a
// reified type parameter in scope.
func (l *lowering) reifiedRef(n *ast.Name, sc *scope) (string, bool) {
	if len(n.Parts) != 1 || n.Kind != ast.NameUnqualified {
		return "", false
	}
	r, ok := sc.reified[n.Parts[0]]
	if ok && !strings.HasPrefix(r, "$this") {
		sc.used[n.Parts[0]] = true
	}
	return r, ok
}

// callSite drops written type arguments and inserts the runtime values of
// the callee's reified parameters.
func (l *lowering) callSite(n ast.Node, argsAfter int, args *ast.ArgList, typeArgs []ast.TypeExpr, sc *scope) error {
	if len(typeArgs) > 0 && args != nil {
		s := ast.Span{Start: argsAfter, End: args.S.Start}
		l.e.replace(s, strings.Repeat("\n", bytes.Count(l.e.src[s.Start:s.End], []byte("\n"))))
	}
	vals := l.reifiedArgs(n)
	if len(vals) == 0 || args == nil {
		return nil
	}
	var texts []string
	for _, v := range vals {
		t, err := l.argValue(v, sc, n)
		if err != nil {
			return err
		}
		texts = append(texts, t)
	}
	text := strings.Join(texts, ", ")
	if len(args.Args) > 0 {
		text += ", "
	}
	l.e.insert(args.S.Start+1, text)
	return nil
}

func (l *lowering) reifiedArgs(n ast.Node) []types.Type {
	if l.e.opts.Reified == nil {
		return nil
	}
	return l.e.opts.Reified(n)
}

func (l *lowering) newExpr(n *ast.New, sc *scope) error {
	if n.Anon != nil {
		return l.walk(n.Anon, sc)
	}
	if cn, ok := n.Class.(*ast.Name); ok {
		if r, ok := l.reifiedRef(cn, sc); ok {
			// new T(args) → $T->instantiate(args)
			if n.Args != nil {
				l.e.replace(ast.Span{Start: n.S.Start, End: n.Args.S.Start}, r+"->instantiate")
				return l.walk(n.Args, sc)
			}
			l.e.replace(n.S, r+"->instantiate()")
			return nil
		}
	} else if err := l.walk(n.Class, sc); err != nil {
		return err
	}
	vals := l.reifiedArgs(n)
	if n.Args == nil {
		// `new Foo<User>` without parentheses: supply them when needed.
		s := ast.Span{Start: n.Class.Span().End, End: n.S.End}
		if len(vals) == 0 {
			if len(n.TypeArgs) > 0 {
				l.e.replace(s, "")
			}
			return nil
		}
		var texts []string
		for _, v := range vals {
			t, err := l.argValue(v, sc, n)
			if err != nil {
				return err
			}
			texts = append(texts, t)
		}
		l.e.replace(s, "("+strings.Join(texts, ", ")+")")
		return nil
	}
	if err := l.callSite(n, n.Class.Span().End, n.Args, n.TypeArgs, sc); err != nil {
		return err
	}
	return l.walk(n.Args, sc)
}

// ------------------------------------------------------------------ closures

func (l *lowering) closure(n *ast.Closure, sc *scope) error {
	c := sc.child()
	if n.Static {
		for k, v := range c.reified {
			if strings.HasPrefix(v, "$this") {
				delete(c.reified, k)
			}
		}
	}
	for _, tp := range n.TypeParams {
		c.params[tp.Name.Name] = tp
		delete(c.reified, tp.Name.Name)
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
	if err := l.walkBody(n.Body, c); err != nil {
		return err
	}
	// Capture the function-level descriptors the body used.
	var captured []string
	for name := range c.used {
		if r, ok := sc.reified[name]; ok && !strings.HasPrefix(r, "$this") {
			captured = append(captured, r)
			sc.used[name] = true
		}
	}
	if len(captured) == 0 {
		return nil
	}
	sort.Strings(captured)
	for _, u := range n.Uses {
		for i, cv := range captured {
			if cv == "$"+u.Var.Name {
				captured = append(captured[:i], captured[i+1:]...)
				break
			}
		}
	}
	if len(captured) == 0 {
		return nil
	}
	if len(n.Uses) > 0 {
		last := n.Uses[len(n.Uses)-1]
		l.e.insert(last.S.End, ", "+strings.Join(captured, ", "))
		return nil
	}
	after := n.S.Start
	if len(n.Params) > 0 {
		after = n.Params[len(n.Params)-1].S.End
	} else {
		after = l.paramsOpen(n.S.Start)
	}
	close := l.find(after, ')')
	l.e.insert(close+1, " use ("+strings.Join(captured, ", ")+")")
	return nil
}

func (l *lowering) arrowFunc(n *ast.ArrowFunc, sc *scope) error {
	c := sc.child()
	for _, tp := range n.TypeParams {
		c.params[tp.Name.Name] = tp
		delete(c.reified, tp.Name.Name)
	}
	c.res = c.res.WithParams(c.res.NativeParams(n.TypeParams))
	l.removeTypeParams(n.S.Start, n.TypeParams)
	tags := append(l.templateTags(n.TypeParams, c), l.signature(n.Params, n.ReturnType, c, nil)...)
	l.addDoc(n.S.Start, "", tags)
	if err := l.walk(n.Body, c); err != nil {
		return err
	}
	for name := range c.used {
		sc.used[name] = true // arrow functions capture by value automatically
	}
	return nil
}

// ------------------------------------------------------------------ is / as

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
			case "void", "never", "list", "map", "non-empty-list", "non-empty-map", "non-empty-array", "class-string", "closure", "scalar", "numeric", "array-key":
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

// argValue renders a type as the argument for a reified parameter: a class
// name string where one suffices, a descriptor otherwise.
func (l *lowering) argValue(t types.Type, sc *scope, at ast.Node) (string, error) {
	switch v := t.(type) {
	case types.Class:
		if len(v.Args) == 0 {
			return "\\" + v.Name + "::class", nil
		}
	case types.Param:
		if r, ok := sc.reified[types.ParamName(v.Name)]; ok {
			if !strings.HasPrefix(r, "$this") {
				sc.used[types.ParamName(v.Name)] = true
			}
			return r, nil
		}
	}
	return l.typeValue(t, sc, at)
}

// typeValue renders a type as a \Phaenon\Type expression.
func (l *lowering) typeValue(t types.Type, sc *scope, at ast.Node) (string, error) {
	rt := RuntimeType
	switch v := t.(type) {
	case nil:
		return rt + "::mixed()", nil
	case types.Param:
		if r, ok := sc.reified[types.ParamName(v.Name)]; ok {
			if !strings.HasPrefix(r, "$this") {
				sc.used[types.ParamName(v.Name)] = true
			}
			return r, nil
		}
		return "", l.errorf(at, "type parameter %s has no runtime value here; declare it reified", types.ParamName(v.Name))
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
