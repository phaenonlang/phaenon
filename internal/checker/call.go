package checker

import (
	"fmt"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

type docblockAssert = docblock.AssertTag

// lookupFunction resolves a function name through the namespace fallback.
func (x *fnCtx) lookupFunction(n *ast.Name) *program.Function {
	ns, global := x.res.Scope().ResolveFunction(n)
	if fn := x.p().Function(ns); fn != nil {
		return fn
	}
	return x.p().Function(global)
}

// signature is a callable's parameters and return type as the call checker
// sees them, whatever declared it.
type signature struct {
	name       string
	params     []*program.Param
	ret        types.Type
	typeParams []*program.TypeParam
	explicit   []types.Type // type arguments written at the call site
	subst      map[string]types.Type
	recv       types.Type     // receiver, for static/$this
	decl       *program.Class // declaring class of a method
	never      bool
	deprecated bool
	// known holds argument types computed before the call is checked (the
	// receiver of a stdlib method call), so the expression is not typed twice.
	known map[ast.Expr]types.Type
}

// ------------------------------------------------------------------ function calls

func (x *fnCtx) call(e *ast.Call, env *state, expected types.Type) types.Type {
	n, isName := e.Callee.(*ast.Name)
	if !isName {
		return x.dynamicCall(e, env, expected)
	}
	lower := strings.ToLower(n.Parts[len(n.Parts)-1])
	if len(n.Parts) == 1 {
		switch lower {
		case "compact":
			x.removed(e.S, "compact()", "an explicit array literal")
		case "extract":
			x.removed(e.S, "extract()", "explicit assignments")
		case "get_defined_vars":
			x.removed(e.S, "get_defined_vars()", "explicit variables")
		}
	}
	fn := x.lookupFunction(n)
	if fn == nil {
		_, global := x.res.Scope().ResolveFunction(n)
		x.report(n.S, P2002, "unknown function %s()", global)
		x.argsOnly(e.Args, env)
		return types.TError
	}
	fn.Signature()
	if fn.Deprecated && x.c.opts.Deprecations {
		x.report(n.S, P4006, "function %s() is deprecated", fn.Name)
	}
	sig := &signature{name: fn.Name + "()", params: fn.Params, ret: fn.Return, typeParams: fn.TypeParams}
	sig.explicit = x.explicitArgs(e.TypeArgs, fn.TypeParams, fn.Name+"()", e.S)
	if e.Args != nil && e.Args.FirstClassCallable {
		return x.callableOf(sig)
	}
	ret := x.checkCall(sig, e.Args, env, e.S, expected)
	if _, isNever := ret.(types.Never); isNever {
		env.dead = true
	}
	if lower == "array_push" || lower == "array_unshift" {
		x.arrayPush(e, env)
	}
	return x.specialReturn(lower, e, ret, env)
}

// arrayPush retypes the by-reference array of array_push / array_unshift
// with the pushed values appended (keys are lost on unshift, so both
// become lists only when they were lists).
func (x *fnCtx) arrayPush(e *ast.Call, env *state) {
	if e.Args == nil || len(e.Args.Args) < 2 {
		return
	}
	target := e.Args.Args[0].Value
	cur := x.pathType(target, env)
	var values []types.Type
	for _, a := range e.Args.Args[1:] {
		if a.Value == nil {
			continue
		}
		t := x.pathType(a.Value, env)
		if a.Unpack {
			_, v, _ := types.IterableKV(t, x.p())
			values = append(values, v)
		} else {
			values = append(values, t)
		}
	}
	if len(values) == 0 {
		return
	}
	pushed := types.MakeUnion(values...)
	result := types.Map(cur, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Shape:
			vals := pushed
			if len(m.Entries) > 0 {
				vals = types.MakeUnion(shapeValues(m), pushed)
			}
			if len(m.Entries) == 0 || m.List {
				return types.Array{Key: types.TInt, Value: vals, List: true, NonEmpty: true}
			}
			return types.Array{Key: types.MakeUnion(shapeKeys(m), types.TInt), Value: vals, NonEmpty: true}
		case types.Array:
			return types.Array{Key: types.MakeUnion(m.Key, types.TInt), Value: types.MakeUnion(m.Value, pushed), List: m.List, NonEmpty: true}
		case types.Null:
			return types.Array{Key: types.TInt, Value: pushed, List: true, NonEmpty: true}
		}
		return m
	})
	x.assignTo(target, result, env, e.S)
}

// specialReturn refines the return type of a few core functions whose
// result depends on the arguments in ways a signature cannot express.
func (x *fnCtx) specialReturn(name string, e *ast.Call, ret types.Type, env *state) types.Type {
	args := e.Args
	if args == nil {
		return ret
	}
	argType := func(i int) types.Type {
		if i < len(args.Args) && args.Args[i].Value != nil {
			return x.pathType(args.Args[i].Value, env)
		}
		return types.TMixed
	}
	switch name {
	case "get_class":
		if len(args.Args) == 1 {
			return types.ClassString{Of: types.StripNull(argType(0))}
		}
	case "array_map":
		// Keys are preserved for a single array; a list stays a list.
		if len(args.Args) == 2 {
			if a, ok := argType(1).(types.Array); ok && a.List {
				if r, ok := ret.(types.Array); ok {
					return types.Array{Key: types.TInt, Value: r.Value, List: true, NonEmpty: a.NonEmpty}
				}
			}
			if _, ok := argType(1).(types.Shape); ok {
				if r, ok := ret.(types.Array); ok {
					return types.Array{Key: r.Key, Value: r.Value, List: true}
				}
			}
		}
	case "array_filter":
		if a, ok := argType(0).(types.Array); ok {
			if r, ok := ret.(types.Array); ok {
				return types.Array{Key: r.Key, Value: r.Value, List: false, NonEmpty: false}
			}
			_ = a
		}
	case "array_values":
		if r, ok := ret.(types.Array); ok {
			if a, ok := argType(0).(types.Array); ok && a.NonEmpty {
				r.NonEmpty = true
			}
			return r
		}
	case "array_keys":
		if sh, ok := argType(0).(types.Shape); ok {
			return types.Array{Key: types.TInt, Value: shapeKeys(sh), List: true, NonEmpty: len(sh.Entries) > 0}
		}
	case "array_merge":
		var out []types.Type
		allShapes := true
		for i := range args.Args {
			t := argType(i)
			if _, ok := t.(types.Shape); !ok {
				allShapes = false
			}
			out = append(out, t)
		}
		if allShapes && len(out) > 0 {
			merged := types.Shape{}
			for _, t := range out {
				for _, en := range t.(types.Shape).Entries {
					if en.IsInt {
						merged = arrayWithEntry(merged, nil, en.Type).(types.Shape)
					} else {
						merged.Entries = upsert(merged.Entries, en)
					}
				}
			}
			return merged
		}
	case "sprintf", "vsprintf", "implode", "join", "str_repeat", "strtolower", "strtoupper", "ucfirst", "trim", "ltrim", "rtrim", "substr":
		return ret
	case "iterator_to_array":
		preserve := len(args.Args) < 2 || isTrueLit(args.Args[1].Value)
		k, v, _ := types.IterableKV(argType(0), x.p())
		if preserve {
			return types.Array{Key: keyOrArrayKey(k), Value: v}
		}
		return types.Array{Key: types.TInt, Value: v, List: true}
	case "func_get_args":
		return types.Array{Key: types.TInt, Value: types.TMixed, List: true}
	}
	return ret
}

