package checker

import (
	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
)

// pipeCall is the call PHP turns a pipe into when its right side is a
// first-class callable or a partial application with one placeholder
// (zend_partial_apply): `A |> f(B, ?, C)` is f(B, A, C), `A |> f(...)` is
// f(A). It returns the call and the right side's own argument list, or nil
// when PHP evaluates the right side and calls the result: several
// placeholders, or `...` after a named argument.
func pipeCall(e *ast.Binary) (ast.Expr, *ast.ArgList) {
	var args *ast.ArgList
	switch r := e.Right.(type) {
	case *ast.Call:
		args = r.Args
	case *ast.MethodCall:
		if r.NullSafe {
			return nil, nil
		}
		args = r.Args
	case *ast.StaticCall:
		args = r.Args
	}
	if args == nil {
		return nil, nil
	}
	var list []*ast.Arg
	switch {
	case args.FirstClassCallable:
		list = []*ast.Arg{{S: e.Left.Span(), Value: e.Left}}
	case args.Partial():
		placeholders, named := 0, false
		for _, a := range args.Args {
			if a.Placeholder != nil {
				placeholders++
			}
			named = named || a.Name != nil
		}
		if placeholders != 1 {
			return nil, nil
		}
		for _, a := range args.Args {
			switch {
			case a.Unpack:
				return nil, nil
			case a.Placeholder == nil:
				list = append(list, a)
			case a.Placeholder.Variadic && named:
				return nil, nil
			default:
				list = append(list, &ast.Arg{S: e.Left.Span(), Name: a.Name, Value: e.Left})
			}
		}
	default:
		return nil, nil
	}
	newArgs := &ast.ArgList{S: args.S, Args: list}
	switch r := e.Right.(type) {
	case *ast.Call:
		c := *r
		c.Args = newArgs
		return &c, args
	case *ast.MethodCall:
		c := *r
		c.Args = newArgs
		return &c, args
	case *ast.StaticCall:
		c := *r
		c.Args = newArgs
		return &c, args
	}
	return nil, nil
}

// recordPipePartial records `A |> f(B, ?, C)` for the emitter: lowered for
// 8.5 to f(B, A, C) when B and C are literals or constants, and otherwise to
// A |> (fn (T $p) => f(B, $p, C)), whose closure runs at once, so that B
// and C are evaluated after A as on 8.6 (§5.3). param is the parameter the
// left side was bound to.
func (x *fnCtx) recordPipePartial(right ast.Expr, args *ast.ArgList, sig *signature, param *program.Param) {
	var placeholder *ast.Arg
	for _, a := range args.Args {
		if a.Placeholder != nil {
			placeholder = a
		}
	}
	slot := pfaSlot{param: param, name: param.Name, required: true, arg: placeholder}
	x.recordPartial(right, args, sig, []pfaSlot{slot}, false, false, true)
}

// pipeDirect reports whether a pipe into a partial application lowers to
// the plain call, f(B, A, C): every bound argument is a literal or a
// constant, so evaluating them after the left side changes nothing.
func pipeDirect(args *ast.ArgList) bool {
	for _, a := range args.Args {
		if a.Placeholder == nil && a.Value != nil && !literalArg(a.Value) {
			return false
		}
	}
	return true
}
