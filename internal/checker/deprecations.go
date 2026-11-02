package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
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
			if a.Unpack || a.Placeholder || a.Value == nil {
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
