package checker

import (
	"math"
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// The constructs PHP deprecates (design/php-versions.md §4), reported as
// P1101 whatever the target: code compiled for 8.5 runs on 8.6 too, and a
// deprecation is a warning about that future. Like the library's P4006,
// they are reported unless --no-deprecations. Library deprecations are not
// here: they come from the stubs' #[\Deprecated] (library.go, call.go).

// deprecated reports a construct PHP deprecates.
func (c *Checker) deprecated(span ast.Span, format string, args ...any) {
	if !c.opts.Deprecations {
		return
	}
	c.report(span, P1101, format, args...)
}

// reservedNames are the names PHP 8.6 deprecates for classes, functions
// and constants, for a future `let` and the pattern-matching `is`
// (php-versions.md §8). Every kind of name compares without regard to case.
var reservedNames = map[string]bool{"let": true, "is": true}

// declaredName judges the name a declaration gives a class, an interface,
// a trait, an enum or a function.
func (c *Checker) declaredName(span ast.Span, kind, name string) {
	lower := strings.ToLower(name)
	switch {
	case reservedNames[lower]:
		c.deprecated(span, "`%s` as %s name is deprecated since PHP 8.6: PHP reserves it", lower, article(kind))
	case kind == "function" && lower == "readonly":
		c.deprecated(span, "a function named readonly is deprecated since PHP 8.6")
	}
}

// constantName judges the name of a global constant, declared with const
// or define().
func (c *Checker) constantName(span ast.Span, name string) {
	if i := strings.LastIndexByte(name, '\\'); i >= 0 {
		name = name[i+1:]
	}
	lower := strings.ToLower(name)
	if reservedNames[lower] || lower == "_" {
		c.deprecated(span, "`%s` as a constant name is deprecated since PHP 8.6", lower)
	}
}

func article(kind string) string {
	if strings.IndexByte("aeiou", kind[0]) >= 0 {
		return "an " + kind
	}
	return "a " + kind
}

// useAliases judges the aliases a use statement gives: let and is for a
// class, a function or a constant, _ for a class or a constant.
func (c *Checker) useAliases(u *ast.Use) {
	for _, it := range u.Items {
		kind := u.Kind
		if it.Kind != ast.UseClass {
			kind = it.Kind
		}
		name := it.Name.Parts[len(it.Name.Parts)-1]
		span := it.S
		if it.Alias != nil {
			name, span = it.Alias.Name, it.Alias.S
		}
		lower := strings.ToLower(name)
		if !reservedNames[lower] && !(lower == "_" && kind != ast.UseFunction) {
			continue
		}
		what := "class"
		switch kind {
		case ast.UseFunction:
			what = "function"
		case ast.UseConst:
			what = "constant"
		}
		c.deprecated(span, "`%s` as the name of an imported %s is deprecated since PHP 8.6", lower, what)
	}
}

// classConstName judges a class constant's name.
func (c *Checker) classConstName(span ast.Span, name string) {
	if strings.EqualFold(name, "namespace") {
		c.deprecated(span, "a class constant named namespace is deprecated since PHP 8.6")
	}
}

// debugInfoReturn judges __debugInfo()'s declared return type: PHP 8.6
// deprecates a nullable one, null being no debug information.
func (c *Checker) debugInfoReturn(name string, ret ast.TypeExpr) {
	if !strings.EqualFold(name, "__debugInfo") || ret == nil || !nullableType(ret) {
		return
	}
	c.deprecated(ret.Span(), "a nullable return type for __debugInfo() is deprecated since PHP 8.6: declare array and return [] for nothing")
}

func nullableType(t ast.TypeExpr) bool {
	switch t := t.(type) {
	case *ast.NullableType:
		return true
	case *ast.UnionType:
		for _, m := range t.Types {
			if nt, ok := m.(*ast.NamedType); ok && len(nt.Name.Parts) == 1 && strings.EqualFold(nt.Name.Parts[0], "null") {
				return true
			}
		}
	}
	return false
}

// structor names __construct() or __destruct() when the context checks the
// body of one, "" otherwise.
func (x *fnCtx) structor() string {
	if x.method == nil {
		return ""
	}
	switch strings.ToLower(x.method.Name) {
	case "__construct":
		return "constructor"
	case "__destruct":
		return "destructor"
	}
	return ""
}

// structorYield judges a yield: in a constructor or a destructor it makes
// the method a generator, whose body then never runs where PHP calls it.
func (x *fnCtx) structorYield(span ast.Span) {
	if s := x.structor(); s != "" {
		x.c.deprecated(span, "yield makes the %s a generator, which is deprecated since PHP 8.6: its body never runs", s)
	}
}

// deprecatedCall judges the calls whose arguments PHP 8.6 deprecates:
// define()'s third argument, is_a() and is_subclass_of() given a class
// name while $allow_string is false, and the names define() and
// class_alias() declare.
func (x *fnCtx) deprecatedCall(lower string, e *ast.Call, env *state) {
	if !x.c.opts.Deprecations || e.Args == nil {
		return
	}
	// argTypes finds the argument passed to a parameter, by position or
	// by name.
	argTypes := func(i int, name string) (ast.Expr, bool) {
		for j, a := range e.Args.Args {
			if a.Unpack || a.Placeholder != nil || a.Value == nil {
				continue
			}
			if (a.Name == nil && j == i) || (a.Name != nil && a.Name.Name == name) {
				return a.Value, true
			}
		}
		return nil, false
	}
	stringLitValue := program.StringValue
	exprType := func(v ast.Expr) types.Type { return x.pathType(v, env) }
	switch lower {
	case "define":
		if v, ok := argTypes(0, "constant_name"); ok {
			if lit, ok := v.(*ast.StringLit); ok {
				x.c.constantName(v.Span(), stringLitValue(lit))
			}
		}
		if v, ok := argTypes(2, "case_insensitive"); ok {
			x.c.deprecated(v.Span(), "define()'s third argument is deprecated since PHP 8.6: case-insensitive constants are gone, and PHP ignores it")
		}
	case "class_alias":
		if v, ok := argTypes(1, "alias"); ok {
			if lit, ok := v.(*ast.StringLit); ok {
				alias := stringLitValue(lit)
				if i := strings.LastIndexByte(alias, '\\'); i >= 0 {
					alias = alias[i+1:]
				}
				if reservedNames[strings.ToLower(alias)] {
					x.c.deprecated(v.Span(), "`%s` as a class alias is deprecated since PHP 8.6", strings.ToLower(alias))
				}
			}
		}
	case "is_a", "is_subclass_of":
		subject, ok := argTypes(0, "object_or_class")
		if !ok {
			return
		}
		allowString := lower == "is_subclass_of" // the defaults differ
		if v, ok := argTypes(2, "allow_string"); ok {
			t := exprType(v)
			switch {
			case types.Equal(t, types.TFalse):
				allowString = false
			case types.Equal(t, types.TTrue):
				allowString = true
			default:
				return // decided at run time
			}
		}
		if allowString || !mayBeString(exprType(subject)) {
			return
		}
		x.c.deprecated(subject.Span(), "%s() with a class name and $allow_string false is deprecated since PHP 8.6: pass allow_string: true, or an object", lower)
	}
}

// mayBeString reports a type that admits a string, mixed aside.
func mayBeString(t types.Type) bool {
	if types.IsMixed(t) {
		return false
	}
	for _, m := range types.Members(t) {
		switch m.(type) {
		case types.String, types.StringLit, types.ClassString:
			return true
		}
	}
	return false
}

// numbersOrStrings reports a type whose every member is an int, a float or
// a string: what ++ and -- take, a member at a time (stepMembers).
func numbersOrStrings(t types.Type) bool {
	for _, m := range types.Members(t) {
		if !types.IsSubtype(m, types.TInt, nil) && !types.IsSubtype(m, types.TFloat, nil) && !types.IsSubtype(m, types.TString, nil) {
			return false
		}
	}
	return true
}

// stepMembers is the type ++ or -- gives an operand whose every member is
// an int, a float or a string, a member at a time, as PHP turns it: an int
// as the sum or the difference with 1 (binaryTypes: 5 becomes 6, the last
// integer a float), a float a float, and a string PHP reads as a number
// (is_numeric()) that number, a literal its exact value. refused reports a
// string member that is no number, or may be none: PHP steps it as text,
// which it deprecates, and PHPStan refuses the operand (postInc.type,
// postDec.type), so the result says nothing then. all reports that every
// member is such a string. since is the release that deprecated the step
// of the refused members, empty where they do not tell it: -- of any of
// them is 8.3's, ++ is 8.3's or 8.5's by the string (incDeprecated), which
// only a literal shows.
func (x *fnCtx) stepMembers(t types.Type, e *ast.IncDec) (result types.Type, refused, all bool, since string) {
	var out []types.Type
	all = true
	// refuse notes a refused member and the release that deprecated its
	// step, "" where the member is no literal.
	releases := map[string]bool{}
	refuse := func(release string) {
		refused = true
		if !e.Inc {
			release = "8.3"
		}
		releases[release] = true
	}
	op, delta := token.Kind('-'), int64(-1)
	if e.Inc {
		op, delta = token.Kind('+'), 1
	}
	for _, m := range types.Members(t) {
		switch {
		case types.IsSubtype(m, types.TInt, nil):
			out = append(out, x.binaryTypes(op, m, types.IntLit(1), e))
			all = false
			continue
		case types.IsSubtype(m, types.TFloat, nil):
			out = append(out, types.TFloat)
			all = false
			continue
		}
		switch m := m.(type) {
		case types.StringLit:
			if !isNumericLiteral(m.V) {
				refuse(incDeprecated(m.V))
				continue
			}
			all = false
			if n, err := strconv.ParseInt(strings.Trim(m.V, " \t\n\r\v\f"), 10, 64); err == nil && (delta > 0 && n < math.MaxInt64 || delta < 0 && n > math.MinInt64) {
				out = append(out, types.IntLit(n+delta))
			} else {
				out = append(out, types.TFloat)
			}
		case types.String:
			all = false
			if !m.Numeric {
				refuse("")
				continue
			}
			out = append(out, types.TInt, types.TFloat)
		case types.ClassString:
			refuse("") // a class name is never a number
		default:
			refuse("")
			all = false
		}
	}
	if len(releases) == 1 {
		for since = range releases {
		}
	}
	return types.MakeUnion(out...), refused, refused && all, since
}

// incDeprecated is the release that deprecated ++ on a string literal that
// is no number: PHP 8.3 the empty string and a string that is not
// alphanumeric (' ', 'a b', '1_000'), whose step never was the next string
// in order, and PHP 8.5 an alphanumeric one ('a', 'Az9'), which
// str_increment() steps.
func incDeprecated(s string) string {
	if s == "" {
		return "8.3"
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return "8.3"
		}
	}
	return "8.5"
}

