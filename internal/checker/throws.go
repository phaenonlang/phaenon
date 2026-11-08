package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Throw analysis (design/tooling.md §9.1): every place that may throw is a
// throw point, collected per try block and per function body. A point is
// explicit when its exceptions are known — a throw, a call whose callee
// declares them — and implicit when it may throw any Throwable: PHP has no
// checked exceptions, so a callee that declares nothing may throw anything.
// The rules are PHPStan 2.2's, so the checker is never more lenient than
// PHPStan where PHPStan is precise; where the checker knows less (PHPStan's
// stubs and throw-type extensions for native functions) a point is
// implicit, which is lenient, never stricter.
//
// Three findings read the points: a catch no point reaches (P2403), a
// throws member the body never throws (P2404), and `@throws void` on a
// body that throws (P2405).

// ThrowPoint is a throw point as Options.Thrown reports it: the classes
// it may raise (less what earlier catches took; "Throwable" for any), and
// whether the exceptions are known (explicit) and come from a callee's
// declaration rather than the body's own throw (call).
type ThrowPoint struct {
	Classes  []string
	Explicit bool
	Call     bool
	At       ast.Span
}

// exported is points as Options.Thrown reports them.
func exported(points []throwPoint) []ThrowPoint {
	out := make([]ThrowPoint, 0, len(points))
	for _, tp := range points {
		e := ThrowPoint{Explicit: tp.explicit, Call: tp.anyThrowable, At: tp.at}
		for _, x := range tp.excs {
			e.Classes = append(e.Classes, x.name)
		}
		out = append(out, e)
	}
	return out
}

// exc is one exception class a throw point may raise, less the classes an
// earlier catch took from it ("Throwable" stands for any exception).
type exc struct {
	name  string
	minus []string
}

// throwPoint is one place that may throw.
type throwPoint struct {
	excs     []exc
	explicit bool
	// anyThrowable: a callee's declaration says what it throws, but not
	// that it throws nothing else, so a catch of Throwable still reaches
	// the point after the earlier catches took every declared class.
	anyThrowable bool
	// at is where an explicit point throws: P2405 points there.
	at ast.Span
}

var throwable = []exc{{name: "Throwable"}}

func isThrowableName(name string) bool {
	return strings.EqualFold(strings.TrimPrefix(name, "\\"), "Throwable")
}

// pushThrows starts collecting the throw points of a try block or a body.
func (x *fnCtx) pushThrows() {
	x.throwFrames = append(x.throwFrames, nil)
}

// popThrows ends the collection pushThrows started and returns its points.
func (x *fnCtx) popThrows() []throwPoint {
	n := len(x.throwFrames)
	points := x.throwFrames[n-1]
	x.throwFrames = x.throwFrames[:n-1]
	return points
}

func (x *fnCtx) addThrows(points ...throwPoint) {
	if n := len(x.throwFrames); n > 0 {
		x.throwFrames[n-1] = append(x.throwFrames[n-1], points...)
	}
}

// implicitThrow records a point that may throw anything.
func (x *fnCtx) implicitThrow() {
	x.addThrows(throwPoint{excs: throwable, anyThrowable: true})
}

// explicitThrow records a point at span that throws the classes of t.
func (x *fnCtx) explicitThrow(t types.Type, anyThrowable bool, at ast.Span) {
	x.addThrows(throwPoint{excs: excsOf(t), explicit: true, anyThrowable: anyThrowable, at: at})
}

// excsOf lists the exception classes of a thrown or declared type. What
// names no class (mixed, object, an intersection) may be any Throwable.
func excsOf(t types.Type) []exc {
	var out []exc
	for _, m := range types.Members(t) {
		switch m := m.(type) {
		case types.Class:
			out = append(out, exc{name: m.Name})
		case types.Static:
			if m.Of != nil {
				out = append(out, exc{name: m.Of.Name})
			}
		case types.This:
			if m.Of != nil {
				out = append(out, exc{name: m.Of.Name})
			}
		case types.Param:
			if m.Bound == nil {
				return throwable
			}
			out = append(out, excsOf(m.Bound)...)
		case types.Never, types.Void:
		default:
			return throwable
		}
	}
	if len(out) == 0 {
		return throwable
	}
	return out
}

// isVoidThrows says a declaration is `@throws void`: it throws nothing.
func isVoidThrows(t types.Type) bool {
	_, ok := t.(types.Void)
	return ok
}

// excOverlap reports whether one exception can be of both classes: one
// extends the other, or one is an interface the other, not final, may
// have a subclass implementing (two interfaces always can).
func (x *fnCtx) excOverlap(a, b string) bool {
	if isThrowableName(a) || isThrowableName(b) {
		return true
	}
	p := x.p()
	if p.IsSubclassOf(a, b) || p.IsSubclassOf(b, a) {
		return true
	}
	ca, cb := p.Class(a), p.Class(b)
	if ca == nil || cb == nil {
		return true // an unknown class is reported where it is named
	}
	ia, ib := ca.Kind == ast.KindInterface, cb.Kind == ast.KindInterface
	switch {
	case ia && ib:
		return true
	case ia:
		return !finalByKeyword(cb)
	case ib:
		return !finalByKeyword(ca)
	}
	return false // two classes on different branches
}

