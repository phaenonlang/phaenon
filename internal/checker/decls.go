package checker

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
	"github.com/phaenonlang/phaenon/stdlib"
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
			c.reportRefused(cl.File == f && declaredOnLoad(f, cl.Decl) && declaredOnLoad(f, d), d.S, P2511, nil, "class %s is already declared%s", cl.Name, declaredAt(f, cl.File, cl.Span))
		}
		return
	}
	c.checkClass(cl)
}

// linkedOnLoad reports a class that PHP links, against its parent, its
// interfaces and its traits, whenever it loads the file that declares it
// (declaredOnLoad), and whose ancestry the checker knows as the target's
// PHP has it: what PHP refuses when it links such a class, the checker
// knows it refuses. A class declared under a condition, in a function's
// body or after a statement that may end the file's run is linked when
// the run reaches it, if it does, an anonymous class when its `new` runs,
// and what PHP refuses there fails that code alone. An ancestor the
// checker does not know, or one the target's library lacks, may be
// another class at run time, which provides what the checker misses.
func (c *Checker) linkedOnLoad(cl *program.Class) bool {
	return cl.Decl != nil && declaredOnLoad(cl.File, cl.Decl) && c.ancestryKnown(cl)
}

// ancestryKnown reports a class whose parent classes, interfaces and
// traits, and theirs in turn, are all classes the checker knows and the
// target's PHP declares.
func (c *Checker) ancestryKnown(cl *program.Class) bool {
	seen := map[*program.Class]bool{}
	var known func(cl *program.Class) bool
	known = func(cl *program.Class) bool {
		if seen[cl] {
			return true
		}
		seen[cl] = true
		names := append(append([]string{}, cl.Interfaces...), cl.Traits...)
		if cl.Parent != "" {
			names = append(names, cl.Parent)
		}
		for _, name := range names {
			a := c.p.Class(name)
			if a == nil || !c.classOnTarget(a) || !known(a) {
				return false
			}
		}
		return true
	}
	return known(cl)
}

// classOnTarget reports a class the target's PHP declares: any but one of
// PHP's library that a newer release added.
func (c *Checker) classOnTarget(cl *program.Class) bool {
	if cl.File == nil || !cl.File.IsStub() {
		return true
	}
	v, newer := stdlib.ClassSince(cl.Name)
	return !newer || c.target().AtLeast(v)
}

// methodOnTarget reports a method the target's PHP declares: any but one
// of PHP's library that a newer release added (SessionHandler::validateId()
// came in 8.6), which on the target is no method to override.
func (c *Checker) methodOnTarget(m *program.Method) bool {
	if m.Class == nil || !c.classOnTarget(m.Class) {
		return false
	}
	if m.Class.File == nil || !m.Class.File.IsStub() {
		return true
	}
	v, newer := stdlib.MethodSince(m.Class.Name, m.Name)
	return !newer || c.target().AtLeast(v)
}

// nativeFinal reports a class declared `final`, which PHP refuses to
// extend; a docblock's @final asks the same of PHPStan alone.
func nativeFinal(cl *program.Class) bool {
	return cl.Decl != nil && cl.Decl.Modifiers&ast.ModFinal != 0
}

// declaredOnLoad reports a class declaration that PHP declares whenever it
// loads the file: a statement of the file, or of a namespace in it, with
// nothing before it but declarations. PHP declares a class that implements
// an interface, uses a trait or extends a class it cannot bind when it
// compiles the file only when the run reaches it, and any other statement
// before it may end the run first: a `return`, an `exit`, a `throw`, a
// call that does. A second such declaration of one name in a file is
// refused when the file is loaded (P2511); one in another file, or under a
// condition, may never be loaded with it, as a polyfill is not.
func declaredOnLoad(f *program.File, decl ast.Stmt) bool {
	found, clear := false, true
	var walk func(stmts []ast.Stmt) bool
	walk = func(stmts []ast.Stmt) bool {
		for _, s := range stmts {
			if s == decl {
				found = true
				return true
			}
			switch s := s.(type) {
			case *ast.Namespace:
				if walk(s.Body) {
					return true
				}
			case *ast.Declare:
				clear = clear && s.Body == nil
			case *ast.Use, *ast.ConstDecl, *ast.FunctionDecl, *ast.ClassDecl, *ast.TypeAlias,
				*ast.OpenTag, *ast.CloseTag, *ast.InlineHTML, *ast.Nop:
			default:
				clear = false
			}
		}
		return false
	}
	if f.AST != nil {
		walk(f.AST.Stmts)
	}
	return found && clear
}

// topLevel reports a declaration that is a statement of the file, or of a
// namespace in it. PHP declares such a function when it compiles the
// file, whatever runs before it, and refuses a second one of its name
// there (P2511); one in another file, or under a condition, may never be
// loaded with it, as a polyfill is not.
func topLevel(f *program.File, decl ast.Stmt) bool {
	var in func(stmts []ast.Stmt) bool
	in = func(stmts []ast.Stmt) bool {
		for _, s := range stmts {
			if s == decl {
				return true
			}
			if ns, ok := s.(*ast.Namespace); ok && in(ns.Body) {
				return true
			}
		}
		return false
	}
	return f.AST != nil && in(f.AST.Stmts)
}

