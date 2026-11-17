package checker

import (
	"fmt"
	"slices"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Partial function application (PHP 8.6, design/php-versions.md §5): a call
// with placeholders creates a closure over the callee. The checker types it
// as Closure(…): R from the callee's declaration, reports at compile time
// what PHP throws when the closure is created, and records for the emitter
// how the closure's parameters are named and typed.

// pfaSlot is one parameter of the closure a partial application creates.
type pfaSlot struct {
	param    *program.Param // the callee's parameter it stands for
	name     string
	required bool
	variadic bool     // the callee's variadic parameter, kept variadic by `...`
	arg      *ast.Arg // the placeholder; nil for the parameters `...` adds
}

// pfaShape maps a partial application's arguments onto the callee.
type pfaShape struct {
	slots []pfaSlot
	// rest is set when `...` stands for the remaining parameters.
	rest bool
	// named is set when a named argument or placeholder is written.
	named bool
	// ok is false when the arguments do not fit the callee (the closure is
	// then typed as far as it can be, and not lowered).
	ok bool
}

// partialShape lays a partial application's arguments over the callee's
// parameters as PHP 8.6 does (zend_partial.c): positional placeholders
// first, then named ones in the order written, then — for `...` — every
// parameter left, with its own optionality, the variadic one last. A
// placeholder makes its parameter required. It reports what PHP throws when
// the closure is created: too many or too few arguments, a required
// parameter left out, a named placeholder for no parameter. A name that
// overwrites an argument or a placeholder before it is reported by
// checkCallArgs, as in any call; overwritten says there is one, and PHP
// throws it before it counts, so no parameter is reported left out then.
func (x *fnCtx) partialShape(sig *signature, args *ast.ArgList, span ast.Span, overwritten bool) *pfaShape {
	shape := &pfaShape{ok: !overwritten}
	params := sig.params
	var variadic *program.Param
	if n := len(params); n > 0 && params[n-1].Variadic {
		variadic = params[n-1]
		params = params[:n-1]
	}
	required := 0
	for i, p := range params {
		if !p.HasDefault {
			required = i + 1
		}
	}
	covered := map[*program.Param]bool{}
	byName := func(name string) *program.Param {
		for _, p := range params {
			if p.Name == name {
				return p
			}
		}
		return nil
	}
	var positional, named []pfaSlot
	given, variadicSlots := 0, 0
	for _, a := range args.Args {
		switch {
		case a.Placeholder != nil && a.Placeholder.Variadic:
			shape.rest = true
		case a.Unpack:
			shape.ok = false // P1003, reported by the syntax pass
		case a.Name != nil:
			shape.named = true
			p := byName(a.Name.Name)
			if a.Placeholder == nil {
				if p != nil {
					covered[p] = true
				}
				continue // an unknown name is checkCall's P2010
			}
			if p == nil {
				x.report(a.S, P2010, "%s has no parameter $%s a named placeholder can stand for (a variadic one cannot be named)", sig.name, a.Name.Name)
				shape.ok = false
				continue
			}
			if covered[p] {
				shape.ok = false // an overwrite: checkCallArgs' P2010
				continue
			}
			covered[p] = true
			named = append(named, pfaSlot{param: p, name: p.Name, required: true, arg: a})
		default:
			if shape.named {
				// A positional argument after a named one: PHP's compile
				// error (P2110, the syntax pass), so no shape.
				shape.ok = false
				return shape
			}
			i := given
			given++
			var p *program.Param
			switch {
			case i < len(params):
				p = params[i]
				covered[p] = true
			case variadic != nil:
				p = variadic
			default:
				continue // too many: reported below
			}
			if a.Placeholder == nil {
				continue
			}
			name := p.Name
			if p == variadic {
				// PHP names a placeholder that binds into the variadic
				// parameter after it, numbered from 0.
				name = fmt.Sprintf("%s%d", p.Name, variadicSlots)
				variadicSlots++
			}
			positional = append(positional, pfaSlot{param: p, name: name, required: true, arg: a})
		}
	}
	switch {
	case given > len(params) && variadic == nil:
		x.report(span, P2102, "partial application of %s expects at most %d argument%s, %d given", sig.name, len(params), plural(len(params)), given)
		shape.ok = false
	case shape.rest:
		// `...` stands for whatever is left, required or not.
	case overwritten:
		// A parameter the overwrite leaves unbound follows from it.
	case !shape.named && given < required:
		limit := "at least"
		if len(params) <= required {
			limit = "exactly"
		}
		x.report(span, P2101, "partial application of %s expects %s %d argument%s, %d given", sig.name, limit, required, plural(required), given)
		shape.ok = false
	default:
		for i, p := range params[:required] {
			if !p.HasDefault && !covered[p] {
				x.report(span, P2101, "partial application of %s leaves argument #%d ($%s) out: name it, or add a placeholder or ...", sig.name, i+1, p.Name)
				shape.ok = false
			}
		}
	}
	shape.slots = append(positional, named...)
	if shape.rest {
		for _, p := range params {
			if !covered[p] {
				shape.slots = append(shape.slots, pfaSlot{param: p, name: p.Name, required: !p.HasDefault})
			}
		}
		if variadic != nil {
			shape.slots = append(shape.slots, pfaSlot{param: variadic, name: variadic.Name, variadic: true})
		}
	}
	return shape
}

// partialClosure is the Closure type of a partial application, its
// parameter types and its return as checkCall resolved them.
func partialClosure(shape *pfaShape, paramType func(*program.Param) types.Type, ret types.Type) types.Type {
	c := types.Callable{Closure: true, HasSig: true, Return: ret}
	if c.Return == nil {
		c.Return = types.TMixed
	}
	for _, s := range shape.slots {
		c.Params = append(c.Params, types.CallableParam{Name: s.name, Type: paramType(s.param), Optional: !s.required,
			Variadic: s.variadic, ByRef: s.param.ByRef})
	}
	return c
}

// partialChoice chooses the type parameters of a partial application's
// callee that its placeholders leave to the closure's callers
// (design/php-versions.md §5.2). subst holds what the bound arguments
// determine, as in a call. In the order it is done, each step by whole
// sets of type parameters so that the order of the arguments says nothing:
//
//  1. generalise, once the arguments that are no closures are typed: a
//     type parameter the closure's callers give values of (givenParams)
//     takes its binding with the literals written in the bound arguments
//     read as their types (literalGeneral), `pick(?, 0)` with
//     `pick<T>(T $a, T $b): T` takes an int and not the 0 alone. Each
//     type parameter is asked by itself, and keeps its binding as written
//     where the general one would leave its bound (`T of 'asc'|'desc'`)
//     or a bound argument the callee took would no longer be taken, or
//     would be retyped if it is by reference (a `Box<T>` beside the
//     literal holds T as it is). A binding of never says nothing of what
//     the callers give, and is dropped (`append([], ?)`).
//  2. The closures and partial applications among the bound arguments are
//     typed (checkCallArgs), with the type parameters as they are then.
//  3. join: what the expected type gives each placeholder joins them
//     (closureTypeArgs), unless a bound argument, a closure included,
//     would no longer be taken: `put($box, ?)` with a Box<'asc'|'desc'>
//     stays a closure of those two.
//  4. A literal is kept as written after all where the place refuses the
//     closure with it generalised and takes the closure with it as
//     written: `takesDir(pick(?, 'asc'))` with a
//     `Closure('asc'|'desc'): 'asc'|'desc'` expected. Each generalised
//     type parameter is asked against the others as written.
//
// A type parameter nothing determines is left to its bound, and the
// result may leave a bound: both are checkCallArgs's.
type partialChoice struct {
	x         *fnCtx
	shape     *pfaShape
	tps       []*program.TypeParam
	subst     map[string]types.Type
	inferable map[string]bool
	declared  func(*program.Param) types.Type // a parameter's type under the call's fresh names
	taken     func() []string                 // what the callee makes of each bound argument (checkCallArgs)
	closure   func() types.Type               // the closure the application makes
	// written is, for each type parameter generalised, its binding with
	// the literals as written.
	written map[string]types.Type
}

// open is the closure's parameters as declared.
func (c *partialChoice) open() []types.CallableParam {
	open := make([]types.CallableParam, len(c.shape.slots))
	for i, s := range c.shape.slots {
		open[i] = types.CallableParam{Type: c.declared(s.param), Variadic: s.variadic}
	}
	return open
}

// set makes subst what m is.
func (c *partialChoice) set(m map[string]types.Type) {
	for name := range c.subst {
		if c.inferable[name] {
			delete(c.subst, name)
		}
	}
	for name, t := range m {
		if c.inferable[name] {
			c.subst[name] = t
		}
	}
}

func (c *partialChoice) copy() map[string]types.Type {
	m := map[string]types.Type{}
	for name, t := range c.subst {
		if c.inferable[name] {
			m[name] = t
		}
	}
	return m
}

// worse reports a bound argument the callee took before and refuses now,
// or one by reference whose parameter has another type now.
func worse(before, now []string) bool {
	for i := range now {
		if now[i] != before[i] && (now[i] == "refused" || strings.HasPrefix(now[i], "&")) {
			return true
		}
	}
	return false
}

// generalise is step 1.
func (c *partialChoice) generalise(general map[string]types.Type) {
	given := map[string]bool{}
	for _, o := range c.open() {
		// Not the T of `callable(T): R $callback`, whose values the
		// callee gives the callback: from the array of array_map(?, $xs).
		givenParams(o.Type, true, c.inferable, given)
	}
	bounds := map[string]types.Type{}
	for _, tp := range c.tps {
		if tp.Bound != nil {
			bounds[tp.Name] = tp.Bound
		}
	}
	names := make([]string, 0, len(given))
	for name := range given {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if _, never := c.subst[name].(types.Never); never {
			delete(c.subst, name)
		}
	}
	before := c.taken()
	c.written = map[string]types.Type{}
	for _, name := range names {
		cur, ok := c.subst[name]
		g, gok := general[name]
		if !ok || !gok || types.Equal(cur, g) {
			continue
		}
		if b := bounds[name]; b != nil {
			b = types.Substitute(b, c.subst)
			if types.IsSubtype(cur, b, c.x.p()) && !types.IsSubtype(g, b, c.x.p()) {
				continue
			}
		}
		c.subst[name] = g
		if !worse(before, c.taken()) {
			c.written[name] = cur
		}
		c.subst[name] = cur
	}
	for name := range c.written {
		c.subst[name] = general[name]
	}
	if len(c.written) > 1 && worse(before, c.taken()) {
		// Each alone is taken and all together are not: none.
		for name, cur := range c.written {
			c.subst[name] = cur
		}
		c.written = nil
	}
}

// join is steps 3 and 4. expected is what the place expects of the
// application; place is that, or what a call the application is an
// argument of would make of its parameter were it to return what it is
// expected to (checkCallArgs).
func (c *partialChoice) join(expected, place types.Type) {
	after := c.copy() // the bound arguments have all spoken
	open := c.open()
	// settle is the choice with the literals of the type parameters in
	// asWritten kept as written: the join, or without it where a bound
	// argument would no longer be taken.
	settle := func(asWritten map[string]bool) map[string]types.Type {
		c.set(after)
		for name := range asWritten {
			c.subst[name] = c.written[name]
		}
		without, before := c.copy(), c.taken()
		c.x.closureTypeArgs(open, expected, c.subst, c.inferable)
		if worse(before, c.taken()) {
			return without
		}
		return c.copy()
	}
	chosen := settle(nil)
	c.set(chosen)
	accepts := c.accepts(place)
	if len(c.written) == 0 || accepts == nil || accepts() {
		return
	}
	all := map[string]bool{}
	for name := range c.written {
		all[name] = true
	}
	asWritten := settle(all)
	if c.set(asWritten); !accepts() {
		c.set(chosen) // the literals are not what the place refuses
		return
	}
	// Which of them the place needs as written: those it refuses the
	// closure for when they alone are generalised.
	needed := map[string]bool{}
	for name := range all {
		rest := map[string]bool{}
		for other := range all {
			if other != name {
				rest[other] = true
			}
		}
		if c.set(settle(rest)); !accepts() {
			needed[name] = true
		}
	}
	if c.set(settle(needed)); !accepts() {
		c.set(asWritten)
	}
}

// accepts is the test of the closure against the callable type the place
// expects, nil where it expects none. A type parameter of a call that is
// still inferring it says nothing: whatever the closure takes and returns
// there is accepted.
func (c *partialChoice) accepts(place types.Type) func() bool {
	exp := expectedSignature(place)
	if exp == nil {
		return nil
	}
	want := *exp
	want.Params = slices.Clone(want.Params)
	unknown := func(t, as types.Type) types.Type {
		if t == nil || !mentionsFreshParam(t) {
			return t
		}
		return types.MapDeep(t, func(m types.Type) types.Type {
			if p, ok := m.(types.Param); ok && strings.Contains(p.Name, "\x00") {
				return as
			}
			return m
		})
	}
	for i := range want.Params {
		want.Params[i].Type = unknown(want.Params[i].Type, types.TNever)
	}
	want.Return = unknown(want.Return, types.TMixed)
	return func() bool { return types.IsAcceptable(c.closure(), want, c.x.p()) }
}

// closureTypeArgs joins, for a closure created from a generic callee (a
// partial application or a first-class callable) whose parameters open are
// left to its callers, what the expected type gives each of them into the
// callee's type parameters: `array_map(pick(?, 0), $names)` makes T
// int|string. The expected type is a declared one, or what a call inferred
// for its parameter, and is taken as it is. Nothing is expected where the
// type holds no callable signature.
func (x *fnCtx) closureTypeArgs(open []types.CallableParam, expected types.Type, subst map[string]types.Type, inferable map[string]bool) {
	exp := expectedSignature(expected)
	if exp == nil {
		return
	}
	for i, o := range open {
		// What the closure's parameter i receives: the expected type's
		// parameter i, its variadic one past them, and for a variadic
		// parameter of the closure every one from i on.
		var wants []types.Type
		n := len(exp.Params)
		switch {
		case o.Variadic && i < n:
			for _, p := range exp.Params[i:] {
				wants = append(wants, p.Type)
			}
		case i < n:
			wants = append(wants, exp.Params[i].Type)
		case n > 0 && exp.Params[n-1].Variadic:
			wants = append(wants, exp.Params[n-1].Type)
		}
		for _, want := range wants {
			// A type the enclosing call is still inferring says nothing
			// yet.
			if want == nil || mentionsFreshParam(want) {
				continue
			}
			unify(o.Type, want, subst, inferable, x.p())
		}
	}
}

// givenParams collects into out the type parameters among names that a
// value of type t gives values of: those it holds but as the parameters of
// a callable in it, which take them (and give those of a callable they
// take in turn).
func givenParams(t types.Type, gives bool, names, out map[string]bool) {
	switch t := t.(type) {
	case nil:
	case types.Param:
		if gives && names[t.Name] {
			out[t.Name] = true
		}
	case types.Callable:
		for _, p := range t.Params {
			givenParams(p.Type, !gives, names, out)
		}
		givenParams(t.Return, gives, names, out)
	case types.Union:
		for _, m := range t.Types {
			givenParams(m, gives, names, out)
		}
	case types.Intersection:
		for _, m := range t.Types {
			givenParams(m, gives, names, out)
		}
	case types.Array:
		givenParams(t.Key, gives, names, out)
		givenParams(t.Value, gives, names, out)
	case types.Iterable:
		givenParams(t.Key, gives, names, out)
		givenParams(t.Value, gives, names, out)
	case types.Shape:
		for _, e := range t.Entries {
			givenParams(e.Type, gives, names, out)
		}
	case types.ObjectShape:
		for _, e := range t.Entries {
			givenParams(e.Type, gives, names, out)
		}
	case types.ClassString:
		givenParams(t.Of, gives, names, out)
	case types.Class:
		// A generic class may give or take its argument: both.
		for _, a := range t.Args {
			givenParams(a, true, names, out)
			givenParams(a, false, names, out)
		}
	}
}

// expectedSignature is the callable type with a signature that an expected
// type holds, the first one as for a closure literal's parameters
// (closureParams); nil when it holds none.
func expectedSignature(expected types.Type) *types.Callable {
	for _, m := range types.Members(expected) {
		if c, ok := m.(types.Callable); ok && c.HasSig {
			return &c
		}
	}
	return nil
}

// firstClassCallable is the type of `f(...)`. A generic callee's closure
// keeps its type parameters open, under fresh identities, for whatever
// receives it to resolve: the inference of a call it is passed to (unify),
// a call of the closure itself (sigOfCallable), or a place that expects a
// callable type (closeOpen). Explicit type arguments are applied.
func (x *fnCtx) firstClassCallable(sig *signature) types.Type {
	c, tps := x.openCallable(sig)
	if len(sig.explicit) == 0 {
		return c
	}
	subst := map[string]types.Type{}
	for i, tp := range tps {
		if i < len(sig.explicit) {
			subst[tp.Name] = sig.explicit[i]
		}
	}
	return types.Substitute(c, subst)
}

// closeOpen resolves a closure that kept a generic function's type
// parameters open against the place it has come to, where that expects a
// callable type that says what the closure receives: the type parameters
// are chosen as a partial application's are, every parameter being a
// placeholder's. `takesTwo(pick(...))` with a
// `Closure(string, float): mixed` expected makes T float|string, and so
// does `$h = pick(...); takesTwo($h)`; what the place leaves undetermined
// is its bound. The closure stays open where nothing says what it
// receives, or where the call that will is still inferring it.
func (x *fnCtx) closeOpen(c types.Callable, expected types.Type, e ast.Expr) types.Type {
	exp := expectedSignature(expected)
	if exp == nil || !c.HasSig || !mentionsFreshParam(c) || mentionsFreshParam(*exp) {
		return c
	}
	var tps []*program.TypeParam
	inferable := map[string]bool{}
	types.Walk(c, func(m types.Type) {
		if p, ok := m.(types.Param); ok && strings.Contains(p.Name, "\x00") && !inferable[p.Name] {
			inferable[p.Name] = true
			tps = append(tps, &program.TypeParam{Name: p.Name, Bound: p.Bound})
		}
	})
	subst := map[string]types.Type{}
	x.closureTypeArgs(c.Params, expected, subst, inferable)
	for _, tp := range tps {
		v, ok := subst[tp.Name]
		switch {
		case !ok && tp.Bound != nil:
			subst[tp.Name] = tp.Bound
		case !ok:
			subst[tp.Name] = types.TMixed
		case tp.Bound != nil && !types.IsMixed(v):
			if bound := types.Substitute(tp.Bound, subst); !types.IsSubtype(v, bound, x.p()) {
				of := "the closure"
				if call, ok := unparen(e).(*ast.Call); ok && call.Args != nil && call.Args.FirstClassCallable {
					if n, named := call.Callee.(*ast.Name); named {
						of = strings.Join(n.Parts, "\\") + "()"
					}
				}
				x.report(e.Span(), P3002, "type argument %s inferred for %s of %s is not a subtype of its bound %s", x.typeName(v), types.ParamName(tp.Name), of, x.typeName(bound))
			}
		}
	}
	// A bound may name another parameter: twice resolves `U of T`.
	return types.Substitute(types.Substitute(c, subst), subst)
}

// literalGeneral is the type at of a bound argument e with the literals
// written in it read as their types, for the binding of a type parameter
// the closure's callers will give other values of: 0 is an int, 'a' a
// string, true a bool, -1.5 a float, `Suit::Hearts` written in place the
// enum, and `[]` an array. A constant is what it stands for: its declared
// type where it has one, else the literal its declaration writes, read the
// same way; one whose declaration writes no literal (`1 + 2`) is the type
// it has, as the expression is. An array literal whose keys and values are
// all literals is a literal all the way down, and its entries are read so.
// Anything else is typed as declared and stays as it is: a variable
// holding 'asc'|'desc', an int<1, 12>, a shape, a call's result. null is a
// type of its own and stays null.
func (x *fnCtx) literalGeneral(e ast.Expr, at types.Type) types.Type {
	if k, declared := x.constantOf(e); k != nil && !(k.File != nil && k.File.IsStub()) {
		// (One of PHP's own is the literal PHP gives it: PHP_INT_SIZE is
		// an int here, whatever the stubs say it may be.)
		switch {
		case k.Type != nil:
			if declared != nil {
				return declared
			}
			return at
		case k.Value != nil && !writtenLiteral(k.Value):
			return at
		}
	}
	if !writtenLiteral(e) {
		return at
	}
	return generalised(at)
}

// writtenLiteral reports an expression that is a literal as written:
// a number (signed or not), a string, true or false, a constant, an enum
// case, or an array literal of such keys and values.
func writtenLiteral(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Paren:
		return writtenLiteral(e.Expr)
	case *ast.IntLit, *ast.FloatLit, *ast.StringLit, *ast.MagicConst, *ast.ConstFetch, *ast.Name:
		return true // a name, however qualified, is a constant's
	case *ast.ClassConstFetch:
		_, named := e.Class.(*ast.Name)
		id, ident := e.Name.(*ast.Ident)
		return named && ident && !strings.EqualFold(id.Name, "class")
	case *ast.Unary:
		switch e.Expr.(type) {
		case *ast.IntLit, *ast.FloatLit:
			return e.Op == token.Kind('-') || e.Op == token.Kind('+')
		}
	case *ast.ArrayLit:
		for _, it := range e.Items {
			if it == nil || it.Unpack || it.ByRef || !writtenLiteral(it.Value) || it.Key != nil && !writtenLiteral(it.Key) {
				return false
			}
		}
		return true
	}
	return false
}

