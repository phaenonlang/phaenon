package checker

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// superglobal reports the variables that are always defined: PHP's
// superglobals (ast.IsSuperglobal), and $argv and $argc for a script's top
// level; see localArgs.
func superglobal(name string) bool {
	return ast.IsSuperglobal(name) || name == "argv" || name == "argc"
}

// localArgs reports whether $argv or $argc names a variable the code itself
// assigned: PHP defines the two only in the global scope, so a function's
// `$argv = $_SERVER['argv'] ?? []` is an ordinary local with the type of
// what it was given, not the script's list<string>.
func localArgs(name string, env *state) bool {
	if name != "argv" && name != "argc" {
		return false
	}
	_, local := env.get(name)
	return local
}

// expr types an expression in env, with an optional expected type for
// contextual typing. It updates env for assignments and other side effects.
// partialResult is a call's type, a Closure where it is a partial
// application whose callee the checker could not type: PHP creates a
// closure of whatever it is, and nothing is reported of a mixed it would
// otherwise be.
func partialResult(args *ast.ArgList, t types.Type) types.Type {
	if args.Partial() && types.IsMixed(t) {
		return types.NewClass("Closure")
	}
	return t
}

func (x *fnCtx) expr(e ast.Expr, env *state, expected types.Type) types.Type {
	t := x.exprType(e, env, expected)
	if c, ok := t.(types.Callable); ok && expected != nil && e != nil {
		t = x.closeOpen(c, expected, e)
	}
	if assume := x.c.opts.Assume; assume != nil && e != nil {
		if a := assume(x.file, e); a != nil {
			t = a
		}
	}
	if observe := x.c.opts.Observe; observe != nil && e != nil {
		observe(x.file, e, t)
	}
	return t
}

func (x *fnCtx) exprType(e ast.Expr, env *state, expected types.Type) types.Type {
	if t, ok := x.argKnown[e]; ok && e != nil {
		return t // a pipe's left side, typed once before the stage it feeds
	}
	switch e := e.(type) {
	case nil:
		return types.TMixed
	case *ast.Paren:
		return x.expr(e.Expr, env, expected)
	case *ast.Variable:
		return x.variable(e, env, false)
	case *ast.VariableVariable:
		x.expr(e.Expr, env, nil)
		x.removed(e.S, "variable variables ($$x)", "a map<string, mixed>")
		return types.TMixed
	case *ast.IntLit:
		return intLitType(e.Raw)
	case *ast.FloatLit:
		if f, err := strconv.ParseFloat(strings.ReplaceAll(e.Raw, "_", ""), 64); err == nil {
			return types.FloatLit{V: f}
		}
		return types.TFloat
	case *ast.StringLit:
		return types.StringLit{V: program.StringValue(e)}
	case *ast.InterpolatedString:
		if e.Kind == ast.StringBacktick {
			x.c.deprecated(e.S, "the backtick operator is deprecated since PHP 8.5: call shell_exec()")
		}
		return x.interpolated(e, env)
	case *ast.ArrayLit:
		return x.arrayLit(e, env, expected)
	case *ast.ConstFetch:
		return x.constFetch(e)
	case *ast.MagicConst:
		return x.magicConst(e)
	case *ast.ClassConstFetch:
		return x.classConstFetch(e, env)
	case *ast.PropertyFetch:
		return x.propertyFetch(e, env, false)
	case *ast.StaticPropertyFetch:
		t, _ := x.staticPropertyFetch(e, env)
		return t
	case *ast.Index:
		return x.index(e, env)
	case *ast.Call:
		return partialResult(e.Args, x.call(e, env, expected))
	case *ast.MethodCall:
		return partialResult(e.Args, x.methodCall(e, env, expected))
	case *ast.StaticCall:
		return partialResult(e.Args, x.staticCall(e, env, expected))
	case *ast.New:
		return x.newExpr(e, env, expected)
	case *ast.Clone:
		return x.cloneExpr(e, env)
	case *ast.Unary:
		return x.unary(e, env)
	case *ast.Cast:
		return x.cast(e, env)
	case *ast.IncDec:
		return x.incDec(e, env)
	case *ast.Binary:
		return x.binary(e, env, expected)
	case *ast.Assign:
		return x.assign(e, env)
	case *ast.Ternary:
		return x.ternary(e, env, expected)
	case *ast.Instanceof:
		x.instanceofType(e, env)
		return types.TBool
	case *ast.Isset:
		return x.isset(e, env)
	case *ast.Empty:
		x.idleNullsafe(e.Expr, env, "empty()", "in empty()")
		x.exprLenient(e.Expr, env)
		return types.TBool
	case *ast.Exit:
		if e.Args != nil {
			for _, a := range e.Args.Args {
				x.expr(a.Value, env, nil)
			}
		}
		env.dead = true
		return types.TNever
	case *ast.Print:
		x.toString(e.Expr, env)
		return types.IntLit(1)
	case *ast.Include:
		x.expr(e.Expr, env, nil)
		x.implicitThrow()
		env.forgetConds() // the included file runs in this scope and may write any variable
		return types.TMixed
	case *ast.Eval:
		x.expr(e.Expr, env, nil)
		x.implicitThrow()
		x.removed(e.S, "eval()", "nothing — generate code at build time instead")
		return types.TMixed
	case *ast.Closure:
		return x.closure(e, env, expected)
	case *ast.ArrowFunc:
		return x.arrowFunc(e, env, expected)
	case *ast.Withdrawn:
		return x.withdrawn(e, env)
	case *ast.Match:
		return x.match(e, env, expected)
	case *ast.Yield:
		defer x.implicitThrow() // the generator's consumer may throw into it
		return x.yield(e, env)
	case *ast.YieldFrom:
		defer x.implicitThrow()
		return x.yieldFrom(e, env)
	case *ast.Throw:
		saved := x.throwOperand
		x.throwOperand = unparen(e.Expr)
		t := x.expr(e.Expr, env, nil)
		x.throwOperand = saved
		if !types.IsMixed(t) && !types.IsSubtype(t, types.NewClass("Throwable"), x.p()) {
			x.report(e.Expr.Span(), P2317, "cannot throw %s: not a Throwable", x.typeName(t))
		}
		x.explicitThrow(t, false, e.S)
		env.dead = true
		return types.TNever
	case *ast.IsExpr:
		return x.isType(e, env)
	case *ast.AsExpr:
		if !e.Nullable {
			defer func() { x.explicitThrow(x.asFailure(e), false, e.S) }() // `as?` gives null instead
		}
		return x.asExpr(e, env)
	case *ast.Name:
		// The parser leaves constants as bare names. A type parameter's
		// name has no runtime value (P3003).
		if tp := x.typeParamRef(e); tp != nil {
			return x.paramAsValue(tp, e.S)
		}
		return x.constFetch(&ast.ConstFetch{S: e.S, Name: e})
	case *ast.Ident:
		return types.StringLit{V: e.Name}
	case *ast.StringPart:
		return types.StringLit{V: e.Raw}
	case *ast.EncapsBraced:
		return x.expr(e.Expr, env, nil)
	case *ast.StringVarName:
		if x.removed(e.S, "${name} string interpolation", "{$name}") {
			return types.TError // the one finding: the value is not judged as well
		}
		return types.TMixed
	}
	return types.TMixed
}

// exprLenient types an expression without reporting undefined variables
// or missing offsets (isset/empty/?? operands).
func (x *fnCtx) exprLenient(e ast.Expr, env *state) types.Type {
	if assume := x.c.opts.Assume; assume != nil && e != nil {
		if a := assume(x.file, e); a != nil {
			return a
		}
	}
	switch e := e.(type) {
	case *ast.Variable:
		return x.variable(e, env, true)
	case *ast.Index:
		// A narrowed offset reads as narrowed here too: `is_bool($e['v'])`
		// after `is_scalar($e['v'])` subtracts bool from scalar, not from
		// the element type.
		if key, ok := x.pathKey(e); ok {
			if t, ok := env.getPath(key); ok {
				return t
			}
		}
		delete(x.skippedChain, e) // a mark of an earlier pass over a loop: this one decides
		if t, ok := x.narrowedByLiteralKeys(e, env); ok {
			return t
		}
		base := x.exprLenient(e.Array, env)
		if inner, ok := unparen(e.Array).(*ast.Index); ok && offsetsOfVariable(inner.Array) {
			// An offset no value of the array has reads as nothing PHPStan
			// knows of, even in isset() and ??: an offset of it is one of
			// mixed, `isset($r['gone']['x'])`, reported once. Only on a
			// variable's arrays: a call's result the flow narrowed (an
			// assertSame()) may have changed since, which PHPStan sees.
			if key := x.literalOffset(inner, env); key != nil && unsetMissing(x.pathType(inner.Array, env), key) {
				x.indexType(types.TMixed, e, env, true)
				return types.TError
			}
		}
		return x.indexType(base, e, env, true) // types the key, once
	case *ast.PropertyFetch:
		x.quietOffsets++
		defer func() { x.quietOffsets-- }()
		return x.propertyFetch(e, env, true)
	case *ast.StaticPropertyFetch:
		x.quietOffsets++
		defer func() { x.quietOffsets-- }()
	case *ast.Paren:
		return x.exprLenient(e.Expr, env)
	}
	return x.expr(e, env, nil)
}

// ------------------------------------------------------------------ atoms

func (x *fnCtx) variable(v *ast.Variable, env *state, lenient bool) types.Type {
	if v.Name == "this" {
		if x.constClosure {
			x.report(v.S, P2531, "a closure in a constant expression cannot use $this")
			return types.TError // reported: its members are not reported again
		}
		if x.class == nil || x.static {
			x.report(v.S, P2311, "$this is not available in a static context")
			return types.TError
		}
		if t, ok := env.getPath("$this"); ok {
			return t
		}
		return x.thisType
	}
	if superglobal(v.Name) && !localArgs(v.Name, env) {
		if v.Name == "argc" {
			return types.TInt
		}
		if v.Name == "argv" {
			return types.Array{Key: types.TInt, Value: types.TString, List: true}
		}
		return types.Array{Key: types.TString, Value: types.TMixed}
	}
	info, ok := env.get(v.Name)
	if !ok {
		if !lenient {
			x.report(v.S, P2008, "undefined variable $%s", v.Name)
			return types.TError
		}
		return types.TMixed
	}
	if info.maybe && !lenient {
		x.report(v.S, P2009, "variable $%s might not be defined", v.Name)
	}
	if x.byRef[v.Name] {
		return widenType(info.t)
	}
	return info.t
}

func intLitType(raw string) types.Type {
	s := strings.ReplaceAll(raw, "_", "")
	var v int64
	var err error
	switch {
	case strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X"):
		v, err = strconv.ParseInt(s[2:], 16, 64)
	case strings.HasPrefix(s, "0b") || strings.HasPrefix(s, "0B"):
		v, err = strconv.ParseInt(s[2:], 2, 64)
	case strings.HasPrefix(s, "0o") || strings.HasPrefix(s, "0O"):
		v, err = strconv.ParseInt(s[2:], 8, 64)
	case len(s) > 1 && s[0] == '0':
		v, err = strconv.ParseInt(s[1:], 8, 64)
	default:
		v, err = strconv.ParseInt(s, 10, 64)
	}
	if err != nil {
		return types.TFloat // overflow: PHP makes it a float
	}
	return types.IntLit(v)
}

func (x *fnCtx) interpolated(e *ast.InterpolatedString, env *state) types.Type {
	nonEmpty := false
	for _, p := range e.Parts {
		switch p := p.(type) {
		case *ast.StringPart:
			if p.Raw != "" {
				nonEmpty = true
			}
		default:
			t := x.expr(p, env, nil)
			x.checkStringable(t, p.Span(), p)
		}
	}
	if e.Kind == ast.StringBacktick {
		return types.MakeUnion(types.TString, types.TNull, types.TFalse)
	}
	if nonEmpty {
		return types.String{NonEmpty: true}
	}
	return types.TString
}

// checkStringable reports a value PHP cannot convert to string, or should
// not: bytes is binary data, not text. operand is the expression the value
// is, nil when the caller has none to name.
func (x *fnCtx) checkStringable(t types.Type, span ast.Span, operand ast.Expr) {
	if mayBeObject(t) {
		x.implicitThrow() // __toString() runs
	}
	for _, m := range types.Members(t) {
		switch m := m.(type) {
		case types.Mixed:
			x.report(span, P2312, "mixed cannot be converted to string")
			return
		case types.Bytes:
			// design/strings.md §2: text and binary never mix silently.
			if v, ok := bytesVariable(operand); ok {
				x.report(span, P2530, "bytes is not text: mb_check_encoding(%[1]s, 'UTF-8') checks it is UTF-8, after which it is string&bytes; (string) %[1]s converts it unchecked; bin2hex() or base64_encode() spells it out", v)
			} else {
				x.report(span, P2530, "bytes is not text: mb_check_encoding() on the bytes checks they are UTF-8, after which they are string&bytes; a (string) cast converts them unchecked; bin2hex() or base64_encode() spells them out")
			}
			return
		case types.Array, types.Shape:
			x.report(span, P2312, "array cannot be converted to string")
			return
		case types.Class:
			// GMP has no __toString(), and converts all the same: its
			// handlers give PHP the number's digits, as PHPStan has it.
			if cl := x.p().Class(m.Name); cl != nil && !cl.IsSubclassOf("Stringable") && !cl.IsSubclassOf("GMP") && cl.FindMethod("__toString") == nil {
				x.report(span, P2312, "%s cannot be converted to string: no __toString()", m.Name)
				return
			}
		case types.Object:
			x.report(span, P2312, "object cannot be converted to string")
			return
		}
	}
}

func (x *fnCtx) toString(e ast.Expr, env *state) types.Type {
	t := x.expr(e, env, nil)
	x.checkStringable(t, e.Span(), e)
	return t
}

func (x *fnCtx) arrayLit(e *ast.ArrayLit, env *state, expected types.Type) types.Type {
	var expectedValue, expectedKey types.Type
	if expected != nil {
		for _, m := range types.Members(expected) {
			if a, ok := m.(types.Array); ok {
				expectedValue, expectedKey = a.Value, a.Key
			}
		}
	}
	shape := types.Shape{}
	isShape := true
	var keys, values []types.Type
	nextInt := int64(0)
	// positioned says nextInt is where the next unkeyed item goes for
	// certain: not after an unpacked value of unknown length.
	positioned := true
	isList := true
	hasItems, nonEmpty := false, false
	for i, item := range e.Items {
		if item == nil {
			continue
		}
		hasItems = true
		if item.Unpack {
			t := x.expr(item.Value, env, nil)
			k, v, ok := types.IterableKV(t, x.p())
			if !ok || mayNotIterate(t) {
				x.report(item.Value.Span(), P2303, "cannot unpack %s", x.typeName(t))
			}
			if sh, ok := unpackedShape(t); ok && isShape && (unpacksByEntry(sh) || !sh.Unsealed && namedOnly(e.Items[i+1:]) && trailingOptional(sh)) {
				// A shape unpacks into its entries: `[...$pair, 'x']` of a
				// `(string, int)` is `list{string, int, 'x'}`, as PHPStan
				// has it. PHP numbers the integer keys anew and keeps the
				// string ones; an entry that may be absent leaves what the
				// literal already has under its key, or nothing.
				for _, se := range sh.Entries {
					entry := types.ShapeEntry{Key: se.Key, Type: se.Type, Optional: se.Optional}
					if se.IsInt {
						// PHP numbers them anew, whatever the operand's keys:
						// they continue a list, the optional ones last, and
						// nothing after an optional one is numbered
						// (namedOnly).
						entry = types.ShapeEntry{IsInt: true, IntKey: nextInt, Type: se.Type, Optional: se.Optional}
						nextInt++
						positioned = positioned && !se.Optional
					} else {
						isList = false
						if before, ok := shapeEntry(shape, entry); ok && se.Optional {
							entry.Type = types.MakeUnion(before.Type, se.Type)
							entry.Optional = before.Optional
						}
					}
					keys = append(keys, shapeEntryKey(entry))
					shape.Entries = upsert(shape.Entries, entry)
					values = append(values, se.Type)
					nonEmpty = nonEmpty || !entry.Optional
				}
				continue
			}
			isShape = false
			positioned = false
			// An unpacked iterable may yield nothing, and what it yields lands
			// under the keys an array can have: PHP throws on any other key,
			// so iterable<mixed, V> unpacks to array<array-key, V>.
			if a, isArray := t.(types.Array); isArray && a.NonEmpty {
				nonEmpty = true
			}
			if !types.IsSubtype(k, types.TArrayKey, nil) {
				k = types.TArrayKey
			}
			// PHP numbers integer keys anew from where the literal is,
			// as many as the value has: any position from there on.
			k = types.Map(k, func(m types.Type) types.Type {
				if _, isInt := m.(types.Int); isInt {
					return types.Int{Lo: nextInt, Hi: math.MaxInt64}
				}
				return m
			})
			keys = append(keys, k)
			values = append(values, v)
			if !types.IsSubtype(k, types.TInt, nil) {
				isList = false
			}
			continue
		}
		nonEmpty = true
		var ev types.Type
		if item.Key != nil {
			kt, stored := x.keyExpr(item.Key, env)
			x.nullOffset(kt, item.Key.Span())
			var entryExpected types.Type = expectedValue
			if sh, ok := expectedShapeEntry(expected, kt); ok {
				entryExpected = sh
			}
			ev = x.expr(item.Value, env, entryExpected)
			keys = append(keys, stored)
			switch k := kt.(type) {
			case types.StringLit:
				if n, err := strconv.ParseInt(k.V, 10, 64); err == nil && strconv.FormatInt(n, 10) == k.V {
					shape.Entries = upsert(shape.Entries, types.ShapeEntry{IsInt: true, IntKey: n, Type: ev})
					if n >= nextInt {
						nextInt = n + 1
					}
					if n != nextInt-1 {
						isList = false
					}
				} else {
					shape.Entries = upsert(shape.Entries, types.ShapeEntry{Key: k.V, Type: ev})
					isList = false
				}
			case types.Int:
				if k.Lo == k.Hi {
					if k.Lo != nextInt {
						isList = false
					}
					shape.Entries = upsert(shape.Entries, types.ShapeEntry{IsInt: true, IntKey: k.Lo, Type: ev})
					if k.Lo >= nextInt {
						nextInt = k.Lo + 1
					}
				} else {
					isShape = false
					isList = false
				}
			case types.BoolLit:
				n := int64(0)
				if k.V {
					n = 1
				}
				shape.Entries = upsert(shape.Entries, types.ShapeEntry{IsInt: true, IntKey: n, Type: ev})
				isList = false
			default:
				isShape = false
				isList = false
				if !types.IsSubtype(kt, types.MakeUnion(types.TArrayKey, types.TBool, types.TFloat, types.TNull), x.p()) && !types.IsMixed(kt) {
					x.report(item.Key.Span(), P2316, "invalid array key type %s", x.typeName(kt))
				}
			}
		} else {
			// A tuple's entry is expected at its position, as a shape's
			// under its key, where the position is known.
			var entryExpected types.Type = expectedValue
			if sh, ok := expectedShapeEntry(expected, types.IntLit(nextInt)); ok && positioned {
				entryExpected = sh
			}
			ev = x.expr(item.Value, env, entryExpected)
			if len(e.Items) == 2 && item == e.Items[0] {
				x.noteRecv(e, ev) // [$this, 'name'] may name a private method
			}
			keys = append(keys, types.IntLit(nextInt))
			shape.Entries = upsert(shape.Entries, types.ShapeEntry{IsInt: true, IntKey: nextInt, Type: ev})
			nextInt++
		}
		values = append(values, ev)
	}
	if !hasItems {
		return types.Shape{List: true} // [] is an empty list
	}
	_ = expectedKey
	if isShape && len(shape.Entries) <= 32 {
		shape.List = isList
		return shape
	}
	return types.Array{Key: types.MakeUnion(keys...), Value: types.MakeUnion(values...), List: isList, NonEmpty: nonEmpty}
}

// unpackedShape is the shape an unpacked value spreads as: itself, or for
// a union of sealed shapes that are all lists or all keyed by strings, one
// shape of their keys, optional where a shape lacks them, as PHPStan
// spreads each and joins the results: `['a' => 1, ...$base]` with `$base`
// either `['b' => 2]` or `[]` is `array{a: 1, b?: 2}`, and `[0, ...$base]`
// with `[1, 2]` or `[3]` is `array{0: 0, 1: 1|3, 2?: 2}`.
func unpackedShape(t types.Type) (types.Shape, bool) {
	if sh, ok := t.(types.Shape); ok {
		return sh, true
	}
	var shapes []types.Shape
	ints, strs := false, false
	for _, m := range types.Members(t) {
		sh, ok := m.(types.Shape)
		if !ok || sh.Unsealed || sh.Callable {
			return types.Shape{}, false
		}
		for j, e := range sh.Entries {
			switch {
			case !e.IsInt:
				strs = true
			case e.Optional || e.IntKey != int64(j):
				return types.Shape{}, false // not a list
			default:
				ints = true
			}
		}
		shapes = append(shapes, sh)
	}
	if len(shapes) < 2 || ints && strs {
		return types.Shape{}, false
	}
	return collapseShapes(shapes), true
}

// trailingOptional reports a shape whose optional integer keys come after
// all its certain ones, as a union of lists of several lengths gives them.
func trailingOptional(s types.Shape) bool {
	optional := false
	for _, e := range s.Entries {
		if !e.IsInt {
			continue
		}
		if !e.Optional && optional {
			return false
		}
		optional = optional || e.Optional
	}
	return true
}

// namedOnly reports items of an array literal that number nothing: each a
// value under a string key, written as a string literal.
func namedOnly(items []*ast.ArrayItem) bool {
	for _, item := range items {
		if item == nil {
			continue
		}
		if item.Unpack || item.Key == nil {
			return false
		}
		if _, ok := item.Key.(*ast.StringLit); !ok {
			return false
		}
	}
	return true
}

