package program

import (
	"slices"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/types"
)

// ensureHeader resolves what the hierarchy needs: parent, interfaces,
// traits, type parameters and aliases. It never needs subtyping, so it
// cannot recurse into member building.
func (c *Class) ensureHeader() {
	if c.ensured != 0 {
		return
	}
	c.ensured = 1
	p := c.File.program
	d := c.Decl
	c.Doc = parseDoc(d.Doc)
	if patch := p.patches[strings.ToLower(c.Name)]; patch != nil && patch.Doc != "" {
		c.Doc = parseDoc(patch.Doc)
	}
	r := &resolver{p: p, f: c.File, scope: c.Scope, class: c, params: map[string]*TypeParam{}, span: c.Span}
	if c.File.Checked {
		r.reportDocErrors(c.Doc)
	}
	// Type parameters first: everything else may mention them.
	c.TypeParams = typeParamsOf(d.TypeParams, c.Doc, r)
	ImplyMapKeyBounds(c.TypeParams, d)
	for _, tp := range c.TypeParams {
		r.params[tp.Name] = tp
	}
	// Aliases are resolved lazily (they may reference each other).
	if c.Doc != nil && (len(c.Doc.TypeAliases) > 0 || len(c.Doc.ImportTypes) > 0) {
		r.aliasNodes = map[string]*docblock.TypeNode{}
		r.resolving = map[string]bool{}
		for _, a := range c.Doc.TypeAliases {
			r.aliasNodes[a.Name] = a.Type
		}
		c.Aliases = map[string]types.Type{}
		r.aliases = c.Aliases
		for _, it := range c.Doc.ImportTypes {
			from := p.Class(r.scope.ResolveClassString(it.From.Name))
			name := it.Name
			if it.Alias != "" {
				name = it.Alias
			}
			if from != nil {
				from.ensureHeader()
				if t, ok := from.Aliases[it.Name]; ok {
					c.Aliases[name] = t
					continue
				}
			}
			c.Aliases[name] = types.TMixed
		}
		for name := range r.aliasNodes {
			r.alias(name)
		}
	}
	switch d.Kind {
	case ast.KindInterface:
		for _, n := range d.Extends {
			c.Interfaces = append(c.Interfaces, r.scope.ResolveClass(n.Name))
		}
	default:
		if len(d.Extends) > 0 {
			c.Parent = r.scope.ResolveClass(d.Extends[0].Name)
		}
		for _, n := range d.Implements {
			c.Interfaces = append(c.Interfaces, r.scope.ResolveClass(n.Name))
		}
	}
	if d.Kind == ast.KindEnum {
		c.Interfaces = append(c.Interfaces, "UnitEnum")
		if d.BackingType != nil {
			c.BackingType = r.native(d.BackingType)
			c.Interfaces = append(c.Interfaces, "BackedEnum")
		}
	}
	for _, m := range d.Members {
		if tu, ok := m.(*ast.TraitUse); ok {
			c.traitUses = append(c.traitUses, tu)
			for _, n := range tu.Names {
				c.Traits = append(c.Traits, r.scope.ResolveClass(n))
			}
		}
	}
	// Generic arguments to the parent and interfaces.
	if c.Doc != nil {
		c.Deprecated = c.Doc.Deprecated
		c.Internal = c.Doc.Internal
		if c.Doc.Final {
			c.Final = true
		}
		c.InterfaceArgs = map[string][]types.Type{}
		for _, e := range c.Doc.Extends {
			if t, ok := r.docType(e).(types.Class); ok {
				if strings.EqualFold(t.Name, c.Parent) {
					c.ParentArgs = t.Args
				} else if d.Kind == ast.KindInterface {
					c.InterfaceArgs[strings.ToLower(t.Name)] = t.Args
				}
			}
		}
		for _, e := range c.Doc.Implements {
			if t, ok := r.docType(e).(types.Class); ok {
				c.InterfaceArgs[strings.ToLower(t.Name)] = t.Args
			}
		}
	}
	// Native generic arguments (Phaenon syntax) override docblocks.
	if c.InterfaceArgs == nil {
		c.InterfaceArgs = map[string][]types.Type{}
	}
	for i, e := range d.Extends {
		if len(e.TypeArgs) == 0 {
			continue
		}
		args := r.NativeArgs(e.TypeArgs)
		if d.Kind == ast.KindInterface {
			c.InterfaceArgs[strings.ToLower(r.scope.ResolveClass(e.Name))] = args
		} else if i == 0 {
			c.ParentArgs = args
		}
	}
	for _, e := range d.Implements {
		if len(e.TypeArgs) > 0 {
			c.InterfaceArgs[strings.ToLower(r.scope.ResolveClass(e.Name))] = r.NativeArgs(e.TypeArgs)
		}
	}
	c.ensured = 2
	c.ensureMembers()
}

// ensure builds the whole class: header and members.
func (c *Class) ensure() { c.ensureHeader() }

