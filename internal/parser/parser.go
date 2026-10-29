// Package parser builds the AST of a PHP or Phaenon source file.
//
// It is a hand-written recursive-descent parser; expressions use Pratt
// parsing with the precedence table of Zend/zend_language_parser.y. The
// parser works on the lossless token stream of the lexer, skipping trivia
// but remembering doc comments for the declarations that follow them.
package parser

import (
	"fmt"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/token"
)

// Error is a syntax error.
type Error struct {
	Offset int
	Line   int
	Msg    string
}

func (e *Error) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// bailout is panicked on the first syntax error and recovered in Parse.
// Error recovery (for the LSP) is a later milestone.
type bailout struct{ err *Error }

// Parser holds the state of one parse.
type Parser struct {
	src  []byte
	toks []token.Token
	i    int // index of the current (non-trivia) token
	mode lexer.Mode
	// docComment is the doc comment that immediately precedes the current token.
	docComment string
	// noAs is set while parsing a foreach subject so that `as` ends the
	// expression instead of starting Phaenon's `x as T`. Nested contexts
	// (parentheses, argument lists, closures) clear it through expr().
	noAs bool
}

// Parse parses a whole file.
func Parse(src []byte, mode lexer.Mode) (file *ast.File, err error) {
	p := &Parser{src: src, toks: lexer.Tokenize(src, mode), mode: mode}
	p.i = -1
	p.advance()
	defer func() {
		if r := recover(); r != nil {
			if b, ok := r.(bailout); ok {
				file, err = nil, b.err
				return
			}
			panic(r)
		}
	}()
	f := &ast.File{}
	f.Stmts = p.statementList(token.EOF)
	f.S = ast.Span{Start: 0, End: len(src)}
	return f, nil
}

// ParseExpr parses a standalone expression (used by tests and tooling).
func ParseExpr(src []byte, mode lexer.Mode) (expr ast.Expr, err error) {
	p := &Parser{src: src, toks: lexer.Tokenize(src, mode), mode: mode}
	p.i = -1
	p.advance()
	defer func() {
		if r := recover(); r != nil {
			if b, ok := r.(bailout); ok {
				expr, err = nil, b.err
				return
			}
			panic(r)
		}
	}()
	e := p.expr()
	p.expect(token.EOF)
	return e, nil
}

// ------------------------------------------------------------------ token stream

func (p *Parser) tok() token.Token { return p.toks[p.i] }
func (p *Parser) kind() token.Kind { return p.toks[p.i].Kind }
func (p *Parser) text() string     { return p.toks[p.i].Text(p.src) }
func (p *Parser) at(k token.Kind) bool {
	return p.toks[p.i].Kind == k
}

// advance moves to the next non-trivia token, collecting a doc comment.
func (p *Parser) advance() {
	p.docComment = ""
	for {
		p.i++
		k := p.toks[p.i].Kind
		if k == token.T_DOC_COMMENT {
			p.docComment = p.text()
			continue
		}
		if k.IsTrivia() {
			continue
		}
		return
	}
}

// next consumes the current token and returns it.
func (p *Parser) next() token.Token {
	t := p.toks[p.i]
	if t.Kind != token.EOF {
		p.advance()
	}
	return t
}

// peek returns the n-th non-trivia token after the current one (n ≥ 1).
func (p *Parser) peek(n int) token.Token {
	j := p.i
	for ; n > 0; n-- {
		for {
			j++
			if j >= len(p.toks) {
				return p.toks[len(p.toks)-1]
			}
			if !p.toks[j].Kind.IsTrivia() {
				break
			}
		}
	}
	return p.toks[j]
}

func (p *Parser) peekKind(n int) token.Kind { return p.peek(n).Kind }

func (p *Parser) accept(k token.Kind) bool {
	if p.at(k) {
		p.advance()
		return true
	}
	return false
}

func (p *Parser) expect(k token.Kind) token.Token {
	if !p.at(k) {
		p.errorf("unexpected %s, expecting %s", p.describe(), describeKind(k))
	}
	return p.next()
}

func (p *Parser) errorf(format string, args ...any) {
	t := p.tok()
	panic(bailout{&Error{Offset: t.Start, Line: t.Line, Msg: fmt.Sprintf(format, args...)}})
}

func (p *Parser) describe() string {
	t := p.tok()
	if t.Kind == token.EOF {
		return "end of file"
	}
	return fmt.Sprintf("token %q (%s)", t.Text(p.src), t.Kind)
}

func describeKind(k token.Kind) string {
	if k < 256 {
		return fmt.Sprintf("%q", string(rune(k)))
	}
	return k.String()
}