// mayBeArray reports whether a value of t may be a PHP array, where null as
// an offset is deprecated: an ArrayAccess object's method gets the null as
// it is, and a string's offset is cast with PHP's warning, not this.
func mayBeArray(t types.Type) bool {
	for _, m := range types.Members(t) {
		if types.IsSubtype(m, types.TArray, nil) {
			return true
		}
	}
	return false
}

// containsNull reports a type with null among its members.
func containsNull(t types.Type) bool {
	for _, m := range types.Members(t) {
		if isNullType(m) {
			return true
		}
	}
	return false
}

// nullOffset reports null as an array's offset, which PHP 8.5 deprecates in
// a read, a write, an isset(), a literal's key and a destructuring's (not in
// unset()).
func (x *fnCtx) nullOffset(raw types.Type, at ast.Span) {
	if isNullType(raw) {
		x.c.deprecated(at, "null as an array offset is deprecated since PHP 8.5: write ''")
	}
}

// isNumericLiteral is PHP's is_numeric() of a string: optional whitespace,
// a sign, digits with a decimal point or an exponent, trailing whitespace.
func isNumericLiteral(s string) bool {
	s = strings.Trim(s, " \t\n\r\v\f")
	if s == "" {
		return false
	}
	if s[0] == '+' || s[0] == '-' {
		s = s[1:]
	}
	digits, dot, exp := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digits = true
		case c == '.' && !dot && !exp:
			dot = true
		case (c == 'e' || c == 'E') && digits && !exp:
			exp, digits = true, false
			if i+1 < len(s) && (s[i+1] == '+' || s[i+1] == '-') {
				i++
			}
		default:
			return false
		}
	}
	return digits
}