// ensureMembers builds the member tables: own members, then trait members.
func (c *Class) ensureMembers() {
	if c.consts != nil {
		return
	}
	c.consts = map[string]*Constant{}
	c.props = map[string]*Property{}
	c.methods = map[string]*Method{}
	c.cases = map[string]*EnumCase{}
	p := c.File.program
	r := p.newResolver(c.File, c.Scope, c, c.Span)
	for _, m := range c.Decl.Members {
		switch m := m.(type) {
		case *ast.ClassConstDecl:
			doc := parseDoc(m.Doc)
			var docT types.Type
			if doc != nil && len(doc.Vars) > 0 {
				docT = r.docType(doc.Vars[0].Type)
			}
			for _, item := range m.Consts {
				k := &Constant{Name: item.Name.Name, Class: c, Vis: visibilityOf(m.Modifiers), Final: m.Modifiers&ast.ModFinal != 0,
					Value: item.Value, File: c.File, Scope: c.Scope, Span: item.S, Attrs: m.Attrs}
				k.Deprecated, k.DeprecatedNote = deprecation(c.Scope, doc, m.Attrs)
				k.Type = r.effective(r.native(m.Type), docT, "constant "+k.Name)
				c.addConst(k)
			}
		case *ast.PropertyDecl:
			doc := parseDoc(m.Doc)
			if c.File.Checked {
				r.span = m.S
				r.reportDocErrors(doc)
			}
			native := r.native(m.Type)
			for _, item := range m.Props {
				var docT types.Type
				if doc != nil && !nativeHasTypeArgs(m.Type) {
					// `\ArrayObject<int, Metric>` written as syntax is the type;
					// a `@var` beside it (left by a migration) is not read.
					for _, v := range doc.Vars {
						if v.Name == "" || v.Name == item.Var.Name {
							docT = r.docType(v.Type)
						}
					}
				}
				prop := &Property{Name: item.Var.Name, Class: c, Vis: visibilityOf(m.Modifiers), Static: m.Modifiers&ast.ModStatic != 0,
					Readonly: m.Modifiers&ast.ModReadonly != 0 || c.Readonly, NativeType: native, HasDefault: item.Default != nil,
					Default: item.Default, Hooks: m.Hooks, Doc: doc, Attrs: m.Attrs, Span: item.S, File: c.File}
				prop.SetVis = setVisibilityOf(m.Modifiers, prop.Vis)
				if prop.Readonly && m.Modifiers&(ast.ModPrivateSet|ast.ModProtectedSet|ast.ModPublicSet) == 0 {
					prop.SetVis = Protected // readonly implies protected(set)
					if prop.Vis == Private {
						prop.SetVis = Private
					}
				}
				prop.Type = r.effective(native, docT, "property $"+prop.Name)
				if prop.Type == nil {
					prop.Type = types.TMixed
				}
				if doc != nil {
					prop.Deprecated = doc.Deprecated
				}
				if len(m.Hooks) > 0 {
					prop.Virtual = !hooksReferenceBacking(m.Hooks, prop.Name)
				}
				c.addProp(prop)
			}
		case *ast.MethodDecl:
			meth := r.buildMethod(c, c.patchedMethod(m))
			if meth.Decl != m && !meth.Deprecated {
				// The overlay retypes a stub method; the stub's
				// #[\Deprecated] still holds.
				meth.Deprecated, meth.DeprecatedNote = deprecation(c.Scope, nil, m.Attrs)
			}
			c.addMethod(meth)
		case *ast.EnumCase:
			cs := &EnumCase{Name: m.Name.Name, Class: c, Value: m.Value, Span: m.S, Attrs: m.Attrs}
			cs.Deprecated, cs.DeprecatedNote = deprecation(c.Scope, parseDoc(m.Doc), m.Attrs)
			c.Cases = append(c.Cases, cs)
			c.cases[cs.Name] = cs
		}
	}
	// Overlay methods the stub does not declare.
	if patch := p.patches[strings.ToLower(c.Name)]; patch != nil {
		for _, pm := range patch.Members {
			if md, ok := pm.(*ast.MethodDecl); ok {
				if _, exists := c.methods[strings.ToLower(md.Name.Name)]; !exists {
					c.addMethod(r.buildMethod(c, md))
				}
			}
		}
	}
	// Enums have the magic read-only properties $name and $value.
	if c.Kind == ast.KindEnum {
		c.addProp(&Property{Name: "name", Class: c, Vis: Public, SetVis: Private, Readonly: true, Magic: true,
			Type: types.String{NonEmpty: true}, NativeType: types.TString, Span: c.Span, File: c.File})
		if c.BackingType != nil {
			c.addProp(&Property{Name: "value", Class: c, Vis: Public, SetVis: Private, Readonly: true, Magic: true,
				Type: c.BackingType, NativeType: c.BackingType, Span: c.Span, File: c.File})
		}
	}
	// The enum interfaces carry the same properties, as PHPStan knows them.
	switch c.Name {
	case "UnitEnum":
		c.addProp(&Property{Name: "name", Class: c, Vis: Public, SetVis: Private, Readonly: true, Magic: true,
			Type: types.String{NonEmpty: true}, NativeType: types.TString, Span: c.Span, File: c.File})
	case "BackedEnum":
		c.addProp(&Property{Name: "value", Class: c, Vis: Public, SetVis: Private, Readonly: true, Magic: true,
			Type: types.TArrayKey, NativeType: types.TArrayKey, Span: c.Span, File: c.File})
	}
	// Promoted constructor parameters are properties.
	if ctor := c.methods["__construct"]; ctor != nil && ctor.Decl != nil {
		for i, ap := range ctor.Decl.Params {
			if ap.Modifiers == 0 {
				continue
			}
			pp := ctor.Params[i]
			// The constructor's own templates stay in the property type, as
			// PHPStan keeps them: a call through the property infers them.
			prop := &Property{Name: pp.Name, Class: c, Vis: visibilityOf(ap.Modifiers), Readonly: ap.Modifiers&ast.ModReadonly != 0 || c.Readonly,
				NativeType: pp.NativeType, Type: pp.Type, HasDefault: false, Hooks: ap.Hooks, Promoted: true, Attrs: ap.Attrs, Span: ap.S, File: c.File}
			prop.SetVis = setVisibilityOf(ap.Modifiers, prop.Vis)
			if prop.Readonly && ap.Modifiers&(ast.ModPrivateSet|ast.ModProtectedSet|ast.ModPublicSet) == 0 {
				prop.SetVis = Protected
				if prop.Vis == Private {
					prop.SetVis = Private
				}
			}
			if _, exists := c.props[prop.Name]; !exists {
				c.addProp(prop)
			}
		}
	}
	// Magic members from the class docblock.
	if c.Doc != nil {
		for _, mt := range c.Doc.Methods {
			m := &Method{Name: mt.Name, Class: c, Vis: Public, Static: mt.Static, Magic: true, Span: c.Span}
			if mt.Return != nil {
				m.Return = r.docType(mt.Return)
			} else {
				m.Return = types.TMixed
			}
			for _, cp := range mt.Params {
				pp := &Param{Name: cp.Name, Type: types.TMixed, HasDefault: cp.Optional, Variadic: cp.Variadic, ByRef: cp.ByRef}
				if cp.Type != nil {
					pp.Type = r.docType(cp.Type)
				}
				m.Params = append(m.Params, pp)
			}
			if _, exists := c.methods[strings.ToLower(m.Name)]; !exists {
				c.addMethod(m)
			}
		}
		for _, pt := range c.Doc.Properties {
			if _, exists := c.props[pt.Name]; exists {
				continue
			}
			c.addProp(&Property{Name: pt.Name, Class: c, Vis: Public, Type: r.docType(pt.Type), Magic: true, Span: c.Span, File: c.File})
		}
	}
	// Traits: copy members not defined by the class itself.
	for _, tu := range c.traitUses {
		c.useTrait(tu)
	}
	c.inheritDocTypes()
	c.inheritParamOuts()
}

