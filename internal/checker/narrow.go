package checker

import (
	"math"
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// cond evaluates a condition and returns the environments in force when
// it is true and when it is false. env itself is left as the state after
// evaluating the condition's side effects (assignments).
func (x *fnCtx) cond(e ast.Expr, env *state) (then, els *state) {
	switch e := e.(type) {
	case *ast.Paren:
		return x.cond(e.Expr, env)
	case *ast.Unary:
		if e.Op == token.Kind('!') {
			t, f := x.cond(e.Expr, env)
			x.condValueType = types.TBool
			return f, t
		}
	case *ast.Binary:
		switch e.Op {
		case token.T_BOOLEAN_AND, token.T_LOGICAL_AND:
			t1, f1 := x.cond(e.Left, env)
			// The right operand runs only where the left holds, a version
			// guard's included (guards.go).
			positive, _ := x.guardOf(e.Left)
			pop := x.c.pushGuard(positive)
			t2, f2 := x.cond(e.Right, t1)
			pop()
			*env = *join(t2, join(f1, f2))
			x.condValueType = types.TBool
			return t2, join(f1, f2)
		case token.T_BOOLEAN_OR, token.T_LOGICAL_OR:
			t1, f1 := x.cond(e.Left, env)
			_, negative := x.guardOf(e.Left)
			pop := x.c.pushGuard(negative)
			t2, f2 := x.cond(e.Right, f1)
			pop()
			*env = *join(join(t1, t2), f2)
			x.condValueType = types.TBool
			return join(t1, t2), f2
		case token.T_LOGICAL_XOR:
			t1, f1 := x.cond(e.Left, env)
			both := join(t1, f1)
			t2, f2 := x.cond(e.Right, both)
			*env = *join(t2, f2)
			x.condValueType = types.TBool
			return join(t2, f2), join(t2, f2)
		case token.T_IS_IDENTICAL, token.T_IS_NOT_IDENTICAL, token.T_IS_EQUAL, token.T_IS_NOT_EQUAL:
			return x.comparison(e, env)
		case token.Kind('>'), token.Kind('<'), token.T_IS_GREATER_OR_EQUAL, token.T_IS_SMALLER_OR_EQUAL:
			return x.ordering(e, env)
		}
	case *ast.Instanceof:
		return x.instanceofCond(e, env)
	case *ast.IsExpr:
		return x.isCond(e, env)
	case *ast.Isset:
		then = env.clone()
		for _, v := range e.Vars {
			t := x.exprLenient(v, env)
			x.narrowPath(then, v, types.StripNull(t), true)
		}
		x.condValueType = types.TBool
		els = env.clone()
		if len(e.Vars) == 1 {
			t := x.exprLenient(e.Vars[0], env)
			if _, isVar := e.Vars[0].(*ast.Variable); isVar && !x.maybeUndefined(e.Vars[0], env) {
				x.narrowPath(els, e.Vars[0], types.Intersect(t, types.TNull, x.p()), false)
			}
		}
		return then, els
	case *ast.Empty:
		t := x.exprLenient(e.Expr, env)
		then, els = env.clone(), env.clone()
		x.narrowPath(then, e.Expr, types.Falsy(t), false)
		x.narrowPath(els, e.Expr, types.Truthy(t), true)
		x.condValueType = types.TBool
		return then, els
	case *ast.Call:
		if then, els, ok := x.predicateCall(e, env); ok {
			return then, els
		}
	case *ast.MethodCall:
		if then, els, ok := x.methodAssert(e, env); ok {
			return then, els
		}
	case *ast.StaticCall:
		// Foo::isDocument($x) with @phpstan-assert-if-true tags.
		if ce, ok := x.calleeOf(e, env); ok && len(ce.asserts) > 0 {
			x.staticCall(e, env, nil)
			then, els = x.applyAsserts(ce, env)
			x.condValueType = types.TBool
			return then, els
		}
	case *ast.Assign:
		// if ($x = expr): the assigned value's truthiness narrows $x.
		t := x.assign(e, env)
		x.condValueType = t
		then, els = env.clone(), env.clone()
		if e.Op == token.Kind('=') {
			x.narrowPath(then, e.Target, types.Truthy(t), true)
			x.narrowPath(els, e.Target, types.Falsy(t), false)
		}
		return then, els
	}
	t := x.expr(e, env, nil)
	x.condValueType = t
	then, els = env.clone(), env.clone()
	x.narrowPath(then, e, types.Truthy(t), true)
	x.narrowPath(els, e, types.Falsy(t), false)
	if c, ok := pregMatchCall(e); ok {
		x.pregNarrow(c, then)
	}
	x.alwaysCheck(e, t)
	// A fixed outcome makes the other branch unreachable (so `true ? 1 : 'a'` is 1).
	if !types.IsMixed(t) {
		if _, never := types.Truthy(t).(types.Never); never {
			then.dead = true
		} else if _, never := types.Falsy(t).(types.Never); never {
			els.dead = true
		}
	}
	return then, els
}

// alwaysCheck reports conditions whose outcome is fixed by their type.
func (x *fnCtx) alwaysCheck(e ast.Expr, t types.Type) {
	if x.loopCond {
		switch e.(type) {
		case *ast.ConstFetch, *ast.Name, *ast.IntLit, *ast.StringLit, *ast.FloatLit:
			return // `while (true)` is idiomatic
		}
	}
	if types.IsMixed(t) {
		return
	}
	if _, ok := t.(types.Never); ok {
		return
	}
	if _, ok := types.Truthy(t).(types.Never); ok {
		x.report(e.Span(), P2402, "condition is always false (%s)", x.typeName(t))
	} else if _, ok := types.Falsy(t).(types.Never); ok {
		x.report(e.Span(), P2402, "condition is always true (%s)", x.typeName(t))
	}
}

// memberProof is the path under which a method_exists() or
// property_exists() that held is remembered for a receiver; the `::`
// makes an assignment to the receiver drop it, and no call does.
func memberProof(receiver string, method bool, name string) string {
	if method {
		return receiver + "::#method " + strings.ToLower(name)
	}
	return receiver + "::#property " + name
}

// provenMember reports whether method_exists() or property_exists() proved
// a member of the receiver expression.
func provenMember(env *state, receiver ast.Expr, method bool, name string) bool {
	base, ok := pathKey(receiver)
	if !ok {
		return false
	}
	_, proven := env.getPath(memberProof(base, method, name))
	return proven
}

// narrowPath sets the narrowed type of a path expression in env; define
// marks a variable as certainly defined.
func (x *fnCtx) narrowPath(env *state, e ast.Expr, t types.Type, define bool) {
	switch p := e.(type) {
	case *ast.Paren:
		x.narrowPath(env, p.Expr, t, define)
		return
	case *ast.Assign:
		// `null !== ($x = $m?->f)` narrows what was assigned, and what it
		// was assigned from.
		if p.Op == token.Kind('=') {
			x.narrowPath(env, p.Target, t, define)
			x.narrowPath(env, p.Value, t, false)
		}
		return
	case *ast.PropertyFetch:
		// A non-null `$m?->f` says $m is not null.
		if p.NullSafe && nonNullType(t) {
			x.narrowPath(env, p.Object, types.StripNull(x.pathType(p.Object, env)), false)
		}
	case *ast.MethodCall:
		if p.NullSafe && nonNullType(t) {
			x.narrowPath(env, p.Object, types.StripNull(x.pathType(p.Object, env)), false)
		}
	case *ast.Index:
		// `is_string($data[$field])` with $field one of a few literals (a
		// foreach over them) narrows each of those offsets, as PHPStan does.
		if v, ok := p.Index.(*ast.Variable); ok {
			if base, ok := pathKey(p.Array); ok {
				if keys := literalOffsetKeys(x.pathType(v, env)); keys != nil {
					for _, k := range keys {
						env.setPath(base+k, t)
					}
					return
				}
			}
		}
	case *ast.Binary:
		// `is_string($a['k'] ?? null)`: a type without null says the offset
		// is set and of that type.
		if p.Op == token.T_COALESCE && !types.IsNullable(t) && !types.IsMixed(t) {
			if n, ok := constName(p.Right); ok && n == "null" {
				x.narrowPath(env, p.Left, t, define)
			}
		}
		return
	case *ast.Variable:
		if p.Name == "this" {
			env.setPath("$this", t)
			return
		}
		if superglobal(p.Name) && !localArgs(p.Name, env) {
			return
		}
		info, ok := env.get(p.Name)
		if !ok && !define {
			return
		}
		if _, isNever := t.(types.Never); isNever && !ok {
			return
		}
		// Narrowing keeps the member paths: the object is the same one.
		env.vars[p.Name] = varInfo{t: t, maybe: ok && info.maybe && !define, native: narrowNative(info.native, t, x.p()), conds: info.conds}
		if ok {
			env.applyConds(p.Name, t, x.p())
		}
		return
	}
	if key, ok := pathKey(e); ok {
		env.setPath(key, t)
	}
}

// literalOffsetKeys spells a union of literal keys the way pathKey spells
// offsets ("['service']", "[3]"); nil when a member is not a literal.
func literalOffsetKeys(t types.Type) []string {
	var keys []string
	for _, m := range types.Members(t) {
		switch m := m.(type) {
		case types.StringLit:
			keys = append(keys, "['"+m.V+"']")
		case types.Int:
			if m.Lo != m.Hi {
				return nil
			}
			keys = append(keys, "["+strconv.FormatInt(m.Lo, 10)+"]")
		default:
			return nil
		}
	}
	return keys
}

// narrowPathNative is narrowPath with the variable's PHP type narrowed by
// the same operation: nt is what the operation leaves of the PHP type, nil
// when the variable was certain.
func (x *fnCtx) narrowPathNative(env *state, e ast.Expr, t, nt types.Type, define bool) {
	x.narrowPath(env, e, t, define)
	if v, ok := variableOf(e); ok {
		if info, ok := env.get(v.Name); ok {
			if nt != nil {
				if _, never := nt.(types.Never); never || types.Equal(nt, t) {
					nt = nil
				}
			}
			info.native = nt
			env.vars[v.Name] = info
		}
	}
}

// nativeVar is the PHP type recorded for a variable expression, nil for
// anything else or for a certain variable.
func (x *fnCtx) nativeVar(e ast.Expr, env *state) types.Type {
	if v, ok := variableOf(e); ok {
		if info, ok := env.get(v.Name); ok {
			return info.native
		}
	}
	return nil
}

func variableOf(e ast.Expr) (*ast.Variable, bool) {
	switch e := e.(type) {
	case *ast.Paren:
		return variableOf(e.Expr)
	case *ast.Assign:
		if e.Op == token.Kind('=') {
			return variableOf(e.Target)
		}
	case *ast.Variable:
		return e, e.Name != "this"
	}
	return nil, false
}

// pathType returns the current type of a path without side effects.
func (x *fnCtx) pathType(e ast.Expr, env *state) types.Type {
	saved := x.c.mark()
	t := x.exprLenient(e, env.clone())
	x.c.rollback(saved)
	return t
}

// comparison narrows on ===, !==, ==, !=.
func (x *fnCtx) comparison(e *ast.Binary, env *state) (then, els *state) {
	strict := e.Op == token.T_IS_IDENTICAL || e.Op == token.T_IS_NOT_IDENTICAL
	negate := e.Op == token.T_IS_NOT_IDENTICAL || e.Op == token.T_IS_NOT_EQUAL
	lt := x.expr(e.Left, env, nil)
	rt := x.expr(e.Right, env, nil)
	x.condValueType = types.TBool
	if !strict {
		x.objectNumberComparison(e.Op, lt, rt, e.S)
	}
	if strict && !types.IsMixed(lt) && !types.IsMixed(rt) && !types.Overlaps(lt, rt, x.p()) {
		word := "false"
		x.condValueType = types.TFalse
		if negate {
			word = "true"
			x.condValueType = types.TTrue
		}
		if !x.nativeOverlap(e.Left, lt, e.Right, rt, env) || bytesAgainstText(lt, rt) {
			x.report(e.S, P2305, "strict comparison between %s and %s is always %s", x.typeName(lt), x.typeName(rt), word)
		}
	}
	then, els = env.clone(), env.clone()
	// count($x) === 0 and friends.
	if c, n, ok := countCompare(e); ok {
		at := x.pathType(c.Args.Args[0].Value, env)
		switch {
		case n == 0:
			x.narrowPath(then, c.Args.Args[0].Value, emptyArray(at), false)
			x.narrowPath(els, c.Args.Args[0].Value, nonEmptyArray(at), false)
		case n > 0:
			x.narrowPath(then, c.Args.Args[0].Value, nonEmptyArray(at), false)
		}
		if negate {
			return els, then
		}
		return then, els
	}
	// `'' === ($u['host'] ?? '')`: the default decides which branch knows
	// the offset. A default the literal cannot be: a match found the offset
	// set, to the literal. A default that is the literal: a mismatch found
	// the offset set, to something else.
	x.coalesceCompared(then, els, e.Left, rt, strict, env)
	x.coalesceCompared(then, els, e.Right, lt, strict, env)
	narrowBy := func(target ast.Expr, targetT, other types.Type) {
		if !isLiteralLike(other) || len(types.Members(other)) > 1 {
			// `$x === $y` with a non-literal, or with one of several values
			// (`$kind !== $person` where $person is null|A|B): the then
			// branch intersects, the else branch learns nothing — $x may
			// still be any one value $y is not.
			if strict {
				x.narrowPath(then, target, types.Intersect(targetT, other, x.p()), true)
			}
			return
		}
		if strict || isNullType(other) {
			var nthen, nels types.Type
			if nold := x.nativeVar(target, env); nold != nil {
				nthen, nels = types.Intersect(nold, other, x.p()), x.subtract(nold, other, x.p())
			}
			x.narrowPathNative(then, target, types.Intersect(targetT, other, x.p()), nthen, true)
			x.narrowPathNative(els, target, x.subtract(targetT, other, x.p()), nels, false)
			if !strict {
				// == null is truthiness-like.
				x.narrowPath(then, target, types.Falsy(targetT), true)
				x.narrowPath(els, target, types.Truthy(targetT), false)
			}
		}
	}
	narrowBy(e.Left, lt, rt)
	narrowBy(e.Right, rt, lt)
	// `1 === preg_match(…)`: the subject matched (with !==, in the branch
	// the swap below makes the else branch).
	for _, side := range []struct {
		call  ast.Expr
		other types.Type
	}{{e.Left, rt}, {e.Right, lt}} {
		if c, ok := pregMatchCall(side.call); ok && types.Equal(side.other, types.Int{Lo: 1, Hi: 1}) {
			x.pregNarrow(c, then)
		}
	}
	// `$x === []` / `$x !== []`
	if isEmptyArrayLit(e.Right) {
		x.narrowPath(then, e.Left, emptyArray(lt), false)
		x.narrowPath(els, e.Left, nonEmptyArray(lt), false)
	}
	if negate {
		return els, then
	}
	return then, els
}

// isCall reports a function, method or static call, parentheses aside.
func isCall(e ast.Expr) bool {
	for {
		p, ok := e.(*ast.Paren)
		if !ok {
			break
		}
		e = p.Expr
	}
	switch e.(type) {
	case *ast.Call, *ast.MethodCall, *ast.StaticCall:
		return true
	}
	return false
}

// pregMatchCall reports a call to PHP's preg_match() with a pattern and a
// subject.
func pregMatchCall(e ast.Expr) (*ast.Call, bool) {
	for {
		p, ok := e.(*ast.Paren)
		if !ok {
			break
		}
		e = p.Expr
	}
	c, ok := e.(*ast.Call)
	if !ok || c.Args == nil || c.Args.FirstClassCallable || len(c.Args.Args) < 2 {
		return nil, false
	}
	n, ok := c.Callee.(*ast.Name)
	if !ok || len(n.Parts) != 1 || !strings.EqualFold(n.Parts[0], "preg_match") {
		return nil, false
	}
	for _, a := range c.Args.Args[:2] {
		if a.Name != nil || a.Unpack || a.Value == nil {
			return nil, false
		}
	}
	return c, true
}

// pregNarrow gives the subject of a preg_match() that matched, in then,
// the type PHPStan gives it: a non-empty-string, when it is a string and
// every match of its constant pattern consumes a character (regex.go).
func (x *fnCtx) pregNarrow(c *ast.Call, then *state) {
	subject := c.Args.Args[1].Value
	st := x.pathType(subject, then)
	if types.IsMixed(st) || !types.IsSubtype(st, types.TString, x.p()) {
		return
	}
	patterns := types.Members(x.pathType(c.Args.Args[0].Value, then))
	if len(patterns) == 0 {
		return
	}
	for _, m := range patterns {
		lit, ok := m.(types.StringLit)
		if !ok || !regexConsumes(lit.V) {
			return
		}
	}
	x.narrowPath(then, subject, types.Intersect(st, types.String{NonEmpty: true}, x.p()), false)
}

// coalesceCompared narrows the left operand of a `??` compared strictly
// with one literal; see comparison.
func (x *fnCtx) coalesceCompared(then, els *state, side ast.Expr, other types.Type, strict bool, env *state) {
	for {
		p, ok := side.(*ast.Paren)
		if !ok {
			break
		}
		side = p.Expr
	}
	co, ok := side.(*ast.Binary)
	if !ok || co.Op != token.T_COALESCE || !strict || !isLiteralLike(other) || len(types.Members(other)) > 1 {
		return
	}
	saved := x.c.mark() // both operands were typed with the comparison already
	def := x.exprLenient(co.Right, env)
	inner := types.StripNull(x.exprLenient(co.Left, env))
	x.c.rollback(saved)
	switch {
	case types.IsMixed(def) || types.IsMixed(inner):
	case !types.Overlaps(def, other, x.p()):
		x.narrowPath(then, co.Left, types.Intersect(inner, other, x.p()), true)
	case isLiteralLike(def) && len(types.Members(def)) == 1:
		x.narrowPath(els, co.Left, x.subtract(inner, other, x.p()), true)
	}
}

func isNullType(t types.Type) bool {
	_, ok := t.(types.Null)
	return ok
}

// isLiteralLike reports whether narrowing against t is meaningful.
func isLiteralLike(t types.Type) bool {
	for _, m := range types.Members(t) {
		switch m := m.(type) {
		case types.Null, types.BoolLit, types.StringLit, types.FloatLit, types.EnumCase, types.Bool:
		case types.Int:
			if m.Lo != m.Hi {
				return false
			}
		case types.Shape:
			if len(m.Entries) > 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func isEmptyArrayLit(e ast.Expr) bool {
	a, ok := e.(*ast.ArrayLit)
	return ok && len(a.Items) == 0
}

func emptyArray(t types.Type) types.Type {
	return types.Map(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Array:
			if m.NonEmpty {
				return types.TNever
			}
			return types.Shape{List: m.List}
		case types.Shape:
			for _, e := range m.Entries {
				if !e.Optional {
					return types.TNever
				}
			}
			return types.Shape{List: m.List}
		}
		return m
	})
}

func nonEmptyArray(t types.Type) types.Type {
	return types.Map(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Array:
			m.NonEmpty = true
			return m
		case types.Shape:
			if len(m.Entries) == 0 && !m.Unsealed {
				return types.TNever
			}
		}
		return m
	})
}

