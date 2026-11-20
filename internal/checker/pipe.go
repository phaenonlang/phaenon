package checker

import (
	"fmt"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/evalorder"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
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
// 8.5 to f(B, A, C) when the pipe folds (recordFold), and otherwise to
// A |> (fn (T $p) => f(B, $p, C)), whose closure runs at once, so that B
// and C are evaluated after A as on 8.6 (§5.3). param is the parameter the
// left side was bound to.
//
// The closure's parameter gets no inline @var: PHPStan types the parameter
// of a closure that is called at once from the value it is called with,
// here A, as it types A in the call f(B, A, C) the pipe is checked as. The
// parameter's type as that call resolved it says less than A where the
// result depends on A beyond it: array_map's `array<K, V> $array` resolved
// for a list<string> is an array<int, string>, of which array_map's result
// is no list. And PHPStan refuses a @var that is wider than what it knows
// of A natively, a string where strtoupper() gave an uppercase-string.
func (x *fnCtx) recordPipePartial(right ast.Expr, args *ast.ArgList, sig *signature, param *program.Param) {
	var placeholder *ast.Arg
	for _, a := range args.Args {
		if a.Placeholder != nil {
			placeholder = a
		}
	}
	slot := pfaSlot{param: param, name: param.Name, required: true, arg: placeholder}
	x.recordPartial(right, args, sig, []pfaSlot{slot}, false, false, true, func(*program.Param) types.Type { return nil })
}

// recordFold records whether PHP evaluates the pipe exactly as the call
// it makes (program.File.Folds): the left side goes by value, and the
// stage's callee and the arguments written before the placeholder compute
// the same whether they are evaluated before the left side, as in the
// call, or after it, as in the pipe (evalorder). The callee is a named
// function other than compact() and its kind, a static method of a named
// class, or a method of $this or of a variable the left side does not
// write; a variable callee the same.
func (x *fnCtx) recordFold(e *ast.Binary, args *ast.ArgList) {
	untyped := x.untypedStage(e, args)
	fold := x.pipeParam != nil && !x.pipeParam.ByRef && x.pipeSig != nil || untyped
	if fold {
		written := evalorder.Writes(e.Left, func(call ast.Expr, i int) bool {
			a := evalorder.Args(call)
			return a != nil && x.byValue[a.Args[i]]
		})
		fold = foldCallee(e.Right, written)
		for _, a := range args.Args {
			if !fold || a.Placeholder != nil {
				break
			}
			fold = a.Value != nil && !a.Unpack && evalorder.Movable(a.Value, written)
		}
	}
	if x.c.file.Folds == nil {
		x.c.file.Folds = map[*ast.Binary]bool{}
	}
	x.c.file.Folds[e] = fold
}

// untypedStage records a pipe's stage whose callee is a value the checker
// could not type, `A |> $cl(?)` with $cl a Closure without a signature,
// `callable` or a string, as the call PHP makes: its one placeholder a
// parameter of mixed, bound arguments where they stand, positional, and
// the closure written for PHP 8.5, where the stage does not fold, passing
// the piped value on by value. 8.6 throws where the parameter is by
// reference, which a pipe cannot pass, and the call passes it then: the
// one difference, for code 8.6 refuses. A named argument names a
// parameter nothing knows, and is not recorded.
func (x *fnCtx) untypedStage(e *ast.Binary, args *ast.ArgList) bool {
	call, ok := e.Right.(*ast.Call)
	if !ok || x.pipeSig != nil || !args.Partial() || x.c.file.Partials[args] != nil {
		return false
	}
	if _, value := call.Callee.(*ast.Variable); !value {
		return false
	}
	sig := &signature{name: "callable"}
	var slot pfaSlot
	for i, a := range args.Args {
		if a.Name != nil || a.Unpack || a.Placeholder != nil && a.Placeholder.Variadic {
			return false
		}
		p := &program.Param{Name: fmt.Sprintf("arg%d", i), Type: types.TMixed, NativeType: types.TMixed}
		if a.Placeholder != nil {
			p.Name = "piped"
			slot = pfaSlot{param: p, name: p.Name, required: true, arg: a}
		}
		sig.params = append(sig.params, p)
	}
	x.recordPartial(e.Right, args, sig, []pfaSlot{slot}, false, false, true, func(*program.Param) types.Type { return nil })
	return true
}

// foldCallee reports a stage's callee that is the same function before
// the left side is evaluated and after.
func foldCallee(right ast.Expr, written evalorder.Set) bool {
	switch c := right.(type) {
	case *ast.Call:
		switch callee := c.Callee.(type) {
		case *ast.Name:
			return !evalorder.ScopeCall(c)
		case *ast.Variable:
			return !written.Has(callee.Name)
		}
	case *ast.StaticCall:
		_, named := c.Class.(*ast.Name)
		_, ident := c.Name.(*ast.Ident)
		return named && ident
	case *ast.MethodCall:
		v, isVar := c.Object.(*ast.Variable)
		_, ident := c.Name.(*ast.Ident)
		return !c.NullSafe && isVar && ident && !written.Has(v.Name)
	}
	return false
}