// inheritDocTypes gives a method without docblock types the docblock types
// of the method it overrides, as PHPStan does (implicit @inheritDoc):
// parameters are matched by name, and constructors are left alone, since a
// child's constructor overrides nothing in PHP. The parent's type
// parameters are mapped through @extends / @implements. Each parameter and
// the return take their type from the first declaration that documents it,
// in the order PHPStan inherits a docblock: the method in the parent's
// table, then what that one inherits, then the methods of the class's
// immediate interfaces in the order declared, each with what it inherits
// (Overridden).
// Symfony's Kernel takes registerBundles() through a trait that documents
// nothing from the KernelInterface of the class using the trait, which
// says iterable<BundleInterface>. An interface that a class in between
// implements, below the one whose method the parent's table holds, gives
// nothing.
func (c *Class) inheritDocTypes() {
	var self []types.Type
	if t, ok := c.TypeOf().(types.Class); ok {
		self = t.Args
	}
	for _, m := range c.Methods {
		if m.Decl == nil || m.FromTrait != nil || strings.EqualFold(m.Name, "__construct") {
			continue
		}
		open := map[*Param]bool{}
		for _, cp := range m.Params {
			hasDoc := cp.NativeType == nil && !types.IsMixed(cp.Type) || cp.NativeType != nil && !types.Equal(cp.Type, cp.NativeType) || ownNative(cp.NativeType) || docParam(m.Doc, cp.Name)
			if !hasDoc {
				open[cp] = true
			}
		}
		openReturn := !(m.Return != nil && (m.NativeReturn == nil || !types.Equal(m.Return, m.NativeReturn)) || ownNative(m.NativeReturn) || m.Doc != nil && m.Doc.Return != nil)
		for _, pm := range inheritedFrom(m) {
			if len(open) == 0 && !openReturn {
				break
			}
			a := pm.Class
			var subst map[string]types.Type
			if args, ok := c.SuperArgs(a.Name, self); ok {
				subst = a.Subst(args)
			}
			for _, pp := range pm.Params {
				cp := paramNamed(m.Params, pp.Name)
				if cp == nil || !open[cp] || pp.Type == nil || !nativeMatches(cp.NativeType, erasedNative(pp.NativeType, pm, a)) {
					continue
				}
				cp.Type = types.Substitute(pp.Type, subst)
				if pp.Inherited || docParam(pm.Doc, pp.Name) || ownNative(pp.NativeType) {
					cp.Inherited = true
					delete(open, cp)
				}
			}
			if openReturn && pm.Return != nil && nativeMatches(m.NativeReturn, erasedNative(pm.NativeReturn, pm, a)) {
				m.Return = types.Substitute(pm.Return, subst)
				if pm.InheritedReturn || pm.Doc != nil && pm.Doc.Return != nil || ownNative(pm.NativeReturn) {
					m.InheritedReturn = true
					openReturn = false
				}
			}
		}
	}
}