// countCompare recognises count($x) <op> N with a literal N (either side).
func countCompare(e *ast.Binary) (*ast.Call, int64, bool) {
	call, ok := e.Left.(*ast.Call)
	lit, ok2 := e.Right.(*ast.IntLit)
	if !ok || !ok2 {
		call, ok = e.Right.(*ast.Call)
		lit, ok2 = e.Left.(*ast.IntLit)
		if !ok || !ok2 {
			return nil, 0, false
		}
	}
	n, isName := call.Callee.(*ast.Name)
	if !isName || len(n.Parts) != 1 || !strings.EqualFold(n.Parts[0], "count") || call.Args == nil || len(call.Args.Args) == 0 {
		return nil, 0, false
	}
	v, ok := types.IsIntLit(intLitType(lit.Raw))
	if !ok {
		return nil, 0, false
	}
	return call, v, true
}

// ordering narrows count($x) > 0 and similar.
func (x *fnCtx) ordering(e *ast.Binary, env *state) (then, els *state) {
	lt := x.expr(e.Left, env, nil)
	rt := x.expr(e.Right, env, nil)
	x.condValueType = types.TBool
	x.objectNumberComparison(e.Op, lt, rt, e.S)
	then, els = env.clone(), env.clone()
	// A range from a docblock (`@param positive-int`) is not certain: PHP
	// may still pass what the check refuses (checker.md §9).
	certain := types.Equal(x.nativeOr(e.Left, lt, env), lt) && types.Equal(x.nativeOr(e.Right, rt, env), rt)
	if li, ok := lt.(types.Int); ok && certain {
		if ri, ok := rt.(types.Int); ok {
			if always, value := orderingOutcome(e.Op, li, ri); always {
				x.report(e.S, P2402, "comparison between %s and %s is always %v", x.typeName(lt), x.typeName(rt), value)
				if value {
					els.dead = true
				} else {
					then.dead = true
				}
			}
		}
	}
	c, n, ok := countCompare(e)
	if !ok {
		x.rangeNarrow(then, e.Left, lt, rt, e.Op)
		x.rangeNarrow(then, e.Right, rt, lt, flipOrdering(e.Op))
		x.rangeNarrow(els, e.Left, lt, rt, negateOrdering(e.Op))
		x.rangeNarrow(els, e.Right, rt, lt, negateOrdering(flipOrdering(e.Op)))
		return then, els
	}
	arg := c.Args.Args[0].Value
	at := x.pathType(arg, env)
	callOnLeft := e.Left.(*ast.Call) == c
	op := e.Op
	if !callOnLeft {
		// N < count($x) is count($x) > N
		switch op {
		case token.Kind('<'):
			op = token.Kind('>')
		case token.Kind('>'):
			op = token.Kind('<')
		case token.T_IS_SMALLER_OR_EQUAL:
			op = token.T_IS_GREATER_OR_EQUAL
		case token.T_IS_GREATER_OR_EQUAL:
			op = token.T_IS_SMALLER_OR_EQUAL
		}
	}
	switch {
	case op == token.Kind('>') && n >= 0, op == token.T_IS_GREATER_OR_EQUAL && n >= 1:
		x.narrowPath(then, arg, nonEmptyArray(at), false)
		if (op == token.Kind('>') && n == 0) || (op == token.T_IS_GREATER_OR_EQUAL && n == 1) {
			x.narrowPath(els, arg, emptyArray(at), false)
		}
	case op == token.Kind('<') && n <= 1, op == token.T_IS_SMALLER_OR_EQUAL && n <= 0:
		x.narrowPath(then, arg, emptyArray(at), false)
		x.narrowPath(els, arg, nonEmptyArray(at), false)
	}
	return then, els
}

