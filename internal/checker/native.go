package checker

import (
	"strings"

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
// the expression's type is certain as inferred. A function, static method
// or static property PHP gives no type is taken at its docblock's word
// here, as the @var checks want it; guaranteedOf does not.
func (x *fnCtx) nativeOf(e ast.Expr, env *state) types.Type {
	return x.phpType(e, env, false)
}

// guaranteedOf is nativeOf for what PHP guarantees at run time: a call or
// a static property whose declaration has no PHP type guarantees nothing
// (mixed), whatever its docblock says, as a PHP caller or a wrong docblock
// can hand over anything. It decides which checks of a withdrawn `is` or
// `as` the operand's type settles in the PHP test phn fmt writes for it, and what a variable assigned the value holds.
func (x *fnCtx) guaranteedOf(e ast.Expr, env *state) types.Type {
	return x.phpType(e, env, true)
}

// guaranteedOr is guaranteedOf with the inferred type as the fallback.
func (x *fnCtx) guaranteedOr(e ast.Expr, t types.Type, env *state) types.Type {
	if n := x.guaranteedOf(e, env); n != nil {
		return n
	}
	return t
}

// phpType is nativeOf, or guaranteedOf with guaranteed set. A property or
// an offset a test narrowed has the PHP type the test left of it, as a
// variable has.
func (x *fnCtx) phpType(e ast.Expr, env *state, guaranteed bool) types.Type {
	if env != nil {
		if key, ok := x.narrowedPathKey(e); ok {
			if t, ok := env.getPath(nativePathKey(key)); ok {
				return t
			}
		}
	}
	switch e := e.(type) {
	case *ast.Paren:
		return x.phpType(e.Expr, env, guaranteed)
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
		if e.Args != nil && (e.Args.FirstClassCallable || e.Args.Partial()) {
			return nil // `strtolower(...)`, `str_repeat(?, 2)`: a Closure, as typed
		}
		fn := x.lookupFunction(n)
		if fn == nil {
			return nil
		}
		fn.Signature()
		if ownReturn := phpsOwnReturn(fn); ownReturn != nil {
			return ownReturn
		}
		return refinedNative(fn.NativeReturn, fn.Return, guaranteed)
	case *ast.StaticCall:
		name, ok := identName(e.Name)
		cl := x.namedClass(e.Class)
		if !ok || cl == nil {
			return nil
		}
		cl.Signature()
		if m := cl.FindMethod(name); m != nil {
			return refinedNative(m.NativeReturn, m.Return, guaranteed)
		}
	case *ast.StaticPropertyFetch:
		v, ok := e.Name.(*ast.Variable)
		cl := x.namedClass(e.Class)
		if !ok || cl == nil {
			return nil
		}
		cl.Signature()
		if pr := cl.FindProperty(v.Name); pr != nil {
			return refinedNative(pr.NativeType, pr.Type, guaranteed)
		}
	case *ast.Index:
		// An element of a refined array (`@param array{body: string}` over
		// a native `array`) is as uncertain as the array: PHP knows mixed,
		// but where PHP's type of the array has the entry, one a write
		// gave it (`$this->labels = ['a' => 'x']`), as PHPStan's native
		// type of the offset is the entry's.
		if n := x.phpType(e.Array, env, guaranteed); n != nil {
			key := types.Type(types.TArrayKey)
			if lit := x.literalOffset(e, env); lit != nil {
				key = lit
			}
			if v := elementPHP(n, key); v != nil {
				return v
			}
			return types.TMixed
		}
	case *ast.Ternary:
		// A ternary or a match whose result may be a value a docblock
		// refines is no certainty either: its PHP type is its branches',
		// the condition's where it is truthy for `?:`.
		if e.Then == nil {
			cond := x.phpType(e.Cond, env, guaranteed)
			els := x.phpType(e.Else, env, guaranteed)
			if cond == nil && els == nil {
				return nil
			}
			return types.MakeUnion(types.Truthy(x.phpOr(e.Cond, cond, env)), x.phpOr(e.Else, els, env))
		}
		return x.branchesPHP([]ast.Expr{e.Then, e.Else}, env, guaranteed)
	case *ast.Match:
		var bodies []ast.Expr
		for _, arm := range e.Arms {
			bodies = append(bodies, arm.Body)
		}
		return x.branchesPHP(bodies, env, guaranteed)
	case *ast.ArrayLit:
		// An array a value PHP's type does not certify is spread into is
		// an array of what PHP's types hold, empty unless an item is no
		// spread: `[...$xs]` of a documented non-empty list may be empty.
		uncertain, filled := false, false
		for _, item := range e.Items {
			if item == nil {
				continue
			}
			if !item.Unpack {
				filled = true
			} else if x.phpType(item.Value, env, guaranteed) != nil {
				uncertain = true
			}
		}
		if uncertain {
			return types.Array{Key: types.TArrayKey, Value: types.TMixed, NonEmpty: filled}
		}
	case *ast.Binary:
		if e.Op == token.T_COALESCE {
			// Either side refined makes the result uncertain. The sides are
			// not typed again here: typing has effects (a `?? throw` ends
			// the flow), and PHP's own type of the result is not needed.
			if x.phpType(e.Left, env, guaranteed) != nil || x.phpType(e.Right, env, guaranteed) != nil {
				return types.TMixed
			}
		}
	}
	return nil
}

// branchesPHP is the PHP type of a value that is one of the branches: nil
// where each is certain, the union of their PHP types, or of their types
// where certain, otherwise.
func (x *fnCtx) branchesPHP(branches []ast.Expr, env *state, guaranteed bool) types.Type {
	phps := make([]types.Type, len(branches))
	any := false
	for i, b := range branches {
		phps[i] = x.phpType(b, env, guaranteed)
		any = any || phps[i] != nil
	}
	if !any {
		return nil
	}
	var out []types.Type
	for i, b := range branches {
		out = append(out, x.phpOr(b, phps[i], env))
	}
	return types.MakeUnion(out...)
}

// phpOr is php, PHP's type of e, or e's type where it is certain (nil).
func (x *fnCtx) phpOr(e ast.Expr, php types.Type, env *state) types.Type {
	if php != nil {
		return php
	}
	return x.pathType(e, env)
}

// phpsOwnReturn is what PHP guarantees a call of one of its own functions
// returns, as PHPStan types the call natively with treatPhpDocTypesAsCertain
// off: its declared return type, nil for a function that is not PHP's own
// or declares none. What the checker infers beyond it from the arguments,
// as PHPStan's return type extensions do, is no type PHP guarantees:
// max(1, $m) is int<1, max> to both and mixed to PHP, and a comparison
// only that range decides is not reported. PHPStan's function map is
// native to it, and where the overlay documents the declared int as a
// range, the map's, the range is what PHP guarantees: count() and strlen()
// are int<0, max>, and `count($xs) < 0` is always false; so is a
// class-string over a declared string, get_class()'s. The overlay's other
// refinements, a list, bytes, a condition on the arguments, are the
// checker's reading, as a docblock's are.
func phpsOwnReturn(fn *program.Function) types.Type {
	if fn.File == nil || fn.NativeReturn == nil {
		return nil
	}
	path := fn.File.Path
	if !strings.HasPrefix(path, "<stub>/php-src/") && !strings.HasPrefix(path, "<stub>/overlay/") && !strings.HasPrefix(path, "<ext>/") {
		return nil
	}
	if fn.Return != nil && rangesOf(fn.Return, fn.NativeReturn) {
		return fn.Return
	}
	return fn.NativeReturn
}

// rangesOf reports a documented type that is the declared one but for its
// int, which it narrows to ranges, and its string, which it narrows to a
// class-string, as PHPStan's function map has them natively: int<0, max>
// over int, int<0, max>|float over int|float, class-string over string.
func rangesOf(documented, declared types.Type) bool {
	widened := types.Map(documented, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Int:
			return types.TInt
		case types.ClassString:
			if m.Of == nil {
				return types.TString
			}
		}
		return m
	})
	return types.Equal(widened, declared)
}

// refinedNative is what PHP holds a declaration to, native erased, when
// its declared type refines it; nil when the two are the same. Where PHP
// declares no type, the docblock is all there is: taken as the
// declaration's type (nil), or, guaranteed, as no guarantee at all (mixed).
func refinedNative(native, refined types.Type, guaranteed bool) types.Type {
	if native == nil {
		if guaranteed && refined != nil && !types.IsMixed(refined) {
			return types.TMixed
		}
		return nil
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
