package migrate

// Lifting generics out of docblocks.
//
// PHPStan code states its generics in comments: `@template T`, `@param
// list<T> $xs`, `@return T`, `@extends Base<T>`, `@var array<K, V>`. Phaenon
// states them in the signature. Lift moves what it can and leaves the rest
// in place with a note, so a migration never changes what PHPStan or PHP
// see: a tag is lifted only when its type is expressible in Phaenon syntax
// and erases back to exactly the native type that was declared.

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// Note records a docblock tag Lift left in place, and why.
type Note struct {
	Line int // 1-based line of the tag in the input
	Msg  string
}

// Lift rewrites the generic docblocks of Phaenon source as syntax.
func Lift(src []byte, opts Options) ([]byte, []Note, error) {
	f, err := parser.Parse(src, lexer.ModePhaenon)
	if err != nil {
		return nil, nil, fmt.Errorf("parse: %w", err)
	}
	l := &lifter{src: src, keepPublic: opts.KeepPublic}
	l.stmts(f.Stmts, nil)
	sort.SliceStable(l.notes, func(i, j int) bool { return l.notes[i].Line < l.notes[j].Line })
	return l.apply(), l.notes, nil
}

type edit struct {
	ast.Span
	text string
}

type lifter struct {
	src        []byte
	edits      []edit
	notes      []Note
	keepPublic bool
	// ns and uses resolve class names the way PHP does, so that a docblock
	// `\App\Model\Row` and a declared, imported `Row` are the same class.
	ns   string
	uses map[string]string
}

var phpTypeKeywords = map[string]bool{
	"int": true, "float": true, "string": true, "bool": true, "mixed": true, "object": true, "void": true,
	"never": true, "null": true, "true": true, "false": true, "self": true, "static": true, "parent": true,
	"iterable": true, "resource": true, "array": true, "callable": true,
}

// fqn resolves a class name written in this file.
func (l *lifter) fqn(name string) string {
	if strings.HasPrefix(name, "\\") {
		return name[1:]
	}
	first, rest, _ := strings.Cut(name, "\\")
	if full, ok := l.uses[strings.ToLower(first)]; ok {
		if rest == "" {
			return full
		}
		return full + "\\" + rest
	}
	if l.ns == "" {
		return name
	}
	return l.ns + "\\" + name
}

// atoms is the erasure of a type: the set of native PHP types it becomes.
type atoms map[string]bool

// scope maps the type parameters in scope to the erasure of their bounds.
// A nil entry is a parameter that stayed in its docblock: types that
// mention it must stay there too.
type scope map[string]atoms

func (sc scope) child() scope {
	out := scope{}
	for k, v := range sc {
		out[k] = v
	}
	return out
}

func (l *lifter) note(off int, format string, args ...any) {
	l.notes = append(l.notes, Note{Line: bytes.Count(l.src[:off], []byte("\n")) + 1, Msg: fmt.Sprintf(format, args...)})
}

func (l *lifter) replace(s ast.Span, text string) { l.edits = append(l.edits, edit{s, text}) }
func (l *lifter) insert(at int, text string)      { l.replace(ast.Span{Start: at, End: at}, text) }

func (l *lifter) apply() []byte {
	sort.SliceStable(l.edits, func(i, j int) bool { return l.edits[i].Start < l.edits[j].Start })
	var out bytes.Buffer
	pos := 0
	for _, e := range l.edits {
		if e.Start < pos {
			continue // overlapping edit: the earlier one wins
		}
		out.Write(l.src[pos:e.Start])
		out.WriteString(e.text)
		pos = e.End
	}
	out.Write(l.src[pos:])
	return out.Bytes()
}

func (l *lifter) stmts(list []ast.Stmt, sc scope) {
	for _, s := range list {
		switch s := s.(type) {
		case *ast.Namespace:
			l.ns = ""
			if s.Name != nil {
				l.ns = strings.Join(s.Name.Parts, "\\")
			}
			l.uses = map[string]string{}
			l.stmts(s.Body, sc)
		case *ast.Use:
			if l.uses == nil {
				l.uses = map[string]string{}
			}
			for _, it := range s.Items {
				kind := s.Kind
				if it.Kind != ast.UseClass {
					kind = it.Kind
				}
				if kind != ast.UseClass {
					continue
				}
				full := strings.Join(it.Name.Parts, "\\")
				if s.Prefix != nil {
					full = strings.Join(s.Prefix.Parts, "\\") + "\\" + full
				}
				alias := it.Name.Parts[len(it.Name.Parts)-1]
				if it.Alias != nil {
					alias = it.Alias.Name
				}
				l.uses[strings.ToLower(alias)] = full
			}
		case *ast.Block:
			l.stmts(s.Stmts, sc)
		case *ast.ClassDecl:
			l.classDecl(s, sc)
		case *ast.FunctionDecl:
			l.funcDecl(s, sc)
		}
	}
}