// inheritParamOuts gives each by-reference parameter of c's methods that
// the method's own docblock documents no @param-out the tag of the method
// it overrides, as PHPStan merges one docblock into another
// (ResolvedPhpDocBlock::mergeParamOutTags), and unlike inheritDocTypes's
// @param in two ways. The tag comes from one declaration, the one PHPStan
// takes the whole docblock from (docParent): a parent's method with a
// docblock that has no @param-out passes on none, though an interface
// beside the parent documents one. And the parameters are matched by
// position, so that a renamed parameter takes the tag of the one it
// replaces, the subject of a conditional type renamed with it; a
// constructor's are matched by name, as PHPStan matches them. The tag is
// merged tag by tag: a docblock of the method's own that gives the
// parameter a @param, or documents another parameter's @param-out, keeps
// the parent's @param-out of it. The parent's type parameters are mapped
// through @extends and @implements; a method's own stay as they are.
//
// A method a trait brings in takes the tag through the class that uses the
// trait, as PHPStan resolves it there, for the calls through that class:
// on parameters of its own, since the trait's are every user's and its
// body's. The trait's own methods take nothing: the body is checked once,
// in the trait, against the trait's own docblock (paramout.go).
func (c *Class) inheritParamOuts() {
	if c.Kind == ast.KindTrait {
		return
	}
	var self []types.Type
	if t, ok := c.TypeOf().(types.Class); ok {
		self = t.Args
	}
	untagged := func(p *Param) bool { return p.ByRef && p.Out == nil }
	for _, m := range c.Methods {
		if m.Decl == nil || !slices.ContainsFunc(m.Params, untagged) {
			continue
		}
		d := m.docParent(0)
		if d == nil {
			continue
		}
		byName := strings.EqualFold(m.Name, "__construct")
		at := func(i int, dp *Param) *Param {
			if byName {
				return paramNamed(m.Params, dp.Name)
			}
			if i < len(m.Params) {
				return m.Params[i]
			}
			return nil
		}
		var subst map[string]types.Type
		if args, ok := c.SuperArgs(d.Class.Name, self); ok {
			subst = d.Class.Subst(args)
		}
		renamed := map[string]string{}
		for i, dp := range d.Params {
			if cp := at(i, dp); cp != nil && cp.Name != dp.Name {
				renamed[dp.Name] = cp.Name
			}
		}
		own := m.FromTrait == nil
		for i, dp := range d.Params {
			cp := at(i, dp)
			if dp.Out == nil || cp == nil || !cp.ByRef || cp.Out != nil {
				continue
			}
			if !own {
				m.Params = slices.Clone(m.Params)
				for j, p := range m.Params {
					cl := *p
					m.Params[j] = &cl
				}
				own = true
				cp = at(i, dp)
			}
			cp.Out = renameSubjects(types.Substitute(dp.Out, subst), renamed)
			cp.OutFrom = dp.OutFrom
			if cp.OutFrom == nil {
				cp.OutFrom = &TagSource{Method: d, Param: dp}
			}
		}
	}
}

// docParent is the declaration PHPStan takes m's docblock from, to merge
// m's own into (PhpDocInheritanceResolver::resolvePhpDocForMethod): the
// first that has a docblock, its own or one it inherits (hasDoc), of the
// method in the parent's table, a concrete constructor included, unless it
// is private or a constructor of PHP's own, and the methods of the class's
// immediate interfaces in the order declared; failing those, the first
// abstract method of the class's traits that has a docblock of its own.
// nil for none.
func (m *Method) docParent(depth int) *Method {
	cl := m.Class
	if cl == nil || m.Magic || depth > maxOverrideDepth {
		return nil
	}
	key := strings.ToLower(m.Name)
	if entry, _ := tableEntry(cl.ParentClass(), key, map[*Class]bool{cl: true}); entry != nil && entry.Vis != Private &&
		(key != "__construct" || !entry.Class.File.IsStub()) && entry.hasDoc(depth+1) {
		return entry
	}
	for _, i := range immediateInterfaces(cl) {
		if entry, _ := tableEntry(i, key, map[*Class]bool{cl: true}); entry != nil && entry.hasDoc(depth+1) {
			return entry
		}
	}
	p := cl.File.program
	for _, name := range cl.Traits {
		if t := p.Class(name); t != nil && t.Kind == ast.KindTrait {
			if tm := t.FindMethod(key); tm != nil && tm.Abstract && tm.Doc != nil && tm.Decl != m.Decl {
				return tm
			}
		}
	}
	return nil
}

