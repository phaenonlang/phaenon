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

// TableEntry is the method PHP's method table of c holds under name, and
// the class whose declaration put it there (tableEntry); nil for none.
func (c *Class) TableEntry(name string) (*Method, *Class) {
	return tableEntry(c, strings.ToLower(name), map[*Class]bool{})
}

// AbstractConstructor is the abstract constructor a constructor of c is
// held to (abstractConstructor); nil for none.
func (c *Class) AbstractConstructor() *Method {
	return abstractConstructor(c)
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
			cur = narrowReturn(cur, pe, h)
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
		cur = widenParam(ce, pe, h)
		changed = true
	}
	if !changed || !isNative(cur) {
		return nil, false
	}
	return cur, true
}

// widenParam is the parameter type that accepts what own, a parameter's
// type, and pe, an overridden declaration's that own does not cover,
// accept: pe when it covers own, else their union, or pe when the union
// has no PHP spelling.
func widenParam(own, pe types.Type, h types.Hierarchy) types.Type {
	if types.IsSubtype(own, pe, h) {
		return pe
	}
	if both := phpUnion(h, own, pe); isNative(both) {
		return both
	}
	return pe
}

// narrowReturn is the return type within both own, a return's type, and
// pe, an overridden declaration's that own exceeds: pe when it is within
// own, else what the two have in common. Where that is no PHP type, an
// intersection the two only may share (iterable&object), or void or never
// under a demand for a value (a type argument void for an R the parent
// returns), it is pe: the checker does not refuse such an override yet,
// and pe at least loads.
func narrowReturn(own, pe types.Type, h types.Hierarchy) types.Type {
	if types.IsSubtype(pe, own, h) {
		return pe
	}
	both := phpUnion(h, types.Intersect(own, pe, h))
	if !isNative(both) || noValue(both) {
		return pe
	}
	for _, m := range types.Members(both) {
		if _, shared := m.(types.Intersection); shared {
			return pe
		}
	}
	return both
}

// phpUnion is the union of ts as a PHP declaration may write it: mixed
// alone, and without a member that another member accepts, since PHP
// refuses a union it can see is redundant (`Foo|object`,
// `Traversable|iterable`, `array|iterable`, `bool|false`, a type twice).
func phpUnion(h types.Hierarchy, ts ...types.Type) types.Type {
	var all []types.Type
	for _, t := range ts {
		if types.IsMixed(t) {
			return types.TMixed
		}
		all = append(all, types.Members(t)...)
	}
	var keep []types.Type
	for i, a := range all {
		redundant := false
		for j, b := range all {
			if i == j || !types.IsSubtype(a, b, h) {
				continue
			}
			// b accepts a: a goes, unless the two are the same type and a
			// is the first of them.
			if !types.IsSubtype(b, a, h) || j < i {
				redundant = true
				break
			}
		}
		if !redundant {
			keep = append(keep, a)
		}
	}
	return types.MakeUnion(keep...)
}

// noValue reports a type that mentions void or never, which no value is
// of, a member of a union or an intersection's included.
func noValue(t types.Type) bool {
	for _, m := range types.Members(t) {
		switch m := m.(type) {
		case types.Void, types.Never:
			return true
		case types.Intersection:
			for _, p := range m.Types {
				if noValue(p) {
					return true
				}
			}
		}
	}
	return false
}

func isNative(t types.Type) bool {
	_, ok := types.Native(t)
	return ok
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
// PHPStan inherits @throws with the rest of a docblock (ThrowsDeclarer).
// nil when nothing declares it (design/tooling.md §9.1).
func (m *Method) DeclaredThrows() types.Type {
	if d := m.ThrowsDeclarer(); d != nil {
		return d.Throws
	}
	return nil
}

// ThrowsDeclarer is the method whose declaration DeclaredThrows reads: m
// when it declares what it throws, else the first method that does among
// those m overrides, in the order PHPStan inherits a docblock: the
// parent's method first, with what that one inherits, then the methods of
// the class's immediate interfaces in the order declared, each with what
// it inherits (Overridden). A constructor reads the parent's constructor
// too, concrete as it may be, as PHPStan does; PHP compares no signature
// with it, which is why Overridden leaves it out. nil when none does.
func (m *Method) ThrowsDeclarer() *Method {
	return m.throwsDeclarer(map[*Method]bool{})
}

func (m *Method) throwsDeclarer(seen map[*Method]bool) *Method {
	if m.Throws != nil {
		return m
	}
	if seen[m] || len(seen) > maxOverrideDepth {
		return nil
	}
	seen[m] = true
	for _, d := range m.inheritsThrowsFrom() {
		if t := d.throwsDeclarer(seen); t != nil {
			return t
		}
	}
	return nil
}

// inheritsThrowsFrom is Overridden, with a constructor's parent
// constructor first: the one the parent's table holds, unless it is
// private, which PHPStan inherits nothing from.
func (m *Method) inheritsThrowsFrom() []*Method {
	over := m.Overridden()
	if m.Class == nil || !strings.EqualFold(m.Name, "__construct") {
		return over
	}
	entry, _ := tableEntry(m.Class.ParentClass(), "__construct", map[*Class]bool{m.Class: true})
	if entry == nil || entry.Vis == Private && !entry.Abstract {
		return over
	}
	return append([]*Method{entry}, slices.DeleteFunc(slices.Clone(over), func(d *Method) bool { return d == entry })...)
}