// constantOf is the declaration of the constant e names, a class's or a
// global one, with its declared type as the expression has it; nil when e
// names none that is found, or an enum case.
func (x *fnCtx) constantOf(e ast.Expr) (*program.Constant, types.Type) {
	switch e := unparen(e).(type) {
	case *ast.ClassConstFetch:
		n, named := e.Class.(*ast.Name)
		id, ident := e.Name.(*ast.Ident)
		if !named || !ident {
			return nil, nil
		}
		saved := x.c.mark()
		name, ok := x.resolveClassName(n, e.S)
		x.c.rollback(saved)
		if !ok {
			return nil, nil
		}
		cl := x.p().Class(name)
		if cl == nil || cl.FindCase(id.Name) != nil {
			return nil, nil
		}
		if k := cl.FindConst(id.Name); k != nil {
			return k, k.Type
		}
	case *ast.Name:
		return x.globalConstant(e)
	case *ast.ConstFetch:
		return x.globalConstant(e.Name)
	}
	return nil, nil
}

// globalConstant is the constant a name stands for as constFetch reads
// it: the namespace's, the one a `use const` imports, or PHP's fallback,
// the global one.
func (x *fnCtx) globalConstant(n *ast.Name) (*program.Constant, types.Type) {
	ns, global := x.res.Scope().ResolveConst(n)
	for _, name := range []string{ns, global} {
		if k := x.p().Constant(name); k != nil {
			return k, k.Type
		}
	}
	return nil, nil
}