// ------------------------------------------------------------------ docblocks

// doc is one doc comment split into tag groups: a group starts at a line
// whose text begins with `@` and runs until the next such line, a blank
// line, or the end.
type doc struct {
	text   string
	start  int // offset of the comment in the source
	lines  []string
	groups []*group
	// removed marks lines to drop; rewrite replaces a line's text.
	removed map[int]bool
	rewrite map[int]string
}

type group struct {
	name  string // tag name, lowercased, without a phpstan-/psalm- prefix
	tag   string // the tag as written (@phpstan-param)
	text  string // the text after the tag, continuation lines joined
	first int    // line index of the tag
	last  int    // line index after the group
	// parsed is the group parsed on its own.
	parsed *docblock.Doc
}

// content is a docblock line without its frame: `/**`, `*`, `*/`, spaces.
func content(line string) string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "/**")
	t = strings.TrimSuffix(t, "*/")
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(t, "*")
	return strings.TrimSpace(t)
}

func (l *lifter) findDoc(text string, before int) *doc {
	if text == "" {
		return nil
	}
	idx := bytes.LastIndex(l.src[:before], []byte(text))
	if idx < 0 {
		return nil
	}
	d := &doc{text: text, start: idx, lines: strings.Split(text, "\n"), removed: map[int]bool{}, rewrite: map[int]string{}}
	var cur *group
	for i, line := range d.lines {
		t := content(line)
		switch {
		case strings.HasPrefix(t, "@"):
			if cur != nil {
				cur.last = i
			}
			tag, rest, _ := strings.Cut(t, " ")
			cur = &group{tag: tag, name: tagName(tag), text: strings.TrimSpace(rest), first: i, last: len(d.lines)}
			d.groups = append(d.groups, cur)
		case cur != nil && t == "":
			cur.last = i
			cur = nil
		case cur != nil:
			cur.text += " " + t
		}
	}
	for _, g := range d.groups {
		g.parsed = docblock.Parse("/** " + g.tag + " " + g.text + " */")
	}
	return d
}

// tagName normalises @phpstan-param, @psalm-return to the plain tag.
func tagName(tag string) string {
	name := strings.ToLower(tag)
	for _, p := range []string{"@phpstan-", "@psalm-", "@phan-"} {
		if strings.HasPrefix(name, p) {
			return "@" + name[len(p):]
		}
	}
	return name
}

// prose is what follows the type (and the `$name`, when the tag has one)
// in a tag's text.
func prose(text string, name bool) string {
	_, rest, err := docblock.ParseTypePrefix(text)
	if err != nil {
		return ""
	}
	rest = strings.TrimSpace(rest)
	if name {
		rest = strings.TrimLeft(rest, "&.")
		if strings.HasPrefix(rest, "$") {
			i := 1
			for i < len(rest) && rest[i] != ' ' {
				i++
			}
			rest = strings.TrimSpace(rest[i:])
		}
	}
	return rest
}

// description is the prose of the tag after its type and name.
func (g *group) description(name bool) string { return prose(g.text, name) }

func (d *doc) remove(g *group) {
	for i := g.first; i < g.last; i++ {
		d.removed[i] = true
	}
}

// keepProse rewrites a lifted `@param T $x prose` line to `@param $x - prose`;
// prose on the following lines stays where it is. The dash is the spelling
// of a parameter description without a type (as in JSDoc): an IDE that
// expects `@param Type $x` would otherwise read the first word of the prose
// as the type. The emitter puts the type back and drops the dash.
func (d *doc) keepProse(g *group, name string) {
	line := d.lines[g.first]
	cut := strings.Index(line, g.tag)
	d.rewrite[g.first] = strings.TrimRight(line[:cut]+"@param $"+name+" - "+paramProse(line[cut+len(g.tag):], name), " ")
	if g.first == len(d.lines)-1 {
		d.rewrite[g.first] += " */"
	}
}

// paramProse is the description on a @param line, typed (`T $x prose`) or
// not (`$x prose`, `$x - prose`), without the dash.
func paramProse(text, name string) string {
	text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "*/"))
	var rest string
	if strings.HasPrefix(strings.TrimLeft(text, "&."), "$") {
		rest = strings.TrimPrefix(strings.TrimLeft(text, "&."), "$"+name)
	} else {
		rest = prose(text, true)
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "-"))
}

