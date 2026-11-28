package checker

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// stmts checks a statement list in sequence.
func (x *fnCtx) stmts(ss []ast.Stmt, env *state) {
	reported := false
	// `if (PHP_VERSION_ID < 80600) { return …; }` guards the statements
	// after it, to the end of the list.
	depth := len(x.c.guards)
	defer func() { x.c.guards = x.c.guards[:depth] }()
	for _, s := range ss {
		if env.dead && !reported {
			switch s.(type) {
			case *ast.Nop, *ast.Label, *ast.ClassDecl, *ast.FunctionDecl, *ast.Use, *ast.Declare, *ast.CloseTag, *ast.InlineHTML:
			default:
				x.report(s.Span(), P2401, "unreachable code")
				reported = true
			}
		}
		x.stmt(s, env)
		if n := len(x.throwEnvs); n > 0 && !env.dead {
			x.throwEnvs[n-1] = join(x.throwEnvs[n-1], env)
		}
		if is, ok := s.(*ast.If); ok && len(is.ElseIfs) == 0 && is.Else == nil && alwaysExits(is.Then) {
			_, negative := x.guardOf(is.Cond)
			x.c.pushGuard(negative) // popped when the list ends
		}
	}
}

// stmt checks one statement.
func (x *fnCtx) stmt(s ast.Stmt, env *state) {
	if at, ok := x.outEnds[s]; ok && !env.dead {
		defer x.outsAtEnd(s, at, env, len(x.returnEnvs))
	}
	if d := statementDoc(s); d != "" {
		// `/** @var array<string, mixed> $data */` before an if, a loop or a
		// try retypes the variable from there on, as PHPStan reads it.
		x.varTags(docblock.Parse(d), env, "")
	}
	switch s := s.(type) {
	case *ast.ExprStmt:
		alive := !env.dead
		x.exprStmt(s, env)
		if _, throws := unparen(s.Expr).(*ast.Throw); alive && env.dead && x.trackNever && !(throws && x.inTry > 0) {
			// A statement of type never, a throw, an exit or a call of a
			// never function, is an exit of the closure (refExits). A
			// throw statement in a try statement's body or catch blocks is
			// none, whether a catch takes it or not, as PHPStan's try
			// statement drops it from its exit points; an exit or a call
			// of a never function there stays one.
			exit := env.clone()
			exit.dead = false
			x.neverEnvs = append(x.neverEnvs, exit)
		}
	case *ast.VoidCastStmt:
		x.expr(s.Expr, env, nil)
	case *ast.Echo:
		for _, e := range s.Exprs {
			x.toString(e, env)
		}
	case *ast.Block:
		x.stmts(s.Stmts, env)
	case *ast.TypeAlias:
		x.checkTypeAlias(s)
	case *ast.Nop, *ast.InlineHTML, *ast.OpenTag, *ast.CloseTag, *ast.Use:
	case *ast.If:
		x.ifStmt(s, env)
	case *ast.While:
		x.whileStmt(s, env)
	case *ast.DoWhile:
		x.doWhile(s, env)
	case *ast.For:
		x.forStmt(s, env)
	case *ast.Foreach:
		x.foreach(s, env)
	case *ast.Switch:
		x.switchStmt(s, env)
	case *ast.Break:
		if s.Levels != nil {
			x.expr(s.Levels, env, nil)
		}
		x.jump(env, s.Levels, false)
	case *ast.Continue:
		if s.Levels != nil {
			x.expr(s.Levels, env, nil)
		}
		x.jump(env, s.Levels, true)
	case *ast.Return:
		x.returnStmt(s, env)
	case *ast.Global:
		x.removed(s.S, "global", "explicit parameters or a service")
		for _, v := range s.Vars {
			if vv, ok := v.(*ast.Variable); ok {
				env.set(vv.Name, types.TMixed)
				x.byRef[vv.Name] = true
				x.handedOver(vv, env)
			}
		}
	case *ast.StaticVars:
		var doc *docblock.Doc
		if s.Doc != "" {
			doc = docblock.Parse(s.Doc)
		}
		for _, v := range s.Vars {
			// The value outlives the call: the initialiser is only what the
			// first call starts from, so without a `@var` (the declaration
			// PHP leaves a static variable) it is mixed, as PHPStan has it.
			// The initialiser is code that runs, since PHP 8.3, and no
			// constant expression: a closure in it may capture and use
			// $this.
			t := types.TMixed
			if v.Default != nil {
				x.expr(v.Default, env, nil)
			}
			if doc != nil {
				for _, vt := range doc.Vars {
					if vt.Name == "" || vt.Name == v.Var.Name {
						t = x.res.Doc(vt.Type)
					}
				}
			}
			env.set(v.Var.Name, t)
			x.byRef[v.Var.Name] = true
			x.handedOver(v.Var, env)
		}
	case *ast.Unset:
		for _, v := range s.Vars {
			switch t := v.(type) {
			case *ast.Variable:
				x.variable(t, env, true)
				env.unset(t.Name)
				x.handedOver(t, env)
			case *ast.Index:
				base := x.exprLenient(t.Array, env)
				x.forgetOffset(t, env)
				var raw types.Type
				if t.Index != nil {
					raw = x.expr(t.Index, env, nil)
				}
				x.unsetOffsetCheck(t, base, raw, env)
				x.unsetIndex(t, base, env, s.S)
			default:
				x.exprLenient(v, env)
				if key, ok := x.pathKey(v); ok {
					env.invalidatePrefix(key)
				}
			}
		}
	case *ast.Try:
		x.tryStmt(s, env)
	case *ast.Goto:
		x.removed(s.S, "goto", "structured control flow")
	case *ast.Label:
		if !x.c.labelTargeted(x.file, s) {
			x.removed(s.S, "labels", "structured control flow")
		}
	case *ast.Declare:
		if s.Body != nil {
			x.stmt(s.Body, env)
		}
		if s.Alt {
			x.removed(s.S, "the alternative declare syntax", "braces")
		}
	case *ast.HaltCompiler:
		env.dead = true
	case *ast.Namespace:
		x.stmts(s.Body, env)
	case *ast.ClassDecl:
		cl := x.p().DeclareAnonymous(x.file, x.res.Scope(), s)
		x.c.checkClass(cl)
	case *ast.FunctionDecl:
		fn := x.p().DeclareFunction(x.file, x.res.Scope(), s)
		x.c.checkFunction(x.file, fn)
	case *ast.ConstDecl:
		for _, k := range s.Consts {
			x.expr(k.Value, env, nil)
		}
	}
}

// exprStmt checks an expression statement, honouring an inline `@var`.
func (x *fnCtx) exprStmt(s *ast.ExprStmt, env *state) {
	x.stmtExpr = s.Expr
	// self::assertNotNull($x) and other callees with @phpstan-assert tags.
	if s.Doc == "" && x.applyCallAsserts(s.Expr, env) {
		return
	}
	// assert($p) narrows what follows.
	if call, ok := s.Expr.(*ast.Call); ok {
		if n, isName := call.Callee.(*ast.Name); isName && len(n.Parts) == 1 && strings.EqualFold(n.Parts[0], "assert") &&
			call.Args != nil && len(call.Args.Args) >= 1 && call.Args.Args[0].Value != nil && !call.Args.FirstClassCallable {
			then, _ := x.cond(call.Args.Args[0].Value, env)
			*env = *then
			return
		}
	}
	if s.Doc == "" {
		x.expr(s.Expr, env, nil)
		return
	}
	doc := docblock.Parse(s.Doc)
	assign, isAssign := s.Expr.(*ast.Assign)
	var v *ast.Variable
	isVar := false
	if isAssign {
		v, isVar = assign.Target.(*ast.Variable)
	}
	var declared types.Type
	target := ""
	if isVar {
		target = v.Name
	}
	for _, vt := range doc.Vars {
		if isAssign && (vt.Name == "" || vt.Name == target) {
			declared = x.res.Doc(vt.Type)
		}
	}
	x.varTags(doc, env, target)
	if declared == nil {
		x.expr(s.Expr, env, nil)
		return
	}
	vt := x.expr(assign.Value, env, declared)
	// A literal value must fit the tag; any other value's PHP type must be
	// narrowed by it, never widened or contradicted (PHPStan's
	// varTag.nativeType), unless the tag narrows a refinement that leaves
	// PHP's type (refinesOutside).
	native := x.nativeOf(assign.Value, env)
	if native == nil {
		native = vt
	}
	switch {
	case isLiteralExpr(assign.Value):
		if !types.IsAcceptable(vt, declared, x.p()) {
			x.report(s.S, P2212, "@var %s does not accept the assigned %s", x.typeName(declared), x.typeName(vt))
		}
	case !types.IsMixed(native) && !types.IsSubtype(declared, withoutTypeArgs(native), x.p()) && !refinesOutside(declared, vt, native, x.p()):
		x.report(s.S, P2212, "@var %s is not a subtype of the assigned %s", x.typeName(declared), x.typeName(native))
	}
	x.bind(nil, assign.Target, declared, env, s.S)
	if isVar {
		// The variable holds what PHP guarantees of the value, which for a
		// declaration PHP gives no type is nothing.
		if g := x.guaranteedOf(assign.Value, env); g != nil {
			native = g
		}
		env.setNative(v.Name, native)
	}
}

// refinesOutside reports a @var tag that narrows a value's refined type
// where that refinement itself leaves PHP's type: `@var bytes` over
// random_bytes(), bytes to the overlay and a string to PHP, which the
// lattice keeps apart. Such a tag narrows PHP's type as well. A refined
// type of mixed says nothing and excuses nothing.
func refinesOutside(declared, refined, native types.Type, h types.Hierarchy) bool {
	return !types.IsMixed(refined) && types.IsSubtype(declared, refined, h) && !types.IsSubtype(refined, withoutTypeArgs(native), h)
}

// varTags applies the named `@var T $x` tags of a docblock above a
// statement to the variables they name, as PHPStan does: $x is retyped for
// what follows, its PHP type staying what it was. The variable the
// statement assigns (target) is handled by the caller.
func (x *fnCtx) varTags(doc *docblock.Doc, env *state, target string) {
	for _, vt := range doc.Vars {
		if vt.Name == "" || vt.Name == target {
			continue
		}
		v := &ast.Variable{Name: vt.Name}
		if info, defined := env.get(vt.Name); defined {
			native := info.native
			if native == nil {
				native = info.t // what PHP knew stays the PHP type
			}
			// The tag retypes the variable, as an assignment would: it
			// applies none of the conditions about it, and they are gone.
			env.written(vt.Name)
			x.narrowPathNative(env, v, x.res.Doc(vt.Type), native, false)
		}
	}
}

// paramsToBounds replaces every type parameter in t by its bound (mixed
// when it has none).
func paramsToBounds(t types.Type) types.Type {
	return types.MapDeep(t, func(m types.Type) types.Type {
		if p, ok := m.(types.Param); ok {
			if p.Bound != nil {
				return p.Bound
			}
			return types.TMixed
		}
		return m
	})
}

// statementDoc is the doc comment before a statement that carries no
// assignment (the assignment kinds read theirs themselves).
func statementDoc(s ast.Stmt) string {
	switch s := s.(type) {
	case *ast.If:
		return s.Doc
	case *ast.While:
		return s.Doc
	case *ast.DoWhile:
		return s.Doc
	case *ast.For:
		return s.Doc
	case *ast.Switch:
		return s.Doc
	case *ast.Try:
		return s.Doc
	case *ast.Echo:
		return s.Doc
	case *ast.Unset:
		return s.Doc
	}
	return ""
}

// withoutTypeArgs is the PHP type behind t: a class without its type
// arguments. A @var tag is checked against what PHP knows, so `@var
// Meta<object>` may widen an inferred Meta<Dog> (PHPStan's varTag.nativeType).
func withoutTypeArgs(t types.Type) types.Type {
	// bytes is a type of its own, not a refinement of string a docblock may
	// narrow to: `@var bytes $out` over a bytes value is no contradiction,
	// over a string it is (the (bytes) cast retypes, strings.md §2).
	return types.Map(t, func(m types.Type) types.Type {
		if _, ok := m.(types.Bytes); ok || types.IsTextBytes(m) {
			return m
		}
		return types.Erase(m)
	})
}

// isLiteralExpr reports whether e is written out in full — a literal, a
// constant, a `new` — so that a `@var` above it is checked for acceptance
// rather than narrowing.
func isLiteralExpr(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Paren:
		return isLiteralExpr(e.Expr)
	case *ast.ArrayLit, *ast.IntLit, *ast.FloatLit, *ast.StringLit, *ast.ConstFetch, *ast.ClassConstFetch, *ast.New:
		return true
	case *ast.Unary:
		// `-1`, the default of parse_url's $component.
		switch e.Expr.(type) {
		case *ast.IntLit, *ast.FloatLit:
			return e.Op == token.Kind('-') || e.Op == token.Kind('+')
		}
	}
	return false
}

