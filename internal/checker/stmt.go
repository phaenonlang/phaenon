package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// stmts checks a statement list in sequence.
func (x *fnCtx) stmts(ss []ast.Stmt, env *state) {
	reported := false
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
	}
}

// stmt checks one statement.
func (x *fnCtx) stmt(s ast.Stmt, env *state) {
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
		x.breakEnvs = append(x.breakEnvs, env.clone())
		env.dead = true
	case *ast.Continue:
		if s.Levels != nil {
			x.expr(s.Levels, env, nil)
		}
		x.continueEnvs = append(x.continueEnvs, env.clone())
		env.dead = true
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
			t := types.TNull
			if v.Default != nil {
				t = widenType(x.expr(v.Default, env, nil))
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
				x.exprLenient(t.Array, env)
				if t.Index != nil {
					x.expr(t.Index, env, nil)
				}
				x.unsetIndex(t, env)
			default:
				x.exprLenient(v, env)
				if key, ok := pathKey(v); ok {
					env.invalidatePrefix(key)
				}
			}
		}
	case *ast.Try:
		x.tryStmt(s, env)
	case *ast.Goto:
		x.removed(s.S, "goto", "structured control flow")
	case *ast.Label:
		x.removed(s.S, "labels", "structured control flow")
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
	if !isAssign || len(doc.Vars) == 0 {
		x.expr(s.Expr, env, nil)
		return
	}
	v, isVar := assign.Target.(*ast.Variable)
	var declared types.Type
	for _, vt := range doc.Vars {
		if vt.Name == "" || (isVar && vt.Name == v.Name) {
			declared = x.res.Doc(vt.Type)
		}
	}
	if declared == nil {
		x.expr(s.Expr, env, nil)
		return
	}
	vt := x.expr(assign.Value, env, declared)
	if !types.IsAcceptable(vt, declared, x.p()) && !types.IsMixed(vt) {
		x.report(s.S, P2212, "@var %s does not match the assigned %s", x.typeName(declared), x.typeName(vt))
	}
	x.assignTo(assign.Target, declared, env, s.S)
}

func (x *fnCtx) unsetIndex(t *ast.Index, env *state) {
	if key, ok := pathKey(t); ok {
		env.invalidatePrefix(key)
	}
	if v, ok := t.Array.(*ast.Variable); ok && t.Index != nil {
		info, defined := env.get(v.Name)
		if !defined {
			return
		}
		keyT := normaliseKey(x.pathType(t.Index, env))
		env.set(v.Name, withKey(info.t, keyT, false))
	}
}

// ------------------------------------------------------------------ control flow

func (x *fnCtx) ifStmt(s *ast.If, env *state) {
	if s.Alt {
		x.removed(s.S, "the alternative if syntax", "braces")
	}
	thenEnv, elseEnv := x.cond(s.Cond, env)
	x.stmt(s.Then, thenEnv)
	ends := []*state{thenEnv}
	cur := elseEnv
	for _, ei := range s.ElseIfs {
		t, f := x.cond(ei.Cond, cur)
		x.stmt(ei.Body, t)
		ends = append(ends, t)
		cur = f
	}
	if s.Else != nil {
		x.stmt(s.Else, cur)
	}
	ends = append(ends, cur)
	*env = *joinAll(ends...)
}

// fixpoint runs a loop body until the environment at the loop head is
// stable. body receives the head environment and returns the environment
// at the end of the body (before the back edge) and the exit environment
// (the condition being false).
func (x *fnCtx) fixpoint(entry *state, body func(head *state) (end, exit *state)) *state {
	savedBreaks, savedContinues := x.breakEnvs, x.continueEnvs
	defer func() { x.breakEnvs, x.continueEnvs = savedBreaks, savedContinues }()
	x.loopDepth++
	defer func() { x.loopDepth-- }()
	head := entry.clone()
	var exit *state
	var breaks []*state
	savedDiags := x.c.diags
	for i := 0; i < 5; i++ {
		x.breakEnvs, x.continueEnvs = nil, nil
		if i < 4 {
			// Diagnostics of non-final iterations are discarded: the
			// types are still converging.
			x.c.diags = savedDiags
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
		for _, e := range s.Step {
			x.expr(e, t, nil)
		}
		return t, f
	})
	*env = *after
}