// hasDoc reports whether PHPStan resolves a docblock for m: its own, or one
// it inherits (docParent).
func (m *Method) hasDoc(depth int) bool {
	return m.Doc != nil || m.docParent(depth) != nil
}

// renameSubjects is t with the parameters a conditional type tests renamed
// as names maps them.
func renameSubjects(t types.Type, names map[string]string) types.Type {
	if len(names) == 0 {
		return t
	}
	return types.MapDeep(t, func(m types.Type) types.Type {
		if c, ok := m.(types.Conditional); ok {
			if to, ok := names[c.ParamName]; ok {
				c.ParamName = to
				return c
			}
		}
		return m
	})
}

// inheritedFrom lists the declarations m inherits a docblock from, in the
// order PHPStan consults them: each one Overridden gives, followed by
// what that one inherits in turn.
func inheritedFrom(m *Method) []*Method {
	var out []*Method
	seen := map[*Method]bool{m: true}
	var walk func(m *Method)
	walk = func(m *Method) {
		for _, d := range m.Overridden() {
			if seen[d] || len(out) > maxOverrideDepth {
				continue
			}
			seen[d] = true
			out = append(out, d)
			walk(d)
		}
	}
	walk(m)
	return out
}

// docParam reports whether a docblock gives the named parameter a type.
func docParam(doc *docblock.Doc, name string) bool {
	if doc == nil {
		return false
	}
	for _, t := range doc.Params {
		if t.Name == name && t.Type != nil {
			return true
		}
	}
	return false
}

// ownNative reports whether a declared type says more than its PHP erasure
// (`class<T>`, `list<int>`, `T`): such a Phaenon signature is the method's
// own typing and inherits nothing from the method it overrides.
func ownNative(t types.Type) bool {
	return t != nil && !types.Equal(types.Erase(t), t)
}