func keyOrArrayKey(k types.Type) types.Type {
	if types.IsSubtype(k, types.TArrayKey, nil) {
		return k
	}
	return types.TArrayKey
}

// dynamicCall types $f(...), (expr)(...), [$o, 'm'](...).
func (x *fnCtx) dynamicCall(e *ast.Call, env *state, expected types.Type) types.Type {
	ct := x.expr(e.Callee, env, nil)
	if e.Args != nil && e.Args.FirstClassCallable {
		return ct
	}
	if types.IsMixed(ct) {
		x.argsOnly(e.Args, env)
		return types.TMixed
	}
	var out []types.Type
	for _, m := range types.Members(ct) {
		switch m := m.(type) {
		case types.Callable:
			if m.HasSig {
				out = append(out, x.checkCall(x.sigOfCallable(m), e.Args, env, e.S, expected))
			} else {
				x.argsOnly(e.Args, env)
				out = append(out, types.TMixed)
			}
		case types.Class, types.Static, types.This, types.Intersection:
			handled := false
			for _, cl := range x.classesOf(m, e.Callee.Span()) {
				if inv := cl.FindMethod("__invoke"); inv != nil {
					sig := &signature{name: cl.Name + "::__invoke()", params: inv.Params, ret: inv.Return, typeParams: inv.TypeParams, recv: m, decl: inv.Class}
					out = append(out, x.checkCall(sig, e.Args, env, e.S, expected))
					handled = true
				} else if strings.EqualFold(cl.Name, "Closure") {
					x.argsOnly(e.Args, env)
					out = append(out, types.TMixed)
					handled = true
				}
			}
			if !handled {
				x.report(e.Callee.Span(), P2109, "%s is not callable", x.typeName(m))
				x.argsOnly(e.Args, env)
				out = append(out, types.TMixed)
			}
		case types.String, types.StringLit, types.ClassString, types.Array, types.Shape, types.Object, types.Unknown, types.Param:
			x.argsOnly(e.Args, env)
			out = append(out, types.TMixed)
		default:
			x.report(e.Callee.Span(), P2109, "%s is not callable", x.typeName(m))
			x.argsOnly(e.Args, env)
			out = append(out, types.TMixed)
		}
	}
	return types.MakeUnion(out...)
}

// sigOfCallable turns a callable type into a signature.
func (x *fnCtx) sigOfCallable(c types.Callable) *signature {
	sig := &signature{name: "callable", ret: c.Return}
	for i, p := range c.Params {
		name := p.Name
		if name == "" {
			name = fmt.Sprintf("%d", i+1)
		}
		sig.params = append(sig.params, &program.Param{Name: name, Type: p.Type, HasDefault: p.Optional, Variadic: p.Variadic, ByRef: p.ByRef})
	}
	return sig
}

// callableOf builds the Closure type of f(...).
func (x *fnCtx) callableOf(sig *signature) types.Type {
	// A generic function's first-class callable stays generic, with
	// fresh parameter identities that call-site inference resolves later.
	rename, _ := x.freshParams(sig.typeParams)
	c := types.Callable{Closure: true, HasSig: true, Return: sig.ret}
	if c.Return == nil {
		c.Return = types.TMixed
	}
	for _, p := range sig.params {
		t := types.Substitute(types.Substitute(p.Type, rename), sig.subst)
		if sig.recv != nil {
			t = replaceStatic(t, sig.recv)
		}
		c.Params = append(c.Params, types.CallableParam{Name: p.Name, Type: t, Optional: p.HasDefault, Variadic: p.Variadic, ByRef: p.ByRef})
	}
	c.Return = types.Substitute(types.Substitute(c.Return, rename), sig.subst)
	if sig.recv != nil {
		c.Return = replaceStatic(c.Return, sig.recv)
	}
	return c
}

// argsOnly types the arguments of a call that cannot be checked further.
func (x *fnCtx) argsOnly(args *ast.ArgList, env *state) {
	if args == nil {
		return
	}
	for _, a := range args.Args {
		if a.Value != nil {
			x.expr(a.Value, env, nil)
		}
	}
}

// ------------------------------------------------------------------ method calls

func (x *fnCtx) methodCall(e *ast.MethodCall, env *state, expected types.Type) types.Type {
	obj := x.expr(e.Object, env, nil)
	if !e.NullSafe && types.IsNullable(obj) && !types.IsMixed(obj) {
		if x.shortCircuit[e.Object] {
			// `$a?->b()->c()`: PHP skips the whole chain when $a is null.
			t := x.methodCallOn(e, types.StripNull(obj), env, expected)
			x.markShortCircuit(e, t)
			return types.Nullable(t)
		}
		if inNullsafeChain(e.Object) {
			return types.Nullable(x.methodCallOn(e, obj, env, expected))
		}
	}
	return x.methodCallOn(e, obj, env, expected)
}

