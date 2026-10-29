package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// checkClassDecl checks a top-level class declaration.
func (c *Checker) checkClassDecl(f *program.File, scope *program.Scope, d *ast.ClassDecl) {
	var cl *program.Class
	if d.Name == nil {
		cl = c.p.DeclareAnonymous(f, scope, d)
	} else {
		cl = c.p.Class(scope.Prefix() + d.Name.Name)
	}
	if cl == nil || cl.Decl != d {
		// A duplicate declaration: the first one won.
		if cl != nil {
			c.report(d.S, P2511, "class %s is already declared", cl.Name)
		}
		return
	}
	c.checkClass(cl)
}

// checkFunctionDecl checks a top-level function declaration.
func (c *Checker) checkFunctionDecl(f *program.File, scope *program.Scope, d *ast.FunctionDecl) {
	fn := c.p.Function(scope.Prefix() + d.Name.Name)
	if fn == nil || fn.Decl != d {
		if fn != nil {
			c.report(d.S, P2511, "function %s() is already declared", fn.Name)
		}
		return
	}
	c.checkFunction(f, fn)
}

func (c *Checker) checkFunction(f *program.File, fn *program.Function) {
	fn.Signature()
	savedFile := c.file
	c.file = f
	defer func() { c.file = savedFile }()
	ctx := c.newCtx(f, fn.Scope, nil, true)
	ctx.function = fn
	ctx.name = "function " + fn.Name + "()"
	ctx.returnType = fn.Return
	ctx.hasReturn = fn.Return != nil
	ctx.res = ctx.res.WithParams(fn.TypeParams)
	for _, tp := range fn.TypeParams {
		ctx.inScope[tp.Name] = true
	}
	ctx.checkSignatureTypes(fn.Decl.TypeParams, fn.Decl.Params, fn.Decl.ReturnType)
	c.checkUntyped(f, fn.Decl.Params, fn.Params, fn.Decl.ReturnType, fn.Return, fn.Decl.Name.Name, fn.Decl.S)
	ctx.body(fn.Params, fn.Decl.Params, fn.Decl.Body, fn.Decl.Name.S)
}

// declSpan is where method-level diagnostics point: the method name.
func declSpan(m *program.Method) ast.Span {
	if m.Decl != nil {
		return m.Decl.Name.S
	}
	return m.Span
}

// traitUseSpan is the span of the class's first `use` clause.
func traitUseSpan(cl *program.Class) ast.Span {
	for _, m := range cl.Decl.Members {
		if tu, ok := m.(*ast.TraitUse); ok {
			return tu.S
		}
	}
	return cl.Span
}

// checkUntyped reports missing declared types in Phaenon files (P2516,
// a warning until the stdlib and collections land).
func (c *Checker) checkUntyped(f *program.File, decls []*ast.Param, params []*program.Param, ret ast.TypeExpr, retT types.Type, name string, span ast.Span) {
	if f.Mode != lexer.ModePhaenon {
		return
	}
	for i, d := range decls {
		if d.Type == nil && i < len(params) && types.IsMixed(params[i].Type) && params[i].NativeType == nil {
			c.report(d.S, P2516, "parameter $%s of %s() has no type", params[i].Name, name)
		}
	}
	switch strings.ToLower(name) {
	case "__construct", "__destruct", "__clone":
		return
	}
	if ret == nil && retT == nil {
		c.report(span, P2516, "%s() has no return type", name)
	}
}