// declaredAt says, for a second declaration in the file here, where the
// first one is, and when one of the two files opens with the emitter's
// header, that it looks like a compiled output left behind: where outputs
// are written beside their sources, one whose .phn was renamed or removed
// stays, declares what the renamed file declares, and is the file an
// autoloader finds under the old name. Such a file is plain PHP to the
// checker, as an output that someone took over and edits is. Empty where
// the first declaration has no file.
func declaredAt(here, there *program.File, span ast.Span) string {
	if there == nil {
		return ""
	}
	line, _ := there.Position(span.Start)
	at := fmt.Sprintf(" (%s:%d)", there.Path, line)
	if there == here {
		return at
	}
	switch {
	case bytes.HasPrefix(there.Src, []byte(generatedHeader)):
		at += ": that file looks like a compiled output left behind, its .phn renamed or removed"
	case bytes.HasPrefix(here.Src, []byte(generatedHeader)):
		at += ": this file looks like a compiled output left behind, its .phn renamed or removed"
	}
	return at
}

// generatedHeader is how a file the emitter wrote opens: the open tag, a
// blank line and the marker's comment (emit.Marker, which this package
// cannot import: the emitter's tests check their goldens with the checker;
// TestGeneratedHeader holds the two together).
const generatedHeader = "<?php\n\n/* @generated by phaenon"

