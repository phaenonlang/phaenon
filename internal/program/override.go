package program

import (
	"slices"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/types"
)

// The override-erasure rule (design/generics.md §4.3, §11): a Phaenon
// method that overrides or implements another keeps its parameters'
// erased types at least as wide as the overridden method's and its erased
// return type no wider, since PHP checks the compiled signatures against
// each other. The emitter applies it; phn migrate predicts it, to lift a
// docblock type only when the compiled signature stays the one PHP saw.

// Overridden returns the declarations m overrides or implements that PHP
// holds it to, nil when there are none, in the order PHPStan inherits a
// docblock from them. First comes the method in the parent's method table
// (tableEntry), unless it is private, which holds m to nothing; PHP checks
// m against that one, and it was checked in turn against every interface
// above it. Then come the methods of the class's immediate interfaces:
// those it declares, but for one a parent class implements already and
// one that another of them extends, whose method the other's table holds
// (PHPStan's ClassReflection::getImmediateInterfaces). Each is the method
// that interface's table holds, its own or one it inherits. A @method tag
// is no declaration.
//
// A constructor is held only to an abstract declaration, since PHP checks
// no other constructor's signature: the abstract constructor the parent's
// table holds, or the one its concrete constructor implements in turn (its
// prototype), and the constructor of any interface above.
func (m *Method) Overridden() []*Method {
	cl := m.Class
	if cl == nil || m.Magic {
		return nil
	}
	key := strings.ToLower(m.Name)
	var out []*Method
	add := func(pm *Method) {
		if pm == nil || pm.Magic || pm == m || slices.Contains(out, pm) {
			return
		}
		out = append(out, pm)
	}
	if key == "__construct" {
		add(abstractConstructor(cl))
		for _, a := range cl.Ancestors() {
			if a.Kind == ast.KindInterface {
				add(a.ownMethod(key))
			}
		}
		return out
	}
	if entry, _ := tableEntry(cl.ParentClass(), key, map[*Class]bool{cl: true}); entry != nil && (entry.Vis != Private || entry.Abstract) {
		add(entry)
	}
	for _, i := range immediateInterfaces(cl) {
		entry, _ := tableEntry(i, key, map[*Class]bool{cl: true})
		add(entry)
	}
	return out
}

// ownMethod is the method of that name in c's own table: declared in c or
// brought in by one of its traits.
func (c *Class) ownMethod(key string) *Method {
	c.ensure()
	if m := c.methods[key]; m != nil && !m.Magic {
		return m
	}
	return nil
}

// tableEntry is the method PHP's method table of c holds under key, and
// the class whose declaration put it there, as zend_do_link_class fills
// the table: c's own method; else a concrete trait method, bound before
// inheritance whatever the order of the traits; else the parent's entry,
// private included; else a trait's abstract method, bound after
// inheritance into an empty slot only (zend_add_trait_method); else the
// first of c's interfaces, in the order declared, whose table holds the
// method, its own before what it inherits. nil for none.
func tableEntry(c *Class, key string, seen map[*Class]bool) (*Method, *Class) {
	if c == nil || seen[c] {
		return nil, nil
	}
	seen[c] = true
	own := c.ownMethod(key)
	if own != nil && (own.FromTrait == nil || !own.Abstract) {
		return own, c
	}
	p := c.File.program
	for _, name := range c.Traits {
		if t := p.Class(name); t != nil && t.Kind == ast.KindTrait {
			if tm := t.FindMethod(key); tm != nil && !tm.Abstract && !tm.Magic {
				return tm, c
			}
		}
	}
	if entry, at := tableEntry(c.ParentClass(), key, seen); entry != nil {
		return entry, at
	}
	if own != nil {
		return own, c
	}
	for _, name := range c.Interfaces {
		if i := p.Class(name); i != nil && i.Kind == ast.KindInterface {
			if entry, at := tableEntry(i, key, seen); entry != nil {
				return entry, at
			}
		}
	}
	return nil, nil
}

