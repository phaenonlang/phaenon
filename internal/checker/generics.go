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
// parameter may be used for, `is` / `as`, explicit type arguments on calls
// and the placement of `reified`.

// RuntimeType is the class that carries a reified type parameter at
// runtime (the `phaenon/runtime` package).
const RuntimeType = "Phaenon\\Type"

// typeParamRef reports whether a name in an expression position refers to
// a type parameter in scope (`new T`, `T::class`, `$x instanceof T`, `T`).
func (x *fnCtx) typeParamRef(n *ast.Name) *program.TypeParam {
	if len(n.Parts) != 1 || n.Kind != ast.NameUnqualified {
		return nil
	}
	return x.res.TypeParam(n.Parts[0])
}

// paramValue types a use of a type parameter as a value and reports the
// uses that erasure forbids. ok is false when the use was rejected.
func (x *fnCtx) paramValue(tp *program.TypeParam, span ast.Span, use string) (t types.Type, cl *program.Class, ok bool) {
	t = x.res.ParamType(tp)
	if names := types.ClassNames(tp.Bound); len(names) == 1 {
		cl = x.p().Class(names[0])
	}
	if !tp.Reified {
		x.report(span, P3003, "%s on erased type parameter %s: declare it `reified`, or test against its bound", use, tp.Name)
		return t, cl, false
	}
	if x.static && x.classParam(tp) {
		x.report(span, P3008, "type parameter %s of class %s is not available in a static member", tp.Name, x.class.Name)
		return t, cl, false
	}
	return t, cl, true
}

// classParam reports whether tp is one of the current class's parameters.
func (x *fnCtx) classParam(tp *program.TypeParam) bool {
	if x.class == nil {
		return false
	}
	for _, c := range x.class.TypeParams {
		if c == tp {
			return true
		}
	}
	return false
}

// paramAsValue types a bare `T` in expression position: the runtime
// descriptor of a reified parameter.
func (x *fnCtx) paramAsValue(tp *program.TypeParam, span ast.Span) types.Type {
	t, _, ok := x.paramValue(tp, span, "use as a value")
	if !ok {
		return types.TError
	}
	return types.Class{Name: RuntimeType, Args: []types.Type{t}}
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
			x.checkTypeExpr(m)
		}
		x.checkTypeExpr(t.Return)
	}
}

