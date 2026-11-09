package parser

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/token"
)

// The syntax of structured concurrency left the language (design/roadmap.md
// D25, concurrency.md §17), and the words `concurrently` and `spawn` are
// names again. Its shapes, which were never PHP, are still recognised in
// Phaenon mode so that the checker can name the construct (P1001) where a
// bare syntax error would say nothing: `concurrently {` and `concurrently
// (…) {`, and `spawn` before a block or before the start of an operand.
// Everything else stays what PHP makes of a name: `concurrently(…)` and
// `spawn(…)` are calls, `spawn[0]`, `spawn - 1` and `spawn + 1` read a
// constant. Nothing is reserved.
//
// The same holds for the spellings roadmap D26 and D27 withdrew on
// 2026-11-09. `fun` is a name again: a declaration spelled with it, `fun`
// before a name and its parameters, parses as the function or method it
// declared, marked Fun, so that the checker reports it (P1001) and phn fmt
// respells it. `is` is a name too: after an operand, where no PHP puts a
// name, `is` and a type parse as the withdrawn test (ast.IsExpr), and so do
// `as`, which stays PHP's keyword, and `as?` (ast.AsExpr).

// concurrentlyAhead reports a withdrawn `concurrently` block at the current
// token: the word, then `{` or `(…) {`.
func (p *Parser) concurrentlyAhead() bool {
	if !p.withdrawnWord("concurrently") {
		return false
	}
	if p.peekKind(1) == token.Kind('{') {
		return true
	}
	if p.peekKind(1) != token.Kind('(') {
		return false
	}
	depth := 0
	for i := 1; ; i++ {
		switch p.peekKind(i) {
		case token.Kind('('):
			depth++
		case token.Kind(')'):
			depth--
			if depth == 0 {
				return p.peekKind(i+1) == token.Kind('{')
			}
		case token.EOF:
			return false
		}
	}
}

// spawnAhead reports a withdrawn `spawn` at the current token: the word,
// then a block or the start of an expression that cannot continue a name.
func (p *Parser) spawnAhead() bool {
	if !p.withdrawnWord("spawn") {
		return false
	}
	switch k := p.peekKind(1); k {
	case token.Kind('{'):
		return true
	case token.Kind('('), token.Kind('['), token.Kind('-'), token.Kind('+'):
		return false
	default:
		return startsExprKind(k)
	}
}

// withdrawnWord reports the word, in any case, as a name in Phaenon mode.
func (p *Parser) withdrawnWord(word string) bool {
	return p.mode == lexer.ModePhaenon && p.at(token.T_STRING) && strings.EqualFold(p.tok().Text(p.src), word)
}

// withdrawn parses `concurrently (opts) { .. }`, `spawn { .. }` or `spawn
// expr`, the operand binding like throw's, as an ast.Withdrawn.
func (p *Parser) withdrawn() ast.Expr {
	kw := p.next()
	w := &ast.Withdrawn{Kw: ast.Span{Start: kw.Start, End: kw.End}, Name: kw.Text(p.src)}
	if p.at(token.Kind('(')) {
		w.Opts = p.argList()
	}
	if p.at(token.Kind('{')) {
		w.Body = p.block()
	} else {
		w.Expr = p.exprBP(bpThrow)
	}
	w.S = p.span(kw.Start)
	return w
}

// tokenAhead is the non-trivia token n places after the current one, the
// current one at 0.
func (p *Parser) tokenAhead(n int) token.Token {
	if n == 0 {
		return p.tok()
	}
	return p.peek(n)
}

// isAmpersand reports either of the lexer's `&` tokens.
func isAmpersand(k token.Kind) bool {
	return k == token.T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG || k == token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG
}

// afterTypeParams returns the position after a balanced `<…>` that starts
// at position i, or -1 when none does: a type parameter list ends with its
// `>`, and a `>>` closes two. Anything that cannot stand in one (`;`, `{`,
// a variable, `=`… before the list closes) ends the scan.
func (p *Parser) afterTypeParams(i int) int {
	if p.tokenAhead(i).Kind != token.Kind('<') {
		return -1
	}
	depth := 0
	for ; ; i++ {
		switch p.tokenAhead(i).Kind {
		case token.Kind('<'):
			depth++
		case token.Kind('>'):
			depth--
		case token.T_SR:
			depth -= 2
		case token.Kind(';'), token.Kind('{'), token.Kind('}'), token.T_VARIABLE, token.EOF:
			return -1
		}
		if depth <= 0 {
			return i + 1
		}
	}
}

