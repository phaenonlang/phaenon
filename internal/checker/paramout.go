package checker

import (
	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// The body of a function or a method is held to the @param-out types of
// its by-reference parameters (P2213), as PHPStan holds it
// (ParameterOutAssignedTypeRule and ParameterOutExecutionEndTypeRule, both
// paramOut.type). A caller reads the tag as what its variable holds once
// the call returns, so
//
//   - every write to such a parameter in the body leaves a value of the
//     tag's type: an assignment, compound or by reference, a write to an
//     offset and an unset() of one, a destructuring, a foreach, a step,
//     and an argument passed by reference to a callee that writes it, a
//     sort of php-src's included, which leaves the type the array had.
//     Reported at the write. A step or a compound assignment whose own
//     operation the checker refuses (P2301), as `$x--` on ?int, is that
//     finding alone;
//   - where the body ends with the parameter unwritten on some way there,
//     what it holds is of the tag's type: the value it was passed,
//     narrowed by the conditions on the way, or joined with what the
//     writes on the other ways left. Reported where the body ends. Where
//     it is written on every way, each write was held to the tag already,
//     and the end is not: one mistake, one finding.
//
// The places the body ends are PHPStan's (its ExecutionEndNode): the last
// statement, and, when that is an if, the last statement of each branch
// (an empty branch itself) and the if when it has no else and none of its
// conditions always holds, read from the types PHP declares (outsPastIf).
// A lone `;` is no statement there, as php-parser drops it, and a body of
// nothing else ends where it is declared. A return before the last
// statement is no such place, as it is none to PHPStan. A statement the
// body cannot go on after, a switch, a try or a loop whose every way
// returns, is held to the returns inside it: on each, to what the ways
// that did not write the parameter leave; with no return inside, a throw,
// an exit, an endless loop or a call of a never function, nothing reaches
// the caller. PHPStan holds such a statement to the state it was entered
// with instead, and reports a return that wrote the parameter on every
// way; the checker does not follow it there.
//
// A finally block runs on the way out of the returns before it: what it
// writes there is written at each of them, with the type it leaves.
//
// Closures and arrow functions are not held to their tags, as PHPStan
// does not hold them. A reference taken to the parameter, `$alias = &$x`,
// counts as a write that hands it over unchecked: PHPStan reports nothing
// of what the alias writes, nor of the end after it, and the same holds of
// `static $x` and `global $x`. A closure that uses the parameter by
// reference does not, and what it may write is part of what the end
// holds. A function of php-src's writes what the overlay's @param-out
// says, held to the tag at the call, and a parameter PHPStan's map says it
// reads is no write, the end of the body held to what the parameter then
// holds, but where PHPStan writes it back all the same (phpwrites.go).
// What is left, settype() and a function with side effects that reads an
// argument that may be an object or a resource, is a write
// the checker does not type, and hands the parameter over unchecked.

// outParam is a by-reference parameter a @param-out documents, with what
// it holds where the body starts.
type outParam struct {
	p     *program.Param
	entry types.Type
}

// How a place where the body ends is reported: a statement after which the
// body ends (an empty body included), a return, and a statement that
// returns inside it.
const (
	endsHere = iota
	returnsHere
	returnsInside
)

// paramOuts prepares the check of the body about to be checked, env being
// the state at its start: the by-reference parameters a @param-out
// documents, and the statements the body ends with, each with the span its
// finding goes to. Nothing is prepared for a body without such a
// parameter. A variadic one is left alone: its variable holds the packed
// arguments, not one of them.
func (x *fnCtx) paramOuts(params []*program.Param, stmts []ast.Stmt, env *state) {
	for _, p := range params {
		if !p.ByRef || p.Out == nil || p.Variadic {
			continue
		}
		if x.outs == nil {
			x.outs = map[string]*outParam{}
		}
		op := &outParam{p: p, entry: p.Type}
		if info, ok := env.get(p.Name); ok {
			op.entry = info.t
		}
		x.outs[p.Name] = op
		x.outOrder = append(x.outOrder, op)
	}
	if x.outs == nil {
		return
	}
	x.trackReturns = true // the returns inside a statement the body ends with (outsAtEnd)
	x.outEnds = map[ast.Stmt]ast.Span{}
	stmts = withoutTrailingNops(stmts)
	if len(stmts) == 0 {
		return
	}
	last := stmts[len(stmts)-1]
	is, ok := last.(*ast.If)
	if !ok {
		x.outEnds[last] = last.Span()
		return
	}
	x.branchEnd(is.Then, is.S)
	for _, ei := range is.ElseIfs {
		x.branchEnd(ei.Body, ei.S)
	}
	if is.Else != nil {
		x.branchEnd(is.Else, is.Else.Span())
	} else {
		x.outEnds[is] = is.S // unless a condition always holds (outsPastIf)
	}
}

// outsPastIf drops the if s, which the body ends with, from the places it
// ends when one of its conditions always holds where it is tested, read
// from the types PHP declares (nativeState): no way leads past the if, as
// PHPStan's $ifAlwaysTrue and $lastElseIfConditionIsTrue have it. env is
// the state the if starts with.
func (x *fnCtx) outsPastIf(s *ast.If, env *state) {
	if _, end := x.outEnds[s]; !end {
		return
	}
	saved := x.c.mark()
	defer x.c.rollback(saved)
	_, f := x.cond(s.Cond, nativeState(env))
	for _, ei := range s.ElseIfs {
		if f.dead {
			break
		}
		_, f = x.cond(ei.Cond, f)
	}
	if f.dead {
		delete(x.outEnds, s)
	}
}

// branchEnd records the statement a branch of the body's last if ends
// with, the branch itself when it is empty, which PHPStan reports at the
// clause (at). A branch after a condition that is always true is never
// reached, and is no place the body ends.
func (x *fnCtx) branchEnd(body ast.Stmt, at ast.Span) {
	stmts := []ast.Stmt{body}
	if b, ok := body.(*ast.Block); ok {
		stmts = b.Stmts
	}
	stmts = withoutTrailingNops(stmts)
	if len(stmts) == 0 {
		x.outEnds[body] = at
		return
	}
	last := stmts[len(stmts)-1]
	x.outEnds[last] = last.Span()
}

// withoutTrailingNops is a statement list without the lone semicolons it
// ends with, which php-parser drops.
func withoutTrailingNops(stmts []ast.Stmt) []ast.Stmt {
	for len(stmts) > 0 {
		if _, nop := stmts[len(stmts)-1].(*ast.Nop); !nop {
			break
		}
		stmts = stmts[:len(stmts)-1]
	}
	return stmts
}

// outsAtEnd holds the state after s, a statement the body ends with, to
// the @param-out types; returns is the count of returns seen before s
// ran. After a statement nothing follows, the returns inside it are where
// the body ends.
func (x *fnCtx) outsAtEnd(s ast.Stmt, at ast.Span, env *state, returns int) {
	if _, end := x.outEnds[s]; !end {
		return // an if past which no way leads (outsPastIf)
	}
	if !env.dead {
		x.outsHeld(env, at, endsHere)
		return
	}
	if len(x.returnEnvs) == returns {
		return // it throws, exits, loops forever or calls a never function: nothing reaches the caller
	}
	exits := x.returnEnvs[returns:]
	if _, isReturn := s.(*ast.Return); isReturn {
		x.outsHeld(joinAll(exits...), at, returnsHere)
		return
	}
	x.outsReturned(exits, at)
}

// outsHeld reports each parameter documented @param-out that the body may
// end with, at at, unwritten and holding a value of another type.
func (x *fnCtx) outsHeld(env *state, at ast.Span, how int) {
	if env.dead {
		return
	}
	for _, op := range x.outOrder {
		if env.outs[op.p.Name] {
			continue // written on every way here: each write was held to the tag
		}
		info, ok := env.get(op.p.Name)
		if !ok {
			continue
		}
		x.outEnd(op, info.t, at, how)
	}
}

// outsReturned reports each parameter documented @param-out that a return
// inside a statement the body ends with may leave unwritten, holding a
// value of another type: what it holds there on the ways that did not
// write it. A return after a write on every way was held to the tag at
// the write.
func (x *fnCtx) outsReturned(exits []*state, at ast.Span) {
	for _, op := range x.outOrder {
		var held []types.Type
		for _, r := range exits {
			if r.dead {
				continue
			}
			every, wrote := r.outs[op.p.Name]
			if wrote && every {
				continue
			}
			info, ok := r.get(op.p.Name)
			if !ok {
				continue
			}
			t := info.t
			if wrote {
				t = types.Intersect(t, op.entry, x.p()) // the ways without a write
			}
			held = append(held, t)
		}
		if len(held) > 0 {
			x.outEnd(op, types.MakeUnion(held...), at, returnsInside)
		}
	}
}

// outEnd reports a parameter documented @param-out that the body ends
// with holding t, at at, unless t is of the tag's type.
func (x *fnCtx) outEnd(op *outParam, t types.Type, at ast.Span, how int) {
	out := x.outType(op.p)
	if _, never := t.(types.Never); never || types.IsSubtype(t, out, x.p()) {
		return
	}
	where := "ends here"
	switch how {
	case returnsHere:
		where = "returns here"
	case returnsInside:
		where = "returns inside this statement"
	}
	remedy := "write a value of type " + x.typeName(out) + " to it before the function ends, or declare the @param-out type it holds"
	if _, never := out.(types.Never); never {
		remedy = "a @param-out never says the function does not return; end it with a throw, or declare the @param-out type it holds"
	}
	x.c.reportRelated(at, P2213, related(x.file, op.p.Span, "parameter $%s declared here", op.p.Name),
		"$%s is documented @param-out %s, and %s %s with $%s holding %s: %s",
		op.p.Name, x.typeName(out), x.name, where, op.p.Name, x.typeName(t), remedy)
}

// wroteOut holds a write of a value of type vt to the variable name, at
// span, to the @param-out type of the parameter of that name, and records
// the write; nothing for any other variable. A write x.outUnchecked marks
// is recorded and held to nothing: its operation was refused, or another
// place holds it.
func (x *fnCtx) wroteOut(name string, vt types.Type, env *state, span ast.Span) {
	op := x.outs[name]
	if op == nil {
		return
	}
	env.wroteOut(name)
	out := x.outType(op.p)
	if x.outUnchecked || types.IsSubtype(vt, out, x.p()) {
		return
	}
	remedy := "write a value of type " + x.typeName(out) + ", or declare the @param-out type the body writes"
	if _, never := out.(types.Never); never {
		remedy = "a @param-out never says the function does not return; declare the @param-out type the body writes"
	}
	x.c.reportRelated(span, P2213, related(x.file, op.p.Span, "parameter $%s declared here", op.p.Name),
		"$%s is documented @param-out %s, and %s writes %s to it here: %s",
		op.p.Name, x.typeName(out), x.name, x.typeName(vt), remedy)
}

// handedOver records a write to the parameter e is, or holds an offset of,
// that is not held to its @param-out type: a reference taken to it, an
// unset(), a static or global variable of its name, a function of
// php-src's that writes what the checker does not type.
func (x *fnCtx) handedOver(e ast.Expr, env *state) {
	if v := x.outRoot(e); v != nil {
		env.wroteOut(v.Name)
	}
}

// outRoot is the parameter documented @param-out that e is, or holds an
// offset of; nil for anything else.
func (x *fnCtx) outRoot(e ast.Expr) *ast.Variable {
	if x.outs == nil {
		return nil
	}
	for {
		switch n := e.(type) {
		case *ast.Paren:
			e = n.Expr
			continue
		case *ast.Index:
			e = n.Array
			continue
		case *ast.Variable:
			if x.outs[n.Name] != nil {
				return n
			}
		}
		return nil
	}
}

// refusedSince reports whether the operation of a step or a compound
// assignment, typed since the count of findings was since, drew a P2301:
// the value it writes is the checker's guess, and the write of it no
// second finding.
func (x *fnCtx) refusedSince(since int) bool {
	if x.outs == nil {
		return false
	}
	if since > len(x.c.diags) {
		since = len(x.c.diags)
	}
	for _, d := range x.c.diags[since:] {
		if d.Code == P2301.ID {
			return true
		}
	}
	return false
}

// outType is a parameter's @param-out type as the body sees it: its own
// type parameters stay themselves, and a conditional type is both its
// branches, as nothing is known of the subject inside.
func (x *fnCtx) outType(p *program.Param) types.Type {
	return x.evalConditional(p.Out, nil)
}

// wroteOut records that the body certainly wrote the parameter of that
// name on the way here.
func (e *state) wroteOut(name string) {
	if e.outs == nil {
		e.outs = map[string]bool{}
	}
	e.outs[name] = true
}

// joinOuts gives n, the meet of a and b, the parameters written on the way
// there: on every way when on every way into both, on some otherwise.
func joinOuts(n, a, b *state) {
	if a.outs == nil && b.outs == nil {
		return
	}
	n.outs = map[string]bool{}
	for k, every := range a.outs {
		n.outs[k] = every && b.outs[k]
	}
	for k := range b.outs {
		if _, ok := a.outs[k]; !ok {
			n.outs[k] = false
		}
	}
}

// finallyWrote gives each return in rs, which the finally block runs
// after, the parameters the block wrote (wrote), on every way through it
// or on some, with what they hold once it has run (in fEnv), as the state
// after the try statement takes the variables the block assigns.
func finallyWrote(rs []*state, wrote map[string]bool, fEnv *state) {
	for _, r := range rs {
		mergeOuts(r, wrote)
		for k := range wrote {
			if info, ok := fEnv.get(k); ok {
				r.vars[k] = varInfo{t: info.t, native: info.native}
			}
		}
	}
}

// mergeOuts adds to e the writes of a block that ran after it: a write on
// every way through the block is one on every way to its end.
func mergeOuts(e *state, wrote map[string]bool) {
	for k, every := range wrote {
		if e.outs == nil {
			e.outs = map[string]bool{}
		}
		if every {
			e.outs[k] = true
		} else if _, ok := e.outs[k]; !ok {
			e.outs[k] = false
		}
	}
}
