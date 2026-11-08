package checker

import (
	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// The PHP type behind a refined one.
//
// A docblock, an inherited generic or a `@var` tag refines what PHP itself
// declares: `normalize(object $value)` in a subclass of a generic normaliser
// is typed `UnitEnum`, `$data = $collection->findOne()` under a `@var
// array<string, mixed>` is typed as that array. The refined type drives
// inference; the PHP type stays what the runtime guarantees. A diagnostic
// that calls a check impossible — an instanceof always true, a `=== null`
// always false — therefore requires both types to agree, as PHPStan does
// with treatPhpDocTypesAsCertain off, Tequila's setting everywhere
// (design/checker.md §9). A defensive check against what a PHP caller may
// pass through an erased signature is not dead code.

// nativeOf returns the PHP-declared type behind an expression — a variable
// a docblock or generic refined, a property, a method result — or nil when
// the expression's type is certain as inferred.
func (x *fnCtx) nativeOf(e ast.Expr, env *state) types.Type {
	switch e := e.(type) {
	case *ast.Paren:
		return x.nativeOf(e.Expr, env)
	case *ast.Variable:
		if v, ok := env.get(e.Name); ok {
			return v.native
		}
	case *ast.PropertyFetch:
		name, ok := identName(e.Name)
		if !ok {
			return nil
		}
		return x.nativeMember(e.Object, e.NullSafe, env, func(cl *program.Class) (native, refined types.Type, decl *program.Class) {
			prs, _, _ := x.classProperties(cl, name)
			if len(prs) == 0 {
				return nil, nil, nil
			}
			// In a trait body, every user's declaration: PHP guarantees
			// one of their native types, whichever class runs the body.
			var natives, refineds []types.Type
			for _, pr := range prs {
				n := pr.NativeType
				if n == nil {
					n = types.TMixed
				}
				natives = append(natives, n)
				refineds = append(refineds, pr.Type)
			}
			return types.MakeUnion(natives...), types.MakeUnion(refineds...), prs[0].Class
		})
	case *ast.MethodCall:
		name, ok := identName(e.Name)
		if !ok {
			return nil
		}
		return x.nativeMember(e.Object, e.NullSafe, env, func(cl *program.Class) (native, refined types.Type, decl *program.Class) {
			if m := cl.FindMethod(name); m != nil {
				return m.NativeReturn, m.Return, m.Class
			}
			return nil, nil, nil
		})
	case *ast.Call:
		// A function the overlay or a docblock refines: array_values()
		// returns a list<V> to the checker and an array to PHP.
		n, ok := e.Callee.(*ast.Name)
		if !ok {
			return nil
		}
		fn := x.lookupFunction(n)
		if fn == nil {
			return nil
		}
		fn.Signature()
		return refinedNative(fn.NativeReturn, fn.Return)
	case *ast.StaticCall:
		name, ok := identName(e.Name)
		cl := x.namedClass(e.Class)
		if !ok || cl == nil {
			return nil
		}
		cl.Signature()
		if m := cl.FindMethod(name); m != nil {
			return refinedNative(m.NativeReturn, m.Return)
		}
	case *ast.StaticPropertyFetch:
		v, ok := e.Name.(*ast.Variable)
		cl := x.namedClass(e.Class)
		if !ok || cl == nil {
			return nil
		}
		cl.Signature()
		if pr := cl.FindProperty(v.Name); pr != nil {
			return refinedNative(pr.NativeType, pr.Type)
		}
	case *ast.Index:
		// An element of a refined array (`@param array{body: string}` over
		// a native `array`) is as uncertain as the array: PHP knows mixed.
		if n := x.nativeOf(e.Array, env); n != nil {
			return types.TMixed
		}
	case *ast.Binary:
		if e.Op == token.T_COALESCE {
			// Either side refined makes the result uncertain. The sides are
			// not typed again here: typing has effects (a `?? throw` ends
			// the flow), and PHP's own type of the result is not needed.
			if x.nativeOf(e.Left, env) != nil || x.nativeOf(e.Right, env) != nil {
				return types.TMixed
			}
		}
	}
	return nil
}

// refinedNative is what PHP holds a declaration to, native erased, when
// its declared type refines it; nil when the two are the same.
func refinedNative(native, refined types.Type) types.Type {
	if native == nil {
		native = types.TMixed
	}
	native = types.Erase(native)
	if refined == nil || types.Equal(native, refined) {
		return nil
	}
	return native
}

// namedClass is the class a static access names (`Foo::`, `self::`,
// `parent::`); nil for an expression or an unknown class, which the
// access itself reports.
func (x *fnCtx) namedClass(e ast.Expr) *program.Class {
	n, ok := e.(*ast.Name)
	if !ok {
		return nil
	}
	switch specialClassName(n) {
	case "self", "static":
		return x.class
	case "parent":
		if x.class != nil {
			return x.class.ParentClass()
		}
		return nil
	}
	return x.p().Class(x.res.ClassName(n))
}

// nativeOr is nativeOf with the inferred type as the fallback.
func (x *fnCtx) nativeOr(e ast.Expr, t types.Type, env *state) types.Type {
	if n := x.nativeOf(e, env); n != nil {
		return n
	}
	return t
}

// nativeOverlap reports whether two operands may still be identical once
// their docblock refinements are set aside; false when neither operand is
// refined, so the inferred types alone decide.
func (x *fnCtx) nativeOverlap(l ast.Expr, lt types.Type, r ast.Expr, rt types.Type, env *state) bool {
	ln, rn := x.nativeOf(l, env), x.nativeOf(r, env)
	if ln == nil && rn == nil {
		return false
	}
	if ln == nil {
		ln = lt
	}
	if rn == nil {
		rn = rt
	}
	return types.IsMixed(ln) || types.IsMixed(rn) || types.Overlaps(ln, rn, x.p())
}

// nativeMember is the native type of a member over every class of the
// receiver; nil when each class declares in PHP exactly what it infers.
func (x *fnCtx) nativeMember(obj ast.Expr, nullSafe bool, env *state, member func(*program.Class) (native, refined types.Type, decl *program.Class)) types.Type {
	recv := x.pathType(obj, env)
	var out []types.Type
	differs := false
	for _, m := range types.Members(types.StripNull(recv)) {
		var name string
		switch c := m.(type) {
		case types.Class:
			name = c.Name
		case types.Static:
			if c.Of != nil {
				name = c.Of.Name
			}
		case types.This:
			if c.Of != nil {
				name = c.Of.Name
			}
		}
		if name == "" {
			return nil
		}
		cl := x.p().Class(name)
		if cl == nil {
			return nil
		}
		cl.Signature()
		native, refined, decl := member(cl)
		if decl == nil {
			return nil
		}
		if native == nil {
			native = types.TMixed
		}
		native = types.Erase(x.bindReceiver(native, m, decl))
		if refined != nil && !types.Equal(native, x.bindReceiver(refined, m, decl)) {
			differs = true
		}
		out = append(out, native)
	}
	if !differs {
		return nil
	}
	if nullSafe || types.IsNullable(recv) {
		out = append(out, types.TNull)
	}
	return types.MakeUnion(out...)
}