// abstractConstructor is the abstract constructor a constructor of cl is
// held to: the one in the parent's table when it is abstract, else the one
// that table's concrete constructor implements, up the chain. nil for none,
// and for a private constructor, which implements nothing.
func abstractConstructor(cl *Class) *Method {
	const key = "__construct"
	seen := map[*Class]bool{}
	for at := cl; at != nil && !seen[at]; {
		seen[at] = true
		entry, holder := tableEntry(at.ParentClass(), key, map[*Class]bool{at: true})
		if entry == nil || entry.Vis == Private && !entry.Abstract {
			return nil
		}
		if entry.Abstract {
			return entry
		}
		at = holder
	}
	return nil
}

// immediateInterfaces lists the interfaces cl declares (an interface: the
// ones it extends) in the order declared, without one that a parent class
// implements already, which PHP does not check cl's methods against again,
// nor one that another of them extends.
func immediateInterfaces(cl *Class) []*Class {
	cl.ensure()
	p := cl.File.program
	var declared []*Class
	for _, name := range cl.Interfaces {
		if i := p.Class(name); i != nil && i.Kind == ast.KindInterface && !slices.Contains(declared, i) {
			declared = append(declared, i)
		}
	}
	parent := cl.ParentClass()
	var out []*Class
	for _, i := range declared {
		if parent != nil && parent.IsSubclassOf(i.Name) {
			continue
		}
		indirect := false
		for _, o := range declared {
			if o != i && o.IsSubclassOf(i.Name) {
				indirect = true
				break
			}
		}
		if !indirect {
			out = append(out, i)
		}
	}
	return out
}

// ErasureOverrides gives, for a method declared in Phaenon, the erased
// types the rule imposes: per parameter index the type a parameter widens
// to, and under key -1 the type the return narrows to. nil when nothing
// changes, for a method declared in PHP, which compiles to nothing, and
// for one a trait brings in, which compiles with the trait. The
// overridden method's types are taken as PHP sees them (PHPSignature), so
// the rule holds down a chain of generic classes: a grandchild is checked
// against what its parent compiles to, not against the parent's own
// erasure.
func (m *Method) ErasureOverrides() map[int]types.Type {
	if !m.inPhaenon() || m.FromTrait != nil {
		return nil
	}
	return m.erasureOverrides(0)
}

// maxOverrideDepth bounds the walk up a hierarchy that is cyclic (an
// error the checker reports elsewhere).
const maxOverrideDepth = 64

func (m *Method) erasureOverrides(depth int) map[int]types.Type {
	if depth > maxOverrideDepth {
		return nil
	}
	decls := m.overriddenSignatures(depth)
	if len(decls) == 0 {
		return nil
	}
	var out map[int]types.Type
	set := func(k int, t types.Type) {
		if out == nil {
			out = map[int]types.Type{}
		}
		out[k] = t
	}
	for i, p := range m.Params {
		if t, ok := m.erasureAt(decls, i, erased(p.NativeType)); ok {
			set(i, t)
		}
	}
	if m.NativeReturn != nil {
		if t, ok := m.erasureAt(decls, -1, types.Erase(m.NativeReturn)); ok {
			set(-1, t)
		}
	}
	return out
}

// ErasureAt is the type the override-erasure rule compiles at slot of m,
// a parameter's index or -1 for the return, where m's declaration erases
// to own (nil: no type): a parameter widens until it accepts everything
// each overridden declaration accepts, a return narrows until it is
// within each one's. ok is false when own meets them all as it is, or
// when the type it would take has no PHP spelling. phn migrate asks it
// for a docblock type it would lift.
func (m *Method) ErasureAt(slot int, own types.Type) (types.Type, bool) {
	return m.erasureAt(m.overriddenSignatures(0), slot, own)
}

// signature is a declaration's parameter and return types as PHP sees
// them.
type signature struct {
	params []types.Type
	ret    types.Type
}

func (m *Method) overriddenSignatures(depth int) []signature {
	var out []signature
	for _, d := range m.Overridden() {
		params, ret := d.phpSignature(depth + 1)
		out = append(out, signature{params, ret})
	}
	return out
}

