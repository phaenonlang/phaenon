package checker

import (
	"fmt"
	"math"
	"slices"
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
	// open marks the call of a closure that kept a generic function's type
	// parameters open (sigOfCallable): they are typeParams here.
	open bool
	// removed marks a call of a function Phaenon does not have, reported
	// as P1001 (extract()): that the variables it writes need a variable
	// to come from is part of the finding, not a second one (P2112). Its
	// other parameters and its return type are checked as any call's.
	removed bool
	// value marks the signature of a callable value's type (sigOfCallable):
	// its parameters' types are what the checker knows of the value, which
	// PHP does not check of it, and a parameter the type gives no name is
	// named by its position (unnamedParam).
	value bool
}

// magicSig is the callee of a partial application of a method that
// __call or __callStatic (magic) serves: PHP 8.6 makes the closure over
// its trampoline, `mixed ...$arguments`, so a placeholder stands for one
// argument of the list ($arguments0, …) and `...` for the rest. The
// closure returns mixed, as the call it makes is typed, and as PHPStan,
// which knows no method __call serves, types it.
func magicSig(e ast.Expr, magic *program.Method, recv types.Type, name string) *signature {
	arguments := &program.Param{Name: "arguments", Type: types.TMixed, NativeType: types.TMixed, Variadic: true}
	return &signature{name: magic.Class.Name + "::" + name + "()", params: []*program.Param{arguments}, ret: types.TMixed,
		file: methodFile(magic), recv: recv, decl: magic.Class, node: e, meth: magic}
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
	x.noteAtOnce(e)
	n, isName := e.Callee.(*ast.Name)
	if !isName {
		return x.dynamicCall(e, env, expected)
	}
	lower := strings.ToLower(n.Parts[len(n.Parts)-1])
	removed := false
	if len(n.Parts) == 1 {
		switch lower {
		case "compact":
			removed = x.removed(e.S, "compact()", "an explicit array literal")
		case "extract":
			removed = x.removed(e.S, "extract()", "explicit assignments")
		case "get_defined_vars":
			removed = x.removed(e.S, "get_defined_vars()", "explicit variables")
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
	sig.removed = removed && fn.File.IsStub()
	sig.explicit = x.explicitArgs(e.TypeArgs, fn.TypeParams, fn.Name+"()", e.S)
	if e.Args != nil && e.Args.FirstClassCallable {
		return x.firstClassCallable(sig)
	}
	if lower == "array_map" && e.Args != nil && len(e.Args.Args) > 2 {
		x.arrayMapOverSeveral(sig, e.Args, env)
	}
	ret := x.checkCall(sig, e.Args, env, e.S, expected)
	if _, isNever := ret.(types.Never); isNever {
		env.dead = true
	}
	if sig.stub && strings.EqualFold(fn.Name, "extract") {
		env.forgetConds() // it may write any variable
	}
	if lower == "array_push" || lower == "array_unshift" {
		x.arrayPush(e, env)
	}
	if e.Args.Partial() {
		// A partial application is the closure checkCall built: what a call
		// returns, refined from its arguments, is not what creating the
		// closure does (sprintf's format, max's operands).
		return ret
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

// defaultType is the type of a parameter's default value as far as it is
// read here: a literal, a constant, named as the callee's own namespace
// names it (valueWhereDeclared), a class constant of the declaring class
// (`self::X`) or of a fully qualified class.
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
		if f, scope, cl := sig.declaredAt(); f != nil {
			return x.c.valueWhereDeclared(f, scope, cl, e, x)
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

// declaredAt is where the callee is declared, whose parameters' defaults
// are read there: its file, namespace and class, a trait's file and
// namespace for a method copied from one; no file where the callee is not
// known (a closure type's).
func (sig *signature) declaredAt() (*program.File, *program.Scope, *program.Class) {
	switch {
	case sig.fn != nil:
		return sig.fn.File, sig.fn.Scope, nil
	case sig.meth != nil:
		at := sig.meth.Class
		if sig.meth.FromTrait != nil {
			at = sig.meth.FromTrait
		}
		return at.File, at.Scope, sig.meth.Class
	}
	return nil, nil, nil
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
	// The overlay's @param-out wrote the array already, with the values
	// as given, and held a parameter documented @param-out to its tag
	// there: this retyping is no second write of it (paramout.go).
	unchecked := x.outUnchecked
	x.outUnchecked = true
	x.assignTo(target, result, env, e.S)
	x.outUnchecked = unchecked
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
	case "array_replace":
		// Lists replace each other's entries number by number, and what is
		// left is a list, as PHPStan has it; the stub's array<K, V> would
		// not say so. A list that cannot be empty makes the result one.
		var values []types.Type
		nonEmpty := false
		for i, a := range args.Args {
			t := argType(i)
			if a.Unpack {
				_, t, _ = types.IterableKV(t, x.p())
			}
			for _, m := range types.Members(t) {
				if !isList(m) {
					return ret
				}
			}
			_, v, ok := types.IterableKV(t, x.p())
			if !ok {
				return ret
			}
			values = append(values, v)
			nonEmpty = nonEmpty || !a.Unpack && certainlyHolds(t)
		}
		if len(values) > 0 {
			return types.Array{Key: types.TInt, Value: types.MakeUnion(values...), List: true, NonEmpty: nonEmpty}
		}
	case "array_merge":
		if len(args.Args) == 0 {
			return ret // the stub's array: PHPStan types no merge of nothing
		}
		if t, ok := mergedShapes(args, argType); ok {
			return t
		}
		// PHP numbers integer keys anew: the result is a list where every
		// array merged has integer keys alone, as PHPStan has it, and is
		// none where one may have a string key. The stub's conditional,
		// unsure of either, gives both.
		intKeys, nonEmpty := true, false
		var values []types.Type
		for i, a := range args.Args {
			t := argType(i)
			if a.Unpack {
				_, t, _ = types.IterableKV(t, x.p())
			}
			k, v, ok := types.IterableKV(t, x.p())
			if !ok {
				return ret
			}
			intKeys = intKeys && types.IsSubtype(k, types.TInt, x.p())
			nonEmpty = nonEmpty || !a.Unpack && certainlyHolds(t)
			values = append(values, v)
		}
		if intKeys {
			return types.Array{Key: types.TInt, Value: types.MakeUnion(values...), List: true, NonEmpty: nonEmpty}
		}
		return types.Map(ret, func(m types.Type) types.Type {
			a, ok := m.(types.Array)
			switch {
			case !ok:
				return m
			case a.List:
				return types.TNever
			}
			a.NonEmpty = a.NonEmpty || nonEmpty
			return a
		})
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
	// What the callback proved of the element, `isset($r['name'])`
	// included, is in its type: the key is there, as PHPStan has it.
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

// omittedOnly are the parameters of PHP's functions and methods whose
// null default stands for the argument left out, and which PHPStan's
// function map holds to their type without null when the argument is
// given (argument.type, PHPStan 2.3, every call form it reads):
// implode()'s $array after a separator, strtr()'s $to, and the others
// below. typeError says PHP itself throws a TypeError for an explicit null
// there, as it does for implode(',', null) and strtr($s, 'ab', null);
// phpversion(null), strtok($s, null), PDO::query($q, null) and new
// ReflectionMethod('C::m', null) run, and stream_context_set_option() and
// XSLTProcessor::setParameter() throw a ValueError.
var omittedOnly = map[string]struct {
	param     string
	typeError bool
}{
	"implode":                       {"array", true},
	"join":                          {"array", true},
	"strtr":                         {"to", true},
	"phpversion":                    {"extension", false},
	"strtok":                        {"token", false},
	"stream_context_set_option":     {"option_name", false},
	"pdo::query":                    {"fetchMode", false},
	"xsltprocessor::setparameter":   {"value", false},
	"reflectionmethod::__construct": {"method", false},
}

// omittedNull reports a parameter of omittedOnly: an argument given there
// is held to the parameter's type without null. typeError says PHP throws
// a TypeError for the null.
func omittedNull(sig *signature, p *program.Param) (ok, typeError bool) {
	var key string
	switch {
	case sig == nil:
		return false, false
	case sig.fn != nil && isStubFile(sig.fn.File):
		key = strings.ToLower(sig.fn.Name)
	case sig.meth != nil && sig.meth.Class != nil && isStubFile(sig.meth.Class.File):
		key = strings.ToLower(sig.meth.Class.Name + "::" + sig.meth.Name)
	default:
		return false, false
	}
	o, found := omittedOnly[key]
	if !found || o.param != p.Name {
		return false, false
	}
	return true, o.typeError
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
		x.unknownCallee(e)
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
				x.unknownCallee(e)
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
					x.unknownCallee(e)
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
			x.unknownCallee(e)
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

// unknownCallee notes a call through a value whose type gives no parameter
// list (Checker.unknownCallee).
func (x *fnCtx) unknownCallee(e *ast.Call) {
	if x.c.unknownCallee == nil {
		x.c.unknownCallee = map[*ast.Call]bool{}
	}
	x.c.unknownCallee[e] = true
}

// sigOfCallable turns a callable type into a signature. The type
// parameters a generic function's first-class callable kept open
// (firstClassCallable) are the signature's: a call of the closure infers
// them from its arguments, `$h = pick(...); $h(1, 'a')`, as a call of the
// function does.
func (x *fnCtx) sigOfCallable(c types.Callable) *signature {
	sig := &signature{name: "callable", ret: c.Return, value: true}
	seen := map[string]bool{}
	types.Walk(c, func(m types.Type) {
		if p, ok := m.(types.Param); ok && strings.Contains(p.Name, "\x00") && !seen[p.Name] {
			seen[p.Name] = true
			sig.typeParams = append(sig.typeParams, &program.TypeParam{Name: p.Name, Bound: p.Bound})
			sig.open = true
		}
	})
	for i, p := range c.Params {
		name := p.Name
		if name == "" {
			name = fmt.Sprintf("%d", i+1)
		}
		sig.params = append(sig.params, &program.Param{Name: name, Type: p.Type, HasDefault: p.Optional, Variadic: p.Variadic, ByRef: p.ByRef})
	}
	return sig
}

// unnamedParam reports the name sigOfCallable gives a parameter its
// callable type does not name, its position (a variadic placeholder's
// offset after it): no PHP name is all digits.
func unnamedParam(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// callableOf builds the Closure type of a callable named by a string
// ('strval', 'Foo::bar'), a generic one with its type parameters open.
func (x *fnCtx) callableOf(sig *signature) types.Type {
	c, _ := x.openCallable(sig)
	return c
}

// openCallable builds the Closure type of sig with a generic callee's type
// parameters open, under fresh identities that the inference of a call it
// is passed to resolves (unify), and returns them.
func (x *fnCtx) openCallable(sig *signature) (types.Callable, []*program.TypeParam) {
	rename, tps := x.freshParams(sig.typeParams)
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
	return c, tps
}

// argsOnly types the arguments of a call that cannot be checked further.
func (x *fnCtx) argsOnly(args *ast.ArgList, env *state) {
	if args == nil {
		return
	}
	for _, a := range args.Args {
		if a.Value != nil {
			x.expr(a.Value, env, nil)
			x.mayBeReferenced(a.Value) // a callee the checker cannot see may take it by reference
		}
	}
}

// mayBeReferenced records the variable e is, or holds an offset of, as one
// a reference may have been taken to where the checker does not follow
// the writes through it (refTaken).
func (x *fnCtx) mayBeReferenced(e ast.Expr) {
	for {
		switch n := e.(type) {
		case *ast.Paren:
			e = n.Expr
			continue
		case *ast.Index:
			e = n.Array
			continue
		case *ast.Variable:
			if x.refTaken == nil {
				x.refTaken = map[string]bool{}
			}
			x.refTaken[n.Name] = true
		}
		return
	}
}

// ------------------------------------------------------------------ method calls

func (x *fnCtx) methodCall(e *ast.MethodCall, env *state, expected types.Type) types.Type {
	defer x.unresolvedCall(e.S, e.Args)
	delete(x.skippedChain, e) // a mark of an earlier pass over a loop: this one decides
	if key, ok := x.pathKey(e); ok {
		if t, ok := env.getPath(key); ok {
			return t // narrowed earlier (assertInstanceOf, instanceof, === null)
		}
	}
	if base, ok := x.pathKey(e.Object); ok {
		// A call may change what the receiver's other methods answer.
		env.invalidateCalls(base)
	}
	x.bindThis = x.closureCallThis(e, env)
	obj := x.expr(e.Object, env, nil)
	x.bindThis = nil
	if x.skippedChain[e.Object] {
		// `$n?->a()->b()` with $n null: reported at the ?->.
		x.argsOnly(e.Args, env)
		return x.skipChain(e)
	}
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
		if base, ok := x.pathKey(e.Object); ok && !(base == "$this" && x.forgetOwnScope(env, first.Class)) {
			env.invalidateUnder(base)
		}
	}
	return ret, true
}

// methodCallOn checks the call on a receiver already typed.
func (x *fnCtx) methodCallOn(e *ast.MethodCall, obj types.Type, env *state, expected types.Type) types.Type {
	x.noteRecv(e, obj)
	if e.Args.Partial() {
		if x.partialRecv == nil {
			x.partialRecv = map[*ast.MethodCall]types.Type{}
		}
		x.partialRecv[e] = obj
	}
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
		if _, isNull := obj.(types.Null); isNull {
			// `null?->m()` is null, what PHP evaluates it to, and the
			// rest of the chain is skipped; PHPStan reports the call
			// (method.nonObject) and types it null. Typed as an error, it
			// would reach the head of a loop whose variable is null on
			// its first pass, `$total = $total?->plus($x) ?? $x`, and
			// read as mixed on the pass whose findings stand (fixpoint).
			x.report(e.Object.Span(), P2105, "cannot call method %s() on null", name)
			x.argsOnly(e.Args, env)
			return x.skipChain(e)
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
	if _, never := base.(types.Never); never {
		// A receiver that is never anything: the code is not reached
		// (propertyType).
		x.argsOnly(e.Args, env)
		return types.TMixed
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
					if magic := cl.FindMethod("__call"); magic != nil {
						if e.Args.Partial() {
							out = append(out, x.checkCall(magicSig(e, magic, m, name), e.Args, env, e.S, expected))
							continue
						}
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
					out = append(out, x.firstClassCallable(sig))
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
					if base, ok := x.pathKey(e.Object); ok && !(base == "$this" && x.forgetOwnScope(env, meth.Class)) {
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
	if x.lateStaticRefused(e) {
		return types.TError
	}
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
				x.reportRefused(x.scopeKnown(), n.S, P2012, "parent used outside a class")
				x.argsOnly(e.Args, env)
				return types.TMixed
			}
			pc := x.class.ParentClass()
			if pc == nil {
				x.reportRefused(x.scopeKnown(), n.S, P2011, "class %s has no parent", x.class.Name)
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
				x.reportRefused(x.scopeKnown(), n.S, P2012, "%s used outside a class", raw)
				x.argsOnly(e.Args, env)
				return types.TMixed
			}
			classes = []*program.Class{x.class}
			viaSelf = true
			// self:: forwards the called class as static:: and parent:: do
			// (PHP's late static binding), in a static method too: the
			// `static` of what it calls is the caller's own.
			if x.static {
				recv = types.Static{Of: &types.Class{Name: x.class.Name, Args: selfArgs(x.class)}}
			} else {
				recv = x.thisType
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
			magic := cl.FindMethod("__callStatic")
			if !x.static && (viaParent || viaSelf) && cl.FindMethod("__call") != nil {
				magic = cl.FindMethod("__call") // PHP calls __call from an object of the class
			}
			if magic != nil {
				if e.Args.Partial() {
					out = append(out, x.checkCall(magicSig(e, magic, recv, name), e.Args, env, e.S, expected))
					continue
				}
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
			out = append(out, x.firstClassCallable(sig))
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

// sideEffects reports a callee that may change an object it is given, as
// PHPStan's hasSideEffects() answers yes and its ArgumentsHandler then
// forgets what the scope held under each argument that may be an object:
//
//   - a function or a method declared, or documented, to return void (a
//     constructor never is one: `new Wrap($o)` forgets nothing, nor does
//     `parent::__construct($o)`), `@phpstan-pure` or not;
//   - one tagged `@phpstan-impure`;
//   - a function or method of PHP's PHPStan lists with side effects
//     though it returns a value (stdlib.HasSideEffects): get_object_vars(),
//     array_push();
//
// and not a method that returns $this or static, which PHPStan takes for
// a fluent setter that leaves its arguments as they were, unless it is
// tagged impure. A closure or a callable held in a variable, a first-class
// callable included, has no reflection to PHPStan and forgets nothing.
func (x *fnCtx) sideEffects(sig *signature) bool {
	var ret types.Type
	var doc *docblock.Doc
	var name string
	switch {
	case sig.fn != nil:
		ret, doc, name = sig.fn.Return, sig.fn.Doc, strings.ToLower(sig.fn.Name)
	case sig.meth != nil:
		m := sig.meth
		if strings.EqualFold(m.Name, "__construct") {
			return m.Doc != nil && m.Doc.Impure
		}
		ret, doc = m.Return, m.Doc
		if m.Class != nil {
			name = strings.ToLower(m.Class.Name) + "::" + strings.ToLower(m.Name)
		}
		switch ret.(type) {
		case types.This, types.Static:
			return doc != nil && doc.Impure // a fluent setter
		}
	default:
		return false
	}
	if doc != nil && doc.Impure {
		return true
	}
	if _, void := ret.(types.Void); void {
		return true
	}
	return sig.stub && stdlib.HasSideEffects(name)
}

// forgetArgument drops what the flow knew under the argument key, of type
// t, after a call that may have changed it (sideEffects), as PHPStan's
// invalidateExpression() of the argument forgets every expression that
// holds it: its properties, the paths under them, its offsets and method
// calls, and where it is a variable, the paths that pass it or index by
// it. The argument itself keeps its type, and a readonly property of it
// what was known of it. `$this` given takes the static members of the
// class's own hierarchy with it, as PHPStan reads `$this` as self and
// static there.
func (x *fnCtx) forgetArgument(env *state, key string, t types.Type) {
	readonly := func(k string) bool {
		name := k[len(key)+len("->"):]
		if !strings.HasPrefix(k[len(key):], "->") || strings.ContainsAny(name, "-[(:") {
			return false
		}
		for _, cn := range types.ClassNames(t) {
			if cl := x.p().Class(cn); cl != nil {
				if pr := cl.FindProperty(name); pr != nil && pr.Readonly {
					return true
				}
			}
		}
		return false
	}
	env.invalidate(func(k string) bool {
		if key == "$this" {
			for _, own := range []string{"self::", "static::", "parent::"} {
				if strings.HasPrefix(strings.ToLower(k), own) {
					return true
				}
			}
		}
		return pathUnder(k, key) && !readonly(k)
	})
	if strings.HasPrefix(key, "$") && !strings.ContainsAny(key[1:], "-[(:$") {
		env.invalidateMentions(key[1:])
	}
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

// traitUserProperties finds a property a trait body uses on $this among the
// classes using the trait: the users' declarations, the users that lack it
// (they neither declare it nor allow dynamic properties), and whether a
// user serves it dynamically (__get, stdClass). Unlike a method, where the
// first user decides, every user counts: PHPStan analyses the body once per
// user, so a read is typed by each user's declaration and a write must suit
// each. A user that is itself a trait passes the question on to its users.
func (x *fnCtx) traitUserProperties(trait *program.Class, name string) (found []*program.Property, missing []*program.Class, dynamic bool) {
	seen := map[string]bool{}
	var walk func(t *program.Class)
	walk = func(t *program.Class) {
		if seen[strings.ToLower(t.Name)] {
			return
		}
		seen[strings.ToLower(t.Name)] = true
		for _, u := range x.p().TraitUsers(t.Name) {
			u.Signature()
			if pr := u.FindProperty(name); pr != nil {
				found = append(found, pr)
				continue
			}
			if u.Kind == ast.KindTrait {
				walk(u)
				continue
			}
			if allowsDynamic(u) {
				dynamic = true
			} else {
				missing = append(missing, u)
			}
		}
	}
	walk(trait)
	return found, missing, dynamic
}

// ------------------------------------------------------------------ new

func (x *fnCtx) newExpr(e *ast.New, env *state, expected types.Type) types.Type {
	if x.lateStaticRefused(e) {
		x.argsOnly(e.Args, env)
		return types.TError
	}
	defer x.unresolvedCall(e.S, nil)
	if e.Anon != nil {
		cl := x.p().DeclareAnonymous(x.file, x.res.Scope(), e.Anon)
		x.c.checkClass(cl)
		args := e.Args
		if args == nil {
			args = e.Anon.Args // `new class (...) extends …`
		}
		if ctor := cl.FindMethod("__construct"); ctor != nil {
			// `new class` calls the constructor from where it stands, as
			// `new Foo` does: its own private one is no more callable from
			// there than an inherited protected one.
			if !x.canAccess(ctor.Vis, ctor.Class) {
				if ctor.Class == cl {
					x.report(e.S, P2108, "constructor of class@anonymous is %s", ctor.Vis)
				} else {
					x.report(e.S, P2108, "constructor of class@anonymous, %s::__construct(), is %s", ctor.Class.Name, ctor.Vis)
				}
			}
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
				x.reportRefused(x.scopeKnown(), c.S, P2011, "class %s has no parent", x.class.Name)
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
					full[i] = types.Substitute(tp.Bound, argsSoFar(cl.TypeParams, full[:i]))
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
		inferFromBounds(cl.TypeParams, sig.subst, x.p())
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
				args[i] = types.Substitute(tp.Bound, argsSoFar(cl.TypeParams, args[:i]))
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

// inferFromBounds gives a class's type parameters that the constructor's
// arguments left unbound what the bound of a bound one says of them, as
// PHPStan infers them: `new LimitIterator($rows)`, $rows an
// ArrayIterator<int, User>, binds TIterator, and its bound
// Iterator<TKey, TValue> then binds TKey to int and TValue to User.
func inferFromBounds(tps []*program.TypeParam, subst map[string]types.Type, h types.Hierarchy) {
	open := map[string]bool{}
	for _, tp := range tps {
		if _, ok := subst[tp.Name]; !ok {
			open[tp.Name] = true
		}
	}
	if len(open) == 0 {
		return
	}
	for _, tp := range tps {
		if v, ok := subst[tp.Name]; ok && !open[tp.Name] && tp.Bound != nil {
			unify(tp.Bound, v, subst, open, h)
		}
	}
}

// argsSoFar maps the type parameters before a missing argument to the
// arguments they took, for a bound that names them.
func argsSoFar(tps []*program.TypeParam, args []types.Type) map[string]types.Type {
	m := make(map[string]types.Type, len(args))
	for i, a := range args {
		m[tps[i].Name] = a
	}
	return m
}

// mentionsTemplate reports whether a type names the template parameter.
func mentionsTemplate(t types.Type, name string) bool {
	found := false
	types.Walk(t, func(n types.Type) {
		if p, ok := n.(types.Param); ok && p.Name == name {
			found = true
		}
	})
	return found
}

// openToBounds replaces in a bound the call's own type parameters that
// its arguments left unbound by their own bounds, in turn, and by mixed
// where they have none: `new IteratorIterator($nodes)` binds TIterator
// before TKey and TValue, which its bound Traversable<TKey, TValue>
// names, and TIterator is held to what their bounds say of them, no more.
func openToBounds(bound types.Type, tps []*program.TypeParam, subst map[string]types.Type) types.Type {
	open := map[string]*program.TypeParam{}
	for _, tp := range tps {
		if _, ok := subst[tp.Name]; !ok {
			open[tp.Name] = tp
		}
	}
	if len(open) == 0 {
		return bound
	}
	var close func(t types.Type, depth int) types.Type
	close = func(t types.Type, depth int) types.Type {
		return types.MapDeep(t, func(n types.Type) types.Type {
			p, ok := n.(types.Param)
			if !ok {
				return n
			}
			tp, isOpen := open[p.Name]
			if !isOpen {
				return n
			}
			if tp.Bound == nil || depth > 8 {
				return types.TMixed
			}
			return close(types.Substitute(tp.Bound, subst), depth+1)
		})
	}
	return close(bound, 0)
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

// overwrite is a named argument, or a string key of an unpacked array, that
// names a parameter an argument or a placeholder before it already fills.
type overwrite struct {
	arg, prev *ast.Arg
	name      string
}

// findOverwrites lays a call's arguments over the callee's parameters in
// order and returns each name that overwrites: PHP's Error "Named
// parameter $x overwrites previous argument" (or "previous placeholder"),
// thrown when the call is made or when a partial application creates its
// closure, a pipe stage's `A |> f(?, x: B)` included. A positional
// argument fills the parameter at its position; one past the declared
// parameters goes into the variadic parameter under a number, which no
// name meets. A variadic function collects a name it does not declare
// under that name, so the same name twice collides. A name no parameter
// has is P2010 of its own (unknown named argument), and a named
// placeholder stands only for a declared parameter (partialShape), so
// neither is counted here.
//
// unpacked gives the entries of an unpacked argument, in order, when its
// type says them all: an integer key fills the next position and a string
// key a name, as PHP unpacks them. An unpacked list it cannot say fills
// positions nobody knows, so no later position is counted; the names
// before it still are.
func findOverwrites(sig *signature, args *ast.ArgList, unpacked func(*ast.Arg) ([]types.ShapeEntry, bool)) []overwrite {
	if args == nil {
		return nil
	}
	params, variadic := sig.params, false
	if n := len(params); n > 0 && params[n-1].Variadic {
		params, variadic = params[:n-1], true
	}
	declared := func(name string) bool {
		for _, p := range params {
			if p.Name == name {
				return true
			}
		}
		return false
	}
	filled := map[string]*ast.Arg{} // a parameter's name → the argument that fills it
	var found []overwrite
	position, lost := 0, false
	fillPosition := func(a *ast.Arg) {
		// A positional argument after a named one is P2110, not an
		// overwrite: the first to fill a parameter keeps it.
		if !lost && position < len(params) && filled[params[position].Name] == nil {
			filled[params[position].Name] = a
		}
		position++
	}
	fillName := func(a *ast.Arg, name string) {
		if !declared(name) && (!variadic || a.Placeholder != nil) {
			return
		}
		if prev := filled[name]; prev != nil {
			found = append(found, overwrite{arg: a, prev: prev, name: name})
			return
		}
		filled[name] = a
	}
	for _, a := range args.Args {
		switch {
		case a.Placeholder != nil && a.Placeholder.Variadic:
			// `...` comes last and stands for what is left.
		case a.Unpack:
			var entries []types.ShapeEntry
			ok := false
			if unpacked != nil {
				entries, ok = unpacked(a)
			}
			if !ok {
				lost = true
				continue
			}
			for _, e := range entries {
				if e.IsInt {
					fillPosition(a)
				} else {
					fillName(a, e.Key)
				}
			}
		case a.Name == nil:
			fillPosition(a)
		default:
			fillName(a, a.Name.Name)
		}
	}
	return found
}

// shapeEntries is what unpacking a value of type t passes, when the type
// says it entry by entry: a sealed array shape with no optional entry.
func shapeEntries(t types.Type) ([]types.ShapeEntry, bool) {
	s, ok := t.(types.Shape)
	if !ok || s.Unsealed {
		return nil, false
	}
	for _, e := range s.Entries {
		if e.Optional {
			return nil, false
		}
	}
	return s.Entries, true
}

// reportOverwrites reports what findOverwrites found as P2010, the code a
// name no parameter has gets too (design/php-versions.md §9, "Overwriting
// names"), at the overwriting argument. In a pipe stage pipeCall gives a named placeholder
// the left side as its value and its span: the finding goes to the name
// the source wrote, as the placeholder it is.
func (x *fnCtx) reportOverwrites(sig *signature, found []overwrite) {
	for _, o := range found {
		span, kind, before := o.arg.S, "argument", "argument"
		switch {
		case o.arg.Placeholder != nil:
			kind = "placeholder"
		case o.arg.Name != nil && o.arg.Value != nil && o.arg.Value == x.pipeOperand:
			span, kind = o.arg.Name.S, "placeholder"
		}
		if o.prev.Placeholder != nil {
			before = "placeholder"
		}
		x.report(span, P2010, "named %s $%s of %s overwrites the %s before it", kind, o.name, sig.name, before)
	}
}

// mergedShapes types array_merge() of shapes as PHPStan does: each
// argument that is a union of shapes, `array{b: 2}|array{}`, collapses into
// one whose keys are optional where a shape lacks them, and the arguments
// merge entry by entry into one shape. One unpacked from a tuple of
// shapes, `...$parts` with `$parts` a `list{array{a: 1}, array{b: 2}}` (or
// a union of such), merges its shapes in order, each tuple's result
// collapsing into one. False where an argument is no shape, an unpacked
// one is no tuple of shapes, or an integer key is optional, which leaves
// the keys after it unnumbered.
func mergedShapes(args *ast.ArgList, argType func(int) types.Type) (types.Type, bool) {
	if args == nil || len(args.Args) == 0 {
		return nil, false
	}
	acc := types.Shape{}
	for i, a := range args.Args {
		var members []types.Shape
		for _, m := range types.Members(argType(i)) {
			sh, ok := m.(types.Shape)
			if !ok {
				return nil, false
			}
			members = append(members, sh)
		}
		if !a.Unpack {
			merged, ok := mergeShapes([]types.Shape{acc, collapseShapes(members)})
			if !ok {
				return nil, false
			}
			acc = merged
			continue
		}
		// An unpacked tuple of shapes is its shapes, in order.
		var results []types.Shape
		for _, tuple := range members {
			if tuple.Unsealed {
				return nil, false
			}
			seq := []types.Shape{acc}
			for j, e := range tuple.Entries {
				inner, isShape := e.Type.(types.Shape)
				if !isShape || e.Optional || !e.IsInt || e.IntKey != int64(j) {
					return nil, false
				}
				seq = append(seq, inner)
			}
			merged, ok := mergeShapes(seq)
			if !ok {
				return nil, false
			}
			results = append(results, merged)
		}
		acc = collapseShapes(results)
	}
	return acc, true
}

// certainlyHolds reports an array type every value of which has an entry:
// a non-empty array, a shape with a required entry.
func certainlyHolds(t types.Type) bool {
	members := types.Members(t)
	for _, m := range members {
		switch m := m.(type) {
		case types.Array:
			if !m.NonEmpty {
				return false
			}
		case types.Shape:
			required := false
			for _, e := range m.Entries {
				required = required || !e.Optional
			}
			if !required {
				return false
			}
		default:
			return false
		}
	}
	return len(members) > 0
}

// mergeShapes is array_merge() of shapes: integer keys numbered anew, a
// later string key replacing an earlier one, which stays where the later
// one is optional; unsealed where one of them is. An optional integer
// entry leaves the number of the next one open, as PHPStan's builder
// keeps it: the next value goes under each number it may have, optional
// unless it is certain (`[1, 2]|[3]` then `[4]|[]` is `list{0: 1|3, 1?:
// 2|4, 2?: 4}`). False where an unsealed shape has integer keys, whose
// further ones shift the numbers.
func mergeShapes(shapes []types.Shape) (types.Shape, bool) {
	unsealed, ints := false, false
	for _, s := range shapes {
		unsealed = unsealed || s.Unsealed
		for _, en := range s.Entries {
			ints = ints || en.IsInt
		}
	}
	if unsealed && ints {
		return types.Shape{}, false
	}
	merged := types.Shape{}
	at := map[string]int{}      // string key → entry
	byNumber := map[int64]int{} // integer key → entry
	next := []int64{0}          // the numbers the next integer entry may have
	for _, s := range shapes {
		for _, en := range s.Entries {
			if en.IsInt {
				last := next[len(next)-1]
				var after []int64
				if en.Optional {
					after = append(after, next...)
				}
				for j, n := range next[:len(next)-1] {
					i := byNumber[n]
					merged.Entries[i].Type = types.MakeUnion(merged.Entries[i].Type, en.Type)
					if j == 0 && !en.Optional {
						// A certain value fills the first open number.
						merged.Entries[i].Optional = false
					}
					after = append(after, n+1)
				}
				byNumber[last] = len(merged.Entries)
				merged.Entries = append(merged.Entries, types.ShapeEntry{IsInt: true, IntKey: last, Type: en.Type, Optional: en.Optional || len(next) > 1})
				after = append(after, last+1)
				slices.Sort(after)
				next = slices.Compact(after)
				continue
			}
			i, found := at[en.Key]
			if !found {
				at[en.Key] = len(merged.Entries)
				merged.Entries = append(merged.Entries, en)
				continue
			}
			if en.Optional {
				// A later optional entry may be missing, and the earlier
				// one's value then stays.
				en.Type, en.Optional = types.MakeUnion(merged.Entries[i].Type, en.Type), merged.Entries[i].Optional
			}
			merged.Entries[i] = en
		}
	}
	merged.Unsealed = unsealed
	merged.List = !unsealed && len(at) == 0 && len(merged.Entries) > 0
	return merged, true
}

// byRefReads names, by function or Class::method, the by-reference
// parameters of php-src that read the argument they are given, as PHPStan
// 2.3 has them: its function map lists the function, and the parameter is
// `&rw_…` there, or is not by reference there at all. An offset passed to
// one is read as any argument is, and may be missing (offsetAccess.notFound).
// Every other by-reference parameter of php-src creates what it is given,
// preg_match()'s $matches, parse_str()'s $result and sodium_memzero()'s
// $string among them. The list classifies each by-reference parameter of
// stdlib/php-src against the function map with the deltas up to PHP 8.5.
var byRefReads = map[string]string{
	"array_multisort":                  "array rest",
	"array_pop":                        "array",
	"array_push":                       "array",
	"array_shift":                      "array",
	"array_splice":                     "array",
	"array_unshift":                    "array",
	"array_walk":                       "array",
	"array_walk_recursive":             "array",
	"arsort":                           "array",
	"asort":                            "array",
	"collator::asort":                  "array",
	"collator::sort":                   "array",
	"collator::sortwithsortkeys":       "array",
	"collator_asort":                   "array",
	"collator_sort":                    "array",
	"collator_sort_with_sort_keys":     "array",
	"datefmt_localtime":                "offset",
	"datefmt_parse":                    "offset",
	"enchant_dict_quick_check":         "suggestions",
	"end":                              "array",
	"extract":                          "array",
	"krsort":                           "array",
	"ksort":                            "array",
	"ldap_parse_reference":             "referrals",
	"mb_convert_variables":             "var vars",
	"mysqli_poll":                      "read error reject",
	"mysqli_stmt::bind_param":          "vars",
	"mysqli_stmt_bind_param":           "vars",
	"natcasesort":                      "array",
	"natsort":                          "array",
	"next":                             "array",
	"numberformatter::parse":           "offset",
	"numberformatter::parsecurrency":   "offset",
	"numfmt_parse":                     "offset",
	"numfmt_parse_currency":            "offset",
	"php_user_filter::filter":          "consumed",
	"prev":                             "array",
	"reset":                            "array",
	"rsort":                            "array",
	"settype":                          "var",
	"shuffle":                          "array",
	"sodium_add":                       "string1",
	"sodium_crypto_generichash_final":  "state",
	"sodium_crypto_generichash_update": "state",
	"sodium_crypto_secretstream_xchacha20poly1305_pull":  "state",
	"sodium_crypto_secretstream_xchacha20poly1305_push":  "state",
	"sodium_crypto_secretstream_xchacha20poly1305_rekey": "state",
	"sort":                   "array",
	"sqlite3stmt::bindparam": "var",
	"stream_select":          "read write except",
	"uasort":                 "array",
	"uksort":                 "array",
	"usort":                  "array",
}

// movesPointerOnly reports the php-src functions whose by-reference array
// keeps its values: end(), next(), prev() and reset() move its internal
// pointer, and the conditions about it stand, as in PHPStan.
func movesPointerOnly(sig *signature) bool {
	if sig.fn == nil {
		return false
	}
	switch strings.ToLower(sig.fn.Name) {
	case "end", "next", "prev", "reset":
		return true
	}
	return false
}

// keepsTheKeys reports a sort of php-src's with no @param-out in the
// overlay that writes back the array it is given with its keys and values,
// in another order (sortKeepingKeys). sort(), rsort() and usort(), which
// leave a list, and uasort() and uksort() are in the overlay.
func keepsTheKeys(sig *signature) bool {
	if sig.fn == nil {
		return false
	}
	switch strings.ToLower(sig.fn.Name) {
	case "asort", "arsort", "ksort", "krsort", "natsort", "natcasesort":
		return true
	}
	return false
}

// writtenThrough forgets the conditions about the variable e is, or whose
// offset it is, which a write the checker does not follow changed: a
// by-reference argument, a by-reference foreach over the array.
func (x *fnCtx) writtenThrough(e ast.Expr, env *state) {
	for {
		switch v := e.(type) {
		case *ast.Paren:
			e = v.Expr
			continue
		case *ast.Index:
			e = v.Array
			continue
		case *ast.Variable:
			if v.Name != "this" {
				env.written(v.Name)
			}
		case *ast.VariableVariable:
			env.forgetConds()
		}
		return
	}
}

// readsByRef reports a by-reference parameter that reads the argument it
// is given, so that an offset passed to it may be missing (P2321), as
// PHPStan has it. Of php-src, those byRefReads names. Of any other code,
// one whose declared type does not take null: PHP creates a missing offset
// as null for the reference, which a parameter with no type, mixed or a
// nullable type takes as it is, and the others refuse.
func readsByRef(sig *signature, p *program.Param) bool {
	if !sig.stub {
		return p.NativeType != nil && !types.IsMixed(p.NativeType) && !types.IsNullable(p.NativeType)
	}
	key := ""
	switch {
	case sig.fn != nil:
		key = strings.ToLower(sig.fn.Name)
	case sig.meth != nil && sig.meth.Class != nil:
		key = strings.ToLower(sig.meth.Class.Name + "::" + sig.meth.Name)
	}
	for _, name := range strings.Fields(byRefReads[key]) {
		if name == p.Name {
			return true
		}
	}
	return false
}

func (x *fnCtx) checkCallArgs(sig *signature, args *ast.ArgList, env *state, span ast.Span, expected types.Type) types.Type {
	// A partial application binds its arguments now and leaves the
	// placeholders' parameters to the closure it creates.
	partial := args.Partial()
	// The names that overwrite an argument before them, found here when no
	// argument is unpacked and otherwise once the unpacked values are typed
	// (below). PHP throws the overwrite first, so what follows from it — a
	// parameter it leaves unbound, the overwriting value against the
	// parameter's type — is not reported as well, and neither is the
	// overwrite in a closure PHP's compiler rejects (P1003).
	hasUnpack := false
	if args != nil {
		for _, a := range args.Args {
			hasUnpack = hasUnpack || a.Unpack
		}
	}
	rejected := rejectedClosure(sig.node, args)
	over := map[*ast.Arg]bool{}
	if !hasUnpack {
		found := findOverwrites(sig, args, nil)
		if !rejected {
			x.reportOverwrites(sig, found)
		}
		for _, o := range found {
			over[o.arg] = true
		}
	}
	var shape *pfaShape
	if partial {
		shape = x.partialShape(sig, args, span, len(over) > 0)
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
	unpackTypes := map[*ast.Arg]types.Type{}
	if args != nil {
		for i, a := range args.Args {
			if a.Placeholder != nil {
				placeholder = true
				continue
			}
			if a.Unpack {
				if lit, ok := unparen(a.Value).(*ast.ArrayLit); ok && !unpacked {
					x.unpackedByRef(lit, i, sig)
				}
				unpacked = true
				if n := len(sig.params); n > 0 && sig.params[n-1].Variadic && a.Name == nil {
					// `f(...$xs)` into `X ...$values`: the elements are the
					// arguments, typed and unified as such (array_push).
					bindings = append(bindings, bound{a, sig.params[n-1], i})
					continue
				}
				t := x.expr(a.Value, env, nil)
				unpackTypes[a] = t
				if _, _, ok := types.IterableKV(t, x.p()); (!ok && !types.IsMixed(t)) || mayNotIterate(t) {
					x.report(a.S, P2303, "cannot unpack %s", x.typeName(t))
				}
				continue
			}
			if a.Name != nil && over[a] {
				// PHP binds nothing to it; its value is typed for what
				// it holds.
				seenNamed = true
				if known(a.Value) == nil {
					x.expr(a.Value, env, nil)
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
				// PHP refuses it when it compiles the call.
				x.reportRefused(true, a.S, P2110, "positional argument after a named argument")
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
	// Required parameters present? Not after an overwrite: PHP throws it
	// before it counts.
	if !unpacked && !placeholder && len(over) == 0 {
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
	// parameter types and infers from their return types. A partial
	// application waits for pass 2 as a closure does: what its
	// placeholders will receive, which the other arguments say
	// (array_map's array after its callback), chooses its callee's type
	// parameters with its bound arguments (partialChoice).
	argTypes := make([]types.Type, len(bindings))
	// For a partial application, what the bound arguments determine with
	// the literals written in them generalised (partialChoice).
	var general map[string]types.Type
	if partial && len(inferable) > 0 {
		general = map[string]types.Type{}
	}
	isClosure := func(e ast.Expr) bool {
		switch e := e.(type) {
		case *ast.Closure, *ast.ArrowFunc:
			return true
		case *ast.Call:
			return e.Args.Partial() && e != x.pipeOperand && known(e) == nil
		case *ast.MethodCall:
			return e.Args.Partial() && e != x.pipeOperand && known(e) == nil
		case *ast.StaticCall:
			return e.Args.Partial() && e != x.pipeOperand && known(e) == nil
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
		if isClosure(b.arg.Value) && !b.param.ByRef {
			continue
		}
		pt := paramType(b.param)
		var at types.Type
		if b.arg.Value != nil && b.arg.Value == x.pipeOperand {
			x.pipeParam, x.pipeSig = b.param, sig
		}
		if b.param.ByRef {
			x.noteByRef(b.arg.Value)
			// The variable, or the one an element passed is of: a pipe's
			// bound argument may not pass it (boundInClosure).
			if v := refRoot(b.arg.Value); v != nil {
				if x.c.refVars == nil {
					x.c.refVars = map[*ast.Variable]bool{}
				}
				x.c.refVars[v] = true
			}
			if at = known(b.arg.Value); at == nil {
				at = x.exprLenient(b.arg.Value, env)
				// A by-reference parameter that reads what it is given,
				// sort()'s and array_push()'s, reads an offset the shape may
				// lack as a direct read does (P2321).
				if idx, ok := unparen(b.arg.Value).(*ast.Index); ok && readsByRef(sig, b.param) {
					x.offsetMayLack(idx, env)
				}
			}
			switch {
			case b.arg.Value == x.pipeOperand:
				x.report(b.arg.S, P2114, "parameter $%s of %s is by reference, and |> passes its left side by value: PHP throws an Error", b.param.Name, sig.name)
			case !isWritable(b.arg.Value) && !sig.removed:
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
			if general != nil {
				unify(types.Substitute(b.param.Type, rename), x.literalGeneral(b.arg.Value, at), general, inferable, x.p())
			}
		}
	}
	for i, b := range bindings {
		if isClosure(b.arg.Value) && !b.param.ByRef || argTypes[i] == nil || !isCallableType(argTypes[i]) {
			continue
		}
		// A closure with open type parameters given to another one's call
		// binds nothing: PHPStan cannot resolve the type parameter there
		// ("Unable to resolve the template type T in call to closure"),
		// so it is the bound here too, with P3010 where the result has it.
		if len(inferable) > 0 && !(sig.open && mentionsFreshParam(argTypes[i])) {
			pt := paramType(b.param)
			unify(pt, argTypes[i], subst, inferable, x.p())
			if general != nil {
				unify(pt, argTypes[i], general, inferable, x.p())
			}
		}
	}
	// A parameter left to its default binds its template parameter to the
	// default's type: `get($id, B $behavior = self::EXCEPTION)` with
	// `@return (B is 1 ? object : ?object)` decides on the constant. One a
	// placeholder stands for is the closure's callers' to give, with its
	// default or without, and binds nothing.
	if len(inferable) > 0 {
		given := map[*program.Param]bool{}
		for _, b := range bindings {
			given[b.param] = true
		}
		if shape != nil {
			for _, s := range shape.slots {
				given[s.param] = true
			}
		}
		for _, p := range sig.params {
			if given[p] || !p.HasDefault || p.Default == nil {
				continue
			}
			if dt := x.defaultType(p.Default, sig); dt != nil {
				pt := paramType(p)
				unify(pt, dt, subst, inferable, x.p())
				if general != nil {
					unify(pt, x.literalGeneral(p.Default, dt), general, inferable, x.p())
				}
			}
		}
	}
	// taken says, for each bound argument typed so far, whether the callee
	// takes it with the type parameters as subst has them; for one bound
	// by reference, the type its parameter has, which the variable is
	// retyped to.
	taken := func() []string {
		out := make([]string, len(bindings))
		for i, b := range bindings {
			if argTypes[i] == nil || b.arg.Unpack || over[b.arg] {
				continue
			}
			pt := finalizeParams(paramType(b.param), tps)
			switch {
			case b.param.ByRef:
				out[i] = "&" + types.PrintQualified(pt)
			case types.IsAcceptable(types.Substitute(argTypes[i], freshOnly(subst)), pt, x.p()):
				out[i] = "taken"
			default:
				out[i] = "refused"
			}
		}
		return out
	}
	// closureNow is the closure a partial application makes with the type
	// parameters as subst has them.
	closureNow := func() types.Type {
		close := func(t types.Type) types.Type {
			t = finalizeParams(types.Substitute(t, freshOnly(subst)), tps)
			if mentionsFreshParam(t) {
				t = finalizeParams(types.Substitute(t, freshOnly(subst)), tps)
			}
			return t
		}
		var ret types.Type
		if sig.ret != nil {
			ret = close(renameForeign(types.Substitute(types.Substitute(sig.ret, rename), subst)))
		}
		return partialClosure(shape, func(p *program.Param) types.Type { return close(paramType(p)) }, ret)
	}
	// A partial application's literals are read as their types now, before
	// the closures among its arguments are typed with its type parameters
	// (partialChoice).
	var choice *partialChoice
	if general != nil {
		choice = &partialChoice{x: x, shape: shape, tps: tps, subst: subst, inferable: inferable, taken: taken, closure: closureNow,
			declared: func(p *program.Param) types.Type { return types.Substitute(p.Type, rename) }}
		choice.generalise(general)
	}
	for i, b := range bindings {
		if !isClosure(b.arg.Value) || b.param.ByRef {
			continue
		}
		pt := paramType(b.param)
		at := known(b.arg.Value)
		if at == nil {
			if _, literal := b.arg.Value.(*ast.Closure); !literal && len(inferable) > 0 && expected != nil && sig.ret != nil && !mentionsFreshParam(expected) {
				// What this call is expected to return says what a
				// partial application among its arguments should: its
				// callee keeps a literal as written where the closure of
				// the generalised one would not do (partialChoice.join).
				if _, arrow := b.arg.Value.(*ast.ArrowFunc); !arrow {
					wished := map[string]types.Type{}
					for k, v := range subst {
						wished[k] = v
					}
					// (A conditional return type is either of its branches.)
					ret := types.MapDeep(renameForeign(types.Substitute(types.Substitute(sig.ret, rename), subst)), func(m types.Type) types.Type {
						if c, ok := m.(types.Conditional); ok {
							return types.MakeUnion(c.Then, c.Else)
						}
						return m
					})
					unify(ret, expected, wished, inferable, x.p())
					if x.wished == nil {
						x.wished = map[ast.Expr]types.Type{}
					}
					x.wished[b.arg.Value] = types.Substitute(types.Substitute(b.param.Type, rename), wished)
				}
			}
			at = x.expr(b.arg.Value, env, pt)
			delete(x.wished, b.arg.Value)
		}
		if rec := x.partialOf(b.arg.Value); rec != nil {
			// PHPStan reads this callback's parameters from the other
			// arguments by itself (program.PartialParam.Typed).
			if sig.stub && sig.fn != nil && phpstanReadsCallback[strings.ToLower(sig.fn.Name)] {
				for j, p := range rec.Params {
					rec.Params[j].Typed = p.Variadic || p.DocArray
				}
			}
		}
		argTypes[i] = at
		if len(inferable) > 0 {
			// Against the declared type, not the one substituted so far: a
			// closure returning int widens an A first bound to the literal 0.
			unify(types.Substitute(b.param.Type, rename), at, subst, inferable, x.p())
		}
	}
	// What the place gives a partial application's placeholders joins its
	// type parameters now that every bound argument is typed.
	if choice != nil {
		place := expected
		if w, ok := x.wished[sig.node]; ok && sig.node != nil {
			place = w
		}
		choice.join(expected, place)
	}
	// Infer from the expected type too (contextual typing of the return),
	// unless the enclosing call is still inferring it: its open parameter
	// says nothing yet, and binding ours to it would leave both open
	// (`$m->toDatabase($m->fromDatabase($doc, $class))`).
	// Not for a partial application, which is not what its callee returns:
	// the type expected of it is the closure's (partialChoice), and the
	// closure is checked against it, as any value is, with the type
	// parameters chosen.
	if len(inferable) > 0 && !partial && expected != nil && sig.ret != nil && !mentionsFreshParam(expected) {
		unify(renameForeign(types.Substitute(types.Substitute(sig.ret, rename), subst)), expected, subst, inferable, x.p())
	}
	// Expose the results under the declared names too (new Foo() reads them).
	for i, tp := range sig.typeParams {
		// Not an open closure's, which has them under fresh names only
		// (sigOfCallable): an argument may hold the same closure's.
		if v, ok := subst[tps[i].Name]; ok && !strings.Contains(tp.Name, "\x00") {
			subst[tp.Name] = v
		}
	}
	// An argument that the native type of a parameter written with the
	// template refuses is the plain mismatch PHP raises, reported before
	// the type argument it stands for: `new LimitIterator($traversable)`
	// passes no Iterator, rather than a TIterator outside its bound.
	nativeMismatch := func(template string) bool {
		found := false
		for i, b := range bindings {
			native := b.param.NativeType
			if b.param.ByRef || b.arg.Unpack || argTypes[i] == nil || native == nil || mentionsParam(native) || !mentionsTemplate(b.param.Type, template) {
				continue
			}
			at := types.Substitute(argTypes[i], freshOnly(subst))
			if at == nil || types.IsAcceptable(at, native, x.p()) {
				continue
			}
			found = true
			x.c.reportRelated(b.arg.S, P2103, related(sig.file, b.param.Span, "parameter $%s of %s declared here", b.param.Name, sig.name),
				"argument %d ($%s) of %s expects %s, %s given", b.idx+1, b.param.Name, sig.name, x.typeName(native), x.typeName(at))
		}
		return found
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
		bound := openToBounds(types.Substitute(tp.Bound, subst), sig.typeParams, subst)
		if !types.IsSubtype(v, bound, x.p()) {
			if nativeMismatch(tp.Name) {
				continue // the argument PHP refuses is the finding
			}
			// By its declared name: an open closure's type parameters are
			// the signature's under their fresh ones (sigOfCallable).
			x.report(span, P3002, "type argument %s inferred for %s of %s is not a subtype of its bound %s", x.typeName(v), types.ParamName(tp.Name), sig.name, x.typeName(bound))
		}
	}
	// With an unpacked argument the overwrites wait for its type: a sealed
	// shape says what it fills, `label(...['a'], prefix: 'b')` and
	// `label('a', ...$args, text: 'b')` with `$args` an array{text: …}.
	if hasUnpack && !rejected {
		for i, b := range bindings {
			if b.arg.Unpack {
				unpackTypes[b.arg] = argTypes[i]
			}
		}
		found := findOverwrites(sig, args, func(a *ast.Arg) ([]types.ShapeEntry, bool) {
			return shapeEntries(unpackTypes[a])
		})
		x.reportOverwrites(sig, found)
		for _, o := range found {
			if !o.arg.Unpack {
				over[o.arg] = true
			}
		}
	}
	// Check acceptability with the final substitution.
	for i, b := range bindings {
		if b.param.ByRef || over[b.arg] {
			continue
		}
		pt := paramType(b.param)
		pt = finalizeParams(pt, tps)
		if omitted, _ := omittedNull(sig, b.param); omitted {
			// Its null stands for the argument left out (omittedOnly).
			pt = types.StripNull(pt)
		}
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
			// The argument's expression is the value, unless it is spread:
			// then what is held to the parameter is an element of it.
			arg := x.given(b.arg.Value)
			if b.arg.Unpack {
				arg = given{}
			}
			declaredAt := related(sig.file, b.param.Span, "parameter $%s of %s declared here", b.param.Name, sig.name)
			if why, written, ok := x.rewrittenWhy(b.arg.Value, at, pt, env, false); ok && !b.arg.Unpack {
				mark := x.c.mark()
				x.c.reportRelated(b.arg.S, P2103, declaredAt, "argument %d ($%s) of %s expects %s, %s given: %s", b.idx+1, b.param.Name, sig.name, x.typeName(pt), x.typeName(at), why)
				x.c.relateAlso(mark, written)
				continue
			}
			x.c.reportUnfit(arg, at, pt, b.arg.S, P2103, declaredAt,
				"argument %d ($%s) of %s expects %s, %s given", b.idx+1, b.param.Name, sig.name, x.typeName(pt), x.typeName(at))
		}
	}
	forgets := x.sideEffects(sig)
	for i, b := range bindings {
		// `render($event)` may change what $event->getResponse() answers.
		key, ok := x.pathKey(b.arg.Value)
		if !ok {
			continue
		}
		env.invalidateCalls(key)
		// An argument that may be an object or a resource, PHPStan's
		// `!isObject()->no()`: an array holding objects is none.
		if forgets && !b.param.ByRef && !b.arg.Unpack && argTypes[i] != nil && mayBeObject(argTypes[i]) {
			x.forgetArgument(env, key, argTypes[i])
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
		// constant, named as the callee's own namespace names it.
		if _, given := argByName[p.Name]; given || p.Default == nil {
			continue
		}
		name, isConst := constName(p.Default)
		if name == "unknown" { // php-src's stubs write `= UNKNOWN` for a default they cannot spell
			continue
		}
		if isConst || isLiteralExpr(p.Default) {
			f, scope, cl := sig.declaredAt()
			argByName[p.Name] = x.c.valueWhereDeclared(f, scope, cl, p.Default, x)
		}
	}
	// array_walk() whose callback takes the value by value writes none back.
	walksByValue := false
	if sig.stub && sig.fn != nil {
		switch strings.ToLower(sig.fn.Name) {
		case "array_walk", "array_walk_recursive":
			for i, b := range bindings {
				if c, ok := argTypes[i].(types.Callable); ok && strings.EqualFold(b.param.Name, "callback") {
					walksByValue = c.HasSig && len(c.Params) > 0 && !c.Params[0].ByRef
				}
			}
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
			if sig.recv != nil {
				out = replaceStatic(out, sig.recv) // `@param-out static` is the receiver's class, as a `@return static` is
			}
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
				// The checker keeps the type, but the value may be another
				// one (settype(), similar_text(), array_walk() with a
				// callback that takes it by reference): what a condition
				// said of the old one is gone. array_multisort() renumbers
				// int keys and keeps a string key with its value.
				// Of a parameter documented @param-out (paramout.go), a pointer
				// moved or a walk by value is no write, and neither is
				// array_multisort(), as PHPStan has them: the end of the body
				// is held to what the parameter holds. A sort that keeps the
				// type of the array writes it, held to the tag at the call;
				// any other write is one the checker does not type, held to
				// nothing.
				v, isVar := b.arg.Value.(*ast.Variable)
				switch {
				case movesPointerOnly(sig) || walksByValue:
				case isVar && sig.fn != nil && strings.EqualFold(sig.fn.Name, "array_multisort"):
					env.reordered(v.Name)
				default:
					x.writtenThrough(b.arg.Value, env)
					if keepsTheKeys(sig) {
						x.sortWrite(b.arg.Value, env, b.arg.S)
					} else {
						x.handedOver(b.arg.Value, env)
					}
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
		if mentionsFreshParam(ret) {
			ret = finalizeParams(types.Substitute(ret, freshOnly(subst)), tps) // `U of T`
		}
		if sig.recv != nil {
			ret = replaceStatic(ret, sig.recv)
		}
		ret = x.evalConditional(ret, argByName)
	}
	// A type parameter of the callee that nothing determined, no argument,
	// no closure, no expected type, and that has no bound to fall back on
	// is mixed from here on (generics.md §3.4 item 4): the call is typed
	// as if it were not generic, silently in PHPStan. The warning names it
	// where an explicit argument (`f<User>(...)`) or a declared type on the
	// receiving side would fix it.
	for i, tp := range sig.typeParams {
		if _, ok := subst[tps[i].Name]; ok || !inferable[tps[i].Name] || tp.Bound != nil {
			continue
		}
		if sig.ret == nil || !types.ContainsParam(sig.ret, tp.Name) {
			continue
		}
		// An argument the checker could not read is the reason here, not
		// the call: a mixed or an unknown, or a callable parameter given
		// anything but a signature the checker knows (`[$this, 'method']`,
		// a string, a plain callable). Reported where it is, if at all.
		opaque := false
		for j, b := range bindings {
			if !types.ContainsParam(b.param.Type, tp.Name) {
				continue
			}
			at := argTypes[j]
			if _, unknown := at.(types.Unknown); at == nil || unknown || types.IsMixed(at) {
				opaque = true
				break
			}
			if _, never := at.(types.Never); never {
				opaque = true // an unreachable branch
				break
			}
			if isCallableType(b.param.Type) {
				c, ok := at.(types.Callable)
				if !ok || !c.HasSig {
					opaque = true
					break
				}
				// A closure that returns nothing (`run(fn() => $this->log())`)
				// binds no T to its return: the result, void, is not used.
				switch c.Return.(type) {
				case nil, types.Void, types.Never:
					opaque = true
				}
				if opaque {
					break
				}
			}
			// A generic class written without its arguments (`Traversable`)
			// says nothing of them either.
			for _, m := range types.Members(at) {
				if c, ok := m.(types.Class); ok && len(c.Args) == 0 && len(x.p().Variances(c.Name)) > 0 {
					opaque = true
				}
			}
			if opaque {
				break
			}
		}
		if opaque {
			continue
		}
		x.report(span, P3010, "type argument %s of %s is not inferable here and is mixed: write it as an explicit type argument, or type what receives the result", types.ParamName(tp.Name), sig.name)
	}
	// The parameters as the arguments resolved them; a type parameter
	// nothing determined widens to its bound (closure types are not
	// generic).
	resolved := func(p *program.Param) types.Type {
		t := finalizeParams(types.Substitute(paramType(p), freshOnly(subst)), tps)
		if mentionsFreshParam(t) {
			// A bound that names another type parameter, `U of T`, is
			// what that one was made.
			t = finalizeParams(types.Substitute(t, freshOnly(subst)), tps)
		}
		return t
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
	case types.Shape:
		// A shape binds entry by entry, by key, as PHPStan infers it:
		// `array{items: list<T>}` against `array{items: list<Row>,
		// total: int}` binds T to Row, and a tuple binds by position. The
		// keys the argument has beyond the parameter's are passed over.
		// An entry a sealed argument lacks binds nothing, so an optional
		// one that is absent leaves its parameter to the other entries;
		// an unsealed argument may hold it, with any value, and binds
		// mixed.
		for _, a := range types.Members(arg) {
			switch a := a.(type) {
			case types.Shape:
				for _, pe := range p.Entries {
					if ae, ok := shapeEntry(a, pe); ok {
						unify(pe.Type, ae.Type, subst, inferable, h)
					} else if a.Unsealed {
						unify(pe.Type, types.TMixed, subst, inferable, h)
					}
				}
			case types.Array:
				// A general array binds from its values the entries as one
				// union, whatever their keys: `array<string, string>` makes
				// T of `array{value?: T, n?: int}` a string. An unsealed
				// shape's values take in whatever its rest holds, mixed,
				// and bind nothing. Whether the array is accepted for the
				// shape is the argument's check.
				if !p.Unsealed && len(p.Entries) > 0 {
					unify(shapeValues(p), a.Value, subst, inferable, h)
				}
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
func (x *fnCtx) pipe(e *ast.Binary, left types.Type, env *state, expected types.Type) types.Type {
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
		// A stage is typed as the call it becomes, with what is expected
		// of the pipe: a call's result is inferred from it, and so is a
		// partial application among its arguments.
		t := x.expr(call, env, expected)
		if args.Partial() && x.pipeParam != nil && x.pipeSig != nil {
			x.recordPipePartial(e.Right, args, x.pipeSig, x.pipeParam)
		}
		x.recordFold(e, args)
		x.settleBound(e, args)
		return t
	}
	operand := &ast.Arg{S: e.Left.Span(), Value: e.Left}
	return x.dynamicCall(&ast.Call{S: e.S, Callee: e.Right, Args: &ast.ArgList{S: e.Left.Span(), Args: []*ast.Arg{operand}}}, env, expected)
}