// rangeNarrow narrows an int compared with an int by `<`, `>`, `<=` or `>=`
// to the range the comparison leaves it: after `$port < 1 || $port > 65535`
// has returned, $port is int<1, 65535>, as PHPStan says. The bound is the
// other operand's range; `$x < $y` with $y in int<0, 10> leaves $x at most 9
// when true and at least 0 when false. Members of $x that are not int (a
// null PHP would compare as 0) stay.
func (x *fnCtx) rangeNarrow(env *state, target ast.Expr, t, bound types.Type, op token.Kind) {
	if env.dead {
		return
	}
	b, ok := bound.(types.Int)
	if !ok {
		return
	}
	var lo, hi int64 = math.MinInt64, math.MaxInt64
	switch op {
	case token.Kind('<'):
		if b.Hi == math.MinInt64 {
			return
		}
		hi = b.Hi - 1
	case token.T_IS_SMALLER_OR_EQUAL:
		hi = b.Hi
	case token.Kind('>'):
		if b.Lo == math.MaxInt64 {
			return
		}
		lo = b.Lo + 1
	case token.T_IS_GREATER_OR_EQUAL:
		lo = b.Lo
	default:
		return
	}
	var out []types.Type
	narrowed := false
	for _, m := range types.Members(t) {
		i, ok := m.(types.Int)
		if !ok {
			out = append(out, m)
			continue
		}
		i.Lo, i.Hi = max(i.Lo, lo), min(i.Hi, hi)
		if i.Lo > i.Hi {
			narrowed = true
			continue // nothing of this member satisfies the comparison
		}
		narrowed = narrowed || i != m
		out = append(out, i)
	}
	if !narrowed {
		return
	}
	if len(out) == 0 {
		x.narrowPath(env, target, types.Never{}, false)
		return
	}
	x.narrowPath(env, target, types.MakeUnion(out...), false)
}