// generalised is the type of a literal expression with its literals read
// as their types, in the entries of its arrays too, and the empty array as
// an array. The type of a literal holds nothing else: no argument of a
// class and no parameter of a callable is looked into.
func generalised(t types.Type) types.Type {
	switch t := t.(type) {
	case types.Int:
		if t.Lo == t.Hi {
			return types.TInt
		}
	case types.StringLit:
		return types.TString
	case types.FloatLit:
		return types.TFloat
	case types.BoolLit:
		return types.TBool
	case types.EnumCase:
		return types.NewClass(t.Class)
	case types.Union:
		ms := make([]types.Type, len(t.Types))
		for i, m := range t.Types {
			ms[i] = generalised(m)
		}
		return types.MakeUnion(ms...)
	case types.Shape:
		if len(t.Entries) == 0 && !t.Unsealed {
			return types.TArray // `[]`: an array, of whatever its callers give
		}
		entries := make([]types.ShapeEntry, len(t.Entries))
		for i, e := range t.Entries {
			e.Type = generalised(e.Type)
			entries[i] = e
		}
		t.Entries = entries
		return t
	case types.Array:
		t.Key, t.Value = generalised(t.Key), generalised(t.Value)
		return t
	}
	return t
}

// ------------------------------------------------------------------ lowering for an 8.5 target