// checkClass checks a class declaration: its header, its members and the
// bodies of its methods and hooks.
func (c *Checker) checkClass(cl *program.Class) {
	key := "class:" + strings.ToLower(cl.Name)
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	cl.Signature()
	savedFile := c.file
	c.file = cl.File
	defer func() { c.file = savedFile }()
	d := cl.Decl
	c.checkHeader(cl)
	phaenon := cl.File.Mode == lexer.ModePhaenon
	if d.Modifiers&ast.ModVar != 0 && phaenon {
		c.report(d.S, P1001, "`var` is not available in Phaenon; use public")
	}

	// Members.
	concrete := cl.Kind == ast.KindClass && !cl.Abstract
	if concrete {
		for _, m := range cl.Unimplemented() {
			if m.Class == cl {
				continue // reported as P2504
			}
			c.report(d.S, P2501, "class %s must implement %s::%s()", cl.Name, m.Class.Name, m.Name)
		}
	}
	for _, m := range cl.Methods {
		if m.FromTrait != nil || m.Magic || m.Decl == nil {
			continue
		}
		c.checkMethod(cl, m)
	}
	for _, pr := range cl.Props {
		if pr.FromTrait != nil || pr.Magic {
			continue
		}
		c.checkProperty(cl, pr)
	}
	for _, k := range cl.Consts {
		if k.Class != cl || k.Value == nil {
			continue
		}
		ctx := c.newCtx(cl.File, cl.Scope, cl, true)
		vt := ctx.expr(k.Value, newEnv(), k.Type)
		if k.Type != nil && !types.IsAcceptable(vt, k.Type, c.p) {
			c.report(k.Span, P2211, "constant %s::%s (%s) does not accept %s", cl.Name, k.Name, types.Print(k.Type), types.Print(vt))
		}
	}
	if cl.Kind == ast.KindEnum {
		c.checkEnum(cl)
	}
	if cl.Kind == ast.KindInterface {
		for _, m := range cl.Methods {
			if m.Vis != program.Public {
				c.report(m.Span, P2514, "interface method %s::%s() must be public", cl.Name, m.Name)
			}
			if m.Decl != nil && m.Decl.HasBody {
				c.report(m.Span, P2514, "interface method %s::%s() cannot have a body", cl.Name, m.Name)
			}
		}
		for _, pr := range cl.Props {
			if len(pr.Hooks) == 0 {
				c.report(pr.Span, P2514, "interface %s cannot declare property $%s", cl.Name, pr.Name)
			}
		}
	}
	if cl.Readonly {
		for _, pr := range cl.Props {
			if pr.Class != cl {
				continue
			}
			if pr.Static {
				c.report(pr.Span, P2513, "readonly class %s cannot declare static property $%s", cl.Name, pr.Name)
			}
			if pr.NativeType == nil {
				c.report(pr.Span, P2513, "readonly class %s: property $%s must have a type", cl.Name, pr.Name)
			}
		}
	}
	// Unused imports are reported per file; attributes are validated lightly.
	for _, g := range d.Attrs {
		c.checkAttrs(cl, g)
	}
	c.checkGenericClass(cl)
}

func (c *Checker) checkAttrs(cl *program.Class, g *ast.AttrGroup) {
	ctx := c.newCtx(cl.File, cl.Scope, cl, true)
	for _, a := range g.Attrs {
		name := ctx.res.ClassName(a.Name)
		if c.p.Class(name) == nil {
			c.report(a.Name.S, P2001, "unknown attribute class %s", name)
			continue
		}
		if a.Args != nil {
			for _, arg := range a.Args.Args {
				if arg.Value != nil {
					ctx.expr(arg.Value, newEnv(), nil)
				}
			}
		}
	}
}