// paramNamed returns the parameter called name, or nil.
func paramNamed(ps []*Param, name string) *Param {
	for _, p := range ps {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// boundSubst replaces type parameters by their bounds (mixed when unbounded).
func boundSubst(t types.Type, tps []*TypeParam) types.Type {
	if len(tps) == 0 || t == nil {
		return t
	}
	m := make(map[string]types.Type, len(tps))
	for _, tp := range tps {
		if tp.Bound != nil {
			m[tp.Name] = tp.Bound
		} else {
			m[tp.Name] = types.TMixed
		}
	}
	return types.Substitute(t, m)
}

// erasedNative is what a parent's declared type looks like in PHP: a
// Phaenon signature (`class-string<T> $x`) declares what a PHP one
// declares in a docblock, so a child written as `string $x` overrides it
// just as it would override the erased `string`.
func erasedNative(t types.Type, m *Method, c *Class) types.Type {
	if t == nil {
		return nil
	}
	return boundSubst(boundSubst(t, m.TypeParams), c.TypeParams)
}

// nativeMatches reports whether two native types agree closely enough for
// a docblock to carry over: equal, or the child's is absent, or the same
// once refinements are erased.
func nativeMatches(child, parent types.Type) bool {
	if child == nil {
		return true
	}
	if parent == nil {
		return false
	}
	if types.Equal(child, parent) {
		return true
	}
	cs, ok1 := types.Native(child)
	ps, ok2 := types.Native(parent)
	return ok1 && ok2 && cs == ps
}

func (c *Class) addConst(k *Constant) {
	if _, exists := c.consts[k.Name]; exists {
		return
	}
	c.consts[k.Name] = k
	c.Consts = append(c.Consts, k)
}

func (c *Class) addProp(p *Property) {
	if _, exists := c.props[p.Name]; exists {
		return
	}
	c.props[p.Name] = p
	c.Props = append(c.Props, p)
}

func (c *Class) addMethod(m *Method) {
	key := strings.ToLower(m.Name)
	if _, exists := c.methods[key]; exists {
		return
	}
	c.methods[key] = m
	c.Methods = append(c.Methods, m)
}

// hooksReferenceBacking reports whether any hook body reads or writes
// `$this->name`, which makes the property backed (non-virtual).
func hooksReferenceBacking(hooks []*ast.PropertyHook, name string) bool {
	found := false
	for _, h := range hooks {
		var nodes []ast.Node
		for _, s := range h.Body {
			nodes = append(nodes, s)
		}
		if h.Expr != nil {
			nodes = append(nodes, h.Expr)
		}
		for _, n := range nodes {
			ast.Inspect(n, func(n ast.Node) bool {
				if pf, ok := n.(*ast.PropertyFetch); ok {
					if v, ok := pf.Object.(*ast.Variable); ok && v.Name == "this" {
						if id, ok := pf.Name.(*ast.Ident); ok && id.Name == name {
							found = true
						}
					}
				}
				return !found
			})
		}
	}
	return found
}

func (r *resolver) buildMethod(c *Class, d *ast.MethodDecl) *Method {
	m := &Method{
		Name:     d.Name.Name,
		Class:    c,
		Vis:      visibilityOf(d.Modifiers),
		Static:   d.Modifiers&ast.ModStatic != 0,
		Abstract: d.Modifiers&ast.ModAbstract != 0 || (c.Kind == ast.KindInterface && !d.HasBody),
		Final:    d.Modifiers&ast.ModFinal != 0,
		ByRef:    d.ByRef,
		Attrs:    d.Attrs,
		Decl:     d,
		Span:     d.S,
	}
	m.Doc = parseDoc(d.Doc)
	rr := r
	rr.span = d.S
	if c.File.Checked {
		rr.reportDocErrors(m.Doc)
	}
	m.TypeParams = typeParamsOf(d.TypeParams, m.Doc, rr)
	ImplyMapKeyBounds(m.TypeParams, signatureNodes(d.Params, d.ReturnType)...)
	rr = rr.withParams(m.TypeParams)
	m.Params = rr.buildParams(d.Params, m.Doc)
	m.NativeReturn = rr.native(d.ReturnType)
	m.Return = rr.buildReturn(d.ReturnType, m.Doc)
	m.Throws = rr.buildThrows(d.Throws, m.Doc)
	m.Deprecated, m.DeprecatedNote = deprecation(c.Scope, m.Doc, d.Attrs)
	if m.Doc != nil {
		m.Asserts = m.Doc.Asserts
		if m.Doc.SelfOut != nil {
			m.SelfOut = rr.docType(m.Doc.SelfOut)
		}
	}
	return m
}

// patchedMethod returns the overlay's declaration of a stub method when
// the overlay redeclares it.
func (c *Class) patchedMethod(m *ast.MethodDecl) *ast.MethodDecl {
	patch := c.File.program.patches[strings.ToLower(c.Name)]
	if patch == nil {
		return m
	}
	for _, pm := range patch.Members {
		if md, ok := pm.(*ast.MethodDecl); ok && strings.EqualFold(md.Name.Name, m.Name.Name) {
			return md
		}
	}
	return m
}

// useTrait copies a trait's members into the class, applying adaptations.
func (c *Class) useTrait(tu *ast.TraitUse) {
	p := c.File.program
	excluded := map[string]bool{} // "trait\x00method" removed by insteadof
	type aliasRule struct {
		trait  string
		method string
		alias  string
		mods   ast.Modifier
	}
	var aliases []aliasRule
	for _, a := range tu.Adaptations {
		tname := ""
		if a.Trait != nil {
			tname = strings.ToLower(c.Scope.ResolveClass(a.Trait))
		}
		if len(a.InsteadOf) > 0 {
			for _, other := range a.InsteadOf {
				excluded[strings.ToLower(c.Scope.ResolveClass(other))+"\x00"+strings.ToLower(a.Method.Name)] = true
			}
			continue
		}
		rule := aliasRule{trait: tname, method: strings.ToLower(a.Method.Name), mods: a.Modifiers}
		if a.Alias != nil {
			rule.alias = a.Alias.Name
		}
		aliases = append(aliases, rule)
	}
	for _, n := range tu.Names {
		tname := c.Scope.ResolveClass(n)
		t := p.Class(tname)
		if t == nil || t.Kind != ast.KindTrait {
			continue
		}
		t.ensure()
		for _, tm := range t.Methods {
			key := strings.ToLower(tm.Name)
			excludedHere := excluded[strings.ToLower(t.Name)+"\x00"+key]
			copyM := *tm
			copyM.Class = c
			if copyM.FromTrait == nil {
				copyM.FromTrait = t
			}
			copyM.rebindSelf(t, c)
			for _, rule := range aliases {
				if rule.method != key || (rule.trait != "" && rule.trait != strings.ToLower(t.Name)) {
					continue
				}
				if rule.alias != "" {
					al := copyM
					al.Name = rule.alias
					if rule.mods != 0 {
						al.Vis = visibilityOf(rule.mods)
					}
					c.addMethod(&al)
				} else if rule.mods != 0 {
					copyM.Vis = visibilityOf(rule.mods)
				}
			}
			if !excludedHere {
				c.addMethod(&copyM)
			}
		}
		for _, tp := range t.Props {
			copyP := *tp
			copyP.Class = c
			if copyP.FromTrait == nil {
				copyP.FromTrait = t
			}
			copyP.Type, copyP.NativeType = asUser(copyP.Type, t, c), asUser(copyP.NativeType, t, c)
			c.addProp(&copyP)
		}
		for _, tk := range t.Consts {
			copyK := *tk
			copyK.Class = c
			copyK.Type = asUser(copyK.Type, t, c)
			c.addConst(&copyK)
		}
	}
}

// rebindSelf makes a method copied from trait into user the user's: its
// parameters, return and @phpstan-self-out read self as the user.
func (m *Method) rebindSelf(trait, user *Class) {
	params := make([]*Param, len(m.Params))
	for i, p := range m.Params {
		cp := *p
		cp.Type, cp.NativeType, cp.Out = asUser(cp.Type, trait, user), asUser(cp.NativeType, trait, user), asUser(cp.Out, trait, user)
		params[i] = &cp
	}
	m.Params = params
	m.Return, m.NativeReturn = asUser(m.Return, trait, user), asUser(m.NativeReturn, trait, user)
	m.SelfOut = asUser(m.SelfOut, trait, user)
}

// asUser is a type a trait's member declares as the class using the trait
// sees it. PHP resolves `self` in a trait's declarations to the class
// using it, and `static` and `$this` to that class or a subclass, as
// PHPStan reads the member in the class's context; the trait's own types
// read them as the trait, in its body. A trait is no type a value can
// have, so its name in a type, written or as self, is the user's, inside a
// type argument, a callable's signature or a shape as well. A trait
// copied into a trait passes the question on to that trait's users.
func asUser(t types.Type, trait, user *Class) types.Type {
	if t == nil {
		return nil
	}
	var args []types.Type
	if tt, ok := user.TypeOf().(types.Class); ok {
		args = tt.Args
	}
	own := func(c *types.Class) bool { return c != nil && strings.EqualFold(c.Name, trait.Name) }
	return types.MapDeep(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Class:
			if own(&m) {
				return types.Class{Name: user.Name, Args: args}
			}
		case types.Static:
			if own(m.Of) {
				return types.Static{Of: &types.Class{Name: user.Name, Args: args}}
			}
		case types.This:
			if own(m.Of) {
				return types.This{Of: &types.Class{Name: user.Name, Args: args}}
			}
		}
		return m
	})
}