// recordPartial stores the emitter's account of a partial application and,
// for an 8.5 target, reports a form with no exact lowering (§5.3). inPipe
// says the application is a pipe's right side that PHP turns into a call;
// callee is the call node; typeOf gives a parameter's type as the call
// resolved it.
func (x *fnCtx) recordPartial(callee ast.Expr, args *ast.ArgList, sig *signature, slots []pfaSlot, rest, named, inPipe bool, typeOf func(*program.Param) types.Type) {
	rec := &program.Partial{}
	for _, s := range slots {
		pp := program.PartialParam{Name: s.name, Optional: !s.required, Variadic: s.variadic, ByRef: s.param.ByRef, Arg: s.arg}
		var declared types.Type = types.TMixed // what PHP declares
		if s.param.NativeType != nil {
			native := x.selfResolved(s.param.NativeType, sig)
			if omitted, typeError := omittedNull(sig, s.param); omitted && typeError && s.required {
				// The stub's null stands for the argument left out, and
				// the closure always passes one: implode() and strtr() throw
				// a TypeError for the null, as the closure's parameter does
				// without it, and PHPStan holds the argument to the type
				// without null (omittedOnly).
				native = types.StripNull(native)
			}
			if n, ok := types.Native(native); ok {
				pp.Native = n
			}
			declared = types.Erase(native)
		}
		if t := typeOf(s.param); t != nil && x.namesParams(t) && !mentionsBytesType(t) && !mentionsNever(t) {
			// A type PHP cannot declare (list<int>, a literal, a generic
			// class) goes into the closure's docblock, for PHPStan. So
			// does the type chosen for a type parameter, where it says
			// more than the parameter's declaration: `pick(?, 0)` declares
			// what `T $a` does, mixed, and its $a is an int. Without it
			// PHPStan would type the closure's result from mixed, and
			// refuse the compiled file where the source was accepted.
			if !types.Equal(t, types.Erase(t)) || mentionsParam(s.param.Type) && !types.Equal(x.selfResolved(t, sig), declared) {
				pp.Doc = types.PrintQualifiedValues(x.selfResolved(t, sig))
				types.Walk(t, func(m types.Type) {
					switch m.(type) {
					case types.Array, types.Shape, types.Iterable:
						pp.DocArray = true
					}
				})
			}
		}
		if pp.Optional && !pp.Variadic {
			if d, ok := x.writableDefault(s.param.Default); ok {
				pp.Default = d
			} else {
				pp.Unwritable = true
			}
		}
		rec.Params = append(rec.Params, pp)
	}
	rec.Pipe, rec.Rest, rec.Named = inPipe, rest, named
	_, rec.Void = sig.ret.(types.Void)
	rec.Refused = x.partialRefusal(callee, args, sig, rec, rest, named, inPipe)
	if x.c.file.Partials == nil {
		x.c.file.Partials = map[*ast.ArgList]*program.Partial{}
	}
	x.c.file.Partials[args] = rec
	if rec.Refused != "" {
		x.c.needsPHP(callee.Span(), phpversion.PHP86, "this partial application", rec.Refused+", or target PHP 8.6")
	}
}