// stripType rewrites a lifted `@var T prose` line to its prose: the
// declaration states the type now, and the docblock keeps what the tag said
// about the value — `/** @var list<string> what it holds */` becomes
// `/** what it holds */`. Prose on the following lines stays where it is.
func (d *doc) stripType(g *group, named bool) {
	line := d.lines[g.first]
	cut := strings.Index(line, g.tag)
	rest := prose(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line[cut+len(g.tag):]), "*/")), named)
	d.rewrite[g.first] = strings.TrimRight(line[:cut]+rest, " ")
	if g.first == len(d.lines)-1 {
		d.rewrite[g.first] += " */"
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// finish applies the recorded line removals and rewrites, deleting the
// comment entirely when nothing but its frame is left.
func (l *lifter) finish(d *doc) {
	if d == nil || (len(d.removed) == 0 && len(d.rewrite) == 0) {
		return
	}
	span := ast.Span{Start: d.start, End: d.start + len(d.text)}
	deleteAll := func() {
		end := span.End
		for end < len(l.src) && isSpace(l.src[end]) {
			end++
		}
		l.replace(ast.Span{Start: span.Start, End: end}, "")
	}
	if len(d.lines) == 1 {
		line := d.lines[0]
		if r, ok := d.rewrite[0]; ok {
			line = r
		}
		if d.removed[0] || content(line) == "" {
			deleteAll()
			return
		}
		l.replace(span, line)
		return
	}
	last := len(d.lines) - 1
	var body []string
	for i := 1; i <= last; i++ {
		line := d.lines[i]
		if r, ok := d.rewrite[i]; ok {
			line = r
		}
		if i == last {
			if d.removed[i] || content(line) != "" && d.removed[i] {
				break
			}
			if content(line) != "" {
				// `* @var int */`: the close shares the line with content.
				body = append(body, strings.TrimSuffix(strings.TrimRight(line, " "), "*/"))
			}
			break
		}
		if d.removed[i] {
			continue
		}
		body = append(body, line)
	}
	blank := func(s string) bool { return content(s) == "" }
	var out []string
	for _, line := range body {
		if blank(line) && (len(out) == 0 || blank(out[len(out)-1])) {
			continue
		}
		out = append(out, line)
	}
	for len(out) > 0 && blank(out[len(out)-1]) {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		deleteAll()
		return
	}
	closeLine := d.lines[last]
	if content(closeLine) != "" {
		closeLine = closeLine[:len(closeLine)-len(strings.TrimLeft(closeLine, " \t"))] + " */"
	}
	l.replace(span, d.lines[0]+"\n"+strings.Join(out, "\n")+"\n"+closeLine)
}

// ------------------------------------------------------------------ declarations

func (l *lifter) classDecl(c *ast.ClassDecl, outer scope) {
	sc := outer.child()
	if c.Name != nil {
		d := l.findDoc(c.Doc, c.S.Start)
		if d != nil {
			l.templates(d, c.TypeParams, c.Name.S.End, sc)
			for _, g := range d.groups {
				switch g.name {
				case "@extends", "@template-extends":
					l.superType(d, g, c.Extends, sc)
				case "@implements", "@template-implements":
					l.superType(d, g, c.Implements, sc)
				}
			}
		}
		for _, tp := range c.TypeParams {
			sc[tp.Name.Name] = l.astAtoms(tp.Bound, sc)
		}
		l.finish(d)
	}
	for _, m := range c.Members {
		switch m := m.(type) {
		case *ast.MethodDecl:
			l.methodDecl(m, sc)
		case *ast.PropertyDecl:
			l.propertyDecl(m, sc)
		case *ast.ClassConstDecl:
			if m.Modifiers&ast.ModPublic != 0 {
				l.dropWord(m.Mods, "public")
			}
		}
	}
}

func (l *lifter) funcDecl(fn *ast.FunctionDecl, outer scope) {
	if !fn.Fun {
		l.replace(fn.Head, "fun")
	}
	l.params(fn.Params)
	sc := outer.child()
	d := l.findDoc(fn.Doc, fn.S.Start)
	if d != nil {
		l.templates(d, fn.TypeParams, fn.Name.S.End, sc)
	}
	for _, tp := range fn.TypeParams {
		sc[tp.Name.Name] = l.astAtoms(tp.Bound, sc)
	}
	lifted := map[string]string{}
	if d != nil {
		lifted = l.signature(d, fn.Params, fn.ReturnType, sc)
	}
	l.paramDocs(fn.Params, lifted, sc)
	l.finish(d)
	l.stmts(fn.Body, sc)
}

func (l *lifter) methodDecl(m *ast.MethodDecl, outer scope) {
	if !m.Fun {
		l.replace(m.Head, l.funHead(m.Head))
	}
	l.params(m.Params)
	sc := outer.child()
	d := l.findDoc(m.Doc, m.S.Start)
	if d != nil {
		l.templates(d, m.TypeParams, m.Name.S.End, sc)
	}
	for _, tp := range m.TypeParams {
		sc[tp.Name.Name] = l.astAtoms(tp.Bound, sc)
	}
	lifted := map[string]string{}
	if d != nil {
		lifted = l.signature(d, m.Params, m.ReturnType, sc)
	}
	l.paramDocs(m.Params, lifted, sc)
	l.finish(d)
	l.stmts(m.Body, sc)
}

func collectNames(t *docblock.TypeNode, into map[string]bool) {
	if t == nil {
		return
	}
	if t.Kind == docblock.KindNamed {
		into[t.Name] = true
	}
	for _, a := range t.Args {
		collectNames(a, into)
	}
	for _, p := range t.Params {
		collectNames(p.Type, into)
	}
	collectNames(t.Return, into)
}

func (l *lifter) propertyDecl(p *ast.PropertyDecl, sc scope) {
	l.dropPublic(p.Modifiers, p.Mods)
	d := l.findDoc(p.Doc, p.S.Start)
	if d == nil {
		return
	}
	for _, g := range d.groups {
		if g.name != "@var" {
			continue
		}
		off := d.start + lineOffset(d, g.first)
		if len(p.Props) != 1 {
			l.note(off, "kept %s: the declaration has several properties", g.tag)
			continue
		}
		if len(g.parsed.Vars) != 1 || g.parsed.Vars[0].Type == nil {
			l.note(off, "kept %s: unreadable type", g.tag)
			continue
		}
		v := g.parsed.Vars[0]
		if v.Name != "" && v.Name != p.Props[0].Var.Name {
			l.note(off, "kept %s: names another property", g.tag)
			continue
		}
		text, ok := l.liftable(off, g, v.Type, p.Type, sc, false)
		if !ok {
			continue
		}
		if g.description(v.Name != "") != "" {
			d.stripType(g, v.Name != "")
		} else {
			d.remove(g)
		}
		l.setType(p.Type, p.Props[0].Var.S.Start, text)
	}
	l.finish(d)
}

// templates lifts the @template tags of a declaration into `<...>` after
// the name at nameEnd, and records the parameters in sc. A constructor's
// template that types a promoted property is lifted like any other: the
// property then holds a value of that type for some argument, which the
// checker and PHPStan infer per use (design/generics.md §11.2).
func (l *lifter) templates(d *doc, native []*ast.TypeParam, nameEnd int, sc scope) {
	var params []string
	for _, g := range d.groups {
		switch g.name {
		case "@template", "@template-covariant", "@template-contravariant":
		default:
			continue
		}
		off := d.start + lineOffset(d, g.first)
		if len(g.parsed.Templates) != 1 {
			l.note(off, "kept %s: unreadable", g.tag)
			continue
		}
		t := g.parsed.Templates[0]
		keep := func(format string, args ...any) {
			sc[t.Name] = nil
			l.note(off, "kept %s %s: "+format, append([]any{g.tag, t.Name}, args...)...)
		}
		if len(native) > 0 {
			keep("the declaration already has type parameters")
			continue
		}
		p := t.Name
		switch t.Variance {
		case docblock.Covariant:
			p = "out " + p
		case docblock.Contravariant:
			p = "in " + p
		}
		bound := atoms{"mixed": true}
		if t.Bound != nil {
			b, ok := render(t.Bound)
			if !ok {
				keep("the bound %s has no Phaenon spelling", t.Bound)
				continue
			}
			p += " of " + b
			bound = l.docAtoms(t.Bound, sc)
		}
		if t.Default != nil {
			def, ok := render(t.Default)
			if !ok {
				keep("the default %s has no Phaenon spelling", t.Default)
				continue
			}
			p += " = " + def
		}
		sc[t.Name] = bound
		params = append(params, p)
		d.remove(g)
	}
	if len(params) > 0 {
		l.insert(nameEnd, "<"+strings.Join(params, ", ")+">")
	}
}

// superType lifts `@extends Base<T>` / `@implements I<T>` into the matching
// clause.
func (l *lifter) superType(d *doc, g *group, clauses []*ast.NamedType, sc scope) {
	off := d.start + lineOffset(d, g.first)
	types := g.parsed.Extends
	if g.name == "@implements" || g.name == "@template-implements" {
		types = g.parsed.Implements
	}
	if len(types) != 1 || types[0].Kind != docblock.KindNamed {
		l.note(off, "kept %s: unreadable", g.tag)
		return
	}
	t := types[0]
	var clause *ast.NamedType
	for _, c := range clauses {
		if strings.EqualFold(l.fqn(t.Name), l.fqn(strings.Join(c.Name.Parts, "\\"))) || nameMatches(t.Name, c.Name) {
			clause = c
		}
	}
	if clause == nil {
		l.note(off, "kept %s %s: no matching clause on the declaration", g.tag, t.Name)
		return
	}
	if len(clause.TypeArgs) > 0 {
		l.note(off, "kept %s %s: the clause already has type arguments", g.tag, t.Name)
		return
	}
	if mentionsUnlifted(t, sc) {
		l.note(off, "kept %s %s: it mentions a type parameter that stayed in the docblock", g.tag, t.Name)
		return
	}
	var args []string
	for _, a := range t.Args {
		s, ok := render(a)
		if !ok {
			l.note(off, "kept %s %s: the argument %s has no Phaenon spelling", g.tag, t.Name, a)
			return
		}
		args = append(args, s)
	}
	if len(args) > 0 {
		l.insert(clause.S.End, "<"+strings.Join(args, ", ")+">")
	}
	d.remove(g) // `@extends Base` without arguments says nothing either way
}

// nameMatches compares a docblock class name with a declared one: equal
// when written the same (ignoring a leading backslash and case), or when
// one is unqualified and matches the other's last segment.
func nameMatches(docName string, n *ast.Name) bool {
	a := strings.ToLower(strings.TrimPrefix(docName, "\\"))
	b := strings.ToLower(strings.Join(n.Parts, "\\"))
	if a == b {
		return true
	}
	last := func(s string) string {
		if i := strings.LastIndexByte(s, '\\'); i >= 0 {
			return s[i+1:]
		}
		return s
	}
	return (!strings.Contains(a, "\\") || !strings.Contains(b, "\\")) && last(a) == last(b)
}

// signature lifts the @param and @return tags of a function-like
// declaration.
func (l *lifter) signature(d *doc, params []*ast.Param, ret ast.TypeExpr, sc scope) map[string]string {
	lifted := map[string]string{}
	for _, g := range d.groups {
		off := d.start + lineOffset(d, g.first)
		switch g.name {
		case "@param":
			if len(g.parsed.Params) != 1 {
				l.note(off, "kept %s: unreadable", g.tag)
				continue
			}
			pt := g.parsed.Params[0]
			if pt.Type == nil {
				// Prose only already; it gets the dash, or goes when there is none.
				switch rest := paramProse(d.lines[g.first][strings.Index(d.lines[g.first], g.tag)+len(g.tag):], pt.Name); {
				case rest == "":
					d.remove(g)
				case !strings.Contains(d.lines[g.first], "$"+pt.Name+" -"):
					d.keepProse(g, pt.Name)
				}
				continue
			}
			var p *ast.Param
			for _, cand := range params {
				if cand.Var.Name == pt.Name {
					p = cand
				}
			}
			if p == nil {
				l.note(off, "kept %s $%s: no such parameter", g.tag, pt.Name)
				continue
			}
			if prev, ok := lifted[pt.Name]; ok {
				if text, _ := render(pt.Type); text == prev {
					d.remove(g)
				} else {
					l.note(off, "kept %s $%s: a second type for the same parameter", g.tag, pt.Name)
				}
				continue
			}
			text, ok := l.liftable(off, g, pt.Type, p.Type, sc, p.Type == nil)
			if !ok {
				continue
			}
			lifted[pt.Name] = text
			at := p.Var.S.Start
			for at > 0 && (l.src[at-1] == '.' || l.src[at-1] == '&') {
				at--
			}
			l.setType(p.Type, at, text)
			if g.description(true) != "" {
				d.keepProse(g, pt.Name)
			} else {
				d.remove(g)
			}
		case "@return":
			t := g.parsed.Return
			if t == nil {
				l.note(off, "kept %s: unreadable", g.tag)
				continue
			}
			if ret == nil {
				l.note(off, "kept %s %s: the declaration has no return type to refine", g.tag, t)
				continue
			}
			text, ok := l.liftable(off, g, t, ret, sc, false)
			if !ok {
				continue
			}
			l.setType(ret, 0, text)
			if g.description(false) != "" {
				l.note(off, "kept %s: it has a description; the type is now also in the declaration", g.tag)
			} else {
				d.remove(g)
			}
		}
	}
	return lifted
}

// paramDocs lifts the `/** @var T */` a promoted property carries on its
// parameter, unless the method's own @param typed the parameter already.
func (l *lifter) paramDocs(params []*ast.Param, lifted map[string]string, sc scope) {
	for _, p := range params {
		if p.Doc == "" {
			continue
		}
		d := l.findDoc(p.Doc, p.S.Start)
		if d == nil {
			continue
		}
		for _, g := range d.groups {
			if g.name != "@var" {
				continue
			}
			off := d.start + lineOffset(d, g.first)
			if _, ok := lifted[p.Var.Name]; ok {
				l.note(off, "kept %s: the @param tag already typed $%s", g.tag, p.Var.Name)
				continue
			}
			if len(g.parsed.Vars) != 1 || g.parsed.Vars[0].Type == nil {
				l.note(off, "kept %s: unreadable type", g.tag)
				continue
			}
			v := g.parsed.Vars[0]
			if v.Name != "" && v.Name != p.Var.Name {
				l.note(off, "kept %s: names another variable", g.tag)
				continue
			}
			text, ok := l.liftable(off, g, v.Type, p.Type, sc, p.Type == nil)
			if !ok {
				continue
			}
			at := p.Var.S.Start
			for at > 0 && (l.src[at-1] == '.' || l.src[at-1] == '&') {
				at--
			}
			l.setType(p.Type, at, text)
			if g.description(v.Name != "") != "" {
				d.stripType(g, v.Name != "")
			} else {
				d.remove(g)
			}
		}
		l.finish(d)
	}
}

// liftable decides whether a docblock type can replace the declared one
// and returns its Phaenon spelling; a refusal is noted.
func (l *lifter) liftable(off int, g *group, docT *docblock.TypeNode, native ast.TypeExpr, sc scope, untyped bool) (string, bool) {
	if written := l.nativeText(native); native != nil && phaenonSpelled(written) {
		// A declaration already written in Phaenon (a .phn lifted again) is
		// the type; the tag is a leftover of an earlier edit and goes.
		return written, true
	}
	text, ok := render(docT)
	if !ok {
		l.note(off, "kept %s %s: no Phaenon spelling for it", g.tag, docT)
		return "", false
	}
	if mentionsUnlifted(docT, sc) {
		l.note(off, "kept %s %s: it mentions a type parameter that stayed in the docblock", g.tag, docT)
		return "", false
	}
	if !l.compatible(docT, native, sc, untyped) {
		l.note(off, "kept %s %s: it erases to a different type than the declared %s", g.tag, docT, l.nativeText(native))
		return "", false
	}
	return text, true
}

// phaenonSpelled reports whether a declared type uses Phaenon syntax — type
// arguments, a closure signature, a tuple, `bytes` — which PHP could not
// have declared, so that a docblock cannot be its source.
func phaenonSpelled(text string) bool {
	if strings.ContainsAny(text, "<(") {
		return true
	}
	for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == '|' || r == '&' || r == '?' }) {
		if part == "bytes" {
			return true
		}
	}
	return false
}