// unpacksByEntry reports whether unpacking s into an array literal can be
// followed entry by entry: it is sealed, and its integer keys, which PHP
// numbers anew, are all certain, so that each has one place.
func unpacksByEntry(s types.Shape) bool {
	if s.Unsealed || s.Callable {
		return false
	}
	for _, e := range s.Entries {
		if e.Optional && e.IsInt {
			return false
		}
	}
	return true
}

func expectedShapeEntry(expected types.Type, key types.Type) (types.Type, bool) {
	if expected == nil {
		return nil, false
	}
	for _, m := range types.Members(expected) {
		sh, ok := m.(types.Shape)
		if !ok {
			continue
		}
		for _, e := range sh.Entries {
			switch k := key.(type) {
			case types.StringLit:
				if !e.IsInt && e.Key == k.V {
					return e.Type, true
				}
			case types.Int:
				if e.IsInt && k.Lo == k.Hi && e.IntKey == k.Lo {
					return e.Type, true
				}
			}
		}
	}
	return nil, false
}

// normaliseKey is the key PHP stores for a value of type t, member by
// member: bool and float keys become ints, null the empty string, and a
// string known to be a canonical decimal integer the int it spells
// (types.StoredKey; design/checker.md §9). A general string stays a string.
func normaliseKey(t types.Type) types.Type {
	if types.IsMixed(t) {
		return types.TArrayKey // PHP casts or refuses; the array gets int|string keys
	}
	return types.Map(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.BoolLit:
			// PHP stores true under 1 and false under 0.
			if m.V {
				return types.IntLit(1)
			}
			return types.IntLit(0)
		case types.FloatLit:
			// PHP stores a float under its integer part (1.7 under 1).
			if v := math.Trunc(m.V); v >= math.MinInt64 && v < math.MaxInt64 {
				return types.IntLit(int64(v))
			}
			return types.TInt
		case types.Bool:
			return types.TInt
		case types.Null:
			return types.StringLit{V: ""}
		case types.Float:
			return types.TInt
		}
		return types.StoredKey(m)
	})
}

// keyExpr types an expression used as an array key: its own type, and the
// key PHP stores for it. A (string) cast written as the key is looked
// through: each member of the operand is cast and normalised on its own,
// before a union would absorb the decimal-int-string an int casts to into
// string, so `$out[(string) $k]` with $k: int|string writes int|string.
func (x *fnCtx) keyExpr(e ast.Expr, env *state) (raw, key types.Type) {
	inner := e
	for {
		p, ok := inner.(*ast.Paren)
		if !ok {
			break
		}
		inner = p.Expr
	}
	if c, ok := inner.(*ast.Cast); ok && c.Op == token.T_STRING_CAST {
		members := x.stringCast(c, x.expr(c.Expr, env, nil))
		keys := make([]types.Type, len(members))
		for i, m := range members {
			keys[i] = normaliseKey(m)
		}
		return types.MakeUnion(members...), types.MakeUnion(keys...)
	}
	raw = x.expr(e, env, nil)
	return raw, normaliseKey(raw)
}

func upsert(entries []types.ShapeEntry, e types.ShapeEntry) []types.ShapeEntry {
	for i, cur := range entries {
		if cur.IsInt == e.IsInt && cur.IntKey == e.IntKey && cur.Key == e.Key {
			entries[i] = e
			return entries
		}
	}
	return append(entries, e)
}

func (x *fnCtx) constFetch(e *ast.ConstFetch) types.Type {
	if t := x.specialConst(e.Name); t != nil {
		return t
	}
	if len(e.Name.Parts) == 1 && e.Name.Kind == ast.NameUnqualified {
		switch strings.ToLower(e.Name.Parts[0]) {
		case "php_eol":
			return types.String{NonEmpty: true}
		case "__compiler_halt_offset__":
			return types.TInt
		}
	}
	ns, global := x.res.Scope().ResolveConst(e.Name)
	for _, name := range []string{ns, global} {
		if k := x.p().Constant(name); k != nil {
			x.c.libraryConstant(e.S, k)
			if k.Deprecated && x.c.opts.Deprecations {
				x.report(e.S, P4006, "constant %s is deprecated%s", k.Name, k.DeprecatedNote)
			}
			t := x.p().ConstantType(k)
			if dynamicConstants[strings.ToLower(name)] {
				// PHP_OS is a string, not this machine's 'Darwin'; a tag
				// (`@var 4|8` on PHP_INT_SIZE) says what the values can be.
				if k.Doc && k.Type != nil {
					t = k.Type
				} else {
					t = widenType(t)
				}
			}
			return t
		}
	}
	x.report(e.S, P2003, "unknown constant %s", global)
	return types.TError
}

// specialConst types true, false and null, which PHP's compiler replaces by
// their value before it looks a constant up (zend_try_ct_eval_const): in
// any letter case, unqualified in any namespace, and under a name that is
// the global one, `\null` or `namespace\null` outside a namespace. nil for
// every other name: `Foo\null`, and `namespace\null` inside a namespace,
// are constants of that namespace.
func (x *fnCtx) specialConst(n *ast.Name) types.Type {
	if len(n.Parts) != 1 {
		return nil
	}
	name := n.Parts[0]
	if n.Kind != ast.NameUnqualified {
		resolved, _ := x.res.Scope().ResolveConst(n)
		if strings.Contains(strings.TrimPrefix(resolved, "\\"), "\\") {
			return nil
		}
		name = strings.TrimPrefix(resolved, "\\")
	}
	switch strings.ToLower(name) {
	case "true":
		return types.TTrue
	case "false":
		return types.TFalse
	case "null":
		return types.TNull
	}
	return nil
}

// dynamicConstants are the constants whose value depends on the machine,
// the build or the version of PHP: their type is known, their value is not
// (PHPStan's dynamicConstantNames). PHP_INT_MAX and PHP_INT_MIN keep their
// 64-bit values: defaults such as explode()'s limit are read from them.
var dynamicConstants = map[string]bool{
	"php_version": true, "php_major_version": true, "php_minor_version": true, "php_release_version": true,
	"php_version_id": true, "php_extra_version": true, "php_windows_version_major": true,
	"php_windows_version_minor": true, "php_windows_version_build": true, "php_zts": true, "php_debug": true,
	"php_maxpathlen": true, "php_os": true, "php_os_family": true, "php_sapi": true, "php_eol": true,
	"php_int_size": true, "php_float_dig": true,
	"php_float_epsilon": true, "php_float_min": true, "php_float_max": true, "default_include_path": true,
	"pear_install_dir": true, "pear_extension_dir": true, "php_extension_dir": true, "php_prefix": true,
	"php_bindir": true, "php_binary": true, "php_mandir": true, "php_libdir": true, "php_datadir": true,
	"php_sysconfdir": true, "php_localstatedir": true, "php_config_file_path": true,
	"php_config_file_scan_dir": true, "php_shlib_suffix": true, "php_fd_setsize": true, "iconv_impl": true,
	"libxml_version": true, "libxml_dotted_version": true, "openssl_version_number": true, "openssl_version_text": true,
}

func (x *fnCtx) magicConst(e *ast.MagicConst) types.Type {
	switch e.Kind {
	case token.T_LINE:
		line, _ := x.file.Position(e.S.Start)
		return types.IntLit(int64(line))
	case token.T_CLASS_C:
		if x.class != nil && !x.class.Anonymous {
			return types.StringLit{V: x.class.Name}
		}
		return types.TString
	case token.T_FILE, token.T_DIR:
		return types.String{NonEmpty: true}
	}
	return types.TString
}

func (x *fnCtx) classConstFetch(e *ast.ClassConstFetch, env *state) types.Type {
	if x.lateStaticRefused(e) {
		return types.TError
	}
	var recv types.Type
	var classes []*program.Class
	if n, ok := e.Class.(*ast.Name); ok {
		if specialClassName(n) == "parent" && x.class != nil {
			if pc := x.class.ParentClass(); pc != nil {
				recv, classes = types.Class{Name: pc.Name, Args: x.class.ParentArgs}, []*program.Class{pc}
			} else {
				x.report(n.S, P2011, "class %s has no parent", x.class.Name)
				return types.TMixed
			}
		} else {
			// X::class is a string the compiler writes: PHP never loads X.
			saved := x.nameOnly
			if id, ok := e.Name.(*ast.Ident); ok && strings.EqualFold(id.Name, "class") {
				x.nameOnly = true
			}
			t, cl := x.classRefType(n, n.S)
			x.nameOnly = saved
			recv = t
			if cl != nil {
				classes = []*program.Class{cl}
			}
		}
	} else {
		recv = x.expr(e.Class, env, nil)
		classes = x.classesOf(recv, e.Class.Span())
	}
	x.noteRecv(e, recv)
	name, ok := identName(e.Name)
	if !ok {
		x.expr(e.Name, env, nil)
		return types.TMixed
	}
	if _, dynamic := e.Class.(*ast.Name); !dynamic && !strings.EqualFold(name, "class") {
		x.memberOnMixed(recv, e.Class.Span(), "cannot access constant %s on mixed", name)
	}
	if strings.EqualFold(name, "class") {
		if lit, ok := recv.(types.Class); ok && !strings.EqualFold(lit.Name, "static") {
			if _, isName := e.Class.(*ast.Name); isName {
				return types.StringLit{V: lit.Name}
			}
		}
		return types.ClassString{Of: types.StripNull(recv)}
	}
	if len(classes) == 0 {
		return types.TMixed
	}
	// `static::X`, `$this::X` and `$object::X` name the constant of the
	// class the value has at run time, which a subclass may override:
	// unless the class is final (an enum is) or the constant is, the
	// constant is its declared type, and mixed without one, as PHPStan
	// types it; its value is not. `self::X` and `Name::X` are the
	// constant written.
	lateBound := true
	if n, ok := e.Class.(*ast.Name); ok && specialClassName(n) != "static" {
		lateBound = false
	}
	var out []types.Type
	for _, cl := range classes {
		if cs := cl.FindCase(name); cs != nil {
			x.c.libraryClassConstant(e.Name.Span(), cs.Class, cs.Name)
			if cs.Deprecated && x.c.opts.Deprecations {
				x.report(e.Name.Span(), P4006, "enum case %s::%s is deprecated%s", cs.Class.Name, cs.Name, cs.DeprecatedNote)
			}
			out = append(out, types.EnumCase{Class: cl.Name, Case: cs.Name})
			continue
		}
		k := cl.FindConst(name)
		if cl.Kind == ast.KindTrait && cl == x.class && (k == nil || lateBound) && !writtenClassName(e.Class) {
			// self::X, static::X and $this::X in a trait's body read the
			// constant of the class the body runs in.
			if ts, lacking := x.traitUserConstants(cl, name, lateBound); len(ts) > 0 {
				if lacking != nil {
					x.report(e.Name.Span(), P2004, "unknown constant %s::%s", lacking.Name, name)
					return types.TError
				}
				out = append(out, ts...)
				continue
			}
		}
		if k == nil {
			x.report(e.Name.Span(), P2004, "unknown constant %s::%s", cl.Name, name)
			return types.TError
		}
		if !x.canAccess(k.Vis, k.Class) {
			x.report(e.Name.Span(), P2108, "constant %s::%s is %s", k.Class.Name, name, k.Vis)
		}
		x.c.libraryClassConstant(e.Name.Span(), k.Class, k.Name)
		if k.Deprecated && x.c.opts.Deprecations {
			x.report(e.Name.Span(), P4006, "constant %s::%s is deprecated%s", k.Class.Name, k.Name, k.DeprecatedNote)
		}
		t := x.p().ConstantType(k)
		if lateBound && !cl.Final && !k.Final && cl.Kind != ast.KindEnum {
			t = k.Type
			if t == nil {
				t = types.TMixed
			}
		}
		out = append(out, x.bindReceiver(t, recv, k.Class))
	}
	return types.MakeUnion(out...)
}

// writtenClassName reports a class reference written as a class's name, not
// self, static or parent, nor an expression.
func writtenClassName(e ast.Expr) bool {
	n, ok := e.(*ast.Name)
	return ok && specialClassName(n) == ""
}

// traitUserConstants types a constant a trait's body reads through self,
// static or $this in each class that uses the trait, anonymous ones
// included, as PHPStan types the body in the context of each, a user that
// is a trait passing the question on to its users: the constant's value
// read through self, its declared type read late bound in a class a
// subclass may override it in (classConstFetch), an enum case as itself.
// A parent's private constant is none of the user's. A user whose own
// method replaces the one being checked does not run its body, and does
// not count. lacking is a user without the constant; nothing is returned
// when no user has it.
func (x *fnCtx) traitUserConstants(trait *program.Class, name string, lateBound bool) (out []types.Type, lacking *program.Class) {
	seen := map[string]bool{}
	var walk func(t *program.Class)
	walk = func(t *program.Class) {
		if seen[strings.ToLower(t.Name)] {
			return
		}
		seen[strings.ToLower(t.Name)] = true
		for _, u := range x.p().TraitUsers(t.Name) {
			u.Signature()
			if x.method != nil && !runsBody(u, x.method) {
				continue // the user's own method runs instead of the trait's
			}
			if u.Kind == ast.KindTrait {
				walk(u)
				continue
			}
			if cs := u.FindCase(name); cs != nil {
				out = append(out, types.EnumCase{Class: u.Name, Case: cs.Name})
				continue
			}
			k := u.FindConst(name)
			if k != nil && k.Vis == program.Private && k.Class != nil && !strings.EqualFold(k.Class.Name, u.Name) {
				k = nil // a parent's private constant: php has none for the user
			}
			if k == nil {
				if lacking == nil {
					lacking = u
				}
				continue
			}
			ct := x.p().ConstantType(k)
			if lateBound && !u.Final && !k.Final && u.Kind != ast.KindEnum {
				ct = k.Type
				if ct == nil {
					ct = types.TMixed
				}
			}
			out = append(out, x.bindReceiver(ct, types.Class{Name: u.Name}, k.Class))
		}
	}
	walk(trait)
	return out, lacking
}

