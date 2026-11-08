package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// This file holds the rules that only Phaenon's generic syntax needs:
// type-argument arity and bounds, variance positions, what an erased type
// parameter may be used for, `is` / `as`, explicit type arguments on calls.

// typeParamRef reports whether a name in an expression position refers to
// a type parameter in scope (`new T`, `T::class`, `$x instanceof T`, `T`).
func (x *fnCtx) typeParamRef(n *ast.Name) *program.TypeParam {
	if len(n.Parts) != 1 || n.Kind != ast.NameUnqualified {
		return nil
	}
	return x.res.TypeParam(n.Parts[0])
}

// paramValue reports a use of a type parameter as a value. Type parameters
// are erased, so `new T`, `T::class` and `$x instanceof T` have nothing to
// run on; the fix is a `class<T>` value (design/generics.md §11.2). ok is
// always false; t and cl describe the parameter for the caller's recovery.
func (x *fnCtx) paramValue(tp *program.TypeParam, span ast.Span, use string) (t types.Type, cl *program.Class, ok bool) {
	t = x.res.ParamType(tp)
	if names := types.ClassNames(tp.Bound); len(names) == 1 {
		cl = x.p().Class(names[0])
	}
	x.report(span, P3003, "%s on type parameter %s, which is erased at runtime: take a class<%s> value and use that instead", use, tp.Name, tp.Name)
	return t, cl, false
}

// paramAsValue types a bare `T` in expression position: always an error,
// a type parameter has no runtime value.
func (x *fnCtx) paramAsValue(tp *program.TypeParam, span ast.Span) types.Type {
	x.paramValue(tp, span, "use as a value")
	return types.TError
}

// ------------------------------------------------------------------ type expressions

// checkTypeExpr validates the generic parts of a type written in source:
// arity and bounds of type arguments, arguments on things that take none.
func (x *fnCtx) checkTypeExpr(t ast.TypeExpr) {
	switch t := t.(type) {
	case nil:
	case *ast.NamedType:
		for _, a := range t.TypeArgs {
			x.checkTypeExpr(a)
		}
		x.checkNamedType(t)
	case *ast.NullableType:
		x.checkTypeExpr(t.Type)
	case *ast.UnionType:
		for _, m := range t.Types {
			x.checkTypeExpr(m)
		}
	case *ast.IntersectionType:
		for _, m := range t.Types {
			x.checkTypeExpr(m)
		}
	case *ast.ClosureType:
		for _, m := range t.Params {
			x.checkTypeExpr(m.Type)
		}
		x.checkTypeExpr(t.Return)
	case *ast.TupleType:
		for _, m := range t.Elems {
			x.checkTypeExpr(m)
		}
	}
}

// keywordArity is the number of type arguments the built-in type keywords
// accept in Phaenon syntax. PHPStan's hyphenated names are not among them:
// they are docblock vocabulary (design/generics.md §11.2).
var keywordArity = map[string][2]int{
	"list": {1, 1}, "map": {2, 2}, "array": {0, 2}, "iterable": {0, 2}, "class": {0, 1},
	"closure": {0, 0}, "callable": {0, 0}, "scalar": {0, 0}, "numeric": {0, 0}, "number": {0, 0},
	"int": {0, 0}, "float": {0, 0}, "string": {0, 0}, "bytes": {0, 0}, "bool": {0, 0}, "mixed": {0, 0},
	"null": {0, 0}, "void": {0, 0}, "never": {0, 0}, "object": {0, 0}, "self": {0, 0},
	"static": {0, 0}, "parent": {0, 0}, "true": {0, 0}, "false": {0, 0}, "resource": {0, 0},
}