// checkFunctionDecl checks a top-level function declaration.
func (c *Checker) checkFunctionDecl(f *program.File, scope *program.Scope, d *ast.FunctionDecl) {
	fn := c.p.Function(scope.Prefix() + d.Name.Name)
	if fn == nil || fn.Decl != d {
		if fn != nil {
			at := ""
			if fn.Decl != nil {
				at = declaredAt(f, fn.File, fn.Decl.S)
			}
			c.reportRefused(fn.File == f && fn.Decl != nil && topLevel(f, fn.Decl), d.S, P2511, nil, "function %s() is already declared%s", fn.Name, at)
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
	ctx.throwsClause(fn.Decl.Throws, fn.Doc, fn.Decl.Name.S)
	c.declaredName(fn.Decl.Name.S, "function", fn.Decl.Name.Name)
	c.checkUntyped(f, fn.Decl.Params, fn.Params, fn.Decl.ReturnType, fn.Return, fn.Decl.Name.Name, fn.Decl.S, fn.Doc, false)
	ctx.pushThrows()
	ctx.declHead = ast.Span{Start: fn.Decl.S.Start, End: fn.Decl.Name.S.End}
	ctx.body(fn.Params, fn.Decl.Params, ctx.exprBody(fn.Decl.Name.Name, fn.Decl.Arrow, fn.Decl.Body), fn.Decl.Name.S)
	points := ctx.popThrows()
	if c.opts.Thrown != nil {
		c.opts.Thrown(f, fn.Decl, exported(points))
	}
	ctx.bodyThrows(fn.Throws, "declares "+voidSpelling(fn.Decl.Throws), false, ctx.name, fn.Decl.Name.S, points)
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
	if line, _ := c.file.Position(at.Start); c.silencing(c.file, line, P2516.ID) != nil {
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
		if l, otherCase := program.OtherCaseTypeName(n.Name); len(n.Args) == 0 && !otherCase {
			switch l {
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
	c.seenLog = append(c.seenLog, key)
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
			c.reportRefused(c.linkedOnLoad(cl), d.S, P2501, nil, "class %s must implement %s::%s()", cl.Name, m.Class.Name, m.Name)
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
		cyclic := c.selfReference(valueDecl{k: k}, cl)
		mark := c.mark()
		ctx := c.newCtx(cl.File, cl.Scope, cl, true)
		ctx.inConst = true
		vt := ctx.expr(k.Value, newEnv(), k.Type)
		if cyclic {
			// The value reads itself, the mistake reported: what its type
			// draws is drawn from a value PHP never makes.
			c.rollback(mark)
			continue
		}
		if k.Type != nil && !types.IsAcceptable(vt, k.Type, c.p) {
			// A @var tag's type is no native one.
			refused := !k.Doc && literalRefused(k.Value, k.Type, false)
			c.reportUnfitRefused(refused, given{expr: k.Value}, vt, k.Type, k.Span, P2211, nil, "constant %s::%s (%s) does not accept %s", cl.Name, k.Name, types.Print(k.Type), types.Print(vt))
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
				c.reportRefused(!m.Magic, m.Span, P2514, nil, "interface method %s::%s() must be public", cl.Name, m.Name)
			}
			if m.Decl != nil && m.Decl.HasBody {
				c.reportRefused(true, m.Span, P2514, nil, "interface method %s::%s() cannot have a body", cl.Name, m.Name)
			}
		}
		for _, pr := range cl.Props {
			if len(pr.Hooks) == 0 {
				// A @property tag is none of PHP's business.
				c.reportRefused(!pr.Magic && pr.Class == cl, pr.Span, P2514, nil, "interface %s cannot declare property $%s", cl.Name, pr.Name)
			}
		}
	}
	if cl.Readonly {
		for _, pr := range cl.Props {
			if pr.Class != cl {
				continue
			}
			// The class's own property PHP refuses when it compiles the
			// file; a trait's, when it links the class.
			refused := !pr.Magic && (pr.FromTrait == nil || c.linkedOnLoad(cl))
			if pr.Static {
				c.reportRefused(refused, pr.Span, P2513, nil, "readonly class %s cannot declare static property $%s", cl.Name, pr.Name)
			}
			if pr.NativeType == nil {
				c.reportRefused(refused, pr.Span, P2513, nil, "readonly class %s: property $%s must have a type", cl.Name, pr.Name)
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
			c.report(a.Name.S, P2001, "%s", c.unknownClass("attribute class", name))
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
			c.report(n.S, P2001, "%s", c.unknownClass("class", name))
			continue
		}
		c.libraryClass(n.Name.S, super)
		if cl.Kind == ast.KindInterface {
			if super.Kind != ast.KindInterface {
				c.reportRefused(c.linkedOnLoad(cl), n.S, P2510, nil, "interface %s cannot extend %s, which is not an interface", cl.Name, super.Name)
			}
		} else {
			if i == 0 && super.Kind != ast.KindClass {
				c.reportRefused(c.linkedOnLoad(cl), n.S, P2510, nil, "class %s cannot extend %s, which is not a class", cl.Name, super.Name)
			}
			if super.Final {
				c.reportRefused(nativeFinal(super) && c.linkedOnLoad(cl), n.S, P2509, nil, "class %s cannot extend final class %s", cl.Name, super.Name)
			}
			if super.IsSubclassOf(cl.Name) && !strings.EqualFold(super.Name, cl.Name) {
				// No order of declaring the two links either: the one
				// declared first finds the other missing.
				c.reportRefused(c.linkedOnLoad(cl), n.S, P2508, nil, "circular inheritance between %s and %s", cl.Name, super.Name)
			}
		}
	}
	for _, n := range d.Implements {
		name := ctx.res.ClassName(n.Name)
		iface := c.p.Class(name)
		if iface == nil {
			c.report(n.S, P2001, "%s", c.unknownClass("interface", name))
			continue
		}
		c.libraryClass(n.Name.S, iface)
		if iface.Kind != ast.KindInterface {
			c.reportRefused(c.linkedOnLoad(cl), n.S, P2510, nil, "%s cannot implement %s, which is not an interface", cl.Name, iface.Name)
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
				c.report(n.S, P2001, "%s", c.unknownClass("trait", name))
				continue
			}
			if t.Kind != ast.KindTrait {
				c.reportRefused(c.linkedOnLoad(cl), n.S, P2510, nil, "%s is not a trait", t.Name)
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
					// The same method brought by both, from a trait they use,
					// is no collision to PHP.
					theirs := other.FindMethod(m.Name)
					same := theirs != nil && theirs.Decl == m.Decl
					c.reportRefused(!same && c.linkedOnLoad(cl), traitUseSpan(cl), P2506, nil, "trait method %s() is defined in both %s and %s", m.Name, other.Name, t.Name)
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
			c.reportRefused(true, cs.Span, P2511, nil, "duplicate enum case %s::%s", cl.Name, cs.Name)
		}
		seen[cs.Name] = true
		if cl.BackingType == nil {
			if cs.Value != nil {
				c.reportRefused(true, cs.Span, P2507, nil, "case %s of non-backed enum %s cannot have a value", cs.Name, cl.Name)
			}
			continue
		}
		if cs.Value == nil {
			c.reportRefused(true, cs.Span, P2507, nil, "case %s of backed enum %s needs a value", cs.Name, cl.Name)
			continue
		}
		cyclic := c.selfReference(valueDecl{cs: cs}, cl)
		mark := c.mark()
		typing := !c.caseTyping[cs]
		if typing {
			if c.caseTyping == nil {
				c.caseTyping = map[*program.EnumCase]bool{}
			}
			c.caseTyping[cs] = true
		}
		vt := ctx.expr(cs.Value, newEnv(), cl.BackingType)
		if typing {
			delete(c.caseTyping, cs)
		}
		if cyclic {
			// The value reads itself, the mistake reported: what its type
			// draws is drawn from a value PHP never makes.
			c.rollback(mark)
			continue
		}
		if !types.IsSubtype(vt, cl.BackingType, c.p) {
			c.report(cs.Value.Span(), P2507, "case %s::%s has value %s, expected %s", cl.Name, cs.Name, types.Print(vt), types.Print(cl.BackingType))
		}
	}
	for _, pr := range cl.Props {
		if pr.Class == cl && !pr.Magic {
			// Its own property PHP refuses when it compiles the file; a
			// trait's, when it declares the enum.
			c.reportRefused(pr.FromTrait == nil || c.linkedOnLoad(cl), pr.Span, P2514, nil, "enum %s cannot declare property $%s", cl.Name, pr.Name)
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
			// PHP refuses it when it declares the enum.
			c.reportRefused(c.linkedOnLoad(cl), span, P2514, nil, "enum %s cannot include magic method %s()", cl.Name, m.Name)
		case lower == "__debuginfo":
			// PHP 8.6 lets an enum describe itself to var_dump()
			// (RFC debugable-enums); 8.5 refuses the method at compile time.
			c.needsPHP(span, phpversion.PHP86, fmt.Sprintf("__debugInfo() on enum %s", cl.Name), "")
		}
	}
	if cl.BackingType != nil {
		if !types.IsSubtype(cl.BackingType, types.MakeUnion(types.TInt, types.TString), nil) || types.IsMixed(cl.BackingType) {
			c.reportRefused(true, cl.Decl.BackingType.Span(), P2507, nil, "enum %s must be backed by int or string", cl.Name)
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
			refused := literalRefused(pr.Default, pr.NativeType, false)
			c.reportUnfitRefused(refused, given{expr: pr.Default}, dt, pr.Type, pr.Default.Span(), P2201, nil, "property %s::$%s (%s) does not accept default %s", cl.Name, pr.Name, types.Print(pr.Type), types.Print(dt))
		}
	}
	for _, h := range pr.Hooks {
		hctx := c.newCtx(cl.File, cl.Scope, cl, false)
		hctx.name = cl.Name + "::$" + pr.Name + "::" + h.Name.Name
		env := newEnv()
		switch strings.ToLower(h.Name.Name) {
		case "get":
			te := propertyTypeExpr(cl, pr.Name)
			hctx.hookRefusesYield = te != nil && !hctx.generatorTypeAdmitted(te, true) && hctx.typeWritten(te)
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
			hctx.hookRefusesYield = true // a set hook returns void
			hctx.returnType = types.TVoid
			hctx.hasReturn = true
			vt := pr.Type
			if len(h.Params) > 0 {
				if pt := hctx.res.Native(h.Params[0].Type); pt != nil {
					vt = pt
					if !types.IsSubtype(pr.Type, pt, c.p) {
						// PHP compares the native types the compiled file
						// writes, Phaenon's and the docblock's erased.
						refused := pr.NativeType != nil && !mentionsParam(pr.NativeType) && !mentionsParam(pt) &&
							!types.IsSubtype(phpType(pr.NativeType), phpType(pt), c.p)
						c.reportRefused(refused, h.Params[0].S, P2512, nil, "set hook parameter of %s::$%s must accept %s", cl.Name, pr.Name, types.Print(pr.Type))
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
		c.reportRefused(true, declSpan(m), P2504, nil, "abstract method %s::%s() in a non-abstract class", cl.Name, m.Name)
	}
	if m.Abstract && d.HasBody && cl.Kind != ast.KindInterface {
		c.reportRefused(true, m.Span, P2504, nil, "abstract method %s::%s() cannot have a body", cl.Name, m.Name)
	}
	c.checkOverride(cl, m)
	c.debugInfoReturn(m.Name, d.ReturnType)
	c.checkUntyped(cl.File, d.Params, m.Params, d.ReturnType, m.Return, m.Name, m.Span, m.Doc, m.InheritedReturn)
	if phaenon {
		for _, p := range d.Params {
			if p.Modifiers != 0 && cl.Kind == ast.KindInterface {
				c.reportRefused(true, p.S, P2514, nil, "interface %s cannot promote constructor properties", cl.Name)
			}
		}
	}
	sctx := c.newCtx(cl.File, cl.Scope, cl, m.Static)
	sctx.res = sctx.res.WithParams(m.TypeParams)
	sctx.checkSignatureTypes(d.TypeParams, d.Params, d.ReturnType)
	sctx.throwsClause(d.Throws, m.Doc, d.Name.S)
	if !d.HasBody {
		return
	}
	ctx := c.newCtx(cl.File, cl.Scope, cl, m.Static)
	ctx.method = m
	ctx.returnExpr = d.ReturnType
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
	ctx.declHead = ast.Span{Start: d.S.Start, End: d.Name.S.End}
	end := ctx.body(m.Params, d.Params, ctx.exprBody(m.Name, d.Arrow, d.Body), d.Name.S)
	c.recordInits(d, append(ctx.returnEnvs, end))
	// A method an override may replace keeps the members its body does not
	// throw: its declaration is the contract (design/tooling.md §9.1).
	overridable := m.Vis != program.Private && !m.Final && !cl.Final && cl.Kind != ast.KindEnum
	points := ctx.popThrows()
	if ctx.c.opts.Thrown != nil {
		ctx.c.opts.Thrown(ctx.file, d, exported(points))
	}
	held, voidSays := heldThrows(m)
	ctx.bodyThrows(held, voidSays, overridable, ctx.name, d.Name.S, points)
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
			// Compiled for 8.5, the attribute is left out; 8.6 refuses it
			// when it links the class.
			c.reportRefused(c.target().AtLeast(phpversion.PHP86) && c.linkedOnLoad(cl), span, P2503, nil, "%s %s::%s has #[\\Override] but no parent class or interface declares it", what, cl.Name, name)
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
				c.reportRefused(c.linkedOnLoad(cl), span, P2503, nil, "property %s::$%s has #[\\Override] but no parent class or interface declares it", cl.Name, pr.Name)
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
		c.reportRefused(c.linkedOnLoad(cl), span, P2502, rel, "cannot redeclare %s %s::$%s as %s %s::$%s", staticWord(parent.Static), parent.Class.Name, parent.Name, staticWord(pr.Static), cl.Name, pr.Name)
	case pr.Readonly != parent.Readonly:
		c.reportRefused(c.linkedOnLoad(cl), span, P2502, rel, "cannot redeclare %s property %s::$%s as %s %s::$%s", readonlyWord(parent.Readonly), parent.Class.Name, parent.Name, readonlyWord(pr.Readonly), cl.Name, pr.Name)
	case pr.Vis > parent.Vis:
		c.reportRefused(c.linkedOnLoad(cl), span, P2502, rel, "access level to %s::$%s must be %s (as in class %s) or weaker", cl.Name, pr.Name, parent.Vis, parent.Class.Name)
	case parent.NativeType == nil && pr.NativeType != nil:
		c.reportRefused(c.linkedOnLoad(cl), span, P2502, rel, "type of %s::$%s must be omitted to match the parent definition in class %s", cl.Name, pr.Name, parent.Class.Name)
	case mentionsParam(parent.Type):
		// Redeclaring a generic property is P3007's finding.
	case parent.NativeType != nil:
		// PHP compares what reaches it: the native types, Phaenon's generics
		// erased.
		want := types.Erase(parent.NativeType)
		if pr.NativeType == nil || !types.Equal(want, types.Erase(pr.NativeType)) {
			c.reportRefused(c.linkedOnLoad(cl), span, P2502, rel, "type of %s::$%s must be %s (as in class %s)", cl.Name, pr.Name, types.Print(want), parent.Class.Name)
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

// constructorBound is the constructor whose visibility a constructor of cl
// must keep, and the class whose method table holds it; nil when there is
// none. PHP lets a constructor narrow the visibility of the one it
// overrides (a private constructor in a subclass of Exception) unless an
// abstract declaration fixes it: zend_inheritance.c checks a constructor
// only against its prototype, which only an abstract one sets. An
// interface's constructor keeps every constructor below it public.
// Otherwise the constructor in the parent's method table decides
// (program's TableEntry): when it is abstract, or implements an abstract
// one in turn (AbstractConstructor), cl's must be at least as visible as
// it is. A private one that is not abstract demands no visibility: PHP
// does go on to check a constructor against a private one, where it
// passes over any other private method, but for final alone, nothing
// being narrower than private.
func (c *Checker) constructorBound(cl *program.Class) (*program.Method, *program.Class) {
	const key = "__construct"
	for _, a := range cl.Ancestors() {
		if a.Kind != ast.KindInterface {
			continue
		}
		a.Signature()
		if im := a.FindMethod(key); im != nil && im.Class.Kind == ast.KindInterface {
			return im, im.Class
		}
	}
	parent := cl.ParentClass()
	if parent == nil {
		return nil, nil
	}
	entry, at := parent.TableEntry(key)
	if entry == nil || entry.Vis == program.Private && !entry.Abstract {
		return nil, nil
	}
	if entry.Abstract || cl.AbstractConstructor() != nil {
		return entry, at
	}
	return nil, nil
}

// checkOverride compares a method with every declaration it overrides: the
// nearest one a parent class has and each interface's. PHP links a class
// against all of them, and so do PHPStan's method compatibility rules: a
// class whose two interfaces disagree on a method fits one of them at best.
// One mistake is one diagnostic, so the first declaration the method breaks
// is the one reported. An extra check ends nothing: the declarations after
// the one that drew it are still compared, for what PHP refuses and PHPStan
// reports, and only another extra check is left unsaid there. Otherwise a
// static method whose documented types break its parent's would hide the
// interface its native types break, and with the extra checks off nothing
// would be reported of a class PHP cannot declare.
func (c *Checker) checkOverride(cl *program.Class, m *program.Method) {
	hasOverrideAttr := hasOverride(m.Attrs)
	parents := overriddenMethods(cl, m)
	if len(parents) == 0 {
		if hasOverrideAttr {
			// A trait's method is judged in the classes that use it, if any.
			c.reportRefused(cl.Kind != ast.KindTrait && c.linkedOnLoad(cl), declSpan(m), P2503, nil, "%s::%s() has #[\\Override] but overrides nothing", cl.Name, m.Name)
		}
		return
	}
	if hasOverrideAttr {
		// The override check runs on the target PHP: a method the target's
		// library lacks overrides nothing there (SessionHandler::validateId()
		// on 8.5).
		c.libraryMethod(declSpan(m), parents[0])
	}
	extra := false
	for i, parent := range parents {
		broken, drewExtra := c.checkOverrideOf(cl, m, parent, i == 0, extra)
		if broken {
			break
		}
		extra = extra || drewExtra
	}
}

// overriddenMethods lists what m overrides: the nearest declaration up the
// parent classes, then every interface's in the order of the ancestors.
// The class's comes first even where an interface sits nearer, on a parent
// that inherits the method it declares: the parent's method under that
// name is still the class's, and PHP checks the override against it. A
// parent class's private method is overridden by nothing, and a farther
// parent class's method is the business of the nearer class that
// overrides it, for PHP and for PHPStan alike. An interface's @method tag,
// which PHP never sees, is compared only when nothing comes before it.
func overriddenMethods(cl *program.Class, m *program.Method) []*program.Method {
	key := strings.ToLower(m.Name)
	var class *program.Method
	var ifaces []*program.Method
	for _, a := range cl.Ancestors() {
		a.Signature()
		pm := a.FindMethod(key)
		if pm == nil || pm.Class != a {
			continue // a declares none of its own; its ancestors are visited too
		}
		switch {
		case a.Kind == ast.KindInterface:
			ifaces = append(ifaces, pm)
		case class == nil && pm.Vis != program.Private:
			class = pm
		}
	}
	var out []*program.Method
	if class != nil {
		out = append(out, class)
	}
	for _, pm := range ifaces {
		if len(out) > 0 && pm.Magic {
			continue
		}
		out = append(out, pm)
	}
	return out
}

// constructorSignature is the constructor whose signature an override of
// parent, the nearest constructor up the parent classes or an interface's,
// is held to, or nil when none is. PHP leaves a constructor free of a
// concrete parent's signature, not of the abstract constructor that one
// implements: the nearest abstract constructor up the parent classes,
// however far up it sits. PHP takes the top-most one, which differs only
// where a nearer abstract constructor widened a farther one; there the
// override is held to the nearer, as PHPStan holds it to the nearest
// constructor. An interface's constructor binds its own signature. The
// final and visibility checks stay the nearest constructor's.
func constructorSignature(parent *program.Method) *program.Method {
	if parent.Abstract || parent.Class.Kind == ast.KindInterface {
		return parent
	}
	seen := map[*program.Class]bool{}
	for c := parent.Class; c != nil && !seen[c]; c = c.ParentClass() {
		seen[c] = true
		c.Signature()
		if pm := c.FindMethod("__construct"); pm != nil && pm.Class == c && pm.Abstract {
			return pm
		}
	}
	return nil
}

// checkOverrideOf compares a method with one it overrides. broken says that
// it reported what PHP refuses or PHPStan reports, which ends the
// comparison with the declarations after this one (checkOverride); extra,
// that it drew an extra check, which ends nothing. first marks the first
// declaration of the list, and afterExtra one that comes after a
// declaration that drew an extra check: another is not reported there.
func (c *Checker) checkOverrideOf(cl *program.Class, m, parent *program.Method, first, afterExtra bool) (broken, extra bool) {
	// refused is what PHP refuses when it links the class on load: an
	// override of a method it declares (an interface's @method tag is
	// none), whose signatures, as the compiled file writes them, do not
	// fit.
	refused := !parent.Magic && c.methodOnTarget(parent) && c.linkedOnLoad(cl)
	mismatch := func(refused bool, span ast.Span, format string, args ...any) {
		broken = true
		c.reportRefused(refused, span, P2502, overridden(parent), format, args...)
	}
	// typeMismatch is mismatch for two types that do not fit as the method
	// states them. For a static method it is an extra check where the
	// native types the source writes fit as PHP compares them
	// (nativesFit): a caller typed against the parent is given what the
	// child documents. The proof compares declarations and no value: php
	// declares the class, and PHPStan compares the documented types of
	// static methods only with reportStaticMethodSignatures, which level
	// max does not set.
	//
	// Where the written native types are apart the finding is a parity
	// finding, static or not, and stays with the extra checks off, though
	// php may well declare the compiled class: the emitter rewrites an
	// overriding method's native types until they fit what they override
	// (program.Method.ErasureOverrides), `(): iterable` over `(): array`
	// compiling to `(): array`. The body is checked against the type the
	// source writes, so this finding is all that stands between a body
	// that returns a generator and a compiled method that refuses it. It
	// is a rule about the source, which the compiled output no longer
	// shows.
	//
	// PHP refuses the class where the native type the compiled file writes
	// at the slot, a parameter's index or -1 for the return, does not fit
	// (compiledApart). Where it fits because the rule rewrote it, the
	// compiled method is another than the source writes: `(): iterable`
	// compiled to `(): array` refuses the generator its body returns, and
	// `(string $x)` compiled to `(int|string $x)` hands the body an int it
	// was checked without. No comment silences either (compiledOtherwise):
	// a comment accepts a finding about what the code does, and here the
	// compiled code does something else than the code that was checked.
	// A type that compiles as written is a comment's to accept: one a
	// docblock or Phaenon's syntax alone narrows, and a native type the
	// erased declaration admits though its type argument does not
	// (`get(): string` under `Held<int>`, whose compiled `get(): mixed` it
	// fits).
	typeMismatch := func(nativesFit, compiledApart bool, slot int, span ast.Span, format string, args ...any) {
		if !m.Static || !parent.Static || !nativesFit {
			_, rewritten := m.ErasureOverrides()[slot]
			why := mayBeSilenced
			switch {
			case refused && compiledApart:
				why = refusedByPHP
			case rewritten && !nativesFit:
				why = compiledOtherwise
			}
			broken = true
			c.reportOf(Parity, why, span, P2502, overridden(parent), format, args...)
			return
		}
		if !afterExtra {
			c.reportExtraRelated(span, P2502, overridden(parent), format, args...)
		}
		extra = true
	}
	if parent.Final {
		c.reportRefused(refused, declSpan(m), P2505, nil, "%s::%s() overrides final method %s::%s()", cl.Name, m.Name, parent.Class.Name, parent.Name)
	}
	if parent.Static != m.Static {
		mismatch(refused, m.Span, "%s::%s() and %s::%s() disagree on static", cl.Name, m.Name, parent.Class.Name, parent.Name)
	}
	ctor := strings.EqualFold(m.Name, "__construct")
	switch {
	case ctor && first:
		// A constructor's visibility is held once, to the constructor
		// constructorBound finds, not to each declaration in turn: PHP
		// lets it narrow a concrete parent's unless an abstract
		// declaration fixes it.
		if by, at := c.constructorBound(cl); by != nil && m.Vis > by.Vis {
			broken = true
			c.reportRefused(!by.Magic && c.methodOnTarget(by) && c.linkedOnLoad(cl), m.Span, P2502, overridden(by), "%s::%s() must be at least %s like %s::%s()", cl.Name, m.Name, by.Vis, at.Name, by.Name)
		}
	case !ctor && m.Vis > parent.Vis:
		mismatch(refused, m.Span, "%s::%s() must be at least %s like %s::%s()", cl.Name, m.Name, parent.Vis, parent.Class.Name, parent.Name)
	}
	if ctor {
		if parent = constructorSignature(parent); parent == nil {
			return broken, extra // a concrete constructor binds no signature
		}
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
	// nativesFit is PHP's own rule of an override, on the native types as
	// the source writes them (written). sub stands where super is
	// declared: the child's return type for the parent's, the parent's
	// parameter type for the child's. A type that is not written is no
	// demand where it is super; where it is sub, a missing return type
	// fits no declared one (PHP: "must be compatible with …(): array"),
	// and a parameter without a type, which takes anything, fits only
	// mixed.
	nativesFit := func(sub, super types.Type, isReturn bool) bool {
		switch {
		case super == nil:
			return true
		case sub == nil:
			return !isReturn && types.IsMixed(phpType(super))
		}
		return types.IsSubtype(phpType(sub), phpType(super), c.p)
	}
	// written is the native type the source writes at a slot of a method,
	// a parameter's index or -1 for the return, as PHP would hold it: for
	// a parameter, with null under a default of null, which makes
	// `array $x = null` a `?array $x`.
	written := func(of *program.Method, decl *program.Class, slot int) types.Type {
		if slot < 0 {
			return bind(of.NativeReturn, decl)
		}
		p := of.Params[slot]
		t := bind(p.NativeType, decl)
		if t != nil && p.Default != nil && isNullConst(p.Default) && !types.IsNullable(t) && !types.IsMixed(t) {
			t = types.Nullable(t)
		}
		return t
	}
	// compiled is the native type the compiled file writes at a slot of a
	// method, a parameter's index or -1 for the return, as PHP holds it:
	// the one written, erased of what PHP does not read (phpType: a type
	// parameter is its bound, whatever argument a subclass passes it), or
	// the one the override-erasure rule rewrote it to; a parameter with a
	// default of null takes null too.
	compiled := func(of *program.Method, slot int) types.Type {
		var t types.Type
		if slot < 0 {
			t = of.NativeReturn
		} else {
			t = of.Params[slot].NativeType
		}
		if w, ok := of.ErasureOverrides()[slot]; ok {
			t = w
		} else if t != nil {
			t = phpType(t)
		}
		if slot >= 0 && t != nil {
			if p := of.Params[slot]; p.Default != nil && isNullConst(p.Default) && !types.IsNullable(t) && !types.IsMixed(t) {
				t = types.Nullable(t)
			}
		}
		return t
	}
	// compiledApart says whether the type the compiled file writes at a
	// slot of m fails a declaration m overrides, as the compiled file
	// writes that one: m's own type, or the one the override-erasure rule
	// rewrote it to, which fits one declaration and may then fail another.
	// PHP refuses the class for it when it links it. A declaration of PHP's
	// library is passed over, whose return type may be tentative, a
	// deprecation alone, and so is a @method tag.
	compiledApart := func(slot int) bool {
		own := compiled(m, slot)
		decls := overriddenMethods(cl, m)
		if ctor {
			decls = []*program.Method{parent}
		}
		for _, d := range decls {
			switch {
			case d.Magic || ofPHP(d.Class) || !c.methodOnTarget(d):
			case slot < 0 && !nativesFit(own, compiled(d, -1), true):
				return true
			case slot >= 0 && slot < len(d.Params) && !nativesFit(compiled(d, slot), own, false):
				return true
			}
		}
		return false
	}
	for i, pp := range parent.Params {
		if i >= len(m.Params) {
			if n := len(m.Params); n > 0 && m.Params[n-1].Variadic {
				break
			}
			mismatch(refused, m.Span, "%s::%s() has fewer parameters than %s::%s()", cl.Name, m.Name, parent.Class.Name, parent.Name)
			break
		}
		cp := m.Params[i]
		if pp.HasDefault && !cp.HasDefault && !cp.Variadic {
			mismatch(refused, cp.Span, "parameter $%s of %s::%s() must be optional like in %s::%s()", cp.Name, cl.Name, m.Name, parent.Class.Name, parent.Name)
		}
		pt, ct := bind(pp.Type, parent.Class), bind(cp.Type, cl)
		if pt != nil && ct != nil && !c.overrideFits(pt, ct) {
			typeMismatch(nativesFit(written(parent, parent.Class, i), written(m, cl, i), false), compiledApart(i), i,
				cp.Span, "parameter $%s of %s::%s() (%s) is not contravariant with %s::%s() (%s)",
				cp.Name, cl.Name, m.Name, types.Print(ct), parent.Class.Name, parent.Name, types.Print(pt))
		}
		if pp.ByRef != cp.ByRef {
			mismatch(refused, cp.Span, "parameter $%s of %s::%s() disagrees on by-reference with %s::%s()", cp.Name, cl.Name, m.Name, parent.Class.Name, parent.Name)
		}
	}
	for i := len(parent.Params); i < len(m.Params); i++ {
		if !m.Params[i].HasDefault && !m.Params[i].Variadic {
			broken = true
			c.reportRefused(refused, m.Params[i].Span, P2502, nil, "extra parameter $%s of %s::%s() must be optional", m.Params[i].Name, cl.Name, m.Name)
		}
	}
	if parent.Return != nil {
		pr, cr := bind(parent.Return, parent.Class), bind(m.Return, cl)
		if cr == nil {
			if _, isVoid := pr.(types.Void); !isVoid && !types.IsMixed(pr) {
				// PHP sees the parent's native return type alone.
				mismatch(refused && parent.NativeReturn != nil && !ofPHP(parent.Class), m.Span, "%s::%s() must declare a return type compatible with %s::%s() (%s)", cl.Name, m.Name, parent.Class.Name, parent.Name, types.Print(pr))
			}
		} else if !c.overrideFits(cr, pr) {
			if _, parentVoid := pr.(types.Void); !(parentVoid && isVoidLike(cr)) {
				typeMismatch(nativesFit(written(m, cl, -1), written(parent, parent.Class, -1), true), compiledApart(-1), -1,
					m.Span, "return type %s of %s::%s() is not covariant with %s::%s() (%s)",
					types.Print(cr), cl.Name, m.Name, parent.Class.Name, parent.Name, types.Print(pr))
			}
		}
	}
	return broken, extra
}

// ofPHP reports a class of PHP's library, the stubs' or an extension's.
func ofPHP(cl *program.Class) bool {
	return cl.File != nil && (cl.File.IsStub() || strings.HasPrefix(cl.File.Path, "<ext>/"))
}

// phpType is a native type as PHP compares it in an override: erased of
// what only Phaenon reads in it (`list<int>` is array, `class<T>` string, a
// type parameter its bound), and of nothing PHP tells apart. types.Erase
// alone would make `false` and `true` the bool they are members of, and
// `array|bool` would pass for `array|false`, which PHP refuses.
func phpType(t types.Type) types.Type {
	switch t := t.(type) {
	case types.BoolLit:
		return t
	case types.Union:
		return types.Map(t, phpType)
	case types.Intersection:
		ms := make([]types.Type, len(t.Types))
		for i, m := range t.Types {
			ms[i] = phpType(m)
		}
		return types.MakeIntersection(ms...)
	}
	return types.Erase(t)
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
		// IsSubtype steps up from a class to a generic ancestor without
		// the arguments the class passes it (`ArrayIterator<int, Cat>` for
		// `Iterator<int, Dog>`); NeverSubtype compares them.
		return !types.NeverSubtype(sub, super, c.p)
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
