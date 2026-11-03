// Package evalorder says when an argument may be evaluated on the other
// side of an expression without changing what either computes.
//
// A pipe evaluates its left side before the stage's other arguments and
// before a method's receiver; the nested call it stands for evaluates the
// arguments written before the inner call first (design/php-versions.md
// §5.3). The pipes codemod writes a nested call as a pipe, and the 8.5
// lowering writes a pipe back as the nested call (design/tooling.md §8.1):
// both are exact only when nothing evaluated in between can change what
// the moved arguments compute. They ask the same two questions, answered
// here once: which variables evaluating an expression may write (Writes),
// and whether an argument reads none of them and has no effect of its own
// (Movable).
//
// The rule is syntactic and conservative. A variable is written by an
// assignment, an increment, a by-reference capture, and by being passed
// to a parameter the caller cannot show to be by value; a variable
// variable, include and eval may write any. Assignments inside a closure
// or an arrow function under the expression count as well. Properties are
// not variables: an argument that reads one is not movable.
package evalorder

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/token"
)

// Set holds the names of the variables an expression may write; Any
// stands for every variable.
type Set map[string]bool

// Any is the Set key for "any variable".
const Any = "*"

// Has reports whether evaluating the expression may write $name. $this is
// never written.
func (s Set) Has(name string) bool {
	return name != "this" && (s[name] || s[Any])
}

// ByValue reports whether argument i of call (a *ast.Call, *ast.MethodCall,
// *ast.StaticCall or *ast.New) is known to go to a parameter that is not by
// reference. A variable passed where it returns false counts as written.
type ByValue func(call ast.Expr, i int) bool

// Writes collects the variables evaluating e may write. byValue resolves
// the calls under e; nil resolves none. An anonymous class's methods write
// variables of their own, its constructor arguments are e's
// (ast.InspectScope).
func Writes(e ast.Expr, byValue ByValue) Set {
	out := Set{}
	if e == nil {
		return out
	}
	ast.InspectScope(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Assign:
			targetVars(n.Target, out)
		case *ast.IncDec:
			targetVars(n.Expr, out)
		case *ast.ClosureUse:
			if n.ByRef {
				out[n.Var.Name] = true
			}
		case *ast.VariableVariable, *ast.Include, *ast.Eval:
			out[Any] = true
		case *ast.New, *ast.Call, *ast.MethodCall, *ast.StaticCall:
			call := n.(ast.Expr)
			args := Args(call)
			if args == nil || args.FirstClassCallable {
				break
			}
			for i, a := range args.Args {
				v := rootVar(a.Value)
				if v == "" {
					continue
				}
				if !a.Unpack && byValue != nil && byValue(call, i) {
					continue
				}
				out[v] = true
			}
		}
		return true
	})
	return out
}

// Args is the argument list of a call or an instantiation, nil when it has
// none written.
func Args(e ast.Expr) *ast.ArgList {
	switch e := e.(type) {
	case *ast.Call:
		return e.Args
	case *ast.MethodCall:
		return e.Args
	case *ast.StaticCall:
		return e.Args
	case *ast.New:
		if e.Anon != nil {
			return e.Anon.Args
		}
		return e.Args
	}
	return nil
}

// targetVars adds the variables an assignment to t writes.
func targetVars(t ast.Expr, out Set) {
	switch t := t.(type) {
	case *ast.ArrayLit:
		for _, it := range t.Items {
			if it != nil {
				targetVars(it.Value, out)
			}
		}
	default:
		if v := rootVar(t); v != "" {
			out[v] = true
		} else if t != nil {
			if _, ok := t.(*ast.StaticPropertyFetch); !ok {
				out[Any] = true
			}
		}
	}
}

// rootVar is the variable an lvalue is rooted in: $a for $a, $a[1], $a->b.
func rootVar(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Variable:
		return e.Name
	case *ast.Index:
		return rootVar(e.Array)
	case *ast.PropertyFetch:
		return rootVar(e.Object)
	}
	return ""
}

// Constant reports a literal or a constant: nothing to evaluate.
func Constant(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.IntLit, *ast.FloatLit, *ast.StringLit, *ast.MagicConst, *ast.ConstFetch, *ast.Name:
		return true
	case *ast.ClassConstFetch:
		_, named := e.Class.(*ast.Name)
		_, ident := e.Name.(*ast.Ident)
		return named && ident
	case *ast.Unary:
		if e.Op == token.Kind('-') || e.Op == token.Kind('+') {
			switch e.Expr.(type) {
			case *ast.IntLit, *ast.FloatLit:
				return true
			}
		}
	case *ast.ArrayLit:
		if e.Kind == ast.ArrayList {
			return false
		}
		for _, it := range e.Items {
			if it == nil || it.ByRef || it.Unpack || it.Key != nil && !Constant(it.Key) || !Constant(it.Value) {
				return false
			}
		}
		return true
	}
	return false
}

// Closure reports an expression that creates a closure: a closure, an
// arrow function, a first-class callable or a partial application.
func Closure(e ast.Expr) bool {
	switch e.(type) {
	case *ast.Closure, *ast.ArrowFunc:
		return true
	}
	args := Args(e)
	if _, isNew := e.(*ast.New); isNew {
		return false
	}
	return args != nil && args.Closure()
}

// Movable reports an argument that computes the same before or after
// writes to the variables in written: a literal, a constant, a variable
// not among them, a closure that captures none of them by value, a
// first-class callable of a named function or static method.
func Movable(e ast.Expr, written Set) bool {
	if Constant(e) {
		return true
	}
	switch e := e.(type) {
	case *ast.Variable:
		return !written.Has(e.Name)
	case *ast.Closure:
		for _, u := range e.Uses {
			if !u.ByRef && written.Has(u.Var.Name) {
				return false
			}
		}
		return true
	case *ast.ArrowFunc:
		params := map[string]bool{}
		for _, p := range e.Params {
			if p.Var != nil {
				params[p.Var.Name] = true
			}
		}
		safe := true
		ast.InspectScope(e.Body, func(n ast.Node) bool {
			if v, ok := n.(*ast.Variable); ok && !params[v.Name] && written.Has(v.Name) {
				safe = false
			}
			return safe
		})
		return safe
	case *ast.Call:
		_, named := e.Callee.(*ast.Name)
		return named && e.Args.FirstClassCallable
	case *ast.StaticCall:
		_, named := e.Class.(*ast.Name)
		return named && e.Args.FirstClassCallable
	}
	return false
}

// scopeCalls read or write the calling scope, or are compiled away
// (assert() under zend.assertions=-1): through a pipe they run as a
// callable, which PHP refuses or evaluates differently.
var scopeCalls = map[string]bool{
	"compact": true, "extract": true, "func_get_args": true, "func_get_arg": true,
	"func_num_args": true, "get_defined_vars": true, "assert": true,
}

// ScopeCall reports a call of compact(), extract() and the other functions
// that cannot become a callable without changing what they do.
func ScopeCall(e ast.Expr) bool {
	c, ok := e.(*ast.Call)
	if !ok {
		return false
	}
	n, ok := c.Callee.(*ast.Name)
	return ok && scopeCalls[strings.ToLower(n.Parts[len(n.Parts)-1])]
}
