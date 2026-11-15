package parser

import (
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/token"
)

// A shape, `array{id: int, name?: string}`, is syntax in the right-hand
// side of a `type` declaration, as the whole type or nested anywhere in
// it, and nowhere else (design/collections.md §6.1, roadmap D29). Between
// `type Name =` and its `;` a brace after `array` can only be a shape.
// Anywhere else it is a mistake with one remedy, to name the shape, and
// the error says so.
//
// One other position has a brace after `array` that is PHP's own: a
// return type before a body, `find(int $id): array {`. There the brace
// opens the body unless what follows it can only begin a shape's entry,
// which no Phaenon body begins as: `id:` is a label, and Phaenon has none.
// The parser then reads the shape, so that the body after it is read as
// one, and reports the shape when the file is parsed.

// misplacedShape is the error of a shape written outside a `type`
// declaration.
const misplacedShape = "a shape is written in a `type` declaration: name it, `type Row = array{…};`, and write the name here"

// aliasedType parses the right-hand side of a `type` declaration.
func (p *Parser) aliasedType() ast.TypeExpr {
	saved := p.shapes
	p.shapes = true
	t := p.typeExpr()
	p.shapes = saved
	return t
}

// typeBeforeBody parses a type that a `{` of PHP's own may follow: a
// return type before a body, an enum's backing type before its members.
// member says the body is a method's, which no `;` follows.
func (p *Parser) typeBeforeBody(member bool) ast.TypeExpr {
	saved, savedMember := p.bodyType, p.memberBody
	p.bodyType, p.memberBody = p.i+1, member
	t := p.typeExpr()
	p.bodyType, p.memberBody = saved, savedMember
	return t
}

// shapeAhead reports whether the `{` at the current token, after the type
// name just read, opens a shape.
func (p *Parser) shapeAhead(name *ast.Name) bool {
	if len(name.Parts) != 1 || name.Kind != ast.NameUnqualified {
		return false
	}
	switch word := strings.ToLower(name.Parts[0]); word {
	case "array":
	case "list", "object":
		if p.shapes {
			p.errorf("%s{…} stays docblock vocabulary: a shape is written array{…}, and a positional one is a tuple, (A, B)", word)
		}
		return false
	default:
		return false
	}
	if p.shapes || !p.bodyMayFollow() {
		return true
	}
	return p.entryAhead()
}

// bodyMayFollow reports whether the type being parsed ends where a body
// may begin: it is a typeBeforeBody, and the current token is at its own
// level, outside the type arguments, the tuples and the closure parameters
// it holds.
func (p *Parser) bodyMayFollow() bool {
	if p.bodyType == 0 {
		return false
	}
	depth := 0
	for _, t := range p.toks[p.bodyType-1 : p.i] {
		switch t.Kind {
		case token.Kind('('), token.Kind('<'):
			depth++
		case token.Kind(')'), token.Kind('>'):
			depth--
		case token.T_SR:
			depth -= 2
		}
	}
	return depth <= 0
}

// entryAhead reports whether the braces opening at the current token can
// only be a shape's: they begin as an entry does, or are empty before
// another body.
func (p *Parser) entryAhead() bool {
	first := p.peek(1)
	switch first.Kind {
	case token.Kind('}'):
		// `array{}` before the body, the arrow or, on a method without a
		// body, the `;`; after a function's `{}` a `;` is a statement.
		k := p.peekKind(2)
		return k == token.Kind('{') || k == token.T_DOUBLE_ARROW || k == token.Kind(';') && p.memberBody
	case token.T_ELLIPSIS:
		return true
	}
	i := 1
	if first.Kind == token.Kind('-') {
		if i++; p.peekKind(i) != token.T_LNUMBER {
			return false
		}
	}
	switch k := p.peekKind(i); {
	case k == token.T_STRING, k == token.T_CONSTANT_ENCAPSED_STRING, k == token.T_LNUMBER, isSemiReserved(k):
	default:
		return false
	}
	switch p.peekKind(i + 1) {
	case token.Kind(':'):
		return true
	case token.Kind('?'):
		// `name ?: expr;` is a statement too: a shape holds no `;`.
		return p.peekKind(i+2) == token.Kind(':') && !p.statementInBraces()
	}
	return false
}

