package program

import (
	"regexp"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/token"
)

// The type tests `is`, `as` and `as?` left the language with roadmap D26
// (2026-11-09): a test is PHP's own. A leftover one is P1001, and phn fmt
// rewrites it into the PHP the emitter wrote for it until then
// (design/tooling.md §5.6), which the checker still derives (Test) because
// most of it depends on the operand's type. This file writes that PHP in
// the test's place, as a person writes it: the functions unqualified, the
// test parenthesised only where the expression around it binds tighter.

// Slots says, for the expressions of a file, how loose an expression may
// be where each stands without parentheses of its own.
type Slots struct {
	prec map[ast.Expr]Prec
	// orOperand marks the operands of ||, where a test joined with && is
	// parenthesised for the reader.
	orOperand map[ast.Expr]bool
}

// SlotsOf reads the slots of a file's expressions.
func SlotsOf(f *ast.File) *Slots {
	s := &Slots{prec: map[ast.Expr]Prec{}, orOperand: map[ast.Expr]bool{}}
	ast.Inspect(f, func(n ast.Node) bool {
		s.of(n)
		return true
	})
	return s
}

// Parenthesise reports whether text binding as tightly as prec needs
// parentheses where node stands.
func (s *Slots) Parenthesise(node ast.Expr, prec Prec) bool {
	slot, ok := s.prec[node]
	if !ok {
		slot = PrecAtom
	}
	return prec < slot || prec == PrecAnd && s.orOperand[node]
}

// of records the places among a node's direct children that take a
// looser expression than an operand of instanceof does.
func (s *Slots) of(n ast.Node) {
	set := func(e ast.Expr, p Prec) {
		if e != nil {
			s.prec[e] = p
		}
	}
	free := func(es ...ast.Expr) {
		for _, e := range es {
			set(e, PrecTernary)
		}
	}
	switch n := n.(type) {
	case *ast.Arg:
		free(n.Value)
	case *ast.Assign:
		free(n.Value)
	case *ast.Return:
		free(n.Expr)
	case *ast.ExprStmt:
		free(n.Expr)
	case *ast.Echo:
		free(n.Exprs...)
	case *ast.Print:
		free(n.Expr)
	case *ast.Throw:
		free(n.Expr)
	case *ast.Yield:
		free(n.Key, n.Value)
	case *ast.Paren:
		free(n.Expr)
	case *ast.ArrayItem:
		free(n.Key, n.Value)
	case *ast.If:
		free(n.Cond)
	case *ast.ElseIf:
		free(n.Cond)
	case *ast.While:
		free(n.Cond)
	case *ast.DoWhile:
		free(n.Cond)
	case *ast.For:
		free(n.Init...)
		free(n.Cond...)
		free(n.Step...)
	case *ast.Match:
		free(n.Subject)
	case *ast.Switch:
		free(n.Subject)
	case *ast.Case:
		free(n.Cond)
	case *ast.Index:
		free(n.Index)
	case *ast.MatchArm:
		free(n.Conds...)
		free(n.Body)
	case *ast.ArrowFunc:
		free(n.Body)
	case *ast.Ternary:
		// A test reads well as a condition; an `as`, itself a ternary,
		// gets parentheses.
		set(n.Cond, PrecOr)
		set(n.Then, PrecOr)
		set(n.Else, PrecOr)
	case *ast.Unary:
		if n.Op == token.Kind('!') {
			set(n.Expr, PrecInstanceof)
		}
	case *ast.Binary:
		var p Prec
		switch n.Op {
		case token.T_LOGICAL_AND, token.T_LOGICAL_OR, token.T_LOGICAL_XOR:
			p = PrecTernary
		case token.T_BOOLEAN_OR:
			p = PrecOr
			s.orOperand[n.Left], s.orOperand[n.Right] = true, true
		case token.T_COALESCE:
			p = PrecOr
		case token.T_BOOLEAN_AND:
			p = PrecAnd
		case token.Kind('|'), token.Kind('^'), token.Kind('&'):
			p = PrecEquality
		case token.T_POW, token.T_PIPE:
			p = PrecAtom
		default:
			p = PrecInstanceof // comparisons, arithmetic, concatenation
		}
		set(n.Left, p)
		set(n.Right, p)
	}
}

// testFunctions are the functions a Test writes, qualified (`\is_int(`).
var testFunctions = regexp.MustCompile(`\\(is_[a-z]+|array_is_list|count|class_exists|interface_exists|mb_check_encoding|get_debug_type)\(`)

// Rewrite is the PHP that replaces the withdrawn test node, whose operand
// is operand, in src: the condition of an `is`, or the whole checked value
// of an `as` (nullable for `as?`). The functions are written unqualified
// unless qualified says that the file's namespace declares one of the same
// name. It fails, with the reason, where the operand would be written
// twice and does not read the same twice (the rule P3012 enforced).
func (t *Test) Rewrite(src []byte, node, operand ast.Expr, as, nullable bool, slots *Slots, qualified func(function string) bool) (string, string) {
	text, prec := t.Cond, t.Prec
	if as {
		text, prec = t.As(nullable), PrecTernary
	}
	text = testFunctions.ReplaceAllStringFunc(text, func(m string) string {
		if qualified != nil && qualified(m[1:len(m)-1]) {
			return m
		}
		return m[1:]
	})
	if slots != nil && slots.Parenthesise(node, prec) {
		text = "(" + text + ")"
	}
	os := operand.Span()
	if strings.Count(text, TestOperand) == 1 {
		return strings.Replace(text, TestOperand, string(src[os.Start:os.End]), 1), ""
	}
	if !ReadsTwice(operand) {
		return "", "the operand would be evaluated twice; assign it to a variable first"
	}
	inner, cast := TestOperandOf(operand)
	is := inner.Span()
	return withOperand(text, string(src[is.Start:is.End]), cast), ""
}

// withOperand writes src for each TestOperand in text. A copy that is a
// (string) cast gets parentheses before an index, where `(string) $x[0]`
// would cast the element, and before instanceof, which a cast binds
// tighter than but a reader need not know it.
func withOperand(text, src string, cast bool) string {
	if !cast {
		return strings.ReplaceAll(text, TestOperand, src)
	}
	src = "(string) " + src
	var b strings.Builder
	for {
		i := strings.Index(text, TestOperand)
		if i < 0 {
			b.WriteString(text)
			return b.String()
		}
		b.WriteString(text[:i])
		text = text[i+len(TestOperand):]
		if strings.HasPrefix(text, "[") || strings.HasPrefix(text, " instanceof ") {
			b.WriteString("(" + src + ")")
		} else {
			b.WriteString(src)
		}
	}
}