// runsBody reports a class whose methods hold a trait method's body,
// under the method's name or an alias (`use T { read as readT; }`): what
// a class copies from a trait keeps its declaration.
func runsBody(c *program.Class, m *program.Method) bool {
	if m.Decl == nil {
		return true
	}
	for _, own := range c.Methods {
		if own.Decl == m.Decl {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ members

func (x *fnCtx) propertyFetch(e *ast.PropertyFetch, env *state, lenient bool) types.Type {
	delete(x.skippedChain, e) // a mark of an earlier pass over a loop: this one decides
	x.prematureRead(e, env)
	if key, ok := x.pathKey(e); ok {
		if t, ok := env.getPath(key); ok {
			// The member was narrowed: its type is known without re-checking
			// the object; only a ?-> on a never-null object is still worth a word.
			obj := x.pathType(e.Object, env)
			x.noteRecv(e, obj)
			if e.NullSafe {
				if !types.IsNullable(obj) && !types.IsMixed(obj) {
					x.report(e.S, P2307, "?-> on %s, which is never null", x.typeName(obj))
				}
			}
			if name, ok := identName(e.Name); ok {
				x.propertyThrows(types.StripNull(obj), name)
			}
			return t
		}
	}
	obj := x.expr(e.Object, env, nil)
	if x.skippedChain[e.Object] {
		return x.skipChain(e) // `$n?->a()->b` with $n null: reported at the ?->
	}
	x.noteRecv(e, obj)
	name, ok := identName(e.Name)
	if !ok {
		x.noteName(e, x.expr(e.Name, env, nil))
		x.implicitThrow()
		x.memberOnMixed(obj, e.Object.Span(), "cannot access a property on mixed")
		return types.TMixed
	}
	x.propertyThrows(types.StripNull(obj), name)
	if !e.NullSafe && types.IsNullable(obj) && !types.IsMixed(obj) {
		if x.shortCircuit[e.Object] {
			// `$a?->b->c`: PHP skips the whole chain when $a is null, and b
			// itself is never null.
			t := x.propertyType(types.StripNull(obj), name, e, env, lenient)
			x.markShortCircuit(e, t)
			return types.Nullable(t)
		}
		if inNullsafeChain(e.Object) {
			// b may be null on its own: that is reported, and the chain's
			// null is still an outcome.
			return types.Nullable(x.propertyType(obj, name, e, env, lenient))
		}
	}
	return x.propertyType(obj, name, e, env, lenient)
}

// inNullsafeChain reports a ?-> further down e's object chain, whose null
// PHP carries through the rest of the chain.
func inNullsafeChain(e ast.Expr) bool {
	for {
		switch n := e.(type) {
		case *ast.PropertyFetch:
			if n.NullSafe {
				return true
			}
			e = n.Object
		case *ast.MethodCall:
			if n.NullSafe {
				return true
			}
			e = n.Object
		default:
			return false
		}
	}
}

// skipChain records that PHP skips e and the rest of its chain: a ?-> on a
// value that is null, or a member reached through one. Its value is null.
func (x *fnCtx) skipChain(e ast.Expr) types.Type {
	if x.skippedChain == nil {
		x.skippedChain = map[ast.Expr]bool{}
	}
	x.skippedChain[e] = true
	return types.TNull
}

// markShortCircuit records that e's own value is never null, so a chain
// continuing from it may skip the null its ?-> introduced.
func (x *fnCtx) markShortCircuit(e ast.Expr, own types.Type) {
	if types.IsNullable(own) || types.IsMixed(own) {
		return
	}
	if x.shortCircuit == nil {
		x.shortCircuit = map[ast.Expr]bool{}
	}
	x.shortCircuit[e] = true
}

// enumCaseProperty types `Suit::Hearts->value` and `->name` as the
// literals they are, as PHPStan does.
func (x *fnCtx) enumCaseProperty(obj types.Type, name string) (types.Type, bool) {
	if name != "value" && name != "name" {
		return nil, false
	}
	var out []types.Type
	for _, m := range types.Members(obj) {
		ec, ok := m.(types.EnumCase)
		if !ok {
			return nil, false
		}
		if name == "name" {
			out = append(out, types.StringLit{V: ec.Case})
			continue
		}
		cl := x.p().Class(ec.Class)
		if cl == nil {
			return nil, false
		}
		cl.Signature()
		cs := cl.FindCase(ec.Case)
		if cs == nil || cs.Value == nil {
			return nil, false
		}
		out = append(out, x.c.caseValue(cl, cs, x))
	}
	if len(out) == 0 {
		return nil, false
	}
	return types.MakeUnion(out...), true
}

// caseValue is the type of a backed enum case's value, typed where the
// enum is declared (valueWhereDeclared) and kept: a case read twice, or by
// several cases, is typed once, as a constant is (program.ConstantType).
// A value met again while it is being typed reads itself, which its case
// reports (P2016, selfReference) and PHP throws for when it evaluates the
// value. There it is mixed, as a constant met again is and as PHPStan
// types it, and the typing ends.
func (c *Checker) caseValue(cl *program.Class, cs *program.EnumCase, reader *fnCtx) types.Type {
	if t, ok := c.caseTypes[cs]; ok {
		return t
	}
	if c.caseTyping[cs] {
		return types.TMixed
	}
	if c.caseTyping == nil {
		c.caseTyping = map[*program.EnumCase]bool{}
	}
	c.caseTyping[cs] = true
	t := c.valueWhereDeclared(cl.File, cl.Scope, cl, cs.Value, reader)
	delete(c.caseTyping, cs)
	if cl.File != nil { // without a file the reader types the value, as its own
		if c.caseTypes == nil {
			c.caseTypes = map[*program.EnumCase]types.Type{}
		}
		c.caseTypes[cs] = t
	}
	return t
}

// valueWhereDeclared types an expression of a declaration in another
// place, a backed enum case's value or a parameter's default, as the file
// that declares it reads it: in that file's namespace and imports, self
// and static naming cl. Typed in the reader, `PREFIX . 'h'` would look for
// the reader's PREFIX and `self::BASE` read the reader's class. What it
// draws is the declaration's own check's to report, once, in the
// declaring file, however many files read it, and is not reported here.
// Where the declaration has no file to read it in, the reader types it.
func (c *Checker) valueWhereDeclared(f *program.File, scope *program.Scope, cl *program.Class, e ast.Expr, reader *fnCtx) types.Type {
	mark := c.mark()
	defer c.rollback(mark)
	if f == nil {
		return reader.expr(e, newEnv(), nil)
	}
	saved := c.file
	c.file = f
	defer func() { c.file = saved }()
	ctx := c.newCtx(f, scope, cl, true)
	ctx.inConst = true
	return ctx.expr(e, newEnv(), nil)
}

// objectShapeProperty finds name in an object shape, or in the object
// shapes of an intersection.
func objectShapeProperty(m types.Type, name string) (types.Type, bool) {
	var shapes []types.ObjectShape
	switch m := m.(type) {
	case types.ObjectShape:
		shapes = append(shapes, m)
	case types.Intersection:
		for _, im := range m.Types {
			if sh, ok := im.(types.ObjectShape); ok {
				shapes = append(shapes, sh)
			}
		}
	}
	for _, sh := range shapes {
		for _, e := range sh.Entries {
			if e.Key == name {
				if e.Optional {
					return types.Nullable(e.Type), true
				}
				return e.Type, true
			}
		}
	}
	return nil, false
}

// propertyType looks up a property on an object type.
func (x *fnCtx) propertyType(obj types.Type, name string, e *ast.PropertyFetch, env *state, lenient bool) types.Type {
	span := e.Name.Span()
	if lit, ok := x.enumCaseProperty(obj, name); ok {
		return lit
	}
	if e.NullSafe {
		if !types.IsNullable(obj) && !types.IsMixed(obj) {
			x.report(e.S, P2307, "?-> on %s, which is never null", x.typeName(obj))
		}
		if _, isNull := obj.(types.Null); isNull {
			// `null?->p` is null, what PHP evaluates it to, and the rest
			// of the chain is skipped; PHPStan reports the access
			// (property.nonObject) and types it null, under ?? and isset()
			// too: they forgive a null on the way, not a receiver that is
			// never anything else. A loop whose variable is null on its
			// first pass reaches its fixed point through that type
			// (fixpoint).
			x.report(e.Object.Span(), P2105, "cannot access property $%s on null", name)
			return x.skipChain(e)
		}
	} else if types.IsNullable(obj) && !types.IsMixed(obj) && !lenient {
		if _, isNull := obj.(types.Null); isNull {
			x.report(e.Object.Span(), P2105, "cannot access property $%s on null", name)
			return types.TNull
		}
		x.report(e.Object.Span(), P2105, "cannot access property $%s on %s (possibly null)", name, x.typeName(obj))
	}
	base := types.StripNull(obj)
	if _, never := base.(types.Never); never {
		// A receiver that is never anything: the code is not reached, as
		// in the branch a test of a value that is only null rules out.
		// PHPStan reports nothing there, and reads the member as mixed.
		return types.TMixed
	}
	if types.IsMixed(base) {
		if _, unknown := base.(types.Unknown); unknown {
			return base // a member of what was already reported stays reported
		}
		// Under isset() and ?? too, as PHPStan has it: they forgive a
		// missing property, not a value that may be no object at all.
		x.memberOnMixed(base, e.Object.Span(), "cannot access property $%s on mixed", name)
		return types.TMixed
	}
	if !types.IsObjectLike(base) {
		if _, isObj := base.(types.Object); !isObj && !lenient {
			x.report(e.Object.Span(), P2105, "cannot access property $%s on %s", name, x.typeName(base))
		}
		return types.TMixed
	}
	var out []types.Type
	for _, m := range types.Members(base) {
		if _, isObj := m.(types.Object); isObj {
			if !lenient && !provenMember(env, e.Object, false, name) {
				// `object` declares no properties (PHPStan's property.notFound),
				// unless property_exists() said so.
				x.report(span, P2005, "unknown property object::$%s", name)
				out = append(out, types.TError) // reported: the chain goes on silently
				continue
			}
			out = append(out, types.TMixed)
			continue
		}
		if t, ok := objectShapeProperty(m, name); ok {
			// `Logger&object{records: list<string>}`: the shape names the
			// property the class does not declare (PHPStan's object shapes).
			out = append(out, t)
			continue
		}
		if sh, isShape := m.(types.ObjectShape); isShape {
			if !lenient {
				x.report(span, P2005, "unknown property $%s on %s", name, x.typeName(sh))
			}
			out = append(out, types.TError)
			continue
		}
		classes := x.classesOf(m, e.Object.Span())
		if _, inter := m.(types.Intersection); inter && len(classes) > 1 {
			// `Refusal&UnitEnum`: the property is found on any member.
			var having []*program.Class
			for _, cl := range classes {
				if cl.FindProperty(name) != nil || allowsDynamic(cl) {
					having = append(having, cl)
				}
			}
			if len(having) > 0 {
				classes = having
			}
		}
		for _, cl := range classes {
			prs, lacking, dynamic := x.classProperties(cl, name)
			if lacking != nil {
				if !lenient {
					x.report(span, P2005, "unknown property %s::$%s", lacking.Name, name)
				}
				if len(prs) == 0 && !dynamic {
					out = append(out, types.TError)
					continue
				}
			}
			if dynamic {
				out = append(out, types.TMixed)
			}
			for _, pr := range prs {
				if pr.Static {
					x.report(span, P2210, "static property %s::$%s accessed through an instance", pr.Class.Name, name)
				}
				if !x.canAccess(pr.Vis, pr.Class) {
					x.report(span, P2108, "property %s::$%s is %s", pr.Class.Name, name, pr.Vis)
				}
				if pr.Deprecated && x.c.opts.Deprecations {
					x.report(span, P4006, "property %s::$%s is deprecated", pr.Class.Name, name)
				}
				out = append(out, x.bindReceiver(pr.Type, m, pr.Class))
			}
		}
	}
	if len(out) == 0 {
		return types.TMixed
	}
	t := types.MakeUnion(out...)
	if e.NullSafe {
		x.markShortCircuit(e, t) // the member's own type, before ?-> adds its null
		if types.IsNullable(obj) {
			t = types.Nullable(t)
		}
	}
	return t
}

// classProperties is what a property name means on class cl: its
// declaration, or, for a trait the property is not declared in, the
// declarations of the trait's users (traitUserProperties), since a trait's
// body runs in each of them. lacking is the class to name as not having
// the property, nil when every class has it; dynamic says a class serves
// it anyway (__get, stdClass, #[AllowDynamicProperties]). A trait no user
// declares the property in is the class lacking it, as before traits were
// looked through.
func (x *fnCtx) classProperties(cl *program.Class, name string) (prs []*program.Property, lacking *program.Class, dynamic bool) {
	if pr := cl.FindProperty(name); pr != nil {
		return []*program.Property{pr}, nil, false
	}
	if cl.Kind == ast.KindTrait {
		found, missing, dyn := x.traitUserProperties(cl, name)
		if len(found) > 0 || dyn {
			if len(missing) > 0 {
				lacking = missing[0]
			}
			return found, lacking, dyn
		}
	}
	if allowsDynamic(cl) {
		return nil, nil, true
	}
	return nil, cl, false
}

// allowsDynamic reports whether unknown properties are fine on the class.
func allowsDynamic(cl *program.Class) bool {
	if strings.EqualFold(cl.Name, "stdClass") || cl.IsSubclassOf("stdClass") {
		return true
	}
	if cl.FindMethod("__get") != nil {
		return true
	}
	for _, g := range cl.Attrs {
		for _, a := range g.Attrs {
			if strings.EqualFold(strings.Join(a.Name.Parts, "\\"), "AllowDynamicProperties") {
				return true
			}
		}
	}
	return false
}

// staticPropertyFetch types Foo::$x and returns the property.
func (x *fnCtx) staticPropertyFetch(e *ast.StaticPropertyFetch, env *state) (types.Type, *program.Property) {
	var classes []*program.Class
	var recv types.Type
	if n, ok := e.Class.(*ast.Name); ok {
		if specialClassName(n) == "parent" && x.class != nil {
			if pc := x.class.ParentClass(); pc != nil {
				classes = []*program.Class{pc}
				recv = types.Class{Name: pc.Name, Args: x.class.ParentArgs}
			}
		} else {
			t, cl := x.classRefType(n, n.S)
			recv = t
			if cl != nil {
				classes = []*program.Class{cl}
			}
		}
	} else {
		recv = x.expr(e.Class, env, nil)
		classes = x.classesOf(recv, e.Class.Span())
	}
	if n, ok := e.Class.(*ast.Name); ok && specialClassName(n) == "parent" && len(classes) == 1 {
		x.noteRecv(e, types.Class{Name: classes[0].Name})
	} else {
		x.noteRecv(e, recv)
	}
	v, ok := e.Name.(*ast.Variable)
	if !ok {
		x.noteName(e, x.expr(e.Name, env, nil))
		return types.TMixed, nil
	}
	if _, named := e.Class.(*ast.Name); !named {
		x.memberOnMixed(recv, e.Class.Span(), "cannot access static property $%s on mixed", v.Name)
	}
	for _, cl := range classes {
		pr := cl.FindProperty(v.Name)
		if pr == nil {
			x.report(v.S, P2007, "unknown static property %s::$%s", cl.Name, v.Name)
			return types.TMixed, nil
		}
		if !pr.Static {
			x.report(v.S, P2111, "property %s::$%s is not static", pr.Class.Name, v.Name)
		}
		if !x.canAccess(pr.Vis, pr.Class) {
			x.report(v.S, P2108, "property %s::$%s is %s", pr.Class.Name, v.Name, pr.Vis)
		}
		if key, ok := x.pathKey(e); ok {
			if t, ok := env.getPath(key); ok {
				return t, pr
			}
		}
		return x.bindReceiver(pr.Type, recv, pr.Class), pr
	}
	return types.TMixed, nil
}

// narrowedByLiteralKeys reads `$a[$k]` with $k one of a few literals (a
// foreach over them) after a check narrowed those offsets one by one
// (narrowPath): the union of the narrowings, when each offset has one.
func (x *fnCtx) narrowedByLiteralKeys(e *ast.Index, env *state) (types.Type, bool) {
	v, ok := e.Index.(*ast.Variable)
	if !ok {
		return nil, false
	}
	base, ok := x.pathKey(e.Array)
	if !ok {
		return nil, false
	}
	keys := literalOffsetKeys(x.pathType(v, env))
	if keys == nil {
		return nil, false
	}
	ts := make([]types.Type, 0, len(keys))
	for _, k := range keys {
		t, found := env.getPath(base + k)
		if !found {
			return nil, false
		}
		ts = append(ts, t)
	}
	return types.MakeUnion(ts...), true
}

// shapeValueTypes lists a shape's entry types.
func shapeValueTypes(m types.Shape) []types.Type {
	out := make([]types.Type, 0, len(m.Entries))
	for _, e := range m.Entries {
		out = append(out, e.Type)
	}
	return out
}

func (x *fnCtx) index(e *ast.Index, env *state) types.Type {
	delete(x.skippedChain, e) // a mark of an earlier pass over a loop: this one decides
	if key, ok := x.pathKey(e); ok {
		if t, ok := env.getPath(key); ok {
			if mayBeObject(x.pathType(e.Array, env)) {
				x.implicitThrow() // offsetGet() runs
			}
			return t
		}
	}
	if t, ok := x.narrowedByLiteralKeys(e, env); ok {
		return t
	}
	base := x.expr(e.Array, env, nil)
	if e.Index == nil {
		x.report(e.S, P2302, "[] can only be used for writing")
		return types.TMixed
	}
	if x.keyOfSameArray(e, env) {
		// A key taken from the array itself is there (writeOffsets).
		if x.writeOffsets == nil {
			x.writeOffsets = map[*ast.Index]bool{}
		}
		x.writeOffsets[e] = true
		defer delete(x.writeOffsets, e)
	}
	return x.indexType(base, e, env, false)
}

// indexType types base[index].
func (x *fnCtx) indexType(base types.Type, e *ast.Index, env *state, lenient bool) types.Type {
	if x.skippedChain[e.Array] {
		// `$n?->all()[0]` with $n null: PHP skips the offset with the rest
		// of the chain, reported at the ?->.
		if e.Index != nil {
			x.expr(e.Index, env, nil)
		}
		return x.skipChain(e)
	}
	if mayBeObject(base) {
		x.implicitThrow() // an ArrayAccess's offsetGet() or offsetExists() runs
	}
	if e.Index == nil {
		return types.TMixed
	}
	raw, idx := x.keyExpr(e.Index, env)
	if mayBeArray(base) {
		x.nullOffset(raw, e.Index.Span())
	}
	base = arrayBounds(base)
	if types.IsMixed(base) {
		if _, unknown := base.(types.Unknown); unknown {
			return base // an offset of what was already reported stays reported
		}
		x.reportNoOffsets(e, base, raw, env)
		return types.TMixed
	}
	// A read of an offset the shapes may lack is P2321, once, and reads as
	// the entry PHPStan reads it as: the finding is the guard missing, not
	// a null to report again where the value goes.
	mayLack := !lenient && x.quietOffsets == 0 && !x.writeOffsets[e] && offsetMayBeMissing(x.arrayMembers(base, idx, e.Array.Span()), idx)
	if mayLack {
		x.reportMayLack(e.Index.Span(), raw, base, false)
	}
	lits := keyLiterals(idx)
	if len(lits) < 2 {
		lits = nil
	} else if !lenient && x.quietOffsets == 0 && !x.writeOffsets[e] && keyMissingEverywhere(base, idx) {
		x.report(e.Index.Span(), P2313, "offset %s does not exist on %s", x.typeName(raw), x.typeName(base))
	}
	var out []types.Type
	// The arrays that do not have the key, reported once after the loop:
	// one mistake, one finding, however many shapes the array may be.
	var lacking []types.Type
	for _, m := range types.Members(base) {
		if types.IsTextBytes(m) {
			// One byte of UTF-8 text is binary data: it may be part of a
			// character, as `$b[$i]` on bytes is (strings.md §2).
			out = append(out, types.TBytes)
			continue
		}
		switch m := m.(type) {
		case types.Shape:
			if lits != nil {
				// A key that is one of a few literals reads the entries
				// they name; under isset() and ?? also the null of one
				// that may be missing.
				var ts []types.Type
				missing := m.Unsealed
				for _, k := range lits {
					entry, ok := constantEntry(m, k)
					if !ok {
						missing = true
						continue
					}
					ts = append(ts, entry.Type)
					missing = missing || entry.Optional
				}
				if m.Unsealed {
					ts = append(ts, types.TMixed)
				}
				if lenient && missing {
					ts = append(ts, types.TNull)
				}
				if len(ts) == 0 && !lenient {
					ts = append(ts, types.TError) // reported above
				}
				out = append(out, types.MakeUnion(ts...))
				continue
			}
			if mayLack {
				if entry, ok := constantEntry(m, idx); ok {
					out = append(out, entry.Type)
					continue
				}
				if !m.Unsealed {
					continue // a shape without the key adds no value
				}
			}
			t, found, certain := shapeOffset(m, idx)
			if !found {
				if !lenient && !m.Unsealed && certain {
					lacking = append(lacking, m)
				}
				switch {
				case m.Unsealed:
					out = append(out, types.TMixed)
				case certain && !lenient:
					out = append(out, types.TError) // reported above: one finding, no null to use next
				case certain:
					out = append(out, types.TNull)
				default:
					// A key the shape may or may not have: one of its values,
					// or nothing (`$counts[$key] ?? 0` on `[]` starts at 0).
					out = append(out, types.MakeUnion(append(shapeValueTypes(m), types.TNull)...))
				}
				continue
			}
			out = append(out, t)
		case types.Array:
			if !types.IsMixed(raw) && !types.Overlaps(idx, m.Key, x.p()) && !lenient {
				lacking = append(lacking, m)
			}
			out = append(out, m.Value)
		case types.String, types.StringLit:
			out = append(out, types.TString)
		case types.Bytes:
			// One byte, still binary: `ord($b[$i])` reads it (strings.md §2).
			out = append(out, types.TBytes)
		case types.Null:
			// A reference to an offset of null makes null an array, as
			// PHP does without a word.
			if !lenient && !x.writeOffsets[e] {
				x.report(e.Array.Span(), P2302, "cannot use null as an array")
			}
			out = append(out, types.TNull)
		case types.Class, types.Static, types.This, types.Intersection, types.Param:
			if p, isParam := m.(types.Param); isParam && openParam(p) {
				// `T` or `T of object`: what arrives may have no offsets.
				x.reportNoOffsets(e, m, raw, env)
				out = append(out, types.TMixed)
				continue
			}
			classes := x.classesOf(m, e.Array.Span())
			var access *program.Class
			for _, cl := range classes {
				if cl.IsSubclassOf("ArrayAccess") {
					access = cl // `Countable&ArrayAccess`: one member is enough
					break
				}
			}
			switch {
			case access != nil:
				if g := access.FindMethod("offsetGet"); g != nil {
					out = append(out, x.bindReceiver(g.Return, m, g.Class))
				} else {
					out = append(out, types.TMixed)
				}
			case len(classes) > 0:
				// An interface or abstract class too, and under isset() and
				// ??, as PHPStan reports them: a value of the type need not
				// be an ArrayAccess.
				x.report(e.Array.Span(), P2302, "cannot use %s as an array", classTypeName(x, m, classes))
				out = append(out, types.TMixed)
			default:
				out = append(out, types.TMixed)
			}
		case types.Object, types.Iterable:
			// An ArrayAccess, or an array for iterable, is one possibility
			// among others that have no offsets.
			x.reportNoOffsets(e, m, raw, env)
			out = append(out, types.TMixed)
		case types.Unknown:
			out = append(out, types.TMixed)
		default:
			if !lenient {
				x.report(e.Array.Span(), P2302, "cannot use %s as an array", x.typeName(m))
			}
			out = append(out, types.TMixed)
		}
	}
	if len(lacking) > 0 {
		x.report(e.Index.Span(), P2313, "offset %s does not exist on %s", x.typeName(raw), x.typeName(types.MakeUnion(lacking...)))
	}
	return types.MakeUnion(out...)
}

// memberOnMixed reports a member access — a method call, a property, a
// class constant or static member through `$x::` — on mixed, as PHPStan
// does at level max (method.nonObject, property.nonObject and their static
// siblings): the value may be no object. An already reported value (the
// error type) and other types are left to the caller.
func (x *fnCtx) memberOnMixed(t types.Type, span ast.Span, format string, args ...any) {
	if _, isMixed := t.(types.Mixed); isMixed {
		x.report(span, P2105, format, args...)
	}
}

// reportNoOffsets reports an offset access on a value not known to have
// offsets — mixed, object, iterable, an open type parameter — as PHPStan
// does at level max (offsetAccess.nonOffsetAccessible): one of the values
// PHP refuses an offset on may be what arrives. It is reported inside
// isset(), ?? and empty() too, where PHP keeps quiet about a missing key
// but not about a value that has no keys; a narrowed path (`is_array($m)`,
// `isset($m['k'])` for that same offset) never gets here. An undefined
// variable reads as mixed under isset() and on the left of a nested write,
// but holds no value whose offsets could be refused: PHP creates the array.
func (x *fnCtx) reportNoOffsets(e *ast.Index, base, raw types.Type, env *state) {
	if x.undefinedRoot(e.Array, env) {
		return
	}
	if e.Index == nil {
		x.report(e.Array.Span(), P2302, "cannot access an offset on %s", x.typeName(base))
		return
	}
	x.report(e.Array.Span(), P2302, "cannot access offset %s on %s", x.typeName(raw), x.typeName(base))
}

// undefinedRoot reports whether an offset chain `$v[…][…]` starts at a
// variable that is not defined at this point.
func (x *fnCtx) undefinedRoot(e ast.Expr, env *state) bool {
	for {
		switch n := e.(type) {
		case *ast.Index:
			e = n.Array
		case *ast.Paren:
			e = n.Expr
		case *ast.Variable:
			if n.Name == "this" || superglobal(n.Name) {
				return false
			}
			_, defined := env.get(n.Name)
			return !defined
		default:
			return false
		}
	}
}

// offsetsOfVariable reports a variable, or an offset of one at any depth.
func offsetsOfVariable(e ast.Expr) bool {
	for {
		switch n := unparen(e).(type) {
		case *ast.Index:
			e = n.Array
		case *ast.Variable:
			return n.Name != "this"
		default:
			return false
		}
	}
}

// arrayBounds is t with each type parameter whose bound is an array read
// as that bound: an offset of `T of array{id: int, name?: string}` is one of
// the shape, as PHPStan reads it.
func arrayBounds(t types.Type) types.Type {
	return types.Map(t, func(m types.Type) types.Type {
		if p, ok := m.(types.Param); ok {
			switch p.Bound.(type) {
			case types.Shape, types.Array:
				return p.Bound
			}
		}
		return m
	})
}

// arrayMembers is t with each ArrayAccess in it read for the offset idx as
// PHPStan's hasOffsetValueType() reads one: an object whose offsetSet()
// takes no such key has none, and reads as a sealed empty shape; any other
// may have it, and reads as a shape that has it, so that it neither says
// the key might not exist nor lets it not exist anywhere: whether it might
// comes from the arrays beside it alone (offsetMayBeMissing).
// `array{name: string}|ArrayAccess<string, string>` has 'name', and
// `list{int, int}|ArrayAccess<string, int>` might not have 0. Any other
// object stays: an offset of it is P2302's.
func (x *fnCtx) arrayMembers(t, idx types.Type, span ast.Span) types.Type {
	var out []types.Type
	for _, m := range types.Members(t) {
		switch m.(type) {
		case types.Class, types.Static, types.This, types.Intersection:
			if keys, ok := x.offsetKeys(m, span); ok {
				if types.Overlaps(normaliseKey(idx), keys, x.p()) {
					out = append(out, shapeHaving(idx))
				} else {
					out = append(out, types.Shape{})
				}
				continue
			}
		}
		out = append(out, m)
	}
	return types.MakeUnion(out...)
}

// offsetKeys is the key type an ArrayAccess takes, the first parameter of
// its offsetSet() as the object binds it (`ArrayAccess<string, int>`'s
// string); false for an object that is no ArrayAccess.
func (x *fnCtx) offsetKeys(m types.Type, span ast.Span) (types.Type, bool) {
	for _, cl := range x.classesOf(m, span) {
		if !cl.IsSubclassOf("ArrayAccess") {
			continue
		}
		if s := cl.FindMethod("offsetSet"); s != nil && len(s.Params) >= 1 && s.Params[0].Type != nil {
			return x.bindReceiver(s.Params[0].Type, m, s.Class), true
		}
		return types.TMixed, true
	}
	return nil, false
}

// shapeHaving is a shape with a certain entry under each literal of the
// key idx, which a read of idx finds there.
func shapeHaving(idx types.Type) types.Shape {
	var s types.Shape
	for _, k := range types.Members(normaliseKey(idx)) {
		switch k := k.(type) {
		case types.StringLit:
			s.Entries = append(s.Entries, types.ShapeEntry{Key: k.V, Type: types.TMixed})
		case types.Int:
			if k.Lo == k.Hi {
				s.Entries = append(s.Entries, types.ShapeEntry{IsInt: true, IntKey: k.Lo, Type: types.TMixed})
			}
		}
	}
	return s
}

// markWritten marks the offset e, `$a['k']['l']`, as written while e is
// evaluated (writeOffsets): PHP creates it for the reference. The arrays
// it is in, `$a['k']`, are read, and one that may be missing is P2321, as
// PHPStan reports it. The function it returns unmarks the offset.
func (x *fnCtx) markWritten(e ast.Expr) func() {
	n, ok := unparen(e).(*ast.Index)
	if !ok {
		return func() {}
	}
	if x.writeOffsets == nil {
		x.writeOffsets = map[*ast.Index]bool{}
	}
	x.writeOffsets[n] = true
	return func() { delete(x.writeOffsets, n) }
}

// offsetMayBeMissing reports a constant offset that the array may lack,
// as PHPStan's offsetAccess.notFound has it at level max ("might not
// exist"): every value of the type is a shape, and one of them has the key
// as an optional entry, or does not name it, unsealed or beside another
// shape that has it. A key that is one of a few literals, `'a'|'c'`, may
// be missing where one of them may be, and another is there or the shape
// is unsealed. Where no value can have the key it does not exist, P2313's;
// null is P2302's, and a general array PHPStan does not hold to its keys,
// so a member of either leaves it to them.
func offsetMayBeMissing(base, idx types.Type) bool {
	lits := keyLiterals(normaliseKey(idx))
	if lits == nil {
		return false
	}
	maybe, has, open := false, false, false
	for _, m := range types.Members(base) {
		s, ok := m.(types.Shape)
		if !ok {
			return false
		}
		for _, k := range lits {
			entry, found := constantEntry(s, k)
			switch {
			case found:
				has = true
				maybe = maybe || entry.Optional
			case s.Unsealed:
				maybe, open = true, true
			default:
				maybe = true // a shape without the key, beside others
			}
		}
	}
	return maybe && (has || open)
}

// keyMissingEverywhere reports a key that is one of a few literals none of
// which any value of the type has: every value a sealed shape without
// them, PHPStan's "Offset 'y'|'z' does not exist".
func keyMissingEverywhere(base, idx types.Type) bool {
	lits := keyLiterals(normaliseKey(idx))
	if len(lits) < 2 {
		return false
	}
	members := types.Members(base)
	for _, m := range members {
		s, ok := m.(types.Shape)
		if !ok || s.Unsealed {
			return false
		}
		for _, k := range lits {
			if _, found := constantEntry(s, k); found {
				return false
			}
		}
	}
	return len(members) > 0
}

// keyLiterals lists the literals a key stands for: itself where it is one
// string or int literal, its members where it is a union of them; nil for
// any other key.
func keyLiterals(key types.Type) []types.Type {
	var out []types.Type
	for _, m := range types.Members(key) {
		switch k := m.(type) {
		case types.StringLit:
			out = append(out, k)
		case types.Int:
			if k.Lo != k.Hi {
				return nil
			}
			out = append(out, k)
		default:
			return nil
		}
	}
	return out
}

// offsetMayLack reports P2321 for an offset an argument by reference reads
// where no test stands guard: what index() would report of the read.
func (x *fnCtx) offsetMayLack(e *ast.Index, env *state) {
	if e.Index == nil || x.writeOffsets[e] {
		return
	}
	if key, ok := x.pathKey(e); ok {
		if _, narrowed := env.getPath(key); narrowed {
			return
		}
	}
	base := x.pathType(e.Array, env)
	if raw := x.pathType(e.Index, env); offsetMayBeMissing(base, raw) {
		x.reportMayLack(e.Index.Span(), raw, base, false)
	}
}

// reportMayLack reports P2321 for the offset raw of base, the offset
// normalised as PHP stores it ('1' is 1). A destructuring has no place for
// a default, so its message leaves ?? out.
func (x *fnCtx) reportMayLack(at ast.Span, raw, base types.Type, destructuring bool) {
	if destructuring {
		x.report(at, P2321, "offset %s might not exist on %s: test it with isset() or array_key_exists() before destructuring", x.typeName(normaliseKey(raw)), x.typeName(base))
		return
	}
	x.report(at, P2321, "offset %s might not exist on %s: test it with isset() or array_key_exists() first, or give ?? a default", x.typeName(normaliseKey(raw)), x.typeName(base))
}

// constantEntry is the entry of a shape a constant key names.
func constantEntry(s types.Shape, idx types.Type) (types.ShapeEntry, bool) {
	switch k := normaliseKey(idx).(type) {
	case types.StringLit:
		for _, e := range s.Entries {
			if !e.IsInt && e.Key == k.V {
				return e, true
			}
		}
	case types.Int:
		for _, e := range s.Entries {
			if k.Lo == k.Hi && e.IsInt && e.IntKey == k.Lo {
				return e, true
			}
		}
	}
	return types.ShapeEntry{}, false
}

// shapeOffset finds the entry for a literal key; certain says the key was
// a literal (so a miss is definite).
func shapeOffset(s types.Shape, idx types.Type) (t types.Type, found, certain bool) {
	idx = normaliseKey(idx)
	switch k := idx.(type) {
	case types.StringLit:
		for _, e := range s.Entries {
			if !e.IsInt && e.Key == k.V {
				if e.Optional {
					return types.Nullable(e.Type), true, true
				}
				return e.Type, true, true
			}
		}
		return nil, false, true
	case types.Int:
		if k.Lo == k.Hi {
			for _, e := range s.Entries {
				if e.IsInt && e.IntKey == k.Lo {
					if e.Optional {
						return types.Nullable(e.Type), true, true
					}
					return e.Type, true, true
				}
			}
			return nil, false, true
		}
	}
	// Unknown key: union of every entry.
	var vs []types.Type
	for _, e := range s.Entries {
		vs = append(vs, e.Type)
	}
	if len(vs) == 0 {
		return nil, false, false
	}
	return types.MakeUnion(vs...), true, false
}

// ------------------------------------------------------------------ operators

func (x *fnCtx) cloneExpr(e *ast.Clone, env *state) types.Type {
	var t types.Type
	if e.Expr != nil {
		t = x.expr(e.Expr, env, nil)
	} else if e.Args != nil && len(e.Args.Args) > 0 {
		t = x.expr(e.Args.Args[0].Value, env, nil)
		for _, a := range e.Args.Args[1:] {
			x.expr(a.Value, env, nil)
		}
	} else {
		return types.TObject
	}
	if !types.IsMixed(t) && !types.IsObjectLike(t) {
		x.report(e.S, P2310, "cannot clone %s", x.typeName(t))
	}
	return t
}

func (x *fnCtx) unary(e *ast.Unary, env *state) types.Type {
	switch e.Op {
	case token.Kind('!'):
		thenEnv, elseEnv := x.cond(e.Expr, env)
		*env = *join(thenEnv, elseEnv)
		return types.TBool
	case token.Kind('-'), token.Kind('+'):
		t := x.expr(e.Expr, env, nil)
		if isGMP(t) {
			return t // GMP overloads them
		}
		if lit, ok := types.IsIntLit(t); ok && e.Op == token.Kind('-') {
			if lit == math.MinInt64 {
				return types.TFloat // -PHP_INT_MIN is past PHP_INT_MAX
			}
			return types.IntLit(-lit)
		}
		if lit, ok := t.(types.FloatLit); ok && e.Op == token.Kind('-') {
			return types.FloatLit{V: -lit.V}
		}
		return x.arithResult(t, t, e.S, "unary "+string(rune(e.Op)))
	case token.Kind('~'):
		t := x.expr(e.Expr, env, nil)
		if isGMP(t) {
			return t
		}
		if types.IsSubtype(t, types.TString, x.p()) && !hasBytes(t) {
			return types.TString
		}
		if types.IsSubtype(t, types.MakeUnion(types.TString, types.TBytes), x.p()) {
			return types.TBytes // the bytes flipped, no text
		}
		// PHP flips the bits of an int, a float it truncates or a
		// string's bytes, and throws on anything else: bool and null too,
		// which arithmetic takes as numbers. A union that holds a GMP is
		// refused too, as PHPStan refuses it.
		if !types.IsMixed(t) && !types.IsSubtype(t, types.MakeUnion(types.TInt, types.TFloat, types.TString, types.TBytes), x.p()) {
			x.report(e.S, P2301, "operator ~ on %s", x.typeName(t))
		}
		if lit, ok := types.IsIntLit(t); ok {
			return types.IntLit(^lit)
		}
		switch {
		case hasBytes(t):
			return types.MakeUnion(types.TInt, types.TBytes)
		case types.Overlaps(t, types.TString, x.p()):
			return types.MakeUnion(types.TInt, types.TString)
		}
		return types.TInt
	case token.Kind('@'):
		return x.expr(e.Expr, env, nil)
	}
	return x.expr(e.Expr, env, nil)
}

func (x *fnCtx) cast(e *ast.Cast, env *state) types.Type {
	t := x.expr(e.Expr, env, nil)
	// A long spelling, (integer) or (real), is lexed as the canonical
	// cast's token and typed as that cast: withdrawnSpellings reports the
	// spelling, and nothing else follows from it.
	switch e.Op {
	case token.T_INT_CAST:
		if types.IsObjectLike(t) && !types.IsMixed(t) && !isGMP(t) { // a GMP casts to its number
			x.report(e.S, P2309, "cannot cast %s to int", x.typeName(t))
		}
		if lit, ok := t.(types.StringLit); ok {
			if n, err := strconv.ParseInt(lit.V, 10, 64); err == nil {
				return types.IntLit(n)
			}
		}
		if _, ok := types.IsIntLit(t); ok {
			return t
		}
		return types.TInt
	case token.T_DOUBLE_CAST:
		if types.IsObjectLike(t) && !types.IsMixed(t) && !isGMP(t) {
			x.report(e.S, P2309, "cannot cast %s to float", x.typeName(t))
		}
		return types.TFloat
	case token.T_BYTES_CAST:
		// (bytes) retypes a string as binary data, free at runtime; it is
		// how a PHP API's binary-in-a-string enters the typed world and
		// how the stdlib builds a bytes value (design/strings.md §2).
		for _, m := range types.Members(t) {
			if types.IsTextBytes(m) {
				continue // string&bytes: bytes already
			}
			switch m.(type) {
			case types.String, types.StringLit, types.ClassString, types.Bytes, types.Unknown, types.Mixed:
			default:
				x.report(e.S, P2309, "cannot cast %s to bytes: only a string holds binary data", x.typeName(t))
				return types.TBytes
			}
		}
		return types.TBytes
	case token.T_STRING_CAST:
		return types.MakeUnion(x.stringCast(e, t)...)
	case token.T_BOOL_CAST:
		return types.TBool
	case token.T_ARRAY_CAST:
		if types.IsMixed(t) {
			return types.TArray
		}
		var out []types.Type
		for _, m := range types.Members(t) {
			switch m := m.(type) {
			case types.Array, types.Shape:
				out = append(out, m)
			case types.Null:
				out = append(out, types.Shape{List: true})
			default:
				if types.IsObjectLike(m) {
					out = append(out, types.Array{Key: types.TString, Value: types.TMixed})
				} else {
					out = append(out, types.Shape{List: true, Entries: []types.ShapeEntry{{IsInt: true, Type: m}}})
				}
			}
		}
		return types.MakeUnion(out...)
	case token.T_OBJECT_CAST:
		return types.NewClass("stdClass")
	case token.T_UNSET_CAST:
		return types.TNull
	}
	return types.TMixed
}

// stringCast types (string) of a value of type t, member by member: an
// int is a decimal-int-string (a literal its literal), as PHPStan types
// it; a bool is the empty string or '1', null the empty string; a string
// stays what it is; anything else that converts is a string. (string) is
// also the explicit, unchecked conversion of bytes for the PHP APIs that
// take binary in a string parameter (strings.md §5). The members are
// returned unjoined for keyExpr, which must see the int part before a
// union absorbs it.
func (x *fnCtx) stringCast(e *ast.Cast, t types.Type) []types.Type {
	x.checkStringable(types.Subtract(t, types.TBytes, x.p()), e.Expr.Span(), nil)
	if types.IsMixed(t) {
		return []types.Type{types.TString}
	}
	var out []types.Type
	for _, m := range types.Members(t) {
		switch m := m.(type) {
		case types.Int:
			if m.Lo == m.Hi {
				out = append(out, types.StringLit{V: strconv.FormatInt(m.Lo, 10)})
			} else {
				out = append(out, types.TDecimalIntString)
			}
		case types.BoolLit:
			if m.V {
				out = append(out, types.StringLit{V: "1"})
			} else {
				out = append(out, types.StringLit{V: ""})
			}
		case types.Bool:
			out = append(out, types.StringLit{V: ""}, types.StringLit{V: "1"})
		case types.Null:
			out = append(out, types.StringLit{V: ""})
		case types.String, types.StringLit, types.ClassString:
			out = append(out, m)
		case types.Never:
		default:
			out = append(out, types.TString)
		}
	}
	return out
}

func (x *fnCtx) incDec(e *ast.IncDec, env *state) types.Type {
	t := x.expr(e.Expr, env, nil)
	var result types.Type
	switch {
	case types.IsSubtype(t, types.TInt, nil):
		// A step is the sum or the difference with 1, typed as that one
		// is: 5 stepped is 6, and the last integer stepped is a float, so
		// that a second step from 9223372036854775806 is one too.
		op := token.Kind('-')
		if e.Inc {
			op = token.Kind('+')
		}
		result = x.binaryTypes(op, t, types.IntLit(1), e)
	case types.IsSubtype(t, types.TFloat, nil):
		result = types.TFloat
	case types.IsSubtype(t, types.MakeUnion(types.TInt, types.TFloat), nil):
		// A counter that may have passed the last integer, or an int|float
		// as declared: PHP steps either, and the result is one of the two.
		result = types.MakeUnion(types.TInt, types.TFloat)
	case numbersOrStrings(t):
		// Each member as PHP turns it: a numeric string becomes the number
		// it reads as, so `$n = '5'; $n++; $n++;` is 6 and then 7. A string
		// that is no number, or may be none, PHP steps as text, which it
		// deprecates, and PHPStan refuses the operand (postInc.type,
		// postDec.type): one finding stands for the mistake, whichever way
		// the deprecations are switched, and the operand keeps its type so
		// that nothing else follows from the step.
		var refused, all bool
		var since string
		if result, refused, all, since = x.stepMembers(t, e); refused {
			// The message names the release that deprecated the step where
			// the operand's literals tell it, and none where they do not: --
			// is 8.3's, ++ is 8.3's of a string that is not alphanumeric,
			// which str_increment() refuses too, and 8.5's of one that is.
			verb, what, advice := "decrement", "a string that is no number", "call str_decrement()"
			if e.Inc {
				verb, advice = "increment", "call str_increment()"
				if since == "8.3" {
					what, advice = "a string that is neither a number nor alphanumeric", ""
				}
			}
			if since != "" {
				since = " since PHP " + since
			}
			msg := "the step of " + what + " is deprecated" + since
			if !all {
				msg = "it may be " + what + ", whose step is deprecated" + since
				advice = strings.TrimPrefix(advice+", or make it a number first", ", or ")
			}
			if advice != "" {
				msg += "; " + advice
			}
			x.report(e.S, P2301, "cannot %s %s: %s", verb, x.typeName(t), msg)
			result = t
			if types.IsSubtype(t, types.TString, nil) {
				result = types.TString
			}
		}
	case types.IsSubtype(t, types.TNull, nil):
		if e.Inc {
			result = types.IntLit(1)
		} else {
			result = types.TNull
		}
	case types.IsMixed(t):
		result = types.TMixed
	case isGMP(t):
		result = t // GMP overloads ++ and --, as its other operators
	case besideGMP(t):
		// A GMP beside other types is stepped as the number PHPStan
		// converts it to, and the others are to be numbers too.
		result = types.MakeUnion(types.TInt, types.TFloat)
		if !types.IsSubtype(gmpAsNumber(t), result, nil) {
			x.report(e.S, P2301, "cannot increment or decrement %s", x.typeName(t))
			result = t
		}
	default:
		x.report(e.S, P2301, "cannot increment or decrement %s", x.typeName(t))
		result = t
	}
	x.assignTo(e.Expr, result, env, e.S)
	if e.Prefix {
		return result
	}
	return t
}

func (x *fnCtx) binary(e *ast.Binary, env *state, expected types.Type) types.Type {
	switch e.Op {
	case token.T_BOOLEAN_AND, token.T_LOGICAL_AND, token.T_BOOLEAN_OR, token.T_LOGICAL_OR, token.T_LOGICAL_XOR:
		thenEnv, elseEnv := x.cond(e, env)
		*env = *join(thenEnv, elseEnv)
		return types.TBool
	case token.T_COALESCE:
		return x.coalesce(e, env, expected)
	case token.T_PIPE:
		x.pipeLeft++
		left := x.expr(e.Left, env, nil)
		x.pipeLeft--
		return x.pipe(e, left, env, expected)
	}
	l := x.expr(e.Left, env, nil)
	r := x.expr(e.Right, env, nil)
	x.certainOperands = types.Equal(x.nativeOr(e.Left, l, env), l) && types.Equal(x.nativeOr(e.Right, r, env), r)
	if e.Op == token.T_IS_IDENTICAL || e.Op == token.T_IS_NOT_IDENTICAL {
		if !types.IsMixed(l) && !types.IsMixed(r) && !types.Overlaps(l, r, x.p()) {
			x.disjointComparison(e, l, r, env)
		}
		return types.TBool
	}
	x.c.noteArithmetic(e, l, r, x.untouched)
	x.arith = arithOperands{e.Left, e.Right}
	defer func() { x.arith = arithOperands{} }()
	return x.binaryTypes(e.Op, l, r, e)
}

// disjointComparison reports a strict comparison of operands whose types
// do not overlap (P2305), and says whether its result is known: a
// comparison of values PHP's own types keep apart is always false (`!==`
// always true), while one of binary data with text, both PHP strings, is
// true when the two hold the same bytes, and the finding is that the two
// kinds are compared (strings.md §2). Disjoint docblock types alone are
// not reported, as PHPStan does not with treatPhpDocTypesAsCertain off,
// and still decide the result for the flow.
func (x *fnCtx) disjointComparison(e *ast.Binary, l, r types.Type, env *state) (known bool) {
	if !x.nativeOverlap(e.Left, l, e.Right, r, env) {
		word := "false"
		if e.Op == token.T_IS_NOT_IDENTICAL {
			word = "true"
		}
		x.report(e.S, P2305, "strict comparison between %s and %s is always %s", x.typeName(l), x.typeName(r), word)
		return true
	}
	if bytesAgainstText(l, r) {
		x.report(e.S, P2305, "strict comparison between %s and %s compares binary data with text, which may still hold the same bytes: %s", x.typeName(l), x.typeName(r), convertBytesHint(e.Left, l, e.Right))
		return false
	}
	return true
}

// convertBytesHint says how to compare binary data with text, naming the
// bytes operand, the one of left and right whose type is bytes (lt is
// left's), as written when it is a variable.
func convertBytesHint(left ast.Expr, lt types.Type, right ast.Expr) string {
	side := right
	if hasBytes(lt) {
		side = left
	}
	if v, ok := bytesVariable(side); ok {
		return "write (bytes) on the text to compare bytes, or test mb_check_encoding(" + v + ", 'UTF-8') first to compare text (the bytes are string&bytes after it)"
	}
	return "write (bytes) on the text to compare bytes, or test the bytes with mb_check_encoding() first to compare text (they are string&bytes after it)"
}

// bytesVariable is a variable as written, `$raw`, for a message to name;
// false for any other expression, which a message calls "the bytes".
func bytesVariable(e ast.Expr) (string, bool) {
	for {
		switch p := e.(type) {
		case *ast.Paren:
			e = p.Expr
			continue
		case *ast.EncapsBraced:
			e = p.Expr
			continue
		}
		break
	}
	if v, ok := e.(*ast.Variable); ok {
		return "$" + v.Name, true
	}
	return "", false
}

// operands are the two sides of a binary operation or a compound
// assignment; nil for any other node.
func operands(n ast.Node) (left, right ast.Expr) {
	switch n := n.(type) {
	case *ast.Binary:
		return n.Left, n.Right
	case *ast.Assign:
		return n.Target, n.Value
	}
	return nil, nil
}

// binaryTypes computes the result of an operator on typed operands.
// bytesAgainstText reports a comparison of binary data with text. Both are
// PHP strings, so the native types overlap and the comparison may even be
// true at runtime; the type confusion is still the finding (strings.md §2).
func bytesAgainstText(l, r types.Type) bool {
	has := func(t types.Type) (b, text bool) {
		for _, m := range types.Members(t) {
			switch m.(type) {
			case types.Bytes:
				b = true
			case types.String, types.StringLit, types.ClassString:
				text = true
			}
		}
		return
	}
	lb, lt := has(l)
	rb, rt := has(r)
	return (lb && rt) || (rb && lt)
}

// equalTextBytes is the value two operands, binary data and text, have
// where they are identical: text held as bytes, string&bytes (the text's
// own literal kept, `'GIF8'&bytes`), and whatever else both may be.
func (x *fnCtx) equalTextBytes(l, r types.Type) types.Type {
	isText := func(t types.Type) bool {
		switch t.(type) {
		case types.String, types.StringLit, types.ClassString:
			return true
		}
		return false
	}
	var out []types.Type
	for _, a := range types.Members(l) {
		for _, b := range types.Members(r) {
			_, aBytes := a.(types.Bytes)
			_, bBytes := b.(types.Bytes)
			switch {
			case aBytes && isText(b):
				out = append(out, types.MakeIntersection(b, types.TBytes))
			case bBytes && isText(a):
				out = append(out, types.MakeIntersection(a, types.TBytes))
			default:
				if m := types.Intersect(a, b, x.p()); !isNeverType(m) {
					out = append(out, m)
				}
			}
		}
	}
	return types.MakeUnion(out...)
}

// concatBytes reports an operand of `.` that is binary data: bytes, maybe
// null (which `.` reads as the empty string), or a value already reported.
// string&bytes, bytes that are text, is binary data too.
func concatBytes(t types.Type) bool {
	found := false
	for _, m := range types.Members(t) {
		if types.IsTextBytes(m) {
			found = true
			continue
		}
		switch m.(type) {
		case types.Bytes:
			found = true
		case types.Null, types.Never:
		case types.Unknown:
			if !types.Equal(m, types.TError) {
				return false
			}
		default:
			return false
		}
	}
	return found
}

// allTextBytes reports a type whose every member is string&bytes.
func allTextBytes(t types.Type) bool {
	for _, m := range types.Members(t) {
		if !types.IsTextBytes(m) {
			return false
		}
	}
	return true
}

// hasBytes reports a type with a bytes member.
func hasBytes(t types.Type) bool {
	for _, m := range types.Members(t) {
		if _, ok := m.(types.Bytes); ok {
			return true
		}
	}
	return false
}

func (x *fnCtx) binaryTypes(op token.Kind, l, r types.Type, e ast.Node) types.Type {
	if (op == token.Kind('/') || op == token.Kind('%')) && mayBeZero(r) {
		x.explicitThrow(types.NewClass("DivisionByZeroError"), false, e.Span())
	}
	span := e.Span()
	if gmpArithmetic[op] {
		if isGMP(l) || isGMP(r) {
			return x.gmpResult(op, l, r, span)
		}
		if op == token.T_POW && (besideGMP(l) || besideGMP(r)) {
			// A power of a union that holds a GMP, or to one, is an error
			// to PHPStan whatever the other operand is.
			t := l
			if !besideGMP(l) {
				t = r
			}
			x.report(span, P2301, "operator ** on %s", x.typeName(t))
			return types.TError
		}
	}
	switch op {
	case token.Kind('.'):
		// `.` joins two values of the same kind (strings.md §2): binary
		// joined to binary stays binary, as `.=` on a bytes buffer does;
		// binary mixed with text is the confusion bytes exists to catch,
		// reported once, its result already reported. An operand already
		// reported (`$s->bytes()` on a string) joins the other side's kind.
		lb, rb := concatBytes(l), concatBytes(r)
		if (lb || types.Equal(l, types.TError)) && (rb || types.Equal(r, types.TError)) && (lb || rb) {
			if allTextBytes(l) && allTextBytes(r) {
				return types.TTextBytes // UTF-8 joined to UTF-8 is UTF-8
			}
			return types.TBytes
		}
		if hasBytes(l) || hasBytes(r) {
			left, right := operands(e)
			bytesSide := right
			if hasBytes(l) {
				bytesSide = left
			}
			if v, ok := bytesVariable(bytesSide); ok {
				x.report(span, P2530, "cannot concatenate bytes and text: mb_check_encoding(%[1]s, 'UTF-8') checks the bytes are UTF-8 text, after which they are string&bytes and join text; (string) %[1]s converts them unchecked; (bytes) takes text as its UTF-8 bytes, and (bytes) '' starts a binary buffer", v)
			} else {
				x.report(span, P2530, "cannot concatenate bytes and text: mb_check_encoding() on the bytes checks they are UTF-8 text, after which they are string&bytes and join text; a (string) cast converts them unchecked; (bytes) takes text as its UTF-8 bytes, and (bytes) '' starts a binary buffer")
			}
			x.checkStringable(types.Subtract(l, types.TBytes, x.p()), span, nil)
			x.checkStringable(types.Subtract(r, types.TBytes, x.p()), span, nil)
			return types.TError
		}
		x.checkStringable(l, span, nil)
		x.checkStringable(r, span, nil)
		if ll, ok := l.(types.StringLit); ok {
			if rl, ok := r.(types.StringLit); ok {
				return types.StringLit{V: ll.V + rl.V}
			}
		}
		if isNonEmptyString(l) || isNonEmptyString(r) {
			return types.String{NonEmpty: true}
		}
		return types.TString
	case token.Kind('+'):
		if isArrayType(l) || isArrayType(r) {
			if isArrayType(l) && isArrayType(r) {
				// An array or a GMP is added to an array only: beside
				// anything else PHPStan has an error, where two unions of
				// arrays and numbers pass.
				if besideGMP(l) && !types.IsSubtype(r, types.TArray, x.p()) || besideGMP(r) && !types.IsSubtype(l, types.TArray, x.p()) {
					x.report(span, P2301, "cannot add %s and %s", x.typeName(l), x.typeName(r))
				}
				return boundedPlus(l, r)
			}
			if !types.IsMixed(l) && !types.IsMixed(r) {
				x.report(span, P2301, "cannot add %s and %s", x.typeName(l), x.typeName(r))
			}
			return types.TArray
		}
		if a, ok := types.IsIntLit(l); ok {
			if b, ok := types.IsIntLit(r); ok {
				if s, ok := addInt(a, b); ok {
					return types.IntLit(s)
				}
				return types.TFloat // past the integers, PHP's result is a float
			}
		}
		return pastTheIntegers(op, l, r, x.arithResult(l, r, span, "+"))
	case token.Kind('-'), token.Kind('*'), token.T_POW:
		if a, ok := types.IsIntLit(l); ok {
			if b, ok := types.IsIntLit(r); ok {
				// Two integers whose result is past the integers give a
				// float, as PHP computes it and PHPStan types it.
				s, ok := int64(0), false
				switch op {
				case token.Kind('-'):
					s, ok = subInt(a, b)
				case token.Kind('*'):
					s, ok = mulInt(a, b)
				case token.T_POW:
					s, ok = powInt(a, b)
				}
				if ok {
					return types.IntLit(s)
				}
				return types.TFloat
			}
		}
		return pastTheIntegers(op, l, r, x.arithResult(l, r, span, opText(op)))
	case token.Kind('/'):
		x.arithResult(l, r, span, "/")
		if a, ok := types.IsIntLit(l); ok {
			if b, ok := types.IsIntLit(r); ok && b != 0 && a%b == 0 {
				if a == math.MinInt64 && b == -1 {
					return types.TFloat // PHP_INT_MIN / -1 is past PHP_INT_MAX
				}
				return types.IntLit(a / b)
			}
		}
		return types.MakeUnion(types.TInt, types.TFloat)
	case token.Kind('%'):
		x.arithResult(l, r, span, "%")
		return types.TInt
	case token.Kind('&'), token.Kind('|'), token.Kind('^'):
		// Two strings combine byte by byte: of text, a string; where one
		// is bytes, bytes, since what comes out is no text (a mask).
		stringish := types.MakeUnion(types.TString, types.TBytes)
		if types.IsSubtype(l, stringish, nil) && types.IsSubtype(r, stringish, nil) {
			if hasBytes(l) || hasBytes(r) {
				return types.TBytes
			}
			return types.TString
		}
		// Flags combine into a literal: PREG_SPLIT_NO_EMPTY |
		// PREG_SPLIT_DELIM_CAPTURE is 3, which a conditional return type
		// on the flags can tell apart (PHPStan folds it too).
		if a, ok := types.IsIntLit(l); ok {
			if b, ok := types.IsIntLit(r); ok {
				switch op {
				case token.Kind('&'):
					return types.IntLit(a & b)
				case token.Kind('|'):
					return types.IntLit(a | b)
				default:
					return types.IntLit(a ^ b)
				}
			}
		}
		x.arithResult(l, r, span, string(rune(op)))
		return types.TInt
	case token.T_SL, token.T_SR:
		x.arithResult(l, r, span, "shift")
		// Literals shift into a literal, as the flags of `1 << 3` do in
		// PHPStan. A shift by 64 or more is 0, or -1 for a negative one
		// shifted right; one by less than 0 throws, and stays int here.
		if a, ok := types.IsIntLit(l); ok {
			if b, ok := types.IsIntLit(r); ok && b >= 0 {
				switch {
				case op == token.T_SL && b >= 64:
					return types.IntLit(0)
				case op == token.T_SL:
					return types.IntLit(a << b)
				case b >= 64 && a < 0:
					return types.IntLit(-1)
				case b >= 64:
					return types.IntLit(0)
				}
				return types.IntLit(a >> b)
			}
		}
		if besideGMP(l) || besideGMP(r) {
			// PHPStan joins the numbers of the two operands, unless one
			// is a float: int|float with a GMP read as a number.
			if !types.IsSubtype(gmpAsNumber(l), types.TFloat, nil) && !types.IsSubtype(gmpAsNumber(r), types.TFloat, nil) {
				return types.MakeUnion(types.TInt, types.TFloat)
			}
		}
		return types.TInt
	case token.T_IS_IDENTICAL, token.T_IS_NOT_IDENTICAL:
		return types.TBool // binary() reports the impossible comparisons
	case token.T_IS_EQUAL, token.T_IS_NOT_EQUAL:
		x.objectNumberComparison(op, l, r, span)
		return types.TBool
	case token.Kind('<'), token.Kind('>'), token.T_IS_SMALLER_OR_EQUAL, token.T_IS_GREATER_OR_EQUAL:
		x.objectNumberComparison(op, l, r, span)
		if li, ok := l.(types.Int); ok {
			if ri, ok := r.(types.Int); ok {
				if always, value := orderingOutcome(op, li, ri); always && x.certainOperands {
					// `@param positive-int $n` over `int $n`: `$n < 1` is a check
					// a PHP caller can still fail (checker.md §9).
					x.report(span, P2402, "comparison between %s and %s is always %v", x.typeName(l), x.typeName(r), value)
				}
			}
		}
		return types.TBool
	case token.T_SPACESHIP:
		x.objectNumberComparison(op, l, r, span)
		return types.Int{Lo: -1, Hi: 1}
	case token.T_INSTANCEOF:
		return types.TBool
	}
	return types.TMixed
}

// gmpArithmetic are the binary operators GMP overloads: arithmetic,
// bitwise and shifts, whose result is a GMP.
var gmpArithmetic = map[token.Kind]bool{
	token.Kind('+'): true, token.Kind('-'): true, token.Kind('*'): true, token.Kind('/'): true,
	token.Kind('%'): true, token.T_POW: true, token.Kind('&'): true, token.Kind('|'): true,
	token.Kind('^'): true, token.T_SL: true, token.T_SR: true,
}

// isGMP reports a type that is a GMP number, every member.
func isGMP(t types.Type) bool {
	members := types.Members(t)
	for _, m := range members {
		if c, ok := m.(types.Class); !ok || !strings.EqualFold(c.Name, "GMP") {
			return false
		}
	}
	return len(members) > 0
}

// besideGMP reports a union that holds a GMP beside other types, `int|GMP`
// or `?GMP`.
func besideGMP(t types.Type) bool {
	members := types.Members(t)
	if len(members) < 2 {
		return false
	}
	for _, m := range members {
		if isGMP(m) {
			return true
		}
	}
	return false
}

// gmpAsNumber is t with the GMP of a union read as the number PHPStan
// converts it to, int|float. Its GMP extension answers only for an operand
// that is a GMP for certain (gmpResult); the member of a union is typed as
// a number among numbers. So `$ig + 1` with `int|GMP $ig` is int|float,
// where PHP computes an int or a GMP: what PHPStan says of the compiled
// code is what the checker says of the source.
func gmpAsNumber(t types.Type) types.Type {
	if !besideGMP(t) {
		return t
	}
	return types.Map(t, func(m types.Type) types.Type {
		if isGMP(m) {
			return types.MakeUnion(types.TInt, types.TFloat)
		}
		return m
	})
}

// gmpResult types l op r where one operand is a GMP: PHP's GMP overloads
// the operator, and the result is a GMP, as PHPStan types it. The other
// operand is a GMP, an int or a numeric string; anything else, a float,
// another string, a bool or null, is P2301, as PHPStan reports it
// ("results in an error").
func (x *fnCtx) gmpResult(op token.Kind, l, r types.Type, span ast.Span) types.Type {
	ok := true
	for _, t := range []types.Type{l, r} {
		if isGMP(t) || types.IsMixed(t) || types.IsSubtype(t, types.MakeUnion(types.TInt, types.String{Numeric: true}), x.p()) {
			continue
		}
		x.report(span, P2301, "operator %s on %s", opText(op), x.typeName(t))
		ok = false
	}
	if !ok {
		return types.TError
	}
	return types.NewClass("GMP")
}

// numberComparable are the classes whose comparison handler takes a number:
// GMP and BcMath\Number compare by value with int and float operands.
var numberComparable = []string{"GMP", "BcMath\\Number"}

// objectNumberComparison reports a comparison of an object with a number.
// PHP compares objects with each other through the class's handler (two
// Time\Duration by length, two DateTimeInterface by instant, other objects
// property by property), but against an int or a float it converts the
// object to a number: a Notice and a meaningless result (PHPStan's
// smaller.invalid, equal.invalid, spaceship.invalid).
func (x *fnCtx) objectNumberComparison(op token.Kind, l, r types.Type, span ast.Span) {
	obj, num := l, r
	if !x.plainObject(obj) || !hasNumber(num) {
		obj, num = r, l
		if !x.plainObject(obj) || !hasNumber(num) {
			return
		}
	}
	x.report(span, P2301, "comparison %s between %s and %s: PHP converts the object to a number", opText(op), x.typeName(l), x.typeName(r))
}

// plainObject reports a type whose every member is an object that does not
// compare with numbers.
func (x *fnCtx) plainObject(t types.Type) bool {
	if types.IsMixed(t) || !types.IsObjectLike(t) {
		return false
	}
	for _, m := range types.Members(t) {
		if _, unknown := m.(types.Unknown); unknown {
			return false // reported already, or a class nothing declares
		}
	}
	for _, name := range types.ClassNames(t) {
		for _, nc := range numberComparable {
			if strings.EqualFold(name, nc) || x.p().IsSubclassOf(name, nc) {
				return false
			}
		}
	}
	return true
}

func hasNumber(t types.Type) bool {
	for _, m := range types.Members(t) {
		switch m.(type) {
		case types.Int, types.Float, types.FloatLit:
			return true
		}
	}
	return false
}

func opText(op token.Kind) string {
	switch op {
	case token.T_IS_EQUAL:
		return "=="
	case token.T_IS_NOT_EQUAL:
		return "!="
	case token.T_IS_SMALLER_OR_EQUAL:
		return "<="
	case token.T_IS_GREATER_OR_EQUAL:
		return ">="
	case token.T_SPACESHIP:
		return "<=>"
	case token.T_POW:
		return "**"
	}
	return string(rune(op))
}

// orderingOutcome decides `l op r` for integer ranges when every pair of
// values agrees; always is false when the ranges overlap.
func orderingOutcome(op token.Kind, l, r types.Int) (always, value bool) {
	switch op {
	case token.Kind('<'):
		if l.Hi < r.Lo {
			return true, true
		}
		if l.Lo >= r.Hi {
			return true, false
		}
	case token.Kind('>'):
		if l.Lo > r.Hi {
			return true, true
		}
		if l.Hi <= r.Lo {
			return true, false
		}
	case token.T_IS_SMALLER_OR_EQUAL:
		if l.Hi <= r.Lo {
			return true, true
		}
		if l.Lo > r.Hi {
			return true, false
		}
	case token.T_IS_GREATER_OR_EQUAL:
		if l.Lo >= r.Hi {
			return true, true
		}
		if l.Hi < r.Lo {
			return true, false
		}
	}
	return false, false
}

func isNonEmptyString(t types.Type) bool {
	switch s := t.(type) {
	case types.StringLit:
		return s.V != ""
	case types.String:
		return s.NonEmpty
	case types.ClassString:
		return true
	case types.Int, types.FloatLit:
		return true
	}
	return false
}

func isArrayType(t types.Type) bool {
	for _, m := range types.Members(t) {
		switch m.(type) {
		case types.Array, types.Shape:
			return true
		}
	}
	return false
}

func addInt(a, b int64) (int64, bool) {
	s := a + b
	if (s > a) != (b > 0) {
		return 0, false
	}
	return s, true
}

// pastTheIntegers is the type of l op r, for `+`, `-`, `*` and `**`, where
// t is what arithResult makes of it. An int is looked at again, since PHP's
// result is a float where the integers end, and PHPStan types it so:
//
//   - a power whose exponent is below zero for certain is a float whatever
//     the base: 2 ** -1 is 0.5, and 1 ** -1 is 1.0;
//   - operands that are a few integers each (a literal, a union of them, a
//     short range, true, false, null) are computed pair by pair: float
//     when every result is past the integers, int|float when some are.
//
// Anything else stays int, as a sum of two plain ints does: there the
// overflow is possible and nobody's finding.
func pastTheIntegers(op token.Kind, l, r, t types.Type) types.Type {
	if !types.Equal(t, types.TInt) {
		return t
	}
	if op == token.T_POW && belowZero(r) {
		return types.TFloat
	}
	as, ok := fewInts(l)
	if !ok {
		return t
	}
	bs, ok := fewInts(r)
	if !ok {
		return t
	}
	past := 0
	for _, a := range as {
		for _, b := range bs {
			ok := false
			switch op {
			case token.Kind('+'):
				_, ok = addInt(a, b)
			case token.Kind('-'):
				_, ok = subInt(a, b)
			case token.Kind('*'):
				_, ok = mulInt(a, b)
			case token.T_POW:
				_, ok = powInt(a, b)
			}
			if !ok {
				past++
			}
		}
	}
	switch past {
	case 0:
		return t
	case len(as) * len(bs):
		return types.TFloat
	}
	return types.MakeUnion(types.TInt, types.TFloat)
}

// belowZero reports an integer type whose every value is below zero: -1,
// -1|-2, int<min, -1>.
func belowZero(t types.Type) bool {
	members := types.Members(t)
	for _, m := range members {
		if i, ok := m.(types.Int); !ok || i.Hi >= 0 {
			return false
		}
	}
	return len(members) > 0
}

// maxFewInts bounds the values fewInts lists for one operand, and so the
// pairs pastTheIntegers computes at maxFewInts squared.
const maxFewInts = 8

// fewInts lists the integers an operand of arithmetic may be, when they are
// at most maxFewInts: its integer literals and short ranges, and the
// numbers PHP reads true, false and null as.
func fewInts(t types.Type) ([]int64, bool) {
	var out []int64
	for _, m := range types.Members(t) {
		switch m := m.(type) {
		case types.Int:
			if uint64(m.Hi)-uint64(m.Lo) >= maxFewInts {
				return nil, false
			}
			for v := m.Lo; ; v++ {
				out = append(out, v)
				if v == m.Hi {
					break
				}
			}
		case types.BoolLit:
			if m.V {
				out = append(out, 1)
			} else {
				out = append(out, 0)
			}
		case types.Bool:
			out = append(out, 0, 1)
		case types.Null:
			out = append(out, 0)
		default:
			return nil, false
		}
		if len(out) > maxFewInts {
			return nil, false
		}
	}
	return out, len(out) > 0
}

// subInt is a - b, and false when the difference is no int64.
func subInt(a, b int64) (int64, bool) {
	s := a - b
	if (s < a) != (b > 0) {
		return 0, false
	}
	return s, true
}

func mulInt(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	// The one overflow the division below does not show: MinInt64 * -1
	// wraps to MinInt64, and so does MinInt64 / -1.
	if a == math.MinInt64 && b == -1 {
		return 0, false
	}
	s := a * b
	if s/b != a {
		return 0, false
	}
	return s, true
}

// powInt is a ** b for the exponents that keep an integer one, b >= 0, and
// false when the power is no int64. A negative exponent gives a float in
// PHP even where the value is whole.
func powInt(a, b int64) (int64, bool) {
	if b < 0 {
		return 0, false
	}
	s, ok := int64(1), true
	for b > 0 {
		if b&1 == 1 {
			if s, ok = mulInt(s, a); !ok {
				return 0, false
			}
		}
		if b >>= 1; b > 0 {
			if a, ok = mulInt(a, a); !ok {
				return 0, false
			}
		}
	}
	return s, true
}

// arithResult types an arithmetic operation and reports non-numeric operands.
func (x *fnCtx) arithResult(l, r types.Type, span ast.Span, op string) types.Type {
	numeric := types.MakeUnion(types.TInt, types.TFloat, types.TBool, types.TNull, types.String{Numeric: true})
	for i, t := range []types.Type{l, r} {
		if types.IsMixed(t) {
			continue
		}
		if !types.IsSubtype(gmpAsNumber(t), numeric, x.p()) {
			if op == "**" && x.arith.left != nil && x.powerOnString(i, l, r) {
				// An extra check, proven by the operands (powerOnString).
				x.reportExtra(span, P2301, "operator %s on %s", op, x.typeName(t))
				continue
			}
			x.report(span, P2301, "operator %s on %s", op, x.typeName(t))
		}
	}
	l, r = gmpAsNumber(l), gmpAsNumber(r)
	isFloat := func(t types.Type) bool {
		for _, m := range types.Members(t) {
			switch m.(type) {
			case types.Float, types.FloatLit:
				return true
			}
		}
		return false
	}
	allInt := func(t types.Type) bool {
		return types.IsSubtype(t, types.MakeUnion(types.TInt, types.TBool, types.TNull), nil)
	}
	switch {
	case isFloat(l) || isFloat(r):
		if allInt(l) && allInt(r) {
			return types.TInt
		}
		if types.IsSubtype(l, types.TFloat, nil) || types.IsSubtype(r, types.TFloat, nil) {
			return types.TFloat
		}
		return types.MakeUnion(types.TInt, types.TFloat)
	case allInt(l) && allInt(r):
		return types.TInt
	}
	return types.MakeUnion(types.TInt, types.TFloat)
}

func (x *fnCtx) coalesce(e *ast.Binary, env *state, expected types.Type) types.Type {
	x.idleNullsafe(e.Left, env, "??", "on the left side of ??")
	left := x.exprLenient(e.Left, env)
	switch e.Left.(type) {
	case *ast.Variable:
		// Only a variable: a property may be uninitialised, an offset may be
		// missing, so `??` on those is isset() and PHPStan keeps quiet. A
		// refined variable (a by-reference out, a docblock) is not certain
		// either (checker.md §9).
		native := x.nativeOf(e.Left, env)
		certain := native == nil || !types.IsNullable(native) && !types.IsMixed(native)
		if certain && !types.IsNullable(left) && !types.IsMixed(left) && !x.maybeUndefined(e.Left, env) {
			x.report(e.Left.Span(), P2308, "left side of ?? is %s, which is never null", x.typeName(left))
		}
	}
	// The right side is evaluated only when the left is null: narrow.
	rightEnv := env.clone()
	right := x.expr(e.Right, rightEnv, expected)
	if rightEnv.dead {
		// `$a['k'] ?? throw …`: past it, the left was set, and not null.
		x.narrowPath(env, e.Left, types.StripNull(left), false)
	}
	*env = *join(env, rightEnv)
	return types.MakeUnion(types.StripNull(left), right)
}

// nullableChain reports a property or method chain one of whose receivers
// may be null: `$this->current->id ?? $x` is null when $this->current is,
// which `??` (isset semantics) swallows, as PHPStan does.
func (x *fnCtx) nullableChain(e ast.Expr, env *state) bool {
	for {
		var obj ast.Expr
		switch f := e.(type) {
		case *ast.PropertyFetch:
			obj = f.Object
		case *ast.MethodCall:
			obj = f.Object
		case *ast.Index:
			obj = f.Array
		default:
			return false
		}
		if t := x.exprLenient(obj, env); types.IsNullable(t) || types.IsMixed(t) {
			return true
		}
		e = obj
	}
}

func (x *fnCtx) maybeUndefined(e ast.Expr, env *state) bool {
	if v, ok := e.(*ast.Variable); ok {
		info, defined := env.get(v.Name)
		return !defined || info.maybe
	}
	return false
}

func (x *fnCtx) ternary(e *ast.Ternary, env *state, expected types.Type) types.Type {
	thenEnv, elseEnv := x.cond(e.Cond, env)
	cv := x.condValueType
	positive, negative := x.guardOf(e.Cond)
	var thenT types.Type
	if e.Then == nil {
		// c ?: b — the value of c when truthy.
		thenT = types.Truthy(x.condValueType)
	} else {
		pop := x.c.pushGuard(positive)
		thenT = x.expr(e.Then, thenEnv, expected)
		pop()
	}
	pop := x.c.pushGuard(negative)
	elseT := x.expr(e.Else, elseEnv, expected)
	pop()
	// A condition the types decide takes one branch, as PHPStan does;
	// the other is still checked.
	alwaysTrue, alwaysFalse := types.Equal(cv, types.TTrue), types.Equal(cv, types.TFalse)
	if thenEnv.dead && !elseEnv.dead && e.Then != nil || alwaysFalse && !elseEnv.dead {
		*env = *elseEnv
	} else if elseEnv.dead && !thenEnv.dead || alwaysTrue && !thenEnv.dead {
		*env = *thenEnv
	} else {
		*env = *join(thenEnv, elseEnv)
	}
	if thenEnv.dead || alwaysFalse {
		return elseT
	}
	if elseEnv.dead || alwaysTrue {
		return thenT
	}
	return types.MakeUnion(thenT, elseT)
}

// idleNullsafe reports a ?-> property fetch that `??`, isset() or empty()
// (op, where it stands) reads: they read a property of null without an
// error, so `$u?->email ?? 'none'` is `$u->email ?? 'none'`, as PHPStan
// reports it (nullsafe.neverNull). The fetch itself is what they read,
// not a call: a method of null throws there too. A receiver that is never
// null is P2307 already.
func (x *fnCtx) idleNullsafe(e ast.Expr, env *state, op, where string) {
	for {
		p, ok := e.(*ast.Paren)
		if !ok {
			break
		}
		e = p.Expr
	}
	f, ok := e.(*ast.PropertyFetch)
	if !ok || !f.NullSafe {
		return
	}
	obj := x.pathType(f.Object, env)
	if !types.IsNullable(obj) && !types.IsMixed(obj) {
		return
	}
	if _, onlyNull := obj.(types.Null); onlyNull {
		// A receiver that is only null makes the operand always null, which
		// PHPStan reports first (nullCoalesce.expr, isset.expr) and in
		// place of this; what reads a member of null says so.
		return
	}
	name := "?->"
	if id, ok := identName(f.Name); ok {
		name += id
	}
	x.report(f.S, P2307, "%s %s is unnecessary: %s reads a property of null without an error; use ->", name, where, op)
}

func (x *fnCtx) isset(e *ast.Isset, env *state) types.Type {
	for _, v := range e.Vars {
		x.idleNullsafe(v, env, "isset()", "in isset()")
		t := x.exprLenient(v, env)
		if !types.IsNullable(t) && !types.IsMixed(t) {
			switch v.(type) {
			case *ast.Variable:
				if !x.maybeUndefined(v, env) {
					x.report(v.Span(), P2314, "isset() on $%s, which is always defined and never null", v.(*ast.Variable).Name)
				}
			}
		}
	}
	return types.TBool
}

// condValueType is the type of the last condition evaluated by cond(),
// for the short ternary.
func (x *fnCtx) condValue() types.Type { return x.condValueType }

// ------------------------------------------------------------------ assignment

func (x *fnCtx) assign(e *ast.Assign, env *state) types.Type {
	if e.ByRef {
		// A reference to a missing offset creates it: no P2321.
		done := x.markWritten(e.Value)
		vt := x.expr(e.Value, env, nil)
		done()
		if v, ok := e.Value.(*ast.Variable); ok {
			x.byRef[v.Name] = true
		}
		target, isVar := e.Target.(*ast.Variable)
		if isVar {
			x.byRef[target.Name] = true
		}
		x.assignTo(e.Target, vt, env, e.S)
		// `$alias = &$r`: one value from here on, which a write to either
		// writes (env.set). A reference to an offset or a property is
		// not followed, as PHPStan does not follow it.
		if v, ok := unparen(e.Value).(*ast.Variable); ok && isVar && v.Name != target.Name && v.Name != "this" && target.Name != "this" {
			env.link(target.Name, v.Name)
		}
		return vt
	}
	if e.Op == token.Kind('=') {
		expected := x.targetType(e.Target, env)
		vt := x.expr(e.Value, env, expected)
		if lit, ok := e.Target.(*ast.ArrayLit); ok {
			x.destructure(lit, vt, env, e.S, e.Value)
			return vt
		}
		x.assignFrom(e.Value, e.Target, vt, env, e.S)
		if v, ok := e.Target.(*ast.Variable); ok {
			env.setNative(v.Name, x.guaranteedOf(e.Value, env))
			x.rememberCondition(v.Name, e.Value, env)
			x.rememberKeyOf(e, v.Name, e.Value, env)
		}
		return vt
	}
	if e.Op == token.T_COALESCE_EQUAL {
		cur := x.exprLenient(e.Target, env)
		rightEnv := env.clone()
		vt := x.expr(e.Value, rightEnv, x.targetType(e.Target, env))
		*env = *join(env, rightEnv)
		res := types.MakeUnion(types.StripNull(cur), vt)
		x.assignTo(e.Target, res, env, e.S)
		return res
	}
	cur := x.expr(e.Target, env, nil)
	vt := x.expr(e.Value, env, nil)
	op := compoundOp(e.Op)
	var res types.Type
	if op == token.T_COALESCE {
		res = types.MakeUnion(types.StripNull(cur), vt)
	} else {
		x.arith = arithOperands{e.Target, e.Value}
		res = x.binaryTypes(op, cur, vt, e)
		x.arith = arithOperands{}
	}
	if idx, ok := e.Target.(*ast.Index); ok && idx.Index != nil && !types.IsNullable(cur) {
		// `$parts[0] .= $chunk` reads the offset before it writes it: the
		// write replaces an existing entry, so a list stays a list (PHPStan's
		// setExistingOffsetValueType, as for the key of a foreach).
		if key, ok := x.pathKey(idx); ok {
			env.setPath(key, cur)
		}
	}
	x.assignTo(e.Target, res, env, e.S)
	return res
}

func compoundOp(k token.Kind) token.Kind {
	switch k {
	case token.T_PLUS_EQUAL:
		return token.Kind('+')
	case token.T_MINUS_EQUAL:
		return token.Kind('-')
	case token.T_MUL_EQUAL:
		return token.Kind('*')
	case token.T_DIV_EQUAL:
		return token.Kind('/')
	case token.T_CONCAT_EQUAL:
		return token.Kind('.')
	case token.T_MOD_EQUAL:
		return token.Kind('%')
	case token.T_AND_EQUAL:
		return token.Kind('&')
	case token.T_OR_EQUAL:
		return token.Kind('|')
	case token.T_XOR_EQUAL:
		return token.Kind('^')
	case token.T_SL_EQUAL:
		return token.T_SL
	case token.T_SR_EQUAL:
		return token.T_SR
	case token.T_POW_EQUAL:
		return token.T_POW
	case token.T_COALESCE_EQUAL:
		return token.T_COALESCE
	}
	return k
}

// targetType returns the declared type of an assignment target (property,
// typed variable), or nil.
func (x *fnCtx) targetType(target ast.Expr, env *state) types.Type {
	if assume := x.c.opts.Assume; assume != nil {
		if a := assume(x.file, target); a != nil {
			return a
		}
	}
	switch t := target.(type) {
	case *ast.PropertyFetch:
		if pr, recv := x.lookupPropertyForWrite(t, env); pr != nil {
			return x.bindReceiver(pr.Type, recv, pr.Class)
		}
	case *ast.StaticPropertyFetch:
		savedDiags := x.c.mark()
		_, pr := x.staticPropertyFetch(t, env)
		x.c.rollback(savedDiags)
		if pr != nil {
			return pr.Type
		}
	case *ast.Index:
		base := x.exprLenient(t.Array, env)
		for _, m := range types.Members(base) {
			if a, ok := m.(types.Array); ok {
				return a.Value
			}
		}
	}
	return nil
}

// lookupPropertyForWrite finds the property an assignment targets without
// reporting (the read path reports).
func (x *fnCtx) lookupPropertyForWrite(t *ast.PropertyFetch, env *state) (*program.Property, types.Type) {
	name, ok := identName(t.Name)
	if !ok {
		return nil, nil
	}
	savedDiags := x.c.mark()
	obj := x.expr(t.Object, env.clone(), nil)
	x.c.rollback(savedDiags)
	for _, cl := range x.classesOf(types.StripNull(obj), t.Object.Span()) {
		if prs, _, _ := x.classProperties(cl, name); len(prs) > 0 {
			return prs[0], obj
		}
	}
	return nil, obj
}

// assignTo records an assignment of a value of type vt to a target.
func (x *fnCtx) assignTo(target ast.Expr, vt types.Type, env *state, span ast.Span) {
	x.assignFrom(nil, target, vt, env, span)
}

// assignFrom is assignTo for a plain assignment, whose value is the
// expression given: where a property refuses the value, that expression is
// what may prove the finding an extra check (extra.go). Every other
// assignment has no expression of its own and passes none.
func (x *fnCtx) assignFrom(value ast.Expr, target ast.Expr, vt types.Type, env *state, span ast.Span) {
	switch t := target.(type) {
	case *ast.Paren:
		x.assignFrom(value, t.Expr, vt, env, span)
	case *ast.Variable:
		if t.Name == "this" {
			x.report(t.S, P2311, "cannot re-assign $this")
			return
		}
		if _, isVoid := vt.(types.Void); isVoid {
			x.report(span, P2205, "result of a void function is used")
			vt = types.TNull
		}
		// A copy of a value a write made the array written is that array:
		// `$s = $a` holds what $a was written to be.
		copied := rewrittenOf(value, env)
		env.set(t.Name, vt)
		if copied != nil && types.Overlaps(vt, copied.written, x.p()) {
			env.rewrite(t.Name, copied)
		}
	case *ast.PropertyFetch:
		x.assignProperty(t, vt, env, span, value)
	case *ast.StaticPropertyFetch:
		pt, pr := x.staticPropertyFetch(t, env)
		if pr != nil && !types.IsAcceptable(vt, pr.Type, x.p()) {
			if why, rel, ok := x.rewrittenWhy(value, vt, pr.Type, env, false); ok {
				x.c.reportRelated(span, P2201, rel, "property %s::$%s (%s) does not accept %s: %s", pr.Class.Name, pr.Name, x.typeName(pt), x.typeName(vt), why)
			} else {
				x.c.reportUnfit(x.given(value), vt, pr.Type, span, P2201, nil, "property %s::$%s (%s) does not accept %s", pr.Class.Name, pr.Name, x.typeName(pt), x.typeName(vt))
			}
		}
		if key, ok := x.pathKey(t); ok {
			env.setPath(key, vt)
		}
	case *ast.Index:
		x.assignIndex(t, vt, env, span)
	case *ast.ArrayLit:
		x.destructure(t, vt, env, span, nil)
	case *ast.VariableVariable:
		x.expr(t.Expr, env, nil)
		x.removed(t.S, "variable variables ($$x)", "a map<string, mixed>")
		env.forgetConds() // it may write any variable
	default:
		x.expr(target, env, nil)
	}
}

func (x *fnCtx) assignProperty(t *ast.PropertyFetch, vt types.Type, env *state, span ast.Span, value ast.Expr) {
	name, ok := identName(t.Name)
	if !ok {
		x.noteRecv(t, x.expr(t.Object, env, nil))
		x.noteName(t, x.expr(t.Name, env, nil))
		x.implicitThrow()
		return
	}
	obj := x.expr(t.Object, env, nil)
	x.noteRecv(t, obj)
	x.propertyThrows(types.StripNull(obj), name)
	base := types.StripNull(obj)
	if types.IsNullable(obj) && !types.IsMixed(obj) {
		x.report(t.Object.Span(), P2105, "cannot assign property $%s on %s (possibly null)", name, x.typeName(obj))
	}
	if types.IsMixed(base) {
		x.memberOnMixed(base, t.Object.Span(), "cannot access property $%s on mixed", name) // the read's words: one finding for `$m->n++`
		return
	}
	if !types.IsObjectLike(base) {
		x.report(t.Object.Span(), P2105, "cannot assign property $%s on %s", name, x.typeName(base))
		return
	}
	for _, m := range types.Members(base) {
		if _, isObj := m.(types.Object); isObj && !provenMember(env, t.Object, false, name) {
			x.report(t.Name.Span(), P2005, "unknown property object::$%s", name)
		}
	}
	for _, cl := range x.classesOf(base, t.Object.Span()) {
		prs, lacking, _ := x.classProperties(cl, name)
		if lacking != nil && !x.isConstructorOf(lacking) {
			x.report(t.Name.Span(), P2005, "unknown property %s::$%s", lacking.Name, name)
		}
		for _, pr := range prs {
			x.assignDeclared(t, pr, name, base, vt, span, value, env)
		}
	}
	if v, ok := t.Object.(*ast.Variable); ok && v.Name == "this" && x.trackInits {
		env.initialise(name)
	}
	if key, ok := x.pathKey(t); ok {
		env.invalidatePrefix(key)
		env.setPath(key, vt)
	}
}

// assignDeclared checks an assignment of vt to the declared property pr.
func (x *fnCtx) assignDeclared(t *ast.PropertyFetch, pr *program.Property, name string, base, vt types.Type, span ast.Span, value ast.Expr, env *state) {
	if pr.Static {
		x.report(t.Name.Span(), P2210, "static property %s::$%s accessed through an instance", pr.Class.Name, name)
	}
	switch {
	case pr.Readonly:
		// Readonly properties are initialised once, in the declaring
		// class's constructor (or __clone); PHP rejects everything else.
		if !x.inClassScope(pr.Class) || !x.inInitialiser() {
			x.report(t.Name.Span(), P2206, "readonly property %s::$%s can only be assigned in the constructor of %s", pr.Class.Name, name, pr.Class.Name)
		} else if pr.HasDefault && strings.EqualFold(x.method.Name, "__construct") && x.c.target().AtLeast(phpversion.PHP86) {
			// PHP 8.6: the default initialised it before the constructor
			// ran; only __clone may reinitialise it.
			x.report(t.Name.Span(), P2206, "readonly property %s::$%s is initialised by its default: the constructor cannot assign it", pr.Class.Name, name)
		}
	case !x.canAccess(pr.SetVis, pr.Class):
		if pr.SetVis != pr.Vis {
			x.report(t.Name.Span(), P2207, "property %s::$%s is %s(set)", pr.Class.Name, name, pr.SetVis)
		} else {
			x.report(t.Name.Span(), P2108, "property %s::$%s is %s", pr.Class.Name, name, pr.Vis)
		}
	}
	if pr.Virtual && !hasSetHook(pr) {
		x.report(t.Name.Span(), P2206, "virtual property %s::$%s has no set hook", pr.Class.Name, name)
	}
	declared := x.bindReceiver(pr.Type, base, pr.Class)
	if !types.IsAcceptable(vt, declared, x.p()) {
		if why, rel, ok := x.rewrittenWhy(value, vt, declared, env, false); ok {
			x.c.reportRelated(span, P2201, rel, "property %s::$%s (%s) does not accept %s: %s", pr.Class.Name, name, x.typeName(declared), x.typeName(vt), why)
			return
		}
		x.c.reportUnfit(x.given(value), vt, declared, span, P2201, nil, "property %s::$%s (%s) does not accept %s", pr.Class.Name, name, x.typeName(declared), x.typeName(vt))
	}
}

func hasSetHook(pr *program.Property) bool {
	for _, h := range pr.Hooks {
		if strings.EqualFold(h.Name.Name, "set") {
			return true
		}
	}
	return false
}

func (x *fnCtx) inClassScope(cl *program.Class) bool {
	if x.class == nil {
		return false
	}
	if strings.EqualFold(x.class.Name, cl.Name) {
		return true
	}
	if x.class.Kind == ast.KindTrait {
		// A trait's body runs in the scope of each class using it: its
		// constructor initialises the user's readonly properties.
		for _, u := range x.p().TraitUsers(x.class.Name) {
			if strings.EqualFold(u.Name, cl.Name) {
				return true
			}
		}
	}
	return false
}

func (x *fnCtx) isConstructorOf(cl *program.Class) bool { return false }

// assignIndex handles $a[k] = v and $a[] = v, updating the array's type.
func (x *fnCtx) assignIndex(t *ast.Index, vt types.Type, env *state, span ast.Span) {
	base := x.exprLenient(t.Array, env)
	if mayBeObject(base) {
		x.implicitThrow() // offsetSet() runs
	}
	var keyT, keyRaw types.Type
	if t.Index != nil {
		raw, key := x.keyExpr(t.Index, env)
		if mayBeArray(base) || containsNull(base) {
			x.nullOffset(raw, t.Index.Span()) // null becomes an array here
		}
		if types.IsMixed(raw) {
			// PHPStan at level max: a mixed key may be anything PHP refuses.
			x.report(t.Index.Span(), P2316, "possibly invalid array key type mixed")
		}
		keyT, keyRaw = key, raw
	}
	// Undefined variable used as an array: it becomes one.
	if v, ok := t.Array.(*ast.Variable); ok {
		if _, defined := env.get(v.Name); !defined {
			base = types.Shape{}
		}
	}
	// `$r[$a][$b] = $v` where $r[$a] may not exist yet: PHP creates the
	// inner array, so the assignment starts from an empty one, not from
	// the mixed an unknown offset reads as.
	if _, nested := t.Array.(*ast.Index); nested && (types.IsMixed(base) || types.IsNullable(base) && types.IsMixed(types.StripNull(base))) {
		if _, isMixed := base.(types.Mixed); isMixed {
			// An entry that exists and is mixed (`array<string, mixed>`'s)
			// may be a value with no offsets: PHPStan reports the write.
			x.reportNoOffsets(t, base, keyRaw, env)
		}
		base = types.Shape{}
	}
	// An offset the flow has seen — the key of the foreach over this array,
	// or an earlier write to it — is replaced, not added: the keys and the
	// list-ness stay (PHPStan's setExistingOffsetValueType).
	existing := false
	if keyT != nil {
		if key, ok := x.pathKey(t); ok {
			if cur, found := env.getPath(key); found && !types.IsNullable(cur) {
				existing = true
			}
		}
	}
	base = boundedShapes(base)
	// A type parameter with a bound is written as its bound: `T of
	// array<string, int>` as that array, `T of list<int>` as that list,
	// `T of Row` as the shape. What it holds afterwards is the array
	// written, no longer T, whose argument may be narrower than the
	// bound (design/generics.md §3.2): rewritten is that parameter. A
	// `T of ArrayAccess<K, V>` is an object, whose offsetSet() takes the
	// value and which stays the one T is. A write a member of the bound
	// refuses, the int of `T of int|array<string, int>`, is that
	// finding alone: the value stays the T it was, as PHPStan keeps it,
	// and is no finding again where T is expected.
	var out []boundMember
	var shapes []types.Shape
	var shapesFrom []*types.Param
	var refused []types.Param
	refuse := func(p *types.Param) {
		if p != nil && !slices.ContainsFunc(refused, func(r types.Param) bool { return types.Equal(r, *p) }) {
			refused = append(refused, *p)
		}
	}
	for _, w := range boundMembers(base) {
		named := w.member // the type a finding names: the parameter, where the member is its bound's
		if w.from != nil {
			named = *w.from
		}
		switch m := w.member.(type) {
		case types.Null:
			shapes, shapesFrom = append(shapes, types.Shape{}), append(shapesFrom, w.from)
		case types.Shape:
			shapes, shapesFrom = append(shapes, m), append(shapesFrom, w.from)
		case types.Array:
			k := m.Key
			list := m.List && (keyT == nil)
			switch {
			case existing:
				list = m.List
			case keyT != nil && types.Equal(keyT, types.IntLit(0)):
				// Offset 0 of a list is its first value, or the one it
				// gets: php keeps a list a list, and so does PHPStan.
				list = m.List
				k = types.MakeUnion(k, keyT)
			case keyT != nil:
				k = types.MakeUnion(k, keyT)
			case !m.List:
				k = types.MakeUnion(k, types.TInt)
			}
			out = append(out, boundMember{types.Array{Key: widenKeys(k), Value: types.MakeUnion(m.Value, vt), List: list, NonEmpty: true}, w.from})
		case types.Mixed:
			x.reportNoOffsets(t, m, keyRaw, env)
			out = append(out, boundMember{m, nil})
		case types.Unknown:
			out = append(out, boundMember{m, nil})
		case types.Class, types.Static, types.This, types.Intersection:
			handled := false
			for _, cl := range x.classesOf(m, t.Array.Span()) {
				if cl.IsSubclassOf("ArrayAccess") {
					handled = true
					if s := cl.FindMethod("offsetSet"); s != nil && len(s.Params) >= 2 {
						declared := x.bindReceiver(s.Params[1].Type, m, s.Class)
						if !types.IsAcceptable(vt, declared, x.p()) {
							// No extra check is told here: PHPStan holds an offset's
							// assignment to a rule of its own (offsetAssign.valueType),
							// stricter than the acceptance of an argument.
							x.report(span, P2103, "%s::offsetSet() expects %s, %s given", cl.Name, x.typeName(declared), x.typeName(vt))
						}
					}
				}
			}
			if !handled {
				x.report(t.Array.Span(), P2302, "cannot use %s as an array", x.typeName(named))
				refuse(w.from)
			}
			out = append(out, boundMember{named, nil})
		case types.Param:
			// A parameter without a bound, or bounded by mixed or object,
			// may be a value with no offsets.
			x.report(t.Array.Span(), P2302, "cannot use %s as an array", x.typeName(m))
			out = append(out, boundMember{m, nil})
		case types.String:
			// A string written at an offset is as long or longer: a
			// non-empty-string stays one, as PHPStan types it.
			out = append(out, boundMember{types.String{NonEmpty: m.NonEmpty}, w.from})
		case types.StringLit:
			out = append(out, boundMember{types.String{NonEmpty: m.V != ""}, w.from})
		case types.Iterable:
			// An iterable may be an object with no offsets; PHPStan
			// reports the write, and types what is left of the value as
			// what was written, which it holds to T where it is returned.
			x.report(t.Array.Span(), P2302, "cannot use %s as an array", x.typeName(named))
			out = append(out, boundMember{m, nil})
		default:
			x.report(t.Array.Span(), P2302, "cannot use %s as an array", x.typeName(named))
			if w.from != nil {
				refuse(w.from) // the parameter stands for the member below
			} else {
				out = append(out, boundMember{m, nil})
			}
		}
	}
	// What a refused parameter's other members would hold is left out:
	// the value is that parameter's still.
	isRefused := func(p *types.Param) bool {
		return p != nil && slices.ContainsFunc(refused, func(r types.Param) bool { return types.Equal(r, *p) })
	}
	var rewritten *types.Param
	var members []types.Type
	for _, o := range out {
		if !isRefused(o.from) {
			members = append(members, o.member)
			rewritten = firstParam(rewritten, o.from)
		}
	}
	var kept []types.Shape
	for i, s := range shapes {
		if !isRefused(shapesFrom[i]) {
			kept = append(kept, s)
			rewritten = firstParam(rewritten, shapesFrom[i])
		}
	}
	if len(kept) > 0 {
		members = append(members, writeShapes(kept, keyT, vt, x.loopDepth == 0)...)
	}
	for _, p := range refused {
		members = append(members, p)
	}
	newT := types.MakeUnion(members...)
	if len(members) == 0 {
		newT = arrayWithEntry(types.Shape{}, keyT, vt)
	}
	before := rewrittenOf(t.Array, env)
	x.assignTo(t.Array, newT, env, span)
	x.keepRewritten(t.Array, before, rewritten, t.S, newT, env)
	if key, ok := x.pathKey(t); ok {
		env.setPath(key, vt)
	}
}

// boundMember is a member of a type written to, and the type parameter
// whose bound it is a member of; from is nil for a member of the type
// itself.
type boundMember struct {
	member types.Type
	from   *types.Param
}

// boundMembers is the members of t, each type parameter with a bound
// (openParam's aside) read as the members of its bound, a bound that is
// itself a parameter as that one's: `T of int|array<string, int>` is int
// and the array, both from T. A parameter a test narrowed, `T&array`
// after is_array() of `T of array<string, int>|string`, is the members
// of its bound that meet the rest of the intersection.
func boundMembers(t types.Type) []boundMember {
	var out []boundMember
	var add func(m types.Type, from *types.Param, depth int)
	add = func(m types.Type, from *types.Param, depth int) {
		if depth < 8 {
			if p, ok := m.(types.Param); ok && !openParam(p) {
				if from == nil {
					from = &p
				}
				for _, b := range types.Members(p.Bound) {
					add(b, from, depth+1)
				}
				return
			}
			if in, ok := m.(types.Intersection); ok {
				if p, rest, ok := narrowedParam(in); ok {
					if from == nil {
						from = &p
					}
					for _, b := range types.Members(p.Bound) {
						if meet := types.MakeIntersection(append([]types.Type{b}, rest...)...); !isNeverType(meet) {
							for _, n := range types.Members(meet) {
								add(n, from, depth+1)
							}
						}
					}
					return
				}
			}
		}
		out = append(out, boundMember{m, from})
	}
	for _, m := range types.Members(t) {
		add(m, nil, 0)
	}
	return out
}

// narrowedParam is the type parameter of an intersection, one with a
// bound, and the intersection's other members: `T&array` is T and array.
func narrowedParam(in types.Intersection) (types.Param, []types.Type, bool) {
	for i, m := range in.Types {
		if p, ok := m.(types.Param); ok && !openParam(p) {
			rest := append(append([]types.Type{}, in.Types[:i]...), in.Types[i+1:]...)
			return p, rest, true
		}
	}
	return types.Param{}, nil, false
}

// firstParam is the parameter already found, else p.
func firstParam(found, p *types.Param) *types.Param {
	if found != nil {
		return found
	}
	return p
}

// rewrittenOf is the write that made the variable target names the array
// written in place of a type parameter's value, nil for any other target.
func rewrittenOf(target ast.Expr, env *state) *paramWrite {
	if v, ok := unparen(target).(*ast.Variable); ok {
		if info, defined := env.get(v.Name); defined {
			return info.rewritten
		}
	}
	return nil
}

// keepRewritten records on the variable target names, once a write into
// it has given it the type now, the write that made it the array written
// in place of a value of the type parameter p, at span: the one that
// stands between every earlier way in and what follows, where a value
// of p reached it. A write into the value that p had already left keeps
// the write that did, before, with what the variable holds now. A
// property or an offset holds no such record: the property is held to
// its declared type as any assignment is.
func (x *fnCtx) keepRewritten(target ast.Expr, before *paramWrite, p *types.Param, span ast.Span, now types.Type, env *state) {
	v, ok := unparen(target).(*ast.Variable)
	if !ok || v.Name == "this" {
		return
	}
	var w *paramWrite
	switch {
	case p != nil:
		w = &paramWrite{param: *p, at: span, written: writtenPart(now, *p)}
	case before != nil:
		w = &paramWrite{param: before.param, at: before.at, written: writtenPart(now, before.param)}
	default:
		return
	}
	env.rewrite(v.Name, w)
}

// writtenPart is t without the values of the type parameter p it holds,
// p itself and a narrowed p (`T&null`): what writes made of it.
func writtenPart(t types.Type, p types.Param) types.Type {
	var out []types.Type
	for _, m := range types.Members(t) {
		if !holdsParam(m, p) {
			out = append(out, m)
		}
	}
	return types.MakeUnion(out...)
}

// holdsParam reports m, a member of a type, as p or an intersection with
// p among its members.
func holdsParam(m types.Type, p types.Param) bool {
	if types.Equal(m, p) {
		return true
	}
	if in, ok := m.(types.Intersection); ok {
		return slices.ContainsFunc(in.Types, func(n types.Type) bool { return types.Equal(n, p) })
	}
	return false
}

// writeShapes writes key => vt into the shapes an array may be. A key that
// is a union of a few literals, `$o[$c ? 'b' : 'c'] = 2` on `array{a: 1}`,
// writes each literal as an optional entry, `array{a: 1, b?: 2, c?: 2}`,
// and adds vt to an entry the shape has, as PHPStan writes it, shape by
// shape. Not in a loop (byLiteral false): there the key may be each
// literal in turn, as in a foreach over `['b', 'c']`, which PHPStan runs
// once for each entry and the checker does not, and the write stays a
// general one. Any other key goes to arrayWithEntry, shape by shape.
func writeShapes(shapes []types.Shape, key, vt types.Type, byLiteral bool) []types.Type {
	var lits []types.Type
	if key != nil && byLiteral {
		if l := keyLiterals(key); len(l) > 1 {
			lits = l
		}
	}
	out := make([]types.Type, 0, len(shapes))
	for _, s := range shapes {
		if lits == nil {
			out = append(out, arrayWithEntry(s, key, vt))
			continue
		}
		entries := append([]types.ShapeEntry{}, s.Entries...)
		at := map[types.ShapeEntry]int{}
		for i, e := range entries {
			at[types.ShapeEntry{Key: e.Key, IntKey: e.IntKey, IsInt: e.IsInt}] = i
		}
		for _, k := range lits {
			entry := arrayWithEntryKey(k, vt)
			if i, ok := at[types.ShapeEntry{Key: entry.Key, IntKey: entry.IntKey, IsInt: entry.IsInt}]; ok {
				entries[i].Type = types.MakeUnion(entries[i].Type, vt)
				continue
			}
			entry.Optional = true
			entries = append(entries, entry)
			s.List = false
		}
		s.Entries = entries
		out = append(out, s)
	}
	return out
}

// maxShapeValues is how many entries the shapes of a union may hold in all
// before boundedShapes gives up on them: sixteen times PHPStan's
// ConstantArrayTypeBuilder::ARRAY_COUNT_LIMIT, so that what a function
// writes in a few branches stays exact, as it was before the bound.
const maxShapeValues = 16 * 256

// boundedShapes is t, an array written to, with its shapes bounded: joins
// of if/else multiply the shapes, two for each, and the checker paid for
// each write in each of them. Below maxShapeValues entries in all t is
// what it was, every shape as it is, so that a write, an append included,
// is exact in each. Past it, in steps, each taken while the last left the
// shapes past the limit:
//   - shapes one key tells apart merge, the key optional, which loses
//     nothing (reduceShapes): ten independent `if ($c) { $o['k'] = …; }`
//     become the one shape PHPStan's joins make of them as they go;
//   - shapes with the same keys and the same tags merge, their values
//     united key by key, as PHPStan's optimizeConstantArrays() merges
//     shapes with the same keys at 256 entries;
//   - the shapes of each tag collapse into one, its keys optional where a
//     shape lacks them;
//   - all of them collapse into one;
//   - and only where that one shape is past the limit too, a general
//     array of their keys and values, whose offsets PHPStan reads as
//     benevolent unions and the checker cannot.
//
// A tag is a key every shape has for certain, a constant in each, as
// `kind` of `{kind: 'a', v: int}` and `{kind: 'b', v: string}`: shapes
// of different tags are kept apart as PHPStan's joins keep them
// (TypeCombinator::reduceArrays), so that a test of the tag still tells
// them apart, up to the last two steps. Every step after the first loses
// what values of different keys said together otherwise: ten if/else,
// each writing one of two keys, reach the third, where a key of one
// branch is optional beside that of the other.
func boundedShapes(t types.Type) types.Type {
	members := types.Members(t)
	var shapes []types.Shape
	var out []types.Type
	for _, m := range members {
		if s, ok := m.(types.Shape); ok {
			shapes = append(shapes, s)
		} else {
			out = append(out, m)
		}
	}
	within := func(groups ...[]types.Shape) (types.Type, bool) {
		count := 0
		for _, g := range groups {
			for _, s := range g {
				count += len(s.Entries)
			}
		}
		if count > maxShapeValues {
			return nil, false
		}
		all := append([]types.Type{}, out...)
		for _, g := range groups {
			for _, s := range g {
				all = append(all, s)
			}
		}
		return types.MakeUnion(all...), true
	}
	if _, ok := within(shapes); ok {
		return t
	}
	shapes = reduceShapes(shapes)
	if r, ok := within(shapes); ok {
		return r
	}
	tags := tagGroups(shapes)
	for i, g := range tags {
		tags[i] = mergeBySignature(g)
	}
	if r, ok := within(tags...); ok {
		return r
	}
	var perTag []types.Shape
	for _, g := range tags {
		perTag = append(perTag, collapseShapes(g))
	}
	if r, ok := within(perTag); ok {
		return r
	}
	if r, ok := within([]types.Shape{collapseShapes(perTag)}); ok {
		return r
	}
	general := types.Array{List: true, NonEmpty: true}
	var keys, values []types.Type
	for _, s := range perTag {
		required := false
		for _, e := range s.Entries {
			if e.IsInt {
				keys = append(keys, types.TInt)
			} else {
				keys = append(keys, types.TString)
			}
			values = append(values, e.Type)
			required = required || !e.Optional
		}
		if s.Unsealed {
			keys, values = append(keys, types.TArrayKey), append(values, types.TMixed)
		}
		general.List = general.List && listLike(s)
		general.NonEmpty = general.NonEmpty && required
	}
	general.Key, general.Value = types.MakeUnion(keys...), types.MakeUnion(values...)
	if general.List {
		general.Key = types.TInt
	}
	return types.MakeUnion(append(out, general)...)
}

// tagGroups splits shapes by their tags, the keys every one of them has
// for certain as one constant each (`kind: 'a'`), in the order the shapes
// come: the shapes of a group share the value of every tag.
func tagGroups(shapes []types.Shape) [][]types.Shape {
	type key = types.ShapeEntry
	tagged := map[key]bool{}
	for i, s := range shapes {
		here := map[key]bool{}
		for _, e := range s.Entries {
			if !e.Optional && constantScalar(e.Type) {
				k := key{Key: e.Key, IntKey: e.IntKey, IsInt: e.IsInt}
				if i == 0 || tagged[k] {
					here[k] = true
				}
			}
		}
		tagged = here
	}
	var order []string
	groups := map[string][]types.Shape{}
	for _, s := range shapes {
		var b strings.Builder
		for _, e := range s.Entries {
			if tagged[key{Key: e.Key, IntKey: e.IntKey, IsInt: e.IsInt}] {
				b.WriteString(types.PrintQualified(e.Type) + "|")
			}
		}
		g := b.String()
		if _, seen := groups[g]; !seen {
			order = append(order, g)
		}
		groups[g] = append(groups[g], s)
	}
	out := make([][]types.Shape, 0, len(order))
	for _, g := range order {
		out = append(out, groups[g])
	}
	return out
}

// constantScalar reports a type that is one constant: a string, an int,
// a bool, a float or null.
func constantScalar(t types.Type) bool {
	switch t := t.(type) {
	case types.StringLit, types.BoolLit, types.FloatLit, types.Null:
		return true
	case types.Int:
		return t.Lo == t.Hi
	}
	return false
}

// mergeBySignature merges the shapes with the same keys into one each,
// their values united key by key (boundedShapes).
func mergeBySignature(shapes []types.Shape) []types.Shape {
	var signatures []string
	groups := map[string][]types.Shape{}
	for _, s := range shapes {
		sig := shapeSignature(s)
		if _, seen := groups[sig]; !seen {
			signatures = append(signatures, sig)
		}
		groups[sig] = append(groups[sig], s)
	}
	merged := make([]types.Shape, 0, len(signatures))
	for _, sig := range signatures {
		merged = append(merged, collapseShapes(groups[sig]))
	}
	return merged
}

// reduceShapes merges two shapes one key tells apart, the one without it
// and the one with it, all else alike, into one where the key is optional,
// as PHPStan's joins merge them as they go (TypeCombinator::reduceArrays):
// their union is that shape exactly, so nothing is lost, and the shapes of
// a tag, `{kind: 'a', …}` and `{kind: 'b', …}`, stay apart. Ten
// independent `if ($c) { $o['k'] = …; }` make 1024 shapes, and one again.
// Each pass merges pairs of shapes untouched in it, until none is left.
func reduceShapes(shapes []types.Shape) []types.Shape {
	for {
		index := make(map[string]int, len(shapes))
		for i, s := range shapes {
			index[shapeSpelling(s, -1)] = i
		}
		touched := make([]bool, len(shapes))
		gone := make([]bool, len(shapes))
		merged := false
		for i, t := range shapes {
			if touched[i] {
				continue
			}
			for j := range t.Entries {
				k, ok := index[shapeSpelling(t, j)]
				if !ok || k == i || touched[k] {
					continue
				}
				entries := append([]types.ShapeEntry{}, t.Entries...)
				entries[j].Optional = true
				t.Entries = entries
				shapes[i] = t
				touched[i], touched[k], gone[k], merged = true, true, true, true
				break
			}
		}
		if !merged {
			return shapes
		}
		kept := shapes[:0:0]
		for i, s := range shapes {
			if !gone[i] {
				kept = append(kept, s)
			}
		}
		shapes = kept
	}
}

// shapeSpelling spells a shape whole, without its entry skip (-1 for
// none), for reduceShapes to find the shape that lacks that entry: its kind
// and every entry's key, whether it is optional, and its type.
func shapeSpelling(s types.Shape, skip int) string {
	var b strings.Builder
	switch {
	case s.Unsealed:
		b.WriteByte('U')
	case s.List:
		b.WriteByte('L')
	default:
		b.WriteByte('A')
	}
	for i, e := range s.Entries {
		if i == skip {
			continue
		}
		if e.IsInt {
			b.WriteString("|i" + strconv.FormatInt(e.IntKey, 10))
		} else {
			b.WriteString("|s" + strconv.Quote(e.Key))
		}
		if e.Optional {
			b.WriteByte('?')
		}
		b.WriteString(":" + types.PrintQualified(e.Type))
	}
	return b.String()
}

// shapeSignature spells a shape's keys, in order, with whether each is
// optional, and whether it is a list or unsealed: shapes with the same one
// merge into one with the same keys (boundedShapes).
func shapeSignature(s types.Shape) string {
	var b strings.Builder
	switch {
	case s.Unsealed:
		b.WriteByte('U')
	case s.List:
		b.WriteByte('L')
	default:
		b.WriteByte('A')
	}
	for _, e := range s.Entries {
		if e.Optional {
			b.WriteByte('?')
		} else {
			b.WriteByte('!')
		}
		if e.IsInt {
			b.WriteString("i" + strconv.FormatInt(e.IntKey, 10))
		} else {
			b.WriteString("s" + strconv.Quote(e.Key))
		}
	}
	return b.String()
}

// listLike reports a shape whose keys number its entries from 0, those
// that may be missing last: a list, as one written `list{…}` or built from
// lists is.
func listLike(s types.Shape) bool {
	if s.Unsealed {
		return false
	}
	if s.List {
		return true
	}
	for i, e := range s.Entries {
		if !e.IsInt || e.IntKey != int64(i) || e.Optional {
			return false
		}
	}
	return true
}

// arrayWithEntryKey is the entry of a constant key, string or int, with the
// value vt.
func arrayWithEntryKey(k, vt types.Type) types.ShapeEntry {
	if i, ok := k.(types.Int); ok {
		return types.ShapeEntry{IsInt: true, IntKey: i.Lo, Type: vt}
	}
	return types.ShapeEntry{Key: k.(types.StringLit).V, Type: vt}
}

// collapseShapes is one shape for the union of shapes, as PHPStan collapses
// the arguments of array_merge() and a spread: each key, its values
// united, optional unless every shape has it for certain; unsealed if one
// is; a list where every shape is one, `list{0: 1|3, 1?: 2}` of `[1, 2]`
// and `[3]`, whose keys may end earlier and have no gap. The entries are
// indexed by key, so that it is linear in them.
func collapseShapes(shapes []types.Shape) types.Shape {
	if len(shapes) == 1 {
		return shapes[0]
	}
	type acc struct {
		typ      types.Type
		seen     int
		optional bool
	}
	var order []types.ShapeEntry
	byKey := map[types.ShapeEntry]*acc{}
	for _, s := range shapes {
		for _, e := range s.Entries {
			k := types.ShapeEntry{Key: e.Key, IntKey: e.IntKey, IsInt: e.IsInt}
			a, ok := byKey[k]
			if !ok {
				a = &acc{}
				byKey[k] = a
				order = append(order, k)
			}
			// United one by one: a union counts its literals before it
			// drops the copies, and widens past 32 (`kind: 'a'` of a
			// thousand shapes would be string).
			a.typ = types.MakeUnion(a.typ, e.Type)
			a.seen++
			a.optional = a.optional || e.Optional
		}
	}
	c := types.Shape{List: true}
	for _, k := range order {
		a := byKey[k]
		k.Type, k.Optional = a.typ, a.optional || a.seen < len(shapes)
		c.Entries = append(c.Entries, k)
	}
	for _, s := range shapes {
		c.Unsealed = c.Unsealed || s.Unsealed
		c.List = c.List && listLike(s)
	}
	c.List = c.List && !c.Unsealed && len(c.Entries) > 0
	return c
}

func widenKeys(k types.Type) types.Type {
	// Keep literal keys up to the union limit; MakeUnion widens beyond it.
	return k
}

// arrayWithEntry adds key => value to a shape (append when key is nil).
func arrayWithEntry(s types.Shape, key types.Type, vt types.Type) types.Type {
	if key == nil {
		next := int64(0)
		for _, e := range s.Entries {
			if e.IsInt && !e.Optional && e.IntKey >= next {
				next = e.IntKey + 1
			}
		}
		if s.Unsealed {
			return types.Array{Key: types.TArrayKey, Value: types.MakeUnion(shapeValues(s), vt), NonEmpty: true}
		}
		if open := openNumbers(s, next); len(open) > 1 {
			return appendOpen(s, open, vt)
		}
		for _, e := range s.Entries {
			if e.IsInt && e.IntKey >= next {
				next = e.IntKey + 1
			}
		}
		s.Entries = append(append([]types.ShapeEntry{}, s.Entries...), types.ShapeEntry{IsInt: true, IntKey: next, Type: vt})
		s.List = s.List || (len(s.Entries) == 1 && next == 0)
		if s.List {
			for i, e := range s.Entries {
				if !e.IsInt || e.IntKey != int64(i) {
					s.List = false
				}
			}
		}
		return s
	}
	switch k := key.(type) {
	case types.StringLit:
		s.Entries = upsert(append([]types.ShapeEntry{}, s.Entries...), types.ShapeEntry{Key: k.V, Type: vt})
		s.List = false
		return s
	case types.Int:
		if k.Lo == k.Hi {
			s.Entries = upsert(append([]types.ShapeEntry{}, s.Entries...), types.ShapeEntry{IsInt: true, IntKey: k.Lo, Type: vt})
			if s.List {
				for i, e := range s.Entries {
					if !e.IsInt || e.IntKey != int64(i) {
						s.List = false
					}
				}
			}
			return s
		}
	}
	// A key that is one of several literals, every one an entry the shape
	// has (`$widths[$i]` with $i the key of a shape of the same length):
	// each of those entries may be the one written, so each takes the value
	// as well; the shape and its list-ness stay (PHPStan does the same).
	if entries, ok := shapeEntriesForKeys(s, key, vt); ok {
		s.Entries = entries
		return s
	}
	if len(s.Entries) == 0 && !s.Unsealed {
		// `$r = []; $r[$k] = $v`: the first entry sets the key and value types.
		return types.Array{Key: key, Value: vt, NonEmpty: true}
	}
	return types.Array{Key: types.MakeUnion(shapeKeys(s), key), Value: types.MakeUnion(shapeValues(s), vt), NonEmpty: true}
}

// openNumbers is the numbers an append to s may take: next, one past its
// last certain integer key, and one past each optional integer key above
// that, which may or may not be there.
func openNumbers(s types.Shape, next int64) []int64 {
	open := []int64{next}
	for _, e := range s.Entries {
		if e.IsInt && e.Optional && e.IntKey >= next {
			open = append(open, e.IntKey+1)
		}
	}
	slices.Sort(open)
	return slices.Compact(open)
}

// appendOpen is s with vt appended under each number in open, as PHPStan's
// builder appends after optional keys: `list{0: 1, 1?: 2}` and 3 is
// `list{0: 1, 1: 2|3, 2?: 3}`. Each value goes where an entry may be, and
// a new last one is optional. In a list the first open number holds an
// entry for certain afterwards: the value went there, or an entry was.
func appendOpen(s types.Shape, open []int64, vt types.Type) types.Shape {
	entries := append([]types.ShapeEntry{}, s.Entries...)
	at := map[int64]int{}
	for i, e := range entries {
		if e.IsInt {
			at[e.IntKey] = i
		}
	}
	for j, n := range open {
		if i, ok := at[n]; ok {
			entries[i].Type = types.MakeUnion(entries[i].Type, vt)
			if j == 0 && s.List {
				entries[i].Optional = false
			}
			continue
		}
		entries = append(entries, types.ShapeEntry{IsInt: true, IntKey: n, Type: vt, Optional: !(j == 0 && s.List)})
	}
	s.Entries = entries
	return s
}

// shapeEntriesForKeys is s's entries with vt joined into every entry a
// literal member of key names, when key is a small set of literals (a
// union, or an int range) and every one of them is an entry s has.
func shapeEntriesForKeys(s types.Shape, key, vt types.Type) ([]types.ShapeEntry, bool) {
	if s.Unsealed || len(s.Entries) == 0 {
		return nil, false
	}
	var keys []types.ShapeEntry
	for _, m := range types.Members(key) {
		switch k := m.(type) {
		case types.StringLit:
			keys = append(keys, types.ShapeEntry{Key: k.V})
		case types.Int:
			if k.Hi-k.Lo >= int64(len(s.Entries)) || k.Hi-k.Lo < 0 {
				return nil, false
			}
			for n := k.Lo; n <= k.Hi; n++ {
				keys = append(keys, types.ShapeEntry{IsInt: true, IntKey: n})
			}
		default:
			return nil, false
		}
	}
	if len(keys) == 0 {
		return nil, false
	}
	written := map[int]bool{}
	for _, k := range keys {
		found := false
		for i, e := range s.Entries {
			if e.IsInt == k.IsInt && e.IntKey == k.IntKey && e.Key == k.Key {
				written[i] = true
				found = true
			}
		}
		if !found {
			return nil, false
		}
	}
	entries := append([]types.ShapeEntry{}, s.Entries...)
	for i := range entries {
		if written[i] {
			entries[i].Type = types.MakeUnion(entries[i].Type, vt)
		}
	}
	return entries, true
}

// destructure handles [$a, 'k' => $b] = value. src, when given, is the
// value's expression: an offset narrowed on it (`is_array($reply[1] ??
// null)`) is what the matching variable receives.
func (x *fnCtx) destructure(t *ast.ArrayLit, vt types.Type, env *state, span ast.Span, src ast.Expr) {
	if t.Kind == ast.ArrayList {
		x.removed(t.S, "list()", "[...] destructuring")
	}
	if !x.destructurable(vt, t.S) {
		// PHPStan's offsetAccess.nonArray: PHP reads null from a scalar or
		// null, and throws for an object that is no ArrayAccess.
		x.report(t.S, P2302, "cannot use array destructuring on %s", x.typeName(vt))
	}
	pos := int64(0)
	for _, item := range t.Items {
		if item == nil {
			pos++
			continue
		}
		var keyT types.Type
		if item.Key != nil {
			var raw types.Type
			raw, keyT = x.keyExpr(item.Key, env)
			if mayBeArray(vt) {
				x.nullOffset(raw, item.Key.Span())
			}
		} else {
			keyT = types.IntLit(pos)
			pos++
		}
		var elem types.Type
		if src != nil {
			var idx ast.Expr = item.Key
			if idx == nil {
				idx = &ast.IntLit{Raw: strconv.FormatInt(pos-1, 10)}
			}
			if key, ok := x.pathKey(&ast.Index{Array: src, Index: idx}); ok {
				if nt, found := env.getPath(key); found {
					elem = nt
				}
			}
		}
		if elem != nil {
			// narrowed on the source
		} else if types.IsMixed(vt) {
			elem = types.TMixed
		} else {
			// A key the shapes may lack is read as a direct read is: P2321,
			// and the entry's own type (indexType). It is reported at the
			// pattern, which PHPStan reports on the first line of.
			mayLack := offsetMayBeMissing(vt, keyT)
			if mayLack {
				x.reportMayLack(t.S, keyT, vt, true)
			}
			var out []types.Type
			for _, m := range types.Members(vt) {
				switch m := m.(type) {
				case types.Shape:
					if entry, ok := constantEntry(m, keyT); ok && mayLack {
						out = append(out, entry.Type)
						continue
					} else if mayLack && !m.Unsealed {
						continue
					}
					et, found, _ := shapeOffset(m, keyT)
					if found {
						out = append(out, et)
					} else {
						out = append(out, types.TNull)
					}
				case types.Array:
					out = append(out, m.Value)
				case types.Null:
					out = append(out, types.TNull)
				default:
					out = append(out, types.TMixed)
				}
			}
			elem = types.MakeUnion(out...)
		}
		if item.ByRef {
			if v, ok := item.Value.(*ast.Variable); ok {
				x.byRef[v.Name] = true
			}
		}
		x.assignTo(item.Value, elem, env, span)
	}
}

// classTypeName names an object type in a diagnostic: the class for one,
// the type as written otherwise (an intersection, `static`).
func classTypeName(x *fnCtx, m types.Type, classes []*program.Class) string {
	if _, isClass := m.(types.Class); isClass && len(classes) == 1 {
		return classes[0].Name
	}
	return x.typeName(m)
}

// destructurable reports whether every member of t can be destructured:
// an array, or an object that is an ArrayAccess. An already reported value
// (the error type) is let through, one mistake one diagnostic.
func (x *fnCtx) destructurable(t types.Type, span ast.Span) bool {
	for _, m := range types.Members(t) {
		switch m := m.(type) {
		case types.Array, types.Shape, types.Never, types.Unknown:
		case types.Class, types.Static, types.This, types.Intersection, types.Param:
			if p, isParam := m.(types.Param); isParam && openParam(p) {
				return false
			}
			if !x.arrayAccess(m, span) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// arrayAccess reports whether an object type is an ArrayAccess: for an
// intersection, any of its classes being one is enough.
func (x *fnCtx) arrayAccess(m types.Type, span ast.Span) bool {
	for _, cl := range x.classesOf(m, span) {
		if cl.IsSubclassOf("ArrayAccess") {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ removed constructs

// removed reports a construct Phaenon does not have (P1), in Phaenon files
// only, and says whether it did: the finding is then the construct's one.
func (x *fnCtx) removed(span ast.Span, what, instead string) bool {
	if x.file.Mode != lexer.ModePhaenon {
		return false
	}
	x.report(span, P1001, "%s is not available in Phaenon; use %s", what, instead)
	return true
}

// withdrawn reports a `concurrently` block or a `spawn`, syntax that left
// the language (P1001; design/roadmap.md D25, concurrency.md §17). The code
// inside is checked as the closure the block compiled to, and what the
// body assigned, like the construct's value, has the error type afterwards:
// the rewrite to amp is the one finding.
func (x *fnCtx) withdrawn(e *ast.Withdrawn, env *state) types.Type {
	x.report(e.Kw, P1001, "`concurrently` blocks and `spawn` left the language (roadmap D25, concurrency.md §17); write amp's async() and await()")
	x.argsOnly(e.Opts, env)
	if e.Expr != nil {
		x.expr(e.Expr, env, nil)
		return types.TError
	}
	c := x.child(false)
	c.generator = bodyHasYield(e.Body)
	cenv := env.clone()
	cenv.dead = false
	c.stmts(e.Body, cenv)
	for name, info := range cenv.vars {
		if outer, ok := env.get(name); !ok || !types.Equal(outer.t, info.t) {
			env.set(name, types.TError)
		}
	}
	return types.TError
}

// boundedPlus is arrayPlus of l and r with their shapes bounded as a write
// bounds them (boundedShapes): `$o += …` in a chain of if/else adds them up
// as writes do. Each pair of shapes gives one, so the product is bounded
// too: past maxShapeValues entries in all, the shapes one key tells apart
// merge first, which loses nothing, and then each side's collapse into
// one, which loses what their values said together, as the steps of
// boundedShapes past the limit do.
func boundedPlus(l, r types.Type) types.Type {
	l, r = boundedShapes(l), boundedShapes(r)
	if pairedEntries(l, r) > maxShapeValues {
		l, r = shapesReduced(l, false), shapesReduced(r, false)
	}
	if pairedEntries(l, r) > maxShapeValues {
		l, r = shapesReduced(l, true), shapesReduced(r, true)
	}
	return arrayPlus(l, r)
}

// pairedEntries is how many entries the shapes of l and r give paired, each
// with each: what arrayPlus of them holds.
func pairedEntries(l, r types.Type) int {
	count := func(t types.Type) (shapes, entries int) {
		for _, m := range types.Members(t) {
			if s, ok := m.(types.Shape); ok {
				shapes++
				entries += len(s.Entries)
			}
		}
		return shapes, entries
	}
	ln, le := count(l)
	rn, re := count(r)
	return ln*re + rn*le
}

// shapesReduced is t with its shapes merged where one key tells two apart
// (reduceShapes), and collapsed into one as well where collapse is true.
func shapesReduced(t types.Type, collapse bool) types.Type {
	var shapes []types.Shape
	var out []types.Type
	for _, m := range types.Members(t) {
		if s, ok := m.(types.Shape); ok {
			shapes = append(shapes, s)
		} else {
			out = append(out, m)
		}
	}
	if len(shapes) < 2 {
		return t
	}
	shapes = reduceShapes(shapes)
	if collapse && len(shapes) > 1 {
		shapes = []types.Shape{collapseShapes(shapes)}
	}
	for _, s := range shapes {
		out = append(out, s)
	}
	return types.MakeUnion(out...)
}

// arrayPlus types `$a + $b` on arrays: the left operand's entries, then the
// right's for the keys the left lacks. Two sealed shapes merge entry by
// entry; two lists are a list, the longer one's numbers following the
// shorter's, as PHPStan has it; anything else is the union of keys and of
// values.
func arrayPlus(l, r types.Type) types.Type {
	var out []types.Type
	for _, lm := range types.Members(l) {
		for _, rm := range types.Members(r) {
			ls, lok := lm.(types.Shape)
			rs, rok := rm.(types.Shape)
			if lok && rok && !ls.Unsealed && !rs.Unsealed {
				merged := types.Shape{Entries: append([]types.ShapeEntry{}, ls.Entries...)}
				for _, e := range rs.Entries {
					have, found := shapeEntry(merged, e)
					switch {
					case !found:
						merged.Entries = append(merged.Entries, e)
					case have.Optional:
						// The right side's entry stands where the left's is
						// missing: the key is there when either has it.
						merged.Entries = upsert(merged.Entries, types.ShapeEntry{Key: e.Key, IntKey: e.IntKey, IsInt: e.IsInt,
							Type: types.MakeUnion(have.Type, e.Type), Optional: e.Optional})
					}
				}
				out = append(out, merged)
				continue
			}
			lk, lv, lne := arrayKV(lm)
			rk, rv, rne := arrayKV(rm)
			if isList(lm) && isList(rm) {
				out = append(out, types.Array{Key: types.TInt, Value: types.MakeUnion(lv, rv), List: true, NonEmpty: lne || rne})
				continue
			}
			out = append(out, types.Array{Key: types.MakeUnion(lk, rk), Value: types.MakeUnion(lv, rv), NonEmpty: lne || rne})
		}
	}
	return types.MakeUnion(out...)
}

// isList reports an array type member that is a list: a list, or a shape
// whose keys number its entries from 0, those that may be missing last.
func isList(m types.Type) bool {
	switch m := m.(type) {
	case types.Array:
		return m.List
	case types.Shape:
		if listLike(m) {
			return true
		}
		// `array{0: int, 1?: int}` is a list as PHPStan reads it: one
		// optional key, the last, leaves no gap; two may.
		for i, e := range m.Entries {
			if !e.IsInt || e.IntKey != int64(i) || e.Optional && i != len(m.Entries)-1 {
				return false
			}
		}
		return !m.Unsealed && len(m.Entries) > 0
	}
	return false
}

// shapeEntry is the entry of s under key's key.
func shapeEntry(s types.Shape, key types.ShapeEntry) (types.ShapeEntry, bool) {
	for _, e := range s.Entries {
		if e.IsInt == key.IsInt && e.IntKey == key.IntKey && e.Key == key.Key {
			return e, true
		}
	}
	return types.ShapeEntry{}, false
}

// shapeEntryKey is the type of an entry's key: the literal it is.
func shapeEntryKey(e types.ShapeEntry) types.Type {
	if e.IsInt {
		return types.IntLit(e.IntKey)
	}
	return types.StringLit{V: e.Key}
}

// arrayKV is an array-like member's key and value types, never for an
// empty shape, and whether it is known to be non-empty.
func arrayKV(m types.Type) (key, value types.Type, nonEmpty bool) {
	switch m := m.(type) {
	case types.Array:
		return m.Key, m.Value, m.NonEmpty
	case types.Shape:
		if len(m.Entries) == 0 && !m.Unsealed {
			return types.TNever, types.TNever, false
		}
		for _, e := range m.Entries {
			if !e.Optional {
				nonEmpty = true
			}
		}
		return shapeKeys(m), shapeValues(m), nonEmpty
	}
	return types.TArrayKey, types.TMixed, false
}

// coalesceConds records on $n, assigned `$a['k'] ?? $default`, the types
// of $n that say the left side was set, each matched exactly as PHPStan
// matches its holders of the assignment: $n without null where the default
// is null (`$n !== null`, isset($n)); $n's truthy part where the default is
// always falsy (`if ($n)`); $n without the default where the default is
// one falsy value, the empty string or 0 or false or [] (`$n !== 0`). Any
// other test of $n, `is_string($n)` after `?? 5`, says nothing of the
// offset, as in PHPStan; a default that may be anything says nothing
// either. After `?? throw` the offset is set already (coalesce).
func (x *fnCtx) coalesceConds(name string, e *ast.Binary, then, els, before, env *state) {
	if _, throws := e.Right.(*ast.Throw); throws {
		return
	}
	info, ok := env.get(name)
	if !ok {
		return
	}
	def := x.expr(e.Right, els, nil)
	if types.IsMixed(def) {
		return
	}
	var whens []types.Type
	add := func(w types.Type) {
		if isNeverType(w) || types.Equal(w, info.t) {
			return
		}
		for _, have := range whens {
			if types.Equal(have, w) {
				return
			}
		}
		whens = append(whens, w)
	}
	if isNullType(def) {
		add(types.StripNull(info.t))
	}
	if isNeverType(types.Truthy(def)) {
		add(types.Truthy(info.t))
	}
	if falsyScalar(def) {
		add(x.subtract(info.t, def, x.p()))
	}
	delete(then.vars, name)
	var holders []*condHolder
	for _, w := range whens {
		h := condBranch(w, then, before)
		h.exact = true
		holders = append(holders, h)
	}
	env.setConds(name, holders...)
}

// falsyScalar reports one of PHP's falsy values as a type: null, false, 0,
// 0.0, the empty string or '0', or the empty array.
func falsyScalar(t types.Type) bool {
	switch v := t.(type) {
	case types.Null:
		return true
	case types.BoolLit:
		return !v.V
	case types.Int:
		return v.Lo == 0 && v.Hi == 0
	case types.FloatLit:
		return v.V == 0
	case types.StringLit:
		return v.V == "" || v.V == "0"
	case types.Shape:
		return len(v.Entries) == 0 && !v.Unsealed
	}
	return false
}

// rememberCondition records on a variable assigned a condition, or a
// ternary, what each branch narrowed: `$ok = is_string($v) && …` says $v
// is a string when $ok is true; `$s = $b instanceof X ? $b->s : null`
// says $b is an X when $s is not null. A later test of the variable
// applies the branch taken (PHPStan's conditional expressions). The
// value was typed already; the branches are read in a silent pass.
func (x *fnCtx) rememberCondition(name string, value ast.Expr, env *state) {
	for {
		p, ok := value.(*ast.Paren)
		if !ok {
			break
		}
		value = p.Expr
	}
	var whenThen, whenElse types.Type
	var condExpr ast.Expr
	var ternary *ast.Ternary
	var coalesce *ast.Binary
	switch v := value.(type) {
	case *ast.Ternary:
		ternary, condExpr = v, v.Cond
	case *ast.Binary:
		switch v.Op {
		case token.T_BOOLEAN_AND, token.T_LOGICAL_AND, token.T_BOOLEAN_OR, token.T_LOGICAL_OR,
			token.T_IS_IDENTICAL, token.T_IS_NOT_IDENTICAL, token.T_IS_EQUAL, token.T_IS_NOT_EQUAL:
			condExpr = v
		case token.T_COALESCE:
			// `$n = $a['k'] ?? null` is `isset($a['k']) ? $a['k'] : null`:
			// a $n that is not null says the offset is set (coalesceConds).
			coalesce, condExpr = v, &ast.Isset{S: v.Left.Span(), Vars: []ast.Expr{v.Left}}
		}
	case *ast.Unary:
		if v.Op == token.Kind('!') {
			condExpr = v
		}
	case *ast.Instanceof, *ast.IsExpr, *ast.Isset, *ast.Empty:
		condExpr = value
	case *ast.Call:
		// The type tests, the UTF-8 test that tells text from bytes
		// (design/strings.md §6) and the test of a key.
		if n, ok := v.Callee.(*ast.Name); ok {
			if name := strings.ToLower(n.Parts[len(n.Parts)-1]); predicateTarget(name) != nil || name == "mb_check_encoding" || name == "array_key_exists" || name == "key_exists" {
				condExpr = value
			}
		}
	}
	if condExpr == nil {
		return
	}
	before := env.clone()
	saved := x.c.mark()
	then, els := x.cond(condExpr, env.clone())
	if coalesce != nil {
		x.coalesceConds(name, coalesce, then, els, before, env)
		x.c.rollback(saved)
		return
	}
	switch {
	case ternary != nil:
		if ternary.Then == nil {
			whenThen = types.Truthy(x.condValueType)
		} else {
			whenThen = x.expr(ternary.Then, then, nil)
		}
		whenElse = x.expr(ternary.Else, els, nil)
	default:
		whenThen, whenElse = types.TTrue, types.TFalse
	}
	x.c.rollback(saved)
	delete(then.vars, name)
	delete(els.vars, name)
	env.setConds(name, x.armHolder(whenThen, whenElse, then, before), x.armHolder(whenElse, whenThen, els, before))
}

// armHolder is the branch of a condition whose value is of type arm, held
// where the variable assigned it has a value the other branch, of type
// other, cannot give, as PHPStan has it: arm less other, when nothing of
// it is a value of other. `$b instanceof X ? $b->n : null` with $b->n an
// ?int says $b is an X where the value is an int. Mixed less a type is no
// type the checker has: that branch is held where a test rules out every
// value of other (condHolder.unless), `$b instanceof X ? $b->s : null`
// with $b->s mixed says $b is an X where the value is not null. nil where
// the variable tells nothing of the branch.
func (x *fnCtx) armHolder(arm, other types.Type, branch, before *state) *condHolder {
	if types.IsMixed(arm) {
		if types.IsMixed(other) {
			return nil
		}
		h := condBranch(nil, branch, before)
		if h != nil {
			h.unless = other
		}
		return h
	}
	if !isNeverType(types.Intersect(arm, other, x.p())) {
		arm = x.subtract(arm, other, x.p())
		if isNeverType(arm) || types.Overlaps(arm, other, x.p()) {
			return nil
		}
	}
	return condBranch(arm, branch, before)
}