// checkHeader validates extends / implements / traits.
func (c *Checker) checkHeader(cl *program.Class) {
	d := cl.Decl
	ctx := c.newCtx(cl.File, cl.Scope, cl, true)
	for i, n := range d.Extends {
		name := ctx.res.ClassName(n.Name)
		super := c.p.Class(name)
		if super == nil {
			c.report(n.S, P2001, "unknown class %s", name)
			continue
		}
		if cl.Kind == ast.KindInterface {
			if super.Kind != ast.KindInterface {
				c.report(n.S, P2510, "interface %s cannot extend %s, which is not an interface", cl.Name, super.Name)
			}
		} else {
			if i == 0 && super.Kind != ast.KindClass {
				c.report(n.S, P2510, "class %s cannot extend %s, which is not a class", cl.Name, super.Name)
			}
			if super.Final {
				c.report(n.S, P2509, "class %s cannot extend final class %s", cl.Name, super.Name)
			}
			if super.IsSubclassOf(cl.Name) && !strings.EqualFold(super.Name, cl.Name) {
				c.report(n.S, P2508, "circular inheritance between %s and %s", cl.Name, super.Name)
			}
		}
	}
	for _, n := range d.Implements {
		name := ctx.res.ClassName(n.Name)
		iface := c.p.Class(name)
		if iface == nil {
			c.report(n.S, P2001, "unknown interface %s", name)
			continue
		}
		if iface.Kind != ast.KindInterface {
			c.report(n.S, P2510, "%s cannot implement %s, which is not an interface", cl.Name, iface.Name)
		}
	}
	for _, m := range d.Members {
		tu, ok := m.(*ast.TraitUse)
		if !ok {
			continue
		}
		for _, n := range tu.Names {
			name := ctx.res.ClassName(n)
			t := c.p.Class(name)
			if t == nil {
				c.report(n.S, P2001, "unknown trait %s", name)
				continue
			}
			if t.Kind != ast.KindTrait {
				c.report(n.S, P2510, "%s is not a trait", t.Name)
			}
		}
	}
	// Trait conflicts: two used traits defining the same method without insteadof.
	seenMethods := map[string]*program.Class{}
	for _, tn := range cl.Traits {
		t := c.p.Class(tn)
		if t == nil {
			continue
		}
		t.Signature()
		for _, m := range t.Methods {
			if m.Abstract {
				continue
			}
			k := strings.ToLower(m.Name)
			if other, dup := seenMethods[k]; dup && !c.resolvedByInsteadof(cl, k) {
				if own := cl.FindMethod(m.Name); own == nil || own.FromTrait != nil {
					c.report(traitUseSpan(cl), P2506, "trait method %s() is defined in both %s and %s", m.Name, other.Name, t.Name)
				}
			}
			seenMethods[k] = t
		}
	}
}

func (c *Checker) resolvedByInsteadof(cl *program.Class, method string) bool {
	for _, m := range cl.Decl.Members {
		tu, ok := m.(*ast.TraitUse)
		if !ok {
			continue
		}
		for _, a := range tu.Adaptations {
			if len(a.InsteadOf) > 0 && strings.EqualFold(a.Method.Name, method) {
				return true
			}
		}
	}
	return false
}

func (c *Checker) checkEnum(cl *program.Class) {
	seen := map[string]bool{}
	ctx := c.newCtx(cl.File, cl.Scope, cl, true)
	for _, cs := range cl.Cases {
		if seen[cs.Name] {
			c.report(cs.Span, P2511, "duplicate enum case %s::%s", cl.Name, cs.Name)
		}
		seen[cs.Name] = true
		if cl.BackingType == nil {
			if cs.Value != nil {
				c.report(cs.Span, P2507, "case %s of non-backed enum %s cannot have a value", cs.Name, cl.Name)
			}
			continue
		}
		if cs.Value == nil {
			c.report(cs.Span, P2507, "case %s of backed enum %s needs a value", cs.Name, cl.Name)
			continue
		}
		vt := ctx.expr(cs.Value, newEnv(), cl.BackingType)
		if !types.IsSubtype(vt, cl.BackingType, c.p) {
			c.report(cs.Value.Span(), P2507, "case %s::%s has value %s, expected %s", cl.Name, cs.Name, types.Print(vt), types.Print(cl.BackingType))
		}
	}
	for _, pr := range cl.Props {
		if pr.Class == cl && !pr.Magic {
			c.report(pr.Span, P2514, "enum %s cannot declare property $%s", cl.Name, pr.Name)
		}
	}
	if cl.BackingType != nil {
		if !types.IsSubtype(cl.BackingType, types.MakeUnion(types.TInt, types.TString), nil) || types.IsMixed(cl.BackingType) {
			c.report(cl.Decl.BackingType.Span(), P2507, "enum %s must be backed by int or string", cl.Name)
		}
	}
}

