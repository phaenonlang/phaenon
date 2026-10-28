package docblock

import (
	"fmt"
	"strconv"
	"strings"
)

// NodeKind classifies a TypeNode.
type NodeKind uint8

const (
	// KindNamed is a name with optional type arguments: int, Foo, array<K, V>,
	// non-empty-string, class-string<T>, $this, static, self.
	KindNamed NodeKind = iota
	KindNullable
	KindUnion
	KindIntersection
	KindStringLit
	KindIntLit
	KindFloatLit
	// KindShape is array{...} / list{...} / object{...}.
	KindShape
	// KindCallable is callable(A, B): R, Closure(A): R, pure-callable(…).
	KindCallable
	// KindConst is Foo::BAR, Foo::*, Foo::PREFIX_*.
	KindConst
	// KindConditional is T is X ? A : B (also $param is X ? A : B).
	KindConditional
	// KindBoolLit is true / false; KindNull is null.
	KindBoolLit
	KindNull
)

// TypeNode is one node of a docblock type.
type TypeNode struct {
	Kind NodeKind
	// Name for KindNamed (as written: may be qualified, may start with \);
	// the shape keyword for KindShape ("array", "list", "object"); the
	// callable keyword for KindCallable; the class for KindConst.
	Name  string
	Args  []*TypeNode // type arguments; union/intersection members; nullable's inner
	Str   string      // string literal value; const name for KindConst
	Int   int64
	Float float64
	Bool  bool
	// Shapes
	Entries  []ShapeEntry
	Unsealed bool
	// Callables
	Params []CallableParam
	Return *TypeNode
	// Conditionals: Subject is X ? Then : Else; Negated for `is not`.
	Subject *TypeNode
	Target  *TypeNode
	Then    *TypeNode
	Else    *TypeNode
	Negated bool
}

// ShapeEntry is one key: T entry of a shape; a list{...} entry has no key.
type ShapeEntry struct {
	Key      string
	HasKey   bool
	IntKey   int64
	IsInt    bool
	Optional bool
	Type     *TypeNode
}

// CallableParam is one parameter of a callable signature.
type CallableParam struct {
	Name     string
	Type     *TypeNode
	Optional bool
	Variadic bool
	ByRef    bool
}

func (n *TypeNode) String() string {
	var b strings.Builder
	n.print(&b, false)
	return b.String()
}

func (n *TypeNode) print(b *strings.Builder, nested bool) {
	switch n.Kind {
	case KindNamed:
		b.WriteString(n.Name)
		if len(n.Args) > 0 {
			b.WriteString("<")
			for i, a := range n.Args {
				if i > 0 {
					b.WriteString(", ")
				}
				a.print(b, false)
			}
			b.WriteString(">")
		}
	case KindNullable:
		b.WriteString("?")
		n.Args[0].print(b, true)
	case KindUnion, KindIntersection:
		sep := "|"
		if n.Kind == KindIntersection {
			sep = "&"
		}
		if nested {
			b.WriteString("(")
		}
		for i, a := range n.Args {
			if i > 0 {
				b.WriteString(sep)
			}
			a.print(b, true)
		}
		if nested {
			b.WriteString(")")
		}
	case KindStringLit:
		b.WriteString("'")
		b.WriteString(n.Str)
		b.WriteString("'")
	case KindIntLit:
		b.WriteString(strconv.FormatInt(n.Int, 10))
	case KindFloatLit:
		b.WriteString(strconv.FormatFloat(n.Float, 'g', -1, 64))
	case KindBoolLit:
		if n.Bool {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case KindNull:
		b.WriteString("null")
	case KindShape:
		b.WriteString(n.Name)
		b.WriteString("{")
		for i, e := range n.Entries {
			if i > 0 {
				b.WriteString(", ")
			}
			if e.HasKey {
				if e.IsInt {
					b.WriteString(strconv.FormatInt(e.IntKey, 10))
				} else {
					b.WriteString(e.Key)
				}
				if e.Optional {
					b.WriteString("?")
				}
				b.WriteString(": ")
			}
			e.Type.print(b, false)
		}
		if n.Unsealed {
			if len(n.Entries) > 0 {
				b.WriteString(", ")
			}
			b.WriteString("...")
		}
		b.WriteString("}")
	case KindCallable:
		b.WriteString(n.Name)
		b.WriteString("(")
		for i, p := range n.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			if p.Type != nil {
				p.Type.print(b, false)
			} else {
				b.WriteString("mixed")
			}
			mark := ""
			if p.Variadic {
				mark += "..."
			}
			if p.ByRef {
				mark += "&"
			}
			if p.Name != "" {
				b.WriteString(" " + mark + "$" + p.Name)
			} else if mark != "" {
				b.WriteString(" " + mark)
			}
			if p.Optional {
				b.WriteString("=")
			}
		}
		b.WriteString(")")
		if n.Return != nil {
			b.WriteString(": ")
			n.Return.print(b, true)
		}
	case KindConst:
		b.WriteString(n.Name)
		b.WriteString("::")
		b.WriteString(n.Str)
	case KindConditional:
		b.WriteString("(")
		n.Subject.print(b, false)
		if n.Negated {
			b.WriteString(" is not ")
		} else {
			b.WriteString(" is ")
		}
		n.Target.print(b, false)
		b.WriteString(" ? ")
		n.Then.print(b, false)
		b.WriteString(" : ")
		n.Else.print(b, false)
		b.WriteString(")")
	}
}