// methodAt reports a method's name and parameter list at position i: a
// name, then `(`, or a balanced `<…>` and then `(`.
func (p *Parser) methodAt(i int, name func(token.Token) bool) bool {
	if !name(p.tokenAhead(i)) {
		return false
	}
	i++
	if j := p.afterTypeParams(i); j >= 0 {
		i = j
	}
	return p.tokenAhead(i).Kind == token.Kind('(')
}

// KeywordRequired reports a method name that keeps PHP's `function`
// (design/functions.md §1.1): a modifier, `var`, `const`, `function` and
// `fn` in any case, and the unqualified `Closure`. After the modifiers,
// these begin a property's or a constant's type (`public static (A&B)|null
// $x`, `Closure(int): string $f`) or would read as a closure; every other
// name, PHP's reserved words included, needs no keyword.
func KeywordRequired(name string) bool {
	switch strings.ToLower(name) {
	case "static", "abstract", "final", "public", "protected", "private", "readonly", "var", "const", "function", "fn":
		return true
	}
	return name == "Closure"
}

// bareMethodName reports a name a method may have without a keyword: an
// identifier, PHP's reserved words included, that KeywordRequired leaves.
func (p *Parser) bareMethodName(t token.Token) bool {
	return (t.Kind == token.T_STRING || isReservedNonModifier(t.Kind)) && !KeywordRequired(t.Text(p.src))
}

// bareMethodAhead reports a method declared without a keyword at the
// current token, after its modifiers (roadmap D27): an optional `&`, a
// name and its parameter list. Anything else begins a property.
func (p *Parser) bareMethodAhead() bool {
	if p.mode != lexer.ModePhaenon {
		return false
	}
	i := 0
	if isAmpersand(p.kind()) {
		i = 1
	}
	return p.methodAt(i, p.bareMethodName)
}

// funAhead reports a declaration spelled with the withdrawn `fun` at the
// current token (roadmap D27): the word, an optional `&`, a name and the
// parameter list. In a class body any identifier is a name, as it was
// after `fun`; at statement level, where `fun & f()` is an expression,
// the name is a function's and the parameter list must be followed by
// what only a declaration has: `{`, `:`, `=>` or `throws`.
func (p *Parser) funAhead(member bool) bool {
	if !p.withdrawnWord("fun") {
		return false
	}
	i := 1
	if isAmpersand(p.tokenAhead(i).Kind) {
		i++
	}
	name := func(t token.Token) bool {
		if member {
			return t.Kind == token.T_STRING || isSemiReserved(t.Kind)
		}
		return t.Kind == token.T_STRING || t.Kind == token.T_READONLY || isPhaenonContextual(t.Kind)
	}
	if !p.methodAt(i, name) {
		return false
	}
	if member {
		return true
	}
	// The parameter list, then what follows it.
	i++
	if j := p.afterTypeParams(i); j >= 0 {
		i = j
	}
	depth := 0
	for ; ; i++ {
		switch p.tokenAhead(i).Kind {
		case token.Kind('('):
			depth++
		case token.Kind(')'):
			depth--
			if depth == 0 {
				switch t := p.tokenAhead(i + 1); t.Kind {
				case token.Kind('{'), token.Kind(':'), token.T_DOUBLE_ARROW:
					return true
				case token.T_STRING:
					return strings.EqualFold(t.Text(p.src), "throws")
				}
				return false
			}
		case token.EOF:
			return false
		}
	}
}

// isAhead reports the withdrawn test `x is T` at the current token, after
// an operand: the name `is`, then the start of a type. No PHP puts a name
// after an operand, so `is` is the test here and a name everywhere else.
func (p *Parser) isAhead() bool {
	if !p.withdrawnWord("is") {
		return false
	}
	switch k := p.peekKind(1); k {
	case token.Kind('?'), token.Kind('('), token.T_ARRAY, token.T_CALLABLE, token.T_STATIC, token.T_LIST, token.T_CLASS,
		token.T_STRING, token.T_NAME_QUALIFIED, token.T_NAME_FULLY_QUALIFIED, token.T_NAME_RELATIVE:
		return true
	default:
		return isPhaenonContextual(k)
	}
}

