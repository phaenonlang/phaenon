package checker

import (
	"math"
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
	if d := statementDoc(s); d != "" {
		// `/** @var array<string, mixed> $data */` before an if, a loop or a
		// try retypes the variable from there on, as PHPStan reads it.
		x.varTags(docblock.Parse(d), env, "")
	}
	switch s := s.(type) {
	case *ast.ExprStmt:
		x.exprStmt(s, env)
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
			t := types.TMixed
			if v.Default != nil {
				x.inConst = true
				x.expr(v.Default, env, nil)
				x.inConst = false
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
		}
	case *ast.Unset:
		for _, v := range s.Vars {
			switch t := v.(type) {
			case *ast.Variable:
				x.variable(t, env, true)
				env.unset(t.Name)
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
	x.assignTo(assign.Target, declared, env, s.S)
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
	if raw != nil && unsetMissing(base, normaliseKey(raw)) {
		x.report(t.Index.Span(), P2313, "offset %s does not exist on %s, so unset cannot remove it", x.typeName(raw), x.typeName(base))
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
			env.set(a.Name, unsetOffset(boundedShapes(info.t), keyT))
		}
	case *ast.PropertyFetch, *ast.StaticPropertyFetch, *ast.Index:
		// Removing an element writes the property that holds the array,
		// whether or not its type changes: a readonly property refuses it
		// as it refuses an assignment. An object's offsetUnset() changes no
		// property, so a property holding none but objects is not written.
		if holdsArray(base) {
			x.writeExisting(t.Array, unsetOffset(base, keyT), env, span)
		}
	}
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
		base := x.pathType(t.Array, env)
		if !holdsArray(base) {
			return
		}
		x.writeExisting(t.Array, withExistingOffset(base, normaliseKey(x.pathType(t.Index, env)), vt), env, span)
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
	var exit *state
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
		back := joinAll(append([]*state{end}, x.continueEnvs...)...)
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
	return joinAll(append([]*state{exit}, breaks...)...)
}

func (x *fnCtx) whileStmt(s *ast.While, env *state) {
	if s.Alt {
		x.removed(s.S, "the alternative while syntax", "braces")
	}
	after := x.fixpoint(env, func(head *state) (*state, *state) {
		x.loopCond = true
		t, f := x.cond(s.Cond, head)
		x.loopCond = false
		x.stmt(s.Body, t)
		return t, f
	})
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
	after := x.fixpoint(env, func(head *state) (*state, *state) {
		var t, f *state
		if len(s.Cond) == 0 {
			t, f = head, head.clone()
			f.dead = true
		} else {
			for _, e := range s.Cond[:len(s.Cond)-1] {
				x.expr(e, head, nil)
			}
			x.loopCond = true
			t, f = x.cond(s.Cond[len(s.Cond)-1], head)
			x.loopCond = false
		}
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
	k, v, ok := types.IterableKV(it, x.p())
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
			x.assignTo(s.Key, k, head, s.Key.Span())
		}
		x.assignTo(s.Value, v, head, s.Value.Span())
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
			var bound []string
			x.refsBeforeCall(s.Body[lead], &bound)
			for _, name := range bound {
				after, ok := bodyEnv.get(name)
				if !ok {
					continue
				}
				// Where the call throws, the variable holds what it held
				// before (null when the binding created it) or what the
				// callee wrote.
				if was, ok := seed.get(name); ok && !was.maybe {
					seed.set(name, types.MakeUnion(was.t, after.t))
				} else {
					seed.set(name, types.Nullable(after.t))
				}
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
		}
		x.stmts(c.Body, cEnv)
		if reached {
			ends = append(ends, cEnv) // a catch nothing reaches never runs
		}
	}
	x.addThrows(points...) // what no catch takes leaves the try statement
	if s.HasFinally {
		fEnv := joinAll(append(append([]*state{}, ends...), thrown)...)
		fEnv.dead = false
		x.inFinally++
		x.stmts(s.Finally, fEnv)
		x.inFinally--
		after := joinAll(ends...)
		// Assignments in finally apply on every path.
		for k, v := range fEnv.vars {
			if _, ok := after.vars[k]; !ok || !types.Equal(after.vars[k].t, v.t) {
				after.vars[k] = varInfo{t: v.t, maybe: v.maybe && after.vars[k].maybe}
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

// refsBeforeCall collects, in evaluation order, the variables an
// expression statement passes by reference before any call in it runs:
// PHP creates them as it binds the argument (SEND_REF). It reports whether
// a call ran, after which it stops.
func (x *fnCtx) refsBeforeCall(n ast.Node, out *[]string) (called bool) {
	switch e := n.(type) {
	case *ast.ExprStmt:
		return x.refsBeforeCall(e.Expr, out)
	case *ast.Paren:
		return x.refsBeforeCall(e.Expr, out)
	case *ast.Assign:
		return x.refsBeforeCall(e.Value, out)
	case *ast.Call:
		if _, named := e.Callee.(*ast.Name); !named && x.refsBeforeCall(e.Callee, out) {
			return true
		}
		return x.argsBeforeCall(e.Args, out)
	case *ast.MethodCall:
		if x.refsBeforeCall(e.Object, out) {
			return true
		}
		return x.argsBeforeCall(e.Args, out)
	case *ast.StaticCall:
		return x.argsBeforeCall(e.Args, out)
	case *ast.New:
		return x.argsBeforeCall(e.Args, out)
	case *ast.PropertyFetch:
		return x.refsBeforeCall(e.Object, out)
	case *ast.Variable, *ast.StringLit, *ast.IntLit, *ast.FloatLit, *ast.ConstFetch, *ast.ClassConstFetch:
		return false
	}
	return true // anything else may call
}

func (x *fnCtx) argsBeforeCall(args *ast.ArgList, out *[]string) bool {
	if args != nil {
		for _, a := range args.Args {
			if a.Value == nil {
				continue
			}
			if v, ok := a.Value.(*ast.Variable); ok && x.c.refVars[v] {
				*out = append(*out, v.Name)
				continue
			}
			if x.refsBeforeCall(a.Value, out) {
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
	switch x.returnType.(type) {
	case types.Void:
		x.report(s.S, P2204, "%s is void and cannot return a value", x.name)
		return
	case types.Never:
		x.report(s.S, P2209, "%s is declared never and cannot return", x.name)
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
		x.report(s.Expr.Span(), P2202, "%s should return %s but returns %s", x.name, x.typeName(expected), x.typeName(t))
	}
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

func (x *fnCtx) yield(e *ast.Yield, env *state) types.Type {
	if !x.generator {
		x.report(e.S, P2315, "yield outside a generator")
	}
	x.structorYield(e.S)
	k, v, send := x.generatorKV()
	var kt, vt types.Type = types.TInt, types.TNull // `yield;` yields null under the next int key
	if e.Key != nil {
		kt = x.expr(e.Key, env, k)
		if x.hasReturn && !types.IsAcceptable(kt, k, x.p()) {
			x.report(e.Key.Span(), P2202, "%s yields key %s, expected %s", x.name, x.typeName(kt), x.typeName(k))
		}
	}
	if e.Value != nil {
		vt = x.expr(e.Value, env, v)
		if x.hasReturn && !types.IsAcceptable(vt, v, x.p()) {
			x.report(e.Value.Span(), P2202, "%s yields %s, expected %s", x.name, x.typeName(vt), x.typeName(v))
		}
	}
	x.yieldKeys = append(x.yieldKeys, kt)
	x.yieldValues = append(x.yieldValues, vt)
	return send
}

func (x *fnCtx) yieldFrom(e *ast.YieldFrom, env *state) types.Type {
	if !x.generator {
		x.report(e.S, P2315, "yield outside a generator")
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
				x.report(decls[i].Default.Span(), P2208, "default value %s does not match parameter $%s of type %s", x.typeName(dt), p.Name, x.typeName(p.Type))
			}
		}
	}
	x.generator = bodyHasYield(stmts)
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
	c := &fnCtx{c: x.c, file: x.file, class: x.class, static: x.static || static, res: x.res, byRef: map[string]bool{}, thisType: x.thisType, inScope: x.inScope}
	c.name = "closure"
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
			x.report(e.Uses[0].S, P2531, "a closure in a constant expression cannot use variables of the enclosing scope")
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
	exits := cenv
	if closureHasByRefUse(e) {
		// A by-reference use is whatever the body assigns to it as well:
		// `use (&$server)` with `$server = null` inside is resource|null
		// throughout, `use (&$rows)` with `$rows[] = …` a list however often
		// the closure runs. The body is iterated to a fixed point like a
		// loop's, every return an exit (PHPStan's processClosureNode).
		c.trackReturns = true
		var end *state
		exits = c.fixpoint(cenv, func(head *state) (*state, *state) {
			c.returnTypes = nil
			c.returnEnvs = nil
			c.yieldKeys, c.yieldValues = nil, nil
			c.stmts(e.Body, head)
			end = head
			all := joinAll(append([]*state{head}, c.returnEnvs...)...)
			return all, all
		})
		c.trackReturns = false
		cenv = end
	} else {
		c.stmts(e.Body, cenv)
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
			x.report(e.Body.Span(), P2204, "arrow function is void and cannot return a value: an arrow function returns its expression")
		case isNever && c.constClosure:
			x.report(e.Body.Span(), P2209, "arrow function is declared never and cannot return: in a constant expression it compiles to a closure that returns its expression")
		case !types.IsAcceptable(bt, ret, x.p()):
			x.report(e.Body.Span(), P2202, "arrow function should return %s but returns %s", x.typeName(ret), x.typeName(bt))
		}
		ret = refinedReturn(ret, []types.Type{bt}, false, false, x.p())
	} else {
		ret = bt
	}
	return types.Callable{Closure: true, HasSig: true, Params: cps, Return: ret}
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
			if isLiteralLike(ct) {
				remaining = x.subtract(remaining, ct, x.p())
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