func mentionsUnlifted(t *docblock.TypeNode, sc scope) bool {
	names := map[string]bool{}
	collectNames(t, names)
	for n := range names {
		if b, ok := sc[n]; ok && b == nil {
			return true
		}
	}
	return false
}

// setType replaces a declared type, or inserts one before offset at when
// there was none.
func (l *lifter) setType(t ast.TypeExpr, at int, text string) {
	if t != nil {
		if string(l.src[t.Span().Start:t.Span().End]) != text {
			l.replace(t.Span(), text)
		}
		return
	}
	l.insert(at, text+" ")
}

func lineOffset(d *doc, line int) int {
	off := 0
	for i := 0; i < line; i++ {
		off += len(d.lines[i]) + 1
	}
	return off
}

func (l *lifter) nativeText(t ast.TypeExpr) string {
	if t == nil {
		return "(none)"
	}
	return string(l.src[t.Span().Start:t.Span().End])
}

// ------------------------------------------------------------------ rendering

// render prints a docblock type in Phaenon syntax, or reports that it has
// no spelling there (shapes, literals, int ranges, callable signatures
// other than Closure, conditional types).
func render(n *docblock.TypeNode) (string, bool) {
	switch n.Kind {
	case docblock.KindNamed:
		name := n.Name
		switch strings.ToLower(name) {
		case "$this":
			name = "static"
		case "array":
			if len(n.Args) == 2 {
				name = "map"
			}
		case "class-string":
			// The one hyphenated name with a Phaenon spelling; the rest stay
			// docblock vocabulary (design/generics.md §11.2).
			name = "class"
		case "int", "float":
			if len(n.Args) > 0 {
				return "", false // int<0, max>
			}
		}
		for _, c := range name {
			if !(c == '\\' || c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80) {
				return "", false
			}
		}
		if len(n.Args) == 0 {
			return name, true
		}
		var args []string
		for _, a := range n.Args {
			s, ok := render(a)
			if !ok {
				return "", false
			}
			args = append(args, s)
		}
		return name + "<" + strings.Join(args, ", ") + ">", true
	case docblock.KindShape:
		// A positional, sealed array{A, B} is the tuple (A, B)
		// (design/collections.md §2.4). One element stays: `(A)` would
		// read as a cast. Named keys stay docblock vocabulary.
		if n.Unsealed || len(n.Entries) < 2 || !(strings.EqualFold(n.Name, "array") || strings.EqualFold(n.Name, "list")) {
			return "", false
		}
		var elems []string
		for i, en := range n.Entries {
			if en.Optional || (en.HasKey && !(en.IsInt && en.IntKey == int64(i))) {
				return "", false
			}
			s, ok := render(en.Type)
			if !ok {
				return "", false
			}
			elems = append(elems, s)
		}
		return "(" + strings.Join(elems, ", ") + ")", true
	case docblock.KindNullable:
		inner, ok := render(n.Args[0])
		if !ok {
			return "", false
		}
		if n.Args[0].Kind == docblock.KindUnion || n.Args[0].Kind == docblock.KindIntersection {
			return inner + "|null", true
		}
		return "?" + inner, true
	case docblock.KindUnion:
		// `X|null` is spelled ?X, as PHP code writes it; a Closure signature
		// can only be nullable that way.
		if len(n.Args) == 2 {
			for i, m := range n.Args {
				other := n.Args[1-i]
				if m.Kind == docblock.KindNull && (other.Kind == docblock.KindCallable || other.Kind == docblock.KindNamed) {
					s, ok := render(other)
					if !ok {
						return "", false
					}
					return "?" + s, true
				}
			}
		}
		var parts []string
		var closure *docblock.TypeNode
		for _, m := range n.Args {
			if m.Kind == docblock.KindCallable {
				if closure != nil {
					return "", false
				}
				// Spelled last: its return type would swallow the members after it.
				closure = m
				continue
			}
			s, ok := render(m)
			if !ok {
				return "", false
			}
			if m.Kind == docblock.KindIntersection {
				s = "(" + s + ")"
			}
			parts = append(parts, s)
		}
		if closure != nil {
			s, ok := render(closure)
			if !ok {
				return "", false
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, "|"), true
	case docblock.KindIntersection:
		var parts []string
		for _, m := range n.Args {
			if m.Kind != docblock.KindNamed {
				return "", false
			}
			s, ok := render(m)
			if !ok {
				return "", false
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, "&"), true
	case docblock.KindCallable:
		if strings.ToLower(strings.TrimPrefix(n.Name, "\\")) != "closure" {
			return "", false
		}
		var params []string
		for _, p := range n.Params {
			if p.Optional || p.Variadic || p.ByRef || p.Type == nil {
				return "", false
			}
			s, ok := render(p.Type)
			if !ok {
				return "", false
			}
			params = append(params, s)
		}
		s := "Closure(" + strings.Join(params, ", ") + ")"
		if n.Return != nil {
			r, ok := render(n.Return)
			if !ok {
				return "", false
			}
			s += ": " + r
		}
		return s, true
	case docblock.KindNull:
		return "null", true
	case docblock.KindBoolLit:
		if n.Bool {
			return "true", true
		}
		return "false", true
	}
	return "", false
}

// ------------------------------------------------------------------ erasure

// compatible reports whether the docblock type erases to the declared
// native type, so that lifting it changes nothing for PHP. An untyped
// parameter (untyped=true) accepts a type that erases to mixed.
func (l *lifter) compatible(docT *docblock.TypeNode, native ast.TypeExpr, sc scope, untyped bool) bool {
	da := l.docAtoms(docT, sc)
	if native == nil {
		return untyped && da["mixed"] && len(da) == 1
	}
	na := l.astAtoms(native, sc)
	if len(da) != len(na) {
		return false
	}
	for k := range da {
		if !na[k] {
			return false
		}
	}
	return true
}

func normalise(set atoms) atoms {
	if set["mixed"] {
		return atoms{"mixed": true}
	}
	if set["bool"] || set["true"] && set["false"] {
		delete(set, "true")
		delete(set, "false")
		set["bool"] = true
	}
	return set
}

var keywordErasure = map[string][]string{
	"list": {"array"}, "non-empty-list": {"array"}, "map": {"array"}, "non-empty-map": {"array"},
	"array": {"array"}, "non-empty-array": {"array"}, "callable-array": {"array"},
	"iterable": {"iterable"}, "callable": {"callable"}, "pure-callable": {"callable"},
	"closure": {"closure"}, "pure-closure": {"closure"},
	"class": {"string"}, "class-string": {"string"}, "interface-string": {"string"}, "enum-string": {"string"}, "trait-string": {"string"},
	"non-empty-string": {"string"}, "numeric-string": {"string"}, "literal-string": {"string"},
	"non-empty-literal-string": {"string"}, "callable-string": {"string"}, "lowercase-string": {"string"},
	"non-empty-lowercase-string": {"string"}, "truthy-string": {"string"}, "non-falsy-string": {"string"},
	"positive-int": {"int"}, "negative-int": {"int"}, "non-negative-int": {"int"}, "non-positive-int": {"int"},
	"array-key": {"int", "string"}, "scalar": {"int", "float", "string", "bool"},
	"numeric": {"int", "float", "string"}, "number": {"int", "float"},
	"$this": {"static"}, "key-of": {"mixed"}, "value-of": {"mixed"},
}

func (l *lifter) docAtoms(n *docblock.TypeNode, sc scope) atoms {
	out := atoms{}
	switch n.Kind {
	case docblock.KindNamed:
		if b, ok := sc[n.Name]; ok {
			for k := range b {
				out[k] = true
			}
			if b == nil {
				out["mixed"] = true
			}
			break
		}
		lower := strings.ToLower(strings.TrimPrefix(n.Name, "\\"))
		if e, ok := keywordErasure[lower]; ok {
			for _, k := range e {
				out[k] = true
			}
			break
		}
		if strings.Contains(lower, "-") {
			out["mixed"] = true
			break
		}
		if phpTypeKeywords[lower] {
			out[lower] = true
			break
		}
		out[strings.ToLower(l.fqn(n.Name))] = true
	case docblock.KindNullable:
		out = l.docAtoms(n.Args[0], sc)
		out["null"] = true
	case docblock.KindUnion:
		for _, m := range n.Args {
			for k := range l.docAtoms(m, sc) {
				out[k] = true
			}
		}
	case docblock.KindIntersection:
		var parts []string
		for _, m := range n.Args {
			for k := range l.docAtoms(m, sc) {
				parts = append(parts, k)
			}
		}
		sort.Strings(parts)
		out[strings.Join(parts, "&")] = true
	case docblock.KindShape:
		if strings.EqualFold(n.Name, "object") {
			out["object"] = true
		} else {
			out["array"] = true
		}
	case docblock.KindCallable:
		if strings.ToLower(strings.TrimPrefix(n.Name, "\\")) == "closure" {
			out["closure"] = true
		} else {
			out["callable"] = true
		}
	case docblock.KindNull:
		out["null"] = true
	case docblock.KindBoolLit:
		if n.Bool {
			out["true"] = true
		} else {
			out["false"] = true
		}
	case docblock.KindIntLit:
		out["int"] = true
	case docblock.KindFloatLit:
		out["float"] = true
	case docblock.KindStringLit:
		out["string"] = true
	default:
		out["mixed"] = true
	}
	return normalise(out)
}

func (l *lifter) astAtoms(t ast.TypeExpr, sc scope) atoms {
	out := atoms{}
	switch t := t.(type) {
	case nil:
		out["mixed"] = true
	case *ast.NamedType:
		name := strings.Join(t.Name.Parts, "\\")
		if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified {
			if b, ok := sc[name]; ok {
				for k := range b {
					out[k] = true
				}
				if b == nil {
					out["mixed"] = true
				}
				break
			}
			if e, ok := keywordErasure[strings.ToLower(name)]; ok {
				for _, k := range e {
					out[k] = true
				}
				break
			}
			if phpTypeKeywords[strings.ToLower(name)] {
				out[strings.ToLower(name)] = true
				break
			}
		}
		if t.Name.Kind == ast.NameFullyQualified {
			out[strings.ToLower(name)] = true
			break
		}
		out[strings.ToLower(l.fqn(name))] = true
	case *ast.NullableType:
		out = l.astAtoms(t.Type, sc)
		out["null"] = true
	case *ast.UnionType:
		for _, m := range t.Types {
			for k := range l.astAtoms(m, sc) {
				out[k] = true
			}
		}
	case *ast.IntersectionType:
		var parts []string
		for _, m := range t.Types {
			for k := range l.astAtoms(m, sc) {
				parts = append(parts, k)
			}
		}
		sort.Strings(parts)
		out[strings.Join(parts, "&")] = true
	case *ast.ClosureType:
		out["closure"] = true
	case *ast.TupleType:
		out["array"] = true
	}
	return normalise(out)
}

// funHead spells a `[modifiers] function` head as `[modifiers] fun`: the
// modifiers as written, minus a `public` that `fun` implies (kept with
// --keep-public). A head broken over lines or holding a comment keeps its
// modifiers and has only the keyword replaced.
func (l *lifter) funHead(head ast.Span) string {
	text := string(l.src[head.Start:head.End])
	if strings.ContainsAny(text, "\n\r/#") {
		return text[:len(text)-len("function")] + "fun"
	}
	var parts []string
	for _, w := range strings.Fields(text[:len(text)-len("function")]) {
		if strings.EqualFold(w, "public") && !l.keepPublic {
			continue
		}
		parts = append(parts, w)
	}
	return strings.Join(append(parts, "fun"), " ")
}

// params drops the `public` of a promoted parameter with an asymmetric
// visibility, `public private(set) T $x`, which Phaenon implies — as a class
// constant's `public` is (design/functions.md §1.1).
func (l *lifter) params(params []*ast.Param) {
	for _, p := range params {
		l.dropPublic(p.Modifiers, p.Mods)
	}
}

func (l *lifter) dropPublic(m ast.Modifier, mods ast.Span) {
	if m&ast.ModPublic == 0 || m&(ast.ModPublicSet|ast.ModProtectedSet|ast.ModPrivateSet) == 0 {
		return
	}
	l.dropWord(mods, "public")
}

// dropWord removes the modifier word (case-insensitively) and the space after
// it from a modifier run, unless --keep-public asked for it to stay.
func (l *lifter) dropWord(mods ast.Span, word string) {
	if l.keepPublic {
		return
	}
	text := string(l.src[mods.Start:mods.End])
	i := strings.Index(strings.ToLower(text), word)
	if i < 0 || (i+len(word) < len(text) && text[i+len(word)] == '(') {
		return
	}
	end := mods.Start + i + len(word)
	for end < len(l.src) && isSpace(l.src[end]) {
		end++
	}
	l.replace(ast.Span{Start: mods.Start + i, End: end}, "")
}
