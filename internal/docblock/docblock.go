// Package docblock parses PHPDoc comments: the tags the checker cares about
// and the PHPStan/Psalm type grammar. It produces a syntax tree of types
// (TypeNode) that the program model resolves against a file's namespace
// and imports — the docblock package itself knows no classes.
package docblock

import (
	"strings"
)

// Doc is the parsed content of one doc comment.
type Doc struct {
	Summary     string
	Params      []ParamTag
	Return      *TypeNode
	Vars        []VarTag
	Templates   []TemplateTag
	Extends     []*TypeNode // @extends / @template-extends
	Implements  []*TypeNode // @implements / @template-implements
	Uses        []*TypeNode // @use (trait generics)
	Mixins      []*TypeNode
	Throws      []*TypeNode
	TypeAliases []TypeAliasTag // @phpstan-type
	ImportTypes []ImportTypeTag
	Methods     []MethodTag
	Properties  []PropertyTag
	Deprecated  bool
	Internal    bool
	Pure        bool
	Immutable   bool
	Final       bool
	ParamOut    []ParamTag // @param-out
	Asserts     []AssertTag
	SelfOut     *TypeNode // @phpstan-self-out / @psalm-self-out
	// Errors lists tags whose type did not parse; the tag is dropped.
	Errors []TagError
}

// ParamTag is @param T $name.
type ParamTag struct {
	Name     string
	Type     *TypeNode
	Variadic bool
	ByRef    bool
	Line     int // 0-based line within the comment
}

// VarTag is @var T [$name].
type VarTag struct {
	Name string
	Type *TypeNode
}

// TemplateTag is @template T [of|as Bound] [= Default].
type TemplateTag struct {
	Name     string
	Bound    *TypeNode
	Default  *TypeNode
	Variance Variance
}

// Variance of a template.
type Variance uint8

const (
	Invariant Variance = iota
	Covariant
	Contravariant
)

// TypeAliasTag is @phpstan-type Name = T (or @phpstan-type Name T).
type TypeAliasTag struct {
	Name string
	Type *TypeNode
}

// ImportTypeTag is @phpstan-import-type Name from Class [as Alias].
type ImportTypeTag struct {
	Name  string
	From  *TypeNode
	Alias string
}

// MethodTag is @method [static] [T] name(params).
type MethodTag struct {
	Static bool
	Return *TypeNode
	Name   string
	Params []CallableParam
}

// PropertyTag is @property[-read|-write] T $name.
type PropertyTag struct {
	Name  string
	Type  *TypeNode
	Read  bool
	Write bool
}

// AssertTag is @phpstan-assert[-if-true|-if-false] T $var.
type AssertTag struct {
	Type    *TypeNode
	Var     string
	IfTrue  bool
	IfFalse bool
	Negated bool
}

// TagError records a tag that could not be parsed.
type TagError struct {
	Tag  string
	Text string
	Msg  string
}

// Parse parses a doc comment (with or without the /** */ delimiters).
func Parse(comment string) *Doc {
	d := &Doc{}
	lines := cleanLines(comment)
	// Group lines into tags: a tag starts at '@' and continues until the
	// next tag; the text before the first tag is the summary.
	var summary []string
	var tags []tagText
	for i, l := range lines {
		if strings.HasPrefix(l, "@") {
			name, rest, _ := strings.Cut(l, " ")
			tags = append(tags, tagText{name: name, text: strings.TrimSpace(rest), line: i})
			continue
		}
		if len(tags) == 0 {
			summary = append(summary, l)
		} else if l != "" {
			t := &tags[len(tags)-1]
			t.text += " " + strings.TrimSpace(l)
		}
	}
	d.Summary = strings.TrimSpace(strings.Join(summary, "\n"))
	for _, t := range tags {
		d.handle(t)
	}
	return d
}

type tagText struct {
	name string
	text string
	line int
}

// cleanLines strips the comment delimiters and leading asterisks.
func cleanLines(c string) []string {
	c = strings.TrimSpace(c)
	c = strings.TrimPrefix(c, "/**")
	c = strings.TrimPrefix(c, "/*")
	c = strings.TrimSuffix(c, "*/")
	var out []string
	for _, l := range strings.Split(c, "\n") {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, "*")
		l = strings.TrimSpace(l)
		out = append(out, l)
	}
	return out
}

func (d *Doc) fail(t tagText, msg string) {
	d.Errors = append(d.Errors, TagError{Tag: t.name, Text: t.text, Msg: msg})
}