// unsetOffsetCheck reports unset() of an offset on a value that has none,
// as PHPStan does: mixed, object, iterable, an open type parameter or an
// object that is no ArrayAccess may refuse it (its
// offsetAccess.nonOffsetAccessible), and on a scalar, a string or null PHP
// throws (unset.offset). A union with an array in it is let through.
func (x *fnCtx) unsetOffsetCheck(t *ast.Index, base, raw types.Type, env *state) {
	if _, isMixed := base.(types.Mixed); isMixed {
		x.reportNoOffsets(t, base, raw, env)
		return
	}
	members := types.Members(base)
	scalarOnly := len(members) > 0
	for _, m := range members {
		switch m := m.(type) {
		case types.Object, types.Iterable:
			x.reportNoOffsets(t, m, raw, env)
			return
		case types.Class, types.Static, types.This, types.Intersection, types.Param:
			if p, isParam := m.(types.Param); isParam && openParam(p) {
				x.reportNoOffsets(t, m, raw, env)
				return
			}
			if classes := x.classesOf(m, t.Array.Span()); len(classes) > 0 && !x.arrayAccess(m, t.Array.Span()) {
				x.report(t.Array.Span(), P2302, "cannot use %s as an array", classTypeName(x, m, classes))
				return
			}
			scalarOnly = false
		case types.Int, types.Float, types.FloatLit, types.Bool, types.BoolLit, types.Null, types.String, types.StringLit, types.ClassString:
		default:
			scalarOnly = false
		}
	}
	if scalarOnly {
		what := "an offset"
		if raw != nil {
			what = "offset " + x.typeName(raw)
		}
		x.report(t.Array.Span(), P2302, "cannot unset %s on %s", what, x.typeName(base))
		return
	}
	// A value of `T of array{…}` is held to its bound's keys, as PHPStan
	// holds it.
	if bounded, _ := writtenBound(base); raw != nil && unsetMissing(bounded, normaliseKey(raw)) {
		x.report(t.Index.Span(), P2313, "offset %s does not exist on %s, so unset cannot remove it", x.typeName(raw), x.typeName(bounded))
	}
}

// unsetIndex types the array `unset($a[$k])` removes an element from, as
// PHPStan does (design/collections.md §2.2, unsetOffset): a variable takes
// the array's new type, and so does a property, which is held to its
// declared type as an assignment is (P2201: a `list<T>` property does not
// accept the list an element left). An array inside another, `$a[$i][$k]`,
// is written back into the one holding it (writeExisting), so the variable
// or property at the root takes its new type and is checked the same way.
func (x *fnCtx) unsetIndex(t *ast.Index, base types.Type, env *state, span ast.Span) {
	if key, ok := pathKey(t); ok {
		env.invalidatePrefix(key)
	}
	if t.Index == nil {
		return
	}
	keyT := normaliseKey(x.pathType(t.Index, env))
	// The shapes are bounded first, as a write bounds them
	// (boundedShapes): unset() in a chain of if/else adds them up as
	// writes do.
	base = boundedShapes(base)
	switch a := t.Array.(type) {
	case *ast.Variable:
		if info, defined := env.get(a.Name); defined {
			// A value of `T of array<…>` is its bound without the entry,
			// no longer T (assignIndex).
			bounded, p := writtenBound(info.t)
			now := unsetOffset(boundedShapes(bounded), keyT)
			env.set(a.Name, now)
			x.keepRewritten(a, info.rewritten, p, t.S, now, env)
			x.wroteOut(a.Name, now, env, span)
		}
	case *ast.PropertyFetch, *ast.StaticPropertyFetch, *ast.Index:
		// Removing an element writes the property that holds the array,
		// whether or not its type changes: a readonly property refuses it
		// as it refuses an assignment. An object's offsetUnset() changes no
		// property, so a property holding none but objects is not written.
		// A value of `T of array<…>` is written as its bound.
		if bounded, _ := writtenBound(base); holdsArray(bounded) {
			x.writeExisting(t.Array, unsetOffset(bounded, keyT), env, span)
		}
	}
}

// iteratedBounds is t with each type parameter with a bound, narrowed or
// not, read as the members of its bound a foreach takes (boundMembers):
// what a foreach over it yields. The null of `T of array<string, int>|null`
// is left out, as a test may have, which the type of T does not show; a
// parameter without a bound, or bounded by mixed or object, stays as it
// is.
func iteratedBounds(t types.Type, h types.Hierarchy) types.Type {
	var out []types.Type
	for _, w := range boundMembers(t) {
		if _, _, ok := types.IterableKV(w.member, h); ok || w.from == nil {
			out = append(out, w.member)
		}
	}
	if len(out) == 0 {
		return t
	}
	return types.MakeUnion(out...)
}

// offsetRootVar is the variable an expression is or holds an offset of,
// `$a` of `$a['inner'][0]`, parentheses aside; nil for any other
// expression and for $this.
func offsetRootVar(e ast.Expr) *ast.Variable {
	for {
		switch v := unparen(e).(type) {
		case *ast.Index:
			e = v.Array
		case *ast.Variable:
			if v.Name == "this" {
				return nil
			}
			return v
		default:
			return nil
		}
	}
}

// writtenBound is t with each type parameter whose bound holds an array,
// narrowed or not, read as its bound, as a write reads it (boundMembers),
// and the first such parameter; t and nil where none is.
func writtenBound(t types.Type) (types.Type, *types.Param) {
	var p *types.Param
	var out []types.Type
	for _, m := range types.Members(t) {
		read := boundMembers(m)
		if len(read) > 0 && read[0].from != nil {
			bound := types.TNever
			for _, w := range read {
				bound = types.MakeUnion(bound, w.member)
			}
			if holdsArray(bound) {
				p = firstParam(p, read[0].from)
				out = append(out, bound)
				continue
			}
		}
		out = append(out, m)
	}
	if p == nil {
		return t, nil
	}
	return types.MakeUnion(out...), p
}

// writeExisting gives the array at target, which holds the offset unset()
// removed, its new type vt, as PHPStan's assignment of the unset to the
// offsets the statement names, which exist (ExistingArrayDimFetch), does:
// each array up the chain keeps its keys, its list-ness and whether it is
// non-empty, and the value at the offset is replaced (withExistingOffset).
// A variable at the root takes its new type; a property is assigned, and
// held to its declared type and to readonly as an assignment is. An
// object's offsetGet() hands over a value no property holds: nothing above
// it is written.
func (x *fnCtx) writeExisting(target ast.Expr, vt types.Type, env *state, span ast.Span) {
	switch t := target.(type) {
	case *ast.Paren:
		x.writeExisting(t.Expr, vt, env, span)
	case *ast.Variable:
		if _, defined := env.get(t.Name); defined && t.Name != "this" {
			env.set(t.Name, vt)
			x.wroteOut(t.Name, vt, env, span)
		}
	case *ast.PropertyFetch, *ast.StaticPropertyFetch:
		x.assignTo(t, vt, env, span)
	case *ast.Index:
		if t.Index == nil {
			return
		}
		key, keyed := pathKey(t)
		if keyed {
			env.invalidatePrefix(key)
		}
		base, p := writtenBound(x.pathType(t.Array, env))
		if !holdsArray(base) {
			return
		}
		before := rewrittenOf(t.Array, env)
		now := withExistingOffset(base, normaliseKey(x.pathType(t.Index, env)), vt)
		x.writeExisting(t.Array, now, env, span)
		x.keepRewritten(t.Array, before, p, t.S, now, env)
		// What was known of this array, an is_array() of `$a['k']` that
		// a general array's type cannot hold, stays known of what is left
		// of it, as PHPStan keeps it through the write.
		if keyed {
			env.setPath(key, vt)
		}
	}
}

// withExistingOffset is t with the value at an offset it has replaced by
// vt (PHPStan's setExistingOffsetValueType): an array keeps its keys, its
// list-ness and whether it is non-empty, its value type widened by vt; a
// shape's entries the key may name take vt, and are certain.
func withExistingOffset(t, key, vt types.Type) types.Type {
	return types.Map(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Array:
			m.Value = types.MakeUnion(m.Value, vt)
			return m
		case types.Shape:
			entries := append([]types.ShapeEntry{}, m.Entries...)
			for i, e := range entries {
				if types.Overlaps(shapeEntryKey(e), key, nil) {
					entries[i].Type, entries[i].Optional = vt, false
				}
			}
			return types.Shape{Entries: entries, Unsealed: m.Unsealed}
		}
		return m
	})
}

// holdsArray reports a type with an array among its members.
func holdsArray(t types.Type) bool {
	for _, m := range types.Members(t) {
		switch m.(type) {
		case types.Array, types.Shape:
			return true
		}
	}
	return false
}

// unsetOffset is the type of an array once unset() removed the offset key
// from it, member by member, as PHPStan 2.3 has it: a list is a list no
// more, its keys those a list has (`array<int<0, max>, T>`); no array is
// still known to be non-empty; a shape loses the entry a constant key
// names and makes optional each entry another key may name, and is no
// list either way, even without its last entry. An offset a sealed shape
// lacks leaves it as it was (unsetMissing reports it).
func unsetOffset(t, key types.Type) types.Type {
	return types.Map(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Array:
			if m.List {
				m.List = false
				m.Key = types.Int{Lo: 0, Hi: math.MaxInt64}
			}
			m.NonEmpty = false
			return m
		case types.Shape:
			return unsetShapeOffset(m, key)
		}
		return m
	})
}

func unsetShapeOffset(s types.Shape, key types.Type) types.Type {
	if _, found, certain := shapeOffset(s, key); certain {
		if !found {
			return s
		}
		var entries []types.ShapeEntry
		for _, e := range s.Entries {
			if !shapeEntryIs(e, key) {
				entries = append(entries, e)
			}
		}
		return types.Shape{Entries: entries, Unsealed: s.Unsealed}
	}
	entries := append([]types.ShapeEntry{}, s.Entries...)
	changed := false
	for i, e := range entries {
		if types.Overlaps(shapeEntryKey(e), key, nil) {
			entries[i].Optional, changed = true, true
		}
	}
	if !changed {
		return s
	}
	return types.Shape{Entries: entries, Unsealed: s.Unsealed}
}

// shapeEntryIs reports the entry a constant key names.
func shapeEntryIs(e types.ShapeEntry, key types.Type) bool {
	switch k := key.(type) {
	case types.StringLit:
		return !e.IsInt && e.Key == k.V
	case types.Int:
		return e.IsInt && k.Lo == k.Hi && e.IntKey == k.Lo
	}
	return false
}

// unsetMissing reports an offset no member of the array has: each is a
// sealed shape, and the constant key names none of its entries. PHPStan
// refuses the unset (unset.offset), as a read of it is refused.
func unsetMissing(t, key types.Type) bool {
	members := types.Members(t)
	for _, m := range members {
		s, ok := m.(types.Shape)
		if !ok || s.Unsealed {
			return false
		}
		if _, found, certain := shapeOffset(s, key); found || !certain {
			return false
		}
	}
	return len(members) > 0
}

// ------------------------------------------------------------------ control flow

func (x *fnCtx) ifStmt(s *ast.If, env *state) {
	if s.Alt {
		x.removed(s.S, "the alternative if syntax", "braces")
	}
	x.outsPastIf(s, env)
	thenEnv, elseEnv := x.cond(s.Cond, env)
	positive, negative := x.guardOf(s.Cond)
	pop := x.c.pushGuard(positive)
	x.stmt(s.Then, thenEnv)
	pop()
	ends := []*state{thenEnv}
	cur := elseEnv
	negatives := []libraryGuard{negative}
	for _, ei := range s.ElseIfs {
		t, f := x.cond(ei.Cond, cur)
		p, n := x.guardOf(ei.Cond)
		pops := []func(){x.c.pushGuard(p)}
		for _, g := range negatives {
			pops = append(pops, x.c.pushGuard(g))
		}
		x.stmt(ei.Body, t)
		for i := len(pops) - 1; i >= 0; i-- {
			pops[i]()
		}
		negatives = append(negatives, n)
		ends = append(ends, t)
		cur = f
	}
	if s.Else != nil {
		var pops []func()
		for _, g := range negatives {
			pops = append(pops, x.c.pushGuard(g))
		}
		x.stmt(s.Else, cur)
		for i := len(pops) - 1; i >= 0; i-- {
			pops[i]()
		}
	}
	ends = append(ends, cur)
	*env = *joinAll(ends...)
}

// deepJump is a `break N` / `continue N` on its way out: levels counts the
// constructs it still leaves, the innermost one it is in included.
type deepJump struct {
	env    *state
	levels int
	cont   bool
}

// jumpLevels is the level count a break or continue names: 1 when absent
// (or not a literal, which PHP refuses).
func jumpLevels(levels ast.Expr) int {
	if lit, ok := levels.(*ast.IntLit); ok {
		if v, err := strconv.Atoi(strings.ReplaceAll(lit.Raw, "_", "")); err == nil && v > 0 {
			return v
		}
	}
	return 1
}

// jump records a break or continue and ends the path: `continue 2` in an
// inner loop is a back edge of the outer one, so its environment travels
// out until it reaches that loop.
func (x *fnCtx) jump(env *state, levels ast.Expr, cont bool) {
	switch n := jumpLevels(levels); {
	case n > 1:
		x.deepJumps = append(x.deepJumps, deepJump{env: env.clone(), levels: n, cont: cont})
	case cont:
		x.continueEnvs = append(x.continueEnvs, env.clone())
	default:
		x.breakEnvs = append(x.breakEnvs, env.clone())
	}
	env.dead = true
}

// unwind passes the deep jumps collected inside a loop or switch body one
// construct out: those whose target is the enclosing construct join its
// breaks or continues, the others keep travelling.
func (x *fnCtx) unwind(inner []deepJump, outerBreaks, outerContinues *[]*state) {
	for _, j := range inner {
		j.levels--
		switch {
		case j.levels > 1:
			x.deepJumps = append(x.deepJumps, j)
		case j.cont:
			*outerContinues = append(*outerContinues, j.env)
		default:
			*outerBreaks = append(*outerBreaks, j.env)
		}
	}
}