// semi expects a statement terminator: ';' or, in PHP mode, a close tag
// (which is left in the stream for the statement list to turn into a node).
func (p *Parser) semi() {
	if p.at(token.T_CLOSE_TAG) {
		return
	}
	p.expect(token.Kind(';'))
}

// end returns the end offset of the last consumed token.
func (p *Parser) end() int {
	for j := p.i - 1; j >= 0; j-- {
		if !p.toks[j].Kind.IsTrivia() {
			return p.toks[j].End
		}
	}
	return 0
}

func (p *Parser) span(start int) ast.Span { return ast.Span{Start: start, End: p.end()} }

// takeDoc returns the doc comment preceding the current token, once.
func (p *Parser) takeDoc() string {
	d := p.docComment
	p.docComment = ""
	return d
}

// ------------------------------------------------------------------ names and identifiers

// isReservedNonModifier reports keywords usable as identifiers after -> and ::
// and as member names (reserved_non_modifiers in the grammar).
func isReservedNonModifier(k token.Kind) bool {
	switch k {
	case token.T_INCLUDE, token.T_INCLUDE_ONCE, token.T_EVAL, token.T_REQUIRE, token.T_REQUIRE_ONCE,
		token.T_LOGICAL_OR, token.T_LOGICAL_XOR, token.T_LOGICAL_AND, token.T_INSTANCEOF, token.T_NEW,
		token.T_CLONE, token.T_EXIT, token.T_IF, token.T_ELSEIF, token.T_ELSE, token.T_ENDIF, token.T_ECHO,
		token.T_DO, token.T_WHILE, token.T_ENDWHILE, token.T_FOR, token.T_ENDFOR, token.T_FOREACH,
		token.T_ENDFOREACH, token.T_DECLARE, token.T_ENDDECLARE, token.T_AS, token.T_TRY, token.T_CATCH,
		token.T_FINALLY, token.T_THROW, token.T_USE, token.T_INSTEADOF, token.T_GLOBAL, token.T_VAR,
		token.T_UNSET, token.T_ISSET, token.T_EMPTY, token.T_CONTINUE, token.T_GOTO, token.T_FUNCTION,
		token.T_CONST, token.T_RETURN, token.T_PRINT, token.T_YIELD, token.T_LIST, token.T_SWITCH,
		token.T_ENDSWITCH, token.T_CASE, token.T_DEFAULT, token.T_BREAK, token.T_ARRAY, token.T_CALLABLE,
		token.T_EXTENDS, token.T_IMPLEMENTS, token.T_NAMESPACE, token.T_TRAIT, token.T_INTERFACE,
		token.T_CLASS, token.T_CLASS_C, token.T_TRAIT_C, token.T_FUNC_C, token.T_METHOD_C, token.T_LINE,
		token.T_FILE, token.T_DIR, token.T_NS_C, token.T_FN, token.T_MATCH, token.T_ENUM, token.T_PROPERTY_C,
		// Phaenon contextual keywords are identifiers everywhere PHP allows a keyword.
		token.T_IS, token.T_TYPE, token.T_OF, token.T_IN, token.T_OUT:
		return true
	}
	return false
}

func isModifierKeyword(k token.Kind) bool {
	switch k {
	case token.T_STATIC, token.T_ABSTRACT, token.T_FINAL, token.T_PRIVATE, token.T_PROTECTED,
		token.T_PUBLIC, token.T_READONLY, token.T_PRIVATE_SET, token.T_PROTECTED_SET, token.T_PUBLIC_SET:
		return true
	}
	return false
}

// isSemiReserved reports the `identifier` rule: T_STRING or any keyword.
func isSemiReserved(k token.Kind) bool {
	return isReservedNonModifier(k) || isModifierKeyword(k)
}

// isPhaenonContextual reports Phaenon's contextual keywords, which are plain
// identifiers whenever the grammar wants a T_STRING.
func isPhaenonContextual(k token.Kind) bool {
	switch k {
	case token.T_TYPE, token.T_OF, token.T_IN, token.T_OUT:
		return true
	}
	return false
}

// atString reports a T_STRING or a Phaenon contextual keyword in a T_STRING position.
func (p *Parser) atString() bool {
	return p.at(token.T_STRING) || isPhaenonContextual(p.kind())
}

// identifier parses `identifier` (T_STRING or semi_reserved).
func (p *Parser) identifier() *ast.Ident {
	if !(p.at(token.T_STRING) || isSemiReserved(p.kind())) {
		p.errorf("unexpected %s, expecting identifier", p.describe())
	}
	t := p.next()
	return &ast.Ident{S: ast.Span{Start: t.Start, End: t.End}, Name: t.Text(p.src)}
}