func (m *Method) erasureAt(decls []signature, slot int, own types.Type) (types.Type, bool) {
	h := m.Class.File.program
	cur := own
	changed := false
	for _, d := range decls {
		if slot < 0 {
			if d.ret == nil || cur == nil {
				continue
			}
			pe := types.Erase(d.ret)
			if types.IsSubtype(cur, pe, h) {
				continue
			}
			if types.IsSubtype(pe, cur, h) {
				cur = pe
			} else if both := types.Intersect(cur, pe, h); isNative(both) {
				cur = both
			} else {
				cur = pe
			}
			changed = true
			continue
		}
		if slot >= len(d.params) {
			continue
		}
		pe, ce := orMixed(erased(d.params[slot])), orMixed(cur)
		if types.IsSubtype(pe, ce, h) {
			continue
		}
		if types.IsSubtype(ce, pe, h) {
			cur = pe
		} else if both := widen(ce, pe, h); isNative(both) {
			cur = both
		} else {
			cur = pe
		}
		changed = true
	}
	if !changed || !isNative(cur) {
		return nil, false
	}
	return cur, true
}

func isNative(t types.Type) bool {
	_, ok := types.Native(t)
	return ok
}

// widen is a type that accepts what t and u accept, for two that do not
// contain each other: their union, without t's members that u already
// accepts, which PHP may refuse as redundant (`Foo|object`).
func widen(t, u types.Type, h types.Hierarchy) types.Type {
	if types.IsMixed(t) || types.IsMixed(u) {
		return types.TMixed
	}
	keep := []types.Type{u}
	for _, mt := range types.Members(t) {
		if !types.IsSubtype(mt, u, h) {
			keep = append(keep, mt)
		}
	}
	return types.MakeUnion(keep...)
}

// PHPSignature returns the native types PHP sees in m's declaration, one
// per parameter and the return, nil where none is declared: a PHP
// declaration's own, a Phaenon declaration's erasure under the override-
// erasure rule — what the compiled method declares.
func (m *Method) PHPSignature() (params []types.Type, ret types.Type) {
	return m.phpSignature(0)
}

func (m *Method) phpSignature(depth int) ([]types.Type, types.Type) {
	params := make([]types.Type, len(m.Params))
	if !m.inPhaenon() {
		for i, p := range m.Params {
			params[i] = p.NativeType
		}
		return params, m.NativeReturn
	}
	for i, p := range m.Params {
		params[i] = erased(p.NativeType)
	}
	ret := erased(m.NativeReturn)
	if m.FromTrait != nil {
		return params, ret // compiled with the trait, against nothing it overrides
	}
	for k, t := range m.erasureOverrides(depth) {
		if k < 0 {
			ret = t
		} else {
			params[k] = t
		}
	}
	return params, ret
}

// inPhaenon reports whether the method is declared in a .phn source, the
// only kind the emitter compiles.
func (m *Method) inPhaenon() bool {
	decl := m.Class
	if m.FromTrait != nil {
		decl = m.FromTrait
	}
	return decl != nil && decl.File != nil && decl.File.Mode == lexer.ModePhaenon
}

func erased(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	return types.Erase(t)
}

func orMixed(t types.Type) types.Type {
	if t == nil {
		return types.TMixed
	}
	return t
}

// DeclaredThrows is what a call of m is known to throw: m's own
// declaration, else the one it inherits from the methods it overrides, as
// PHPStan inherits @throws with the rest of a docblock: the parent's
// method first, with what that one inherits, then the methods of the
// class's immediate interfaces in the order declared (Overridden). nil
// when nothing declares it (design/tooling.md §9.1).
func (m *Method) DeclaredThrows() types.Type {
	return m.declaredThrows(map[*Method]bool{})
}

func (m *Method) declaredThrows(seen map[*Method]bool) types.Type {
	if m.Throws != nil {
		return m.Throws
	}
	if seen[m] || len(seen) > maxOverrideDepth {
		return nil
	}
	seen[m] = true
	for _, d := range m.Overridden() {
		if t := d.declaredThrows(seen); t != nil {
			return t
		}
	}
	return nil
}
