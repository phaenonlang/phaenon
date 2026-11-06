package checker

import (
	"math"
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// superglobals are always defined arrays. $argv and $argc are among them
// for a script's top level; see localArgs.
var superglobals = map[string]bool{
	"GLOBALS": true, "_SERVER": true, "_GET": true, "_POST": true, "_FILES": true,
	"_COOKIE": true, "_SESSION": true, "_REQUEST": true, "_ENV": true, "argv": true, "argc": true,
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
func (x *fnCtx) expr(e ast.Expr, env *state, expected types.Type) types.Type {
	t := x.exprType(e, env, expected)
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
	case *ast.Concurrently:
		// It rethrows the first failure of its tasks.
		defer x.implicitThrow()
		return x.concurrently(e, env, true)
	case *ast.Spawn:
		return x.spawn(e, env)
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
			defer x.explicitThrow(types.NewClass("TypeError"), false, e.S) // `as?` gives null instead
		}
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
		if key, ok := pathKey(e); ok {
			if t, ok := env.getPath(key); ok {
				return t
			}
		}
		if t, ok := x.narrowedByLiteralKeys(e, env); ok {
			return t
		}
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
	if superglobals[v.Name] && !localArgs(v.Name, env) {
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
			x.report(span, P2530, "bytes is not text: \\Phaenon\\Bytes::text() decodes it as UTF-8, Bytes::hex() or Bytes::base64() spells it out")
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
			if !ok || mayNotIterate(t) {
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
			kt, stored := x.keyExpr(item.Key, env)
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
			ev = x.expr(item.Value, env, expectedValue)
			if len(e.Items) == 2 && item == e.Items[0] {
				x.noteRecv(e, ev) // [$this, 'name'] may name a private method
			}
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

// normaliseKey is the key PHP stores for a value of type t, member by
// member: bool and float keys become ints, null the empty string, and a
// string known to be a canonical decimal integer the int it spells
// (types.StoredKey; design/checker.md §9). A general string stays a string.
func normaliseKey(t types.Type) types.Type {
	if types.IsMixed(t) {
		return types.TArrayKey // PHP casts or refuses; the array gets int|string keys
	}
	return types.Map(t, func(m types.Type) types.Type {
		switch m.(type) {
		case types.BoolLit, types.Bool:
			return types.TInt
		case types.Null:
			return types.StringLit{V: ""}
		case types.Float, types.FloatLit:
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
		out = append(out, x.bindReceiver(x.p().ConstantType(k), recv, k.Class))
	}
	return types.MakeUnion(out...)
}

// ------------------------------------------------------------------ members

func (x *fnCtx) propertyFetch(e *ast.PropertyFetch, env *state, lenient bool) types.Type {
	x.prematureRead(e, env)
	if key, ok := pathKey(e); ok {
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
		out = append(out, x.expr(cs.Value, newEnv(), nil))
	}
	if len(out) == 0 {
		return nil, false
	}
	return types.MakeUnion(out...), true
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
	} else if types.IsNullable(obj) && !types.IsMixed(obj) && !lenient {
		if _, isNull := obj.(types.Null); isNull {
			x.report(e.Object.Span(), P2105, "cannot access property $%s on null", name)
			return types.TNull
		}
		x.report(e.Object.Span(), P2105, "cannot access property $%s on %s (possibly null)", name, x.typeName(obj))
	}
	base := types.StripNull(obj)
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
		if key, ok := pathKey(e); ok {
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
	base, ok := pathKey(e.Array)
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
	if key, ok := pathKey(e); ok {
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
	return x.indexType(base, e, env, false)
}

// indexType types base[index].
func (x *fnCtx) indexType(base types.Type, e *ast.Index, env *state, lenient bool) types.Type {
	if mayBeObject(base) {
		x.implicitThrow() // an ArrayAccess's offsetGet() or offsetExists() runs
	}
	if e.Index == nil {
		return types.TMixed
	}
	raw, idx := x.keyExpr(e.Index, env)
	if types.IsMixed(base) {
		if _, unknown := base.(types.Unknown); unknown {
			return base // an offset of what was already reported stays reported
		}
		x.reportNoOffsets(e, base, raw, env)
		return types.TMixed
	}
	var out []types.Type
	for _, m := range types.Members(base) {
		switch m := m.(type) {
		case types.Shape:
			t, found, certain := shapeOffset(m, idx)
			if !found {
				if !lenient && !m.Unsealed && certain {
					x.report(e.Index.Span(), P2313, "offset %s does not exist on %s", x.typeName(raw), x.typeName(m))
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
				x.report(e.Index.Span(), P2313, "offset %s does not exist on %s", x.typeName(raw), x.typeName(m))
			}
			out = append(out, m.Value)
		case types.String, types.StringLit:
			out = append(out, types.TString)
		case types.Bytes:
			// One byte, still binary: `ord($b[$i])` reads it (strings.md §2).
			out = append(out, types.TBytes)
		case types.Null:
			if !lenient {
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
			if n.Name == "this" || superglobals[n.Name] {
				return false
			}
			_, defined := env.get(n.Name)
			return !defined
		default:
			return false
		}
	}
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
	case token.T_BYTES_CAST:
		// (bytes) retypes a string as binary data, free at runtime; it is
		// how a PHP API's binary-in-a-string enters the typed world and
		// how the stdlib builds a bytes value (design/strings.md §2).
		for _, m := range types.Members(t) {
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
	if strings.Contains(strings.ToLower(e.Raw), "integer") || strings.Contains(strings.ToLower(e.Raw), "boolean") ||
		strings.Contains(strings.ToLower(e.Raw), "double") || strings.Contains(strings.ToLower(e.Raw), "real") || strings.Contains(strings.ToLower(e.Raw), "binary") {
		x.removed(e.S, e.Raw+" cast", "the canonical cast")
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
	x.checkStringable(types.Subtract(t, types.TBytes, x.p()), e.Expr.Span())
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
		x.pipeLeft++
		left := x.expr(e.Left, env, nil)
		x.pipeLeft--
		return x.pipe(e, left, env)
	}
	l := x.expr(e.Left, env, nil)
	r := x.expr(e.Right, env, nil)
	x.certainOperands = types.Equal(x.nativeOr(e.Left, l, env), l) && types.Equal(x.nativeOr(e.Right, r, env), r)
	if e.Op == token.T_IS_IDENTICAL || e.Op == token.T_IS_NOT_IDENTICAL {
		if !types.IsMixed(l) && !types.IsMixed(r) && !types.Overlaps(l, r, x.p()) && (!x.nativeOverlap(e.Left, l, e.Right, r, env) || bytesAgainstText(l, r)) {
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

// concatBytes reports an operand of `.` that is binary data: bytes, maybe
// null (which `.` reads as the empty string), or a value already reported.
func concatBytes(t types.Type) bool {
	found := false
	for _, m := range types.Members(t) {
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
	switch op {
	case token.Kind('.'):
		// `.` joins two values of the same kind (strings.md §2): binary
		// joined to binary stays binary, as `.=` on a bytes buffer does;
		// binary mixed with text is the confusion bytes exists to catch,
		// reported once, its result already reported. An operand already
		// reported (`$s->bytes()` on a string) joins the other side's kind.
		lb, rb := concatBytes(l), concatBytes(r)
		if (lb || types.Equal(l, types.TError)) && (rb || types.Equal(r, types.TError)) && (lb || rb) {
			return types.TBytes
		}
		if hasBytes(l) || hasBytes(r) {
			x.report(span, P2530, "cannot concatenate bytes and text: \\Phaenon\\Bytes::text() decodes bytes as UTF-8, (bytes) takes text as its UTF-8 bytes, (bytes) '' starts a binary buffer")
			x.checkStringable(types.Subtract(l, types.TBytes, x.p()), span)
			x.checkStringable(types.Subtract(r, types.TBytes, x.p()), span)
			return types.TError
		}
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
				return arrayPlus(l, r)
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
		x.spawnEscapes(e, env)
		expected := x.targetType(e.Target, env)
		vt := x.expr(e.Value, env, expected)
		if lit, ok := e.Target.(*ast.ArrayLit); ok {
			x.destructure(lit, vt, env, e.S, e.Value)
			return vt
		}
		x.assignTo(e.Target, vt, env, e.S)
		if v, ok := e.Target.(*ast.Variable); ok {
			env.setNative(v.Name, x.nativeOf(e.Value, env))
			x.rememberCondition(v.Name, e.Value, env)
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
	if idx, ok := e.Target.(*ast.Index); ok && idx.Index != nil && !types.IsNullable(cur) {
		// `$parts[0] .= $chunk` reads the offset before it writes it: the
		// write replaces an existing entry, so a list stays a list (PHPStan's
		// setExistingOffsetValueType, as for the key of a foreach).
		if key, ok := pathKey(idx); ok {
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
		x.destructure(t, vt, env, span, nil)
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
			x.assignDeclared(t, pr, name, base, vt, span)
		}
	}
	if v, ok := t.Object.(*ast.Variable); ok && v.Name == "this" && x.trackInits {
		env.initialise(name)
	}
	if key, ok := pathKey(t); ok {
		env.invalidatePrefix(key)
		env.setPath(key, vt)
	}
}

// assignDeclared checks an assignment of vt to the declared property pr.
func (x *fnCtx) assignDeclared(t *ast.PropertyFetch, pr *program.Property, name string, base, vt types.Type, span ast.Span) {
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
		x.report(span, P2201, "property %s::$%s (%s) does not accept %s", pr.Class.Name, name, x.typeName(declared), x.typeName(vt))
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
		if key, ok := pathKey(t); ok {
			if cur, found := env.getPath(key); found && !types.IsNullable(cur) {
				existing = true
			}
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
			list := m.List && (keyT == nil)
			switch {
			case existing:
				list = m.List
			case keyT != nil:
				k = types.MakeUnion(k, keyT)
			case !m.List:
				k = types.MakeUnion(k, types.TInt)
			}
			out = append(out, types.Array{Key: widenKeys(k), Value: types.MakeUnion(m.Value, vt), List: list, NonEmpty: true})
		case types.Mixed:
			x.reportNoOffsets(t, m, keyRaw, env)
			out = append(out, m)
		case types.Unknown:
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
			_, keyT = x.keyExpr(item.Key, env)
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
			if key, ok := pathKey(&ast.Index{Array: src, Index: idx}); ok {
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

// removed reports a construct Phaenon does not have (P1), in Phaenon files only.
func (x *fnCtx) removed(span ast.Span, what, instead string) {
	if x.file.Mode == lexer.ModePhaenon {
		x.report(span, P1001, "%s is not available in Phaenon; use %s", what, instead)
	}
}

// arrayPlus types `$a + $b` on arrays: the left operand's entries, then the
// right's for the keys the left lacks. Two sealed shapes merge entry by
// entry; anything else is the union of keys and of values.
func arrayPlus(l, r types.Type) types.Type {
	var out []types.Type
	for _, lm := range types.Members(l) {
		for _, rm := range types.Members(r) {
			ls, lok := lm.(types.Shape)
			rs, rok := rm.(types.Shape)
			if lok && rok && !ls.Unsealed && !rs.Unsealed {
				merged := types.Shape{Entries: append([]types.ShapeEntry{}, ls.Entries...)}
				for _, e := range rs.Entries {
					if !hasShapeEntry(merged, e) {
						merged.Entries = append(merged.Entries, e)
					}
				}
				out = append(out, merged)
				continue
			}
			lk, lv, lne := arrayKV(lm)
			rk, rv, rne := arrayKV(rm)
			out = append(out, types.Array{Key: types.MakeUnion(lk, rk), Value: types.MakeUnion(lv, rv), NonEmpty: lne || rne})
		}
	}
	return types.MakeUnion(out...)
}

func hasShapeEntry(s types.Shape, e types.ShapeEntry) bool {
	for _, have := range s.Entries {
		if have.IsInt == e.IsInt && have.IntKey == e.IntKey && have.Key == e.Key {
			return true
		}
	}
	return false
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
	switch v := value.(type) {
	case *ast.Ternary:
		ternary, condExpr = v, v.Cond
	case *ast.Binary:
		switch v.Op {
		case token.T_BOOLEAN_AND, token.T_LOGICAL_AND, token.T_BOOLEAN_OR, token.T_LOGICAL_OR,
			token.T_IS_IDENTICAL, token.T_IS_NOT_IDENTICAL, token.T_IS_EQUAL, token.T_IS_NOT_EQUAL:
			condExpr = v
		}
	case *ast.Unary:
		if v.Op == token.Kind('!') {
			condExpr = v
		}
	case *ast.Instanceof, *ast.IsExpr, *ast.Isset, *ast.Empty:
		condExpr = value
	case *ast.Call:
		if n, ok := v.Callee.(*ast.Name); ok && predicateTarget(strings.ToLower(n.Parts[len(n.Parts)-1])) != nil {
			condExpr = value
		}
	}
	if condExpr == nil {
		return
	}
	before := env.clone()
	saved := x.c.mark()
	then, els := x.cond(condExpr, env.clone())
	if ternary != nil {
		if ternary.Then == nil {
			whenThen = types.Truthy(x.condValueType)
		} else {
			whenThen = x.expr(ternary.Then, then, nil)
		}
		whenElse = x.expr(ternary.Else, els, nil)
	} else {
		whenThen, whenElse = types.TTrue, types.TFalse
	}
	x.c.rollback(saved)
	// Only values that tell the branches apart carry information: PHPStan
	// records nothing for `$b instanceof X ? $b->s : null` when $b->s is
	// mixed, since null is one of its values.
	if _, never := types.Intersect(whenThen, whenElse, x.p()).(types.Never); !never {
		return
	}
	delete(then.vars, name)
	delete(els.vars, name)
	env.setConds(name, condBranch(whenThen, then, before), condBranch(whenElse, els, before))
}