// keywordNamedMethod refuses, with the rule in the message, a method
// declared without a keyword whose name keeps `function`
// (design/functions.md §1.1): after the modifiers, the last of them, or
// `const`, `fn` or `Closure`, followed by what can only be a parameter list
// and a body. The parse itself stays a lookahead of one token after the
// name; this only says why it fails.
func (p *Parser) keywordNamedMethod(modified bool) {
	name := ""
	at := 0
	switch {
	case p.at(token.Kind('(')) && modified:
		name = p.toks[p.prevIndex()].Text(p.src)
	case p.at(token.T_CONST), p.at(token.T_FN):
		name, at = p.text(), 1
	case p.at(token.T_STRING) && p.text() == "Closure":
		name, at = p.text(), 1
	default:
		return
	}
	if !p.methodShapeAt(at) {
		return
	}
	p.errorf("a method named %s keeps `function`: `public function %s()` (design/functions.md §1.1)", name, name)
}

// prevIndex is the index of the non-trivia token before the current one.
func (p *Parser) prevIndex() int {
	j := p.i - 1
	for j > 0 && p.toks[j].Kind.IsTrivia() {
		j--
	}
	return j
}

// methodShapeAt reports a parameter list at position i, `(…)` or a cast
// token, followed by what only a method has before a body: `{`, `=>` or
// `;`, where a property's or a constant's type goes on to its variable or
// its name and `=`.
func (p *Parser) methodShapeAt(i int) bool {
	if k := p.tokenAhead(i).Kind; k != token.Kind('(') && !isCastKind(k) {
		return false
	}
	depth := 0
	for ; ; i++ {
		switch p.tokenAhead(i).Kind {
		case token.Kind('('):
			depth++
		case token.Kind(')'):
			depth--
		case token.Kind('{'), token.T_DOUBLE_ARROW, token.Kind(';'):
			if depth == 0 {
				return true
			}
		case token.T_VARIABLE:
			if depth == 0 {
				return false
			}
		case token.Kind('='), token.Kind('}'), token.EOF:
			return false
		}
	}
}

func isCastKind(k token.Kind) bool {
	switch k {
	case token.T_INT_CAST, token.T_DOUBLE_CAST, token.T_STRING_CAST, token.T_ARRAY_CAST, token.T_OBJECT_CAST, token.T_BOOL_CAST, token.T_BYTES_CAST:
		return true
	}
	return false
}

// memberModifiers refuses, in Phaenon mode, the runs of modifiers PHP
// refuses on a class member when it compiles the class, so that no build
// writes PHP that does not compile: a modifier written twice, two
// visibilities, and on a method `readonly` or `final` with `abstract`. The
// run starts at token index from and ends at the current token.
func (p *Parser) memberModifiers(from int, method bool) {
	seen := map[token.Kind]bool{}
	visibility := false
	for j := from; j < p.i; j++ {
		t := p.toks[j]
		if t.Kind.IsTrivia() {
			continue
		}
		word := strings.ToLower(t.Text(p.src))
		switch t.Kind {
		case token.T_PUBLIC, token.T_PROTECTED, token.T_PRIVATE:
			if visibility {
				p.errorAt(t, "Multiple access type modifiers are not allowed")
			}
			visibility = true
		case token.T_STATIC, token.T_ABSTRACT, token.T_FINAL, token.T_READONLY, token.T_VAR,
			token.T_PUBLIC_SET, token.T_PROTECTED_SET, token.T_PRIVATE_SET:
			if seen[t.Kind] {
				p.errorAt(t, "Multiple %s modifiers are not allowed", word)
			}
		default:
			continue
		}
		seen[t.Kind] = true
	}
	if !method {
		return
	}
	switch {
	case seen[token.T_READONLY]:
		p.errorf("Cannot use 'readonly' as method modifier")
	case seen[token.T_ABSTRACT] && seen[token.T_FINAL]:
		p.errorf("Cannot use the final modifier on an abstract method")
	case seen[token.T_PUBLIC_SET] || seen[token.T_PROTECTED_SET] || seen[token.T_PRIVATE_SET]:
		p.errorf("Cannot use an asymmetric visibility on a method")
	}
}