// fixpoint runs a loop body until the environment at the loop head is
// stable. body receives the head environment and returns the environment
// at the end of the body (before the back edge) and the exit environment
// (the condition being false).
func (x *fnCtx) fixpoint(entry *state, body func(head *state) (end, exit *state)) *state {
	savedBreaks, savedContinues, savedDeep := x.breakEnvs, x.continueEnvs, x.deepJumps
	defer func() { x.breakEnvs, x.continueEnvs = savedBreaks, savedContinues }()
	x.loopDepth++
	defer func() { x.loopDepth-- }()
	head := entry.clone()
	var exit, back *state
	var breaks []*state
	savedDiags := x.c.mark()
	for i := 0; i < 5; i++ {
		x.breakEnvs, x.continueEnvs, x.deepJumps = nil, nil, nil
		if i < 4 {
			// Diagnostics of non-final iterations are discarded: the
			// types are still converging.
			x.c.rollback(savedDiags)
		}
		end, ex := body(head.clone())
		exit = ex
		breaks = x.breakEnvs
		back = joinAll(append([]*state{end}, x.continueEnvs...)...)
		next := join(entry, back)
		if next.equal(head) {
			break
		}
		if i >= 1 {
			next.widen(head)
		}
		head = next
		if i == 4 {
			break
		}
	}
	inner := x.deepJumps
	x.deepJumps = savedDeep
	x.unwind(inner, &savedBreaks, &savedContinues)
	x.loopBack, x.loopBreaks = back, breaks
	return joinAll(append([]*state{exit}, breaks...)...)
}

// entersOnce reports whether a loop's conditions (a for's last one is its
// test; an empty for has none) hold where the loop starts, read from the
// types PHP declares: its body then runs at least once, as PHPStan reads
// a for or a while whose first test is always true with
// treatPhpDocTypesAsCertain off. The conditions are held to what both
// read alike: variables, constants, literals and the operators over them,
// and no variable a reference may have been taken to where the checker
// does not follow its writes. A call, a property or an offset decides
// nothing.
func (x *fnCtx) entersOnce(entry *state, conds []ast.Expr) bool {
	if len(conds) == 0 {
		return true // `for (;;)`
	}
	for _, c := range conds {
		if !x.plainCondition(c, entry) {
			return false
		}
	}
	native := nativeState(entry)
	saved := x.c.mark()
	for _, c := range conds[:len(conds)-1] {
		x.expr(c, native, nil)
	}
	x.loopCond = true
	_, f := x.cond(conds[len(conds)-1], native)
	x.loopCond = false
	x.c.rollback(saved)
	return f.dead
}

// nativeState is e as PHP declares it: each variable holds the type PHP
// guarantees of it (state.native), and no narrowed member path or
// condition a docblock's type may have drawn is kept.
func nativeState(e *state) *state {
	n := e.clone()
	for k, v := range n.vars {
		if v.native != nil {
			v.t, v.native = v.native, nil
		}
		v.conds = nil
		n.vars[k] = v
	}
	n.paths = map[string]types.Type{}
	return n
}

// plainCondition reports a loop condition entersOnce may decide on: one
// over literals, constants of a fixed value and variables whose type PHP
// guarantees, in env, the state the loop starts with.
func (x *fnCtx) plainCondition(e ast.Expr, env *state) bool {
	return x.overGuaranteed(e, env, false)
}

// plainValue reports a value PHP itself guarantees the type of, which
// makes the variable assigned it guaranteed: a variable that is, or a
// literal, a constant of a fixed value, or an operator over those and
// over variables whose type no docblock refines (native is nil), whose
// result the checker computes as PHP does.
func (x *fnCtx) plainValue(e ast.Expr, env *state) bool {
	if v, ok := unparen(e).(*ast.Variable); ok {
		return x.guaranteedVar(v.Name, env)
	}
	return x.overGuaranteed(e, env, true)
}

// overGuaranteed reports an expression of literals, constants of a fixed
// value and guaranteed variables, joined by operators; certain asks the
// variables to be ones no docblock refines either.
func (x *fnCtx) overGuaranteed(e ast.Expr, env *state, certain bool) bool {
	ok := true
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Variable:
			info, _ := env.get(n.Name)
			ok = ok && x.guaranteedVar(n.Name, env) && (!certain || info.native == nil)
		case *ast.ConstFetch:
			ok = ok && x.fixedConst(n.Name)
			return false
		case *ast.Name: // a constant, as the parser leaves one
			ok = ok && x.fixedConst(n)
			return false
		case *ast.ClassConstFetch:
			ok = ok && fixedClassConst(n)
			return false
		case *ast.Binary, *ast.Unary, *ast.Paren, *ast.IntLit, *ast.FloatLit, *ast.StringLit:
		default:
			ok = false
		}
		return ok
	})
	return ok
}

// guaranteedVar reports a variable defined on every way, whose type PHP
// guarantees (varInfo.guaranteed), and to which no reference was taken
// that the checker does not follow.
func (x *fnCtx) guaranteedVar(name string, env *state) bool {
	info, ok := env.get(name)
	return ok && info.guaranteed && !info.maybe && !x.byRef[name] && !x.refTaken[name]
}

// fixedConst reports a constant whose value is fixed where the code is
// read: true, false and null, and a constant a `const` statement of the
// project or its packages declares. One define() declares may be another
// on another way, and PHP's own may be this machine's (PHP_INT_MAX).
func (x *fnCtx) fixedConst(n *ast.Name) bool {
	if x.specialConst(n) != nil {
		return true
	}
	return x.fixedConstIn(n, x.res.Scope(), map[*program.Constant]bool{})
}

// fixedConstIn resolves a constant's name in the scope it is written in
// and holds the constant to fixedConst's rule, the value its `const`
// statement gives it included: `const OVER = DYN;` over a constant
// define() declares is no more fixed than DYN. seen ends a cycle, which
// decides nothing.
func (x *fnCtx) fixedConstIn(n *ast.Name, scope *program.Scope, seen map[*program.Constant]bool) bool {
	if scope == nil {
		return false
	}
	ns, global := scope.ResolveConst(n)
	for _, name := range []string{ns, global} {
		k := x.p().Constant(name)
		if k == nil {
			continue
		}
		if k.Defined || k.Polyfilled || k.File == nil || k.File.IsStub() || seen[k] {
			return false
		}
		seen[k] = true
		return x.fixedValue(k.Value, k.Scope, seen)
	}
	return false
}

// fixedValue reports a `const` statement's value made of literals, true,
// false and null, constants of a fixed value and class constants named on
// a class, joined by operators.
func (x *fnCtx) fixedValue(e ast.Expr, scope *program.Scope, seen map[*program.Constant]bool) bool {
	if e == nil {
		return false
	}
	ok := true
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ConstFetch:
			ok = ok && x.fixedName(n.Name, scope, seen)
			return false
		case *ast.Name:
			ok = ok && x.fixedName(n, scope, seen)
			return false
		case *ast.ClassConstFetch:
			ok = ok && fixedClassConst(n)
			return false
		case *ast.Binary, *ast.Unary, *ast.Paren, *ast.IntLit, *ast.FloatLit, *ast.StringLit:
		default:
			ok = false
		}
		return ok
	})
	return ok
}

// fixedName is true, false or null, or a constant fixedConstIn takes.
func (x *fnCtx) fixedName(n *ast.Name, scope *program.Scope, seen map[*program.Constant]bool) bool {
	if len(n.Parts) == 1 && n.Kind != ast.NameRelative {
		switch strings.ToLower(n.Parts[0]) {
		case "true", "false", "null":
			return true
		}
	}
	return x.fixedConstIn(n, scope, seen)
}

// fixedClassConst reports a class constant named on a class, not on
// static or an object, whose value a subclass may give anew.
func fixedClassConst(e *ast.ClassConstFetch) bool {
	n, ok := e.Class.(*ast.Name)
	return ok && !strings.EqualFold(n.Parts[len(n.Parts)-1], "static")
}

// leftAfterOnce is the state after a loop entersOnce found the body of:
// the condition failing after an iteration, or a break, and never on the
// way in (fixpoint's back edge and breaks).
func (x *fnCtx) leftAfterOnce(test func(*state) (t, f *state)) *state {
	back, breaks := x.loopBack, x.loopBreaks
	saved := x.c.mark()
	_, f := test(back.clone())
	x.c.rollback(saved)
	return joinAll(append([]*state{f}, breaks...)...)
}

func (x *fnCtx) whileStmt(s *ast.While, env *state) {
	if s.Alt {
		x.removed(s.S, "the alternative while syntax", "braces")
	}
	test := func(e *state) (*state, *state) {
		x.loopCond = true
		defer func() { x.loopCond = false }()
		return x.cond(s.Cond, e)
	}
	once := x.entersOnce(env, []ast.Expr{s.Cond})
	after := x.fixpoint(env, func(head *state) (*state, *state) {
		t, f := test(head)
		x.stmt(s.Body, t)
		return t, f
	})
	if once {
		after = x.leftAfterOnce(test)
	}
	*env = *after
}

func (x *fnCtx) doWhile(s *ast.DoWhile, env *state) {
	after := x.fixpoint(env, func(head *state) (*state, *state) {
		x.stmt(s.Body, head)
		x.loopCond = true
		t, f := x.cond(s.Cond, head)
		x.loopCond = false
		return t, f
	})
	*env = *after
}

func (x *fnCtx) forStmt(s *ast.For, env *state) {
	if s.Alt {
		x.removed(s.S, "the alternative for syntax", "braces")
	}
	for _, e := range s.Init {
		x.expr(e, env, nil)
	}
	test := func(head *state) (t, f *state) {
		if len(s.Cond) == 0 {
			t, f = head, head.clone()
			f.dead = true
			return t, f
		}
		for _, e := range s.Cond[:len(s.Cond)-1] {
			x.expr(e, head, nil)
		}
		x.loopCond = true
		defer func() { x.loopCond = false }()
		return x.cond(s.Cond[len(s.Cond)-1], head)
	}
	once := x.entersOnce(env, s.Cond)
	after := x.fixpoint(env, func(head *state) (*state, *state) {
		t, f := test(head)
		x.stmt(s.Body, t)
		// `continue` runs the step too: `for ($i = 1; ; ++$i) { … continue; }`
		// does not loop on $i = 1.
		for _, c := range append([]*state{t}, x.continueEnvs...) {
			for _, e := range s.Step {
				x.expr(e, c, nil)
			}
		}
		return t, f
	})
	if once {
		after = x.leftAfterOnce(test)
	}
	*env = *after
}

// mayNotIterate reports whether t has a member that foreach may refuse or
// that iterates as properties rather than as a collection — mixed, object,
// an open type parameter — which PHPStan reports at level max
// (foreach.nonIterable) where IterableKV, serving inference, lets them
// through as mixed.
func mayNotIterate(t types.Type) bool {
	for _, m := range types.Members(t) {
		switch m := m.(type) {
		case types.Mixed, types.Object:
			return true
		case types.Param:
			if openParam(m) {
				return true
			}
		}
	}
	return false
}

// openParam reports whether a type parameter's bound says nothing about
// offsets or iteration: none, mixed or object.
func openParam(p types.Param) bool {
	switch p.Bound.(type) {
	case nil, types.Mixed, types.Object:
		return true
	default:
		return false
	}
}

func (x *fnCtx) foreach(s *ast.Foreach, env *state) {
	if s.Alt {
		x.removed(s.S, "the alternative foreach syntax", "braces")
	}
	it := x.expr(s.Expr, env, nil)
	if mayBeObject(it) {
		x.implicitThrow() // the iterator's methods run
	}
	// `T of list<int>` yields what its bound yields, as PHPStan iterates
	// it: its keys are ints, and a write under one is a write under a key
	// the list has. So does a T of a union or of iterable, and a T a test
	// narrowed (`T&array`). Whether the value may be iterated at all is
	// asked of it as it is.
	_, _, ok := types.IterableKV(it, x.p())
	k, v, _ := types.IterableKV(iteratedBounds(it, x.p()), x.p())
	if !ok || mayNotIterate(it) {
		x.report(s.Expr.Span(), P2303, "foreach over %s", x.typeName(it))
	}
	if s.Doc != "" {
		doc := docblock.Parse(s.Doc)
		for _, vt := range doc.Vars {
			if vv, isVar := s.Value.(*ast.Variable); vt.Name == "" || (isVar && vt.Name == vv.Name) {
				v = x.res.Doc(vt.Type)
			}
			if kv, isVar := s.Key.(*ast.Variable); isVar && vt.Name == kv.Name {
				k = x.res.Doc(vt.Type)
			}
		}
	}
	if s.ByRef {
		if vv, isVar := s.Value.(*ast.Variable); isVar {
			x.byRef[vv.Name] = true
		}
		x.writtenThrough(s.Expr, env) // the body may write any element
		// The body may write any element of a value of `T of array<…>`,
		// or of an array in it (`foreach ($a['inner'] as &$v)`): what the
		// variable holds is its bound, no longer T, as PHPStan has it
		// after the loop.
		if a := offsetRootVar(s.Expr); a != nil {
			if info, defined := env.get(a.Name); defined {
				if bounded, p := writtenBound(info.t); p != nil {
					env.set(a.Name, bounded)
					x.keepRewritten(a, info.rewritten, p, s.Expr.Span(), bounded, env)
				}
			}
		}
	}
	nonEmpty := false
	empty := ok // an array that holds nothing never runs the body
	for _, m := range types.Members(it) {
		switch m := m.(type) {
		case types.Array:
			nonEmpty = nonEmpty || m.NonEmpty
			_, noKey := m.Key.(types.Never)
			_, noValue := m.Value.(types.Never)
			empty = empty && (noKey || noValue)
		case types.Shape:
			nonEmpty = nonEmpty || len(m.Entries) > 0 && !m.Entries[0].Optional
			empty = empty && len(m.Entries) == 0 && !m.Unsealed
		default:
			empty = false
		}
	}
	var before *state
	if empty {
		before = env.clone()
	}
	after := x.fixpoint(env, func(head *state) (*state, *state) {
		exit := head.clone()
		if s.Key != nil {
			x.bind(nil, s.Key, k, head, s.Key.Span())
		}
		x.bind(nil, s.Value, v, head, s.Value.Span())
		if s.Key != nil {
			// `$xs[$k]` is an offset the iteration found: a write to it
			// replaces a value, so a list stays a list (PHPStan's
			// setExistingOffsetValueType).
			if key, ok := x.pathKey(&ast.Index{Array: s.Expr, Index: s.Key}); ok {
				head.setPath(key, v)
			}
		}
		if nit := x.guaranteedOf(s.Expr, head); nit != nil {
			// The loop variables hold what PHP's view of the iterable yields.
			if nk, nv, ok := types.IterableKV(nit, x.p()); ok {
				if kv, isVar := s.Key.(*ast.Variable); isVar {
					head.setNative(kv.Name, nk)
				}
				if vv, isVar := s.Value.(*ast.Variable); isVar {
					head.setNative(vv.Name, nv)
				}
			}
		}
		// `foreach (array_keys($r) as $k)`: `$r[$k]` is there, as PHPStan
		// has it.
		if arr := x.keysSource(s.Expr); arr != nil {
			if key, ok := x.pathKey(&ast.Index{S: s.Value.Span(), Array: arr, Index: s.Value}); ok {
				head.setPath(key, x.valuesOf(arr, head))
			}
		}
		x.stmt(s.Body, head)
		if nonEmpty {
			// At least one iteration ran: the exit is the last one's end, or
			// a `continue` in it.
			exit = joinAll(append([]*state{head}, x.continueEnvs...)...)
		}
		return head, exit
	})
	if nonEmpty && !after.dead {
		// At least one iteration ran: the loop variables are defined.
		for _, e := range []ast.Expr{s.Key, s.Value} {
			if vv, isVar := e.(*ast.Variable); isVar {
				if info, ok := after.get(vv.Name); ok {
					after.vars[vv.Name] = varInfo{t: info.t}
				}
			}
		}
	}
	if empty {
		// The body is checked, but what it would do never happens: the
		// state is the one before the loop (PHPStan's foreach over an
		// array not iterable at least once).
		x.report(s.Expr.Span(), P2409, "foreach over %s, which holds nothing: the body never runs", x.typeName(it))
		after = before
	}
	*env = *after
}