// flipOrdering turns `N < $x` into `$x > N`: the comparison seen from the
// right operand.
func flipOrdering(op token.Kind) token.Kind {
	switch op {
	case token.Kind('<'):
		return token.Kind('>')
	case token.Kind('>'):
		return token.Kind('<')
	case token.T_IS_SMALLER_OR_EQUAL:
		return token.T_IS_GREATER_OR_EQUAL
	case token.T_IS_GREATER_OR_EQUAL:
		return token.T_IS_SMALLER_OR_EQUAL
	}
	return op
}

// negateOrdering is the comparison that holds when op does not.
func negateOrdering(op token.Kind) token.Kind {
	switch op {
	case token.Kind('<'):
		return token.T_IS_GREATER_OR_EQUAL
	case token.Kind('>'):
		return token.T_IS_SMALLER_OR_EQUAL
	case token.T_IS_SMALLER_OR_EQUAL:
		return token.Kind('>')
	case token.T_IS_GREATER_OR_EQUAL:
		return token.Kind('<')
	}
	return op
}

// instanceofCond narrows on instanceof and reports always-true/false.
func (x *fnCtx) instanceofCond(e *ast.Instanceof, env *state) (then, els *state) {
	t := x.expr(e.Expr, env, nil)
	target := x.instanceofTarget(e, env)
	x.condValueType = types.TBool
	then, els = env.clone(), env.clone()
	if target == nil {
		return then, els
	}
	_, literalClass := e.Class.(*ast.Name)
	if nt := x.nativeOr(e.Expr, t, env); !types.IsMixed(t) && literalClass {
		// `$x instanceof $class` with a class-string<Foo> tests for a
		// subclass the string names: never decided here, as PHPStan.
		if _, isObject := target.(types.Object); !isObject {
			switch {
			case types.IsSubtype(t, target, x.p()) && !isParamType(t):
				x.condValueType = types.TTrue
				if !types.IsMixed(nt) && types.IsSubtype(nt, target, x.p()) {
					x.report(e.S, P2306, "%s is always an instance of %s", x.typeName(t), x.typeName(target))
				}
			case !types.Overlaps(t, target, x.p()):
				x.condValueType = types.TFalse
				if !types.IsMixed(nt) && !types.Overlaps(nt, target, x.p()) {
					x.report(e.S, P2306, "%s is never an instance of %s", x.typeName(t), x.typeName(target))
				}
			}
		}
	}
	var nthen, nels types.Type
	if nold := x.nativeVar(e.Expr, env); nold != nil {
		nthen, nels = types.Intersect(nold, target, x.p()), x.subtract(nold, target, x.p())
	}
	x.narrowPathNative(then, e.Expr, types.Intersect(t, target, x.p()), nthen, true)
	x.narrowPathNative(els, e.Expr, x.subtract(t, target, x.p()), nels, false)
	return then, els
}