// methodCallOn checks the call on a receiver already typed.
func (x *fnCtx) methodCallOn(e *ast.MethodCall, obj types.Type, env *state, expected types.Type) types.Type {
	name, ok := identName(e.Name)
	if !ok {
		x.expr(e.Name, env, nil)
		x.argsOnly(e.Args, env)
		return types.TMixed
	}
	if e.NullSafe {
		if !types.IsNullable(obj) && !types.IsMixed(obj) {
			x.report(e.S, P2307, "?-> on %s, which is never null", x.typeName(obj))
		}
	} else if types.IsNullable(obj) && !types.IsMixed(obj) {
		if _, isNull := obj.(types.Null); isNull {
			x.report(e.Object.Span(), P2105, "cannot call method %s() on null", name)
			x.argsOnly(e.Args, env)
			return types.TMixed
		}
		x.report(e.Object.Span(), P2105, "cannot call method %s() on %s (possibly null)", name, x.typeName(obj))
	}
	base := types.StripNull(obj)
	if types.IsMixed(base) {
		x.argsOnly(e.Args, env)
		return types.TMixed
	}
	if cls, ambiguous := stdlibReceiver(base); cls != "" || ambiguous {
		return x.stdlibMethod(e, cls, name, obj, env, expected)
	}
	if !types.IsObjectLike(base) {
		if c, isCallable := base.(types.Callable); isCallable && c.Closure {
			// Closure methods: call, bindTo, …
			base = types.NewClass("Closure")
		} else {
			x.report(e.Object.Span(), P2105, "cannot call method %s() on %s", name, x.typeName(base))
			x.argsOnly(e.Args, env)
			return types.TMixed
		}
	}
	var out []types.Type
	for _, m := range types.Members(base) {
		if _, isObj := m.(types.Object); isObj {
			x.argsOnly(e.Args, env)
			out = append(out, types.TMixed)
			continue
		}
		classes := x.classesOf(m, e.Object.Span())
		if len(classes) == 0 {
			x.argsOnly(e.Args, env)
			out = append(out, types.TMixed)
			continue
		}
		for _, cl := range classes {
			meth := cl.FindMethod(name)
			if meth == nil && cl.Kind == ast.KindTrait {
				meth = x.traitUserMethod(cl, name)
			}
			if meth == nil {
				if cl.FindMethod("__call") != nil {
					x.argsOnly(e.Args, env)
					out = append(out, types.TMixed)
					continue
				}
				x.report(e.Name.Span(), P2006, "unknown method %s::%s()", cl.Name, name)
				x.argsOnly(e.Args, env)
				out = append(out, types.TError)
				continue
			}
			if !x.canAccess(meth.Vis, meth.Class) {
				x.report(e.Name.Span(), P2108, "method %s::%s() is %s", meth.Class.Name, name, meth.Vis)
			}
			if meth.Deprecated && x.c.opts.Deprecations {
				x.report(e.Name.Span(), P4006, "method %s::%s() is deprecated", meth.Class.Name, name)
			}
			sig := &signature{name: meth.Class.Name + "::" + meth.Name + "()", params: meth.Params, ret: meth.Return,
				typeParams: meth.TypeParams, recv: m, decl: meth.Class}
			sig.explicit = x.explicitArgs(e.TypeArgs, meth.TypeParams, sig.name, e.S)
			if e.Args != nil && e.Args.FirstClassCallable {
				sig.subst = x.memberSubst(m, meth.Class)
				out = append(out, x.callableOf(sig))
				continue
			}
			ret := x.checkCall(sig, e.Args, env, e.S, expected)
			if meth.SelfOut != nil {
				x.narrowPath(env, e.Object, x.bindReceiver(meth.SelfOut, m, meth.Class), false)
			}
			out = append(out, ret)
		}
	}
	t := types.MakeUnion(out...)
	if _, isNever := t.(types.Never); isNever {
		env.dead = true
	}
	if e.NullSafe {
		x.markShortCircuit(e, t) // the result's own type, before ?-> adds its null
		if types.IsNullable(obj) {
			t = types.Nullable(t)
		}
	}
	return t
}