func (x *fnCtx) switchStmt(s *ast.Switch, env *state) {
	if s.Alt {
		x.removed(s.S, "the alternative switch syntax", "braces")
	}
	subject := x.expr(s.Subject, env, nil)
	savedBreaks, savedContinues, savedDeep := x.breakEnvs, x.continueEnvs, x.deepJumps
	x.breakEnvs, x.continueEnvs, x.deepJumps = nil, nil, nil
	defer func() { x.breakEnvs, x.continueEnvs = savedBreaks, savedContinues }()
	remaining := subject
	var fall *state
	hasDefault := false
	// `switch (true)` is a chain of conditions, as PHPStan reads it: a case
	// runs where its condition holds and every one before it failed.
	switchTrue := isTrueLit(s.Subject)
	failed := env.clone()
	for i, c := range s.Cases {
		if c.Semi.End > c.Semi.Start {
			x.c.deprecated(c.Semi, "a case ended by ; is deprecated since PHP 8.5: end it with :")
		}
		caseEnv := env.clone()
		if c.Cond == nil {
			hasDefault = true
			x.narrowPath(caseEnv, s.Subject, remaining, false)
			if switchTrue && i == len(s.Cases)-1 {
				caseEnv = failed
			}
		} else if switchTrue {
			caseEnv, failed = x.cond(c.Cond, failed)
		} else {
			ct := x.expr(c.Cond, caseEnv, nil)
			// switch compares loosely ('1' == 1), so only enum cases and
			// null narrow the subject — as PHPStan does.
			if isLiteralLike(ct) && isIdentityLiteral(ct) {
				x.narrowPath(caseEnv, s.Subject, types.Intersect(remaining, ct, x.p()), false)
				remaining = x.subtract(remaining, ct, x.p())
			}
			if t := x.testedArm(caseEnv, s.Subject, c.Cond); t != nil {
				caseEnv = t
			}
		}
		if fall != nil {
			caseEnv = join(fall, caseEnv)
		}
		x.stmts(c.Body, caseEnv)
		if caseEnv.dead {
			fall = nil
		} else {
			fall = caseEnv
		}
	}
	ends := append([]*state{}, x.breakEnvs...)
	ends = append(ends, x.continueEnvs...)
	if fall != nil {
		ends = append(ends, fall)
	}
	if !hasDefault {
		ends = append(ends, env)
	}
	*env = *joinAll(ends...)
	if x.loopDepth > 0 {
		// `continue` inside a switch continues the enclosing loop.
		savedContinues = append(savedContinues, x.continueEnvs...)
	}
	inner := x.deepJumps
	x.deepJumps = savedDeep
	x.unwind(inner, &savedBreaks, &savedContinues)
}

// isIdentityLiteral reports whether every member of t compares by identity
// under ==: enum cases and null.
func isIdentityLiteral(t types.Type) bool {
	for _, m := range types.Members(t) {
		switch m.(type) {
		case types.EnumCase, types.Null:
		default:
			return false
		}
	}
	return true
}

func sameKind(a, b types.Type) bool {
	for _, m := range types.Members(a) {
		for _, n := range types.Members(b) {
			if types.Overlaps(m, n, nil) {
				return true
			}
		}
	}
	return false
}

func (x *fnCtx) tryStmt(s *ast.Try, env *state) {
	returns := len(x.returnEnvs) // the returns the finally block runs after
	nevers := len(x.neverEnvs)
	x.inTry++ // the body and the catch blocks, not the finally block
	bodyEnv := env.clone()
	// The catch blocks start from where the body can first throw (calls
	// throw, as PHPStan's throw points are): after the leading statements
	// that call nothing, with what the first statement that can throw
	// binds by reference before it calls anything. The state after every
	// statement joins in as the body runs (stmts).
	none := newEnv()
	none.dead = true
	x.throwEnvs = append(x.throwEnvs, none)
	top := len(x.throwEnvs) - 1
	x.pushThrows()
	// The catches take what the body throws, not what they or the finally
	// block throw: the count covers the body alone.
	matchCaught := x.catchesUnhandledMatch(s)
	if matchCaught {
		x.matchCaught++
	}
	seed := bodyEnv.clone()
	lead, thrower := 0, false
	for lead < len(s.Body) && cannotThrow(s.Body[lead]) {
		points := len(x.throwFrames[len(x.throwFrames)-1])
		before := bodyEnv.clone()
		x.stmt(s.Body[lead], bodyEnv)
		x.throwEnvs[top] = join(x.throwEnvs[top], bodyEnv)
		lead++
		if len(x.throwFrames[len(x.throwFrames)-1]) > points {
			// It throws after all (an operator's TypeError, as the throw
			// analysis has it): the catches may see the state before it.
			seed, thrower = before, true
			break
		}
		seed = bodyEnv.clone()
	}
	x.stmts(s.Body[lead:], bodyEnv)
	if matchCaught {
		x.matchCaught--
	}
	original := x.popThrows()
	if lead < len(s.Body) && !thrower {
		if inner, ok := s.Body[lead].(*ast.Try); ok && x.trySeeds[inner] != nil {
			seed = x.trySeeds[inner] // its body's first throw point is ours
		} else {
			w := refWalk{plain: true}
			x.refsBeforeCall(s.Body[lead], &w)
			for _, b := range w.bound {
				after, ok := bodyEnv.get(b.name)
				if !ok {
					continue
				}
				// Where the call throws, the variable holds what it held
				// before or what the callee wrote; the binding created it
				// when it had no value, as null, or as the array holding
				// null at the offset bound.
				if was, ok := seed.get(b.name); ok && !was.maybe {
					seed.set(b.name, types.MakeUnion(was.t, after.t))
					continue
				}
				if _, itself := b.arg.(*ast.Variable); itself {
					seed.set(b.name, types.Nullable(after.t))
					continue
				}
				_, created := refBinding(b.arg, func(k *ast.Variable) types.Type {
					if info, ok := seed.get(k.Name); ok {
						return normaliseKey(info.t)
					}
					return types.StringLit{V: ""} // an undefined key is null, stored as ''
				})
				if was, ok := seed.get(b.name); ok {
					created = types.MakeUnion(was.t, created) // where it had a value
				}
				seed.set(b.name, types.MakeUnion(created, after.t))
			}
		}
	}
	if x.trySeeds == nil {
		x.trySeeds = map[*ast.Try]*state{}
	}
	x.trySeeds[s] = seed
	thrown := join(x.throwEnvs[len(x.throwEnvs)-1], seed)
	x.throwEnvs = x.throwEnvs[:len(x.throwEnvs)-1]
	if n := len(x.throwEnvs); n > 0 {
		// What no catch here takes reaches the enclosing try's catches with
		// the state it was thrown in.
		x.throwEnvs[n-1] = join(x.throwEnvs[n-1], thrown)
	}
	ends := []*state{bodyEnv}
	var caught []types.Type
	var caughtNames []string
	points := original
	for _, c := range s.Catches {
		cEnv := thrown.clone()
		var ts []types.Type
		var names []string // "" where the type is unknown or no Throwable
		var spans []ast.Span
		for _, n := range c.Types {
			if x.reportedName(n) {
				// A refused closure's type parameter (P1007): its catch
				// says nothing more.
				ts = append(ts, types.TError)
				names = append(names, "")
				spans = append(spans, n.S)
				continue
			}
			saved := x.nameOnly
			x.nameOnly = true // a catch of a class PHP lacks never matches, and fails nothing
			name, ok := x.resolveClassName(n, n.S)
			x.nameOnly = saved
			spans = append(spans, n.S)
			if !ok {
				ts = append(ts, types.TObject)
				names = append(names, "")
				continue
			}
			t := types.Class{Name: name}
			if !x.p().IsSubclassOf(name, "Throwable") {
				x.report(n.S, P2318, "%s is not a Throwable", name)
				names = append(names, "")
			} else {
				names = append(names, name)
			}
			for _, prev := range caught {
				if types.IsSubtype(t, prev, x.p()) {
					x.report(n.S, P2403, "%s is already caught by an earlier catch (%s)", name, x.typeName(prev))
				}
			}
			ts = append(ts, t)
		}
		var reached bool
		points, reached = x.catchClause(names, spans, points, original, caughtNames)
		for _, n := range names {
			if n != "" {
				caughtNames = append(caughtNames, n)
			}
		}
		caught = append(caught, ts...)
		if c.Var != nil {
			cEnv.set(c.Var.Name, types.MakeUnion(ts...))
			if reached {
				x.wroteOut(c.Var.Name, types.MakeUnion(ts...), cEnv, c.Var.S) // a by-reference parameter (paramout.go)
			}
		}
		x.stmts(c.Body, cEnv)
		if reached {
			ends = append(ends, cEnv) // a catch nothing reaches never runs
		}
	}
	x.addThrows(points...) // what no catch takes leaves the try statement
	x.inTry--
	if s.HasFinally {
		fEnv := joinAll(append(append([]*state{}, ends...), thrown)...)
		fEnv.dead = false
		// What the block writes of a parameter documented @param-out, it
		// writes on every way out of the try, at a return inside it too.
		entered := fEnv.outs
		fEnv.outs = nil
		start := fEnv.clone()
		exits := slices.Concat(x.returnEnvs[min(returns, len(x.returnEnvs)):], x.neverEnvs[min(nevers, len(x.neverEnvs)):])
		x.inFinally++
		x.stmts(s.Finally, fEnv)
		x.inFinally--
		if x.trackNever && !fEnv.dead {
			// The block runs on the way out of the closure's exits in the
			// try statement as well, and what they leave is what it
			// writes (PHPStan's processFinallyScope).
			for _, e := range exits {
				finallyRan(e, start, fEnv)
			}
		}
		wrote := fEnv.outs
		fEnv.outs = entered
		mergeOuts(fEnv, wrote)
		if x.trackReturns && returns <= len(x.returnEnvs) {
			finallyWrote(x.returnEnvs[returns:], wrote, fEnv)
		}
		after := joinAll(ends...)
		mergeOuts(after, wrote)
		// Assignments in finally apply on every path.
		for k, v := range fEnv.vars {
			if _, ok := after.vars[k]; !ok || !types.Equal(after.vars[k].t, v.t) {
				after.vars[k] = varInfo{t: v.t, maybe: v.maybe && after.vars[k].maybe, rewritten: v.rewritten}
			}
		}
		if fEnv.dead {
			after.dead = true
		}
		*env = *after
		return
	}
	*env = *joinAll(ends...)
}

// finallyRan gives exit, a state that leaves a try statement, what its
// finally block wrote, the block having started in start and ended in
// end: each variable the block assigned another type or defined, as
// PHPStan's processFinallyScope has it.
func finallyRan(exit, start, end *state) {
	for k, v := range end.vars {
		if was, ok := start.vars[k]; ok && types.Equal(was.t, v.t) {
			continue
		}
		exit.vars[k] = v
	}
}

// catchesUnhandledMatch reports a try statement one of whose catches takes
// the UnhandledMatchError a match without a matching arm throws: a catch
// of that class, of Error or of Throwable. PHPStan reads a caught name as
// it is written, as a global class (TryCatchTypeVisitor), where PHP
// resolves it: a catch counts when both readings take the error, so that
// a match PHPStan reports, under an alias, stays P2304, and one whose catch
// names a class of the namespace, which takes nothing, stays P2304 too.
func (x *fnCtx) catchesUnhandledMatch(s *ast.Try) bool {
	for _, c := range s.Catches {
		for _, n := range c.Types {
			written := strings.Join(n.Parts, "\\")
			resolved := x.res.Scope().ResolveClass(n)
			if x.p().IsSubclassOf("UnhandledMatchError", written) && x.p().IsSubclassOf("UnhandledMatchError", resolved) {
				return true
			}
		}
	}
	return false
}