func isParamType(t types.Type) bool {
	for _, m := range types.Members(t) {
		if _, ok := m.(types.Param); ok {
			return true
		}
	}
	return false
}

// instanceofTarget resolves the right-hand side of instanceof to a type.
func (x *fnCtx) instanceofTarget(e *ast.Instanceof, env *state) types.Type {
	switch c := e.Class.(type) {
	case *ast.Name:
		if specialClassName(c) == "parent" && x.class != nil {
			if pc := x.class.ParentClass(); pc != nil {
				return types.Class{Name: pc.Name}
			}
		}
		// instanceof with a class the running PHP lacks is false, not an
		// error: no library check.
		saved := x.nameOnly
		x.nameOnly = true
		t, _ := x.classRefType(c, c.S)
		x.nameOnly = saved
		return types.StripNull(t)
	default:
		ct := x.expr(e.Class, env, nil)
		var out []types.Type
		for _, m := range types.Members(ct) {
			switch m := m.(type) {
			case types.ClassString:
				if m.Of != nil {
					out = append(out, m.Of)
				} else {
					out = append(out, types.TObject)
				}
			case types.StringLit:
				out = append(out, types.Class{Name: m.V})
			case types.Class, types.Static, types.This:
				out = append(out, m)
			default:
				out = append(out, types.TObject)
			}
		}
		return types.MakeUnion(out...)
	}
}

// instanceofType types an instanceof used as a value.
func (x *fnCtx) instanceofType(e *ast.Instanceof, env *state) {
	then, els := x.instanceofCond(e, env)
	*env = *join(then, els)
}

// predicateTarget is the type an is_*() function tests for, nil for other
// names.
func predicateTarget(name string) types.Type {
	switch name {
	case "is_int", "is_integer", "is_long":
		return types.TInt
	case "is_float", "is_double":
		return types.TFloat
	case "is_string":
		return types.TString
	case "is_bool":
		return types.TBool
	case "is_array":
		return types.TArray
	case "is_object":
		return types.TObject
	case "is_null":
		return types.TNull
	case "is_iterable":
		return types.TIterable
	case "is_callable":
		return types.TCallable
	case "is_scalar":
		return types.TScalar
	case "is_numeric":
		return types.TNumeric
	case "is_countable":
		return types.MakeUnion(types.TArray, types.NewClass("Countable"))
	case "is_resource":
		return types.TResource
	}
	return nil
}