func (x *fnCtx) foreach(s *ast.Foreach, env *state) {
	if s.Alt {
		x.removed(s.S, "the alternative foreach syntax", "braces")
	}
	it := x.expr(s.Expr, env, nil)
	k, v, ok := types.IterableKV(it, x.p())
	if !ok {
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
	for _, m := range types.Members(it) {
		switch m := m.(type) {
		case types.Array:
			nonEmpty = nonEmpty || m.NonEmpty
		case types.Shape:
			nonEmpty = nonEmpty || len(m.Entries) > 0 && !m.Entries[0].Optional
		}
	}
	after := x.fixpoint(env, func(head *state) (*state, *state) {
		exit := head.clone()
		if s.Key != nil {
			x.assignTo(s.Key, k, head, s.Key.Span())
		}
		x.assignTo(s.Value, v, head, s.Value.Span())
		x.stmt(s.Body, head)
		if nonEmpty {
			exit = head // at least one iteration ran
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
	*env = *after
}

func (x *fnCtx) switchStmt(s *ast.Switch, env *state) {
	if s.Alt {
		x.removed(s.S, "the alternative switch syntax", "braces")
	}
	subject := x.expr(s.Subject, env, nil)
	savedBreaks, savedContinues := x.breakEnvs, x.continueEnvs
	x.breakEnvs, x.continueEnvs = nil, nil
	defer func() { x.breakEnvs, x.continueEnvs = savedBreaks, savedContinues }()
	remaining := subject
	var fall *state
	hasDefault := false
	for _, c := range s.Cases {
		caseEnv := env.clone()
		if c.Cond == nil {
			hasDefault = true
			x.narrowPath(caseEnv, s.Subject, remaining, false)
		} else {
			ct := x.expr(c.Cond, caseEnv, nil)
			// switch compares loosely ('1' == 1), so only enum cases and
			// null narrow the subject — as PHPStan does.
			if isLiteralLike(ct) && isIdentityLiteral(ct) {
				x.narrowPath(caseEnv, s.Subject, types.Intersect(remaining, ct, x.p()), false)
				remaining = x.subtract(remaining, ct, x.p())
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
	x.throwEnvs = append(x.throwEnvs, env.clone())
	x.stmts(s.Body, bodyEnv)
	thrown := x.throwEnvs[len(x.throwEnvs)-1]
	x.throwEnvs = x.throwEnvs[:len(x.throwEnvs)-1]
	ends := []*state{bodyEnv}
	var caught []types.Type
	for _, c := range s.Catches {
		cEnv := thrown.clone()
		var ts []types.Type
		for _, n := range c.Types {
			name, ok := x.resolveClassName(n, n.S)
			if !ok {
				ts = append(ts, types.TObject)
				continue
			}
			t := types.Class{Name: name}
			if !x.p().IsSubclassOf(name, "Throwable") {
				x.report(n.S, P2318, "%s is not a Throwable", name)
			}
			for _, prev := range caught {
				if types.IsSubtype(t, prev, x.p()) {
					x.report(n.S, P2403, "%s is already caught by an earlier catch (%s)", name, x.typeName(prev))
				}
			}
			ts = append(ts, t)
		}
		caught = append(caught, ts...)
		if c.Var != nil {
			cEnv.set(c.Var.Name, types.MakeUnion(ts...))
		}
		x.stmts(c.Body, cEnv)
		ends = append(ends, cEnv)
	}
	if s.HasFinally {
		fEnv := joinAll(append(append([]*state{}, ends...), thrown)...)
		fEnv.dead = false
		x.stmts(s.Finally, fEnv)
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

func (x *fnCtx) returnStmt(s *ast.Return, env *state) {
	defer func() { env.dead = true }()
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
	if !x.hasReturn {
		x.returnTypes = append(x.returnTypes, t)
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
	k, v, send := x.generatorKV()
	if e.Key != nil {
		kt := x.expr(e.Key, env, k)
		if x.hasReturn && !types.IsAcceptable(kt, k, x.p()) {
			x.report(e.Key.Span(), P2202, "%s yields key %s, expected %s", x.name, x.typeName(kt), x.typeName(k))
		}
	}
	if e.Value != nil {
		vt := x.expr(e.Value, env, v)
		if x.hasReturn && !types.IsAcceptable(vt, v, x.p()) {
			x.report(e.Value.Span(), P2202, "%s yields %s, expected %s", x.name, x.typeName(vt), x.typeName(v))
		}
	}
	return send
}

func (x *fnCtx) yieldFrom(e *ast.YieldFrom, env *state) types.Type {
	if !x.generator {
		x.report(e.S, P2315, "yield outside a generator")
	}
	t := x.expr(e.Expr, env, nil)
	if _, _, ok := types.IterableKV(t, x.p()); !ok {
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
				return false
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

// body checks a function body: parameters become variables, statements run,
// and a missing return is reported.
func (x *fnCtx) body(params []*program.Param, decls []*ast.Param, stmts []ast.Stmt, span ast.Span) *state {
	env := newEnv()
	for i, p := range params {
		t := p.Type
		if p.Variadic {
			t = types.Array{Key: types.TInt, Value: t, List: true}
		}
		env.set(p.Name, t)
		if p.ByRef {
			x.byRef[p.Name] = true
		}
		if i < len(decls) && decls[i].Default != nil {
			dt := x.expr(decls[i].Default, newEnv(), p.Type)
			if !types.IsAcceptable(dt, p.Type, x.p()) {
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
func (x *fnCtx) child(static bool) *fnCtx {
	c := &fnCtx{c: x.c, file: x.file, class: x.class, static: x.static || static, res: x.res, byRef: map[string]bool{}, thisType: x.thisType, inScope: x.inScope}
	c.name = "closure"
	return c
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
		if p.Type == nil && exp != nil {
			if i < len(exp.Params) {
				p.Type = exp.Params[i].Type
			} else if n := len(exp.Params); n > 0 && exp.Params[n-1].Variadic {
				p.Type = exp.Params[n-1].Type
			}
		}
		if p.Type == nil {
			p.Type = types.TMixed
		}
		if d.Default != nil && isNullConst(d.Default) {
			p.Type = types.Nullable(p.Type)
		}
		params = append(params, p)
		cps = append(cps, types.CallableParam{Name: p.Name, Type: p.Type, Optional: p.HasDefault, Variadic: p.Variadic, ByRef: p.ByRef})
	}
	return params, cps
}

func (x *fnCtx) closure(e *ast.Closure, env *state, expected types.Type) types.Type {
	c := x.child(e.Static)
	c.closureTypeParams(e.TypeParams)
	c.checkSignatureTypes(nil, e.Params, e.ReturnType, false, "a closure")
	params, cps := c.closureParams(e.Params, expected)
	c.returnType = x.res.Native(e.ReturnType)
	c.hasReturn = c.returnType != nil
	cenv := newEnv()
	for _, p := range params {
		t := p.Type
		if p.Variadic {
			t = types.Array{Key: types.TInt, Value: t, List: true}
		}
		cenv.set(p.Name, t)
	}
	for _, u := range e.Uses {
		info, ok := env.get(u.Var.Name)
		if u.ByRef {
			c.byRef[u.Var.Name] = true
			x.byRef[u.Var.Name] = true
			if !ok {
				env.set(u.Var.Name, types.TNull)
				info = varInfo{t: types.TNull}
			}
			cenv.set(u.Var.Name, widenType(info.t))
			continue
		}
		if !ok {
			x.report(u.S, P2008, "undefined variable $%s", u.Var.Name)
			cenv.set(u.Var.Name, types.TMixed)
			continue
		}
		cenv.set(u.Var.Name, info.t)
	}
	c.generator = bodyHasYield(e.Body)
	c.stmts(e.Body, cenv)
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
	if ret == nil {
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
	}
	return types.Callable{Closure: true, HasSig: true, Params: cps, Return: ret}
}

func (x *fnCtx) arrowFunc(e *ast.ArrowFunc, env *state, expected types.Type) types.Type {
	c := x.child(e.Static)
	c.arrow = true
	c.closureTypeParams(e.TypeParams)
	c.checkSignatureTypes(nil, e.Params, e.ReturnType, false, "an arrow function")
	params, cps := c.closureParams(e.Params, expected)
	c.returnType = x.res.Native(e.ReturnType)
	c.hasReturn = c.returnType != nil
	cenv := env.clone()
	cenv.dead = false
	for _, p := range params {
		t := p.Type
		if p.Variadic {
			t = types.Array{Key: types.TInt, Value: t, List: true}
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
		if _, isVoid := ret.(types.Void); !isVoid && !types.IsAcceptable(bt, ret, x.p()) {
			x.report(e.Body.Span(), P2202, "arrow function should return %s but returns %s", x.typeName(ret), x.typeName(bt))
		}
	} else {
		ret = bt
	}
	return types.Callable{Closure: true, HasSig: true, Params: cps, Return: ret}
}

func (x *fnCtx) match(e *ast.Match, env *state, expected types.Type) types.Type {
	subject := x.expr(e.Subject, env, nil)
	isTrue := false
	if n, ok := constName(e.Subject); ok && n == "true" {
		isTrue = true
	}
	remaining := subject
	var results []types.Type
	var ends []*state
	hasDefault := false
	cur := env.clone()
	for _, arm := range e.Arms {
		armEnv := cur.clone()
		if arm.Conds == nil {
			hasDefault = true
			if !isTrue {
				x.narrowPath(armEnv, e.Subject, remaining, false)
			}
		} else if isTrue {
			var thens []*state
			for _, cond := range arm.Conds {
				t, f := x.cond(cond, cur)
				thens = append(thens, t)
				cur = f
			}
			armEnv = joinAll(thens...)
		} else {
			var ts []types.Type
			impossible := false
			for _, cond := range arm.Conds {
				ct := x.expr(cond, armEnv, nil)
				if !types.IsMixed(subject) && !types.IsMixed(ct) && !types.Overlaps(subject, ct, x.p()) {
					x.report(cond.Span(), P2305, "match arm %s can never match %s", x.typeName(ct), x.typeName(subject))
					impossible = true
				}
				ts = append(ts, ct)
			}
			ct := types.MakeUnion(ts...)
			// An arm that cannot match is reported, not narrowed to never.
			if !impossible {
				x.narrowPath(armEnv, e.Subject, types.Intersect(remaining, ct, x.p()), false)
			}
			if isLiteralLike(ct) {
				remaining = x.subtract(remaining, ct, x.p())
			}
		}
		results = append(results, x.expr(arm.Body, armEnv, expected))
		ends = append(ends, armEnv)
	}
	if !hasDefault {
		if isTrue {
			ends = append(ends, cur)
		} else if !types.IsMixed(subject) {
			if _, done := remaining.(types.Never); !done {
				x.report(e.S, P2304, "match does not handle %s", x.typeName(remaining))
			}
		}
	}
	*env = *joinAll(ends...)
	if len(results) == 0 {
		return types.TNever
	}
	return types.MakeUnion(results...)
}