// handle dispatches one tag. PHPStan-prefixed tags override the plain ones;
// psalm-prefixed are accepted as aliases.
func (d *Doc) handle(t tagText) {
	name := strings.ToLower(t.name)
	prefixed := false
	for _, p := range []string{"@phpstan-", "@psalm-", "@phan-"} {
		if strings.HasPrefix(name, p) {
			name = "@" + strings.TrimPrefix(name, p)
			prefixed = true
			break
		}
	}
	switch name {
	case "@param", "@param-out":
		typ, rest, err := parseTypePrefix(t.text)
		if err != nil {
			d.fail(t, err.Error())
			return
		}
		p := ParamTag{Type: typ, Line: t.line}
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, "&") {
			p.ByRef = true
			rest = strings.TrimSpace(rest[1:])
		}
		if strings.HasPrefix(rest, "...") {
			p.Variadic = true
			rest = strings.TrimSpace(rest[3:])
		}
		if !strings.HasPrefix(rest, "$") {
			d.fail(t, "expected $name after the type")
			return
		}
		p.Name = identPrefix(rest[1:])
		if name == "@param-out" {
			d.ParamOut = append(d.ParamOut, p)
			return
		}
		d.Params = replaceParam(d.Params, p, prefixed)
	case "@return":
		typ, _, err := parseTypePrefix(t.text)
		if err != nil {
			d.fail(t, err.Error())
			return
		}
		if d.Return == nil || prefixed {
			d.Return = typ
		}
	case "@var":
		typ, rest, err := parseTypePrefix(t.text)
		if err != nil {
			d.fail(t, err.Error())
			return
		}
		v := VarTag{Type: typ}
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, "$") {
			v.Name = identPrefix(rest[1:])
		}
		if prefixed {
			// Replace an earlier plain @var for the same name.
			for i := range d.Vars {
				if d.Vars[i].Name == v.Name {
					d.Vars[i] = v
					return
				}
			}
		}
		d.Vars = append(d.Vars, v)
	case "@template", "@template-covariant", "@template-contravariant":
		tt, err := parseTemplate(t.text)
		if err != nil {
			d.fail(t, err.Error())
			return
		}
		switch name {
		case "@template-covariant":
			tt.Variance = Covariant
		case "@template-contravariant":
			tt.Variance = Contravariant
		}
		d.Templates = append(d.Templates, tt)
	case "@extends", "@template-extends":
		d.appendType(&d.Extends, t)
	case "@implements", "@template-implements":
		d.appendType(&d.Implements, t)
	case "@use", "@template-use":
		d.appendType(&d.Uses, t)
	case "@mixin":
		d.appendType(&d.Mixins, t)
	case "@throws":
		d.appendType(&d.Throws, t)
	case "@type":
		// @phpstan-type Name = T  |  @phpstan-type Name T
		nm := identPrefix(t.text)
		rest := strings.TrimSpace(t.text[len(nm):])
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "="))
		typ, _, err := parseTypePrefix(rest)
		if err != nil || nm == "" {
			d.fail(t, "malformed type alias")
			return
		}
		d.TypeAliases = append(d.TypeAliases, TypeAliasTag{Name: nm, Type: typ})
	case "@import-type":
		// @phpstan-import-type Name from Class [as Alias]
		fields := strings.Fields(t.text)
		if len(fields) < 3 || strings.ToLower(fields[1]) != "from" {
			d.fail(t, "malformed import-type")
			return
		}
		from, _, err := parseTypePrefix(fields[2])
		if err != nil {
			d.fail(t, err.Error())
			return
		}
		it := ImportTypeTag{Name: fields[0], From: from}
		if len(fields) >= 5 && strings.ToLower(fields[3]) == "as" {
			it.Alias = fields[4]
		}
		d.ImportTypes = append(d.ImportTypes, it)
	case "@method":
		m, err := parseMethodTag(t.text)
		if err != nil {
			d.fail(t, err.Error())
			return
		}
		d.Methods = append(d.Methods, m)
	case "@property", "@property-read", "@property-write":
		typ, rest, err := parseTypePrefix(t.text)
		if err != nil {
			d.fail(t, err.Error())
			return
		}
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "$") {
			d.fail(t, "expected $name")
			return
		}
		d.Properties = append(d.Properties, PropertyTag{Name: identPrefix(rest[1:]), Type: typ,
			Read: name != "@property-write", Write: name != "@property-read"})
	case "@assert", "@assert-if-true", "@assert-if-false":
		text := t.text
		a := AssertTag{IfTrue: name == "@assert-if-true", IfFalse: name == "@assert-if-false"}
		if strings.HasPrefix(text, "!") {
			a.Negated = true
			text = strings.TrimSpace(text[1:])
		}
		typ, rest, err := parseTypePrefix(text)
		if err != nil {
			d.fail(t, err.Error())
			return
		}
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "$") {
			d.fail(t, "expected $var")
			return
		}
		a.Type = typ
		a.Var = strings.TrimPrefix(rest, "$")
		d.Asserts = append(d.Asserts, a)
	case "@self-out", "@this-out":
		typ, _, err := parseTypePrefix(t.text)
		if err == nil {
			d.SelfOut = typ
		}
	case "@deprecated":
		d.Deprecated = true
	case "@internal":
		d.Internal = true
	case "@pure":
		d.Pure = true
	case "@immutable", "@readonly":
		d.Immutable = true
	case "@final":
		d.Final = true
	}
}