// cannotThrow reports a statement that calls nothing: an expression
// statement or echo of values, variables, operators other than / and %
// (DivisionByZeroError), assignments and closures being created.
func cannotThrow(s ast.Stmt) bool {
	var es []ast.Expr
	switch s := s.(type) {
	case *ast.ExprStmt:
		es = []ast.Expr{s.Expr}
	case *ast.Echo:
		es = s.Exprs
	case *ast.Nop:
		return true
	default:
		return false
	}
	ok := true
	for _, e := range es {
		ast.Inspect(e, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Closure, *ast.ArrowFunc:
				return false // created, not run
			case *ast.Call, *ast.MethodCall, *ast.StaticCall, *ast.New, *ast.Throw, *ast.Include, *ast.Eval,
				*ast.Exit, *ast.Clone, *ast.Yield, *ast.YieldFrom, *ast.Match:
				ok = false
			case *ast.InterpolatedString:
				ok = n.Kind != ast.StringBacktick // a shell command
			case *ast.Binary:
				if n.Op == token.Kind('/') || n.Op == token.Kind('%') {
					ok = false
				}
			case *ast.Assign:
				if n.Op == token.T_DIV_EQUAL || n.Op == token.T_MOD_EQUAL {
					ok = false
				}
			}
			return ok
		})
	}
	return ok
}

// refBound is a variable an argument passed by reference binds, and the
// argument (refBinding).
type refBound struct {
	name string
	arg  ast.Expr
}

// refBinding is the variable a by-reference argument binds, `$m` itself or
// the array `$m['k']`, `$m[$k][0]` and `$m[]` write into, with the value
// the binding creates when the variable had none: PHP fetches the offsets
// for writing before the call runs (SEND_REF, FETCH_DIM_W), which makes
// the variable an array holding null there. A key that is neither a
// literal nor a variable may throw before the binding (a call, a constant
// PHP cannot find), and binds nothing here. keyOf types a variable key as
// PHP stores it; nil when only the variable is asked for.
func refBinding(e ast.Expr, keyOf func(*ast.Variable) types.Type) (*ast.Variable, types.Type) {
	switch e := e.(type) {
	case *ast.Paren:
		return refBinding(e.Expr, keyOf)
	case *ast.Variable:
		if e.Name == "this" {
			return nil, nil
		}
		return e, types.TNull
	case *ast.Index:
		v, inner := refBinding(e.Array, keyOf)
		if v == nil {
			return nil, nil
		}
		var entry types.ShapeEntry
		switch k := e.Index.(type) {
		case *ast.Variable:
			var key types.Type = types.TArrayKey
			if keyOf != nil {
				key = keyOf(k)
			}
			if !storableKey(key) {
				// An object or an array: PHP has made the variable an
				// empty array when it refuses the offset.
				return v, wrapCreated(inner, types.Shape{})
			}
			return v, wrapCreated(inner, types.Array{Key: key, Value: types.TNull})
		case nil:
			entry = types.ShapeEntry{IsInt: true, IntKey: 0}
		default:
			lit := literalKeyType(k)
			if lit == nil {
				return nil, nil
			}
			key := normaliseKey(lit)
			if !isLiteralKey(key) {
				return nil, nil
			}
			entry = arrayWithEntryKey(key, nil)
		}
		// The inner offsets were null, now arrays: what the binding makes
		// is the shape of the outer offsets around this one.
		entry.Type = types.TNull
		return v, wrapCreated(inner, types.Shape{Entries: []types.ShapeEntry{entry}})
	}
	return nil, nil
}

// storableKey reports a key type every member of which PHP stores as
// it is, an int or a string.
func storableKey(t types.Type) bool {
	for _, m := range types.Members(t) {
		switch m.(type) {
		case types.Int, types.String, types.StringLit:
		default:
			return false
		}
	}
	return true
}

// literalKeyType is the type of a key written as a literal: a number, a
// string, true, false or null, a negative number; nil for any other key.
func literalKeyType(e ast.Expr) types.Type {
	switch e := e.(type) {
	case *ast.Paren:
		return literalKeyType(e.Expr)
	case *ast.IntLit:
		return intLitType(e.Raw)
	case *ast.FloatLit:
		if f, err := strconv.ParseFloat(strings.ReplaceAll(e.Raw, "_", ""), 64); err == nil {
			return types.FloatLit{V: f}
		}
	case *ast.StringLit:
		return types.StringLit{V: program.StringValue(e)}
	case *ast.ConstFetch:
		return literalKeyType(e.Name)
	case *ast.Name:
		if len(e.Parts) == 1 {
			switch strings.ToLower(e.Parts[0]) {
			case "true":
				return types.TTrue
			case "false":
				return types.TFalse
			case "null":
				return types.TNull
			}
		}
	case *ast.Unary:
		if e.Op != token.Kind('-') {
			return nil
		}
		switch t := literalKeyType(e.Expr).(type) {
		case types.Int:
			if t.Lo == t.Hi && t.Lo != math.MinInt64 {
				return types.IntLit(-t.Lo)
			}
		case types.FloatLit:
			return types.FloatLit{V: -t.V}
		}
	}
	return nil
}

// plainOperand reports a literal (literalKeyType) or a plain variable:
// what PHP evaluates without running code that may throw.
func plainOperand(e ast.Expr) bool {
	if _, ok := e.(*ast.Variable); ok {
		return true
	}
	return literalKeyType(e) != nil
}

// plainPath reports a plain variable, or an offset of one whose keys are
// literals or plain variables: an assignment's target of that form
// evaluates nothing before the value.
func plainPath(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Variable:
		return true
	case *ast.Index:
		return (e.Index == nil || plainOperand(e.Index)) && plainPath(e.Array)
	}
	return false
}

// wrapCreated puts the array an offset write creates where the null of
// the enclosing write stood: `$m['a']['b']` is array{a: array{b: null}}.
func wrapCreated(outer, inner types.Type) types.Type {
	switch o := outer.(type) {
	case types.Shape:
		last := o.Entries[0]
		last.Type = wrapCreated(last.Type, inner)
		return types.Shape{Entries: []types.ShapeEntry{last}}
	case types.Array:
		return types.Array{Key: o.Key, Value: wrapCreated(o.Value, inner)}
	}
	return inner
}

// refWalk is what refsBeforeCall finds: the variables bound by reference
// so far, in evaluation order, and whether everything evaluated so far is
// a literal or a plain variable. An offset binds only while that holds:
// a property's read before it may throw (an uninitialised property, a
// __get), and so may a class constant's, a computed key, an assignment's
// computed target.
type refWalk struct {
	bound []refBound
	plain bool
}

// refsBeforeCall collects, in evaluation order, the variables an
// expression statement passes by reference before any call in it runs:
// PHP creates them as it binds the argument (SEND_REF). It reports whether
// a call ran, or an offset was fetched, after which it stops.
func (x *fnCtx) refsBeforeCall(n ast.Node, w *refWalk) (called bool) {
	switch e := n.(type) {
	case *ast.ExprStmt:
		return x.refsBeforeCall(e.Expr, w)
	case *ast.Paren:
		return x.refsBeforeCall(e.Expr, w)
	case *ast.Assign:
		if !plainPath(e.Target) {
			w.plain = false
		}
		return x.refsBeforeCall(e.Value, w)
	case *ast.Call:
		// A function, a method or a class is looked up before the
		// arguments are bound, which throws for one PHP cannot find: a
		// name it is written as is the checker's to resolve, a value's
		// is not.
		if _, named := e.Callee.(*ast.Name); !named {
			w.plain = false
			if x.refsBeforeCall(e.Callee, w) {
				return true
			}
		}
		return x.argsBeforeCall(e.Args, w)
	case *ast.MethodCall:
		if _, ok := e.Name.(*ast.Ident); !ok {
			w.plain = false
		}
		if x.refsBeforeCall(e.Object, w) {
			return true
		}
		return x.argsBeforeCall(e.Args, w)
	case *ast.StaticCall:
		_, method := e.Name.(*ast.Ident)
		if _, class := e.Class.(*ast.Name); !class || !method {
			w.plain = false
		}
		return x.argsBeforeCall(e.Args, w)
	case *ast.New:
		if _, class := e.Class.(*ast.Name); !class {
			w.plain = false
		}
		return x.argsBeforeCall(e.Args, w)
	case *ast.Variable, *ast.StringLit, *ast.IntLit, *ast.FloatLit:
		return false
	case *ast.ConstFetch:
		if literalKeyType(e) == nil {
			w.plain = false // a constant PHP may not find
		}
		return false
	case *ast.PropertyFetch:
		w.plain = false
		return x.refsBeforeCall(e.Object, w)
	case *ast.ClassConstFetch:
		w.plain = false
		return false
	}
	return true // anything else may call
}

func (x *fnCtx) argsBeforeCall(args *ast.ArgList, w *refWalk) bool {
	if args != nil {
		for _, a := range args.Args {
			if a.Value == nil {
				w.plain = false // a partial application's placeholder
				continue
			}
			if a.Unpack {
				w.plain = false // the unpacking may iterate a Traversable
			}
			if v, ok := a.Value.(*ast.Variable); ok && x.c.refVars[v] {
				w.bound = append(w.bound, refBound{v.Name, a.Value})
				continue
			}
			if ix, ok := a.Value.(*ast.Index); ok {
				// An offset passed by reference binds its variable as PHP
				// fetches it for writing, where an offset of a scalar
				// throws and one of an ArrayAccess runs offsetGet(): the
				// walk stops after it, as it does after an offset read.
				if v, _ := refBinding(ix, nil); v != nil && x.c.refVars[v] && w.plain {
					w.bound = append(w.bound, refBound{v.Name, a.Value})
				}
				return true
			}
			if x.refsBeforeCall(a.Value, w) {
				return true
			}
		}
	}
	return true // the call runs
}

func (x *fnCtx) returnStmt(s *ast.Return, env *state) {
	if x.inFinally > 0 {
		// It discards the exception in flight, or the try block's own
		// return value.
		x.c.deprecated(s.S, "return inside a finally block is deprecated since PHP 8.6: it discards what the try block returned or threw")
	}
	if s.Expr != nil && x.structor() != "" {
		x.c.deprecated(s.S, "returning a value from a %s is deprecated since PHP 8.6", x.structor())
	}
	defer func() {
		if x.trackReturns {
			x.returnEnvs = append(x.returnEnvs, env.clone())
		}
		env.dead = true
	}()
	var declared types.Type // an unnamed `@var T` above the return types the value
	if s.Doc != "" {
		doc := docblock.Parse(s.Doc)
		x.varTags(doc, env, "")
		for _, vt := range doc.Vars {
			if vt.Name == "" {
				declared = x.res.Doc(vt.Type)
			}
		}
	}
	if s.Expr == nil {
		if x.hasReturn && !x.generator {
			switch x.returnType.(type) {
			case types.Void, types.Never, types.Null:
			default:
				if !types.IsMixed(x.returnType) || true {
					x.report(s.S, P2202, "%s should return %s but returns nothing", x.name, x.typeName(x.returnType))
				}
			}
		}
		return
	}
	x.sawReturnValue = true
	if x.arrow {
		return
	}
	expected := x.returnType
	if x.generator {
		expected = x.generatorReturn()
	}
	t := x.expr(s.Expr, env, expected)
	if declared != nil && s.Expr != nil {
		native := x.nativeOr(s.Expr, t, env)
		switch {
		case isLiteralExpr(s.Expr):
			if !types.IsAcceptable(t, declared, x.p()) {
				x.report(s.S, P2212, "@var %s does not accept the returned %s", x.typeName(declared), x.typeName(t))
			}
		case !types.IsMixed(native) && !types.IsSubtype(declared, withoutTypeArgs(native), x.p()) && !refinesOutside(declared, t, native, x.p()):
			x.report(s.S, P2212, "@var %s is not a subtype of the returned %s", x.typeName(declared), x.typeName(native))
		}
		t = declared
	}
	if returned := x.c.opts.Returned; returned != nil {
		returned(x.file, s, t)
	}
	x.returnTypes = append(x.returnTypes, t)
	if !x.hasReturn {
		return
	}
	// PHP refuses the return when it compiles the file where the native
	// type says void or never; a docblock's says nothing to it, and a
	// generator's return type is held elsewhere.
	switch x.returnType.(type) {
	case types.Void:
		x.reportRefused(writtenAs(x.returnExpr, "void") && !x.generator, s.S, P2204, "%s is void and cannot return a value", x.name)
		return
	case types.Never:
		x.reportRefused(writtenAs(x.returnExpr, "never") && !x.generator, s.S, P2209, "%s is declared never and cannot return", x.name)
		return
	}
	if expected == nil {
		return
	}
	if _, isVoid := t.(types.Void); isVoid {
		x.report(s.Expr.Span(), P2205, "result of a void function is used")
		return
	}
	if !types.IsAcceptable(t, expected, x.p()) {
		// With an inline @var the type held to the declaration is the
		// tag's, of which the expression says nothing.
		value := s.Expr
		if declared != nil {
			value = nil
		}
		if declared == nil && x.reportRewritten(s.Expr, t, expected, env) {
			return
		}
		x.c.reportUnfit(x.given(value), t, expected, s.Expr.Span(), P2202, nil, "%s should return %s but returns %s", x.name, x.typeName(expected), x.typeName(t))
	}
}

// reportRewritten reports a variable returned where its type parameter is
// declared, after a write made it the array written (rewrittenWhy), with
// the write as the finding's related position. False where the value is
// no such variable, or the declared type does not name its parameter:
// the plain finding stands.
func (x *fnCtx) reportRewritten(value ast.Expr, got, want types.Type, env *state) bool {
	why, rel, ok := x.rewrittenWhy(value, got, want, env, true)
	if !ok {
		return false
	}
	x.c.reportRelated(value.Span(), P2202, rel, "%s should return %s but returns %s: %s", x.name, x.typeName(want), x.typeName(got), why)
	return true
}