func (x *fnCtx) staticCall(e *ast.StaticCall, env *state, expected types.Type) types.Type {
	name, ok := identName(e.Name)
	if !ok {
		x.expr(e.Name, env, nil)
		x.argsOnly(e.Args, env)
		return types.TMixed
	}
	var recv types.Type
	var classes []*program.Class
	viaParent, viaSelf := false, false
	if n, isName := e.Class.(*ast.Name); isName {
		raw := x.res.ClassName(n)
		switch raw {
		case "parent":
			if x.class == nil {
				x.report(n.S, P2012, "parent used outside a class")
				x.argsOnly(e.Args, env)
				return types.TMixed
			}
			pc := x.class.ParentClass()
			if pc == nil {
				x.report(n.S, P2011, "class %s has no parent", x.class.Name)
				x.argsOnly(e.Args, env)
				return types.TMixed
			}
			classes = []*program.Class{pc}
			viaParent = true
			if x.static {
				recv = types.Static{Of: &types.Class{Name: x.class.Name, Args: selfArgs(x.class)}}
			} else {
				recv = x.thisType
			}
		case "self", "static":
			if x.class == nil {
				x.report(n.S, P2012, "%s used outside a class", raw)
				x.argsOnly(e.Args, env)
				return types.TMixed
			}
			classes = []*program.Class{x.class}
			viaSelf = true
			if raw == "static" || !x.static {
				if x.static {
					recv = types.Static{Of: &types.Class{Name: x.class.Name, Args: selfArgs(x.class)}}
				} else {
					recv = x.thisType
				}
			} else {
				recv = types.Class{Name: x.class.Name, Args: selfArgs(x.class)}
			}
		default:
			t, cl := x.classRefType(n, n.S)
			recv = t
			if cl != nil {
				classes = []*program.Class{cl}
			}
		}
	} else {
		ct := x.expr(e.Class, env, nil)
		// $obj::method() and $classString::method()
		for _, m := range types.Members(ct) {
			switch m := m.(type) {
			case types.ClassString:
				if m.Of != nil {
					recv = m.Of
					classes = append(classes, x.classesOf(m.Of, e.Class.Span())...)
				}
			case types.StringLit:
				recv = types.Class{Name: m.V}
				classes = append(classes, x.classesOf(recv, e.Class.Span())...)
			case types.Class, types.Static, types.This, types.Intersection:
				recv = m
				classes = append(classes, x.classesOf(m, e.Class.Span())...)
			}
		}
	}
	if len(classes) == 0 {
		x.argsOnly(e.Args, env)
		return types.TMixed
	}
	var out []types.Type
	for _, cl := range classes {
		meth := cl.FindMethod(name)
		if meth == nil && cl.Kind == ast.KindTrait {
			meth = x.traitUserMethod(cl, name)
		}
		if meth == nil {
			if cl.FindMethod("__callStatic") != nil || (!x.static && cl.FindMethod("__call") != nil && (viaParent || viaSelf)) {
				x.argsOnly(e.Args, env)
				out = append(out, types.TMixed)
				continue
			}
			x.report(e.Name.Span(), P2006, "unknown method %s::%s()", cl.Name, name)
			x.argsOnly(e.Args, env)
			out = append(out, types.TError)
			continue
		}
		if !meth.Static {
			// Allowed for parent::/self:: from an instance context of a related class.
			related := x.class != nil && !x.static && (x.class.IsSubclassOf(cl.Name) || cl.IsSubclassOf(x.class.Name))
			if !related {
				x.report(e.Name.Span(), P2106, "non-static method %s::%s() called statically", meth.Class.Name, name)
			}
		}
		if meth.Abstract && (viaParent || viaSelf) && !meth.Static {
			x.report(e.Name.Span(), P2106, "cannot call abstract method %s::%s()", meth.Class.Name, name)
		}
		if !x.canAccess(meth.Vis, meth.Class) {
			x.report(e.Name.Span(), P2108, "method %s::%s() is %s", meth.Class.Name, name, meth.Vis)
		}
		if meth.Deprecated && x.c.opts.Deprecations {
			x.report(e.Name.Span(), P4006, "method %s::%s() is deprecated", meth.Class.Name, name)
		}
		sig := &signature{name: meth.Class.Name + "::" + meth.Name + "()", params: meth.Params, ret: meth.Return,
			typeParams: meth.TypeParams, recv: recv, decl: meth.Class}
		sig.explicit = x.explicitArgs(e.TypeArgs, meth.TypeParams, sig.name, e.S)
		if e.Args != nil && e.Args.FirstClassCallable {
			sig.subst = x.memberSubst(recv, meth.Class)
			out = append(out, x.callableOf(sig))
			continue
		}
		out = append(out, x.checkCall(sig, e.Args, env, e.S, expected))
	}
	t := types.MakeUnion(out...)
	if _, isNever := t.(types.Never); isNever {
		env.dead = true
	}
	return t
}

// traitUserMethod finds a method a trait body calls on $this/self/static
// among the classes using the trait, as PHPStan does by analysing a trait
// in the context of each user. The first user that has it decides.
func (x *fnCtx) traitUserMethod(trait *program.Class, name string) *program.Method {
	for _, u := range x.p().TraitUsers(trait.Name) {
		u.Signature()
		if m := u.FindMethod(name); m != nil {
			return m
		}
	}
	return nil
}

// ------------------------------------------------------------------ new

func (x *fnCtx) newExpr(e *ast.New, env *state) types.Type {
	if e.Anon != nil {
		cl := x.p().DeclareAnonymous(x.file, x.res.Scope(), e.Anon)
		x.c.checkClass(cl)
		args := e.Args
		if args == nil {
			args = e.Anon.Args // `new class (...) extends …`
		}
		if ctor := cl.FindMethod("__construct"); ctor != nil {
			sig := &signature{name: "class@anonymous::__construct()", params: ctor.Params, ret: types.TVoid, decl: ctor.Class}
			x.checkCall(sig, args, env, e.S, nil)
		} else {
			x.argsOnly(args, env)
		}
		return types.Class{Name: cl.Name}
	}
	var cl *program.Class
	var recv types.Type
	// viaString: the class comes from a class<T> value, whose runtime class
	// is any subclass of T, so T itself being abstract says nothing.
	viaString := false
	switch c := e.Class.(type) {
	case *ast.Name:
		raw := x.res.ClassName(c)
		if raw == "parent" && x.class != nil {
			cl = x.class.ParentClass()
			if cl == nil {
				x.report(c.S, P2011, "class %s has no parent", x.class.Name)
				x.argsOnly(e.Args, env)
				return types.TObject
			}
			recv = types.Class{Name: cl.Name, Args: x.class.ParentArgs}
		} else {
			t, found := x.classRefType(c, c.S)
			recv = t
			cl = found
		}
	default:
		ct := x.expr(e.Class, env, nil)
		var out []types.Type
		for _, m := range types.Members(ct) {
			switch m := m.(type) {
			case types.ClassString:
				viaString = true
				if m.Of != nil {
					out = append(out, m.Of)
				} else {
					out = append(out, types.TObject)
				}
			case types.StringLit:
				viaString = true
				out = append(out, types.Class{Name: m.V})
			case types.Class, types.Static, types.This:
				out = append(out, m)
			default:
				out = append(out, types.TObject)
			}
		}
		recv = types.MakeUnion(out...)
		classes := x.classesOf(recv, e.Class.Span())
		if len(classes) == 1 {
			cl = classes[0]
		}
	}
	if cl == nil {
		x.argsOnly(e.Args, env)
		if recv == nil {
			return types.TObject
		}
		return recv
	}
	_, viaParam := recv.(types.Param)
	if !cl.IsInstantiable() && !viaParam && !viaString {
		what := "abstract class"
		switch {
		case cl.Kind == ast.KindInterface:
			what = "interface"
		case cl.Kind == ast.KindTrait:
			what = "trait"
		case cl.Kind == ast.KindEnum:
			what = "enum"
		}
		if _, isStatic := recv.(types.Static); !isStatic {
			x.report(e.S, P2107, "cannot instantiate %s %s", what, cl.Name)
		}
	}
	ctor := cl.FindMethod("__construct")
	result := recv
	// Explicit arguments close the class type: new Foo<User>(…).
	if c, ok := recv.(types.Class); ok && len(c.Args) == 0 && len(e.TypeArgs) > 0 {
		if args := x.explicitArgs(e.TypeArgs, cl.TypeParams, cl.Name, e.S); args != nil {
			full := make([]types.Type, len(cl.TypeParams))
			for i, tp := range cl.TypeParams {
				switch {
				case i < len(args):
					full[i] = args[i]
				case tp.Default != nil:
					full[i] = tp.Default
				case tp.Bound != nil:
					full[i] = tp.Bound
				default:
					full[i] = types.TMixed
				}
			}
			recv = types.Class{Name: c.Name, Args: full}
			result = recv
		}
	}
	if ctor == nil {
		if e.Args != nil && len(e.Args.Args) > 0 && !(viaString && !cl.IsInstantiable()) {
			x.report(e.Args.S, P2102, "class %s has no constructor but %d arguments are given", cl.Name, len(e.Args.Args))
		}
		x.argsOnly(e.Args, env)
		return result
	}
	if !x.canAccess(ctor.Vis, ctor.Class) {
		x.report(e.S, P2108, "constructor of %s is %s", cl.Name, ctor.Vis)
	}
	sig := &signature{name: cl.Name + "::__construct()", params: ctor.Params, ret: types.TVoid, typeParams: ctor.TypeParams, recv: recv, decl: ctor.Class}
	// Infer the class's own type arguments from the constructor call.
	if c, ok := recv.(types.Class); ok && len(c.Args) == 0 && len(cl.TypeParams) > 0 {
		sig.typeParams = append(append([]*program.TypeParam{}, cl.TypeParams...), ctor.TypeParams...)
		sig.subst = map[string]types.Type{}
		x.checkCall(sig, e.Args, env, e.S, nil)
		args := make([]types.Type, len(cl.TypeParams))
		for i, tp := range cl.TypeParams {
			if t, ok := sig.subst[tp.Name]; ok {
				args[i] = t
			} else if tp.Default != nil {
				args[i] = tp.Default
			} else if tp.Bound != nil {
				args[i] = tp.Bound
			} else {
				args[i] = types.TMixed
			}
		}
		return types.Class{Name: c.Name, Args: args}
	}
	x.checkCall(sig, e.Args, env, e.S, nil)
	return result
}