// ------------------------------------------------------------------ parser

type typeParser struct {
	s string
	i int
}

type parseError struct{ msg string }

func (e *parseError) Error() string { return e.msg }

func errorf(format string, args ...any) error {
	return &parseError{fmt.Sprintf(format, args...)}
}

func (p *typeParser) eof() bool { return p.i >= len(p.s) }

func (p *typeParser) skipSpace() {
	for !p.eof() && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *typeParser) peek(s string) bool { return strings.HasPrefix(p.s[p.i:], s) }

func (p *typeParser) accept(s string) bool {
	if p.peek(s) {
		p.i += len(s)
		return true
	}
	return false
}

func (p *typeParser) peekVar() bool { return p.peek("$") }

func (p *typeParser) ident() string {
	id := identPrefix(p.s[p.i:])
	p.i += len(id)
	return id
}

// name reads a (qualified) type name: \Foo\Bar, non-empty-string, $this.
func (p *typeParser) name() string {
	start := p.i
	if p.accept("$") {
		p.ident()
		return p.s[start:p.i]
	}
	for !p.eof() {
		c := p.s[p.i]
		switch {
		case c == '\\', c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c >= 0x80:
			p.i++
		case c == '-' && p.i+1 < len(p.s) && isAlpha(p.s[p.i+1]) && p.i > start:
			p.i++
		default:
			return p.s[start:p.i]
		}
	}
	return p.s[start:p.i]
}