// rewrittenWhy says why value, of type got, does not fit want, which names
// a type parameter, where a write made the variable value is the array
// written (paramWrite): the caller's argument for the parameter may be
// narrower than the bound, array{a: 5} for `T of array<string, int>`,
// and a value written into is that no longer. rel is the write. It holds
// where got holds what was written, not only a value of the parameter a
// test narrowed or one another assignment gave; and the advice to declare
// the bound, for a return (returned), where the value read so fits it and
// T is what is declared.
//
// Each finding so explained is a parity one in both modes. PHPStan
// reports the place where the bound is a list or a shape, or the write an
// unset() or one in a branch, and keeps T where a general array is
// written under a key in one straight line; no proof at the place tells
// those apart (extra.go): the variable is named by the write and by the
// place, and a variable named more than once proves nothing.
func (x *fnCtx) rewrittenWhy(value ast.Expr, got, want types.Type, env *state, returned bool) (string, *Related, bool) {
	v, ok := unparen(value).(*ast.Variable)
	if !ok {
		return "", nil, false
	}
	info, defined := env.get(v.Name)
	if !defined || info.rewritten == nil {
		return "", nil, false
	}
	w := info.rewritten
	if !slices.ContainsFunc(types.Members(want), func(m types.Type) bool { return types.Equal(m, w.param) }) {
		return "", nil, false
	}
	holds := false
	for _, m := range types.Members(writtenPart(got, w.param)) {
		holds = holds || !isNeverType(m) && types.Overlaps(m, w.written, x.p())
	}
	if !holds {
		return "", nil, false
	}
	p := w.param.Name
	bound := x.boundText(w.param)
	line, _ := x.file.Position(w.at.Start)
	why := fmt.Sprintf("$%s is no longer %s after it was written on line %d", v.Name, p, line)
	switch {
	case !types.IsAcceptable(w.written, w.param.Bound, x.p()):
		// What the write produced leaves the bound.
		why += fmt.Sprintf(", and does not fit %s either", bound)
	default:
		why += fmt.Sprintf(", since the caller's %s may be narrower than %s", p, bound)
		asBound := types.Map(got, func(m types.Type) types.Type {
			if holdsParam(m, w.param) {
				return w.param.Bound
			}
			return m
		})
		if returned && types.Equal(want, w.param) && types.IsAcceptable(asBound, w.param.Bound, x.p()) {
			why += fmt.Sprintf("; declare the return type as %s", bound)
		}
	}
	return why, related(x.file, w.at, "written here"), true
}

// boundText is the bound of a type parameter of the function, the method
// or the class being checked, or of one a closure is written in, as its
// declaration writes it; as the lattice prints it where the declaration
// is not at hand.
func (x *fnCtx) boundText(p types.Param) string {
	for c := x; c != nil; c = c.enclosing {
		var lists [][]*program.TypeParam
		if c.function != nil {
			lists = append(lists, c.function.TypeParams)
		}
		if c.method != nil {
			lists = append(lists, c.method.TypeParams)
		}
		if c.class != nil && strings.EqualFold(c.class.Name, p.Owner) {
			lists = append(lists, c.class.TypeParams)
		}
		for _, tps := range lists {
			for _, tp := range tps {
				if tp.Name == p.Name && tp.BoundText != "" {
					return tp.BoundText
				}
			}
		}
	}
	return x.typeName(p.Bound)
}

// generatorReturn is the R of Generator<K, V, S, R> (or mixed).
func (x *fnCtx) generatorReturn() types.Type {
	for _, m := range types.Members(x.returnType) {
		if c, ok := m.(types.Class); ok && strings.EqualFold(c.Name, "Generator") && len(c.Args) == 4 {
			return c.Args[3]
		}
	}
	return nil
}

// generatorKV returns the key, value and send types of the generator.
func (x *fnCtx) generatorKV() (k, v, send types.Type) {
	k, v, send = types.TMixed, types.TMixed, types.TMixed
	for _, m := range types.Members(x.returnType) {
		c, ok := m.(types.Class)
		if !ok {
			continue
		}
		switch strings.ToLower(c.Name) {
		case "generator":
			if len(c.Args) >= 2 {
				k, v = c.Args[0], c.Args[1]
			}
			if len(c.Args) >= 3 {
				send = c.Args[2]
			}
		case "iterator", "traversable", "iteratoraggregate":
			if len(c.Args) == 2 {
				k, v = c.Args[0], c.Args[1]
			} else if len(c.Args) == 1 {
				v = c.Args[0]
			}
		}
	}
	for _, m := range types.Members(x.returnType) {
		if it, ok := m.(types.Iterable); ok {
			k, v = it.Key, it.Value
		}
	}
	return
}

// yieldRefused says whether PHP refuses a yield that is no generator's
// when it compiles the file: one in top-level code or in a constant
// expression is. A property hook's body is a method's to PHP, which a
// yield makes a generator; it is refused where the hook's return type
// does not admit one (hookRefusesYield).
func (x *fnCtx) yieldRefused() bool {
	return x.class == nil || x.inConst || x.hookRefusesYield
}

func (x *fnCtx) yield(e *ast.Yield, env *state) types.Type {
	if !x.generator {
		x.reportRefused(x.yieldRefused(), e.S, P2315, "yield outside a generator")
	}
	x.structorYield(e.S)
	k, v, send := x.generatorKV()
	var kt, vt types.Type = types.TInt, types.TNull // `yield;` yields null under the next int key
	if e.Key != nil {
		kt = x.expr(e.Key, env, k)
		if x.hasReturn && !types.IsAcceptable(kt, k, x.p()) {
			if why, rel, ok := x.rewrittenWhy(e.Key, kt, k, env, false); ok {
				x.c.reportRelated(e.Key.Span(), P2202, rel, "%s yields key %s, expected %s: %s", x.name, x.typeName(kt), x.typeName(k), why)
			} else {
				x.c.reportUnfit(x.given(e.Key), kt, k, e.Key.Span(), P2202, nil, "%s yields key %s, expected %s", x.name, x.typeName(kt), x.typeName(k))
			}
		}
	}
	if e.Value != nil {
		vt = x.expr(e.Value, env, v)
		if x.hasReturn && !types.IsAcceptable(vt, v, x.p()) {
			if why, rel, ok := x.rewrittenWhy(e.Value, vt, v, env, false); ok {
				x.c.reportRelated(e.Value.Span(), P2202, rel, "%s yields %s, expected %s: %s", x.name, x.typeName(vt), x.typeName(v), why)
			} else {
				x.c.reportUnfit(x.given(e.Value), vt, v, e.Value.Span(), P2202, nil, "%s yields %s, expected %s", x.name, x.typeName(vt), x.typeName(v))
			}
		}
	}
	x.yieldKeys = append(x.yieldKeys, kt)
	x.yieldValues = append(x.yieldValues, vt)
	return send
}

func (x *fnCtx) yieldFrom(e *ast.YieldFrom, env *state) types.Type {
	if !x.generator {
		x.reportRefused(x.yieldRefused(), e.S, P2315, "yield outside a generator")
	}
	x.structorYield(e.S)
	t := x.expr(e.Expr, env, nil)
	if k, v, ok := types.IterableKV(t, x.p()); ok {
		x.yieldKeys = append(x.yieldKeys, k) // yield from keeps the inner keys
		x.yieldValues = append(x.yieldValues, v)
	} else {
		x.report(e.Expr.Span(), P2303, "yield from %s", x.typeName(t))
	}
	for _, m := range types.Members(t) {
		if c, ok := m.(types.Class); ok && strings.EqualFold(c.Name, "Generator") && len(c.Args) == 4 {
			return c.Args[3]
		}
	}
	return types.TMixed
}

// ------------------------------------------------------------------ bodies