// keywordArity is the number of type arguments the built-in type keywords
// accept in Phaenon syntax.
var keywordArity = map[string][2]int{
	"list": {1, 1}, "non-empty-list": {1, 1}, "map": {2, 2},
	"array": {0, 2}, "non-empty-array": {0, 2}, "iterable": {0, 2},
	"class-string": {0, 1}, "closure": {0, 0}, "callable": {0, 0},
	"int": {0, 0}, "float": {0, 0}, "string": {0, 0}, "bool": {0, 0}, "mixed": {0, 0},
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
		if ar, ok := keywordArity[lower]; ok {
			if given < ar[0] || given > ar[1] {
				x.report(t.S, P3001, "%s takes %s, %d given", lower, arityText(ar[0], ar[1]), given)
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

// checkArity checks a type-argument list against the declared parameters,
// including the bounds.
func (x *fnCtx) checkArity(t *ast.NamedType, what string, tps []*program.TypeParam, given int) {
	if given == 0 {
		return // Foo without arguments means its bounds, as in PHPStan
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
// bounds and defaults, and where `reified` may appear.
func (x *fnCtx) checkTypeParams(tps []*ast.TypeParam, reifiedAllowed bool, where string) {
	seen := map[string]bool{}
	for _, tp := range tps {
		if seen[tp.Name.Name] {
			x.report(tp.S, P2511, "type parameter %s is declared twice", tp.Name.Name)
		}
		seen[tp.Name.Name] = true
		x.checkTypeExpr(tp.Bound)
		x.checkTypeExpr(tp.Default)
		if tp.Reified && !reifiedAllowed {
			x.report(tp.S, P3006, "%s cannot declare a reified type parameter", where)
		}
		if tp.Reified && tp.Variance != ast.Invariant {
			x.report(tp.S, P3006, "reified type parameter %s cannot be in or out: a runtime value is invariant", tp.Name.Name)
		}
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

// checkGenericClass applies the class-level rules: reified placement,
// generic property redeclaration, static members and variance.
func (c *Checker) checkGenericClass(cl *program.Class) {
	d := cl.Decl
	if cl.File.Mode != lexer.ModePhaenon || d == nil {
		return
	}
	ctx := c.newCtx(cl.File, cl.Scope, cl, false)
	where := "class " + cl.Name
	reifiedAllowed := d.Kind == ast.KindClass
	switch d.Kind {
	case ast.KindInterface:
		where = "interface " + cl.Name
	case ast.KindTrait:
		where = "trait " + cl.Name
	case ast.KindEnum:
		where = "enum " + cl.Name
	}
	if d.Kind == ast.KindInterface {
		where += " (no constructor carries it)"
	}
	ctx.checkTypeParams(d.TypeParams, reifiedAllowed, where)
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
	// A reified parent needs its argument from every subclass: either a
	// closed type (`extends Repository<User>`) or a reified parameter of
	// the subclass itself.
	if d.Kind == ast.KindClass && len(d.Extends) > 0 {
		if parent := cl.ParentClass(); parent != nil {
			parent.Signature()
			for i, tp := range parent.TypeParams {
				if !tp.Reified {
					continue
				}
				if i >= len(cl.ParentArgs) {
					x := d.Extends[0]
					c.report(x.S, P3005, "%s has a reified type parameter %s; extends %s<…> must bind it", parent.Name, tp.Name, parent.Name)
					continue
				}
				if !ctx.closedOrReified(cl.ParentArgs[i]) {
					c.report(d.Extends[0].TypeArgs[i].Span(), P3005, "the argument for reified %s of %s must be a class type or a reified parameter of %s", tp.Name, parent.Name, cl.Name)
				}
			}
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
	// Class parameters are instance-only: a static member cannot see one.
	for _, m := range cl.Methods {
		if m.Class != cl || !m.Static || m.Decl == nil {
			continue
		}
		for i, p := range m.Params {
			if i < len(m.Decl.Params) && m.Decl.Params[i].Type != nil && ctx.mentionsClassParam(p.NativeType) {
				c.report(m.Decl.Params[i].Type.Span(), P3008, "type parameter of class %s used in static method %s(): declare the method's own <T>", cl.Name, m.Name)
			}
		}
		if m.Decl.ReturnType != nil && ctx.mentionsClassParam(m.NativeReturn) {
			c.report(m.Decl.ReturnType.Span(), P3008, "type parameter of class %s used in static method %s(): declare the method's own <T>", cl.Name, m.Name)
		}
	}
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

// closedOrReified reports whether a type argument can be turned into a
// runtime value: a class type, or a reified parameter in scope.
func (x *fnCtx) closedOrReified(t types.Type) bool {
	switch t := t.(type) {
	case types.Class:
		return true
	case types.Param:
		tp := x.res.TypeParam(t.Name)
		return tp != nil && tp.Reified
	}
	return false
}

// checkSignatureTypes validates the written types of a function or
// method: type arguments, and its own type parameters.
func (x *fnCtx) checkSignatureTypes(tps []*ast.TypeParam, params []*ast.Param, ret ast.TypeExpr, reifiedAllowed bool, where string) {
	if x.file.Mode != lexer.ModePhaenon {
		return
	}
	x.checkTypeParams(tps, reifiedAllowed, where)
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
	x.checkTypeParams(a.TypeParams, false, "type alias "+a.Name.Name)
	x.checkTypeExpr(a.Type)
	x.res = saved
}

// ------------------------------------------------------------------ is / as

// testType resolves the right-hand side of `is` / `as`: the type, with
// every erased type parameter in it reported.
func (x *fnCtx) testType(t ast.TypeExpr, op string) types.Type {
	x.checkTypeExpr(t)
	target := x.res.Native(t)
	types.Walk(target, func(m types.Type) {
		p, ok := m.(types.Param)
		if !ok {
			return
		}
		if tp := x.res.TypeParam(types.ParamName(p.Name)); tp != nil {
			x.paramValue(tp, t.Span(), "`"+op+"`")
		}
	})
	return target
}

// isCond narrows on `$x is T`, exactly as instanceof does for classes.
func (x *fnCtx) isCond(e *ast.IsExpr, env *state) (then, els *state) {
	t := x.expr(e.Expr, env, nil)
	target := x.testType(e.Type, "is")
	x.condValueType = types.TBool
	then, els = env.clone(), env.clone()
	if !types.IsMixed(t) && !isParamType(t) && !isParamType(target) {
		if types.IsSubtype(t, target, x.p()) {
			x.report(e.S, P2306, "%s is always %s", x.typeName(t), x.typeName(target))
		} else if !types.Overlaps(t, target, x.p()) {
			x.report(e.S, P2306, "%s is never %s", x.typeName(t), x.typeName(target))
		}
	}
	x.narrowPath(then, e.Expr, types.Intersect(t, target, x.p()), true)
	x.narrowPath(els, e.Expr, x.subtract(t, target, x.p()), false)
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
	target := x.testType(e.Type, "as")
	result := target
	if !types.IsMixed(t) && !isParamType(t) && !isParamType(target) {
		if !types.Overlaps(t, target, x.p()) {
			x.report(e.S, P2306, "%s is never %s", x.typeName(t), x.typeName(target))
		} else {
			result = types.Intersect(t, target, x.p())
		}
	}
	if e.Nullable {
		return types.Nullable(result)
	}
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
func (x *fnCtx) closureTypeParams(tps []*ast.TypeParam) {
	if len(tps) == 0 {
		return
	}
	x.checkTypeParams(tps, false, "a closure")
	params := x.res.NativeParams(tps)
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