// ------------------------------------------------------------------ argument checking

// checkCall matches arguments to parameters, infers template arguments,
// checks types and returns the (substituted) return type.
func (x *fnCtx) checkCall(sig *signature, args *ast.ArgList, env *state, span ast.Span, expected types.Type) types.Type {
	subst := sig.subst
	if subst == nil {
		subst = map[string]types.Type{}
		if sig.decl != nil && sig.recv != nil {
			for k, v := range x.memberSubst(sig.recv, sig.decl) {
				subst[k] = v
			}
		}
	}
	// The callee's own type parameters get fresh identities for this call,
	// so its T is never confused with a T of the caller.
	rename, tps := x.freshParams(sig.typeParams)
	inferable := map[string]bool{}
	for _, tp := range tps {
		inferable[tp.Name] = true
	}
	for _, tp := range sig.typeParams {
		delete(subst, tp.Name)
	}
	for i, a := range sig.explicit {
		if i < len(tps) {
			subst[tps[i].Name] = a
			delete(inferable, tps[i].Name)
		}
	}
	paramType := func(p *program.Param) types.Type {
		t := types.Substitute(types.Substitute(p.Type, rename), subst)
		if sig.recv != nil {
			t = replaceStatic(t, sig.recv)
		}
		return t
	}
	// Type parameters in the signature that belong to neither the callee
	// nor the current scope (a constructor's templates kept on a property,
	// an interface's templates on an unparameterised receiver) are inferred
	// here too, as PHPStan does.
	collectForeign := func(t types.Type) {
		types.Walk(t, func(m types.Type) {
			if p, ok := m.(types.Param); ok && !strings.Contains(p.Name, "\x00") && !x.inScope[p.Name] && !inferable[p.Name] {
				inferable[p.Name] = true
				tps = append(tps, &program.TypeParam{Name: p.Name, Bound: p.Bound})
				delete(subst, p.Name)
			}
		})
	}
	for _, p := range sig.params {
		collectForeign(paramType(p))
	}
	if sig.ret != nil {
		collectForeign(types.Substitute(types.Substitute(sig.ret, rename), subst))
	}
	// Map arguments to parameters.
	type bound struct {
		arg   *ast.Arg
		param *program.Param
		idx   int
	}
	var bindings []bound
	seenNamed := false
	unpacked := false
	placeholder := false
	if args != nil {
		for i, a := range args.Args {
			if a.Placeholder {
				placeholder = true
				continue
			}
			if a.Unpack {
				unpacked = true
				t := x.expr(a.Value, env, nil)
				if _, _, ok := types.IterableKV(t, x.p()); !ok && !types.IsMixed(t) {
					x.report(a.S, P2303, "cannot unpack %s", x.typeName(t))
				}
				continue
			}
			if a.Name != nil {
				seenNamed = true
				var p *program.Param
				for _, cand := range sig.params {
					if cand.Name == a.Name.Name {
						p = cand
					}
				}
				if p == nil {
					if len(sig.params) > 0 && sig.params[len(sig.params)-1].Variadic {
						p = sig.params[len(sig.params)-1]
					} else {
						x.report(a.S, P2010, "unknown named argument $%s for %s", a.Name.Name, sig.name)
						x.expr(a.Value, env, nil)
						continue
					}
				}
				bindings = append(bindings, bound{a, p, i})
				continue
			}
			if seenNamed {
				x.report(a.S, P2110, "positional argument after a named argument")
			}
			var p *program.Param
			if i < len(sig.params) {
				p = sig.params[i]
			} else if len(sig.params) > 0 && sig.params[len(sig.params)-1].Variadic {
				p = sig.params[len(sig.params)-1]
			}
			if p == nil {
				if !unpacked {
					x.report(a.S, P2102, "%s expects %d argument%s, %d given", sig.name, len(sig.params), plural(len(sig.params)), len(args.Args))
				}
				x.expr(a.Value, env, nil)
				continue
			}
			bindings = append(bindings, bound{a, p, i})
		}
	}
	// Required parameters present?
	if !unpacked && !placeholder {
		required := 0
		for _, p := range sig.params {
			if !p.HasDefault && !p.Variadic {
				required++
			}
		}
		given := map[*program.Param]bool{}
		for _, b := range bindings {
			given[b.param] = true
		}
		missing := 0
		for _, p := range sig.params {
			if !p.HasDefault && !p.Variadic && !given[p] {
				missing++
			}
		}
		if missing > 0 {
			n := 0
			if args != nil {
				n = len(args.Args)
			}
			x.report(span, P2101, "%s expects at least %d argument%s, %d given", sig.name, required, plural(required), n)
		}
	}
	// Pass 0 types every non-closure argument and infers from those that
	// are not callables; pass 1 infers from callable-typed arguments (a
	// generic first-class callable resolves its own parameters against
	// what pass 0 found); pass 2 types closures with their expected
	// parameter types and infers from their return types.
	argTypes := make([]types.Type, len(bindings))
	isClosure := func(e ast.Expr) bool {
		switch e.(type) {
		case *ast.Closure, *ast.ArrowFunc:
			return true
		}
		return false
	}
	isCallableType := func(t types.Type) bool {
		for _, m := range types.Members(t) {
			if c, ok := m.(types.Callable); ok && c.HasSig {
				return true
			}
		}
		return false
	}
	for i, b := range bindings {
		if isClosure(b.arg.Value) {
			continue
		}
		pt := paramType(b.param)
		var at types.Type
		if b.param.ByRef {
			if at = sig.known[b.arg.Value]; at == nil {
				at = x.exprLenient(b.arg.Value, env)
			}
			if !isWritable(b.arg.Value) {
				x.report(b.arg.S, P2112, "argument %d of %s is by reference and needs a variable", b.idx+1, sig.name)
			}
		} else {
			if at = sig.known[b.arg.Value]; at == nil {
				at = x.expr(b.arg.Value, env, pt)
			}
		}
		argTypes[i] = at
		if len(inferable) > 0 && !isCallableType(at) {
			unify(pt, at, subst, inferable, x.p())
		}
	}
	for i, b := range bindings {
		if isClosure(b.arg.Value) || argTypes[i] == nil || !isCallableType(argTypes[i]) {
			continue
		}
		if len(inferable) > 0 {
			unify(paramType(b.param), argTypes[i], subst, inferable, x.p())
		}
	}
	for i, b := range bindings {
		if !isClosure(b.arg.Value) {
			continue
		}
		pt := paramType(b.param)
		at, isKnown := sig.known[b.arg.Value]
		if !isKnown {
			at = x.expr(b.arg.Value, env, pt)
		}
		argTypes[i] = at
		if len(inferable) > 0 {
			// Against the declared type, not the one substituted so far: a
			// closure returning int widens an A first bound to the literal 0.
			unify(types.Substitute(b.param.Type, rename), at, subst, inferable, x.p())
		}
	}
	// Infer from the expected type too (contextual typing of the return).
	if len(inferable) > 0 && expected != nil && sig.ret != nil {
		unify(types.Substitute(types.Substitute(sig.ret, rename), subst), expected, subst, inferable, x.p())
	}
	// Expose the results under the declared names too (new Foo() reads them).
	for i, tp := range sig.typeParams {
		if v, ok := subst[tps[i].Name]; ok {
			subst[tp.Name] = v
		}
	}
	// An inferred type argument must respect its bound, like a written one
	// (`repo(Order::class)` against `class<T of User>`): PHPStan reports
	// the argument, Phaenon the type argument it stands for.
	for i, tp := range sig.typeParams {
		v, ok := subst[tps[i].Name]
		if !ok || !inferable[tps[i].Name] || tp.Bound == nil {
			continue
		}
		if _, unknown := v.(types.Unknown); unknown || types.IsMixed(v) {
			continue // mixed may still fit; the argument itself is reported where it is passed
		}
		bound := types.Substitute(tp.Bound, subst)
		if !types.IsSubtype(v, bound, x.p()) {
			x.report(span, P3002, "type argument %s inferred for %s of %s is not a subtype of its bound %s", x.typeName(v), tp.Name, sig.name, x.typeName(bound))
		}
	}
	// Check acceptability with the final substitution.
	for i, b := range bindings {
		if b.param.ByRef {
			continue
		}
		pt := paramType(b.param)
		pt = finalizeParams(pt, tps)
		at := types.Substitute(argTypes[i], freshOnly(subst)) // generic callables as resolved
		if at == nil {
			continue
		}
		if _, isVoid := at.(types.Void); isVoid {
			x.report(b.arg.S, P2205, "result of a void function is used")
			continue
		}
		if !types.IsAcceptable(at, pt, x.p()) {
			x.report(b.arg.S, P2103, "argument %d ($%s) of %s expects %s, %s given", b.idx+1, b.param.Name, sig.name, x.typeName(pt), x.typeName(at))
		}
	}
	argByName := map[string]types.Type{}
	var bag []types.Type // the arguments a variadic parameter received, as one list
	bagParam := ""
	for i, b := range bindings {
		if argTypes[i] == nil {
			continue
		}
		if b.param.Variadic {
			bagParam = b.param.Name
			if b.arg.Unpack {
				if _, v, ok := types.IterableKV(argTypes[i], x.p()); ok {
					bag = append(bag, v)
				}
			} else {
				bag = append(bag, argTypes[i])
			}
			continue
		}
		argByName[b.param.Name] = argTypes[i]
	}
	if bagParam != "" {
		argByName[bagParam] = types.Array{Key: types.TInt, Value: types.MakeUnion(bag...), List: true, NonEmpty: len(bag) > 0}
	}
	for _, p := range sig.params {
		// An omitted argument is its default, when that is a literal or a
		// global constant (anything else would need the callee's scope).
		if _, given := argByName[p.Name]; given || p.Default == nil {
			continue
		}
		name, isConst := constName(p.Default)
		if name == "unknown" { // php-src's stubs write `= UNKNOWN` for a default they cannot spell
			continue
		}
		if isConst || isLiteralExpr(p.Default) {
			saved := x.c.mark()
			argByName[p.Name] = x.expr(p.Default, newEnv(), nil)
			x.c.rollback(saved)
		}
	}
	// By-reference parameters define or retype their arguments.
	for i, b := range bindings {
		if !b.param.ByRef {
			continue
		}
		out := b.param.Out
		if out != nil {
			out = x.evalConditional(types.Substitute(types.Substitute(out, rename), subst), argByName)
		}
		if out == nil {
			out = types.Substitute(types.Substitute(b.param.Type, rename), subst)
			if argTypes[i] != nil && !types.IsMixed(argTypes[i]) && !x.maybeUndefined(b.arg.Value, env) {
				continue // keep the caller's more precise type
			}
		}
		x.assignTo(b.arg.Value, finalizeParams(out, tps), env, b.arg.S)
	}
	// Property narrowings survive calls, as in PHPStan: `$this->x`
	// stays non-null across `$this->log()`.
	if placeholder {
		return x.callableOf(&signature{name: sig.name, params: sig.params, ret: sig.ret, subst: subst, recv: sig.recv})
	}
	ret := sig.ret
	if ret == nil {
		return types.TMixed
	}
	ret = types.Substitute(types.Substitute(ret, rename), subst)
	ret = finalizeParams(ret, tps)
	if sig.recv != nil {
		ret = replaceStatic(ret, sig.recv)
	}
	return x.evalConditional(ret, argByName)
}