// bodyHasYield reports whether a body makes its function a generator
// (closures inside do not count).
func bodyHasYield(stmts []ast.Stmt) bool {
	found := false
	for _, s := range stmts {
		ast.Inspect(s, func(n ast.Node) bool {
			switch n.(type) {
			case *ast.Yield, *ast.YieldFrom:
				found = true
			case *ast.Closure, *ast.ArrowFunc, *ast.ClassDecl, *ast.FunctionDecl:
				return false // a yield inside a nested closure or declaration is its own generator's
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

// variadicArray is what a variadic parameter declared `T ...$xs` holds
// inside its function: the positional arguments under 0, 1, … and, since
// PHP 8.0, the named arguments under their names, so
// array<int<0, max>|string, T>, as PHPStan types it — not list<T>. A
// caller still passes T values, or spreads any iterable of T.
func variadicArray(elem types.Type) types.Type {
	return types.Array{Key: types.MakeUnion(types.Int{Lo: 0, Hi: math.MaxInt64}, types.TString), Value: elem}
}

// body checks a function body: parameters become variables, statements run,
// and a missing return is reported.
func (x *fnCtx) body(params []*program.Param, decls []*ast.Param, stmts []ast.Stmt, span ast.Span) *state {
	env := newEnv()
	x.untouched = x.untouchedParams(params, decls, stmts)
	// A method that overrides another compiles with the parameters the
	// override-erasure rule widened (design/generics.md §4.3): a PHP caller
	// may pass what the overridden method accepts.
	var widened map[int]types.Type
	if x.method != nil {
		widened = x.method.ErasureOverrides()
	}
	for i, p := range params {
		t := p.Type
		if p.Variadic {
			t = variadicArray(t)
		}
		if assume := x.c.opts.Assume; assume != nil && i < len(decls) {
			if a := assume(x.file, decls[i]); a != nil {
				t = a
			}
		}
		env.set(p.Name, t)
		// What PHP holds the parameter to: the erasure of what was written
		// (list<Foo> is an array to PHP, class<T> a string), or the type the
		// rule widened it to.
		native := types.TMixed
		if p.NativeType != nil {
			native = types.Erase(p.NativeType)
		}
		if w, ok := widened[i]; ok {
			native = w
		}
		if p.Variadic {
			native = variadicArray(native)
		}
		env.setNative(p.Name, native)
		env.guarantee(p.Name)
		if p.ByRef {
			x.byRef[p.Name] = true
		}
		if i < len(decls) && decls[i].Default != nil {
			x.inConst = true
			dt := x.expr(decls[i].Default, newEnv(), p.Type)
			x.inConst = false
			// A default for a parameter typed by a template is held to the
			// template's bound: `B $behavior = 1` with `B of 1|2`.
			if !types.IsAcceptable(dt, paramsToBounds(p.Type), x.p()) {
				// The native type the compiled file writes: the one written,
				// or the one the override-erasure rule widened it to.
				nt := p.NativeType
				if w, ok := widened[i]; ok {
					nt = w
				}
				refused := literalRefused(decls[i].Default, nt, true)
				x.c.reportUnfitRefused(refused, given{expr: decls[i].Default}, dt, paramsToBounds(p.Type), decls[i].Default.Span(), P2208, nil, "default value %s does not match parameter $%s of type %s", x.typeName(dt), p.Name, x.typeName(p.Type))
			}
		}
	}
	x.generator = bodyHasYield(stmts)
	x.generatorReturnType()
	x.paramOuts(params, stmts, env)
	if len(withoutTrailingNops(stmts)) == 0 {
		at := x.declHead
		if at.End == 0 {
			at = span
		}
		x.outsHeld(env, at, endsHere) // an empty body ends where it is declared
	}
	x.stmts(stmts, env)
	if !env.dead && x.hasReturn && !x.generator {
		// Reported where the body ends, like PHPStan: the last statement.
		at := span
		if len(stmts) > 0 {
			at = stmts[len(stmts)-1].Span()
		}
		switch x.returnType.(type) {
		case types.Void:
		case types.Never:
			x.report(at, P2209, "%s is declared never but can return", x.name)
		default:
			x.report(at, P2203, "%s should return %s but a return statement is missing", x.name, x.typeName(x.returnType))
		}
	}
	return env
}

// child creates the context of a closure declared in x.
// labelTargeted reports whether a goto names the label in its scope (the
// function, method, closure or hook around it, or the file's top-level
// code): the goto is the mistake, P1001, and its label is part of it.
// A label no goto names is reported on its own.
func (c *Checker) labelTargeted(f *program.File, l *ast.Label) bool {
	if c.targetedLabels == nil {
		c.targetedLabels = map[*program.File]map[*ast.Label]bool{}
	}
	m, ok := c.targetedLabels[f]
	if !ok {
		m = map[*ast.Label]bool{}
		var scope func(ast.Node)
		scope = func(n ast.Node) {
			labels := map[string][]*ast.Label{}
			gotos := map[string]bool{}
			ast.Inspect(n, func(sub ast.Node) bool {
				switch s := sub.(type) {
				case *ast.FunctionDecl, *ast.MethodDecl, *ast.Closure, *ast.PropertyHook:
					if sub != n {
						scope(sub)
						return false
					}
				case *ast.Label:
					labels[s.Name.Name] = append(labels[s.Name.Name], s)
				case *ast.Goto:
					gotos[s.Label.Name] = true
				}
				return true
			})
			for name, ls := range labels {
				for _, label := range ls {
					m[label] = gotos[name]
				}
			}
		}
		if f.AST != nil {
			scope(f.AST)
		}
		c.targetedLabels[f] = m
	}
	return m[l]
}

func (x *fnCtx) child(static bool) *fnCtx {
	c := &fnCtx{c: x.c, file: x.file, class: x.class, static: x.static || static, res: x.res, byRef: map[string]bool{}, thisType: x.thisType, inScope: x.inScope, enclosing: x}
	c.name = "closure"
	// An arrow function reads the variables of its function, the untouched
	// parameters among them (untouched.go); a closure that names one, in
	// its `use` or as a variable of its own, has taken the property away.
	c.untouched = x.untouched
	if b := x.bindThis; b != nil {
		x.bindThis = nil
		if !static {
			c.class, c.static, c.thisType = b.class, false, b.this
		}
	}
	return c
}

// thisBinding is the object Closure::call() binds a closure to.
type thisBinding struct {
	class *program.Class
	this  types.Type
}

// closureCallThis is what `(fn () => …)->call($object)` binds the
// closure literal to: $this as the object, in its class's scope, as
// PHPStan's Scope::enterClosureCall types it. nil for any other call.
func (x *fnCtx) closureCallThis(e *ast.MethodCall, env *state) *thisBinding {
	recv := e.Object
	for {
		p, ok := recv.(*ast.Paren)
		if !ok {
			break
		}
		recv = p.Expr
	}
	switch f := recv.(type) {
	case *ast.Closure:
		if f.Static {
			return nil
		}
	case *ast.ArrowFunc:
		if f.Static {
			return nil
		}
	default:
		return nil
	}
	name, ok := identName(e.Name)
	if !ok || !strings.EqualFold(name, "call") || e.Args == nil || len(e.Args.Args) == 0 {
		return nil
	}
	if a := e.Args.Args[0]; a.Name != nil || a.Unpack || a.Value == nil {
		return nil
	}
	ct, ok := x.pathType(e.Args.Args[0].Value, env).(types.Class)
	if !ok {
		return nil
	}
	cl := x.p().Class(ct.Name)
	if cl == nil {
		return nil
	}
	return &thisBinding{class: cl, this: ct}
}

// closureParams builds the parameters of a closure, taking untyped ones
// from the expected callable type.
func (x *fnCtx) closureParams(decls []*ast.Param, expected types.Type) ([]*program.Param, []types.CallableParam) {
	var exp *types.Callable
	for _, m := range types.Members(expected) {
		if c, ok := m.(types.Callable); ok && c.HasSig {
			exp = &c
			break
		}
	}
	var params []*program.Param
	var cps []types.CallableParam
	for i, d := range decls {
		p := &program.Param{Name: d.Var.Name, NativeType: x.res.Native(d.Type), HasDefault: d.Default != nil, Default: d.Default, ByRef: d.ByRef, Variadic: d.Variadic, Span: d.S}
		p.Type = p.NativeType
		var want types.Type // what the caller passes, from the expected signature
		if exp != nil {
			if i < len(exp.Params) {
				want = exp.Params[i].Type
			} else if n := len(exp.Params); n > 0 && exp.Params[n-1].Variadic {
				want = exp.Params[n-1].Type
			}
		}
		if p.Type == nil && want != nil {
			p.Type = want
		}
		if p.Type == nil {
			p.Type = types.TMixed
		}
		if d.Default != nil && isNullConst(d.Default) {
			p.Type = types.Nullable(p.Type)
		}
		declared := p.Type
		// The body sees the declared type narrowed by what the caller passes:
		// `fn (array $row) => $row['id']` over list<array{id: int}> reads an
		// int, `fn (string $c)` over list<class-string<Foo>> a class-string
		// (PHPStan does the same). The closure's own signature keeps the
		// declared type.
		if p.NativeType != nil && want != nil && !types.IsMixed(want) && !mentionsParam(want) {
			if r := types.Intersect(declared, want, x.p()); !isNeverType(r) {
				p.Type = r
			}
		}
		params = append(params, p)
		cps = append(cps, types.CallableParam{Name: p.Name, Type: declared, Optional: p.HasDefault, Variadic: p.Variadic, ByRef: p.ByRef})
	}
	return params, cps
}

// capturedOffset reports a path of offsets, `$r['k']` or `$r['x'][$k]`,
// whose array and every variable naming an offset of it a closure
// captures: what the scope knows of it holds in the closure's body.
func capturedOffset(k string, captured map[string]bool) bool {
	root, rest, found := strings.Cut(strings.TrimPrefix(k, "$"), "[")
	if !found || !strings.HasPrefix(k, "$") || !captured[root] || strings.Contains(rest, "->") || strings.Contains(rest, "::") {
		return false
	}
	for {
		_, after, mentions := strings.Cut(rest, "$")
		if !mentions {
			return true
		}
		end := strings.IndexAny(after, "[]")
		if end < 0 || !captured[after[:end]] {
			return false
		}
		rest = after[end:]
	}
}

// callEntry is the state a call of a closure starts with, the closure's
// body iterated to a fixed point for its by-reference uses: its parameters
// and what it captured by value as entry has them, and what it captured
// by reference as head, where the calls before it left it.
func callEntry(entry, head *state, uses []*ast.ClosureUse) *state {
	call := entry.clone()
	for _, u := range uses {
		if !u.ByRef {
			continue
		}
		name := u.Var.Name
		if info, ok := head.vars[name]; ok {
			call.vars[name] = info
		}
		for k := range call.paths {
			if pathOf(k, name) {
				delete(call.paths, k)
			}
		}
		for k, t := range head.paths {
			if pathOf(k, name) {
				call.paths[k] = t
			}
		}
	}
	return call
}

// pathOf reports a path, `$r['k']` or `$r->p`, that starts at the variable
// of that name.
func pathOf(path, name string) bool {
	return strings.HasPrefix(path, "$"+name+"[") || strings.HasPrefix(path, "$"+name+"->")
}

func closureHasByRefUse(e *ast.Closure) bool {
	for _, u := range e.Uses {
		if u.ByRef {
			return true
		}
	}
	return false
}

func isNeverType(t types.Type) bool {
	_, ok := t.(types.Never)
	return ok
}

func (x *fnCtx) closure(e *ast.Closure, env *state, expected types.Type) types.Type {
	c := x.child(e.Static || x.inConst)
	if x.inConst {
		c.constClosure = true
		if len(e.Uses) > 0 {
			// PHP refuses the use list when it compiles the file; $this in
			// such a closure (variable) fails only when the closure runs.
			x.reportRefused(true, e.Uses[0].S, P2531, "a closure in a constant expression cannot use variables of the enclosing scope")
		}
	}
	c.closureTypeParams(e.TypeParams)
	c.checkSignatureTypes(nil, e.Params, e.ReturnType)
	params, cps := c.closureParams(e.Params, expected)
	c.returnType = c.res.Native(e.ReturnType)
	c.hasReturn = c.returnType != nil
	cenv := newEnv()
	for _, p := range params {
		t := p.Type
		if p.Variadic {
			t = variadicArray(t)
		}
		cenv.set(p.Name, t)
	}
	// An array is captured with what the scope knows of its offsets where
	// the closure is created, `isset($r['k'])` before it included, by value
	// or by reference alike, as PHPStan has it: its constant offsets, and
	// those another captured variable names, `$r[$k]` with `use ($r, $k)`
	// after `$k = array_key_first($r)`. An offset named by a variable the
	// closure does not capture is not the closure's.
	captured := map[string]bool{}
	capturePaths := func(name string) { captured[name] = true }
	for _, u := range e.Uses {
		info, ok := env.get(u.Var.Name)
		if u.ByRef {
			c.byRef[u.Var.Name] = true
			x.byRef[u.Var.Name] = true
			if held := x.heldRef(u.Var.Name); held != nil {
				if c.heldUses == nil {
					c.heldUses = map[string][]types.Type{}
				}
				c.heldUses[u.Var.Name] = held // its reads stay within the types (byRefValue)
			}
			if !ok {
				env.set(u.Var.Name, types.TNull)
				info = varInfo{t: types.TNull}
			}
			cenv.set(u.Var.Name, info.t)
			capturePaths(u.Var.Name)
			continue
		}
		if !ok {
			if x.inConst {
				// A constant expression has no variables to capture: the
				// use list is the one finding (P2531 above).
				cenv.set(u.Var.Name, types.TError)
				continue
			}
			x.report(u.S, P2008, "undefined variable $%s", u.Var.Name)
			cenv.set(u.Var.Name, types.TMixed)
			continue
		}
		cenv.set(u.Var.Name, info.t)
		capturePaths(u.Var.Name)
	}
	for k, t := range env.paths {
		if capturedOffset(k, captured) {
			cenv.setPath(k, t)
		}
	}
	c.generator = bodyHasYield(e.Body)
	c.generatorReturnType()
	exits := cenv
	if closureHasByRefUse(e) {
		// A by-reference use is whatever the body assigns to it as well:
		// `use (&$server)` with `$server = null` inside is resource|null
		// throughout, `use (&$rows)` with `$rows[] = …` a list however often
		// the closure runs. The body is iterated to a fixed point like a
		// loop's, every return an exit (PHPStan's processClosureNode). Each
		// pass is a call of its own: it starts from the closure's
		// parameters and what it captured by value, and only what it
		// captured by reference carries over from the calls before.
		c.trackReturns = true
		c.trackNever = refFirst(e)
		var end *state
		exits = c.fixpoint(cenv, func(head *state) (*state, *state) {
			c.returnTypes = nil
			c.returnEnvs = nil
			c.neverEnvs = nil
			c.yieldKeys, c.yieldValues = nil, nil
			call := callEntry(cenv, head, e.Uses)
			c.stmts(e.Body, call)
			end = call
			all := joinAll(append([]*state{call}, c.returnEnvs...)...)
			return all, all
		})
		c.trackReturns = false
		cenv = end
	} else {
		c.trackReturns = refFirst(e)
		c.trackNever = refFirst(e)
		c.stmts(e.Body, cenv)
		c.trackReturns = false
	}
	if refFirst(e) {
		x.recordRefExit(e, cenv, c.returnEnvs, c.neverEnvs)
	}
	// What the body assigns to a by-reference use is what the enclosing
	// scope may see once the closure has run — at any of its exits, an
	// early `return` after `$detached[] = $m` included.
	for _, u := range e.Uses {
		if !u.ByRef {
			continue
		}
		if inner, ok := exits.get(u.Var.Name); ok {
			if outer, ok := env.get(u.Var.Name); ok {
				env.set(u.Var.Name, types.MakeUnion(outer.t, inner.t))
			}
		}
	}
	ret := c.returnType
	if !cenv.dead && c.hasReturn && !c.generator {
		switch ret.(type) {
		case types.Void:
		case types.Never:
			x.report(e.S, P2209, "closure is declared never but can return")
		default:
			x.report(e.S, P2203, "closure should return %s but a return statement is missing", x.typeName(ret))
		}
	}
	if gen := c.yieldedGenerator(); gen != nil && (ret == nil || types.IsSubtype(gen, ret, x.p())) {
		// What the generator yields types it, as the returns type a closure
		// that returns (PHPStan infers both): `static function (): \Generator
		// { yield 'x'; }` is a Generator<int, 'x', mixed, void>.
		ret = gen
	} else if ret == nil {
		switch {
		case c.generator:
			ret = types.NewClass("Generator")
		case len(c.returnTypes) > 0:
			ret = types.MakeUnion(c.returnTypes...)
			if !cenv.dead {
				ret = types.Nullable(ret)
			}
		default:
			ret = types.TVoid
		}
	} else {
		ret = refinedReturn(ret, c.returnTypes, c.generator, cenv.dead, x.p())
	}
	return types.Callable{Closure: true, HasSig: true, Params: cps, Return: ret}
}

// refinedReturn is a closure's declared return type narrowed to what its
// body returns when that fits: `function (): array { return ['a' => 1]; }`
// is a closure returning array{a: 1}, as PHPStan types it. A body that
// never reaches a return — every path throws or exits — returns never,
// which any closure type accepts (PHPStan: `Closure(): never`).
// yieldedGenerator is the Generator a generator closure's body makes: its
// keys and values are what it yields, its return what it returns (void when
// nothing). Nil when the closure is no generator or no yield was reached.
func (x *fnCtx) yieldedGenerator() types.Type {
	if !x.generator || len(x.yieldValues) == 0 {
		return nil
	}
	var r types.Type = types.TVoid
	if len(x.returnTypes) > 0 {
		r = types.MakeUnion(x.returnTypes...)
	}
	return types.Class{Name: "Generator", Args: []types.Type{types.MakeUnion(x.yieldKeys...), types.MakeUnion(x.yieldValues...), types.TMixed, r}}
}

func refinedReturn(declared types.Type, returned []types.Type, generator, dead bool, h types.Hierarchy) types.Type {
	if _, isVoid := declared.(types.Void); isVoid || generator {
		return declared
	}
	if len(returned) == 0 {
		if dead {
			return types.TNever
		}
		return declared
	}
	u := types.MakeUnion(returned...)
	if types.IsSubtype(u, declared, h) {
		return u
	}
	return declared
}

func (x *fnCtx) arrowFunc(e *ast.ArrowFunc, env *state, expected types.Type) types.Type {
	c := x.child(e.Static || x.inConst)
	c.constClosure = x.inConst
	c.arrow = true
	c.closureTypeParams(e.TypeParams)
	c.checkSignatureTypes(nil, e.Params, e.ReturnType)
	params, cps := c.closureParams(e.Params, expected)
	c.returnType = c.res.Native(e.ReturnType)
	c.hasReturn = c.returnType != nil
	cenv := env.clone()
	cenv.dead = false
	for _, p := range params {
		t := p.Type
		if p.Variadic {
			t = variadicArray(t)
		}
		cenv.set(p.Name, t)
	}
	var expectedRet types.Type = c.returnType
	if expectedRet == nil {
		for _, m := range types.Members(expected) {
			if cl, ok := m.(types.Callable); ok && cl.HasSig {
				expectedRet = cl.Return
			}
		}
	}
	c.generator = bodyHasYield([]ast.Stmt{&ast.ExprStmt{S: e.Body.Span(), Expr: e.Body}})
	if c.generator {
		return types.Callable{Closure: true, HasSig: true, Params: cps, Return: c.generatorArrow(e, cenv)}
	}
	bt := c.expr(e.Body, cenv, expectedRet)
	ret := c.returnType
	if ret != nil {
		// An arrow function is `return expr;` to PHP, whatever its return
		// type, unlike the expression body of a method or a function
		// (design/functions.md §3): the compiler refuses the return under
		// void. Under never it compiles, and fails when the expression
		// returns, unless the arrow function stands in a constant
		// expression: there it is written as a closure whose body is
		// `return expr;`, which the compiler refuses under never as well.
		_, isVoid := ret.(types.Void)
		_, isNever := ret.(types.Never)
		switch {
		case isVoid:
			x.reportRefused(true, e.Body.Span(), P2204, "arrow function is void and cannot return a value: an arrow function returns its expression")
		case isNever && c.constClosure:
			x.reportRefused(true, e.Body.Span(), P2209, "arrow function is declared never and cannot return: in a constant expression it compiles to a closure that returns its expression")
		case !types.IsAcceptable(bt, ret, x.p()):
			if why, rel, ok := c.rewrittenWhy(e.Body, bt, ret, cenv, true); ok {
				x.c.reportRelated(e.Body.Span(), P2202, rel, "arrow function should return %s but returns %s: %s", x.typeName(ret), x.typeName(bt), why)
			} else {
				x.c.reportUnfit(x.given(e.Body), bt, ret, e.Body.Span(), P2202, nil, "arrow function should return %s but returns %s", x.typeName(ret), x.typeName(bt))
			}
		}
		ret = refinedReturn(ret, []types.Type{bt}, false, false, x.p())
	} else {
		ret = bt
	}
	return types.Callable{Closure: true, HasSig: true, Params: cps, Return: ret}
}

// generatorArrow checks the body of an arrow function that yields, which
// makes it a generator, as PHP has it (`fn () => yield 1`), and returns
// the closure's return type: the Generator of what the body yields, its
// return the body's value, which the closure returns to PHP, and void for
// a body that is a yield or a yield from itself, the value of what the
// caller sends, as PHPStan types it. A declared return type holds the
// yields, as a generator function's does.
func (x *fnCtx) generatorArrow(e *ast.ArrowFunc, env *state) types.Type {
	refused := x.generatorReturnType()
	var expected types.Type
	if x.hasReturn {
		expected = x.generatorReturn()
	}
	bt := x.expr(e.Body, env, expected)
	if !yieldsItsValue(e.Body) {
		if expected != nil && !types.IsAcceptable(bt, expected, x.p()) {
			x.report(e.Body.Span(), P2202, "arrow function should return %s but returns %s", x.typeName(expected), x.typeName(bt))
		}
		x.returnTypes = append(x.returnTypes, bt)
	}
	ret := x.returnType
	if gen := x.yieldedGenerator(); gen != nil && (ret == nil || refused || types.IsSubtype(gen, ret, x.p())) {
		return gen
	}
	if ret == nil || refused {
		return types.NewClass("Generator")
	}
	return ret
}

// yieldsItsValue reports a body whose value is always what a yield in it
// gives, what the caller sends: a yield or a yield from, or a match or a
// ternary each branch of which is one. Its Generator returns void, as
// PHPStan types `fn () => yield 1`, whose value the caller decides.
func yieldsItsValue(e ast.Expr) bool {
	switch e := unparen(e).(type) {
	case *ast.Yield, *ast.YieldFrom:
		return true
	case *ast.Ternary:
		return e.Then != nil && yieldsItsValue(e.Then) && yieldsItsValue(e.Else)
	case *ast.Match:
		for _, arm := range e.Arms {
			if !yieldsItsValue(arm.Body) {
				return false
			}
		}
		return len(e.Arms) > 0
	}
	return false
}

// generatorReturnType reports a function, a method, a closure or an arrow
// function that yields under a return type that does not admit the
// Generator it returns, as PHP refuses it when it compiles the file
// ("Generator return type must be a supertype of Generator, int given"),
// at the declared type: PHP admits a type that holds object or mixed, or
// names Traversable, Iterator or Generator itself, iterable included, at
// its top level or in an intersection that is all of it. It says whether
// it reported. PHP refuses the type the compiled file writes: the one
// written (typeWritten), unless the override-erasure rule narrows it.
func (x *fnCtx) generatorReturnType() bool {
	if !x.generator || x.returnExpr == nil || x.generatorTypeAdmitted(x.returnExpr, true) {
		return false
	}
	span := x.returnExpr.Span()
	written := x.typeWritten(x.returnExpr) && (x.method == nil || x.method.ErasureOverrides()[-1] == nil)
	x.reportRefused(written, span, P2202, "Generator return type must be a supertype of Generator, %s given: %s yields, and returns a Generator", x.file.Src[span.Start:span.End], x.name)
	return true
}

// typeWritten reports a native type the compiled file writes as the
// source does, as far as generatorTypeAdmitted reads it: one that names no
// type parameter, which is erased to its bound, and no type alias, which is
// erased to the type it stands for.
func (x *fnCtx) typeWritten(t ast.TypeExpr) bool {
	if mentionsParam(x.res.Native(t)) {
		return false
	}
	alias := false
	ast.Inspect(t, func(n ast.Node) bool {
		if nt, ok := n.(*ast.NamedType); ok && nt.Name != nil && x.res.TypeAlias(nt.Name) != nil {
			alias = true
		}
		return !alias
	})
	return !alias
}

// writtenAs reports a native type written as the keyword alone, `void` or
// `never`.
func writtenAs(t ast.TypeExpr, keyword string) bool {
	n, ok := t.(*ast.NamedType)
	return ok && n.Name != nil && len(n.Name.Parts) == 1 && n.Name.Kind == ast.NameUnqualified && strings.EqualFold(n.Name.Parts[0], keyword)
}

// generatorTypeAdmitted is PHP's test of a generator's return type, top
// says t is the whole type or a member of the union that is.
func (x *fnCtx) generatorTypeAdmitted(t ast.TypeExpr, top bool) bool {
	switch t := t.(type) {
	case *ast.NullableType:
		return x.generatorTypeAdmitted(t.Type, top)
	case *ast.UnionType:
		for _, m := range t.Types {
			if x.generatorTypeAdmitted(m, false) {
				return true
			}
		}
		return false
	case *ast.IntersectionType:
		if !top {
			return false // a DNF member: PHP looks at the union's names only
		}
		for _, m := range t.Types {
			if x.generatorTypeAdmitted(m, false) {
				return true
			}
		}
		return false
	case *ast.NamedType:
		if t.Name == nil {
			return false
		}
		if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified {
			switch strings.ToLower(t.Name.Parts[0]) {
			case "object", "mixed", "iterable":
				return true
			}
		}
		switch strings.ToLower(x.res.ClassName(t.Name)) {
		case "traversable", "iterator", "generator":
			return true
		}
	}
	return false
}

// classOfSubject returns the object expression of a `$x::class` match
// subject, nil for any other subject.
func classOfSubject(e ast.Expr) ast.Expr {
	c, ok := e.(*ast.ClassConstFetch)
	if !ok {
		return nil
	}
	if id, ok := c.Name.(*ast.Ident); !ok || !strings.EqualFold(id.Name, "class") {
		return nil
	}
	if _, isName := c.Class.(*ast.Name); isName {
		return nil
	}
	return c.Class
}

// classNameConst resolves a `Foo::class` arm condition to the class name.
func (x *fnCtx) classNameConst(e ast.Expr) (string, bool) {
	c, ok := e.(*ast.ClassConstFetch)
	if !ok {
		return "", false
	}
	id, ok := c.Name.(*ast.Ident)
	if !ok || !strings.EqualFold(id.Name, "class") {
		return "", false
	}
	n, ok := c.Class.(*ast.Name)
	if !ok {
		return "", false
	}
	return x.res.ClassName(n), true
}

// testedArm is env where the subject of a match equals the value of an
// arm, or the subject of a switch the value of a case, read as PHPStan
// reads it, `subject === value`: `match (isset($r['k'])) { true => … }`
// narrows as the test does, `match (count($p)) { 2 => … }` as the count
// compared and `match (gettype($v)) { 'integer' => … }` as gettype()
// compared. Only a subject that is such a test (isset(), empty(), a call
// typed bool), count() or gettype() qualifies, against a bool, an int or
// a string literal, where a switch's loose comparison is the same (none
// of gettype()'s names is numeric); nil otherwise.
func (x *fnCtx) testedArm(env *state, subject, value ast.Expr) *state {
	cmp := &ast.Binary{S: value.Span(), Op: token.T_IS_IDENTICAL, Left: subject, Right: value}
	_, isBool := boolConst(value)
	switch s := unparen(subject).(type) {
	case *ast.Isset, *ast.Empty:
		if !isBool {
			return nil
		}
	case *ast.Call, *ast.StaticCall, *ast.MethodCall:
		if _, isCount := x.countCompare(cmp); isCount {
			break
		}
		if _, isGettype := gettypeArg(s); isGettype {
			if lit, ok := unparen(value).(*ast.StringLit); !ok || gettypeNames[program.StringValue(lit)] == nil {
				return nil
			}
			break
		}
		if !isBool || !types.Equal(x.pathType(s, env), types.TBool) {
			return nil
		}
	default:
		return nil
	}
	saved := x.c.mark()
	then, _ := x.cond(cmp, env)
	x.c.rollback(saved)
	return then
}

func (x *fnCtx) match(e *ast.Match, env *state, expected types.Type) types.Type {
	subject := x.expr(e.Subject, env, nil)
	switch s := subject.(type) {
	case types.This:
		if s.Of != nil {
			subject = *s.Of // `match ($this)` in an enum: the cases are the subject
		}
	case types.Static:
		if s.Of != nil {
			subject = *s.Of
		}
	}
	isTrue := false
	if n, ok := constName(e.Subject); ok && n == "true" {
		isTrue = true
	}
	remaining := subject
	// `match ($event::class) { Opened::class => … }` narrows $event itself.
	classOf := classOfSubject(e.Subject)
	var classRemaining types.Type
	if classOf != nil {
		classRemaining = x.pathType(classOf, env)
	}
	var results []types.Type
	var ends []*state
	hasDefault := false
	cur := env.clone()
	// In `match (true)`, a condition and the arm it opens run only when the
	// conditions before it failed, and the arm only when its condition
	// held: version guards as in if/elseif/else (guards.go).
	var negatives, failed []libraryGuard // before the arm; every condition, for default
	if isTrue {
		for _, arm := range e.Arms {
			for _, cond := range arm.Conds {
				_, negative := x.guardOf(cond)
				failed = append(failed, negative)
			}
		}
	}
	for _, arm := range e.Arms {
		armEnv := cur.clone()
		var guards []libraryGuard
		switch {
		case isTrue && arm.Conds == nil:
			guards = failed // default runs when every condition failed, wherever it is written
		case isTrue:
			guards = append(guards, negatives...)
		}
		if arm.Conds == nil {
			hasDefault = true
			if !isTrue {
				x.narrowPath(armEnv, e.Subject, remaining, false)
				if classOf != nil && classRemaining != nil {
					x.narrowPath(armEnv, classOf, classRemaining, false)
				}
			}
		} else if isTrue {
			var thens []*state
			for _, cond := range arm.Conds {
				pop := x.c.pushGuards(negatives...)
				t, f := x.cond(cond, cur)
				pop()
				thens = append(thens, t)
				cur = f
				_, negative := x.guardOf(cond)
				negatives = append(negatives, negative)
			}
			if len(arm.Conds) == 1 {
				positive, _ := x.guardOf(arm.Conds[0])
				guards = append(guards, positive)
			}
			armEnv = joinAll(thens...)
		} else {
			var ts []types.Type
			impossible := false
			for _, cond := range arm.Conds {
				ct := x.expr(cond, armEnv, nil)
				if !types.IsMixed(subject) && !types.IsMixed(ct) && !types.Overlaps(subject, ct, x.p()) {
					if bytesAgainstText(subject, ct) {
						// Both are PHP strings, which may hold the same
						// bytes: the arm can match, and the finding is
						// that binary data meets text (strings.md §2).
						x.report(cond.Span(), P2305, "match arm %s on %s compares binary data with text, which may still hold the same bytes: %s", x.typeName(ct), x.typeName(subject), convertBytesHint(e.Subject, subject, cond))
					} else {
						x.report(cond.Span(), P2305, "match arm %s can never match %s", x.typeName(ct), x.typeName(subject))
					}
					impossible = true
				}
				ts = append(ts, ct)
			}
			ct := types.MakeUnion(ts...)
			// An arm that cannot match is reported, not narrowed to never.
			if !impossible {
				x.narrowPath(armEnv, e.Subject, types.Intersect(remaining, ct, x.p()), false)
				var thens []*state
				for _, cond := range arm.Conds {
					if t := x.testedArm(armEnv, e.Subject, cond); t != nil {
						thens = append(thens, t)
					}
				}
				if len(thens) == len(arm.Conds) {
					armEnv = joinAll(thens...)
				}
			}
			// An arm takes the values its conditions are; a condition of
			// several values, a bool, is one of them, which says nothing of
			// what is left, and the arm's other conditions take theirs.
			for _, t := range ts {
				if oneValue(t) {
					remaining = x.subtract(remaining, t, x.p())
				}
			}
			if classOf != nil && classRemaining != nil {
				var classes []types.Type
				for _, cond := range arm.Conds {
					if name, ok := x.classNameConst(cond); ok {
						classes = append(classes, types.NewClass(name))
					}
				}
				if len(classes) == len(arm.Conds) && !impossible {
					target := types.MakeUnion(classes...)
					x.narrowPath(armEnv, classOf, types.Intersect(classRemaining, target, x.p()), false)
					classRemaining = x.subtract(classRemaining, target, x.p())
				}
			}
		}
		pop := x.c.pushGuards(guards...)
		results = append(results, x.expr(arm.Body, armEnv, expected))
		pop()
		ends = append(ends, armEnv)
	}
	if !hasDefault {
		_, done := remaining.(types.Never)
		if isTrue {
			ends = append(ends, cur)
		} else if !types.IsMixed(subject) {
			// A try around it that catches the error is the handling of the
			// values no arm takes.
			if !done && x.matchCaught == 0 {
				x.report(e.S, P2304, "match does not handle %s", x.typeName(remaining))
			}
		}
		if isTrue || types.IsMixed(subject) || !done {
			x.explicitThrow(types.NewClass("UnhandledMatchError"), false, e.S)
		}
	}
	*env = *joinAll(ends...)
	if len(results) == 0 {
		return types.TNever
	}
	return types.MakeUnion(results...)
}
