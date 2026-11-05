package checker

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
	"github.com/phaenonlang/phaenon/stdlib"
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
	file       *program.File  // the file that declares it, for "declared here"
	never      bool
	deprecated bool
	// known holds argument types computed before the call is checked (the
	// receiver of a stdlib method call), so the expression is not typed twice.
	known map[ast.Expr]types.Type
	// stub marks a php-src declaration: its by-reference parameters are
	// read-write unless the overlay says @param-out (PHPStan's functionMap
	// makes the same distinction), where user code's are writes.
	stub bool
	// node is the call being checked: a partial application of it is
	// recorded for the emitter under its arguments.
	node ast.Expr
	// fn or meth is the callee, whose declaration says what the call
	// throws (throws.go); neither for a callable of unknown origin.
	fn   *program.Function
	meth *program.Method
}

// methodFile is the file that declares a method: its trait's when a trait
// brought it in.
func methodFile(m *program.Method) *program.File {
	if m.FromTrait != nil {
		return m.FromTrait.File
	}
	if m.Class != nil {
		return m.Class.File
	}
	return nil
}

func isStubFile(f *program.File) bool {
	return f != nil && strings.HasPrefix(f.Path, "<stub>/")
}

// ------------------------------------------------------------------ function calls

func (x *fnCtx) call(e *ast.Call, env *state, expected types.Type) types.Type {
	defer x.unresolvedCall(e.S, e.Args)
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
		if blockingSleeps[lower] && x.inBlock {
			x.report(e.S, P2605, "%s() parks the whole process, the event loop and every task with it: Scope::sleep() yields instead", lower)
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
	x.c.libraryFunction(n.S, fn)
	if fn.File.IsStub() {
		x.deprecatedCall(strings.ToLower(fn.Name), e, env)
	}
	if fn.Deprecated && x.c.opts.Deprecations {
		x.report(n.S, P4006, "function %s() is deprecated%s", fn.Name, fn.DeprecatedNote)
	}
	sig := &signature{name: fn.Name + "()", params: fn.Params, ret: fn.Return, typeParams: fn.TypeParams, stub: isStubFile(fn.File), node: e, file: fn.File, fn: fn}
	sig.explicit = x.explicitArgs(e.TypeArgs, fn.TypeParams, fn.Name+"()", e.S)
	if e.Args != nil && e.Args.FirstClassCallable {
		return x.callableOf(sig)
	}
	if lower == "array_map" && e.Args != nil && len(e.Args.Args) > 2 {
		x.arrayMapOverSeveral(sig, e.Args, env)
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

// ownsParam reports a type parameter of the code being checked: the class's
// or the method's own, told from a foreign one of the same name (a `T` of
// another class's constructor kept on a property) by its owner.
func (x *fnCtx) ownsParam(p types.Param) bool {
	if !x.inScope[p.Name] {
		return false // a constructor's template kept open on a property, seen from elsewhere
	}
	own := ""
	if x.class != nil {
		own = x.class.Name
	}
	// Ours, or inherited with the signature from a parent or an interface
	// (PHPStan reads the phpdoc down the hierarchy; such a parameter is in
	// scope because the method's signature mentions it).
	return p.Owner == "" || strings.EqualFold(p.Owner, own) || x.p().IsSubclassOf(own, p.Owner)
}

// closeForeignParams replaces, in an argument's type, the type parameters
// nothing here can bind by their bounds: a promoted property typed with its
// constructor's `@template T` (`DocumentMapper<T> $mapper`) is read as
// DocumentMapper<T> so a call through it infers T, but passed on as a value
// it is a DocumentMapper<object>, as PHPStan reads it. The caller's own
// parameters and the callee's fresh ones stay.
func (x *fnCtx) closeForeignParams(t types.Type) types.Type {
	if !mentionsParam(t) {
		return t
	}
	return types.MapDeep(t, func(m types.Type) types.Type {
		p, ok := m.(types.Param)
		if !ok || x.ownsParam(p) || strings.Contains(p.Name, "\x00") {
			return m // ours (the class's or this method's), or the callee's
		}
		if p.Bound != nil {
			return p.Bound
		}
		return types.TMixed
	})
}

// expectsCallable reports a parameter type with a callable member.
func expectsCallable(t types.Type) bool {
	for _, m := range types.Members(t) {
		if _, ok := m.(types.Callable); ok {
			return true
		}
	}
	return false
}

// namedCallable is the signature behind a callable string: a function
// ('strval', '\\is_string') or a static method ('Foo::bar'); nil when the
// name is unknown.
func (x *fnCtx) namedCallable(name string) types.Type {
	name = strings.TrimPrefix(name, "\\")
	if cls, meth, ok := strings.Cut(name, "::"); ok {
		cl := x.p().Class(cls)
		if cl == nil {
			return nil
		}
		cl.Signature()
		m := cl.FindMethod(meth)
		if m == nil {
			return nil
		}
		return x.callableOf(&signature{name: cl.Name + "::" + m.Name + "()", params: m.Params, ret: m.Return, typeParams: m.TypeParams, decl: m.Class, recv: types.Class{Name: cl.Name}})
	}
	fn := x.lookupFunction(&ast.Name{Parts: strings.Split(name, "\\"), Kind: ast.NameFullyQualified})
	if fn == nil {
		return nil
	}
	fn.Signature()
	return x.callableOf(&signature{name: fn.Name + "()", params: fn.Params, ret: fn.Return, typeParams: fn.TypeParams, stub: isStubFile(fn.File)})
}

// defaultType is the type of a parameter's default value as far as it can
// be read without the callee's scope: a literal, a constant, a class
// constant of the declaring class (`self::X`) or of a named class.
func (x *fnCtx) defaultType(e ast.Expr, sig *signature) types.Type {
	saved := x.c.mark()
	defer x.c.rollback(saved) // a default the callee's scope alone can read is not this call's problem
	switch d := e.(type) {
	case *ast.IntLit, *ast.FloatLit, *ast.StringLit:
		return x.exprLenient(d, newEnv())
	case *ast.Name, *ast.ConstFetch:
		var n *ast.Name
		if nn, ok := d.(*ast.Name); ok {
			n = nn
		} else {
			n = d.(*ast.ConstFetch).Name
		}
		if len(n.Parts) == 1 && n.Parts[0] == "UNKNOWN" {
			return nil // php-src's placeholder
		}
		return x.constFetch(&ast.ConstFetch{S: e.Span(), Name: n})
	case *ast.ClassConstFetch:
		id, ok := d.Name.(*ast.Ident)
		n, isName := d.Class.(*ast.Name)
		if !ok || !isName {
			return nil
		}
		var cl *program.Class
		switch specialClassName(n) {
		case "self", "static":
			cl = sig.decl
		case "":
			if n.Kind == ast.NameFullyQualified {
				cl = x.p().Class(strings.Join(n.Parts, "\\"))
			}
		}
		if cl == nil {
			return nil
		}
		cl.Signature()
		if k := cl.FindConst(id.Name); k != nil {
			return x.p().ConstantType(k)
		}
	}
	return nil
}

// arrayMapOverSeveral adapts array_map's signature to a call with several
// arrays: the callback takes one value of each, in order, and the result
// is a list (keys are not preserved then).
func (x *fnCtx) arrayMapOverSeveral(sig *signature, args *ast.ArgList, env *state) {
	if len(sig.params) < 2 {
		return
	}
	cb, ok := sig.params[0].Type.(types.Callable)
	if !ok || !cb.HasSig {
		return
	}
	known := map[ast.Expr]types.Type{}
	var values []types.CallableParam
	for _, a := range args.Args[1:] {
		if a.Value == nil || a.Unpack || a.Name != nil {
			return
		}
		t := x.expr(a.Value, env, nil)
		known[a.Value] = t
		_, v, ok := types.IterableKV(t, x.p())
		if !ok {
			v = types.TMixed
		}
		values = append(values, types.CallableParam{Type: v})
	}
	callback := *sig.params[0]
	callback.Type = types.Callable{HasSig: true, Params: values, Return: cb.Return}
	params := []*program.Param{&callback}
	for _, p := range sig.params[1:] {
		q := *p
		q.Type = types.TArray
		params = append(params, &q)
	}
	sig.params = params
	sig.ret = types.Array{Key: types.TInt, Value: cb.Return, List: true}
	sig.known = known
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
	case "json_encode":
		// JSON_THROW_ON_ERROR among the flags makes the false return
		// impossible (PHPStan's JsonThrowOnErrorDynamicReturnTypeExtension).
		var flags ast.Expr
		for i, a := range args.Args {
			if (a.Name == nil && i == 1) || (a.Name != nil && a.Name.Name == "flags") {
				flags = a.Value
			}
		}
		if flags != nil && mentionsConst(flags, "json_throw_on_error") {
			return types.Subtract(ret, types.TFalse, x.p())
		}
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
			if sh, ok := argType(1).(types.Shape); ok {
				if r, ok := ret.(types.Array); ok {
					if sh.Unsealed {
						return types.Array{Key: r.Key, Value: r.Value, List: sh.List || shapeIsList(sh)}
					}
					// A sealed shape maps entry by entry: the keys stay, each
					// value is what the callback returns (PHPStan keeps the
					// constant array too).
					mapped := types.Shape{List: sh.List}
					for _, e := range sh.Entries {
						mapped.Entries = append(mapped.Entries, types.ShapeEntry{Key: e.Key, IntKey: e.IntKey, IsInt: e.IsInt, Optional: e.Optional, Type: r.Value})
					}
					return mapped
				}
			}
		}
	case "array_filter":
		if sh, ok := argType(0).(types.Shape); ok && len(sh.Entries) == 0 {
			return sh // nothing to filter: still []
		}
		if a, ok := argType(0).(types.Array); ok {
			if r, ok := ret.(types.Array); ok {
				// The values are the array's: a callback over the keys
				// (ARRAY_FILTER_USE_KEY) binds V to the key type as well.
				v := a.Value
				if len(args.Args) == 2 && args.Args[1].Value != nil {
					if nv := x.filteredValue(args.Args[1].Value, a.Value, env); nv != nil {
						v = nv
					}
				}
				return types.Array{Key: r.Key, Value: v, List: false, NonEmpty: false}
			}
		}
	case "array_values":
		if sh, ok := argType(0).(types.Shape); ok && len(sh.Entries) == 0 {
			return sh // nothing to renumber: still []
		}
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
	case "sprintf", "vsprintf":
		// A format with text around its conversions never gives an empty
		// string (PHPStan says non-falsy-string).
		if len(args.Args) > 0 {
			if lit, ok := args.Args[0].Value.(*ast.StringLit); ok && formatHasText(program.StringValue(lit)) {
				return types.String{NonEmpty: true}
			}
		}
		return ret
	case "implode", "join":
		return x.implodeType(args, argType, ret)
	case "str_repeat", "strtolower", "strtoupper", "ucfirst", "trim", "ltrim", "rtrim", "substr":
		return ret
	case "filter_var":
		// The filter constant says what comes back, the flags whether
		// failure is false or null (PHPStan's FilterVar extension).
		if len(args.Args) == 0 {
			return ret
		}
		var filter int64 = 516 // FILTER_DEFAULT
		if len(args.Args) >= 2 {
			lit, ok := argType(1).(types.Int)
			if !ok || lit.Lo != lit.Hi {
				return ret
			}
			filter = lit.Lo
		}
		var t types.Type
		switch filter {
		case 257: // FILTER_VALIDATE_INT
			t = types.TInt
		case 259: // FILTER_VALIDATE_FLOAT
			t = types.TFloat
		case 258: // FILTER_VALIDATE_BOOL
			t = types.TBool
		case 272, 273, 274, 275, 276, 277, 516: // REGEXP, URL, EMAIL, IP, MAC, DOMAIN, DEFAULT
			t = types.TString
		default:
			return ret
		}
		var fail types.Type = types.TFalse
		if len(args.Args) >= 3 && args.Args[2].Value != nil && mentionsConst(args.Args[2].Value, "filter_null_on_failure") {
			fail = types.TNull
		}
		return types.MakeUnion(t, fail)
	case "array_sum":
		// Ints add up to an int, non-negative ones to a non-negative int,
		// floats to a float (PHPStan's ArraySumFunctionDynamicReturnTypeExtension).
		if _, v, ok := types.IterableKV(argType(0), x.p()); ok {
			return arraySumType(v, ret)
		}
	case "preg_split":
		if t := pregSplitType(args); t != nil {
			return t
		}
	case "min", "max":
		var ts []types.Type
		if len(args.Args) == 1 {
			_, v, ok := types.IterableKV(argType(0), x.p())
			if !ok {
				return ret
			}
			ts = []types.Type{v}
		} else {
			for i := range args.Args {
				ts = append(ts, argType(i))
			}
		}
		return minMaxType(name == "max", ts)
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

// shapeIsList reports a shape whose keys are 0..n-1 in order, a list
// whether or not it was written as one.
func shapeIsList(s types.Shape) bool {
	for i, e := range s.Entries {
		if !e.IsInt || e.IntKey != int64(i) {
			return false
		}
	}
	return true
}

// arraySumType is array_sum() over values of type v: int for ints
// (int<0, max> when none is negative), float for floats, int|float when
// both or anything else numeric may be among them; ret when v says
// nothing.
func arraySumType(v, ret types.Type) types.Type {
	ints, floats := 0, 0
	nonNegative := true
	for _, m := range types.Members(v) {
		switch m := m.(type) {
		case types.Int:
			ints++
			nonNegative = nonNegative && m.Lo >= 0
		case types.Float, types.FloatLit:
			floats++
		default:
			return ret
		}
	}
	switch {
	case ints > 0 && floats == 0:
		if nonNegative {
			return types.Int{Lo: 0, Hi: math.MaxInt64}
		}
		return types.TInt
	case floats > 0 && ints == 0:
		return types.TFloat
	}
	return ret
}

// pregSplitType is what preg_split() returns under the flags written at
// the call: pieces, non-empty under PREG_SPLIT_NO_EMPTY, each paired with
// its offset under PREG_SPLIT_OFFSET_CAPTURE; nil without flags (the
// overlay's default applies).
func pregSplitType(args *ast.ArgList) types.Type {
	if args == nil {
		return nil
	}
	var flags ast.Expr
	for i, a := range args.Args {
		if (a.Name == nil && i == 3) || (a.Name != nil && a.Name.Name == "flags") {
			flags = a.Value
		}
	}
	if flags == nil {
		return nil
	}
	var piece types.Type = types.TString
	if mentionsConst(flags, "preg_split_no_empty") {
		piece = types.String{NonEmpty: true}
	}
	if mentionsConst(flags, "preg_split_offset_capture") {
		piece = types.Shape{List: true, Entries: []types.ShapeEntry{{IsInt: true, IntKey: 0, Type: piece}, {IsInt: true, IntKey: 1, Type: types.Int{Lo: 0, Hi: math.MaxInt64}}}}
	}
	return types.MakeUnion(types.Array{Key: types.TInt, Value: piece, List: true}, types.TFalse)
}

// pregMatchesType is what preg_match() / preg_match_all() leave in
// $matches under the flags written at the call: PREG_OFFSET_CAPTURE pairs
// every match with its offset, PREG_SET_ORDER lists the sets. nil when the
// overlay's default applies.
func pregMatchesType(sigName, param string, args *ast.ArgList) types.Type {
	name := strings.ToLower(sigName)
	if param != "matches" || (name != "preg_match()" && name != "preg_match_all()") || args == nil {
		return nil
	}
	var flags ast.Expr
	for i, a := range args.Args {
		if (a.Name == nil && i == 3) || (a.Name != nil && a.Name.Name == "flags") {
			flags = a.Value
		}
	}
	if flags == nil {
		return nil
	}
	offsets := mentionsConst(flags, "preg_offset_capture")
	setOrder := mentionsConst(flags, "preg_set_order")
	if !offsets && !setOrder {
		return nil
	}
	var match types.Type = types.TString
	if offsets {
		match = types.Shape{List: true, Entries: []types.ShapeEntry{{IsInt: true, IntKey: 0, Type: types.TString}, {IsInt: true, IntKey: 1, Type: types.TInt}}}
	}
	group := types.Array{Key: types.TArrayKey, Value: match}
	if name == "preg_match()" {
		return group
	}
	if setOrder {
		return types.Array{Key: types.TInt, Value: group, List: true}
	}
	return types.Array{Key: types.TArrayKey, Value: types.Array{Key: types.TInt, Value: match, List: true}}
}

// filteredValue is the value type array_filter() keeps under its callback:
// what the callback's condition narrows the value to (`fn (?string $a) =>
// null !== $a` keeps strings), or what a predicate name ('is_string')
// asks for. nil when the callback says nothing usable.
func (x *fnCtx) filteredValue(callback ast.Expr, value types.Type, env *state) types.Type {
	var param string
	var cond ast.Expr
	switch f := callback.(type) {
	case *ast.ArrowFunc:
		if len(f.Params) != 1 {
			return nil
		}
		param, cond = f.Params[0].Var.Name, f.Body
	case *ast.Closure:
		if len(f.Params) != 1 || len(f.Body) != 1 {
			return nil
		}
		r, ok := f.Body[0].(*ast.Return)
		if !ok || r.Expr == nil {
			return nil
		}
		param, cond = f.Params[0].Var.Name, r.Expr
	case *ast.StringLit:
		name := strings.ToLower(strings.TrimPrefix(program.StringValue(f), "\\"))
		if target := predicateTarget(name); target != nil {
			return types.Intersect(value, target, x.p())
		}
		return nil
	case *ast.Call:
		// is_string(...): the predicate as a first-class callable
		n, ok := f.Callee.(*ast.Name)
		if !ok || f.Args == nil || !f.Args.FirstClassCallable {
			return nil
		}
		if target := predicateTarget(strings.ToLower(n.Parts[len(n.Parts)-1])); target != nil {
			return types.Intersect(value, target, x.p())
		}
		return nil
	default:
		return nil
	}
	saved := x.c.mark()
	cenv := env.clone()
	cenv.set(param, value)
	then, _ := x.cond(cond, cenv)
	x.c.rollback(saved) // the callback was checked as an argument already
	if info, ok := then.get(param); ok {
		return info.t
	}
	return nil
}

// minMaxType types min()/max() of the given arguments: the range the
// result can lie in when every argument is an int (`max(1, $n)` is
// int<1, max>), else the union of the arguments.
func minMaxType(isMax bool, ts []types.Type) types.Type {
	var ints []types.Int
	for _, t := range ts {
		for _, m := range types.Members(t) {
			i, ok := m.(types.Int)
			if !ok {
				return types.MakeUnion(ts...)
			}
			ints = append(ints, i)
		}
	}
	if len(ints) == 0 {
		return types.MakeUnion(ts...)
	}
	lo, hi := ints[0].Lo, ints[0].Hi
	for _, i := range ints[1:] {
		if isMax {
			lo, hi = max(lo, i.Lo), max(hi, i.Hi)
		} else {
			lo, hi = min(lo, i.Lo), min(hi, i.Hi)
		}
	}
	return types.Int{Lo: lo, Hi: hi}
}

// implodeType refines implode(): a sealed array of literals joined by a
// literal separator is that literal, and a non-empty array of non-empty
// strings joins into a non-empty string (PHPStan's
// ImplodeFunctionReturnTypeExtension).
func (x *fnCtx) implodeType(args *ast.ArgList, argType func(int) types.Type, ret types.Type) types.Type {
	for _, a := range args.Args {
		if a.Name != nil || a.Unpack || a.Placeholder != nil {
			return ret
		}
	}
	var sep, arr types.Type
	switch len(args.Args) {
	case 1:
		sep, arr = types.StringLit{V: ""}, argType(0)
	case 2:
		sep, arr = argType(0), argType(1)
	default:
		return ret
	}
	if sh, ok := arr.(types.Shape); ok && !sh.Unsealed {
		if s, ok := sep.(types.StringLit); ok {
			if parts, ok := literalParts(sh); ok {
				return types.StringLit{V: strings.Join(parts, s.V)}
			}
		}
	}
	nonEmpty := false
	switch a := arr.(type) {
	case types.Array:
		nonEmpty = a.NonEmpty
	case types.Shape:
		for _, en := range a.Entries {
			nonEmpty = nonEmpty || !en.Optional
		}
	}
	if _, v, ok := types.IterableKV(arr, x.p()); ok && nonEmpty && types.IsSubtype(v, types.String{NonEmpty: true}, x.p()) {
		return types.String{NonEmpty: true}
	}
	return ret
}

// literalParts spells out the entries of a shape whose every entry is
// present and a string or int literal, as implode() writes them.
func literalParts(sh types.Shape) ([]string, bool) {
	parts := make([]string, 0, len(sh.Entries))
	for _, en := range sh.Entries {
		if en.Optional {
			return nil, false
		}
		switch t := en.Type.(type) {
		case types.StringLit:
			parts = append(parts, t.V)
		case types.Int:
			if t.Lo != t.Hi {
				return nil, false
			}
			parts = append(parts, strconv.FormatInt(t.Lo, 10))
		default:
			return nil, false
		}
	}
	return parts, true
}

// formatHasText reports a printf format with literal text beside its
// conversion specifications: 'publish %s' has, '%s' has not.
func formatHasText(format string) bool {
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			return true
		}
		i++
		if i < len(format) && format[i] == '%' {
			return true // a literal percent sign
		}
		for i < len(format) && !((format[i] >= 'a' && format[i] <= 'z') || (format[i] >= 'A' && format[i] <= 'Z')) {
			i++
		}
	}
	return false
}

// mentionsConst reports a constant expression (`A | B`, parentheses) that
// names the constant, compared case-insensitively.
func mentionsConst(e ast.Expr, name string) bool {
	switch e := e.(type) {
	case *ast.Binary:
		return mentionsConst(e.Left, name) || mentionsConst(e.Right, name)
	case *ast.Paren:
		return mentionsConst(e.Expr, name)
	case *ast.ArrayLit:
		for _, it := range e.Items {
			if it != nil && it.Value != nil && mentionsConst(it.Value, name) {
				return true
			}
		}
		return false
	}
	n, ok := constName(e)
	return ok && n == name
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
				sig := x.sigOfCallable(m)
				sig.node = e
				out = append(out, x.checkCall(sig, e.Args, env, e.S, expected))
			} else {
				x.argsOnly(e.Args, env)
				out = append(out, types.TMixed)
			}
		case types.Class, types.Static, types.This, types.Intersection:
			handled := false
			for _, cl := range x.classesOf(m, e.Callee.Span()) {
				if inv := cl.FindMethod("__invoke"); inv != nil {
					sig := &signature{name: cl.Name + "::__invoke()", params: inv.Params, ret: inv.Return, typeParams: inv.TypeParams, recv: m, decl: inv.Class, node: e, meth: inv}
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
	defer x.unresolvedCall(e.S, e.Args)
	if key, ok := pathKey(e); ok {
		if t, ok := env.getPath(key); ok {
			return t // narrowed earlier (assertInstanceOf, instanceof, === null)
		}
	}
	if base, ok := pathKey(e.Object); ok {
		// A call may change what the receiver's other methods answer.
		env.invalidateCalls(base)
	}
	x.bindThis = x.closureCallThis(e, env)
	obj := x.expr(e.Object, env, nil)
	x.bindThis = nil
	if objects := objectMembers(obj); objects != nil && x.benevolentResult(e.Object, env) {
		obj = objects
	}
	if c, ok := obj.(types.Callable); ok && c.Closure && c.HasSig && c.Return != nil {
		// Closure::call() returns what the closure returns.
		if name, ok := identName(e.Name); ok && strings.EqualFold(name, "call") {
			x.methodCallOn(e, obj, env, expected)
			return c.Return
		}
	}
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

// objectMembers is the members of a union that are objects, nil when the
// union has no other members or no such member.
func objectMembers(t types.Type) types.Type {
	var objs []types.Type
	others := false
	for _, m := range types.Members(t) {
		switch m.(type) {
		case types.Class, types.Object, types.Static, types.This, types.Intersection:
			objs = append(objs, m)
		default:
			others = true
		}
	}
	if len(objs) == 0 || !others {
		return nil
	}
	return types.MakeUnion(objs...)
}

// benevolentResult reports a call whose result PHPStan reads as a
// benevolent union (stdlib.Benevolent): a method called on it is checked
// on the members that are objects, as `$redis->multi()->set(…)->del(…)`
// in a pipeline and `$doc->createElement('a')->setAttribute(…)` are.
func (x *fnCtx) benevolentResult(e ast.Expr, env *state) bool {
	for {
		p, ok := e.(*ast.Paren)
		if !ok {
			break
		}
		e = p.Expr
	}
	switch c := e.(type) {
	case *ast.Call:
		n, ok := c.Callee.(*ast.Name)
		return ok && stdlib.Benevolent(strings.ToLower(n.Parts[len(n.Parts)-1]))
	case *ast.MethodCall:
		name, ok := identName(c.Name)
		if !ok {
			return false
		}
		name = strings.ToLower(name)
		for _, cn := range types.ClassNames(x.pathType(c.Object, env)) {
			cl := x.p().Class(cn)
			if cl == nil {
				continue
			}
			// The map names the declaring class: Redis::set, DOMNode::appendChild.
			if m := cl.FindMethod(name); m != nil && stdlib.Benevolent(strings.ToLower(m.Class.Name)+"::"+name) {
				return true
			}
		}
	}
	return false
}

// unionCall checks a call on a union receiver as PHPStan does
// (UnionTypeMethodReflection): when every member declares the method with
// the same parameters, once, against the union of the members' parameter
// types, returning the union of their results. RepositoryInterface<User>
// |RepositoryInterface<object> so takes any object, as one of its members
// does. It checks nothing and reports false when the members' methods do
// not line up: a member without the method, a generic one, another
// parameter list, other exceptions, a @phpstan-self-out.
func (x *fnCtx) unionCall(e *ast.MethodCall, base types.Type, name string, env *state, expected types.Type) (types.Type, bool) {
	members := types.Members(base)
	if len(members) < 2 || e.Args != nil && e.Args.FirstClassCallable {
		return nil, false
	}
	type part struct {
		recv  types.Type
		meth  *program.Method
		subst map[string]types.Type
	}
	var parts []part
	for _, m := range members {
		ct, ok := m.(types.Class)
		if !ok {
			return nil, false
		}
		cl := x.p().Class(ct.Name)
		if cl == nil {
			return nil, false
		}
		meth := cl.FindMethod(name)
		if meth == nil || len(meth.TypeParams) > 0 || meth.SelfOut != nil || !x.canAccess(meth.Vis, meth.Class) {
			return nil, false
		}
		parts = append(parts, part{m, meth, x.memberSubst(m, meth.Class)})
	}
	first := parts[0].meth
	for _, p := range parts[1:] {
		sameThrows := p.meth.Throws == nil && first.Throws == nil || p.meth.Throws != nil && first.Throws != nil && types.Equal(p.meth.Throws, first.Throws)
		if len(p.meth.Params) != len(first.Params) || !sameThrows {
			return nil, false
		}
		for i, pp := range p.meth.Params {
			fp := first.Params[i]
			if !strings.EqualFold(pp.Name, fp.Name) || pp.ByRef != fp.ByRef || pp.Variadic != fp.Variadic || pp.HasDefault != fp.HasDefault {
				return nil, false
			}
		}
	}
	of := func(p part, t types.Type) types.Type {
		if t == nil {
			return types.TMixed
		}
		return replaceStatic(types.Substitute(t, p.subst), p.recv)
	}
	params := make([]*program.Param, len(first.Params))
	for i := range first.Params {
		var ts []types.Type
		for _, p := range parts {
			ts = append(ts, of(p, p.meth.Params[i].Type))
		}
		cp := *first.Params[i]
		cp.Type = types.MakeUnion(ts...)
		params[i] = &cp
	}
	var rets []types.Type
	void := true
	for _, p := range parts {
		rets = append(rets, of(p, p.meth.Return))
		if _, ok := p.meth.Return.(types.Void); !ok {
			void = false
		}
		x.c.libraryMethod(e.Name.Span(), p.meth)
		if p.meth.Deprecated && x.c.opts.Deprecations {
			x.report(e.Name.Span(), P4006, "method %s::%s() is deprecated%s", p.meth.Class.Name, name, p.meth.DeprecatedNote)
		}
	}
	sig := &signature{name: first.Class.Name + "::" + first.Name + "()", params: params, ret: types.MakeUnion(rets...), stub: isStubFile(first.Class.File),
		file: methodFile(first), decl: first.Class, node: e, meth: first}
	ret := x.checkCall(sig, e.Args, env, e.S, expected)
	if void {
		if base, ok := pathKey(e.Object); ok && !(base == "$this" && x.forgetOwnScope(env, first.Class)) {
			env.invalidateUnder(base)
		}
	}
	return ret, true
}

// methodCallOn checks the call on a receiver already typed.
func (x *fnCtx) methodCallOn(e *ast.MethodCall, obj types.Type, env *state, expected types.Type) types.Type {
	x.noteRecv(e, obj)
	name, ok := identName(e.Name)
	if !ok {
		x.noteName(e, x.expr(e.Name, env, nil))
		x.memberOnMixed(obj, e.Object.Span(), "cannot call a method on mixed")
		x.argsOnly(e.Args, env)
		return types.TMixed
	}
	base := types.StripNull(obj)
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
		if types.IsObjectLike(base) || types.IsMixed(base) {
			// A string or an array that may be null is reported once,
			// below, as the value it is.
			x.report(e.Object.Span(), P2105, "cannot call method %s() on %s (possibly null)", name, x.typeName(obj))
		}
	}
	if types.IsMixed(base) {
		x.memberOnMixed(base, e.Object.Span(), "cannot call method %s() on mixed", name)
		x.argsOnly(e.Args, env)
		if _, unknown := base.(types.Unknown); unknown {
			return base // a call on what was already reported stays reported
		}
		return types.TMixed
	}
	if !types.IsObjectLike(base) {
		if c, isCallable := base.(types.Callable); isCallable && c.Closure {
			// Closure methods: call, bindTo, …
			base = types.NewClass("Closure")
		} else {
			// A string, an array, a scalar: PHP's error (PHPStan's
			// method.nonObject), and the call's value is not known.
			recv := obj
			if e.NullSafe {
				recv = base
			}
			x.report(e.Object.Span(), P2105, "cannot call method %s() on %s", name, x.typeName(recv))
			x.argsOnly(e.Args, env)
			return types.TError
		}
	}
	var out []types.Type
	if t, ok := x.unionCall(e, base, name, env, expected); ok {
		out = append(out, t)
	} else {
		for _, m := range types.Members(base) {
			if _, isObj := m.(types.Object); isObj {
				// `object` declares no methods (PHPStan's method.notFound),
				// unless method_exists() said so.
				x.argsOnly(e.Args, env)
				if !provenMember(env, e.Object, true, name) {
					x.report(e.Name.Span(), P2006, "unknown method object::%s()", name)
					out = append(out, types.TError) // reported: the chain goes on silently
					continue
				}
				out = append(out, types.TMixed)
				continue
			}
			classes := x.classesOf(m, e.Object.Span())
			if len(classes) == 0 {
				x.argsOnly(e.Args, env)
				out = append(out, types.TMixed)
				continue
			}
			if _, inter := m.(types.Intersection); inter && len(classes) > 1 {
				// `A&B`: the method is found on any member; only when none has
				// it does every member report.
				var having []*program.Class
				for _, cl := range classes {
					if cl.FindMethod(name) != nil || cl.FindMethod("__call") != nil {
						having = append(having, cl)
					}
				}
				if len(having) > 0 {
					classes = having
				}
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
				x.c.libraryMethod(e.Name.Span(), meth)
				if meth.Deprecated && x.c.opts.Deprecations {
					x.report(e.Name.Span(), P4006, "method %s::%s() is deprecated%s", meth.Class.Name, name, meth.DeprecatedNote)
				}
				sig := &signature{name: meth.Class.Name + "::" + meth.Name + "()", params: meth.Params, ret: meth.Return, stub: isStubFile(meth.Class.File), file: methodFile(meth),
					typeParams: meth.TypeParams, recv: m, decl: meth.Class, node: e, meth: meth}
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
				if _, isVoid := meth.Return.(types.Void); isVoid {
					// A void method is called for what it does to its object:
					// what was known of the members under the receiver may be
					// gone (PHPStan forgets them as well); the receiver stands.
					// On $this, the class's static members may have changed too.
					if base, ok := pathKey(e.Object); ok && !(base == "$this" && x.forgetOwnScope(env, meth.Class)) {
						env.invalidateUnder(base)
					}
				}
				out = append(out, ret)
			}
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
	defer x.unresolvedCall(e.S, e.Args)
	name, ok := identName(e.Name)
	if !ok {
		x.noteName(e, x.expr(e.Name, env, nil))
		x.argsOnly(e.Args, env)
		return types.TMixed
	}
	var recv types.Type
	var classes []*program.Class
	viaParent, viaSelf := false, false
	if n, isName := e.Class.(*ast.Name); isName {
		raw := specialClassName(n)
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
		x.memberOnMixed(ct, e.Class.Span(), "cannot call static method %s() on mixed", name)
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
	if viaParent {
		x.noteRecv(e, types.Class{Name: classes[0].Name})
	} else {
		x.noteRecv(e, recv)
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
		x.c.libraryMethod(e.Name.Span(), meth)
		if meth.Deprecated && x.c.opts.Deprecations {
			x.report(e.Name.Span(), P4006, "method %s::%s() is deprecated%s", meth.Class.Name, name, meth.DeprecatedNote)
		}
		sig := &signature{name: meth.Class.Name + "::" + meth.Name + "()", params: meth.Params, ret: meth.Return, stub: isStubFile(meth.Class.File), file: methodFile(meth),
			typeParams: meth.TypeParams, recv: recv, decl: meth.Class, node: e, meth: meth}
		sig.explicit = x.explicitArgs(e.TypeArgs, meth.TypeParams, sig.name, e.S)
		if e.Args != nil && e.Args.FirstClassCallable {
			sig.subst = x.memberSubst(recv, meth.Class)
			out = append(out, x.callableOf(sig))
			continue
		}
		out = append(out, x.checkCall(sig, e.Args, env, e.S, expected))
		// A side-effecting method of the class's own hierarchy, called
		// statically (`self::flush()`, `parent::__construct()`), may change
		// the object and the class's static members.
		if _, isVoid := meth.Return.(types.Void); isVoid || (!meth.Static && strings.EqualFold(meth.Name, "__construct")) {
			x.forgetOwnScope(env, meth.Class)
		}
	}
	t := types.MakeUnion(out...)
	if _, isNever := t.(types.Never); isNever {
		env.dead = true
	}
	return t
}

// forgetOwnScope drops what was narrowed about $this and the static members
// of the current class's hierarchy after a side-effecting call to a method
// declared in decl, when the current class is decl or inherits from it, and
// reports whether it applied. As PHPStan does, it keeps a readonly
// property of $this and a private property declared in another class than
// decl, which the method cannot reach.
func (x *fnCtx) forgetOwnScope(env *state, decl *program.Class) bool {
	if x.class == nil || decl == nil || !x.class.IsSubclassOf(decl.Name) {
		return false
	}
	own := func(class string) bool {
		switch class {
		case "self", "static", "parent":
			return true
		}
		// A path spells the class as written, lowercased, without a
		// leading `\`: either reading may be the one meant.
		parts := strings.Split(class, "\\")
		kind := ast.NameQualified
		if len(parts) == 1 {
			kind = ast.NameUnqualified
		}
		for _, k := range []ast.NameKind{kind, ast.NameFullyQualified} {
			if name := x.res.ClassName(&ast.Name{Kind: k, Parts: parts}); name != "" && x.class.IsSubclassOf(name) {
				return true
			}
		}
		return false
	}
	keep := func(key string) bool {
		var name string
		instance := strings.HasPrefix(key, "$this->")
		if instance {
			name = key[len("$this->"):]
		} else if i := strings.Index(key, "::$"); i > 0 {
			name = key[i+len("::$"):]
		} else {
			return false
		}
		if strings.ContainsAny(name, "-[(:") {
			return false // a path under the member: dropped with it
		}
		pr := x.class.FindProperty(name)
		if pr == nil || pr.Static == instance {
			return false
		}
		return (instance && pr.Readonly) || (pr.Vis == program.Private && !strings.EqualFold(pr.Class.Name, decl.Name))
	}
	env.invalidateOwnScope(own, keep)
	return true
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

func (x *fnCtx) newExpr(e *ast.New, env *state, expected types.Type) types.Type {
	defer x.unresolvedCall(e.S, nil)
	if e.Anon != nil {
		cl := x.p().DeclareAnonymous(x.file, x.res.Scope(), e.Anon)
		x.c.checkClass(cl)
		args := e.Args
		if args == nil {
			args = e.Anon.Args // `new class (...) extends …`
		}
		if ctor := cl.FindMethod("__construct"); ctor != nil {
			sig := &signature{name: "class@anonymous::__construct()", params: ctor.Params, ret: types.TVoid, decl: ctor.Class, node: e, meth: ctor}
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
		if specialClassName(c) == "parent" && x.class != nil {
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
		if !viaString {
			x.markResolved(e.S) // a class without a constructor throws nothing
		}
		if e.Args != nil && len(e.Args.Args) > 0 && !(viaString && !cl.IsInstantiable()) {
			x.report(e.Args.S, P2102, "class %s has no constructor but %d arguments are given", cl.Name, len(e.Args.Args))
		}
		x.argsOnly(e.Args, env)
		return result
	}
	if !x.canAccess(ctor.Vis, ctor.Class) {
		x.report(e.S, P2108, "constructor of %s is %s", cl.Name, ctor.Vis)
	}
	sig := &signature{name: cl.Name + "::__construct()", params: ctor.Params, ret: types.TVoid, typeParams: ctor.TypeParams, recv: recv, decl: ctor.Class, file: methodFile(ctor), node: e, meth: ctor}
	// Infer the class's own type arguments from the constructor call.
	if c, ok := recv.(types.Class); ok && len(c.Args) == 0 && len(cl.TypeParams) > 0 {
		sig.typeParams = append(append([]*program.TypeParam{}, cl.TypeParams...), ctor.TypeParams...)
		sig.subst = map[string]types.Type{}
		x.checkCall(sig, e.Args, env, e.S, nil)
		args := make([]types.Type, len(cl.TypeParams))
		var want types.Class
		if exp, ok := expected.(types.Class); ok && strings.EqualFold(exp.Name, c.Name) && len(exp.Args) == len(cl.TypeParams) {
			want = exp // `$this->bag = new Bag()`: the property says what the bag holds
		}
		for i, tp := range cl.TypeParams {
			if t, ok := sig.subst[tp.Name]; ok {
				// Generalised, as PHPStan does: new ArrayObject(['a']) is
				// ArrayObject<int, string>, not ArrayObject<0, 'a'>.
				args[i] = widenType(t)
			} else if want.Name != "" {
				args[i] = want.Args[i]
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
	if x.class != nil && len(x.class.TypeParams) > 0 && namesClass(recv, x.class.Name) {
		// `new static($id)` inside the generic class: T is whatever the
		// runtime class makes of it, so an argument is held to T's bound.
		subst := x.memberSubst(recv, ctor.Class)
		if subst == nil {
			subst = map[string]types.Type{}
		}
		for _, tp := range x.class.TypeParams {
			if tp.Bound != nil {
				subst[tp.Name] = tp.Bound
			} else {
				subst[tp.Name] = types.TMixed
			}
		}
		sig.subst = subst
	}
	x.checkCall(sig, e.Args, env, e.S, nil)
	return result
}

// namesClass reports a static, $this or class type of the named class.
func namesClass(t types.Type, name string) bool {
	switch t := t.(type) {
	case types.Static:
		return t.Of != nil && strings.EqualFold(t.Of.Name, name)
	case types.This:
		return t.Of != nil && strings.EqualFold(t.Of.Name, name)
	case types.Class:
		return strings.EqualFold(t.Name, name)
	}
	return false
}

// ------------------------------------------------------------------ argument checking

// checkCall matches arguments to parameters, infers template arguments,
// checks types and returns the (substituted) return type.
func (x *fnCtx) checkCall(sig *signature, args *ast.ArgList, env *state, span ast.Span, expected types.Type) types.Type {
	ret := x.checkCallArgs(sig, args, env, span, expected)
	x.recordCall(sig, args, span, ret)
	return ret
}

func (x *fnCtx) checkCallArgs(sig *signature, args *ast.ArgList, env *state, span ast.Span, expected types.Type) types.Type {
	// A partial application binds its arguments now and leaves the
	// placeholders' parameters to the closure it creates.
	partial := args.Partial()
	var shape *pfaShape
	if partial {
		shape = x.partialShape(sig, args, span)
	}
	known := func(e ast.Expr) types.Type {
		if t, ok := sig.known[e]; ok {
			return t
		}
		return x.argKnown[e]
	}
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
	// foreign maps a type parameter the signature mentions but nobody owns
	// here (see collectForeign) to the fresh identity it gets for this call.
	foreign := map[string]types.Param{}
	foreignKey := func(p types.Param) string { return p.Name + "|" + p.Owner }
	renameForeign := func(t types.Type) types.Type {
		if len(foreign) == 0 {
			return t
		}
		return types.MapDeep(t, func(m types.Type) types.Type {
			if p, ok := m.(types.Param); ok {
				if f, ok := foreign[foreignKey(p)]; ok {
					return f
				}
			}
			return m
		})
	}
	paramType := func(p *program.Param) types.Type {
		t := types.Substitute(types.Substitute(p.Type, rename), subst)
		if len(foreign) > 0 {
			// A foreign parameter is bound under its fresh name: rename, then
			// read the binding.
			t = types.Substitute(renameForeign(t), freshOnly(subst))
		}
		if sig.recv != nil {
			t = replaceStatic(t, sig.recv)
		}
		return t
	}
	// Type parameters in the signature that belong to neither the callee
	// nor the current scope (a constructor's templates kept on a property,
	// an interface's templates on an unparameterised receiver) are inferred
	// here too, as PHPStan does.
	// Each gets a fresh identity, as the callee's own do, so that a `T` of
	// a constructor never collides with the caller's `T` by name.
	collectForeign := func(t types.Type) {
		types.Walk(t, func(m types.Type) {
			p, ok := m.(types.Param)
			if !ok || strings.Contains(p.Name, "\x00") || x.ownsParam(p) {
				return
			}
			if _, done := foreign[foreignKey(p)]; done {
				return
			}
			x.c.callSeq++
			fresh := types.Param{Name: fmt.Sprintf("%s\x00%d", p.Name, x.c.callSeq), Bound: p.Bound, Owner: p.Owner}
			foreign[foreignKey(p)] = fresh
			inferable[fresh.Name] = true
			tps = append(tps, &program.TypeParam{Name: fresh.Name, Bound: p.Bound})
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
			if a.Placeholder != nil {
				placeholder = true
				continue
			}
			if a.Unpack {
				unpacked = true
				if n := len(sig.params); n > 0 && sig.params[n-1].Variadic && a.Name == nil {
					// `f(...$xs)` into `X ...$values`: the elements are the
					// arguments, typed and unified as such (array_push).
					bindings = append(bindings, bound{a, sig.params[n-1], i})
					continue
				}
				t := x.expr(a.Value, env, nil)
				if _, _, ok := types.IterableKV(t, x.p()); (!ok && !types.IsMixed(t)) || mayNotIterate(t) {
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
				if !unpacked && !partial {
					x.report(a.S, P2102, "%s expects %d argument%s, %d given", sig.name, len(sig.params), plural(len(sig.params)), len(args.Args))
				}
				x.expr(a.Value, env, nil)
				continue
			}
			bindings = append(bindings, bound{a, p, i})
		}
	}
	if sig.stub {
		for _, b := range bindings {
			x.c.libraryParam(b.arg.S, sig.name, b.param.Name)
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
		if x.pipeLeft > 0 && !b.param.ByRef {
			if x.byValue == nil {
				x.byValue = map[*ast.Arg]bool{}
			}
			x.byValue[b.arg] = true
		}
		if isClosure(b.arg.Value) {
			continue
		}
		pt := paramType(b.param)
		var at types.Type
		if b.arg.Value != nil && b.arg.Value == x.pipeOperand {
			x.pipeParam, x.pipeSig = b.param, sig
		}
		if b.param.ByRef {
			x.noteByRef(b.arg.Value)
			if v, ok := b.arg.Value.(*ast.Variable); ok {
				if x.c.refVars == nil {
					x.c.refVars = map[*ast.Variable]bool{}
				}
				x.c.refVars[v] = true
			}
			if at = known(b.arg.Value); at == nil {
				at = x.exprLenient(b.arg.Value, env)
			}
			switch {
			case b.arg.Value == x.pipeOperand:
				x.report(b.arg.S, P2114, "parameter $%s of %s is by reference, and |> passes its left side by value: PHP throws an Error", b.param.Name, sig.name)
			case !isWritable(b.arg.Value):
				x.report(b.arg.S, P2112, "argument %d of %s is by reference and needs a variable", b.idx+1, sig.name)
			}
		} else {
			if at = known(b.arg.Value); at == nil {
				at = x.expr(b.arg.Value, env, pt)
			}
			if lit, ok := at.(types.StringLit); ok && expectsCallable(pt) {
				if c := x.namedCallable(lit.V); c != nil {
					at = c // 'strval' passed as a callback: its signature types the call
				}
			}
		}
		at = x.closeForeignParams(at)
		argTypes[i] = at
		if b.arg.Unpack {
			_, v, ok := types.IterableKV(at, x.p())
			if mayNotIterate(at) {
				// PHPStan's argument.unpackNonIterable; its elements are not
				// checked again against the parameter.
				x.report(b.arg.S, P2303, "cannot unpack %s", x.typeName(at))
				continue
			}
			if !ok {
				if !types.IsMixed(at) {
					x.report(b.arg.S, P2303, "cannot unpack %s", x.typeName(at))
				}
				continue
			}
			at = v
		}
		if len(inferable) > 0 && !isCallableType(at) {
			// Against the declared type, not the one substituted so far:
			// a second argument for `X ...$values` (array_push's '-e' then
			// a string) or `array<K, V> ...$arrays` widens the binding
			// instead of being checked against the first argument's.
			unify(types.Substitute(b.param.Type, rename), at, subst, inferable, x.p())
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
	// A parameter left to its default binds its template parameter to the
	// default's type: `get($id, B $behavior = self::EXCEPTION)` with
	// `@return (B is 1 ? object : ?object)` decides on the constant.
	if len(inferable) > 0 {
		given := map[*program.Param]bool{}
		for _, b := range bindings {
			given[b.param] = true
		}
		for _, p := range sig.params {
			if given[p] || !p.HasDefault || p.Default == nil {
				continue
			}
			if dt := x.defaultType(p.Default, sig); dt != nil {
				unify(paramType(p), dt, subst, inferable, x.p())
			}
		}
	}
	for i, b := range bindings {
		if !isClosure(b.arg.Value) {
			continue
		}
		pt := paramType(b.param)
		at := known(b.arg.Value)
		if at == nil {
			at = x.expr(b.arg.Value, env, pt)
		}
		argTypes[i] = at
		if len(inferable) > 0 {
			// Against the declared type, not the one substituted so far: a
			// closure returning int widens an A first bound to the literal 0.
			unify(types.Substitute(b.param.Type, rename), at, subst, inferable, x.p())
		}
	}
	// Infer from the expected type too (contextual typing of the return),
	// unless the enclosing call is still inferring it: its open parameter
	// says nothing yet, and binding ours to it would leave both open
	// (`$m->toDatabase($m->fromDatabase($doc, $class))`).
	if len(inferable) > 0 && expected != nil && sig.ret != nil && !mentionsFreshParam(expected) {
		unify(renameForeign(types.Substitute(types.Substitute(sig.ret, rename), subst)), expected, subst, inferable, x.p())
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
		if x.c.opts.CallableArg != nil && b.arg.Value != nil {
			x.c.opts.CallableArg(x.file, b.arg.Value, pt)
		}
		at := types.Substitute(argTypes[i], freshOnly(subst)) // generic callables as resolved
		if at == nil {
			continue
		}
		if b.arg.Unpack {
			_, v, ok := types.IterableKV(at, x.p())
			if !ok {
				continue // reported where the argument was typed
			}
			at = v
		}
		if _, isVoid := at.(types.Void); isVoid {
			if b.arg.Value == x.pipeOperand {
				x.report(b.arg.S, P2205, "the stage before |> returns void: %s receives null", sig.name)
			} else {
				x.report(b.arg.S, P2205, "result of a void function is used")
			}
			continue
		}
		if !types.IsAcceptable(at, pt, x.p()) {
			x.c.reportRelated(b.arg.S, P2103, related(sig.file, b.param.Span, "parameter $%s of %s declared here", b.param.Name, sig.name),
				"argument %d ($%s) of %s expects %s, %s given", b.idx+1, b.param.Name, sig.name, x.typeName(pt), x.typeName(at))
		}
	}
	for _, b := range bindings {
		// `render($event)` may change what $event->getResponse() answers.
		if key, ok := pathKey(b.arg.Value); ok {
			env.invalidateCalls(key)
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
		if !b.param.ByRef || b.arg.Value == x.pipeOperand {
			continue // a piped value is a temporary (P2114): nothing is written back
		}
		out := b.param.Out
		if out != nil {
			out = x.evalConditional(types.Substitute(types.Substitute(out, rename), subst), argByName)
		}
		if pm := pregMatchesType(sig.name, b.param.Name, args); pm != nil {
			out = pm
		}
		if out == nil {
			out = types.Substitute(types.Substitute(b.param.Type, rename), subst)
			defined := argTypes[i] != nil && !x.maybeUndefined(b.arg.Value, env)
			if sig.stub && defined {
				if x.c.opts.ByRef != nil {
					x.c.opts.ByRef(x.file, b.arg.Value, nil)
				}
				continue // php-src reads and writes the reference (sort, end, ksort): the type stays
			}
			if types.IsMixed(out) && defined && !types.IsMixed(argTypes[i]) {
				out = types.TMixed // user code's untyped reference: PHPStan says mixed afterwards
			}
			// A typed reference may now hold anything of its type: `$seen = null;
			// record($seen)` with `?Context &$seen` leaves ?Context, as PHPStan does.
		}
		x.assignTo(b.arg.Value, finalizeParams(out, tps), env, b.arg.S)
		if x.c.opts.ByRef != nil {
			x.c.opts.ByRef(x.file, b.arg.Value, finalizeParams(out, tps))
		}
		if v, ok := b.arg.Value.(*ast.Variable); ok {
			// What a by-reference parameter writes is known from a docblock
			// (@param-out, or the @param of an untyped reference): PHP
			// guarantees only the declared type of the reference, if any.
			native := types.TMixed
			if b.param.NativeType != nil {
				native = types.Erase(b.param.NativeType)
			}
			env.setNative(v.Name, native)
		}
	}
	// Property narrowings survive calls, as in PHPStan: `$this->x`
	// stays non-null across `$this->log()`.
	ret := sig.ret
	if ret == nil {
		ret = types.TMixed
	} else {
		ret = types.Substitute(types.Substitute(ret, rename), subst)
		ret = renameForeign(ret)
		ret = types.Substitute(ret, freshOnly(subst))
		ret = finalizeParams(ret, tps)
		if sig.recv != nil {
			ret = replaceStatic(ret, sig.recv)
		}
		ret = x.evalConditional(ret, argByName)
	}
	// The parameters as the arguments resolved them; a type parameter
	// nothing determined widens to its bound (closure types are not
	// generic).
	resolved := func(p *program.Param) types.Type {
		t := types.Substitute(paramType(p), freshOnly(subst))
		return finalizeParams(t, tps)
	}
	if x.pipeSig == sig && x.pipeParam != nil {
		x.pipeParamType = resolved(x.pipeParam)
	}
	if partial {
		if sig.node != nil && shape.ok {
			x.recordPartial(sig.node, args, sig, shape.slots, shape.rest, shape.named, false, resolved)
		}
		return partialClosure(shape, resolved, ret)
	}
	return ret
}

// evalConditional resolves conditional return types (`$x is Foo ? A : B`,
// `T is Foo ? A : B`) once the arguments and type parameters are known.
// An undecidable condition yields the union of both branches.
func (x *fnCtx) evalConditional(t types.Type, args map[string]types.Type) types.Type {
	return types.MapDeep(t, func(m types.Type) types.Type {
		c, ok := m.(types.Conditional)
		if !ok {
			return m
		}
		// The branches were mapped already (MapDeep works bottom-up): a
		// conditional inside a shape inside a list resolves as well.
		subject := c.Subject
		if c.ParamName != "" {
			subject = args[c.ParamName]
		}
		if subject == nil || isParamType(subject) || types.IsMixed(subject) {
			return types.MakeUnion(c.Then, c.Else)
		}
		yes := types.IsSubtype(subject, c.Target, x.p())
		no := !types.Overlaps(subject, c.Target, x.p())
		if c.Negated {
			yes, no = no, yes
		}
		switch {
		case yes:
			return c.Then
		case no:
			return c.Else
		}
		return types.MakeUnion(c.Then, c.Else)
	})
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
// fittingCallable picks, from a union made only of callable signatures,
// the first one a callable argument of known arity can serve; nil when the
// union or the argument is anything else.
func fittingCallable(u types.Union, arg types.Type) types.Type {
	ac, ok := arg.(types.Callable)
	if !ok || !ac.HasSig {
		return nil
	}
	required, accepts := 0, len(ac.Params)
	for _, p := range ac.Params {
		if p.Variadic {
			accepts = 1 << 30
		} else if !p.Optional {
			required++
		}
	}
	var first types.Type
	for _, m := range u.Types {
		c, ok := m.(types.Callable)
		if !ok {
			return nil
		}
		if !c.HasSig {
			continue
		}
		given := len(c.Params)
		if given >= required && given <= accepts && first == nil {
			first = c
		}
	}
	return first
}

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
		// `new \ReflectionClass($this)`: T is the class, not a `$this` the
		// callee's own static would be substituted for.
		switch a := arg.(type) {
		case types.This:
			if a.Of != nil {
				arg = *a.Of
			}
		case types.Static:
			if a.Of != nil {
				arg = *a.Of
			}
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
			if alt := fittingCallable(u, am); alt != nil {
				// `(callable(V): bool)|(callable(V, K): bool)|(callable(K): bool)`
				// against one closure: the first alternative its arity fits
				// binds, the others would bind the same parameter twice.
				unify(alt, am, subst, inferable, h)
				return
			}
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
				if len(a.Entries) == 0 && !a.Unsealed {
					// `[]` has no keys and no values: never, so that what a
					// later argument adds (array_push's X) is all there is.
					unify(p.Key, types.TNever, subst, inferable, h)
					unify(p.Value, types.TNever, subst, inferable, h)
					continue
				}
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

// pipe types `left |> stage` stage by stage, as PHP runs it
// (design/php-versions.md §5.4): a first-class callable or a partial
// application with one placeholder is the call PHP turns it into —
// `A |> f(B, ?, C)` is checked exactly as f(B, A, C), generic inference,
// closures in bound arguments and by-reference parameters included — and
// any other right side is called with the left side, ($stage)(A). The left
// side is typed once, before the stage.
func (x *fnCtx) pipe(e *ast.Binary, left types.Type, env *state) types.Type {
	savedOperand, savedParam, savedSig := x.pipeOperand, x.pipeParam, x.pipeSig
	x.pipeOperand, x.pipeParam, x.pipeSig = e.Left, nil, nil
	if x.argKnown == nil {
		x.argKnown = map[ast.Expr]types.Type{}
	}
	x.argKnown[e.Left] = left
	defer func() {
		delete(x.argKnown, e.Left)
		x.pipeOperand, x.pipeParam, x.pipeSig = savedOperand, savedParam, savedSig
	}()
	if call, args := pipeCall(e); call != nil {
		t := x.expr(call, env, nil)
		if args.Partial() && x.pipeParam != nil && x.pipeSig != nil {
			x.recordPipePartial(e.Right, args, x.pipeSig, x.pipeParam)
		}
		x.recordFold(e, args)
		return t
	}
	operand := &ast.Arg{S: e.Left.Span(), Value: e.Left}
	return x.dynamicCall(&ast.Call{S: e.S, Callee: e.Right, Args: &ast.ArgList{S: e.Left.Span(), Args: []*ast.Arg{operand}}}, env, nil)
}