// phpstanReadsCallback are PHP's functions whose callback PHPStan types by
// itself, each parameter from the array (or the subject) the callback is
// applied to, in place of what the closure declares: it then refuses an
// inline @var of an array type that is not within what it read, a
// `list<int>` beside the `array{1, 2}` of a literal argument
// (varTag.nativeType). Nothing but array_map's result is learned from
// such a callback, and PHPStan computes that from the callback as it
// typed it, so the closure written there needs no @var of its own.
// Everywhere else the @var is what types the closure for PHPStan: a
// callable parameter of any other function, PHP's (usort, array_reduce) or
// not, leaves the closure's parameters as declared when PHPStan infers
// the function's type parameters from it. Measured on PHPStan 2.3.
var phpstanReadsCallback = map[string]bool{
	"array_map": true, "array_filter": true, "array_walk": true,
	"array_find": true, "array_find_key": true, "array_any": true, "array_all": true,
	"preg_replace_callback": true,
}

// partialOf is the emitter's account of the partial application e is, nil
// when it is none or was not recorded.
func (x *fnCtx) partialOf(e ast.Expr) *program.Partial {
	var args *ast.ArgList
	switch e := e.(type) {
	case *ast.Call:
		args = e.Args
	case *ast.MethodCall:
		args = e.Args
	case *ast.StaticCall:
		args = e.Args
	}
	if args == nil {
		return nil
	}
	return x.c.file.Partials[args]
}