// ------------------------------------------------------------------ lookup

// ParentClass returns the parent class, loading it if needed.
func (c *Class) ParentClass() *Class {
	c.ensureHeader()
	if c.Parent == "" {
		return nil
	}
	return c.File.program.Class(c.Parent)
}

// IsSubclassOf reports whether c is name or inherits from / implements it.
func (c *Class) IsSubclassOf(name string) bool {
	return c.isSubclassOf(name, map[string]bool{})
}

func (c *Class) isSubclassOf(name string, seen map[string]bool) bool {
	if strings.EqualFold(c.Name, name) {
		return true
	}
	key := strings.ToLower(c.Name)
	if seen[key] {
		return false
	}
	seen[key] = true
	c.ensureHeader()
	p := c.File.program
	if c.Parent != "" {
		if pc := p.Class(c.Parent); pc != nil && pc.isSubclassOf(name, seen) {
			return true
		}
	}
	for _, i := range c.Interfaces {
		if strings.EqualFold(i, name) {
			return true
		}
		if ic := p.Class(i); ic != nil && ic.isSubclassOf(name, seen) {
			return true
		}
	}
	return false
}

// Supertypes returns the direct supertypes: the parent first, then the interfaces.
func (c *Class) Supertypes() []*Class {
	c.ensureHeader()
	p := c.File.program
	var out []*Class
	if c.Parent != "" {
		if pc := p.Class(c.Parent); pc != nil {
			out = append(out, pc)
		}
	}
	for _, i := range c.Interfaces {
		if ic := p.Class(i); ic != nil {
			out = append(out, ic)
		}
	}
	return out
}

// Ancestors returns every supertype, nearest first, without duplicates.
func (c *Class) Ancestors() []*Class {
	var out []*Class
	seen := map[string]bool{strings.ToLower(c.Name): true}
	queue := []*Class{c}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, s := range cur.Supertypes() {
			k := strings.ToLower(s.Name)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, s)
			queue = append(queue, s)
		}
	}
	return out
}

// FindMethod finds a method by name in the class, its traits (already
// flattened) and its ancestors. Interface methods are found last.
func (c *Class) FindMethod(name string) *Method {
	key := strings.ToLower(name)
	c.ensure()
	if m, ok := c.methods[key]; ok {
		return m
	}
	for _, a := range c.Ancestors() {
		a.ensure()
		if m, ok := a.methods[key]; ok {
			return m
		}
	}
	return nil
}

// FindProperty finds a property by name in the class and its parents
// (interfaces cannot declare properties).
func (c *Class) FindProperty(name string) *Property {
	c.ensure()
	if pr, ok := c.props[name]; ok {
		return pr
	}
	for _, a := range c.Ancestors() {
		a.ensure()
		if pr, ok := a.props[name]; ok {
			return pr
		}
	}
	return nil
}

// FindConst finds a class constant in the class and its ancestors.
func (c *Class) FindConst(name string) *Constant {
	c.ensure()
	if k, ok := c.consts[name]; ok {
		return k
	}
	for _, a := range c.Ancestors() {
		a.ensure()
		if k, ok := a.consts[name]; ok {
			return k
		}
	}
	return nil
}

// FindCase finds an enum case.
func (c *Class) FindCase(name string) *EnumCase {
	c.ensure()
	return c.cases[name]
}

