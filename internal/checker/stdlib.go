package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Method syntax on lists, maps, strings and bytes (design/collections.md
// §3.1, strings.md §4): `$xs->map($f)` is `\Phaenon\Seq::map($xs, $f)`. The
// receiver's static type picks the class; the checker records the choice
// on the file for the emitter, which rewrites the call as the static one.

// stdlibKind is the word a diagnostic uses for each stdlib class's receiver.
var stdlibKind = map[string]string{
	"Phaenon\\Seq": "list", "Phaenon\\Map": "map", "Phaenon\\Str": "string", "Phaenon\\Bytes": "bytes",
}

// stdlibReceiver names the stdlib class behind a receiver type, or reports
// a receiver that mixes kinds (a list with an object, a string with a map),
// which no one class can serve. A \Stringable class counts as a string
// when the receiver is also a string (`string|Id`); on its own it is an
// object whose own methods come first (methodCallOn falls back to Str).
func (x *fnCtx) stdlibReceiver(t types.Type) (class string, ambiguous bool) {
	kinds := make([]string, 0, 2)
	hasText := false
	for _, m := range types.Members(t) {
		k := ""
		switch m := m.(type) {
		case types.Array:
			k = "Phaenon\\Map"
			if m.List {
				k = "Phaenon\\Seq"
			}
		case types.Shape:
			k = "Phaenon\\Map"
			if m.List {
				k = "Phaenon\\Seq"
			}
		case types.String, types.StringLit, types.ClassString:
			k = "Phaenon\\Str"
			hasText = true
		case types.Bytes:
			k = "Phaenon\\Bytes"
		case types.Class:
			k = "-"
			if x.isStringable(m.Name) {
				k = "?Str"
			}
		default:
			k = "-" // objects, scalars, callables: not the stdlib's
		}
		kinds = append(kinds, k)
	}
	kind := ""
	for _, k := range kinds {
		if k == "?Str" {
			k = "-"
			if hasText {
				k = "Phaenon\\Str"
			}
		}
		if kind == "" {
			kind = k
		} else if kind != k {
			return "", true
		}
	}
	if kind == "-" {
		return "", false
	}
	return kind, false
}

// isStringable reports a class whose values convert to string.
func (x *fnCtx) isStringable(name string) bool {
	cl := x.p().Class(name)
	return cl != nil && (cl.IsSubclassOf("Stringable") || cl.FindMethod("__toString") != nil)
}

// stringableFallback reports a receiver made of \Stringable classes none
// of which declares (or magically handles) the method, while Str has it:
// `$id->startsWith('u-')` on a value object (design/strings.md §6).
func (x *fnCtx) stringableFallback(base types.Type, name string) bool {
	seen := false
	for _, m := range types.Members(base) {
		c, ok := m.(types.Class)
		if !ok {
			return false
		}
		cl := x.p().Class(c.Name)
		if cl == nil || !x.isStringable(c.Name) {
			return false
		}
		cl.Signature()
		if cl.FindMethod(name) != nil || cl.FindMethod("__call") != nil {
			return false
		}
		seen = true
	}
	if !seen {
		return false
	}
	str := x.p().Class("Phaenon\\Str")
	if str == nil {
		return false
	}
	str.Signature()
	m := str.FindMethod(name)
	return m != nil && m.Static
}

// purePath reports an expression the emitter may evaluate twice to guard
// a call against null: variables, property and constant paths, indexing by
// a literal or a variable. A call, a cast or an operator is not one.
func purePath(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Variable:
		return true
	case *ast.ConstFetch:
		return true
	case *ast.PropertyFetch:
		_, ok := e.Name.(*ast.Ident)
		return ok && purePath(e.Object)
	case *ast.StaticPropertyFetch:
		_, c := e.Class.(*ast.Name)
		_, v := e.Name.(*ast.Variable)
		return c && v
	case *ast.ClassConstFetch:
		_, c := e.Class.(*ast.Name)
		_, n := e.Name.(*ast.Ident)
		return c && n
	case *ast.Index:
		switch e.Index.(type) {
		case *ast.IntLit, *ast.StringLit, *ast.Variable, *ast.ConstFetch, *ast.ClassConstFetch:
			return purePath(e.Array)
		}
		return false
	}
	return false
}

// stdlibMethod checks `$recv->name(args)` as the static call
// Class::name($recv, args) and records the target for the emitter.
func (x *fnCtx) stdlibMethod(e *ast.MethodCall, class, name string, obj types.Type, env *state, expected types.Type) types.Type {
	if class == "" {
		x.report(e.Object.Span(), P2519, "cannot call %s() on %s: a list, a map and a string have different methods; narrow the value first", name, x.typeName(types.StripNull(obj)))
		x.argsOnly(e.Args, env)
		return types.TError
	}
	x.c.stdlibUsed[class] = true
	short := class[strings.LastIndex(class, "\\")+1:]
	kind := stdlibKind[class]
	// A null receiver (`?->`, or a chain `$a?->b` already short-circuited)
	// makes the emitter guard the call: `null === $x ? null : Str::f($x)`.
	// The receiver is then written twice, so it has to be a path with no
	// side effects (collections.md §3.1).
	guard := e.NullSafe || x.shortCircuit[e.Object]
	if guard && !purePath(e.Object) {
		x.report(e.S, P2519, "%s() on a %s that may be null: the compiler cannot evaluate this receiver only once; assign it to a variable first", name, kind)
		x.argsOnly(e.Args, env)
		return types.TError
	}
	cl := x.p().Class(class)
	if cl == nil {
		x.report(e.Name.Span(), P2001, "unknown class %s", class)
		x.argsOnly(e.Args, env)
		return types.TMixed
	}
	cl.Signature()
	m := cl.FindMethod(name)
	if m == nil || !m.Static {
		x.report(e.Name.Span(), P2006, "unknown method %s() on %s (%s has none)", name, x.typeName(types.StripNull(obj)), short)
		x.argsOnly(e.Args, env)
		return types.TError
	}
	if e.Args == nil || e.Args.FirstClassCallable {
		x.report(e.S, P2519, "%s() on a %s receiver must be called; a first-class callable is %s::%s(...)", name, kind, short, name)
		return types.TError
	}
	args := &ast.ArgList{S: e.Args.S, Args: append([]*ast.Arg{{S: e.Object.Span(), Value: e.Object}}, e.Args.Args...)}
	sig := &signature{name: short + "::" + m.Name + "()", params: m.Params, ret: m.Return, typeParams: m.TypeParams, decl: m.Class,
		known: map[ast.Expr]types.Type{e.Object: types.StripNull(obj)}}
	if m.Deprecated && x.c.opts.Deprecations {
		x.report(e.Name.Span(), P4006, "method %s::%s() is deprecated", short, name)
	}
	if x.c.file != nil {
		if x.c.file.MethodTargets == nil {
			x.c.file.MethodTargets = map[*ast.MethodCall]program.MethodTarget{}
		}
		x.c.file.MethodTargets[e] = program.MethodTarget{Class: class, Guard: guard}
	}
	t := x.checkCall(sig, args, env, e.S, expected)
	if e.NullSafe {
		x.markShortCircuit(e, t) // the result's own type, before ?-> adds its null
		if types.IsNullable(obj) {
			t = types.Nullable(t)
		}
	}
	return t
}