// namesParams reports a type whose type parameters, if it has any, can be
// written here: those of the function and the class being checked, and
// none a call left open.
func (x *fnCtx) namesParams(t types.Type) bool {
	ok := true
	types.Walk(t, func(m types.Type) {
		if p, isParam := m.(types.Param); isParam && (strings.Contains(p.Name, "\x00") || !x.ownsParam(p)) {
			ok = false
		}
	})
	return ok
}

// mentionsNever reports a type with never in it: no value has it, and a
// @var of it tells PHPStan the code after it is dead.
func mentionsNever(t types.Type) bool {
	found := false
	types.Walk(t, func(m types.Type) {
		if _, ok := m.(types.Never); ok {
			found = true
		}
	})
	return found
}

// mentionsBytesType reports a type with bytes in it, which PHPStan does
// not know.
func mentionsBytesType(t types.Type) bool {
	found := false
	types.Walk(t, func(m types.Type) {
		if _, ok := m.(types.Bytes); ok {
			found = true
		}
	})
	return found
}

// selfResolved replaces self and static in a parameter type by the class
// they name, so that the type can be written outside it.
func (x *fnCtx) selfResolved(t types.Type, sig *signature) types.Type {
	if sig.decl == nil {
		return t
	}
	return types.MapDeep(t, func(m types.Type) types.Type {
		switch m.(type) {
		case types.Static, types.This:
			return types.NewClass(sig.decl.Name)
		}
		return m
	})
}

// writableDefault prints a parameter's default as PHP code that means the
// same in any file: a literal, or a global constant written fully
// qualified. Anything else — a class constant, `new`, php-src's UNKNOWN —
// is not copied.
func (x *fnCtx) writableDefault(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.IntLit, *ast.FloatLit, *ast.StringLit:
		src := x.sourceOf(e)
		return src, src != ""
	case *ast.Unary:
		if inner, ok := x.writableDefault(e.Expr); ok && (e.Op == token.Kind('-') || e.Op == token.Kind('+')) {
			if _, num := e.Expr.(*ast.IntLit); num {
				return string(rune(e.Op)) + inner, true
			}
			if _, num := e.Expr.(*ast.FloatLit); num {
				return string(rune(e.Op)) + inner, true
			}
		}
	case *ast.Name:
		name, ok := constName(e)
		switch {
		case !ok:
			return "", false
		case name == "null" || name == "true" || name == "false":
			return name, true
		case name == "unknown":
			return "", false
		}
		if c := x.p().Constant(strings.Join(e.Parts, "\\")); c != nil {
			return "\\" + c.Name, true
		}
	case *ast.ArrayLit:
		if len(e.Items) == 0 {
			return "[]", true
		}
		var parts []string
		for _, it := range e.Items {
			if it == nil || it.Unpack || it.ByRef {
				return "", false
			}
			v, ok := x.writableDefault(it.Value)
			if !ok {
				return "", false
			}
			if it.Key != nil {
				k, ok := x.writableDefault(it.Key)
				if !ok {
					return "", false
				}
				v = k + " => " + v
			}
			parts = append(parts, v)
		}
		return "[" + strings.Join(parts, ", ") + "]", true
	}
	return "", false
}