func finalByKeyword(c *program.Class) bool {
	return c.Decl != nil && c.Decl.Modifiers&ast.ModFinal != 0
}

// subOfAny reports a class that one of names covers.
func (x *fnCtx) subOfAny(name string, names []string) bool {
	for _, n := range names {
		if x.p().IsSubclassOf(name, n) {
			return true
		}
	}
	return false
}

// reaches reports whether a catch of class c can catch something the point
// may throw.
func (x *fnCtx) reaches(c string, tp throwPoint) bool {
	for _, e := range tp.excs {
		if x.subOfAny(c, e.minus) {
			continue
		}
		if x.excOverlap(c, e.name) {
			return true
		}
	}
	return false
}

// subtractCaught takes what a catch of class c catches from the point; ok is
// false when nothing is left.
func (x *fnCtx) subtractCaught(tp throwPoint, c string) (throwPoint, bool) {
	var left []exc
	for _, e := range tp.excs {
		switch {
		case x.p().IsSubclassOf(e.name, c) || isThrowableName(c):
			// caught whole
		case x.p().IsSubclassOf(c, e.name) || isThrowableName(e.name):
			left = append(left, exc{name: e.name, minus: append(append([]string{}, e.minus...), c)})
		default:
			left = append(left, e)
		}
	}
	tp.excs = left
	return tp, len(left) > 0
}

// catchClause matches one catch against the points the earlier catches
// left (PHPStan's NodeScopeResolver, TryCatch): it reports each of its
// types no point reaches and returns the points it leaves. caught are the
// classes of the earlier catches; a type they cover was reported as
// already caught. original are the try block's points before any catch.
func (x *fnCtx) catchClause(names []string, spans []ast.Span, points, original []throwPoint, caught []string) []throwPoint {
	matched := make([]bool, len(names))
	any := false
	for i, n := range names {
		if n == "" {
			matched[i] = true // unknown or not a Throwable: reported already
			continue
		}
		if x.subOfAny(n, caught) {
			matched[i] = true // already caught: P2403 says so
			continue
		}
		if isThrowableName(n) {
			if len(points) > 0 {
				matched[i], any = true, true
				continue
			}
			for _, tp := range original {
				if tp.anyThrowable {
					matched[i], any = true, true
					break
				}
			}
			continue
		}
		for _, tp := range points {
			if x.reaches(n, tp) {
				matched[i], any = true, true
				break
			}
		}
	}
	for i, ok := range matched {
		if !ok {
			x.report(spans[i], P2403, "%s is never thrown in the try block", names[i])
		}
	}
	if !any {
		return points
	}
	var left []throwPoint
	for _, tp := range points {
		keep := true
		for _, n := range names {
			if n == "" {
				continue
			}
			if tp, keep = x.subtractCaught(tp, n); !keep {
				break
			}
		}
		if keep {
			left = append(left, tp)
		}
	}
	return left
}

// recordCall records the throw point of a call whose callee is known
// (tooling.md §9.1): the callee's declared exceptions, or an implicit
// point when it declares none. ret is the call's type.
func (x *fnCtx) recordCall(sig *signature, args *ast.ArgList, span ast.Span, ret types.Type) {
	if x.resolvedCalls == nil {
		x.resolvedCalls = map[ast.Span]bool{}
	}
	x.resolvedCalls[span] = true
	var declared types.Type
	isFunc, ctor := false, false
	switch {
	case sig.meth != nil:
		declared = sig.meth.DeclaredThrows()
		ctor = strings.EqualFold(sig.meth.Name, "__construct")
	case sig.fn != nil:
		declared = sig.fn.Throws
		isFunc = true
	default:
		x.implicitThrow() // a callable whose declaration is unknown
		return
	}
	for _, p := range sig.params {
		if expectsCallable(p.Type) {
			// PHPStan adds the throw points of a callable a function calls
			// at once; the checker does not follow callables, and an
			// implicit point is the lenient side of that.
			x.implicitThrow()
			return
		}
	}
	if declared != nil {
		if !isVoidThrows(declared) {
			x.explicitThrow(declared, true, span)
		}
		return
	}
	if _, ok := sig.ret.(types.Never); ok && !ctor {
		x.explicitThrow(types.NewClass("Throwable"), true, span)
		return
	}
	if isFunc && sig.stub && argCount(args) == 0 && requiredParams(sig.params) == 0 {
		return // time(), microtime(): PHPStan counts them as not throwing
	}
	if sig.node != nil && sig.node == x.throwOperand {
		// `throw new NotFound($id)`, `throw $this->error()`: what makes the
		// thrown object is not counted as throwing.
		if ctor && x.p().IsSubclassOf(sig.meth.Class.Name, "Throwable") {
			return
		}
		if !ctor && ret != nil && !types.IsMixed(ret) && types.IsSubtype(ret, types.NewClass("Throwable"), x.p()) {
			return
		}
	}
	x.implicitThrow()
}