func isAlpha(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// parseType parses a full type: union of intersections of atoms, with the
// conditional form on top.
func (p *typeParser) parseType() (*TypeNode, error) {
	p.skipSpace()
	// Conditional: `T is X ? A : B` / `$param is not X ? A : B`.
	t, err := p.parseUnion()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.peek("is ") || p.peek("is\t") {
		p.i += 2
		p.skipSpace()
		neg := false
		if p.accept("not ") {
			neg = true
			p.skipSpace()
		}
		target, err := p.parseUnion()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if !p.accept("?") {
			return nil, errorf("expected ? in conditional type")
		}
		then, err := p.parseType()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if !p.accept(":") {
			return nil, errorf("expected : in conditional type")
		}
		els, err := p.parseType()
		if err != nil {
			return nil, err
		}
		return &TypeNode{Kind: KindConditional, Subject: t, Target: target, Then: then, Else: els, Negated: neg}, nil
	}
	return t, nil
}

func (p *typeParser) parseUnion() (*TypeNode, error) {
	first, err := p.parseIntersection()
	if err != nil {
		return nil, err
	}
	members := []*TypeNode{first}
	for {
		p.skipSpace()
		if !p.accept("|") {
			break
		}
		m, err := p.parseIntersection()
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	if len(members) == 1 {
		return first, nil
	}
	return &TypeNode{Kind: KindUnion, Args: members}, nil
}

func (p *typeParser) parseIntersection() (*TypeNode, error) {
	first, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	members := []*TypeNode{first}
	for {
		save := p.i
		p.skipSpace()
		// `&` followed by `$` or `...` is a by-ref marker in a callable
		// parameter list, not an intersection.
		if !p.accept("&") || p.peek("$") || p.peek("...") {
			p.i = save
			break
		}
		m, err := p.parseAtom()
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	if len(members) == 1 {
		return first, nil
	}
	return &TypeNode{Kind: KindIntersection, Args: members}, nil
}

func (p *typeParser) parseAtom() (*TypeNode, error) {
	p.skipSpace()
	if p.eof() {
		return nil, errorf("type expected")
	}
	c := p.s[p.i]
	switch {
	case c == '?':
		p.i++
		inner, err := p.parseAtom()
		if err != nil {
			return nil, err
		}
		return &TypeNode{Kind: KindNullable, Args: []*TypeNode{inner}}, nil
	case c == '(':
		p.i++
		inner, err := p.parseType()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if !p.accept(")") {
			return nil, errorf("expected )")
		}
		return p.suffix(inner)
	case c == '\'' || c == '"':
		p.i++
		start := p.i
		for !p.eof() && p.s[p.i] != c {
			if p.s[p.i] == '\\' {
				p.i++
			}
			p.i++
		}
		if p.eof() {
			return nil, errorf("unterminated string literal")
		}
		raw := p.s[start:p.i]
		p.i++
		return p.suffix(&TypeNode{Kind: KindStringLit, Str: strings.NewReplacer(`\'`, `'`, `\"`, `"`, `\\`, `\`).Replace(raw)})
	case c == '-' || c == '+' || c >= '0' && c <= '9':
		return p.suffix(p.number())
	}
	name := p.name()
	if name == "" {
		return nil, errorf("unexpected %q", p.s[p.i:])
	}
	lower := strings.ToLower(name)
	switch lower {
	case "null":
		return p.suffix(&TypeNode{Kind: KindNull})
	case "true", "false":
		return p.suffix(&TypeNode{Kind: KindBoolLit, Bool: lower == "true"})
	}
	n := &TypeNode{Kind: KindNamed, Name: name}
	p.skipSpace()
	// Foo::CONST, Foo::*, Foo::PREFIX_*
	if p.accept("::") {
		start := p.i
		for !p.eof() && (isAlpha(p.s[p.i]) || p.s[p.i] >= '0' && p.s[p.i] <= '9' || p.s[p.i] == '*') {
			p.i++
		}
		return p.suffix(&TypeNode{Kind: KindConst, Name: name, Str: p.s[start:p.i]})
	}
	// Shapes: array{...}, list{...}, object{...}, non-empty-array{...}
	if p.peek("{") {
		return p.parseShape(name)
	}
	// Callables: callable(...), Closure(...), pure-callable(...), \Closure(...)
	if p.peek("(") && isCallableName(lower) {
		return p.parseCallable(name)
	}
	// Generic arguments.
	if p.accept("<") {
		for {
			p.skipSpace()
			// int<min, max> uses bare min/max; `*` wildcard (Psalm) — read as names.
			arg, err := p.parseType()
			if err != nil {
				return nil, err
			}
			n.Args = append(n.Args, arg)
			p.skipSpace()
			if p.accept(",") {
				continue
			}
			if p.accept(">") {
				break
			}
			return nil, errorf("expected , or > in type arguments of %s", name)
		}
	}
	return p.suffix(n)
}

// suffix handles postfix array syntax: T[] (any depth).
func (p *typeParser) suffix(n *TypeNode) (*TypeNode, error) {
	for {
		save := p.i
		p.skipSpace()
		if p.accept("[]") {
			n = &TypeNode{Kind: KindNamed, Name: "array", Args: []*TypeNode{n}}
			continue
		}
		p.i = save
		return n, nil
	}
}

func isCallableName(lower string) bool {
	switch strings.TrimPrefix(lower, "\\") {
	case "callable", "closure", "pure-callable", "pure-closure":
		return true
	}
	return false
}

func (p *typeParser) number() *TypeNode {
	start := p.i
	if p.s[p.i] == '-' || p.s[p.i] == '+' {
		p.i++
	}
	isFloat := false
	for !p.eof() {
		c := p.s[p.i]
		if c >= '0' && c <= '9' || c == '_' {
			p.i++
		} else if c == '.' && p.i+1 < len(p.s) && p.s[p.i+1] >= '0' && p.s[p.i+1] <= '9' {
			isFloat = true
			p.i++
		} else if (c == 'e' || c == 'E') && p.i+1 < len(p.s) && (p.s[p.i+1] >= '0' && p.s[p.i+1] <= '9' || p.s[p.i+1] == '-' || p.s[p.i+1] == '+') {
			isFloat = true
			p.i += 2
		} else {
			break
		}
	}
	raw := strings.ReplaceAll(p.s[start:p.i], "_", "")
	if isFloat {
		f, _ := strconv.ParseFloat(raw, 64)
		return &TypeNode{Kind: KindFloatLit, Float: f}
	}
	i, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		f, _ := strconv.ParseFloat(raw, 64)
		return &TypeNode{Kind: KindFloatLit, Float: f}
	}
	return &TypeNode{Kind: KindIntLit, Int: i}
}

func (p *typeParser) parseShape(name string) (*TypeNode, error) {
	p.accept("{")
	n := &TypeNode{Kind: KindShape, Name: name}
	for {
		p.skipSpace()
		if p.accept("}") {
			return n, nil
		}
		if p.accept("...") {
			n.Unsealed = true
			p.skipSpace()
			p.accept(",")
			continue
		}
		var e ShapeEntry
		// Try `key?: type` — key is an identifier, a quoted string, or an int.
		save := p.i
		key, isInt, intKey, hasKey := p.shapeKey()
		p.skipSpace()
		if hasKey && (p.accept("?:") || p.accept(":")) {
			e.HasKey, e.Key, e.IsInt, e.IntKey = true, key, isInt, intKey
			e.Optional = p.s[p.i-2] == '?'
		} else {
			p.i = save
		}
		t, err := p.parseType()
		if err != nil {
			return nil, err
		}
		e.Type = t
		n.Entries = append(n.Entries, e)
		p.skipSpace()
		if p.accept(",") {
			continue
		}
		if p.accept("}") {
			return n, nil
		}
		return nil, errorf("expected , or } in shape")
	}
}

func (p *typeParser) shapeKey() (key string, isInt bool, intKey int64, ok bool) {
	if p.eof() {
		return "", false, 0, false
	}
	c := p.s[p.i]
	switch {
	case c == '\'' || c == '"':
		p.i++
		start := p.i
		for !p.eof() && p.s[p.i] != c {
			p.i++
		}
		if p.eof() {
			return "", false, 0, false
		}
		key = p.s[start:p.i]
		p.i++
		return key, false, 0, true
	case c == '-' || c >= '0' && c <= '9':
		start := p.i
		if c == '-' {
			p.i++
		}
		for !p.eof() && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
		}
		v, err := strconv.ParseInt(p.s[start:p.i], 10, 64)
		if err != nil {
			return "", false, 0, false
		}
		return "", true, v, true
	}
	id := p.name()
	if id == "" || strings.Contains(id, "\\") {
		return "", false, 0, false
	}
	return id, false, 0, true
}

func (p *typeParser) parseCallable(name string) (*TypeNode, error) {
	p.accept("(")
	n := &TypeNode{Kind: KindCallable, Name: name}
	for {
		p.skipSpace()
		if p.accept(")") {
			break
		}
		var cp CallableParam
		if !p.peekVar() && !p.peek("...") && !p.peek("&") {
			t, err := p.parseType()
			if err != nil {
				return nil, err
			}
			cp.Type = t
		}
		p.skipSpace()
		if p.accept("...") {
			cp.Variadic = true
			p.skipSpace()
		}
		if p.accept("&") {
			cp.ByRef = true
			p.skipSpace()
		}
		if p.accept("$") {
			cp.Name = p.ident()
			p.skipSpace()
		}
		if p.accept("=") {
			cp.Optional = true
			p.skipSpace()
		}
		n.Params = append(n.Params, cp)
		if p.accept(",") {
			continue
		}
		if p.accept(")") {
			break
		}
		return nil, errorf("expected , or ) in callable parameters")
	}
	save := p.i
	p.skipSpace()
	if p.accept(":") {
		p.skipSpace()
		// The return type binds tighter than | at this level: callable(): int|string
		// is (callable(): int)|string in PHPStan; a parenthesised type groups.
		ret, err := p.parseAtom()
		if err != nil {
			return nil, err
		}
		n.Return = ret
	} else {
		p.i = save
	}
	return n, nil
}