func (c *Checker) checkProperty(cl *program.Class, pr *program.Property) {
	phaenon := cl.File.Mode == lexer.ModePhaenon
	if pr.NativeType == nil && phaenon && types.IsMixed(pr.Type) && !pr.Promoted {
		c.report(pr.Span, P2516, "property %s::$%s has no type", cl.Name, pr.Name)
	}
	ctx := c.newCtx(cl.File, cl.Scope, cl, pr.Static)
	if pr.HasDefault && pr.Default != nil {
		dt := ctx.expr(pr.Default, newEnv(), pr.Type)
		if !types.IsAcceptable(dt, pr.Type, c.p) {
			c.report(pr.Default.Span(), P2201, "property %s::$%s (%s) does not accept default %s", cl.Name, pr.Name, types.Print(pr.Type), types.Print(dt))
		}
	}
	for _, h := range pr.Hooks {
		hctx := c.newCtx(cl.File, cl.Scope, cl, false)
		hctx.name = cl.Name + "::$" + pr.Name + "::" + h.Name.Name
		env := newEnv()
		switch strings.ToLower(h.Name.Name) {
		case "get":
			hctx.returnType = pr.Type
			hctx.hasReturn = true
			if h.Expr != nil {
				t := hctx.expr(h.Expr, env, pr.Type)
				if !types.IsAcceptable(t, pr.Type, c.p) {
					c.report(h.Expr.Span(), P2512, "get hook of %s::$%s returns %s, expected %s", cl.Name, pr.Name, types.Print(t), types.Print(pr.Type))
				}
			} else if h.Body != nil {
				hctx.stmts(h.Body, env)
				if !env.dead {
					c.report(h.S, P2203, "get hook of %s::$%s must return %s", cl.Name, pr.Name, types.Print(pr.Type))
				}
			}
		case "set":
			hctx.returnType = types.TVoid
			hctx.hasReturn = true
			vt := pr.Type
			if len(h.Params) > 0 {
				if pt := hctx.res.Native(h.Params[0].Type); pt != nil {
					vt = pt
					if !types.IsSubtype(pr.Type, pt, c.p) {
						c.report(h.Params[0].S, P2512, "set hook parameter of %s::$%s must accept %s", cl.Name, pr.Name, types.Print(pr.Type))
					}
				}
				env.set(h.Params[0].Var.Name, vt)
			} else {
				env.set("value", vt)
			}
			if h.Expr != nil {
				hctx.expr(h.Expr, env, nil)
			} else if h.Body != nil {
				hctx.stmts(h.Body, env)
			}
		}
	}
}

func (c *Checker) checkMethod(cl *program.Class, m *program.Method) {
	d := m.Decl
	phaenon := cl.File.Mode == lexer.ModePhaenon
	concrete := cl.Kind == ast.KindClass && !cl.Abstract
	if m.Abstract && concrete {
		c.report(declSpan(m), P2504, "abstract method %s::%s() in a non-abstract class", cl.Name, m.Name)
	}
	if m.Abstract && d.HasBody && cl.Kind != ast.KindInterface {
		c.report(m.Span, P2504, "abstract method %s::%s() cannot have a body", cl.Name, m.Name)
	}
	c.checkOverride(cl, m)
	c.checkUntyped(cl.File, d.Params, m.Params, d.ReturnType, m.Return, m.Name, m.Span)
	if phaenon {
		for _, p := range d.Params {
			if p.Modifiers != 0 && cl.Kind == ast.KindInterface {
				c.report(p.S, P2514, "interface %s cannot promote constructor properties", cl.Name)
			}
		}
	}
	sctx := c.newCtx(cl.File, cl.Scope, cl, m.Static)
	sctx.res = sctx.res.WithParams(m.TypeParams)
	sctx.checkSignatureTypes(d.TypeParams, d.Params, d.ReturnType)
	if !d.HasBody {
		return
	}
	ctx := c.newCtx(cl.File, cl.Scope, cl, m.Static)
	ctx.method = m
	ctx.name = cl.Name + "::" + m.Name + "()"
	ctx.returnType = m.Return
	ctx.hasReturn = m.Return != nil
	ctx.res = ctx.res.WithParams(m.TypeParams)
	for _, tp := range m.TypeParams {
		ctx.inScope[tp.Name] = true
	}
	ctx.body(m.Params, d.Params, d.Body, d.Name.S)
}