func (x *fnCtx) checkNamedType(t *ast.NamedType) {
	given := len(t.TypeArgs)
	if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified {
		name := t.Name.Parts[0]
		if tp := x.res.TypeParam(name); tp != nil {
			if given > 0 {
				x.report(t.S, P3001, "type parameter %s takes no type arguments", name)
			}
			return
		}
		lower := strings.ToLower(name)
		if (lower == "self" || lower == "static") && given > 0 {
			if cl := x.res.Class(); cl != nil {
				cl.Signature()
				x.checkArity(t, lower, cl.TypeParams, given) // `self<T>` in a generic class
				return
			}
		}
		if ar, ok := keywordArity[lower]; ok {
			if lower == "list" && given == 0 && x.testedNames[t] {
				return // `is list`: list<mixed> (design/generics.md §11.4)
			}
			if given < ar[0] || given > ar[1] {
				x.report(t.S, P3001, "%s takes %s, %d given", lower, arityText(ar[0], ar[1]), given)
			} else if lower == "map" && given == 2 {
				x.checkMapKey(t.TypeArgs[0])
			}
			return
		}
		if a := x.res.FileAlias(name); a != nil {
			x.checkArity(t, name, a.Params, given)
			return
		}
	}
	name := x.res.ClassName(t.Name)
	switch name {
	case "self", "static", "parent":
		if cl := x.res.Class(); cl != nil && given > 0 && name != "parent" {
			cl.Signature()
			x.checkArity(t, name, cl.TypeParams, given) // `self<T>` in a generic class
			return
		}
		if given > 0 {
			x.report(t.S, P3001, "%s takes no type arguments", name)
		}
		return
	}
	cl := x.p().Class(name)
	if cl == nil {
		return // reported by the resolver
	}
	cl.Signature()
	x.checkArity(t, cl.Name, cl.TypeParams, given)
}

// checkMapKey enforces map's key bound (design/collections.md §2): a key
// type is int|string, or a type parameter whose bound is; an unbounded
// parameter used as a key means `K of int|string`.
func (x *fnCtx) checkMapKey(key ast.TypeExpr) {
	k := x.res.Native(key)
	if k == nil || types.IsMixed(k) {
		return
	}
	if p, ok := k.(types.Param); ok {
		if p.Bound == nil {
			return
		}
		k = p.Bound
	}
	if _, unknown := k.(types.Unknown); unknown || types.IsSubtype(k, types.TArrayKey, x.p()) {
		return
	}
	x.report(key.Span(), P2518, "a map key is int|string, not %s", x.typeName(k))
}

// checkArity checks a type-argument list against the declared parameters,
// including the bounds.
func (x *fnCtx) checkArity(t *ast.NamedType, what string, tps []*program.TypeParam, given int) {
	if given == 0 {
		return // Foo without arguments means its bounds, as in PHPStan
	}
	if given == 1 && program.ValueOnlyIterable(what) {
		return // Iterator<V>: the value alone, the key left mixed
	}
	required := 0
	for _, tp := range tps {
		if tp.Default == nil {
			required++
		}
	}
	if given > len(tps) || given < required {
		x.report(t.S, P3001, "%s takes %s, %d given", what, arityText(required, len(tps)), given)
		return
	}
	args := x.res.NativeArgs(t.TypeArgs)
	subst := map[string]types.Type{}
	for i, tp := range tps {
		if i < len(args) {
			subst[tp.Name] = args[i]
		} else if tp.Default != nil {
			subst[tp.Name] = tp.Default
		}
	}
	for i, tp := range tps {
		if i >= len(args) || tp.Bound == nil {
			continue
		}
		bound := types.Substitute(tp.Bound, subst)
		if !types.IsSubtype(args[i], bound, x.p()) {
			x.report(t.TypeArgs[i].Span(), P3002, "type argument %s for %s of %s is not a subtype of its bound %s", x.typeName(args[i]), tp.Name, what, x.typeName(bound))
		}
	}
}