// stringIdent parses a plain T_STRING (no keywords).
func (p *Parser) stringIdent() *ast.Ident {
	if !p.atString() {
		p.errorf("unexpected %s, expecting identifier", p.describe())
	}
	t := p.next()
	return &ast.Ident{S: ast.Span{Start: t.Start, End: t.End}, Name: t.Text(p.src)}
}

func (p *Parser) atName() bool {
	switch p.kind() {
	case token.T_STRING, token.T_NAME_QUALIFIED, token.T_NAME_FULLY_QUALIFIED, token.T_NAME_RELATIVE:
		return true
	}
	return isPhaenonContextual(p.kind())
}

// name parses the `name` rule.
func (p *Parser) name() *ast.Name {
	if !p.atName() {
		p.errorf("unexpected %s, expecting name", p.describe())
	}
	t := p.next()
	return nameFromToken(t, p.src)
}

func nameFromToken(t token.Token, src []byte) *ast.Name {
	n := &ast.Name{S: ast.Span{Start: t.Start, End: t.End}}
	text := t.Text(src)
	switch t.Kind {
	case token.T_NAME_FULLY_QUALIFIED:
		n.Kind = ast.NameFullyQualified
		text = text[1:]
	case token.T_NAME_QUALIFIED:
		n.Kind = ast.NameQualified
	case token.T_NAME_RELATIVE:
		n.Kind = ast.NameRelative
		text = text[len("namespace\\"):]
	default:
		n.Kind = ast.NameUnqualified
	}
	n.Parts = splitName(text)
	return n
}

func splitName(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// className parses class_name: `static` or a name.
func (p *Parser) className() *ast.Name {
	if p.at(token.T_STATIC) {
		t := p.next()
		return &ast.Name{S: ast.Span{Start: t.Start, End: t.End}, Parts: []string{"static"}}
	}
	return p.name()
}

func (p *Parser) variable() *ast.Variable {
	t := p.expect(token.T_VARIABLE)
	return &ast.Variable{S: ast.Span{Start: t.Start, End: t.End}, Name: t.Text(p.src)[1:]}
}

// ------------------------------------------------------------------ types

// typeExpr parses type_expr: T, ?T, A|B, A&B, (A&B)|C, and Phaenon generics.
func (p *Parser) typeExpr() ast.TypeExpr {
	start := p.tok().Start
	if p.accept(token.Kind('?')) {
		t := p.singleType()
		return &ast.NullableType{S: p.span(start), Type: t}
	}
	first := p.unionMember()
	if p.at(token.Kind('|')) {
		types := []ast.TypeExpr{first}
		for p.accept(token.Kind('|')) {
			types = append(types, p.unionMember())
		}
		return &ast.UnionType{S: p.span(start), Types: types}
	}
	if p.at(token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG) {
		return p.intersectionRest(start, first, false)
	}
	return first
}

// unionMember parses a type, a parenthesised intersection (A&B) or, in
// Phaenon, a tuple (A, B).
func (p *Parser) unionMember() ast.TypeExpr {
	start := p.tok().Start
	if p.at(token.Kind('(')) && !p.atClosureTypeStart() {
		p.next()
		first := p.singleType()
		if p.mode == lexer.ModePhaenon && p.at(token.Kind(',')) {
			tt := &ast.TupleType{Elems: []ast.TypeExpr{first}}
			for p.accept(token.Kind(',')) {
				tt.Elems = append(tt.Elems, p.typeExpr())
			}
			p.expect(token.Kind(')'))
			tt.S = p.span(start)
			return tt
		}
		t := p.intersectionRest(start, first, true)
		p.expect(token.Kind(')'))
		t.S = p.span(start)
		return t
	}
	return p.singleType()
}

func (p *Parser) intersectionRest(start int, first ast.TypeExpr, parenthesised bool) *ast.IntersectionType {
	types := []ast.TypeExpr{first}
	for p.accept(token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG) {
		types = append(types, p.singleType())
	}
	return &ast.IntersectionType{S: p.span(start), Types: types, Parenthesised: parenthesised}
}

func (p *Parser) atClosureTypeStart() bool { return false }

// singleType parses a named type: int, Foo, static, array, callable, Foo<T>, class<T>, Closure(A): R.
func (p *Parser) singleType() ast.TypeExpr {
	start := p.tok().Start
	var name *ast.Name
	switch {
	case p.at(token.T_ARRAY), p.at(token.T_CALLABLE), p.at(token.T_STATIC), p.at(token.T_LIST):
		t := p.next()
		name = &ast.Name{S: ast.Span{Start: t.Start, End: t.End}, Parts: []string{t.Text(p.src)}}
	case p.mode == lexer.ModePhaenon && p.at(token.T_CLASS):
		// `class<T>` is the type of T::class (design/generics.md §11.2). PHP
		// never puts `class` in a type position, so the keyword is free here.
		t := p.next()
		name = &ast.Name{S: ast.Span{Start: t.Start, End: t.End}, Parts: []string{"class"}}
	default:
		name = p.name()
	}
	nt := &ast.NamedType{S: p.span(start), Name: name}
	if p.mode == lexer.ModePhaenon {
		if len(name.Parts) == 1 && name.Parts[0] == "Closure" && (p.at(token.Kind('(')) || p.atCast()) {
			return p.closureType(start)
		}
		if p.at(token.Kind('<')) {
			nt.TypeArgs = p.typeArgs()
			nt.S = p.span(start)
		}
	}
	return nt
}

// closureType parses Phaenon's Closure(A, B): R after the Closure name.
func (p *Parser) closureType(start int) ast.TypeExpr {
	var params []ast.TypeExpr
	if p.atCast() {
		// `Closure(int): string` — the lexer read `(int)` as a cast token.
		t := p.next()
		text := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t.Text(p.src), "("), ")"))
		params = append(params, &ast.NamedType{S: ast.Span{Start: t.Start + 1, End: t.End - 1},
			Name: &ast.Name{S: ast.Span{Start: t.Start + 1, End: t.End - 1}, Kind: ast.NameUnqualified, Parts: []string{text}}})
	} else {
		p.expect(token.Kind('('))
		for !p.at(token.Kind(')')) {
			params = append(params, p.typeExpr())
			if !p.accept(token.Kind(',')) {
				break
			}
		}
		p.expect(token.Kind(')'))
	}
	ct := &ast.ClosureType{Params: params}
	if p.accept(token.Kind(':')) {
		ct.Return = p.typeExpr()
	}
	ct.S = p.span(start)
	return ct
}