// checkOverride compares a method with the one it overrides.
func (c *Checker) checkOverride(cl *program.Class, m *program.Method) {
	hasOverrideAttr := false
	for _, g := range m.Attrs {
		for _, a := range g.Attrs {
			if strings.EqualFold(a.Name.Parts[len(a.Name.Parts)-1], "Override") {
				hasOverrideAttr = true
			}
		}
	}
	var parent *program.Method
	key := strings.ToLower(m.Name)
	for _, a := range cl.Ancestors() {
		a.Signature()
		if pm := a.FindMethod(key); pm != nil && pm.Class != cl {
			if pm.Vis == program.Private && pm.Class.Kind != ast.KindInterface {
				continue
			}
			parent = pm
			break
		}
	}
	if parent == nil {
		if hasOverrideAttr {
			c.report(declSpan(m), P2503, "%s::%s() has #[\\Override] but overrides nothing", cl.Name, m.Name)
		}
		return
	}
	if parent.Final {
		c.report(declSpan(m), P2505, "%s::%s() overrides final method %s::%s()", cl.Name, m.Name, parent.Class.Name, parent.Name)
	}
	if parent.Static != m.Static {
		c.report(m.Span, P2502, "%s::%s() and %s::%s() disagree on static", cl.Name, m.Name, parent.Class.Name, parent.Name)
	}
	if m.Vis > parent.Vis {
		c.report(m.Span, P2502, "%s::%s() must be at least %s like %s::%s()", cl.Name, m.Name, parent.Vis, parent.Class.Name, parent.Name)
	}
	if strings.EqualFold(m.Name, "__construct") && !parent.Abstract && parent.Class.Kind != ast.KindInterface {
		return // constructors are exempt unless the parent's is abstract
	}
	// Map the parent's types into this class's generic arguments.
	ctx := c.newCtx(cl.File, cl.Scope, cl, m.Static)
	self := cl.TypeOf()
	bind := func(t types.Type, decl *program.Class) types.Type {
		if t == nil {
			return nil
		}
		return ctx.bindReceiver(t, self, decl)
	}
	for i, pp := range parent.Params {
		if i >= len(m.Params) {
			if n := len(m.Params); n > 0 && m.Params[n-1].Variadic {
				break
			}
			c.report(m.Span, P2502, "%s::%s() has fewer parameters than %s::%s()", cl.Name, m.Name, parent.Class.Name, parent.Name)
			break
		}
		cp := m.Params[i]
		if pp.HasDefault && !cp.HasDefault && !cp.Variadic {
			c.report(cp.Span, P2502, "parameter $%s of %s::%s() must be optional like in %s::%s()", cp.Name, cl.Name, m.Name, parent.Class.Name, parent.Name)
		}
		pt, ct := bind(pp.Type, parent.Class), bind(cp.Type, cl)
		if pt != nil && ct != nil && !types.IsSubtype(types.Erase(pt), types.Erase(ct), c.p) && !types.IsSubtype(pt, ct, c.p) {
			c.report(cp.Span, P2502, "parameter $%s of %s::%s() (%s) is not contravariant with %s::%s() (%s)",
				cp.Name, cl.Name, m.Name, types.Print(ct), parent.Class.Name, parent.Name, types.Print(pt))
		}
		if pp.ByRef != cp.ByRef {
			c.report(cp.Span, P2502, "parameter $%s of %s::%s() disagrees on by-reference with %s::%s()", cp.Name, cl.Name, m.Name, parent.Class.Name, parent.Name)
		}
	}
	for i := len(parent.Params); i < len(m.Params); i++ {
		if !m.Params[i].HasDefault && !m.Params[i].Variadic {
			c.report(m.Params[i].Span, P2502, "extra parameter $%s of %s::%s() must be optional", m.Params[i].Name, cl.Name, m.Name)
		}
	}
	if parent.Return != nil {
		pr, cr := bind(parent.Return, parent.Class), bind(m.Return, cl)
		if cr == nil {
			if _, isVoid := pr.(types.Void); !isVoid && !types.IsMixed(pr) {
				c.report(m.Span, P2502, "%s::%s() must declare a return type compatible with %s::%s() (%s)", cl.Name, m.Name, parent.Class.Name, parent.Name, types.Print(pr))
			}
		} else if !types.IsSubtype(cr, pr, c.p) && !types.IsSubtype(types.Erase(cr), types.Erase(pr), c.p) {
			if _, parentVoid := pr.(types.Void); !(parentVoid && isVoidLike(cr)) {
				c.report(m.Span, P2502, "return type %s of %s::%s() is not covariant with %s::%s() (%s)",
					types.Print(cr), cl.Name, m.Name, parent.Class.Name, parent.Name, types.Print(pr))
			}
		}
	}
}

func isVoidLike(t types.Type) bool {
	switch t.(type) {
	case types.Void, types.Never:
		return true
	}
	return false
}