// predicateCall narrows on is_*() and the other predicate functions.
func (x *fnCtx) predicateCall(e *ast.Call, env *state) (then, els *state, ok bool) {
	n, isName := e.Callee.(*ast.Name)
	if !isName || e.Args == nil || len(e.Args.Args) == 0 || e.Args.FirstClassCallable {
		return nil, nil, false
	}
	name := strings.ToLower(n.Parts[len(n.Parts)-1])
	if len(n.Parts) > 1 && n.Kind != ast.NameFullyQualified {
		return nil, nil, false
	}
	arg := e.Args.Args[0].Value
	target := predicateTarget(name)
	if target == nil {
		switch name {
		case "is_a", "is_subclass_of":
			if len(e.Args.Args) < 2 {
				return nil, nil, false
			}
			x.call(e, env, nil)
			ct := x.pathType(e.Args.Args[1].Value, env)
			var cls types.Type
			switch c := ct.(type) {
			case types.StringLit:
				cls = types.Class{Name: c.V}
			case types.ClassString:
				cls = types.TObject // some class: the subject is an object, or a class-string
				if c.Of != nil {
					cls = c.Of
				}
			case types.String:
				cls = types.TObject
			}
			then, els = env.clone(), env.clone()
			x.condValueType = types.TBool
			if cls == nil {
				return then, els, true
			}
			at := x.pathType(arg, env)
			// $allow_string: false by default for is_a, true for
			// is_subclass_of; an argument that is not a literal may be
			// either, which the class-string reading covers.
			allowString := name == "is_subclass_of"
			if len(e.Args.Args) >= 3 {
				lit, ok := constName(e.Args.Args[2].Value)
				allowString = !ok || lit != "false"
			}
			if allowString {
				x.narrowPath(then, arg, types.Intersect(at, types.MakeUnion(cls, types.ClassString{Of: cls}), x.p()), true)
			} else {
				x.narrowPath(then, arg, types.Intersect(at, cls, x.p()), true)
				x.narrowPath(els, arg, x.subtract(at, cls, x.p()), false)
			}
			return then, els, true
		case "array_key_exists", "key_exists":
			if len(e.Args.Args) < 2 {
				return nil, nil, false
			}
			x.call(e, env, nil)
			then, els = env.clone(), env.clone()
			x.condValueType = types.TBool
			keyT := x.pathType(arg, env)
			arrExpr := e.Args.Args[1].Value
			at := x.pathType(arrExpr, env)
			x.narrowPath(then, arrExpr, withKey(at, keyT, true), false)
			x.narrowPath(els, arrExpr, withKey(at, keyT, false), false)
			return then, els, true
		case "in_array":
			if len(e.Args.Args) < 3 || !isTrueLit(e.Args.Args[2].Value) {
				return nil, nil, false
			}
			x.call(e, env, nil)
			then, els = env.clone(), env.clone()
			x.condValueType = types.TBool
			at := x.pathType(arg, env)
			hay := x.pathType(e.Args.Args[1].Value, env)
			_, v, _ := types.IterableKV(hay, x.p())
			if isLiteralLike(v) {
				x.narrowPath(then, arg, types.Intersect(at, v, x.p()), true)
				x.narrowPath(els, arg, x.subtract(at, v, x.p()), false)
			} else {
				x.narrowPath(then, arg, types.Intersect(at, v, x.p()), true)
			}
			return then, els, true
		case "assert":
			then, els = x.cond(arg, env)
			x.condValueType = types.TBool
			return then, then.clone(), true
		case "method_exists", "property_exists":
			x.call(e, env, nil)
			x.condValueType = types.TBool
			then, els = env.clone(), env.clone()
			// `method_exists($o, 'toArray')`: on an `object` the member is
			// known from here on, as PHPStan's hasMethod / hasProperty.
			if len(e.Args.Args) >= 2 {
				if base, ok := pathKey(arg); ok {
					if lit, ok := e.Args.Args[1].Value.(*ast.StringLit); ok {
						then.setPath(memberProof(base, name == "method_exists", program.StringValue(lit)), types.TMixed)
					}
				}
			}
			return then, els, true
		case "function_exists":
			x.call(e, env, nil)
			x.condValueType = types.TBool
			return env.clone(), env.clone(), true
		default:
			// User functions with @phpstan-assert.
			fn := x.lookupFunction(n)
			if fn == nil || len(fn.Signature().Asserts) == 0 {
				return nil, nil, false
			}
			x.call(e, env, nil)
			then, els = x.applyAsserts(x.functionCallee(fn, e.Args), env)
			x.condValueType = types.TBool
			return then, els, true
		}
	}
	x.call(e, env, nil)
	x.condValueType = types.TBool
	at := x.pathType(arg, env)
	then, els = env.clone(), env.clone()
	var nthen, nels types.Type
	if nold := x.nativeVar(arg, env); nold != nil {
		nthen, nels = types.Intersect(nold, target, x.p()), x.subtract(nold, target, x.p())
	}
	x.narrowPathNative(then, arg, types.Intersect(at, target, x.p()), nthen, true)
	x.narrowPathNative(els, arg, x.subtract(at, target, x.p()), nels, false)
	if nt := x.nativeOr(arg, at, env); !types.IsMixed(at) {
		if _, isNever := types.Intersect(at, target, x.p()).(types.Never); isNever {
			x.condValueType = types.TFalse
			if _, never := types.Intersect(nt, target, x.p()).(types.Never); never && !types.IsMixed(nt) {
				x.report(e.S, P2402, "%s() on %s is always false", name, x.typeName(at))
			}
		} else if types.IsSubtype(at, target, x.p()) && !isParamType(at) {
			x.condValueType = types.TTrue
			if !types.IsMixed(nt) && types.IsSubtype(nt, target, x.p()) {
				x.report(e.S, P2402, "%s() on %s is always true", name, x.typeName(at))
			}
		}
	}
	return then, els, true
}

// subtract is types.Subtract with enums expanded into their cases first, so
// that removing Suit::Hearts from Suit leaves the other cases.
func (x *fnCtx) subtract(t, target types.Type, h types.Hierarchy) types.Type {
	return types.Subtract(x.expandEnums(t), target, h)
}