// MethodTable returns every method visible on the class (own, trait and
// inherited), keyed by lower-cased name; the nearest definition wins.
func (c *Class) MethodTable() map[string]*Method {
	out := map[string]*Method{}
	c.ensure()
	for k, m := range c.methods {
		out[k] = m
	}
	for _, a := range c.Ancestors() {
		a.ensure()
		for k, m := range a.methods {
			if cur, ok := out[k]; !ok || (cur.Abstract && !m.Abstract) {
				out[k] = m
			}
		}
	}
	return out
}

// Unimplemented returns the abstract methods a concrete class fails to
// implement.
func (c *Class) Unimplemented() []*Method {
	var out []*Method
	for _, m := range c.MethodTable() {
		if m.Abstract {
			out = append(out, m)
		}
	}
	return out
}

// IsInstantiable reports whether `new` is allowed on the class.
func (c *Class) IsInstantiable() bool {
	return c.Kind == ast.KindClass && !c.Abstract
}

// nativeHasTypeArgs reports a native type written with Phaenon's type
// arguments (`Foo<T>`, `list<T>`, `map<K, V>`, a tuple), which no docblock
// refines; a plain `array` is still what its @var says.
func nativeHasTypeArgs(t ast.TypeExpr) bool {
	switch t := t.(type) {
	case *ast.NamedType:
		return len(t.TypeArgs) > 0
	case *ast.NullableType:
		return nativeHasTypeArgs(t.Type)
	case *ast.UnionType:
		for _, m := range t.Types {
			if nativeHasTypeArgs(m) {
				return true
			}
		}
	case *ast.IntersectionType:
		for _, m := range t.Types {
			if nativeHasTypeArgs(m) {
				return true
			}
		}
	case *ast.TupleType:
		return true
	}
	return false
}

// SuperArgs maps the type arguments of c (args, in c's parameter order) to
// the arguments of the named supertype, following @extends/@implements.
// ok is false when the supertype is not an ancestor.
func (c *Class) SuperArgs(super string, args []types.Type) ([]types.Type, bool) {
	return c.superArgs(super, args, map[string]bool{})
}

func (c *Class) superArgs(super string, args []types.Type, seen map[string]bool) ([]types.Type, bool) {
	if strings.EqualFold(c.Name, super) {
		return args, true
	}
	key := strings.ToLower(c.Name)
	if seen[key] {
		return nil, false
	}
	seen[key] = true
	c.ensureHeader()
	subst := c.Subst(args)
	p := c.File.program
	if c.Parent != "" {
		if pc := p.Class(c.Parent); pc != nil {
			pargs := substAll(c.ParentArgs, subst)
			if r, ok := pc.superArgs(super, pargs, seen); ok {
				return r, true
			}
		}
	}
	for _, i := range c.Interfaces {
		if ic := p.Class(i); ic != nil {
			iargs := substAll(c.InterfaceArgs[strings.ToLower(i)], subst)
			if r, ok := ic.superArgs(super, iargs, seen); ok {
				return r, true
			}
		}
	}
	return nil, false
}

// Subst builds the substitution of the class's type parameters by args;
// missing arguments map to the parameter's bound (or mixed).
func (c *Class) Subst(args []types.Type) map[string]types.Type {
	c.ensureHeader()
	if len(c.TypeParams) == 0 {
		return nil
	}
	m := make(map[string]types.Type, len(c.TypeParams))
	for i, tp := range c.TypeParams {
		switch {
		case i < len(args) && args[i] != nil:
			m[tp.Name] = args[i]
		case tp.Default != nil:
			m[tp.Name] = tp.Default
		case tp.Bound != nil:
			// A bound may name the parameters before it: the TIterator of
			// a LimitIterator<int, Dog> is an Iterator<int, Dog>.
			m[tp.Name] = types.Substitute(tp.Bound, m)
		default:
			m[tp.Name] = types.TMixed
		}
	}
	return m
}

func substAll(ts []types.Type, subst map[string]types.Type) []types.Type {
	if len(ts) == 0 || subst == nil {
		return ts
	}
	out := make([]types.Type, len(ts))
	for i, t := range ts {
		out[i] = types.Substitute(t, subst)
	}
	return out
}

// TypeOf returns the class as a type (with its own parameters as arguments).
func (c *Class) TypeOf() types.Type {
	c.ensureHeader()
	if len(c.TypeParams) == 0 {
		return types.Class{Name: c.Name}
	}
	args := make([]types.Type, len(c.TypeParams))
	for i, tp := range c.TypeParams {
		args[i] = types.Param{Name: tp.Name, Bound: tp.Bound, Owner: c.Name}
	}
	return types.Class{Name: c.Name, Args: args}
}

// Signature returns the method with its types built (they are built with
// the class, so this is a no-op accessor kept for symmetry).
func (m *Method) Signature() *Method { return m }

// Signature returns the class with its header and members built.
func (c *Class) Signature() *Class {
	c.ensure()
	return c
}
