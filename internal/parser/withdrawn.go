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