func arityText(min, max int) string {
	switch {
	case min == max && min == 0:
		return "no type arguments"
	case min == max:
		return itoa(min) + " type argument" + plural(min)
	default:
		return itoa(min) + " to " + itoa(max) + " type arguments"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// checkTypeParams validates a declaration's own type parameters: their
// bounds and defaults.
func (x *fnCtx) checkTypeParams(tps []*ast.TypeParam) {
	seen := map[string]bool{}
	for _, tp := range tps {
		if seen[tp.Name.Name] {
			x.report(tp.S, P2511, "type parameter %s is declared twice", tp.Name.Name)
		}
		seen[tp.Name.Name] = true
		x.checkTypeExpr(tp.Bound)
		x.checkTypeExpr(tp.Default)
		if tp.Default != nil && tp.Bound != nil {
			d, b := x.res.Native(tp.Default), x.res.Native(tp.Bound)
			if !types.IsSubtype(d, b, x.p()) {
				x.report(tp.Default.Span(), P3002, "default %s of %s is not a subtype of its bound %s", x.typeName(d), tp.Name.Name, x.typeName(b))
			}
		}
	}
}

// ------------------------------------------------------------------ variance

// variancePos is a position in a type: +1 covariant (output), -1
// contravariant (input), 0 invariant.
type variancePos int8

// checkVariance reports class type parameters used against their declared
// variance. Private members are exempt (they are not part of the
// subtyping contract).
func (c *Checker) checkVariance(cl *program.Class) {
	annotated := false
	for _, tp := range cl.TypeParams {
		if tp.Native && tp.Variance != types.Invariant {
			annotated = true
		}
	}
	if !annotated {
		return
	}
	ctx := c.newCtx(cl.File, cl.Scope, cl, false)
	for _, m := range cl.Methods {
		if m.Class != cl || m.Vis == program.Private || m.Decl == nil || m.Static {
			continue
		}
		if strings.EqualFold(m.Name, "__construct") {
			continue // PHPStan exempts constructors: they are not part of the contract
		}
		for i, p := range m.Params {
			span := m.Span
			if i < len(m.Decl.Params) {
				span = m.Decl.Params[i].S
			}
			ctx.variance(p.Type, -1, span, "parameter $"+p.Name+" of "+m.Name+"()")
		}
		if m.Return != nil {
			span := m.Span
			if m.Decl.ReturnType != nil {
				span = m.Decl.ReturnType.Span()
			}
			ctx.variance(m.Return, +1, span, "return type of "+m.Name+"()")
		}
	}
	for _, p := range cl.Props {
		if p.Class != cl || p.Vis == program.Private || p.Static {
			continue
		}
		pos := variancePos(0)
		if p.Readonly {
			pos = +1 // never written after construction
		}
		ctx.variance(p.Type, pos, p.Span, "property $"+p.Name)
	}
}

func (x *fnCtx) variance(t types.Type, pos variancePos, span ast.Span, where string) {
	switch t := t.(type) {
	case nil:
	case types.Param:
		for _, tp := range x.class.TypeParams {
			if tp.Name != t.Name || !tp.Native {
				continue
			}
			switch {
			case tp.Variance == types.Covariant && pos != +1:
				x.report(span, P3004, "out %s cannot be used in %s (an input position)", tp.Name, where)
			case tp.Variance == types.Contravariant && pos != -1:
				x.report(span, P3004, "in %s cannot be used in %s (an output position)", tp.Name, where)
			}
		}
	case types.Union:
		for _, m := range t.Types {
			x.variance(m, pos, span, where)
		}
	case types.Intersection:
		for _, m := range t.Types {
			x.variance(m, pos, span, where)
		}
	case types.Array:
		x.variance(t.Key, pos, span, where)
		x.variance(t.Value, pos, span, where)
	case types.Shape:
		for _, e := range t.Entries {
			x.variance(e.Type, pos, span, where)
		}
	case types.Iterable:
		x.variance(t.Key, pos, span, where)
		x.variance(t.Value, pos, span, where)
	case types.ClassString:
		x.variance(t.Of, pos, span, where)
	case types.Callable:
		for _, p := range t.Params {
			x.variance(p.Type, -pos, span, where)
		}
		x.variance(t.Return, pos, span, where)
	case types.Class:
		vs := x.p().Variances(t.Name)
		for i, a := range t.Args {
			argPos := variancePos(0)
			if i < len(vs) {
				switch vs[i] {
				case types.Covariant:
					argPos = pos
				case types.Contravariant:
					argPos = -pos
				}
			}
			x.variance(a, argPos, span, where)
		}
	case types.Static:
		if t.Of != nil {
			x.variance(*t.Of, pos, span, where)
		}
	case types.This:
		if t.Of != nil {
			x.variance(*t.Of, pos, span, where)
		}
	case types.Conditional:
		x.variance(t.Then, pos, span, where)
		x.variance(t.Else, pos, span, where)
	}
}

// ------------------------------------------------------------------ declarations

// checkGenericClass applies the class-level rules: generic property
// redeclaration, static members and variance.
func (c *Checker) checkGenericClass(cl *program.Class) {
	d := cl.Decl
	if cl.File.Mode != lexer.ModePhaenon || d == nil {
		return
	}
	ctx := c.newCtx(cl.File, cl.Scope, cl, false)
	ctx.checkTypeParams(d.TypeParams)
	for _, e := range d.Extends {
		ctx.checkTypeExpr(e)
	}
	for _, e := range d.Implements {
		ctx.checkTypeExpr(e)
	}
	for _, m := range d.Members {
		if pd, ok := m.(*ast.PropertyDecl); ok {
			ctx.checkTypeExpr(pd.Type)
		}
	}
	// Properties whose type mentions a class parameter cannot be
	// redeclared: PHP property types are invariant and erasure fixes them
	// in the declaring class.
	for _, p := range cl.Props {
		if p.Class != cl || p.FromTrait != nil || p.Static || p.Magic {
			continue
		}
		for _, a := range cl.Ancestors() {
			if a == cl {
				continue
			}
			a.Signature()
			pp := a.FindProperty(p.Name)
			if pp == nil || pp.Class != a || pp.Vis == program.Private {
				continue
			}
			if mentionsParam(pp.Type) {
				c.report(p.Span, P3007, "property $%s is declared with a generic type in %s and cannot be redeclared", p.Name, a.Name)
			}
			break
		}
	}
	// A static property is one slot shared by every instantiation, so no
	// class parameter can type it. A static method may use one: late static
	// binding resolves it (`static::aggregateClass(): class<T>`, the idiom
	// of design/generics.md §11.2).
	for _, p := range cl.Props {
		if p.Class == cl && p.Static && p.NativeType != nil && ctx.mentionsClassParam(p.NativeType) {
			c.report(p.Span, P3008, "type parameter of class %s used in static property $%s", cl.Name, p.Name)
		}
	}
	c.checkVariance(cl)
}

func mentionsParam(t types.Type) bool {
	found := false
	types.Walk(t, func(m types.Type) {
		if _, ok := m.(types.Param); ok {
			found = true
		}
	})
	return found
}

// mentionsFreshParam reports a type parameter a call is inferring: the
// fresh names checkCallArgs gives them hold a NUL.
func mentionsFreshParam(t types.Type) bool {
	found := false
	types.Walk(t, func(m types.Type) {
		if p, ok := m.(types.Param); ok && strings.Contains(p.Name, "\x00") {
			found = true
		}
	})
	return found
}

func (x *fnCtx) mentionsClassParam(t types.Type) bool {
	if x.class == nil {
		return false
	}
	found := false
	types.Walk(t, func(m types.Type) {
		if p, ok := m.(types.Param); ok {
			for _, tp := range x.class.TypeParams {
				if tp.Name == p.Name && tp.Native {
					found = true
				}
			}
		}
	})
	return found
}

// checkSignatureTypes validates the written types of a function or
// method: type arguments, and its own type parameters.
func (x *fnCtx) checkSignatureTypes(tps []*ast.TypeParam, params []*ast.Param, ret ast.TypeExpr) {
	for _, p := range params {
		x.libraryTypes(p.Type)
	}
	x.libraryTypes(ret)
	if x.file.Mode != lexer.ModePhaenon {
		return
	}
	x.checkTypeParams(tps)
	for _, p := range params {
		x.checkTypeExpr(p.Type)
	}
	x.checkTypeExpr(ret)
}

// checkTypeAlias validates a `type` statement.
func (x *fnCtx) checkTypeAlias(a *ast.TypeAlias) {
	decl := x.res.FileAlias(a.Name.Name)
	if decl == nil || decl.Decl != a {
		x.report(a.Name.S, P2511, "type alias %s is already declared", a.Name.Name)
		return
	}
	saved := x.res
	x.res = x.res.WithParams(decl.Params)
	x.checkTypeParams(a.TypeParams)
	x.checkTypeExpr(a.Type)
	x.res = saved
}

// ------------------------------------------------------------------ is / as

// testType resolves the right-hand side of `is` / `as`, where a bare
// `list` the operand is tested against is list<mixed>. A map with a mixed
// key is the array PHP's is_array() narrows to: every key of a PHP array
// is an int or a string. narrow is what a passing test narrows the operand
// to (testNarrowing); ok says the type drew no finding of its own.
func (x *fnCtx) testType(t ast.TypeExpr) (target, narrow types.Type, ok bool) {
	x.testedNames = testedNames(t, nil)
	mark := x.c.mark()
	x.checkTypeExpr(t)
	x.testedNames = nil
	// A finding is reported once: a pass that meets it again, unless the
	// first pass was rolled back, finds it seen and reports nothing.
	// Whether the type has one is kept from the pass that first saw it.
	if x.c.typeFindings == nil {
		x.c.typeFindings = map[ast.TypeExpr]bool{}
	}
	found, known := x.c.typeFindings[t]
	if !known {
		found = len(x.c.diags) > mark
		x.c.typeFindings[t] = found
	}
	ok = !found
	target = types.MapDeep(x.res.Native(t), func(m types.Type) types.Type {
		if a, ok := m.(types.Array); ok && types.IsMixed(a.Key) {
			a.Key = types.TArrayKey
			return a
		}
		return m
	})
	return target, testNarrowing(target, false), ok
}

// testedNames are the named types an operand, or one of a tuple's members,
// is tested against: the members of unions, intersections, ?T and tuples,
// not type arguments (`Box<list>`, `class<list>`).
func testedNames(t ast.TypeExpr, into map[*ast.NamedType]bool) map[*ast.NamedType]bool {
	if into == nil {
		into = map[*ast.NamedType]bool{}
	}
	switch t := t.(type) {
	case *ast.NamedType:
		into[t] = true
	case *ast.NullableType:
		testedNames(t.Type, into)
	case *ast.UnionType:
		for _, m := range t.Types {
			testedNames(m, into)
		}
	case *ast.IntersectionType:
		for _, m := range t.Types {
			testedNames(m, into)
		}
	case *ast.TupleType:
		for _, m := range t.Elems {
			testedNames(m, into)
		}
	}
	return into
}

// testNarrowing is what PHPStan narrows the operand of a passing test to on
// the output, which the checker may not exceed (design/checker.md §9): the
// tested type, except a tuple whose test is one side of a || (`?(int,
// string)`, `int|(int, string)`). PHPStan keeps such a tuple's count and
// drops its members' types — and the count too when a member is a tuple —
// as the branch where the other side held never read the members.
func testNarrowing(t types.Type, underOr bool) types.Type {
	switch v := t.(type) {
	case types.Union:
		ms := make([]types.Type, len(v.Types))
		for i, m := range v.Types {
			ms[i] = testNarrowing(m, true)
		}
		return types.MakeUnion(ms...)
	case types.Intersection:
		ms := make([]types.Type, len(v.Types))
		for i, m := range v.Types {
			ms[i] = testNarrowing(m, underOr)
		}
		return types.MakeIntersection(ms...)
	case types.Shape:
		if !tuple(v) {
			return t
		}
		entries := make([]types.ShapeEntry, len(v.Entries))
		for i, e := range v.Entries {
			if underOr {
				if containsTuple(e.Type) {
					return anyArray
				}
				e.Type = types.TMixed
			} else {
				e.Type = testNarrowing(e.Type, false)
			}
			entries[i] = e
		}
		v.Entries = entries
		return v
	}
	return t
}

func containsTuple(t types.Type) bool {
	found := false
	types.Walk(t, func(m types.Type) {
		if s, ok := m.(types.Shape); ok && tuple(s) {
			found = true
		}
	})
	return found
}

// settles reports the test of an operand of type t, which PHP holds to nt,
// against target when the types decide it: always or never true, as both
// the inferred and the PHP type say (P2306). A test against mixed is
// always true.
func (x *fnCtx) settles(t, nt, target types.Type) (always, never bool) {
	if types.IsMixed(target) {
		return true, false
	}
	if types.IsMixed(t) || isParamType(t) || isParamType(target) {
		return false, false
	}
	always = types.IsSubtype(t, target, x.p()) && !types.IsMixed(nt) && types.IsSubtype(nt, target, x.p())
	never = !types.Overlaps(t, target, x.p()) && !types.IsMixed(nt) && !types.Overlaps(nt, target, x.p())
	return always, never
}

// reportSettled reports a test the types settle (P2306), unless the type
// has no test at all: that is the one finding (P3011).
func (x *fnCtx) reportSettled(span ast.Span, t, nt, target types.Type, untestable bool) (never bool) {
	always, never := x.settles(t, nt, target)
	switch {
	case untestable:
	case always:
		x.report(span, P2306, "%s is always %s", x.typeName(t), x.typeName(target))
	case never:
		x.report(span, P2306, "%s is never %s", x.typeName(t), x.typeName(target))
	}
	return never
}

// isCond narrows on `$x is T`, exactly as instanceof does for classes.
func (x *fnCtx) isCond(e *ast.IsExpr, env *state) (then, els *state) {
	t := x.expr(e.Expr, env, nil)
	target, narrow, typeOK := x.testType(e.Type)
	x.condValueType = types.TBool
	then, els = env.clone(), env.clone()
	nt := x.guaranteedOr(e.Expr, t, env)
	if !types.IsMixed(t) && !isParamType(t) && !isParamType(target) {
		switch {
		case types.IsSubtype(t, target, x.p()):
			x.condValueType = types.TTrue
		case !types.Overlaps(t, target, x.p()):
			x.condValueType = types.TFalse
		}
	}
	c, untestable := x.lowerTest(e, e.Expr, e.Type, "is", t, nt, target, typeOK, env)
	x.reportSettled(e.S, t, nt, target, untestable)
	var nthen, nels types.Type
	nold := x.nativeVar(e.Expr, env)
	if nold != nil {
		nthen = types.Intersect(nold, narrow, x.p())
	}
	x.narrowPathNative(then, e.Expr, types.Intersect(t, narrow, x.p()), nthen, true)
	// The else branch narrows as PHPStan does on the emitted test, which
	// is less than the whole type for a test joined with && (elseType).
	if c != nil {
		if nold != nil {
			nels = x.elseType(c, nold)
		}
		x.narrowPathNative(els, e.Expr, x.elseType(c, t), nels, false)
	}
	return then, els
}

// isType types an `is` used as a value.
func (x *fnCtx) isType(e *ast.IsExpr, env *state) types.Type {
	then, els := x.isCond(e, env)
	*env = *join(then, els)
	return types.TBool
}

// asExpr types `$x as T` (TypeError on failure) and `$x as? T` (null).
func (x *fnCtx) asExpr(e *ast.AsExpr, env *state) types.Type {
	t := x.expr(e.Expr, env, nil)
	target, narrow, typeOK := x.testType(e.Type)
	nt := x.guaranteedOr(e.Expr, t, env)
	op := "as"
	if e.Nullable {
		op = "as?"
	}
	c, untestable := x.lowerTest(e, e.Expr, e.Type, op, t, nt, target, typeOK, env)
	lowered := c != nil
	never := x.reportSettled(e.S, t, nt, target, untestable)
	result := narrow
	if !types.IsMixed(t) && !isParamType(t) && !isParamType(target) && !never {
		result = types.Intersect(t, narrow, x.p())
	}
	if e.Nullable {
		return types.Nullable(result)
	}
	if !lowered {
		return result
	}
	// `$x as T` throws unless $x is a T: after it, $x is one, as PHPStan
	// reads `\is_int($x) ? $x : throw …`.
	var nthen types.Type
	if nold := x.nativeVar(e.Expr, env); nold != nil {
		nthen = types.Intersect(nold, narrow, x.p())
	}
	x.narrowPathNative(env, e.Expr, result, nthen, true)
	return result
}

// ------------------------------------------------------------------ explicit type arguments

// explicitArgs resolves `<T, U>` written at a call site against the
// callee's own type parameters. The result seeds the inference.
func (x *fnCtx) explicitArgs(exprs []ast.TypeExpr, tps []*program.TypeParam, what string, span ast.Span) []types.Type {
	if len(exprs) == 0 {
		return nil
	}
	for _, t := range exprs {
		x.checkTypeExpr(t)
	}
	args := x.res.NativeArgs(exprs)
	if len(tps) == 0 {
		x.report(span, P3009, "%s is not generic but type arguments are given", what)
		return nil
	}
	if len(args) > len(tps) {
		x.report(span, P3001, "%s takes %s, %d given", what, arityText(0, len(tps)), len(args))
		return nil
	}
	subst := map[string]types.Type{}
	for i, a := range args {
		subst[tps[i].Name] = a
	}
	for i, a := range args {
		if tps[i].Bound == nil {
			continue
		}
		bound := types.Substitute(tps[i].Bound, subst)
		if !types.IsSubtype(a, bound, x.p()) {
			x.report(exprs[i].Span(), P3002, "type argument %s for %s of %s is not a subtype of its bound %s", x.typeName(a), tps[i].Name, what, x.typeName(bound))
		}
	}
	return args
}

// closureTypeParams brings a closure's own `<T>` into scope.
func (x *fnCtx) closureTypeParams(tps []*ast.TypeParam, decl []*ast.Param, ret ast.TypeExpr) {
	if len(tps) == 0 {
		return
	}
	x.checkTypeParams(tps)
	params := x.res.NativeParams(tps)
	nodes := make([]ast.Node, 0, len(decl)+1)
	for _, p := range decl {
		if p.Type != nil {
			nodes = append(nodes, p.Type)
		}
	}
	if ret != nil {
		nodes = append(nodes, ret)
	}
	program.ImplyMapKeyBounds(params, nodes...)
	x.res = x.res.WithParams(params)
	scope := make(map[string]bool, len(x.inScope)+len(params))
	for k, v := range x.inScope {
		scope[k] = v
	}
	for _, tp := range params {
		scope[tp.Name] = true
	}
	x.inScope = scope
}