// evalConditional resolves conditional return types (`$x is Foo ? A : B`,
// `T is Foo ? A : B`) once the arguments and type parameters are known.
// An undecidable condition yields the union of both branches.
func (x *fnCtx) evalConditional(t types.Type, args map[string]types.Type) types.Type {
	switch c := t.(type) {
	case types.Conditional:
		subject := c.Subject
		if c.ParamName != "" {
			subject = args[c.ParamName]
		}
		then := x.evalConditional(c.Then, args)
		els := x.evalConditional(c.Else, args)
		if subject == nil || isParamType(subject) || types.IsMixed(subject) {
			return types.MakeUnion(then, els)
		}
		yes := types.IsSubtype(subject, c.Target, x.p())
		no := !types.Overlaps(subject, c.Target, x.p())
		if c.Negated {
			yes, no = no, yes
		}
		switch {
		case yes:
			return then
		case no:
			return els
		}
		return types.MakeUnion(then, els)
	case types.Union:
		return types.Map(c, func(m types.Type) types.Type { return x.evalConditional(m, args) })
	}
	return t
}

// freshParams gives type parameters per-call identities: T becomes
// "T\x00<n>" (printed as T), so two calls — or a caller and its callee —
// never share a parameter by accident. rename maps the declared names to
// the fresh parameters.
func (x *fnCtx) freshParams(tps []*program.TypeParam) (map[string]types.Type, []*program.TypeParam) {
	if len(tps) == 0 {
		return nil, nil
	}
	x.c.callSeq++
	rename := make(map[string]types.Type, len(tps))
	fresh := make([]*program.TypeParam, len(tps))
	for i, tp := range tps {
		id := fmt.Sprintf("%s\x00%d", tp.Name, x.c.callSeq)
		fresh[i] = &program.TypeParam{Name: id, Bound: tp.Bound, Default: tp.Default, Variance: tp.Variance, Native: tp.Native}
		rename[tp.Name] = types.Param{Name: id, Bound: tp.Bound, Owner: tp.Name}
	}
	for _, f := range fresh {
		f.Bound = types.Substitute(f.Bound, rename)
		f.Default = types.Substitute(f.Default, rename)
	}
	for k, v := range rename {
		p := v.(types.Param)
		p.Bound = types.Substitute(p.Bound, rename)
		rename[k] = p
	}
	return rename, fresh
}