func (x *fnCtx) expandEnums(t types.Type) types.Type {
	return types.Map(t, func(m types.Type) types.Type {
		c, ok := m.(types.Class)
		if !ok {
			return m
		}
		cl := x.p().Class(c.Name)
		if cl == nil || cl.Kind != ast.KindEnum {
			return m
		}
		cl.Signature()
		if len(cl.Cases) == 0 {
			return m
		}
		cases := make([]types.Type, 0, len(cl.Cases))
		for _, cs := range cl.Cases {
			cases = append(cases, types.EnumCase{Class: cl.Name, Case: cs.Name})
		}
		return types.MakeUnion(cases...)
	})
}

func isTrueLit(e ast.Expr) bool {
	n, ok := constName(e)
	return ok && n == "true"
}

// withKey marks a shape key as present (or absent) in t.
func withKey(t types.Type, key types.Type, present bool) types.Type {
	key = normaliseKey(key)
	return types.Map(t, func(m types.Type) types.Type {
		if _, isMixed := m.(types.Mixed); isMixed && present {
			// array_key_exists('k', $m) answers true only for an array
			// that has the key.
			switch k := key.(type) {
			case types.StringLit:
				return types.Shape{Entries: []types.ShapeEntry{{Key: k.V, Type: types.TMixed}}, Unsealed: true}
			case types.Int:
				if k.Lo == k.Hi {
					return types.Shape{Entries: []types.ShapeEntry{{IsInt: true, IntKey: k.Lo, Type: types.TMixed}}, Unsealed: true}
				}
			}
			return types.Array{Key: types.TArrayKey, Value: types.TMixed, NonEmpty: true}
		}
		s, ok := m.(types.Shape)
		if !ok {
			if a, ok := m.(types.Array); ok && present {
				a.NonEmpty = true
				return a
			}
			return m
		}
		entries := append([]types.ShapeEntry{}, s.Entries...)
		for i, e := range entries {
			match := false
			switch k := key.(type) {
			case types.StringLit:
				match = !e.IsInt && e.Key == k.V
			case types.Int:
				match = e.IsInt && k.Lo == k.Hi && e.IntKey == k.Lo
			}
			if !match {
				continue
			}
			if present {
				entries[i].Optional = false
			} else {
				if !e.Optional {
					return types.TNever
				}
				entries = append(entries[:i], entries[i+1:]...)
			}
			return types.Shape{Entries: entries, Unsealed: s.Unsealed, List: s.List}
		}
		if present && !s.Unsealed {
			return types.TNever
		}
		return s
	})
}

// methodAssert narrows on methods with @phpstan-assert tags.
func (x *fnCtx) methodAssert(e *ast.MethodCall, env *state) (then, els *state, ok bool) {
	name, isIdent := identName(e.Name)
	if !isIdent {
		return nil, nil, false
	}
	obj := x.pathType(e.Object, env)
	if then, els, ok := x.reflectionNarrow(e, name, obj, env); ok {
		return then, els, true
	}
	var m *program.Method
	for _, cl := range x.classesOf(types.StripNull(obj), e.Object.Span()) {
		if mm := cl.FindMethod(name); mm != nil {
			m = mm
			break
		}
	}
	if m == nil || len(m.Asserts) == 0 {
		return nil, nil, false
	}
	x.methodCall(e, env, nil)
	then, els = x.applyAsserts(x.methodCallee(m, types.StripNull(obj), e.Args, e.Object), env)
	x.condValueType = types.TBool
	return then, els, true
}

// callee is what a call statement resolves to, for assertion handling.
type callee struct {
	asserts []docblockAssert
	params  []*program.Param
	tps     []*program.TypeParam
	args    *ast.ArgList
	this    ast.Expr
	// res resolves the assert types in the callee's own scope (its file,
	// class and templates); classSubst maps the declaring class's type
	// parameters to the receiver's arguments.
	res        *program.Resolver
	classSubst map[string]types.Type
}

// methodCallee builds the callee record of a method found on a receiver.
func (x *fnCtx) methodCallee(m *program.Method, recv types.Type, args *ast.ArgList, this ast.Expr) callee {
	return callee{asserts: m.Asserts, params: m.Params, tps: m.TypeParams, args: args, this: this,
		res:        x.p().NewResolver(m.Class.File, m.Class.Scope, m.Class, m.Span).WithParams(m.TypeParams),
		classSubst: x.memberSubst(recv, m.Class)}
}

// functionCallee builds the callee record of a function.
func (x *fnCtx) functionCallee(fn *program.Function, args *ast.ArgList) callee {
	fn.Signature()
	return callee{asserts: fn.Asserts, params: fn.Params, tps: fn.TypeParams, args: args,
		res: x.p().NewResolver(fn.File, fn.Scope, nil, fn.Span).WithParams(fn.TypeParams)}
}

// calleeOf finds the function or method a call expression targets, without
// reporting (the call itself is checked separately).
func (x *fnCtx) calleeOf(e ast.Expr, env *state) (callee, bool) {
	switch e := e.(type) {
	case *ast.Call:
		n, ok := e.Callee.(*ast.Name)
		if !ok || e.Args == nil || e.Args.FirstClassCallable {
			return callee{}, false
		}
		fn := x.lookupFunction(n)
		if fn == nil {
			return callee{}, false
		}
		return x.functionCallee(fn, e.Args), true
	case *ast.MethodCall:
		name, ok := identName(e.Name)
		if !ok || e.Args == nil || e.Args.FirstClassCallable {
			return callee{}, false
		}
		obj := x.pathType(e.Object, env)
		saved := x.c.mark()
		classes := x.classesOf(types.StripNull(obj), e.Object.Span())
		x.c.rollback(saved)
		for _, cl := range classes {
			if m := cl.FindMethod(name); m != nil {
				return x.methodCallee(m, types.StripNull(obj), e.Args, e.Object), true
			}
		}
	case *ast.StaticCall:
		name, ok := identName(e.Name)
		if !ok || e.Args == nil || e.Args.FirstClassCallable {
			return callee{}, false
		}
		var classes []*program.Class
		if n, isName := e.Class.(*ast.Name); isName {
			switch x.res.ClassName(n) {
			case "self", "static":
				if x.class != nil {
					classes = []*program.Class{x.class}
				}
			case "parent":
				if x.class != nil {
					if pc := x.class.ParentClass(); pc != nil {
						classes = []*program.Class{pc}
					}
				}
			default:
				if cl := x.p().Class(x.res.ClassName(n)); cl != nil {
					classes = []*program.Class{cl}
				}
			}
		} else {
			saved := x.c.mark()
			classes = x.classesOf(x.pathType(e.Class, env), e.Class.Span())
			x.c.rollback(saved)
		}
		for _, cl := range classes {
			if m := cl.FindMethod(name); m != nil {
				return x.methodCallee(m, types.Class{Name: cl.Name}, e.Args, nil), true
			}
		}
	}
	return callee{}, false
}

