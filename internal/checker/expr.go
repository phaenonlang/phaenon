package checker

import (
	"math"
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// superglobals are always defined arrays.
var superglobals = map[string]bool{
	"GLOBALS": true, "_SERVER": true, "_GET": true, "_POST": true, "_FILES": true,
	"_COOKIE": true, "_SESSION": true, "_REQUEST": true, "_ENV": true, "argv": true, "argc": true,
}

// expr types an expression in env, with an optional expected type for
// contextual typing. It updates env for assignments and other side effects.
func (x *fnCtx) expr(e ast.Expr, env *state, expected types.Type) types.Type {
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
		return x.call(e, env, expected)
	case *ast.MethodCall:
		return x.methodCall(e, env, expected)
	case *ast.StaticCall:
		return x.staticCall(e, env, expected)
	case *ast.New:
		return x.newExpr(e, env)
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
		return types.TMixed
	case *ast.Eval:
		x.expr(e.Expr, env, nil)
		x.removed(e.S, "eval()", "nothing — generate code at build time instead")
		return types.TMixed
	case *ast.Closure:
		return x.closure(e, env, expected)
	case *ast.ArrowFunc:
		return x.arrowFunc(e, env, expected)
	case *ast.Match:
		return x.match(e, env, expected)
	case *ast.Yield:
		return x.yield(e, env)
	case *ast.YieldFrom:
		return x.yieldFrom(e, env)
	case *ast.Throw:
		t := x.expr(e.Expr, env, nil)
		if !types.IsMixed(t) && !types.IsSubtype(t, types.NewClass("Throwable"), x.p()) {
			x.report(e.Expr.Span(), P2317, "cannot throw %s: not a Throwable", x.typeName(t))
		}
		env.dead = true
		return types.TNever
	case *ast.IsExpr:
		return x.isType(e, env)
	case *ast.AsExpr:
		return x.asExpr(e, env)
	case *ast.Name:
		// The parser leaves constants as bare names. A type parameter's
		// name denotes its runtime descriptor.
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
		x.removed(e.S, "${name} string interpolation", "{$name}")
		return types.TMixed
	}
	return types.TMixed
}

// exprLenient types an expression without reporting undefined variables
// or missing offsets (isset/empty/?? operands).
func (x *fnCtx) exprLenient(e ast.Expr, env *state) types.Type {
	switch e := e.(type) {
	case *ast.Variable:
		return x.variable(e, env, true)
	case *ast.Index:
		base := x.exprLenient(e.Array, env)
		if e.Index != nil {
			x.expr(e.Index, env, nil)
		}
		return x.indexType(base, e, env, true)
	case *ast.PropertyFetch:
		return x.propertyFetch(e, env, true)
	case *ast.Paren:
		return x.exprLenient(e.Expr, env)
	}
	return x.expr(e, env, nil)
}

// ------------------------------------------------------------------ atoms