// freshOnly keeps the substitutions of per-call parameters (fresh ids);
// the declared names of a callee's class parameters must not touch the
// caller's types, which may use the same names.
func freshOnly(subst map[string]types.Type) map[string]types.Type {
	out := map[string]types.Type{}
	for k, v := range subst {
		if strings.Contains(k, "\x00") {
			out[k] = v
		}
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func isWritable(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Variable, *ast.PropertyFetch, *ast.StaticPropertyFetch, *ast.Index, *ast.VariableVariable:
		return true
	case *ast.Paren:
		return isWritable(e.Expr)
	}
	return false
}

// finalizeParams replaces the callee's own type parameters that were not
// inferred by their bound (or mixed).
func finalizeParams(t types.Type, tps []*program.TypeParam) types.Type {
	if len(tps) == 0 || t == nil {
		return t
	}
	subst := map[string]types.Type{}
	for _, tp := range tps {
		if types.ContainsParam(t, tp.Name) {
			if tp.Bound != nil {
				subst[tp.Name] = tp.Bound
			} else {
				subst[tp.Name] = types.TMixed
			}
		}
	}
	return types.Substitute(t, subst)
}

// unify infers template arguments by matching a parameter type against an
// argument type.
func unify(param, arg types.Type, subst map[string]types.Type, inferable map[string]bool, h types.Hierarchy) {
	if param == nil || arg == nil {
		return
	}
	switch p := param.(type) {
	case types.Param:
		if !inferable[p.Name] {
			return
		}
		if _, isVoid := arg.(types.Void); isVoid {
			return
		}
		if cur, ok := subst[p.Name]; ok {
			if !types.IsSubtype(arg, cur, h) {
				subst[p.Name] = types.MakeUnion(cur, arg)
			}
		} else {
			subst[p.Name] = arg
		}
	case types.Union:
		// T|null against X|null: strip null; T|class-string<T> against the
		// argument's members by kind.
		pm := types.StripNull(p)
		am := types.StripNull(arg)
		if u, ok := pm.(types.Union); ok {
			// Structured members first (class-string<T> against 'Foo');
			// a bare T member only catches what nothing else matched.
			bound := false
			for _, member := range u.Types {
				if _, isParam := member.(types.Param); isParam {
					continue
				}
				for _, a := range types.Members(am) {
					if kindMatches(member, a) {
						before := len(subst)
						unify(member, a, subst, inferable, h)
						if len(subst) != before {
							bound = true
						}
					}
				}
			}
			if !bound {
				for _, member := range u.Types {
					if _, isParam := member.(types.Param); isParam {
						unify(member, am, subst, inferable, h)
					}
				}
			}
			return
		}
		unify(pm, am, subst, inferable, h)
	case types.Array:
		for _, a := range types.Members(arg) {
			switch a := a.(type) {
			case types.Array:
				unify(p.Key, a.Key, subst, inferable, h)
				unify(p.Value, a.Value, subst, inferable, h)
			case types.Shape:
				unify(p.Key, shapeKeys(a), subst, inferable, h)
				unify(p.Value, shapeValues(a), subst, inferable, h)
			}
		}
	case types.Iterable:
		k, v, ok := types.IterableKV(arg, h)
		if ok {
			unify(p.Key, k, subst, inferable, h)
			unify(p.Value, v, subst, inferable, h)
		}
	case types.ClassString:
		if p.Of == nil {
			return
		}
		for _, a := range types.Members(arg) {
			switch a := a.(type) {
			case types.StringLit:
				unify(p.Of, types.Class{Name: a.V}, subst, inferable, h)
			case types.ClassString:
				if a.Of != nil {
					unify(p.Of, a.Of, subst, inferable, h)
				} else {
					unify(p.Of, types.TObject, subst, inferable, h)
				}
			case types.String:
				unify(p.Of, types.TObject, subst, inferable, h)
			}
		}
	case types.Class:
		if len(p.Args) == 0 {
			return
		}
		for _, a := range types.Members(arg) {
			var ac types.Class
			switch a := a.(type) {
			case types.Class:
				ac = a
			case types.Static:
				if a.Of != nil {
					ac = *a.Of
				}
			case types.This:
				if a.Of != nil {
					ac = *a.Of
				}
			default:
				continue
			}
			args := ac.Args
			if !strings.EqualFold(ac.Name, p.Name) {
				if prog, ok := h.(*program.Program); ok {
					if from := prog.Class(ac.Name); from != nil {
						if mapped, ok := from.SuperArgs(p.Name, ac.Args); ok {
							args = mapped
						} else {
							continue
						}
					}
				}
			}
			for i := range p.Args {
				if i < len(args) {
					unify(p.Args[i], args[i], subst, inferable, h)
				}
			}
		}
	case types.Callable:
		for _, a := range types.Members(arg) {
			ac, ok := a.(types.Callable)
			if !ok || !ac.HasSig {
				continue
			}
			// A generic callable (the first-class callable of a generic
			// function) resolves its own parameters against ours first,
			// so array_map(self::make(...), $items) types R from T.
			foreign := map[string]bool{}
			bounds := map[string]types.Type{}
			types.Walk(ac, func(t types.Type) {
				if fp, ok := t.(types.Param); ok && strings.Contains(fp.Name, "\x00") && !inferable[fp.Name] {
					foreign[fp.Name] = true
					bounds[fp.Name] = fp.Bound
				}
			})
			if len(foreign) > 0 {
				local := map[string]types.Type{}
				for i := range ac.Params {
					if i < len(p.Params) && ac.Params[i].Type != nil && p.Params[i].Type != nil {
						unify(ac.Params[i].Type, types.Substitute(p.Params[i].Type, subst), local, foreign, h)
					}
				}
				for name := range foreign {
					if _, ok := local[name]; !ok {
						if b := bounds[name]; b != nil {
							local[name] = b
						} else {
							local[name] = types.TMixed
						}
					}
				}
				if resolved, ok := types.Substitute(ac, local).(types.Callable); ok {
					ac = resolved
				}
				// Remember the resolution: the argument is checked with it.
				for k, v := range local {
					subst[k] = v
				}
			}
			for i := range p.Params {
				if i < len(ac.Params) && ac.Params[i].Type != nil {
					// A callback may accept more than the position offers
					// (int|string for K of string): a bound parameter that
					// already fits is not widened.
					if pp, isParam := p.Params[i].Type.(types.Param); isParam {
						if cur, bound := subst[pp.Name]; bound && types.IsSubtype(cur, ac.Params[i].Type, h) {
							continue
						}
					}
					unify(p.Params[i].Type, ac.Params[i].Type, subst, inferable, h)
				}
			}
			if p.Return != nil && ac.Return != nil {
				unify(p.Return, ac.Return, subst, inferable, h)
			}
		}
	case types.Intersection:
		for _, m := range p.Types {
			unify(m, arg, subst, inferable, h)
		}
	}
}

func kindMatches(param, arg types.Type) bool {
	switch param.(type) {
	case types.Param:
		return true
	case types.ClassString:
		switch arg.(type) {
		case types.StringLit, types.ClassString, types.String:
			return true
		}
		return false
	case types.Array, types.Shape:
		switch arg.(type) {
		case types.Array, types.Shape:
			return true
		}
		return false
	case types.Class, types.Object:
		return types.IsObjectLike(arg)
	case types.Callable:
		_, ok := arg.(types.Callable)
		return ok
	case types.Iterable:
		_, _, ok := types.IterableKV(arg, nil)
		return ok
	}
	return false
}

// pipe types `left |> callee`: callee(left).
func (x *fnCtx) pipe(e *ast.Binary, left types.Type, env *state) types.Type {
	ct := x.expr(e.Right, env, nil)
	for _, m := range types.Members(ct) {
		if c, ok := m.(types.Callable); ok && c.HasSig {
			if len(c.Params) > 0 && c.Params[0].Type != nil && !types.IsAcceptable(left, c.Params[0].Type, x.p()) {
				x.report(e.Left.Span(), P2103, "pipe operand expects %s, %s given", x.typeName(c.Params[0].Type), x.typeName(left))
			}
			if c.Return != nil {
				return c.Return
			}
		}
	}
	return types.TMixed
}
