package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/types"
)

// The rules of design/concurrency.md §5 and §7: a `concurrently` block is
// checked as the closure it compiles to, with the enclosing variables in
// scope and what the body assigns flowing back out; `spawn` evaluates to
// Amp\Future<T> and is legal only inside a block.

// FutureClass is the handle type of a task.
const FutureClass = "Amp\\Future"

// CancellationClass is amp's cancellation token interface.
const CancellationClass = "Amp\\Cancellation"

// concurrently checks a block. valueUsed says whether the block is an
// expression whose value is read; a `return expr;` in a block used as a
// statement is P2603 (decision C7).
func (x *fnCtx) concurrently(e *ast.Concurrently, env *state, valueUsed bool) types.Type {
	x.concurrentlyOptions(e.Opts, env)
	c := x.child(false)
	c.name = "concurrently block"
	c.inBlock = true
	c.blockBody = e
	c.blockValueUsed = valueUsed
	c.blockEntry = env
	c.generator = bodyHasYield(e.Body) // reported once, as P2603, by noCrossing
	cenv := env.clone()
	cenv.dead = false
	c.stmts(e.Body, cenv)
	x.noCrossing(e.Body)
	// What the body assigned is what the enclosing scope sees afterwards: the
	// lowered closure captures those variables by reference.
	for name, info := range cenv.vars {
		outer, ok := env.get(name)
		switch {
		case !ok:
			env.set(name, info.t)
		case !types.Equal(outer.t, info.t):
			env.set(name, types.MakeUnion(outer.t, info.t))
		}
	}
	var t types.Type = types.TVoid
	if len(c.returnTypes) > 0 {
		t = types.MakeUnion(c.returnTypes...)
	}
	return t
}

// spawn checks `spawn expr` or `spawn { … }` and types it Amp\Future<T>.
func (x *fnCtx) spawn(e *ast.Spawn, env *state) types.Type {
	if !x.inBlock {
		x.report(e.Kw, P2601, "spawn outside of a concurrently block")
	}
	c := x.child(false)
	c.inBlock = true
	c.name = "spawn"
	cenv := env.clone()
	cenv.dead = false
	var t types.Type
	if e.Expr != nil {
		c.arrow = true
		t = c.expr(e.Expr, cenv, nil)
	} else {
		c.generator = bodyHasYield(e.Body)
		c.stmts(e.Body, cenv)
		x.noCrossing(e.Body)
		// The body is a by-value closure in another fiber: a write that
		// means to change a variable of the enclosing scope changes a copy
		// (P2604). A plain `$x = …` to a name that exists outside is a local
		// of the task, as it would be in any closure.
		for _, v := range mutatedVariables(e.Body) {
			if _, outer := env.get(v.Name); outer {
				x.report(v.S, P2604, "$%s is a variable of the enclosing scope: a spawn body writes to a copy of it — return the value instead", v.Name)
			}
		}
		t = types.TVoid
		if len(c.returnTypes) > 0 {
			t = types.MakeUnion(c.returnTypes...)
		}
	}
	if x.p().Class(FutureClass) == nil {
		x.report(e.Kw, P2002, "%s is not declared", FutureClass)
		return types.TError
	}
	return types.Class{Name: FutureClass, Args: []types.Type{t}}
}

// concurrentlyOptions checks the named options of a block: `timeout:` an
// int|float in seconds, `cancellation:` an Amp\Cancellation (§4.1).
func (x *fnCtx) concurrentlyOptions(opts *ast.ArgList, env *state) {
	if opts == nil {
		return
	}
	for _, a := range opts.Args {
		if a.Name == nil || a.Unpack || a.Placeholder != nil || a.Value == nil {
			x.report(a.S, P2606, "concurrently takes named options only: timeout and cancellation")
			if a.Value != nil {
				x.expr(a.Value, env, nil)
			}
			continue
		}
		switch strings.ToLower(a.Name.Name) {
		case "timeout":
			t := x.expr(a.Value, env, nil)
			if !types.IsAcceptable(t, types.MakeUnion(types.TInt, types.TFloat), x.p()) {
				x.report(a.Value.Span(), P2606, "timeout: expects seconds as int|float, got %s", x.typeName(t))
			}
		case "cancellation":
			t := x.expr(a.Value, env, nil)
			if !types.IsAcceptable(t, types.NewClass(CancellationClass), x.p()) {
				x.report(a.Value.Span(), P2606, "cancellation: expects %s, got %s", CancellationClass, x.typeName(t))
			}
		default:
			x.report(a.Name.S, P2606, "unknown concurrently option %s: timeout and cancellation exist", a.Name.Name)
			x.expr(a.Value, env, nil)
		}
	}
}