func (x *fnCtx) variable(v *ast.Variable, env *state, lenient bool) types.Type {
	if v.Name == "this" {
		if x.class == nil || x.static {
			x.report(v.S, P2311, "$this is not available in a static context")
			return types.TObject
		}
		if t, ok := env.getPath("$this"); ok {
			return t
		}
		return x.thisType
	}
	if superglobals[v.Name] {
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
			x.checkStringable(t, p.Span())
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

func (x *fnCtx) checkStringable(t types.Type, span ast.Span) {
	for _, m := range types.Members(t) {
		switch m := m.(type) {
		case types.Mixed:
			x.report(span, P2312, "mixed cannot be converted to string")
			return
		case types.Array, types.Shape:
			x.report(span, P2312, "array cannot be converted to string")
			return
		case types.Class:
			if cl := x.p().Class(m.Name); cl != nil && !cl.IsSubclassOf("Stringable") && cl.FindMethod("__toString") == nil {
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
	x.checkStringable(t, e.Span())
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
	isList := true
	nonEmpty := false
	for _, item := range e.Items {
		if item == nil {
			continue
		}
		nonEmpty = true
		if item.Unpack {
			t := x.expr(item.Value, env, nil)
			k, v, ok := types.IterableKV(t, x.p())
			if !ok {
				x.report(item.Value.Span(), P2303, "cannot unpack %s", x.typeName(t))
			}
			isShape = false
			keys = append(keys, k)
			values = append(values, v)
			if !types.IsSubtype(k, types.TInt, nil) {
				isList = false
			}
			continue
		}
		var ev types.Type
		if item.Key != nil {
			kt := x.expr(item.Key, env, nil)
			var entryExpected types.Type = expectedValue
			if sh, ok := expectedShapeEntry(expected, kt); ok {
				entryExpected = sh
			}
			ev = x.expr(item.Value, env, entryExpected)
			keys = append(keys, normaliseKey(kt))
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
			ev = x.expr(item.Value, env, expectedValue)
			keys = append(keys, types.IntLit(nextInt))
			shape.Entries = upsert(shape.Entries, types.ShapeEntry{IsInt: true, IntKey: nextInt, Type: ev})
			nextInt++
		}
		values = append(values, ev)
	}
	if !nonEmpty {
		return types.Shape{List: true} // [] is an empty list
	}
	_ = expectedKey
	if isShape && len(shape.Entries) <= 32 {
		shape.List = isList
		return shape
	}
	return types.Array{Key: types.MakeUnion(keys...), Value: types.MakeUnion(values...), List: isList, NonEmpty: true}
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

func normaliseKey(t types.Type) types.Type {
	switch k := t.(type) {
	case types.BoolLit, types.Bool:
		return types.TInt
	case types.Null:
		return types.StringLit{V: ""}
	case types.Float, types.FloatLit:
		return types.TInt
	case types.StringLit:
		if n, err := strconv.ParseInt(k.V, 10, 64); err == nil && strconv.FormatInt(n, 10) == k.V {
			return types.IntLit(n)
		}
	}
	return t
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
	if len(e.Name.Parts) == 1 && e.Name.Kind == ast.NameUnqualified {
		switch strings.ToLower(e.Name.Parts[0]) {
		case "true":
			return types.TTrue
		case "false":
			return types.TFalse
		case "null":
			return types.TNull
		case "php_eol":
			return types.String{NonEmpty: true}
		case "__compiler_halt_offset__":
			return types.TInt
		}
	}
	ns, global := x.res.Scope().ResolveConst(e.Name)
	for _, name := range []string{ns, global} {
		if k := x.p().Constant(name); k != nil {
			return x.p().ConstantType(k)
		}
	}
	x.report(e.S, P2003, "unknown constant %s", global)
	return types.TError
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
	var recv types.Type
	var classes []*program.Class
	if n, ok := e.Class.(*ast.Name); ok {
		raw := x.res.ClassName(n)
		if raw == "parent" && x.class != nil {
			if pc := x.class.ParentClass(); pc != nil {
				recv, classes = types.Class{Name: pc.Name, Args: x.class.ParentArgs}, []*program.Class{pc}
			} else {
				x.report(n.S, P2011, "class %s has no parent", x.class.Name)
				return types.TMixed
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
	name, ok := identName(e.Name)
	if !ok {
		x.expr(e.Name, env, nil)
		return types.TMixed
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
	var out []types.Type
	for _, cl := range classes {
		if cs := cl.FindCase(name); cs != nil {
			out = append(out, types.EnumCase{Class: cl.Name, Case: cs.Name})
			continue
		}
		k := cl.FindConst(name)
		if k == nil {
			x.report(e.Name.Span(), P2004, "unknown constant %s::%s", cl.Name, name)
			return types.TError
		}
		if !x.canAccess(k.Vis, k.Class) {
			x.report(e.Name.Span(), P2108, "constant %s::%s is %s", k.Class.Name, name, k.Vis)
		}
		out = append(out, x.bindReceiver(x.p().ConstantType(k), recv, k.Class))
	}
	return types.MakeUnion(out...)
}

// ------------------------------------------------------------------ members

func (x *fnCtx) propertyFetch(e *ast.PropertyFetch, env *state, lenient bool) types.Type {
	if key, ok := pathKey(e); ok {
		if t, ok := env.getPath(key); ok {
			// still type the object for side effects / diagnostics? The
			// object is a stable path; skip re-checking.
			return t
		}
	}
	obj := x.expr(e.Object, env, nil)
	name, ok := identName(e.Name)
	if !ok {
		x.expr(e.Name, env, nil)
		return types.TMixed
	}
	return x.propertyType(obj, name, e, env, lenient)
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
		out = append(out, x.expr(cs.Value, newEnv(), nil))
	}
	if len(out) == 0 {
		return nil, false
	}
	return types.MakeUnion(out...), true
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
	} else if types.IsNullable(obj) && !types.IsMixed(obj) && !lenient {
		if _, isNull := obj.(types.Null); isNull {
			x.report(e.Object.Span(), P2105, "cannot access property $%s on null", name)
			return types.TNull
		}
		x.report(e.Object.Span(), P2105, "cannot access property $%s on %s (possibly null)", name, x.typeName(obj))
	}
	base := types.StripNull(obj)
	if types.IsMixed(base) {
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
			out = append(out, types.TMixed)
			continue
		}
		classes := x.classesOf(m, e.Object.Span())
		for _, cl := range classes {
			pr := cl.FindProperty(name)
			if pr == nil {
				if allowsDynamic(cl) {
					out = append(out, types.TMixed)
					continue
				}
				if !lenient {
					x.report(span, P2005, "unknown property %s::$%s", cl.Name, name)
				}
				out = append(out, types.TError)
				continue
			}
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
	if len(out) == 0 {
		return types.TMixed
	}
	t := types.MakeUnion(out...)
	if e.NullSafe && types.IsNullable(obj) {
		t = types.Nullable(t)
	}
	return t
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
		raw := x.res.ClassName(n)
		if raw == "parent" && x.class != nil {
			if pc := x.class.ParentClass(); pc != nil {
				classes = []*program.Class{pc}
				recv = types.Class{Name: pc.Name}
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
	v, ok := e.Name.(*ast.Variable)
	if !ok {
		x.expr(e.Name, env, nil)
		return types.TMixed, nil
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
		if key, ok := pathKey(e); ok {
			if t, ok := env.getPath(key); ok {
				return t, pr
			}
		}
		return x.bindReceiver(pr.Type, recv, pr.Class), pr
	}
	return types.TMixed, nil
}

func (x *fnCtx) index(e *ast.Index, env *state) types.Type {
	if key, ok := pathKey(e); ok {
		if t, ok := env.getPath(key); ok {
			return t
		}
	}
	base := x.expr(e.Array, env, nil)
	if e.Index == nil {
		x.report(e.S, P2302, "[] can only be used for writing")
		return types.TMixed
	}
	return x.indexType(base, e, env, false)
}

// indexType types base[index].
func (x *fnCtx) indexType(base types.Type, e *ast.Index, env *state, lenient bool) types.Type {
	if e.Index == nil {
		return types.TMixed
	}
	idx := x.expr(e.Index, env, nil)
	if types.IsMixed(base) {
		return types.TMixed
	}
	var out []types.Type
	for _, m := range types.Members(base) {
		switch m := m.(type) {
		case types.Shape:
			t, found, certain := shapeOffset(m, idx)
			if !found {
				if !lenient && !m.Unsealed && certain {
					x.report(e.Index.Span(), P2313, "offset %s does not exist on %s", x.typeName(idx), x.typeName(m))
				}
				if m.Unsealed || !certain {
					out = append(out, types.TMixed)
				} else {
					out = append(out, types.TNull)
				}
				continue
			}
			out = append(out, t)
		case types.Array:
			if !types.IsMixed(idx) && !types.Overlaps(normaliseKey(idx), m.Key, x.p()) && !lenient {
				x.report(e.Index.Span(), P2313, "offset %s does not exist on %s", x.typeName(idx), x.typeName(m))
			}
			out = append(out, m.Value)
		case types.String, types.StringLit:
			out = append(out, types.TString)
		case types.Null:
			if !lenient {
				x.report(e.Array.Span(), P2302, "cannot use null as an array")
			}
			out = append(out, types.TNull)
		case types.Class, types.Static, types.This, types.Intersection, types.Param:
			var handled bool
			for _, cl := range x.classesOf(m, e.Array.Span()) {
				if cl.IsSubclassOf("ArrayAccess") {
					if g := cl.FindMethod("offsetGet"); g != nil {
						out = append(out, x.bindReceiver(g.Return, m, g.Class))
						handled = true
						continue
					}
				}
				if !lenient {
					x.report(e.Array.Span(), P2302, "cannot use %s as an array", cl.Name)
				}
				handled = true
				out = append(out, types.TMixed)
			}
			if !handled {
				out = append(out, types.TMixed)
			}
		case types.Object, types.Iterable, types.Unknown:
			out = append(out, types.TMixed)
		default:
			if !lenient {
				x.report(e.Array.Span(), P2302, "cannot use %s as an array", x.typeName(m))
			}
			out = append(out, types.TMixed)
		}
	}
	return types.MakeUnion(out...)
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
		if lit, ok := types.IsIntLit(t); ok && e.Op == token.Kind('-') && lit != math.MinInt64 {
			return types.IntLit(-lit)
		}
		if lit, ok := t.(types.FloatLit); ok && e.Op == token.Kind('-') {
			return types.FloatLit{V: -lit.V}
		}
		return x.arithResult(t, t, e.S, "unary "+string(rune(e.Op)))
	case token.Kind('~'):
		t := x.expr(e.Expr, env, nil)
		if types.IsSubtype(t, types.TString, x.p()) {
			return types.TString
		}
		return types.TInt
	case token.Kind('@'):
		return x.expr(e.Expr, env, nil)
	}
	return x.expr(e.Expr, env, nil)
}

func (x *fnCtx) cast(e *ast.Cast, env *state) types.Type {
	t := x.expr(e.Expr, env, nil)
	switch e.Op {
	case token.T_INT_CAST:
		if types.IsObjectLike(t) && !types.IsMixed(t) {
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
		if types.IsObjectLike(t) && !types.IsMixed(t) {
			x.report(e.S, P2309, "cannot cast %s to float", x.typeName(t))
		}
		return types.TFloat
	case token.T_STRING_CAST:
		x.checkStringable(t, e.Expr.Span())
		if lit, ok := types.IsIntLit(t); ok {
			return types.StringLit{V: strconv.FormatInt(lit, 10)}
		}
		if _, ok := t.(types.StringLit); ok {
			return t
		}
		if types.IsSubtype(t, types.TString, x.p()) {
			return t
		}
		return types.TString
	case token.T_BOOL_CAST:
		return types.TBool
	case token.T_ARRAY_CAST:
		for _, m := range types.Members(t) {
			switch m.(type) {
			case types.Array, types.Shape:
				return t
			}
		}
		if types.IsObjectLike(t) {
			return types.Array{Key: types.TString, Value: types.TMixed}
		}
		return types.TArray
	case token.T_OBJECT_CAST:
		return types.NewClass("stdClass")
	case token.T_UNSET_CAST:
		return types.TNull
	}
	if strings.Contains(strings.ToLower(e.Raw), "integer") || strings.Contains(strings.ToLower(e.Raw), "boolean") ||
		strings.Contains(strings.ToLower(e.Raw), "double") || strings.Contains(strings.ToLower(e.Raw), "real") || strings.Contains(strings.ToLower(e.Raw), "binary") {
		x.removed(e.S, e.Raw+" cast", "the canonical cast")
	}
	return types.TMixed
}

func (x *fnCtx) incDec(e *ast.IncDec, env *state) types.Type {
	t := x.expr(e.Expr, env, nil)
	var result types.Type
	switch {
	case types.IsSubtype(t, types.TInt, nil):
		result = types.TInt
	case types.IsSubtype(t, types.TFloat, nil):
		result = types.TFloat
	case types.IsSubtype(t, types.TString, nil):
		result = types.TString
	case types.IsSubtype(t, types.TNull, nil):
		if e.Inc {
			result = types.IntLit(1)
		} else {
			result = types.TNull
		}
	case types.IsMixed(t):
		result = types.TMixed
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
		left := x.expr(e.Left, env, nil)
		return x.pipe(e, left, env)
	}
	l := x.expr(e.Left, env, nil)
	r := x.expr(e.Right, env, nil)
	if e.Op == token.T_IS_IDENTICAL || e.Op == token.T_IS_NOT_IDENTICAL {
		if !types.IsMixed(l) && !types.IsMixed(r) && !types.Overlaps(l, r, x.p()) && !x.nativeOverlap(e.Left, l, e.Right, r, env) {
			word := "false"
			if e.Op == token.T_IS_NOT_IDENTICAL {
				word = "true"
			}
			x.report(e.S, P2305, "strict comparison between %s and %s is always %s", x.typeName(l), x.typeName(r), word)
		}
		return types.TBool
	}
	return x.binaryTypes(e.Op, l, r, e)
}

// binaryTypes computes the result of an operator on typed operands.
func (x *fnCtx) binaryTypes(op token.Kind, l, r types.Type, e ast.Node) types.Type {
	span := e.Span()
	switch op {
	case token.Kind('.'):
		x.checkStringable(l, span)
		x.checkStringable(r, span)
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
				return types.MakeUnion(l, r)
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
			}
		}
		return x.arithResult(l, r, span, "+")
	case token.Kind('-'), token.Kind('*'), token.T_POW:
		if a, ok := types.IsIntLit(l); ok {
			if b, ok := types.IsIntLit(r); ok {
				switch op {
				case token.Kind('-'):
					if s, ok := addInt(a, -b); ok && b != math.MinInt64 {
						return types.IntLit(s)
					}
				case token.Kind('*'):
					if s, ok := mulInt(a, b); ok {
						return types.IntLit(s)
					}
				}
			}
		}
		return x.arithResult(l, r, span, string(rune(op)))
	case token.Kind('/'):
		x.arithResult(l, r, span, "/")
		if a, ok := types.IsIntLit(l); ok {
			if b, ok := types.IsIntLit(r); ok && b != 0 && a%b == 0 {
				return types.IntLit(a / b)
			}
		}
		return types.MakeUnion(types.TInt, types.TFloat)
	case token.Kind('%'):
		x.arithResult(l, r, span, "%")
		return types.TInt
	case token.Kind('&'), token.Kind('|'), token.Kind('^'):
		if types.IsSubtype(l, types.TString, nil) && types.IsSubtype(r, types.TString, nil) {
			return types.TString
		}
		x.arithResult(l, r, span, string(rune(op)))
		return types.TInt
	case token.T_SL, token.T_SR:
		x.arithResult(l, r, span, "shift")
		return types.TInt
	case token.T_IS_IDENTICAL, token.T_IS_NOT_IDENTICAL:
		return types.TBool // binary() reports the impossible comparisons
	case token.T_IS_EQUAL, token.T_IS_NOT_EQUAL:
		return types.TBool
	case token.Kind('<'), token.Kind('>'), token.T_IS_SMALLER_OR_EQUAL, token.T_IS_GREATER_OR_EQUAL:
		if li, ok := l.(types.Int); ok {
			if ri, ok := r.(types.Int); ok {
				if always, value := orderingOutcome(op, li, ri); always {
					x.report(span, P2402, "comparison between %s and %s is always %v", x.typeName(l), x.typeName(r), value)
				}
			}
		}
		return types.TBool
	case token.T_SPACESHIP:
		return types.Int{Lo: -1, Hi: 1}
	case token.T_INSTANCEOF:
		return types.TBool
	}
	return types.TMixed
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

func mulInt(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	s := a * b
	if s/b != a {
		return 0, false
	}
	return s, true
}

// arithResult types an arithmetic operation and reports non-numeric operands.
func (x *fnCtx) arithResult(l, r types.Type, span ast.Span, op string) types.Type {
	numeric := types.MakeUnion(types.TInt, types.TFloat, types.TBool, types.TNull, types.String{Numeric: true})
	for _, t := range []types.Type{l, r} {
		if types.IsMixed(t) {
			continue
		}
		if !types.IsSubtype(t, numeric, x.p()) {
			x.report(span, P2301, "operator %s on %s", op, x.typeName(t))
		}
	}
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
	left := x.exprLenient(e.Left, env)
	switch e.Left.(type) {
	case *ast.Variable, *ast.PropertyFetch, *ast.StaticPropertyFetch:
		if !types.IsNullable(left) && !types.IsMixed(left) && !x.maybeUndefined(e.Left, env) {
			x.report(e.Left.Span(), P2308, "left side of ?? is %s, which is never null", x.typeName(left))
		}
	}
	// The right side is evaluated only when the left is null: narrow.
	rightEnv := env.clone()
	right := x.expr(e.Right, rightEnv, expected)
	*env = *join(env, rightEnv)
	return types.MakeUnion(types.StripNull(left), right)
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
	var thenT types.Type
	if e.Then == nil {
		// c ?: b — the value of c when truthy.
		thenT = types.Truthy(x.condValueType)
	} else {
		thenT = x.expr(e.Then, thenEnv, expected)
	}
	elseT := x.expr(e.Else, elseEnv, expected)
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

func (x *fnCtx) isset(e *ast.Isset, env *state) types.Type {
	for _, v := range e.Vars {
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
		vt := x.expr(e.Value, env, nil)
		if v, ok := e.Value.(*ast.Variable); ok {
			x.byRef[v.Name] = true
		}
		if v, ok := e.Target.(*ast.Variable); ok {
			x.byRef[v.Name] = true
		}
		x.assignTo(e.Target, vt, env, e.S)
		return vt
	}
	if e.Op == token.Kind('=') {
		expected := x.targetType(e.Target, env)
		vt := x.expr(e.Value, env, expected)
		x.assignTo(e.Target, vt, env, e.S)
		if v, ok := e.Target.(*ast.Variable); ok {
			env.setNative(v.Name, x.nativeOf(e.Value, env))
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
		res = x.binaryTypes(op, cur, vt, e)
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
	switch t := target.(type) {
	case *ast.PropertyFetch:
		if pr, recv := x.lookupPropertyForWrite(t, env); pr != nil {
			return x.bindReceiver(pr.Type, recv, pr.Class)
		}
	case *ast.StaticPropertyFetch:
		savedDiags := x.c.diags
		_, pr := x.staticPropertyFetch(t, env)
		x.c.diags = savedDiags
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
	savedDiags := x.c.diags
	obj := x.expr(t.Object, env.clone(), nil)
	x.c.diags = savedDiags
	for _, cl := range x.classesOf(types.StripNull(obj), t.Object.Span()) {
		if pr := cl.FindProperty(name); pr != nil {
			return pr, obj
		}
	}
	return nil, obj
}

// assignTo records an assignment of a value of type vt to a target.
func (x *fnCtx) assignTo(target ast.Expr, vt types.Type, env *state, span ast.Span) {
	switch t := target.(type) {
	case *ast.Paren:
		x.assignTo(t.Expr, vt, env, span)
	case *ast.Variable:
		if t.Name == "this" {
			x.report(t.S, P2311, "cannot re-assign $this")
			return
		}
		if _, isVoid := vt.(types.Void); isVoid {
			x.report(span, P2205, "result of a void function is used")
			vt = types.TNull
		}
		env.set(t.Name, vt)
	case *ast.PropertyFetch:
		x.assignProperty(t, vt, env, span)
	case *ast.StaticPropertyFetch:
		pt, pr := x.staticPropertyFetch(t, env)
		if pr != nil && !types.IsAcceptable(vt, pr.Type, x.p()) {
			x.report(span, P2201, "property %s::$%s (%s) does not accept %s", pr.Class.Name, pr.Name, x.typeName(pt), x.typeName(vt))
		}
		if key, ok := pathKey(t); ok {
			env.setPath(key, vt)
		}
	case *ast.Index:
		x.assignIndex(t, vt, env, span)
	case *ast.ArrayLit:
		x.destructure(t, vt, env, span)
	case *ast.VariableVariable:
		x.expr(t.Expr, env, nil)
		x.removed(t.S, "variable variables ($$x)", "a map<string, mixed>")
	default:
		x.expr(target, env, nil)
	}
}

func (x *fnCtx) assignProperty(t *ast.PropertyFetch, vt types.Type, env *state, span ast.Span) {
	name, ok := identName(t.Name)
	if !ok {
		x.expr(t.Object, env, nil)
		x.expr(t.Name, env, nil)
		return
	}
	obj := x.expr(t.Object, env, nil)
	base := types.StripNull(obj)
	if types.IsNullable(obj) && !types.IsMixed(obj) {
		x.report(t.Object.Span(), P2105, "cannot assign property $%s on %s (possibly null)", name, x.typeName(obj))
	}
	if types.IsMixed(base) {
		return
	}
	if !types.IsObjectLike(base) {
		x.report(t.Object.Span(), P2105, "cannot assign property $%s on %s", name, x.typeName(base))
		return
	}
	for _, cl := range x.classesOf(base, t.Object.Span()) {
		pr := cl.FindProperty(name)
		if pr == nil {
			if !allowsDynamic(cl) && !x.isConstructorOf(cl) {
				x.report(t.Name.Span(), P2005, "unknown property %s::$%s", cl.Name, name)
			}
			continue
		}
		if pr.Static {
			x.report(t.Name.Span(), P2210, "static property %s::$%s accessed through an instance", pr.Class.Name, name)
		}
		switch {
		case pr.Readonly:
			// Readonly properties are initialised once, in the declaring
			// class's constructor (or __clone); PHP rejects everything else.
			if !x.inClassScope(pr.Class) || !x.inInitialiser() {
				x.report(t.Name.Span(), P2206, "readonly property %s::$%s can only be assigned in the constructor of %s", pr.Class.Name, name, pr.Class.Name)
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
			x.report(span, P2201, "property %s::$%s (%s) does not accept %s", pr.Class.Name, name, x.typeName(declared), x.typeName(vt))
		}
	}
	if key, ok := pathKey(t); ok {
		env.invalidatePrefix(key)
		env.setPath(key, vt)
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
	return x.class != nil && strings.EqualFold(x.class.Name, cl.Name)
}

func (x *fnCtx) isConstructorOf(cl *program.Class) bool { return false }

// assignIndex handles $a[k] = v and $a[] = v, updating the array's type.
func (x *fnCtx) assignIndex(t *ast.Index, vt types.Type, env *state, span ast.Span) {
	base := x.exprLenient(t.Array, env)
	var keyT types.Type
	if t.Index != nil {
		keyT = normaliseKey(x.expr(t.Index, env, nil))
	}
	// Undefined variable used as an array: it becomes one.
	if v, ok := t.Array.(*ast.Variable); ok {
		if _, defined := env.get(v.Name); !defined {
			base = types.Shape{}
		}
	}
	var out []types.Type
	for _, m := range types.Members(base) {
		switch m := m.(type) {
		case types.Null:
			m2 := types.Shape{}
			out = append(out, arrayWithEntry(m2, keyT, vt))
		case types.Shape:
			out = append(out, arrayWithEntry(m, keyT, vt))
		case types.Array:
			k := m.Key
			if keyT != nil {
				k = types.MakeUnion(k, keyT)
			} else if !m.List {
				k = types.MakeUnion(k, types.TInt)
			}
			list := m.List && (keyT == nil)
			out = append(out, types.Array{Key: widenKeys(k), Value: types.MakeUnion(m.Value, vt), List: list, NonEmpty: true})
		case types.Mixed, types.Unknown:
			out = append(out, m)
		case types.Class, types.Static, types.This, types.Intersection:
			handled := false
			for _, cl := range x.classesOf(m, t.Array.Span()) {
				if cl.IsSubclassOf("ArrayAccess") {
					handled = true
					if s := cl.FindMethod("offsetSet"); s != nil && len(s.Params) >= 2 {
						declared := x.bindReceiver(s.Params[1].Type, m, s.Class)
						if !types.IsAcceptable(vt, declared, x.p()) {
							x.report(span, P2103, "%s::offsetSet() expects %s, %s given", cl.Name, x.typeName(declared), x.typeName(vt))
						}
					}
				}
			}
			if !handled {
				x.report(t.Array.Span(), P2302, "cannot use %s as an array", x.typeName(m))
			}
			out = append(out, m)
		case types.String, types.StringLit:
			out = append(out, types.TString)
		default:
			x.report(t.Array.Span(), P2302, "cannot use %s as an array", x.typeName(m))
			out = append(out, m)
		}
	}
	newT := types.MakeUnion(out...)
	if len(out) == 0 {
		newT = arrayWithEntry(types.Shape{}, keyT, vt)
	}
	x.assignTo(t.Array, newT, env, span)
	if key, ok := pathKey(t); ok {
		env.setPath(key, vt)
	}
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
			if e.IsInt && e.IntKey >= next {
				next = e.IntKey + 1
			}
		}
		if s.Unsealed {
			return types.Array{Key: types.TArrayKey, Value: types.MakeUnion(shapeValues(s), vt), NonEmpty: true}
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
	if len(s.Entries) == 0 && !s.Unsealed {
		// `$r = []; $r[$k] = $v`: the first entry sets the key and value types.
		return types.Array{Key: key, Value: vt, NonEmpty: true}
	}
	return types.Array{Key: types.MakeUnion(shapeKeys(s), key), Value: types.MakeUnion(shapeValues(s), vt), NonEmpty: true}
}

// destructure handles [$a, 'k' => $b] = value.
func (x *fnCtx) destructure(t *ast.ArrayLit, vt types.Type, env *state, span ast.Span) {
	if t.Kind == ast.ArrayList {
		x.removed(t.S, "list()", "[...] destructuring")
	}
	pos := int64(0)
	for _, item := range t.Items {
		if item == nil {
			pos++
			continue
		}
		var keyT types.Type
		if item.Key != nil {
			keyT = normaliseKey(x.expr(item.Key, env, nil))
		} else {
			keyT = types.IntLit(pos)
			pos++
		}
		var elem types.Type
		if types.IsMixed(vt) {
			elem = types.TMixed
		} else {
			var out []types.Type
			for _, m := range types.Members(vt) {
				switch m := m.(type) {
				case types.Shape:
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

// ------------------------------------------------------------------ removed constructs

// removed reports a construct Phaenon does not have (P1), in Phaenon files only.
func (x *fnCtx) removed(span ast.Span, what, instead string) {
	if x.file.Mode == lexer.ModePhaenon {
		x.report(span, P1001, "%s is not available in Phaenon; use %s", what, instead)
	}
}
