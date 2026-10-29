package program

import (
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
					Value: item.Value, File: c.File, Scope: c.Scope, Span: item.S}
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
				if doc != nil {
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
			c.addMethod(r.buildMethod(c, c.patchedMethod(m)))
		case *ast.EnumCase:
			cs := &EnumCase{Name: m.Name.Name, Class: c, Value: m.Value, Span: m.S}
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
}

// inheritDocTypes gives a method without docblock types the docblock types
// of the method it overrides, as PHPStan does (implicit @inheritDoc):
// parameters are matched by name, and constructors are left alone, since a
// child's constructor overrides nothing in PHP. The parent's type
// parameters are mapped through @extends / @implements.
func (c *Class) inheritDocTypes() {
	var self []types.Type
	if t, ok := c.TypeOf().(types.Class); ok {
		self = t.Args
	}
	for _, m := range c.Methods {
		if m.Decl == nil || m.FromTrait != nil || strings.EqualFold(m.Name, "__construct") {
			continue
		}
		key := strings.ToLower(m.Name)
		for _, a := range c.Ancestors() {
			a.ensure()
			pm, ok := a.methods[key]
			if !ok || pm.Vis == Private {
				continue
			}
			var subst map[string]types.Type
			if args, ok := c.SuperArgs(a.Name, self); ok {
				subst = a.Subst(args)
			}
			for _, pp := range pm.Params {
				cp := paramNamed(m.Params, pp.Name)
				if cp == nil {
					continue
				}
				hasDoc := cp.NativeType == nil && !types.IsMixed(cp.Type) || cp.NativeType != nil && !types.Equal(cp.Type, cp.NativeType)
				if hasDoc || pp.Type == nil || !nativeMatches(cp.NativeType, erasedNative(pp.NativeType, pm, a)) {
					continue
				}
				cp.Type = types.Substitute(pp.Type, subst)
			}
			hasDocReturn := m.Return != nil && (m.NativeReturn == nil || !types.Equal(m.Return, m.NativeReturn))
			if !hasDocReturn && pm.Return != nil && nativeMatches(m.NativeReturn, erasedNative(pm.NativeReturn, pm, a)) {
				m.Return = types.Substitute(pm.Return, subst)
			}
			break
		}
	}
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
	rr = rr.withParams(m.TypeParams)
	m.Params = rr.buildParams(d.Params, m.Doc)
	m.NativeReturn = rr.native(d.ReturnType)
	m.Return = rr.buildReturn(d.ReturnType, m.Doc)
	if m.Doc != nil {
		m.Deprecated = m.Doc.Deprecated
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
			c.addProp(&copyP)
		}
		for _, tk := range t.Consts {
			copyK := *tk
			copyK.Class = c
			c.addConst(&copyK)
		}
	}
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
			m[tp.Name] = tp.Bound
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
