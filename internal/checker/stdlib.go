package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Method syntax on lists, maps, strings and bytes (design/collections.md
// §3.1, strings.md §4): `$xs->map($f)` is `\Phaenon\Seq::map($xs, $f)`. The
// receiver's static type picks the class; the checker records the choice
// on the file for the emitter, which rewrites the call as the static one.

// stdlibReceiver names the stdlib class behind a receiver type, or reports
// a receiver that mixes kinds (a list with an object, a string with a map),
// which no one class can serve.
func stdlibReceiver(t types.Type) (class string, ambiguous bool) {
	kind := ""
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
		case types.Bytes:
			k = "Phaenon\\Bytes"
		default:
			k = "-" // objects, scalars, callables: not the stdlib's
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

// stdlibMethod checks `$recv->name(args)` as the static call
// Class::name($recv, args) and records the target for the emitter.
func (x *fnCtx) stdlibMethod(e *ast.MethodCall, class, name string, obj types.Type, env *state, expected types.Type) types.Type {
	if class == "" {
		x.report(e.Object.Span(), P2519, "cannot call %s() on %s: a list, a map and a string have different methods; narrow the value first", name, x.typeName(types.StripNull(obj)))
		x.argsOnly(e.Args, env)
		return types.TError
	}
	short := class[strings.LastIndex(class, "\\")+1:]
	if e.NullSafe {
		x.report(e.S, P2519, "?-> is not available on a %s receiver; test for null first", strings.ToLower(short))
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
		x.report(e.S, P2519, "%s() on a %s receiver must be called; a first-class callable is %s::%s(...)", name, strings.ToLower(short), short, name)
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
			x.c.file.MethodTargets = map[*ast.MethodCall]string{}
		}
		x.c.file.MethodTargets[e] = class
	}
	return x.checkCall(sig, args, env, e.S, expected)
}