// statementInBraces reports whether a `;` stands directly inside the
// braces opening at the current token.
func (p *Parser) statementInBraces() bool {
	depth := 0
	for j := p.i; j < len(p.toks); j++ {
		switch p.toks[j].Kind {
		case token.Kind('{'), token.Kind('('), token.Kind('['), token.T_CURLY_OPEN, token.T_DOLLAR_OPEN_CURLY_BRACES, token.T_ATTRIBUTE:
			depth++
		case token.Kind('}'), token.Kind(')'), token.Kind(']'):
			if depth--; depth == 0 {
				return false
			}
		case token.Kind(';'):
			if depth == 1 {
				return true
			}
		case token.EOF:
			return true
		}
	}
	return true
}

// shapeType parses array{key: T, other?: U, ...} from its `{`, the name
// `array` read. Every entry has a key; `...` as the last one leaves the
// shape unsealed; a trailing comma is allowed.
func (p *Parser) shapeType(start int) ast.TypeExpr {
	if !p.shapes && p.misplaced == nil {
		t := p.tok()
		p.misplaced = &Error{Offset: t.Start, Line: t.Line, Msg: misplacedShape}
	}
	p.expect(token.Kind('{'))
	st := &ast.ShapeType{}
	for !p.at(token.Kind('}')) {
		if p.accept(token.T_ELLIPSIS) {
			if p.at(token.Kind('<')) {
				p.errorf("a shape's rest is written `...`: the typed form, `...<K, V>`, is not read")
			}
			st.Unsealed = true
			p.accept(token.Kind(','))
			if !p.at(token.Kind('}')) {
				p.errorf("`...` is a shape's last entry")
			}
			break
		}
		st.Entries = append(st.Entries, p.shapeEntry())
		if !p.accept(token.Kind(',')) {
			break
		}
	}
	p.expect(token.Kind('}'))
	st.S = p.span(start)
	return st
}

// shapeEntry parses `key: T` or `key?: T`. The keys are the ones the
// docblock grammar reads (docblock's shapeKey): a name, PHP's reserved
// words included, as after `->`; a quoted string for a key that is not a
// name; an integer.
func (p *Parser) shapeEntry() ast.ShapeEntry {
	t := p.tok()
	e := ast.ShapeEntry{}
	switch {
	case t.Kind == token.T_CONSTANT_ENCAPSED_STRING:
		p.next()
		e.Raw = t.Text(p.src)
		e.Name = p.quotedKey(t, e.Raw)
	case t.Kind == token.T_LNUMBER, t.Kind == token.Kind('-') && p.peekKind(1) == token.T_LNUMBER:
		if p.accept(token.Kind('-')) {
			e.Raw = "-"
		}
		digits := p.next().Text(p.src)
		e.Raw += digits
		v, err := strconv.ParseInt(e.Raw, 10, 64)
		if err != nil || len(digits) > 1 && digits[0] == '0' {
			p.errorAt(t, "a shape's integer key is written in decimal, as 0 or -1: %s is not", e.Raw)
		}
		e.IsInt, e.Int = true, v
	case t.Kind == token.Kind('"'):
		p.errorf("a shape's quoted key is a plain string: it holds no interpolation")
	case (t.Kind == token.T_STRING || isSemiReserved(t.Kind)) && (p.peekKind(1) == token.Kind(':') || p.peekKind(1) == token.Kind('?')):
		p.next()
		e.Raw = t.Text(p.src)
		e.Name = e.Raw
	default:
		p.errorf("every entry of a shape has a key, `key: T`: a positional shape is a tuple, (A, B)")
	}
	e.Key = ast.Span{Start: t.Start, End: p.end()}
	e.Optional = p.accept(token.Kind('?'))
	p.expect(token.Kind(':'))
	e.Type = p.typeExpr()
	e.S = p.span(t.Start)
	return e
}

// quotedKey is the key a quoted string spells: the text between its
// quotes, as a docblock's shape reads one. An escape sequence would make
// the two readings differ, so a key holds none.
func (p *Parser) quotedKey(t token.Token, raw string) string {
	if len(raw) < 2 || raw[0] != '\'' && raw[0] != '"' {
		p.errorAt(t, "a shape's quoted key is a plain string, 'key' or \"key\": %s is not", raw)
	}
	key := raw[1 : len(raw)-1]
	for i := 0; i+1 < len(key); i++ {
		if key[i] != '\\' {
			continue
		}
		escapes := `\'`
		if raw[0] == '"' {
			escapes = `\"$nrtvefxu01234567`
		}
		if strings.IndexByte(escapes, key[i+1]) >= 0 {
			p.errorAt(t, "a shape's quoted key is taken as written and holds no escape sequence: %s", raw)
		}
	}
	return key
}