// sourceOf returns the text of a literal node. Literals of stubs and vendor
// files are read from their own file; the node does not say which, so the
// text is taken from the literal's value instead.
func (x *fnCtx) sourceOf(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.IntLit:
		return e.Raw
	case *ast.FloatLit:
		return e.Raw
	case *ast.StringLit:
		return e.Raw
	}
	return ""
}

// partialRefusal says why a partial application has no exact 8.5 lowering
// (§5.3), or "" when it has one.
func (x *fnCtx) partialRefusal(callee ast.Expr, args *ast.ArgList, sig *signature, rec *program.Partial, rest, named, inPipe bool) string {
	params := map[string]bool{}
	for _, p := range rec.Params {
		params[p.Name] = true
	}
	for _, a := range args.Args {
		if a.Placeholder != nil || a.Value == nil {
			continue
		}
		var p *program.Param
		if a.Name != nil {
			for _, cand := range sig.params {
				if cand.Name == a.Name.Name {
					p = cand
				}
			}
		}
		if p != nil && p.ByRef {
			return "it binds an argument by reference, which a closure written for PHP 8.5 cannot"
		}
		if inPipe {
			if writes(a.Value) {
				return "a bound argument writes to a variable, which the closure written for PHP 8.5 would do to its own copy"
			}
		} else if !stableArg(a.Value) {
			return "a bound argument is computed (a call, a property, an operator), which a closure written for PHP 8.5 would compute at every call: assign it to a variable first"
		}
		if name := clash(a.Value, params); name != "" {
			return fmt.Sprintf("a bound argument reads $%s, which the closure's parameter of that name would hide: rename the variable", name)
		}
	}
	// Positional by-reference bindings.
	given := 0
	for _, a := range args.Args {
		if a.Name != nil || a.Unpack || (a.Placeholder != nil && a.Placeholder.Variadic) {
			continue
		}
		if a.Placeholder == nil && given < len(sig.params) && sig.params[given].ByRef {
			return "it binds an argument by reference, which a closure written for PHP 8.5 cannot"
		}
		if a.Placeholder == nil && given >= len(sig.params) && len(sig.params) > 0 && sig.params[len(sig.params)-1].ByRef {
			return "it binds an argument by reference, which a closure written for PHP 8.5 cannot"
		}
		given++
	}
	if !inPipe {
		switch c := callee.(type) {
		case *ast.MethodCall:
			if !stableReceiver(c.Object) {
				return "its receiver is computed, which a closure written for PHP 8.5 would compute at every call: assign it to a variable first"
			}
			if name := clash(c.Object, params); name != "" {
				return fmt.Sprintf("its receiver $%s has the name of a parameter of the closure, which would hide it: rename the variable", name)
			}
		case *ast.Call:
			if _, isName := c.Callee.(*ast.Name); !isName {
				if v, isVar := c.Callee.(*ast.Variable); !isVar {
					return "the callee is computed, which a closure written for PHP 8.5 would compute at every call: assign it to a variable first"
				} else if params[v.Name] {
					return fmt.Sprintf("the callee $%s has the name of a parameter of the closure, which would hide it: rename the variable", v.Name)
				}
			}
		case *ast.StaticCall:
			if _, isName := c.Class.(*ast.Name); !isName {
				if !stableReceiver(c.Class) {
					return "its class is computed, which a closure written for PHP 8.5 would compute at every call: assign it to a variable first"
				}
			}
		}
	}
	if rest {
		variadic := false
		unwritable := false
		for _, p := range rec.Params {
			if p.Arg == nil {
				variadic = variadic || p.Variadic
				unwritable = unwritable || p.Unwritable
			}
		}
		if named && (variadic || unwritable) {
			return "`...` after a named argument would unpack after it, which PHP forbids"
		}
	}
	return ""
}

// stableArg reports a bound argument an arrow function may evaluate at each
// call with the result PHP 8.6 computes once: a literal, a constant, a
// variable (an arrow function captures it by value when it is created).
func stableArg(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Paren:
		return stableArg(e.Expr)
	case *ast.Variable:
		return true
	case *ast.IntLit, *ast.FloatLit, *ast.StringLit, *ast.Name, *ast.ConstFetch, *ast.MagicConst:
		return true
	case *ast.ClassConstFetch:
		_, named := e.Class.(*ast.Name)
		_, ident := e.Name.(*ast.Ident)
		return named && ident
	case *ast.Unary:
		return (e.Op == token.Kind('-') || e.Op == token.Kind('+')) && stableArg(e.Expr)
	case *ast.ArrayLit:
		for _, it := range e.Items {
			if it == nil {
				continue
			}
			if it.ByRef || (it.Key != nil && !stableArg(it.Key)) || !stableArg(it.Value) {
				return false
			}
		}
		return true
	}
	return false
}

