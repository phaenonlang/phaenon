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
// holds it to, nil when there are none. First comes the parent's: the
// nearest declaration up the parent chain, a class's own or one a trait
// brought in, else the interface method an ancestor implements without
// declaring it, which is what the parent's method table holds; a private
// one holds m to nothing. PHP checks m against that one alone, and it was
// checked in turn against every interface above it, so an interface of an
// ancestor demands nothing more of m. Then come the methods of the
// interfaces m's class implements itself, and of the interfaces those
// extend, in the order they are declared. A @method tag is no
// declaration. This is also the order PHPStan inherits a docblock in: the
// parent's method first, then the class's own interfaces.
//
// A constructor is held only to an abstract declaration, since PHP checks
// no other constructor's signature: the nearest abstract constructor up
// the parent chain, which the parent's concrete one passes on as its
// prototype, and the constructor of any interface above.
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
	add(parentEntry(cl, key))
	for _, a := range ownInterfaces(cl) {
		add(a.ownMethod(key))
	}
	return out
}

// ownMethod is the method of that name in c's own table: declared in c or
// brought in by one of its traits.
func (c *Class) ownMethod(key string) *Method {
	c.ensure()
	return c.methods[key]
}

// parentEntry is the method PHP finds under key in the method table of
// cl's parent, nil for none or for a private one.
func parentEntry(cl *Class, key string) *Method {
	parent := cl.ParentClass()
	seen := map[*Class]bool{cl: true}
	for pc := parent; pc != nil && !seen[pc]; pc = pc.ParentClass() {
		seen[pc] = true
		if pm := pc.ownMethod(key); pm != nil && !pm.Magic {
			if pm.Vis == Private && !pm.Abstract {
				return nil
			}
			return pm
		}
	}
	if parent == nil {
		return nil
	}
	for _, a := range parent.Ancestors() {
		if a.Kind == ast.KindInterface {
			if pm := a.ownMethod(key); pm != nil {
				return pm
			}
		}
	}
	return nil
}

// abstractConstructor is the nearest abstract constructor up cl's parent
// chain, nil for none.
func abstractConstructor(cl *Class) *Method {
	seen := map[*Class]bool{cl: true}
	for pc := cl.ParentClass(); pc != nil && !seen[pc]; pc = pc.ParentClass() {
		seen[pc] = true
		if pm := pc.ownMethod("__construct"); pm != nil && pm.Abstract && !pm.Magic {
			return pm
		}
	}
	return nil
}

// ownInterfaces lists the interfaces cl declares it implements (an
// interface: extends), and the ones those extend, breadth first.
func ownInterfaces(cl *Class) []*Class {
	cl.ensure()
	p := cl.File.program
	var out []*Class
	seen := map[*Class]bool{cl: true}
	var queue []*Class
	push := func(names []string) {
		for _, n := range names {
			if ic := p.Class(n); ic != nil && !seen[ic] && ic.Kind == ast.KindInterface {
				seen[ic] = true
				out = append(out, ic)
				queue = append(queue, ic)
			}
		}
	}
	push(cl.Interfaces)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		cur.ensure()
		push(cur.Interfaces)
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
// interfaces m's class implements itself (Overridden). nil when nothing
// declares it (design/tooling.md §9.1).
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
