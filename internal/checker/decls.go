package checker

import (
	"fmt"
	"slices"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/phpversion"
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
	ctx.throwsClause(fn.Decl.Throws)
	c.declaredName(fn.Decl.Name.S, "function", fn.Decl.Name.Name)
	c.checkUntyped(f, fn.Decl.Params, fn.Params, fn.Decl.ReturnType, fn.Return, fn.Decl.Name.Name, fn.Decl.S, fn.Doc, false)
	ctx.pushThrows()
	ctx.body(fn.Params, fn.Decl.Params, ctx.exprBody(fn.Decl.Name.Name, fn.Decl.Arrow, fn.Decl.Body), fn.Decl.Name.S)
	points := ctx.popThrows()
	if c.opts.Thrown != nil {
		c.opts.Thrown(f, fn.Decl, exported(points))
	}
	ctx.bodyThrows(fn.Throws, false, ctx.name, fn.Decl.Name.S, points)
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

// checkUntyped reports missing declared types in Phaenon files (P2516, an
// error: "mixed is written, never inferred silently", collections.md §7),
// and the arrays and iterables they declare without an element type
// (collections.md §2). doc is the declaration's docblock; inheritedRet
// reports a return type taken from the method it overrides.
func (c *Checker) checkUntyped(f *program.File, decls []*ast.Param, params []*program.Param, ret ast.TypeExpr, retT types.Type, name string, span ast.Span, doc *docblock.Doc, inheritedRet bool) {
	if f.Mode != lexer.ModePhaenon {
		return
	}
	for i, d := range decls {
		if i >= len(params) {
			break
		}
		p := params[i]
		if d.Type == nil && types.IsMixed(p.Type) && p.NativeType == nil {
			c.report(d.S, P2516, "parameter $%s of %s() has no type", p.Name, name)
			continue
		}
		tag := paramTag(doc, p.Name)
		if tag == nil && d.Doc != "" {
			tag = varTag(docblock.Parse(d.Doc))
		}
		if kind := elementless(tag, p.Inherited, d.Type); kind != "" {
			c.report(d.S, P2516, "parameter $%s of %s(): %s", p.Name, name, noElementMsg(kind))
			if tag == nil {
				c.noteElementless(f, d.Type, d.S)
			}
		}
	}
	switch strings.ToLower(name) {
	case "__construct", "__destruct", "__clone":
		return
	}
	if ret == nil && retT == nil {
		c.report(span, P2516, "%s() has no return type", name)
		return
	}
	var tag *docblock.TypeNode
	if doc != nil {
		tag = doc.Return
	}
	if kind := elementless(tag, inheritedRet, ret); kind != "" {
		c.report(span, P2516, "%s() returns %s", name, noElementMsg(kind))
		if tag == nil {
			c.noteElementless(f, ret, span)
		}
	}
}

// elementless names the array or iterable a declaration's type leaves
// without an element type, "" when there is none. The type is the slot's
// own docblock tag when it has one; else, when it took its type from the
// method it overrides (inherited), it names what that method names;
// else it is the declared syntax.
func elementless(tag *docblock.TypeNode, inherited bool, native ast.TypeExpr) string {
	switch {
	case tag != nil:
		return noElementDoc(tag)
	case inherited:
		return ""
	}
	return noElementType(native)
}

// noteElementless hands Options.Elementless the names in a declared type
// that noElementType finds, unless an ignore comment silences the finding
// reported at at: the author chose to leave the type as it is, and PHPStan
// reports an ignore that no longer matches.
func (c *Checker) noteElementless(f *program.File, t ast.TypeExpr, at ast.Span) {
	if c.opts.Elementless == nil {
		return
	}
	if line, _ := c.file.Position(at.Start); c.ignored[c.file.Path][line] {
		return
	}
	ast.Inspect(t, func(n ast.Node) bool {
		if nt, ok := n.(*ast.NamedType); ok && len(nt.TypeArgs) == 0 && len(nt.Name.Parts) == 1 && nt.Name.Kind == ast.NameUnqualified {
			if l := strings.ToLower(nt.Name.Parts[0]); l == "array" || l == "iterable" {
				c.opts.Elementless(f, nt)
			}
		}
		return true
	})
}

func noElementMsg(kind string) string {
	return fmt.Sprintf("%s with no element type (%s<mixed> for any)", kind, kind)
}

// noElementType finds an array or iterable with no element type in
// declared syntax: `array`, `?array`, `map<string, array>`, `iterable`, a
// closure type's parameter.
func noElementType(t ast.TypeExpr) string {
	switch t := t.(type) {
	case *ast.NamedType:
		if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified && len(t.TypeArgs) == 0 {
			if n := strings.ToLower(t.Name.Parts[0]); n == "array" || n == "iterable" {
				return n
			}
		}
		for _, a := range t.TypeArgs {
			if n := noElementType(a); n != "" {
				return n
			}
		}
	case *ast.NullableType:
		return noElementType(t.Type)
	case *ast.UnionType:
		for _, m := range t.Types {
			if n := noElementType(m); n != "" {
				return n
			}
		}
	case *ast.IntersectionType:
		for _, m := range t.Types {
			if n := noElementType(m); n != "" {
				return n
			}
		}
	case *ast.ClosureType:
		for _, p := range t.Params {
			if n := noElementType(p.Type); n != "" {
				return n
			}
		}
		return noElementType(t.Return)
	case *ast.TupleType:
		for _, e := range t.Elems {
			if n := noElementType(e); n != "" {
				return n
			}
		}
	}
	return ""
}

// noElementDoc is noElementType for a docblock type, where `list`, `map`
// and their non-empty forms may be written bare too.
func noElementDoc(n *docblock.TypeNode) string {
	if n == nil {
		return ""
	}
	switch n.Kind {
	case docblock.KindNamed:
		if len(n.Args) == 0 {
			switch l := strings.ToLower(n.Name); l {
			case "array", "iterable", "list", "map", "non-empty-array", "non-empty-list", "non-empty-map":
				return l
			}
		}
	case docblock.KindShape:
		for _, e := range n.Entries {
			if k := noElementDoc(e.Type); k != "" {
				return k
			}
		}
	case docblock.KindCallable:
		for _, p := range n.Params {
			if k := noElementDoc(p.Type); k != "" {
				return k
			}
		}
		return noElementDoc(n.Return)
	case docblock.KindConditional:
		if k := noElementDoc(n.Then); k != "" {
			return k
		}
		return noElementDoc(n.Else)
	}
	for _, a := range n.Args {
		if k := noElementDoc(a); k != "" {
			return k
		}
	}
	return ""
}

// paramTag is the type a docblock's @param gives the named parameter.
func paramTag(doc *docblock.Doc, name string) *docblock.TypeNode {
	if doc == nil {
		return nil
	}
	for _, t := range doc.Params {
		if t.Name == name && t.Type != nil {
			return t.Type
		}
	}
	return nil
}

// varTag is the type of a docblock's @var: a property's, or a promoted
// parameter's own.
func varTag(doc *docblock.Doc) *docblock.TypeNode {
	if doc == nil {
		return nil
	}
	for _, v := range doc.Vars {
		if v.Type != nil {
			return v.Type
		}
	}
	return nil
}

// checkClass checks a class declaration: its header, its members and the
// bodies of its methods and hooks.
// scopeParams brings the type parameters a type mentions into scope.
func scopeParams(ctx *fnCtx, t types.Type) {
	types.Walk(t, func(m types.Type) {
		if p, ok := m.(types.Param); ok {
			ctx.inScope[p.Name] = true
		}
	})
}

func (c *Checker) checkClass(cl *program.Class) {
	if c.p.IsAlias(cl.Name) {
		return // a declaration kept for IDEs under `if (false)`: the alias is the class
	}
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
	if d.Name != nil {
		c.declaredName(d.Name.S, kindWord(cl), d.Name.Name)
	}
	// Declared types name classes PHP resolves when it checks a value.
	tctx := c.newCtx(cl.File, cl.Scope, cl, false)
	for _, m := range d.Members {
		switch m := m.(type) {
		case *ast.PropertyDecl:
			tctx.libraryTypes(m.Type)
		case *ast.ClassConstDecl:
			tctx.libraryTypes(m.Type)
			for _, k := range m.Consts {
				c.classConstName(k.Name.S, k.Name.Name)
			}
		}
	}
	phaenon := cl.File.Mode == lexer.ModePhaenon
	if phaenon {
		// `var` is the member's modifier: a property's, or, read by
		// position since roadmap D27, a method's, which PHP refuses.
		for _, m := range d.Members {
			switch m := m.(type) {
			case *ast.PropertyDecl:
				if m.Modifiers&ast.ModVar != 0 {
					c.report(m.Mods, P1001, "`var` is not available in Phaenon; use public")
				}
			case *ast.MethodDecl:
				if m.Modifiers&ast.ModVar != 0 {
					c.report(m.Head, P1001, "`var` is not available in Phaenon, and PHP refuses it on a method; leave it out")
				}
			}
		}
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
		ctx.inConst = true
		vt := ctx.expr(k.Value, newEnv(), k.Type)
		if k.Type != nil && !types.IsAcceptable(vt, k.Type, c.p) {
			c.report(k.Span, P2211, "constant %s::%s (%s) does not accept %s", cl.Name, k.Name, types.Print(k.Type), types.Print(vt))
		}
	}
	if cl.Kind == ast.KindEnum {
		c.checkEnum(cl)
	}
	c.checkConstOverrides(cl)
	c.checkPropertyOverrides(cl)
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
	c.checkUnusedPrivate(cl)
	c.initClasses = append(c.initClasses, cl)
}

func (c *Checker) checkAttrs(cl *program.Class, g *ast.AttrGroup) {
	ctx := c.newCtx(cl.File, cl.Scope, cl, true)
	ctx.inConst = true
	for _, a := range g.Attrs {
		name := ctx.res.ClassName(a.Name)
		ac := c.p.Class(name)
		if ac == nil {
			c.report(a.Name.S, P2001, "unknown attribute class %s", name)
			continue
		}
		c.libraryClass(a.Name.S, ac)
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
		c.libraryClass(n.Name.S, super)
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
		c.libraryClass(n.Name.S, iface)
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

// enumForbiddenMagic are the magic methods PHP refuses on an enum
// (zend_verify_enum_magic_methods); __call, __callStatic and __invoke are
// allowed, and __debugInfo since PHP 8.6.
var enumForbiddenMagic = map[string]bool{
	"__construct": true, "__destruct": true, "__clone": true, "__get": true, "__set": true, "__unset": true,
	"__isset": true, "__tostring": true, "__serialize": true, "__unserialize": true, "__sleep": true,
	"__wakeup": true, "__set_state": true,
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
	for _, m := range cl.Methods {
		if m.Magic || m.Decl == nil {
			continue
		}
		span := declSpan(m)
		if m.FromTrait != nil && cl.Decl.Name != nil {
			span = cl.Decl.Name.S // the trait is elsewhere; PHP names the enum
		}
		lower := strings.ToLower(m.Name)
		switch {
		case enumForbiddenMagic[lower]:
			c.report(span, P2514, "enum %s cannot include magic method %s()", cl.Name, m.Name)
		case lower == "__debuginfo":
			// PHP 8.6 lets an enum describe itself to var_dump()
			// (RFC debugable-enums); 8.5 refuses the method at compile time.
			c.needsPHP(span, phpversion.PHP86, fmt.Sprintf("__debugInfo() on enum %s", cl.Name), "")
		}
	}
	if cl.BackingType != nil {
		if !types.IsSubtype(cl.BackingType, types.MakeUnion(types.TInt, types.TString), nil) || types.IsMixed(cl.BackingType) {
			c.report(cl.Decl.BackingType.Span(), P2507, "enum %s must be backed by int or string", cl.Name)
		}
	}
}

// propertyTypeExpr is the declared type of the class's property name.
func propertyTypeExpr(cl *program.Class, name string) ast.TypeExpr {
	if cl.Decl == nil {
		return nil
	}
	for _, m := range cl.Decl.Members {
		if pd, ok := m.(*ast.PropertyDecl); ok {
			for _, it := range pd.Props {
				if it.Var != nil && it.Var.Name == name {
					return pd.Type
				}
			}
		}
	}
	return nil
}

func (c *Checker) checkProperty(cl *program.Class, pr *program.Property) {
	phaenon := cl.File.Mode == lexer.ModePhaenon
	if pr.NativeType == nil && phaenon && types.IsMixed(pr.Type) && !pr.Promoted {
		c.report(pr.Span, P2516, "property %s::$%s has no type", cl.Name, pr.Name)
	} else if phaenon && pr.NativeType != nil && !pr.Promoted && pr.FromTrait == nil {
		// A promoted property is its constructor parameter's (checkUntyped).
		inherited := !types.Equal(pr.Type, pr.NativeType)
		tag, native := varTag(pr.Doc), propertyTypeExpr(cl, pr.Name)
		if kind := elementless(tag, inherited, native); kind != "" {
			c.report(pr.Span, P2516, "property %s::$%s: %s", cl.Name, pr.Name, noElementMsg(kind))
			if tag == nil {
				c.noteElementless(cl.File, native, pr.Span)
			}
		}
	}
	ctx := c.newCtx(cl.File, cl.Scope, cl, pr.Static)
	ctx.inConst = true
	if pr.Readonly && pr.HasDefault && pr.Default != nil && pr.FromTrait == nil {
		// PHP 8.5 refuses the declaration at compile time. Lowering it into
		// the constructor would change hasDefaultValue(),
		// newInstanceWithoutConstructor() and unserialize()
		// (design/php-versions.md §4), so it is refused.
		c.needsPHP(pr.Default.Span(), phpversion.PHP86, fmt.Sprintf("a default for readonly property %s::$%s", cl.Name, pr.Name), "assign it in the constructor")
	}
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
	c.debugInfoReturn(m.Name, d.ReturnType)
	c.checkUntyped(cl.File, d.Params, m.Params, d.ReturnType, m.Return, m.Name, m.Span, m.Doc, m.InheritedReturn)
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
	sctx.throwsClause(d.Throws)
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
	// A signature inherited from a parent or an interface brings its
	// templates into scope: `get(string $className)` under
	// `get<T>(class<T> $className): Meta<T>` reads and returns T.
	for _, p := range m.Params {
		scopeParams(ctx, p.Type)
	}
	scopeParams(ctx, m.Return)
	ctx.pushThrows()
	ctx.trackInits, ctx.trackReturns = true, true
	end := ctx.body(m.Params, d.Params, ctx.exprBody(m.Name, d.Arrow, d.Body), d.Name.S)
	c.recordInits(d, append(ctx.returnEnvs, end))
	// A method an override may replace keeps the members its body does not
	// throw: its declaration is the contract (design/tooling.md §9.1).
	overridable := m.Vis != program.Private && !m.Final && !cl.Final && cl.Kind != ast.KindEnum
	points := ctx.popThrows()
	if ctx.c.opts.Thrown != nil {
		ctx.c.opts.Thrown(ctx.file, d, exported(points))
	}
	ctx.bodyThrows(m.Throws, overridable, ctx.name, d.Name.S, points)
}

// hasOverride reports an #[\Override] attribute among a declaration's.
func hasOverride(groups []*ast.AttrGroup) bool {
	for _, g := range groups {
		for _, a := range g.Attrs {
			if strings.EqualFold(a.Name.Parts[len(a.Name.Parts)-1], "Override") {
				return true
			}
		}
	}
	return false
}

// checkConstOverrides verifies #[\Override] on class constants and enum
// cases (PHP 8.6, RFC override_constants), as PHP does when it links the
// class: a parent class or an interface must declare a constant of that
// name, a parent's private one not counting. On an 8.5 target the emitter
// drops the attribute, which 8.5 refuses on constants; the check is all the
// attribute does, so it applies to both targets. A trait's constants are
// judged in the classes that use it, as PHP judges them.
func (c *Checker) checkConstOverrides(cl *program.Class) {
	if cl.Kind == ast.KindTrait {
		return
	}
	judge := func(name string, attrs []*ast.AttrGroup, span ast.Span, what string) {
		if !hasOverride(attrs) {
			return
		}
		parent := c.parentConstant(cl, name)
		if parent == nil {
			c.report(span, P2503, "%s %s::%s has #[\\Override] but no parent class or interface declares it", what, cl.Name, name)
			return
		}
		c.libraryClassConstant(span, parent.Class, name)
	}
	for _, k := range cl.Consts {
		if k.Class != cl {
			continue
		}
		span := k.Span
		if k.File != cl.File && cl.Decl.Name != nil {
			span = cl.Decl.Name.S // copied from a trait declared elsewhere
		}
		judge(k.Name, k.Attrs, span, "constant")
	}
	for _, m := range cl.Decl.Members {
		if cs, ok := m.(*ast.EnumCase); ok {
			judge(cs.Name.Name, cs.Attrs, cs.Name.S, "enum case")
		}
	}
}

// checkPropertyOverrides verifies #[\Override] on properties (PHP 8.5),
// promoted ones included, as PHP does when it links the class: a parent
// class must declare a property of that name that is not private, or an
// interface of the class one (with hooks), static or not. A property of
// cl's own that redeclares a trait's overrides nothing. A trait's
// properties are judged in each class using it, as PHP judges them, on
// the class's name when the trait is in another file. The attribute is the
// floor's, so the target does not matter; what a property hook's own
// #[\Override] means is a parent's hook, not checked here.
func (c *Checker) checkPropertyOverrides(cl *program.Class) {
	if cl.Kind == ast.KindTrait || cl.Kind == ast.KindEnum {
		return
	}
	for _, pr := range cl.Props {
		if pr.Class != cl || pr.Magic {
			continue
		}
		span := pr.Span
		if pr.File != cl.File && cl.Decl.Name != nil {
			span = cl.Decl.Name.S // copied from a trait declared elsewhere
		}
		parent := c.parentProperty(cl, pr.Name)
		if parent == nil {
			if hasOverride(pr.Attrs) {
				c.report(span, P2503, "property %s::$%s has #[\\Override] but no parent class or interface declares it", cl.Name, pr.Name)
			}
			continue
		}
		c.checkPropertyRedeclaration(cl, pr, parent, span)
	}
}

// checkPropertyRedeclaration holds a property that redeclares a parent
// class's to the rules PHP checks when it links the class: the same
// static-ness, the same readonly-ness, a visibility no narrower, and the
// same native type, or none when the parent declares none (Exception::$code
// is untyped, so a subclass cannot promote a typed, readonly $code). The
// first rule broken is reported, in PHP's order and words. An interface's
// property is not held here: a class implements it, with hooks of its own,
// and hooked properties follow rules of their own.
func (c *Checker) checkPropertyRedeclaration(cl *program.Class, pr, parent *program.Property, span ast.Span) {
	if parent.Class.Kind == ast.KindInterface || len(pr.Hooks) > 0 || len(parent.Hooks) > 0 {
		return
	}
	rel := related(parent.File, parent.Span, "%s::$%s declared here", parent.Class.Name, parent.Name)
	staticWord := func(static bool) string {
		if static {
			return "static"
		}
		return "non-static"
	}
	readonlyWord := func(readonly bool) string {
		if readonly {
			return "readonly"
		}
		return "non-readonly"
	}
	switch {
	case pr.Static != parent.Static:
		c.reportRelated(span, P2502, rel, "cannot redeclare %s %s::$%s as %s %s::$%s", staticWord(parent.Static), parent.Class.Name, parent.Name, staticWord(pr.Static), cl.Name, pr.Name)
	case pr.Readonly != parent.Readonly:
		c.reportRelated(span, P2502, rel, "cannot redeclare %s property %s::$%s as %s %s::$%s", readonlyWord(parent.Readonly), parent.Class.Name, parent.Name, readonlyWord(pr.Readonly), cl.Name, pr.Name)
	case pr.Vis > parent.Vis:
		c.reportRelated(span, P2502, rel, "access level to %s::$%s must be %s (as in class %s) or weaker", cl.Name, pr.Name, parent.Vis, parent.Class.Name)
	case parent.NativeType == nil && pr.NativeType != nil:
		c.reportRelated(span, P2502, rel, "type of %s::$%s must be omitted to match the parent definition in class %s", cl.Name, pr.Name, parent.Class.Name)
	case mentionsParam(parent.Type):
		// Redeclaring a generic property is P3007's finding.
	case parent.NativeType != nil:
		// PHP compares what reaches it: the native types, Phaenon's generics
		// erased.
		want := types.Erase(parent.NativeType)
		if pr.NativeType == nil || !types.Equal(want, types.Erase(pr.NativeType)) {
			c.reportRelated(span, P2502, rel, "type of %s::$%s must be %s (as in class %s)", cl.Name, pr.Name, types.Print(want), parent.Class.Name)
		}
	}
}

// parentProperty finds the property of that name a parent class or an
// interface of cl declares, a class's private property aside.
func (c *Checker) parentProperty(cl *program.Class, name string) *program.Property {
	for _, a := range cl.Ancestors() {
		a.Signature()
		for _, pr := range a.Props {
			if pr.Name != name || pr.Class != a || pr.Magic {
				continue
			}
			if pr.Vis == program.Private && a.Kind != ast.KindInterface {
				continue
			}
			return pr
		}
	}
	return nil
}

// parentConstant finds the constant of that name a parent class or an
// interface of cl declares, a class's private constant aside.
func (c *Checker) parentConstant(cl *program.Class, name string) *program.Constant {
	for _, a := range cl.Ancestors() {
		a.Signature()
		for _, k := range a.Consts {
			if k.Name != name || k.Class != a {
				continue
			}
			if k.Vis == program.Private && a.Kind != ast.KindInterface {
				continue
			}
			return k
		}
	}
	return nil
}

// checkOverride compares a method with every declaration it overrides: the
// nearest one a parent class has and each interface's. PHP links a class
// against all of them, and so do PHPStan's method compatibility rules: a
// class whose two interfaces disagree on a method fits one of them at best.
// One mistake is one diagnostic, so the first declaration the method breaks
// is the one reported.
func (c *Checker) checkOverride(cl *program.Class, m *program.Method) {
	hasOverrideAttr := hasOverride(m.Attrs)
	parents := overriddenMethods(cl, m)
	if len(parents) == 0 {
		if hasOverrideAttr {
			c.report(declSpan(m), P2503, "%s::%s() has #[\\Override] but overrides nothing", cl.Name, m.Name)
		}
		return
	}
	if hasOverrideAttr {
		// The override check runs on the target PHP: a method the target's
		// library lacks overrides nothing there (SessionHandler::validateId()
		// on 8.5).
		c.libraryMethod(declSpan(m), parents[0])
	}
	for _, parent := range parents {
		if c.checkOverrideOf(cl, m, parent) {
			break
		}
	}
}

// overriddenMethods lists what m overrides, nearest first: the first
// declaration found through the ancestors, then every other interface's.
// A parent class's private method is overridden by nothing, and a farther
// parent class's method is the business of the nearer class that overrides
// it, for PHP and for PHPStan alike.
func overriddenMethods(cl *program.Class, m *program.Method) []*program.Method {
	var out []*program.Method
	key := strings.ToLower(m.Name)
	for _, a := range cl.Ancestors() {
		a.Signature()
		pm := a.FindMethod(key)
		if pm == nil || pm.Class == cl || slices.Contains(out, pm) {
			continue
		}
		iface := pm.Class.Kind == ast.KindInterface
		if pm.Vis == program.Private && !iface {
			continue
		}
		if len(out) > 0 && (!iface || pm.Magic) {
			continue
		}
		out = append(out, pm)
	}
	return out
}

// checkOverrideOf compares a method with one it overrides and tells whether
// it reported anything.
func (c *Checker) checkOverrideOf(cl *program.Class, m, parent *program.Method) bool {
	broken := false
	mismatch := func(span ast.Span, format string, args ...any) {
		broken = true
		c.reportRelated(span, P2502, overridden(parent), format, args...)
	}
	if parent.Final {
		c.report(declSpan(m), P2505, "%s::%s() overrides final method %s::%s()", cl.Name, m.Name, parent.Class.Name, parent.Name)
	}
	if parent.Static != m.Static {
		mismatch(m.Span, "%s::%s() and %s::%s() disagree on static", cl.Name, m.Name, parent.Class.Name, parent.Name)
	}
	if m.Vis > parent.Vis {
		mismatch(m.Span, "%s::%s() must be at least %s like %s::%s()", cl.Name, m.Name, parent.Vis, parent.Class.Name, parent.Name)
	}
	if strings.EqualFold(m.Name, "__construct") && !parent.Abstract && parent.Class.Kind != ast.KindInterface {
		return broken // constructors are exempt unless the parent's is abstract
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
			mismatch(m.Span, "%s::%s() has fewer parameters than %s::%s()", cl.Name, m.Name, parent.Class.Name, parent.Name)
			break
		}
		cp := m.Params[i]
		if pp.HasDefault && !cp.HasDefault && !cp.Variadic {
			mismatch(cp.Span, "parameter $%s of %s::%s() must be optional like in %s::%s()", cp.Name, cl.Name, m.Name, parent.Class.Name, parent.Name)
		}
		pt, ct := bind(pp.Type, parent.Class), bind(cp.Type, cl)
		if pt != nil && ct != nil && !c.overrideFits(pt, ct) {
			mismatch(cp.Span, "parameter $%s of %s::%s() (%s) is not contravariant with %s::%s() (%s)",
				cp.Name, cl.Name, m.Name, types.Print(ct), parent.Class.Name, parent.Name, types.Print(pt))
		}
		if pp.ByRef != cp.ByRef {
			mismatch(cp.Span, "parameter $%s of %s::%s() disagrees on by-reference with %s::%s()", cp.Name, cl.Name, m.Name, parent.Class.Name, parent.Name)
		}
	}
	for i := len(parent.Params); i < len(m.Params); i++ {
		if !m.Params[i].HasDefault && !m.Params[i].Variadic {
			broken = true
			c.report(m.Params[i].Span, P2502, "extra parameter $%s of %s::%s() must be optional", m.Params[i].Name, cl.Name, m.Name)
		}
	}
	if parent.Return != nil {
		pr, cr := bind(parent.Return, parent.Class), bind(m.Return, cl)
		if cr == nil {
			if _, isVoid := pr.(types.Void); !isVoid && !types.IsMixed(pr) {
				mismatch(m.Span, "%s::%s() must declare a return type compatible with %s::%s() (%s)", cl.Name, m.Name, parent.Class.Name, parent.Name, types.Print(pr))
			}
		} else if !c.overrideFits(cr, pr) {
			if _, parentVoid := pr.(types.Void); !(parentVoid && isVoidLike(cr)) {
				mismatch(m.Span, "return type %s of %s::%s() is not covariant with %s::%s() (%s)",
					types.Print(cr), cl.Name, m.Name, parent.Class.Name, parent.Name, types.Print(pr))
			}
		}
	}
	return broken
}

// overrideFits reports whether sub may stand where an overridden method
// declares super: the child's return type for the parent's, the parent's
// parameter type for the child's. PHP's own rule is on the types it sees,
// the erased ones, and it is kept whole. Phaenon's types say more
// (`list<int>` and `list<string>` are both PHP's array), and there the
// override is wrong when the two are certainly apart: what PHPStan reports
// as method.childParameterType and method.childReturnType. A type that
// only may fit passes, as it does there. A child that writes
// `map<string, mixed>` where a vendor parent has a bare `array`, or
// `class<User>` for its `class-string`, narrows what the parent left
// open, and code that works is full of it.
func (c *Checker) overrideFits(sub, super types.Type) bool {
	if types.IsSubtype(sub, super, c.p) {
		return true
	}
	if !types.IsSubtype(types.Erase(sub), types.Erase(super), c.p) {
		return false
	}
	return !types.NeverSubtype(sub, super, c.p)
}

func isVoidLike(t types.Type) bool {
	switch t.(type) {
	case types.Void, types.Never:
		return true
	}
	return false
}

// overridden is "declared here" for the method an override breaks.
func overridden(m *program.Method) *Related {
	span := m.Span
	if m.Decl != nil && m.Decl.Name != nil {
		span = m.Decl.Name.S
	}
	return related(methodFile(m), span, "%s::%s() declared here", m.Class.Name, m.Name)
}