// stableReceiver is a receiver an arrow function may evaluate at each call:
// a variable or $this.
func stableReceiver(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Variable:
		return true
	case *ast.Paren:
		return stableReceiver(e.Expr)
	}
	return false
}

// writes reports an expression that assigns: written inside a closure, the
// assignment would change the closure's copy of the variable. What an
// anonymous class's methods assign is theirs; its constructor arguments
// are the expression's (ast.InspectScope).
func writes(e ast.Expr) bool {
	found := false
	ast.InspectScope(e, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.Assign, *ast.IncDec, *ast.Yield, *ast.YieldFrom:
			found = true
		case *ast.Closure, *ast.ArrowFunc:
			return false
		}
		return !found
	})
	return found
}

// clash returns a variable the expression reads that has the name of one
// of the closure's parameters. An anonymous class's properties and method
// bodies read none: only its constructor arguments are the expression's.
func clash(e ast.Expr, params map[string]bool) string {
	name := ""
	ast.InspectScope(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Variable:
			if params[n.Name] && name == "" {
				name = n.Name
			}
		case *ast.Closure, *ast.ArrowFunc:
			return false
		}
		return name == ""
	})
	return name
}

// ------------------------------------------------------------------ syntax PHP checks at compile time

// partialSyntax reports what PHP 8.6's compiler rejects in argument lists
// with placeholders (P1003): a variadic placeholder that is not last or not
// alone, placeholders beside unpacking, a closure created from `new`, from
// a `?->` call or as an attribute's arguments. The parser accepts these as
// zendparse does. It also reports writes through a constant's object for a
// target before 8.6 (RFC const_object_property_write).
func (c *Checker) partialSyntax(f *program.File) {
	ast.Inspect(f.AST, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ArgList:
			c.argListSyntax(n)
		case *ast.New:
			if n.Args.Closure() {
				c.report(n.Args.S, P1003, "cannot create a closure from a new expression")
			}
		case *ast.MethodCall:
			if n.NullSafe && n.Args.Closure() {
				c.report(n.Args.S, P1003, "cannot combine ?-> with creating a closure")
			}
		case *ast.Attribute:
			if n.Args.Closure() {
				c.report(n.Args.S, P1003, "cannot create a closure as an attribute's arguments")
			}
		case *ast.Assign:
			c.constObjectWrite(n.Target)
		case *ast.IncDec:
			c.constObjectWrite(n.Expr)
		case *ast.Unset:
			for _, e := range n.Vars {
				c.constObjectWrite(e)
			}
		}
		return true
	})
}

// rejectedClosure says whether PHP's compiler rejects the closure a call
// with these arguments creates, as partialSyntax reports it (P1003): from
// `new` or a `?->` call, with a variadic placeholder that is not the last
// argument, or beside unpacking. PHP stops there, so a name that would
// overwrite an argument in it is not reported as well.
func rejectedClosure(call ast.Expr, args *ast.ArgList) bool {
	if !args.Partial() {
		return false
	}
	switch n := call.(type) {
	case *ast.New:
		return true
	case *ast.MethodCall:
		if n.NullSafe {
			return true
		}
	}
	for i, a := range args.Args {
		if a.Unpack || (a.Placeholder != nil && a.Placeholder.Variadic && i != len(args.Args)-1) {
			return true
		}
	}
	return false
}

func (c *Checker) argListSyntax(a *ast.ArgList) {
	if !a.Partial() {
		return
	}
	variadicAt, unpack, named := -1, false, false
	for i, arg := range a.Args {
		switch {
		case arg.Unpack:
			unpack = true
		case arg.Placeholder != nil && arg.Placeholder.Variadic:
			if variadicAt >= 0 {
				c.report(arg.S, P1003, "the variadic placeholder may only appear once")
				return
			}
			variadicAt = i
			continue
		case arg.Name != nil:
			named = true
		case named:
			c.report(arg.S, P2110, "positional argument after a named argument")
		}
		if variadicAt >= 0 {
			c.report(a.Args[variadicAt].S, P1003, "the variadic placeholder must be the last argument")
			return
		}
	}
	if unpack {
		c.report(a.S, P1003, "cannot combine partial application and unpacking")
	}
}

// constObjectWrite reports, for a target before 8.6, a write whose object is
// a constant: `C->x = 1`, `C[0]->x++`, `K::O->x .= …`.
func (c *Checker) constObjectWrite(target ast.Expr) {
	pf, ok := target.(*ast.PropertyFetch)
	if !ok {
		// `C->list[] = 1` writes through the property too.
		for {
			ix, isIndex := target.(*ast.Index)
			if !isIndex {
				break
			}
			target = ix.Array
		}
		if pf, ok = target.(*ast.PropertyFetch); !ok {
			return
		}
	}
	obj := pf.Object
	for {
		if ix, isIndex := obj.(*ast.Index); isIndex {
			obj = ix.Array
			continue
		}
		if inner, isProp := obj.(*ast.PropertyFetch); isProp {
			obj = inner.Object
			continue
		}
		break
	}
	switch obj.(type) {
	case *ast.Name, *ast.ConstFetch, *ast.ClassConstFetch:
		c.needsPHP(target.Span(), phpversion.PHP86, "writing a property through a constant", "")
	}
}