// noCrossing reports a yield or a goto in a body that compiles to a
// closure: the generator would be the closure's, and a label cannot be
// reached across a function boundary (P2603).
func (x *fnCtx) noCrossing(body []ast.Stmt) {
	labels := map[string]bool{}
	for _, s := range body {
		ast.Inspect(s, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Label:
				labels[n.Name.Name] = true
			case *ast.Closure, *ast.ArrowFunc, *ast.ClassDecl, *ast.FunctionDecl, *ast.Spawn, *ast.Concurrently:
				return false
			}
			return true
		})
	}
	for _, s := range body {
		ast.Inspect(s, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Yield, *ast.YieldFrom:
				x.report(n.Span(), P2603, "yield inside a concurrently or spawn block: the block is a closure in the output, the generator would be its own")
				return false
			case *ast.Goto:
				if !labels[n.Label.Name] {
					x.report(n.S, P2603, "goto out of a concurrently or spawn block")
				}
			case *ast.Closure, *ast.ArrowFunc, *ast.ClassDecl, *ast.FunctionDecl, *ast.Spawn, *ast.Concurrently:
				return false
			}
			return true
		})
	}
}

// blockExit reports a break or continue that would leave a concurrently
// block: the block is a closure in the output, there is no loop to reach.
func (x *fnCtx) blockExit(span ast.Span, levels ast.Expr, what string) {
	if x.blockBody == nil {
		return
	}
	if jumpLevels(levels) > x.loopDepth {
		x.report(span, P2603, "%s leaves the concurrently block: the block is a closure in the output; return from it instead", what)
	}
}

// blockReturn applies the block rules to a `return expr;` in a block body:
// a value nobody reads (P2603) and a task handle leaving its block (P2602).
func (x *fnCtx) blockReturn(s *ast.Return, t types.Type) {
	if x.blockBody == nil || s.Expr == nil {
		return
	}
	if !x.blockValueUsed {
		x.report(s.S, P2603, "return with a value in a concurrently block used as a statement: this return leaves the block, not the function, and nothing reads the value")
	}
	if mentionsClass(t, FutureClass) {
		x.report(s.Expr.Span(), P2602, "a task handle leaves its concurrently block: await it inside, or return the result")
	}
}

// spawnEscapes reports a `spawn` result stored where it outlives its block:
// a property, a static, or a variable that exists outside the block, which
// the lowering would capture by reference (P2602).
func (x *fnCtx) spawnEscapes(e *ast.Assign, env *state) {
	if _, ok := e.Value.(*ast.Spawn); !ok {
		return
	}
	switch t := e.Target.(type) {
	case *ast.PropertyFetch, *ast.StaticPropertyFetch:
		x.report(e.S, P2602, "a task handle is stored outside its concurrently block")
	case *ast.Variable:
		if x.blockEntry != nil {
			if _, outer := x.blockEntry.get(t.Name); outer {
				x.report(e.S, P2602, "$%s exists outside the concurrently block, so the task handle would outlive it: use a new variable", t.Name)
			}
		}
	}
}

// blockingSleeps are the functions that park the whole process, loop and
// tasks included (P2605); Scope::sleep() yields instead.
var blockingSleeps = map[string]bool{"sleep": true, "usleep": true, "time_nanosleep": true, "time_sleep_until": true}

// mentionsClass reports whether t holds an instance of the named class
// anywhere in a union.
func mentionsClass(t types.Type, class string) bool {
	for _, m := range types.Members(t) {
		if c, ok := m.(types.Class); ok && strings.EqualFold(c.Name, class) {
			return true
		}
	}
	return false
}

// mutatedVariables lists the variables a body changes in place, in source
// order: `++`/`--`, compound assignments (`+=`, `??=`, …), element writes
// (`$x[] = …`, `$x['k'] = …`), and a plain `$x = …` whose right-hand side
// reads $x. A plain assignment of a fresh value is a rebinding, not a
// mutation. Nested functions and classes are their own scopes; an
// anonymous class's constructor arguments are the body's (ast.InspectScope).
func mutatedVariables(body []ast.Stmt) []*ast.Variable {
	var out []*ast.Variable
	var root func(e ast.Expr) *ast.Variable
	root = func(e ast.Expr) *ast.Variable {
		switch e := e.(type) {
		case *ast.Variable:
			return e
		case *ast.Index:
			return root(e.Array)
		}
		return nil
	}
	for _, s := range body {
		ast.InspectScope(s, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Assign:
				v := root(n.Target)
				if v == nil {
					return true
				}
				_, element := n.Target.(*ast.Index)
				if element || n.Op != '=' || readsVariable(n.Value, v.Name) {
					out = append(out, v)
				}
			case *ast.IncDec:
				if v := root(n.Expr); v != nil {
					out = append(out, v)
				}
			case *ast.Closure, *ast.ArrowFunc, *ast.FunctionDecl, *ast.Spawn:
				return false
			}
			return true
		})
	}
	return out
}

// readsVariable reports whether e mentions $name (nested closures' bodies
// and classes aside, an anonymous class's constructor arguments included).
func readsVariable(e ast.Expr, name string) bool {
	found := false
	ast.InspectScope(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Variable:
			if n.Name == name {
				found = true
			}
		case *ast.Closure:
			return false
		}
		return !found
	})
	return found
}