// applyCallAsserts handles a call statement whose callee declares
// unconditional @phpstan-assert tags (PHPUnit's assertNotNull and friends).
func (x *fnCtx) applyCallAsserts(e ast.Expr, env *state) bool {
	ce, ok := x.calleeOf(e, env)
	if !ok {
		return false
	}
	plain := false
	for _, a := range ce.asserts {
		if !a.IfTrue && !a.IfFalse {
			plain = true
		}
	}
	if !plain {
		return false
	}
	x.expr(e, env, nil)
	then, _ := x.applyAsserts(ce, env)
	*env = *then
	return true
}

// applyAsserts turns @phpstan-assert tags into narrowings of the argument
// expressions (or $this). Template parameters of the callee mentioned by
// the tags are inferred from the arguments first.
func (x *fnCtx) applyAsserts(ce callee, env *state) (then, els *state) {
	asserts, params, args, this := ce.asserts, ce.params, ce.args, ce.this
	then, els = env.clone(), env.clone()
	res := ce.res
	if res == nil {
		res = x.res.WithParams(ce.tps)
	}
	subst := map[string]types.Type{}
	if len(ce.tps) > 0 && args != nil {
		inferable := map[string]bool{}
		for _, tp := range ce.tps {
			inferable[tp.Name] = true
		}
		for i, arg := range args.Args {
			if arg.Value == nil {
				continue
			}
			var p *program.Param
			if arg.Name != nil {
				for _, cand := range params {
					if cand.Name == arg.Name.Name {
						p = cand
					}
				}
			} else if i < len(params) {
				p = params[i]
			}
			if p != nil {
				unify(p.Type, x.pathType(arg.Value, env), subst, inferable, x.p())
			}
		}
	}
	for _, a := range asserts {
		var target ast.Expr
		if a.Var == "this" {
			target = this
		} else {
			idx := -1
			for i, p := range params {
				if p.Name == a.Var {
					idx = i
				}
			}
			if idx < 0 || args == nil {
				continue
			}
			for i, arg := range args.Args {
				if (arg.Name == nil && i == idx) || (arg.Name != nil && arg.Name.Name == a.Var) {
					target = arg.Value
				}
			}
		}
		if target == nil {
			continue
		}
		t := types.Substitute(types.Substitute(res.Doc(a.Type), ce.classSubst), subst)
		if b, ok := t.(types.BoolLit); ok && a.Var != "this" && !a.IfTrue && !a.IfFalse && !a.Negated {
			// `@phpstan-assert true $condition` (PHPUnit's assertTrue): the
			// argument is a condition that holds from here on.
			if key, ok := pathKey(target); ok && isCall(target) {
				// A call's result an earlier assertion fixed is a docblock
				// fact, which PHPStan takes as uncertain: the object may
				// have changed since (another object holding it was
				// called). Asserted again, this assertion decides it.
				delete(then.paths, key)
			}
			saved := x.c.mark()
			cthen, cels := x.cond(target, then)
			x.c.rollback(saved) // the argument was checked when the call was
			if b.V {
				then = cthen
			} else {
				then = cels
			}
			els = then.clone()
			continue
		}
		cur := x.pathType(target, env)
		narrowed := types.Intersect(cur, t, x.p())
		if a.Exact {
			narrowed = t
		}
		subtracted := x.subtract(cur, t, x.p())
		if a.Negated {
			narrowed, subtracted = subtracted, narrowed
		}
		switch {
		case a.IfTrue:
			x.narrowPath(then, target, narrowed, true)
		case a.IfFalse:
			x.narrowPath(els, target, narrowed, true)
		default:
			x.narrowPath(then, target, narrowed, true)
			x.narrowPath(els, target, narrowed, true)
		}
	}
	return then, els
}

// reflectionNarrow types `$r->isSubclassOf(Foo::class)` and
// `$r->implementsInterface(Foo::class)` on a ReflectionClass: true means
// the reflected class is a Foo, so $r is ReflectionClass<Foo> from there
// (PHPStan's type-specifying extension does the same).
func (x *fnCtx) reflectionNarrow(e *ast.MethodCall, name string, obj types.Type, env *state) (then, els *state, ok bool) {
	switch strings.ToLower(name) {
	case "issubclassof", "implementsinterface":
	default:
		return nil, nil, false
	}
	if e.Args == nil || len(e.Args.Args) != 1 || e.Args.FirstClassCallable {
		return nil, nil, false
	}
	rc, isClass := types.StripNull(obj).(types.Class)
	if !isClass || !strings.EqualFold(rc.Name, "ReflectionClass") {
		return nil, nil, false
	}
	cls, isLit := x.classNameConst(e.Args.Args[0].Value)
	if !isLit {
		return nil, nil, false
	}
	x.methodCall(e, env, nil)
	x.condValueType = types.TBool
	then, els = env.clone(), env.clone()
	x.narrowPath(then, e.Object, types.Class{Name: "ReflectionClass", Args: []types.Type{types.Class{Name: cls}}}, false)
	return then, els, true
}

// nonNullType reports a type that excludes null and is not empty or mixed.
func nonNullType(t types.Type) bool {
	if _, never := t.(types.Never); never || t == nil {
		return false
	}
	return !types.IsNullable(t) && !types.IsMixed(t)
}
