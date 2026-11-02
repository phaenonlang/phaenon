package program

import (
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

// Overridden returns the method m overrides or implements, as the rule
// sees it: the nearest ancestor's declaration that is not private (an
// interface's always counts), not a @method tag, and for a constructor
// only an abstract or interface one, since PHP checks no other
// constructor's signature. nil when there is none.
func (m *Method) Overridden() *Method {
	cl := m.Class
	if cl == nil || m.Magic {
		return nil
	}
	key := strings.ToLower(m.Name)
	var parent *Method
	for _, a := range cl.Ancestors() {
		if a == cl {
			continue
		}
		a.Signature()
		pm := a.FindMethod(key)
		if pm == nil || pm.Class == cl || pm.Magic {
			continue
		}
		if pm.Vis == Private && pm.Class.Kind != ast.KindInterface {
			continue
		}
		parent = pm
		break
	}
	if parent == nil {
		return nil
	}
	if key == "__construct" && !parent.Abstract && parent.Class.Kind != ast.KindInterface {
		return nil
	}
	return parent
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
	parent := m.Overridden()
	if parent == nil {
		return nil
	}
	pparams, pret := parent.phpSignature(depth + 1)
	h := m.Class.File.program
	var out map[int]types.Type
	set := func(k int, t types.Type) {
		if out == nil {
			out = map[int]types.Type{}
		}
		out[k] = t
	}
	for i, pt := range pparams {
		if i >= len(m.Params) {
			break
		}
		pe, ce := orMixed(erased(pt)), orMixed(erased(m.Params[i].NativeType))
		if !types.IsSubtype(pe, ce, h) {
			if _, ok := types.Native(pe); ok {
				set(i, pe)
			}
		}
	}
	if pret != nil && m.NativeReturn != nil {
		pe, ce := types.Erase(pret), types.Erase(m.NativeReturn)
		if !types.IsSubtype(ce, pe, h) {
			if _, ok := types.Native(pe); ok {
				set(-1, pe)
			}
		}
	}
	return out
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
