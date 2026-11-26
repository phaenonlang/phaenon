package checker

import (
	"strconv"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Extra checks: where a finding is one, and what proves it.
//
// An extra check (design/roadmap.md D33) is a claim: PHPStan makes no such
// finding of this code at level max, and php runs it. (A rule of PHPStan's
// that the checker deliberately lacks may speak on the same line all the
// same; the parity run names those lines.) The claim is made at the place
// the finding is reported, and it needs proof there. Where the checker
// cannot prove it the finding is reported through report, a parity
// finding, which stays with the extra checks off: a doubt costs a line in
// the parity run's table (checkerAlone), never a finding the switch hides.
//
// The checker's type of a value is no proof. PHPStan knows more than the
// lattice wherever the lattice has let go: it folds strtolower('ABC') to
// 'abc' and count([1, 2, 3]) to 3, keeps a union of forty literals forty
// values where the lattice has an int, and a union of consecutive ones
// where the lattice has a range. A value typed `string` or `int` here may
// be a known value there, and PHPStan then reports what a vaguer type would
// pass. Nor is what the checker has followed of a variable through the
// function a proof: PHPStan learns of a value by more ways than a flow
// analysis of this size models (an assertion tag inherited from an
// interface, a condition kept with another variable, a variable written by
// name), and it remembers what it was told of an expression as it does of
// a variable (`$i ** -1` after `assert(is_float($i ** -1))` is a float).
// A proof is something PHPStan cannot know more about than the checker
// does, by the syntax of the function alone:
//
//   - a declaration's own words, where the finding compares declarations
//     and no value: the static method rule (decls.go);
//   - an untouched parameter (untouched.go): one declared by its function
//     or method alone, whose only occurrence in the body is the read the
//     finding is about. There is no other place where either tool could
//     have learnt anything, of it or of an expression that holds it;
//   - a literal, which both read alike.
//
// Each place that may report an extra check is listed with its proof, and
// with the rule PHPStan judges the place by, in extraCheckSites
// (kinds_test.go): a new call fails TestExtraCheckSites until it is listed.

// reportExtra records an extra check: a finding that is true, which
// PHPStan does not make at level max, on code php runs. The place that
// calls it holds the proof (above); the golden corpus marks the line
// (`// want P2301 extra`), and the parity run holds the mark to PHPStan.
// With Options.NoExtraChecks the finding is not returned.
func (c *Checker) reportExtra(span ast.Span, code Code, format string, args ...any) {
	c.reportOf(Extra, mayBeSilenced, span, code, nil, format, args...)
}

// reportExtraRelated is reportExtra with the declaration the finding is
// about.
func (c *Checker) reportExtraRelated(span ast.Span, code Code, rel *Related, format string, args ...any) {
	c.reportOf(Extra, mayBeSilenced, span, code, rel, format, args...)
}

// reportExtra is the checker's: an extra check, with its proof at the call.
func (x *fnCtx) reportExtra(span ast.Span, code Code, format string, args ...any) {
	x.c.reportExtra(span, code, format, args...)
}

// given is a value held to a declared type: its expression, as far as it
// is at hand where the finding is reported (the argument, what a return
// returns, the right-hand side of a plain assignment, a default), and the
// untouched parameters of the function it stands in. The zero value is a
// value whose expression is not at hand, or whose type is not the
// expression's (under an inline @var): it proves nothing.
type given struct {
	expr   ast.Expr
	params untouched
}

// given is a value's expression in the function being checked.
func (x *fnCtx) given(value ast.Expr) given { return given{value, x.untouched} }

// reportUnfit reports a value of type got given where want is declared,
// which types.IsAcceptable has refused. It is a parity finding unless one
// of two proofs makes it an extra check:
//
//   - the value is known alike to both tools (knownAlike), and fits as
//     PHPStan compares array shapes (types.ShapesAsPHPStan): a general
//     array given for a list shape, which may lack its offset 0 and be no
//     list where the receiver relies on one, or a longer tuple given for a
//     shorter, which is exact (design/collections.md §2.4) and may be
//     counted or spread. PHPStan accepts both of a value whose type is
//     the same to it as to the checker, which is known of an untouched
//     parameter's declared type and of a literal. Of a value from
//     elsewhere it is not: what a function returns, each tool describes
//     on its own, and the claim would rest on the two descriptions
//     agreeing;
//   - the value is itself arithmetic over integers that PHPStan types more
//     widely than the checker, and PHPStan's type fits (arithmeticFits).
//
// The place has to be one PHPStan judges by the acceptance of the value's
// type, as it does an argument, a return, a yield, a property and the
// defaults: the assignment of an offset it holds to a stricter rule of its
// own, and that place reports through report.
func (c *Checker) reportUnfit(v given, got, want types.Type, span ast.Span, code Code, rel *Related, format string, args ...any) {
	if v.expr != nil && (c.arithmeticFits(v, want) ||
		knownAlike(v.expr, v.params) && types.IsAcceptable(got, want, types.ShapesAsPHPStan(c.p))) {
		c.reportExtraRelated(span, code, rel, format, args...)
		return
	}
	c.reportRelated(span, code, rel, format, args...)
}

// reportUnfitRefused is reportUnfit for a default or a constant's value
// that PHP refuses when it compiles the file where refused says so
// (literalRefused): a finding about code PHP refuses, which no comment
// silences, and no extra check.
func (c *Checker) reportUnfitRefused(refused bool, v given, got, want types.Type, span ast.Span, code Code, rel *Related, format string, args ...any) {
	if refused {
		c.reportRefused(true, span, code, rel, format, args...)
		return
	}
	c.reportUnfit(v, got, want, span, code, rel, format, args...)
}

// literalRefused reports a default or a constant's value written as a
// literal of a kind its native type does not take, which PHP refuses when
// it compiles the file: "Cannot use string as default value for parameter
// $x of type int", the same of a property, "Cannot use string as value
// for class constant …". native is the type the compiled file writes.
// Only a literal is judged: PHP evaluates a constant expression when the
// value is first needed, most of them, and the checker's type of a value
// says more than PHP's kinds do. null is a parameter's default whatever
// its type (param), which PHP 8.5 makes nullable, deprecated. A member
// the checker does not spell as PHP's own, a type parameter, which is
// erased, among them, takes anything.
func literalRefused(e ast.Expr, native types.Type, param bool) bool {
	kind := literalKind(e)
	if kind == "" || native == nil || param && kind == "null" {
		return false
	}
	for _, m := range types.Members(native) {
		switch m := m.(type) {
		case types.Int:
			if kind == "int" {
				return false
			}
		case types.Float:
			if kind == "int" || kind == "float" {
				return false
			}
		case types.String, types.StringLit, types.ClassString, types.Bytes:
			if kind == "string" {
				return false
			}
		case types.Bool:
			if kind == "true" || kind == "false" {
				return false
			}
		case types.BoolLit:
			if kind == strconv.FormatBool(m.V) {
				return false
			}
		case types.Null:
			if kind == "null" {
				return false
			}
		case types.Array, types.Shape, types.Iterable:
			if kind == "array" {
				return false
			}
		case types.Class, types.Object, types.Static, types.This, types.EnumCase, types.ObjectShape, types.Intersection:
			// An object takes no literal.
		default:
			return false
		}
	}
	return true
}

// literalKind is the kind PHP gives a literal in its message: "int",
// "float", "string", "array", "true", "false" or "null"; "" for any other
// expression.
func literalKind(e ast.Expr) string {
	switch e := unparen(e).(type) {
	case *ast.IntLit:
		return "int"
	case *ast.FloatLit:
		return "float"
	case *ast.StringLit:
		return "string"
	case *ast.ArrayLit:
		return "array"
	case *ast.Unary:
		if e.Op == token.Kind('-') || e.Op == token.Kind('+') {
			switch e.Expr.(type) {
			case *ast.IntLit:
				return "int"
			case *ast.FloatLit:
				return "float"
			}
		}
		return ""
	}
	if name, ok := constName(e); ok && (name == "true" || name == "false" || name == "null") {
		return name
	}
	return ""
}

// knownAlike reports whether PHPStan knows no more of a value than the
// checker does: it is an untouched parameter, whose type is its
// declaration's for both; a scalar literal; or an array literal of such
// values under no keys or literal ones. A constant, a property, a call and
// whatever else PHPStan may have computed or narrowed further is not.
func knownAlike(e ast.Expr, params untouched) bool {
	switch e := unparen(e).(type) {
	case *ast.Variable:
		_, ok := params[e.Name]
		return ok
	case *ast.ArrayLit:
		for _, item := range e.Items {
			if item == nil || item.Unpack || item.ByRef || item.Key != nil && !scalarLiteral(item.Key) || !knownAlike(item.Value, params) {
				return false
			}
		}
		return true
	default:
		return scalarLiteral(e)
	}
}

// scalarLiteral reports an int, a float or a string written out, a signed
// number, and true, false and null.
func scalarLiteral(e ast.Expr) bool {
	switch e := unparen(e).(type) {
	case *ast.IntLit, *ast.FloatLit, *ast.StringLit:
		return true
	case *ast.Unary:
		switch e.Expr.(type) {
		case *ast.IntLit, *ast.FloatLit:
			return e.Op == token.Kind('-') || e.Op == token.Kind('+')
		}
	case *ast.Name, *ast.ConstFetch:
		if name, ok := constName(e); ok {
			return name == "true" || name == "false" || name == "null"
		}
	}
	return false
}

// intArith is how PHPStan types an arithmetic expression over integers
// that it does not compute, whatever its value here.
type intArith uint8

const (
	// sumOfInts: `+`, `-` and `*` of two ints are an int for PHPStan,
	// which does not follow the result past PHP_INT_MAX.
	sumOfInts intArith = iota + 1
	// powerOfInts: `**` of two ints is PHPStan's benevolent int|float,
	// which passes wherever an int or a float does.
	powerOfInts
)

// noteArithmetic records, of `l op r` typed just now, how PHPStan types it
// where it types it more widely than the checker. That needs proof of two
// things, neither of which a type here gives:
//
//   - PHPStan does not compute the value, which it does where both
//     operands are known values, however many: one operand has to be an
//     int of which PHPStan knows nothing, and that is only an untouched
//     parameter typed by PHP alone. A docblock may list values, and the
//     lattice writes `2|3` as a range and forty values as int;
//   - the other operand is an int to PHPStan as it is here: an int written
//     out, or an untouched parameter whose declared type is an int's, which
//     is another parameter then, since each is named once.
func (c *Checker) noteArithmetic(e *ast.Binary, l, r types.Type, params untouched) {
	var as intArith
	switch e.Op {
	case token.Kind('+'), token.Kind('-'), token.Kind('*'):
		as = sumOfInts
	case token.T_POW:
		as = powerOfInts
	}
	unknown := func(operand ast.Expr, t types.Type) bool {
		v, ok := unparen(operand).(*ast.Variable)
		return ok && params[v.Name] && types.Equal(t, types.TInt)
	}
	anInt := func(operand ast.Expr, t types.Type) bool {
		if !types.IsSubtype(t, types.TInt, nil) {
			return false
		}
		switch o := unparen(operand).(type) {
		case *ast.IntLit:
			return true
		case *ast.Unary:
			_, lit := o.Expr.(*ast.IntLit)
			return lit && (o.Op == token.Kind('-') || o.Op == token.Kind('+'))
		case *ast.Variable:
			_, ok := params[o.Name]
			return ok
		}
		return false
	}
	if as == 0 || !(unknown(e.Left, l) && anInt(e.Right, r) || unknown(e.Right, r) && anInt(e.Left, l)) {
		delete(c.intArith, e)
		return
	}
	if c.intArith == nil {
		c.intArith = map[*ast.Binary]intArith{}
	}
	c.intArith[e] = as
}

// arithmeticFits reports whether a value that did not fit want is itself
// arithmetic over integers whose result PHPStan types more widely than the
// checker (noteArithmetic), and PHPStan's type fits: `$i ** -2` is a float
// for every int, which an int parameter refuses under strict_types, where
// PHPStan has a benevolent int|float. Kept in a variable first, or wrapped
// in anything but parentheses, the value is told from nothing.
func (c *Checker) arithmeticFits(v given, want types.Type) bool {
	b, ok := unparen(v.expr).(*ast.Binary)
	if !ok {
		return false
	}
	switch c.intArith[b] {
	case sumOfInts:
		return types.IsAcceptable(types.TInt, want, c.p)
	case powerOfInts:
		return types.IsAcceptable(types.TInt, want, c.p) || types.IsAcceptable(types.TFloat, want, c.p)
	}
	return false
}

// arithOperands are the two sides of the arithmetic being typed.
type arithOperands struct {
	left, right ast.Expr
}

// powerOnString reports whether `**` on an operand that is no number, the
// left one (which == 0) or the right, is an extra check: `'abc' ** 2`
// throws TypeError, as every arithmetic operator does on a string that is
// no number, and PHPStan reports the others on `string` and this one only
// on a string it knows the value of, an array or a class. The proof is
// that PHPStan knows no value, and reports the expression for nothing
// else:
//
//   - the operand is an untouched parameter typed by PHP alone, and its
//     type is `string`, with or without int, float and null beside it. A
//     string from anywhere else may be a known value to PHPStan:
//     strtolower('ABC'), PHP_EOL, a string narrowed to a list of values;
//   - the other operand is a number written out, an untouched parameter
//     declared an int or a float, or another such string, each a
//     parameter of its own. Of anything else PHPStan may think otherwise
//     than the checker (a mixed, which it reports), and it reports the
//     expression once for both operands.
func (x *fnCtx) powerOnString(which int, l, r types.Type) bool {
	plain := types.MakeUnion(types.TInt, types.TFloat, types.TNull)
	unknownString := func(operand ast.Expr, t types.Type) bool {
		v, ok := unparen(operand).(*ast.Variable)
		if !ok || types.IsMixed(t) || !x.untouched[v.Name] {
			return false
		}
		isString := false
		for _, m := range types.Members(t) {
			switch {
			case types.Equal(m, types.TString):
				isString = true
			case !types.IsSubtype(m, plain, nil):
				return false
			}
		}
		return isString
	}
	aNumber := func(operand ast.Expr, t types.Type) bool {
		if types.IsMixed(t) || !types.IsSubtype(t, types.MakeUnion(types.TInt, types.TFloat), nil) {
			return false
		}
		switch o := unparen(operand).(type) {
		case *ast.IntLit, *ast.FloatLit:
			return true
		case *ast.Unary:
			switch o.Expr.(type) {
			case *ast.IntLit, *ast.FloatLit:
				return o.Op == token.Kind('-') || o.Op == token.Kind('+')
			}
		case *ast.Variable:
			_, ok := x.untouched[o.Name]
			return ok
		}
		return false
	}
	operand, t, other, ot := x.arith.left, l, x.arith.right, r
	if which == 1 {
		operand, t, other, ot = x.arith.right, r, x.arith.left, l
	}
	return unknownString(operand, t) && (aNumber(other, ot) || unknownString(other, ot))
}