func argCount(args *ast.ArgList) int {
	if args == nil {
		return 0
	}
	return len(args.Args)
}

func requiredParams(ps []*program.Param) int {
	n := 0
	for _, p := range ps {
		if !p.HasDefault && !p.Variadic {
			n++
		}
	}
	return n
}

// unresolvedCall gives a call whose callee the checker did not resolve an
// implicit throw point; a first-class callable calls nothing.
func (x *fnCtx) unresolvedCall(span ast.Span, args *ast.ArgList) {
	if args != nil && args.FirstClassCallable {
		return
	}
	if !x.resolvedCalls[span] {
		x.implicitThrow()
	}
}

// propertyThrows records the throw point of reading or writing a
// property: none when no hook can run, that is when the property is not
// hooked and is private or its class is final; otherwise, a subclass may
// hook it, so the point is implicit (PHPStan's
// getThrowPointsFromPropertyHook). A property the checker cannot find may
// be served by __get or __set.
func (x *fnCtx) propertyThrows(obj types.Type, name string) {
	names := types.ClassNames(obj)
	if len(names) == 0 {
		x.implicitThrow()
		return
	}
	for _, cn := range names {
		cl := x.p().Class(cn)
		if cl == nil {
			x.implicitThrow()
			return
		}
		cl.Signature()
		prs, lacking, dynamic := x.classProperties(cl, name)
		if len(prs) == 0 || lacking != nil || dynamic {
			x.implicitThrow()
			return
		}
		for _, pr := range prs {
			if pr.Magic || len(pr.Hooks) > 0 || pr.Vis != program.Private && (pr.Class == nil || !pr.Class.Final) {
				x.implicitThrow()
				return
			}
		}
	}
}

// mayBeObject says whether converting, iterating or indexing a value of
// type t may call a method of an object (__toString, getIterator,
// offsetGet): the point is then implicit.
func mayBeObject(t types.Type) bool {
	for _, m := range types.Members(t) {
		if types.IsTextBytes(m) {
			continue // string&bytes: a string
		}
		switch m.(type) {
		case types.Array, types.Shape, types.String, types.StringLit, types.ClassString, types.Bytes,
			types.Int, types.Float, types.FloatLit, types.Bool, types.BoolLit, types.Null, types.Never, types.Void, types.EnumCase:
		default:
			return true
		}
	}
	return false
}

// mayBeZero says whether a divisor may be zero: anything but an integer or
// float that excludes it.
func mayBeZero(t types.Type) bool {
	for _, m := range types.Members(t) {
		switch m := m.(type) {
		case types.Int:
			if m.Lo <= 0 && 0 <= m.Hi {
				return true
			}
		case types.FloatLit:
			if m.V == 0 {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// bodyThrows checks a body's escaping throw points against its own
// declaration: P2405 at each explicit point under `@throws void`, where
// PHPStan reports it, and P2404 at the declaration for a member nothing
// throws. overridable methods keep their members: their
// declaration is the contract their overrides are held to.
func (x *fnCtx) bodyThrows(declared types.Type, overridable bool, what string, at ast.Span, points []throwPoint) {
	if declared == nil {
		return
	}
	if isVoidThrows(declared) {
		for _, tp := range points {
			if !tp.explicit {
				continue
			}
			for _, e := range tp.excs {
				x.report(tp.at, P2405, "%s declares @throws void but throws %s here", what, e.name)
			}
		}
		return
	}
	if overridable {
		return
	}
	for _, m := range x.declaredExcs(declared) {
		used := false
		for _, tp := range points {
			if x.reaches(m.name, tp) {
				used = true
				break
			}
		}
		if !used {
			x.report(at, P2404, "%s declares it throws %s, which its body never throws", what, m.name)
		}
	}
}

// markResolved says a call's throw point is decided without a point.
func (x *fnCtx) markResolved(span ast.Span) {
	if x.resolvedCalls == nil {
		x.resolvedCalls = map[ast.Span]bool{}
	}
	x.resolvedCalls[span] = true
}

// unparen is e without its parentheses.
func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.Paren)
		if !ok {
			return e
		}
		e = p.Expr
	}
}

// declaredExcs lists a declaration's members that name Throwables: one
// that does not is P2319 or P2001 already.
func (x *fnCtx) declaredExcs(t types.Type) []exc {
	var out []exc
	for _, m := range types.Members(t) {
		if p, ok := m.(types.Param); ok && (p.Bound == nil || !types.IsSubtype(p.Bound, types.NewClass("Throwable"), x.p())) {
			continue
		}
		for _, e := range excsOf(m) {
			if isThrowableName(e.name) || x.p().Class(e.name) != nil && x.p().IsSubclassOf(e.name, "Throwable") {
				out = append(out, e)
			}
		}
	}
	return out
}