func (d *Doc) appendType(dst *[]*TypeNode, t tagText) {
	typ, _, err := parseTypePrefix(t.text)
	if err != nil {
		d.fail(t, err.Error())
		return
	}
	*dst = append(*dst, typ)
}

// replaceParam adds p, replacing an earlier tag for the same name when the
// new one is a prefixed (@phpstan-param) tag; a plain tag never replaces a
// prefixed one.
func replaceParam(ps []ParamTag, p ParamTag, prefixed bool) []ParamTag {
	for i := range ps {
		if ps[i].Name == p.Name {
			if prefixed {
				ps[i] = p
			}
			return ps
		}
	}
	return append(ps, p)
}

func parseTemplate(text string) (TemplateTag, error) {
	name := identPrefix(text)
	if name == "" {
		return TemplateTag{}, errorf("template name expected")
	}
	tt := TemplateTag{Name: name}
	rest := strings.TrimSpace(text[len(name):])
	lower := strings.ToLower(rest)
	if strings.HasPrefix(lower, "of ") || strings.HasPrefix(lower, "as ") {
		typ, r, err := parseTypePrefix(strings.TrimSpace(rest[3:]))
		if err != nil {
			return tt, err
		}
		tt.Bound = typ
		rest = strings.TrimSpace(r)
	}
	if strings.HasPrefix(rest, "=") {
		typ, _, err := parseTypePrefix(strings.TrimSpace(rest[1:]))
		if err != nil {
			return tt, err
		}
		tt.Default = typ
	}
	return tt, nil
}

// parseMethodTag parses `[static] [ReturnType] name(Type $a, Type $b = default)`.
func parseMethodTag(text string) (MethodTag, error) {
	m := MethodTag{}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "static ") {
		m.Static = true
		text = strings.TrimSpace(text[7:])
	}
	paren := strings.Index(text, "(")
	if paren < 0 {
		return m, errorf("method signature expected")
	}
	head := strings.TrimSpace(text[:paren])
	// head is "name" or "Type name"
	sp := strings.LastIndexAny(head, " \t")
	if sp >= 0 {
		typ, _, err := parseTypePrefix(strings.TrimSpace(head[:sp]))
		if err != nil {
			return m, err
		}
		m.Return = typ
		m.Name = strings.TrimSpace(head[sp+1:])
	} else {
		m.Name = head
	}
	close := strings.LastIndex(text, ")")
	if close < paren {
		return m, errorf("unclosed parameter list")
	}
	params := strings.TrimSpace(text[paren+1 : close])
	if params == "" {
		return m, nil
	}
	p := &typeParser{s: params}
	for {
		p.skipSpace()
		if p.eof() {
			break
		}
		var cp CallableParam
		if !p.peekVar() {
			typ, err := p.parseType()
			if err != nil {
				return m, err
			}
			cp.Type = typ
		}
		p.skipSpace()
		if p.accept("...") {
			cp.Variadic = true
		}
		if p.accept("&") {
			cp.ByRef = true
		}
		p.skipSpace()
		if p.accept("$") {
			cp.Name = p.ident()
		}
		p.skipSpace()
		if p.accept("=") {
			cp.Optional = true
			// skip the default value up to the next top-level comma
			depth := 0
			for !p.eof() {
				c := p.s[p.i]
				if c == '(' || c == '[' || c == '{' {
					depth++
				} else if c == ')' || c == ']' || c == '}' {
					depth--
				} else if c == ',' && depth == 0 {
					break
				}
				p.i++
			}
		}
		m.Params = append(m.Params, cp)
		p.skipSpace()
		if !p.accept(",") {
			break
		}
	}
	return m, nil
}

// identPrefix returns the identifier at the start of s.
func identPrefix(s string) string {
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80 || (i > 0 && c >= '0' && c <= '9') {
			i++
			continue
		}
		break
	}
	return s[:i]
}

// ParseType parses a standalone type string.
func ParseType(s string) (*TypeNode, error) {
	p := &typeParser{s: s}
	t, err := p.parseType()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if !p.eof() {
		return nil, errorf("unexpected %q", p.s[p.i:])
	}
	return t, nil
}

// parseTypePrefix parses the type at the start of s and returns the rest.
func parseTypePrefix(s string) (*TypeNode, string, error) {
	p := &typeParser{s: s}
	p.skipSpace()
	if p.eof() {
		return nil, "", errorf("type expected")
	}
	t, err := p.parseType()
	if err != nil {
		return nil, "", err
	}
	return t, p.s[p.i:], nil
}