// atCast reports whether the current token is a `(type)` cast.
func (p *Parser) atCast() bool {
	switch p.kind() {
	case token.T_INT_CAST, token.T_DOUBLE_CAST, token.T_STRING_CAST, token.T_ARRAY_CAST, token.T_OBJECT_CAST, token.T_BOOL_CAST:
		return true
	}
	return false
}

// typeArgs parses <T, U> in a type position (Phaenon only).
func (p *Parser) typeArgs() []ast.TypeExpr {
	p.expect(token.Kind('<'))
	var args []ast.TypeExpr
	for {
		args = append(args, p.typeExpr())
		if !p.accept(token.Kind(',')) {
			break
		}
	}
	p.closeAngle()
	return args
}

// closeAngle consumes a '>' — splitting a '>>' or '>=' token when nested
// type arguments end together.
func (p *Parser) closeAngle() {
	switch p.kind() {
	case token.Kind('>'):
		p.next()
	case token.T_SR, token.T_IS_GREATER_OR_EQUAL:
		// Split the token in place: keep the second half as the current token.
		t := p.toks[p.i]
		rest := token.Token{Kind: token.Kind(t.Text(p.src)[1]), Start: t.Start + 1, End: t.End, Line: t.Line}
		p.toks[p.i] = token.Token{Kind: token.Kind('>'), Start: t.Start, End: t.Start + 1, Line: t.Line}
		p.toks = append(p.toks[:p.i+1], append([]token.Token{rest}, p.toks[p.i+1:]...)...)
		p.next()
	default:
		p.errorf("unexpected %s, expecting '>'", p.describe())
	}
}

// typeParams parses Phaenon's <[in|out] T [of Bound] [= Default], ...>
// after a declaration name, when present.
func (p *Parser) typeParams() []*ast.TypeParam {
	if p.mode != lexer.ModePhaenon || !p.at(token.Kind('<')) {
		return nil
	}
	p.next()
	var params []*ast.TypeParam
	for {
		start := p.tok().Start
		tp := &ast.TypeParam{}
		switch {
		case p.at(token.T_IN) && p.peekKind(1) != token.T_OF && p.peekKind(1) != token.Kind(',') && p.peekKind(1) != token.Kind('>'):
			p.next()
			tp.Variance = ast.Contravariant
		case p.at(token.T_OUT) && p.peekKind(1) != token.T_OF && p.peekKind(1) != token.Kind(',') && p.peekKind(1) != token.Kind('>'):
			p.next()
			tp.Variance = ast.Covariant
		}
		tp.Name = p.stringIdent()
		if p.accept(token.T_OF) {
			tp.Bound = p.typeExpr()
		}
		if p.accept(token.Kind('=')) {
			tp.Default = p.typeExpr()
		}
		tp.S = p.span(start)
		params = append(params, tp)
		if !p.accept(token.Kind(',')) {
			break
		}
	}
	p.closeAngle()
	return params
}
