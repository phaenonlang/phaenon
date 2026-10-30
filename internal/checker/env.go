package checker

import (
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// varInfo is what the flow analysis knows about one variable.
type varInfo struct {
	t     types.Type
	maybe bool // might not be defined on some path
	// native is the PHP-declared type behind t when a docblock or a generic
	// refined it, nil when t is certain. Impossibility diagnostics (an
	// instanceof that is always true, a === that is always false) need both
	// types to agree, as PHPStan does with treatPhpDocTypesAsCertain off.
	native types.Type
}

// state is the abstract state at one program point: the type of every
// variable, narrowed member paths, and whether the point is reachable.
type state struct {
	vars  map[string]varInfo
	paths map[string]types.Type // "$this->x", "$a['k']" → narrowed type
	dead  bool
}

func newEnv() *state {
	return &state{vars: map[string]varInfo{}, paths: map[string]types.Type{}}
}

func (e *state) clone() *state {
	n := &state{vars: make(map[string]varInfo, len(e.vars)), paths: make(map[string]types.Type, len(e.paths)), dead: e.dead}
	for k, v := range e.vars {
		n.vars[k] = v
	}
	for k, v := range e.paths {
		n.paths[k] = v
	}
	return n
}

// set defines or redefines a variable and drops narrowings under it.
func (e *state) set(name string, t types.Type) {
	e.vars[name] = varInfo{t: t}
	e.invalidatePrefix("$" + name)
	e.invalidateMentions(name)
}

// invalidateMentions drops the paths that pass the variable as an argument
// or use it as an index (`$e->last($x)`, `$events[$i]`): another value
// addresses another member.
func (e *state) invalidateMentions(name string) {
	needle := "$" + name
	for k := range e.paths {
		for i := 0; i < len(k); {
			j := strings.Index(k[i:], needle)
			if j < 0 {
				break
			}
			j += i
			end := j + len(needle)
			if j > 0 && strings.ContainsRune("([,", rune(k[j-1])) && end < len(k) && strings.ContainsRune(",)]", rune(k[end])) {
				delete(e.paths, k)
				break
			}
			i = end
		}
	}
}

// setMaybe marks a variable as possibly undefined (after a merge).
func (e *state) setMaybe(name string, t types.Type) {
	e.vars[name] = varInfo{t: t, maybe: true}
}

func (e *state) get(name string) (varInfo, bool) {
	v, ok := e.vars[name]
	return v, ok
}

// setNative records the PHP-declared type behind a variable's refined type;
// nil, or a type equal to the refined one, marks the variable certain.
func (e *state) setNative(name string, native types.Type) {
	v, ok := e.vars[name]
	if !ok {
		return
	}
	if native == nil || types.Equal(native, v.t) {
		v.native = nil
	} else {
		v.native = native
	}
	e.vars[name] = v
}

// joinNative merges the native types of a variable at a join point.
func joinNative(a, b varInfo) types.Type {
	if a.native == nil && b.native == nil {
		return nil
	}
	na, nb := a.native, b.native
	if na == nil {
		na = a.t
	}
	if nb == nil {
		nb = b.t
	}
	return types.MakeUnion(na, nb)
}

// narrowNative narrows the native type along with the refined one; a
// native type the narrowing leaves nothing of, or equal to the refined
// type, marks the variable certain.
func narrowNative(native, t types.Type, p *program.Program) types.Type {
	if native == nil {
		return nil
	}
	n := types.Intersect(native, types.Erase(t), p) // list<Foo> narrows PHP's view to array, not to list<Foo>
	if _, never := n.(types.Never); never || types.Equal(n, t) {
		return nil
	}
	return n
}

func (e *state) unset(name string) {
	delete(e.vars, name)
	e.invalidatePrefix("$" + name)
}

func (e *state) setPath(key string, t types.Type) {
	e.paths[key] = t
}

func (e *state) getPath(key string) (types.Type, bool) {
	t, ok := e.paths[key]
	return t, ok
}

// invalidatePrefix drops every narrowed path that starts with prefix
// followed by a member or offset separator (or equals it).
func (e *state) invalidatePrefix(prefix string) {
	for k := range e.paths {
		if k == prefix || (strings.HasPrefix(k, prefix) && (strings.HasPrefix(k[len(prefix):], "->") || strings.HasPrefix(k[len(prefix):], "[") || strings.HasPrefix(k[len(prefix):], "::"))) {
			delete(e.paths, k)
		}
	}
}

// invalidateCalls drops the narrowed method-call paths under a receiver:
// `$dm->flush()` is no longer null once `$dm->add($x)` has run.
func (e *state) invalidateCalls(receiver string) {
	for k := range e.paths {
		if strings.HasPrefix(k, receiver+"->") && strings.Contains(k[len(receiver):], "(") {
			delete(e.paths, k)
		}
	}
}

// argPathKeys spells a call's arguments for a path, when every one is a
// constant or a variable.
func argPathKeys(args *ast.ArgList) ([]string, bool) {
	var keys []string
	for _, a := range args.Args {
		if a.Unpack || a.Value == nil {
			return nil, false
		}
		k, ok := constantArgKey(a.Value)
		if !ok {
			return nil, false
		}
		if a.Name != nil {
			k = a.Name.Name + ":" + k
		}
		keys = append(keys, k)
	}
	return keys, true
}

// constantArgKey spells a constant or variable argument for a call path: a
// literal, a constant, a class constant (`Stamp::class`) or `$x`.
func constantArgKey(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.Variable:
		return "$" + e.Name, true
	case *ast.StringLit:
		return "'" + program.StringValue(e) + "'", true
	case *ast.IntLit:
		return e.Raw, true
	case *ast.ConstFetch:
		return strings.ToLower(strings.Join(e.Name.Parts, "\\")), true
	case *ast.ClassConstFetch:
		n, ok := e.Class.(*ast.Name)
		id, isIdent := e.Name.(*ast.Ident)
		if !ok || !isIdent {
			return "", false
		}
		return strings.ToLower(strings.Join(n.Parts, "\\")) + "::" + id.Name, true
	}
	return "", false
}

// invalidateMembers drops every property narrowing (after a call that may
// mutate objects) but keeps offset narrowings on local arrays, which are
// values.
func (e *state) invalidateMembers() {
	for k := range e.paths {
		if strings.Contains(k, "->") || strings.Contains(k, "::") {
			delete(e.paths, k)
		}
	}
}

// join merges two environments at a control-flow meet.
func join(a, b *state) *state {
	if a.dead {
		return b.clone()
	}
	if b.dead {
		return a.clone()
	}
	n := newEnv()
	for k, va := range a.vars {
		if vb, ok := b.vars[k]; ok {
			n.vars[k] = varInfo{t: types.MakeUnion(va.t, vb.t), maybe: va.maybe || vb.maybe, native: joinNative(va, vb)}
		} else {
			n.vars[k] = varInfo{t: va.t, maybe: true, native: va.native}
		}
	}
	for k, vb := range b.vars {
		if _, ok := a.vars[k]; !ok {
			n.vars[k] = varInfo{t: vb.t, maybe: true, native: vb.native}
		}
	}
	for k, ta := range a.paths {
		if tb, ok := b.paths[k]; ok {
			n.paths[k] = types.MakeUnion(ta, tb)
		}
	}
	return n
}

// joinAll merges several environments.
func joinAll(es ...*state) *state {
	var out *state
	for _, e := range es {
		if e == nil {
			continue
		}
		if out == nil {
			out = e.clone()
			continue
		}
		out = join(out, e)
	}
	if out == nil {
		out = newEnv()
		out.dead = true
	}
	return out
}

// equal reports whether two environments agree on every variable (for the
// loop fixed point).
func (e *state) equal(o *state) bool {
	if e.dead != o.dead || len(e.vars) != len(o.vars) {
		return false
	}
	for k, v := range e.vars {
		w, ok := o.vars[k]
		if !ok || v.maybe != w.maybe || !types.Equal(v.t, w.t) {
			return false
		}
	}
	return true
}

// widen generalises the variables that changed between prev and e so loop
// iteration converges: literals become their primitive, lists stay lists.
func (e *state) widen(prev *state) {
	for k, v := range e.vars {
		if pv, ok := prev.vars[k]; ok && !types.Equal(pv.t, v.t) {
			e.vars[k] = varInfo{t: widenType(v.t), maybe: v.maybe}
		}
	}
}

func widenType(t types.Type) types.Type {
	return types.Map(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Int:
			return types.TInt
		case types.StringLit:
			return types.TString
		case types.FloatLit:
			return types.TFloat
		case types.BoolLit:
			return types.TBool
		case types.Shape:
			if len(m.Entries) == 0 {
				return m // `[]` has no keys to widen; the first entry decides them
			}
			values := widenType(shapeValues(m))
			if m.List {
				return types.Array{Key: types.TInt, Value: values, List: true}
			}
			return types.Array{Key: widenType(shapeKeys(m)), Value: values}
		case types.Array:
			return types.Array{Key: widenType(m.Key), Value: widenType(m.Value), List: m.List}
		}
		return m
	})
}

func shapeKeys(s types.Shape) types.Type {
	var ks []types.Type
	for _, e := range s.Entries {
		if e.IsInt {
			ks = append(ks, types.IntLit(e.IntKey))
		} else {
			ks = append(ks, types.StringLit{V: e.Key})
		}
	}
	if len(ks) == 0 {
		return types.TArrayKey
	}
	return types.MakeUnion(ks...)
}

func shapeValues(s types.Shape) types.Type {
	var vs []types.Type
	for _, e := range s.Entries {
		vs = append(vs, e.Type)
	}
	if len(vs) == 0 {
		return types.TMixed
	}
	return types.MakeUnion(vs...)
}

// names returns the variable names, sorted (for deterministic diagnostics).
func (e *state) names() []string {
	out := make([]string, 0, len(e.vars))
	for k := range e.vars {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pathKey returns the narrowing key of an expression that denotes a
// stable path: $x, $x->p, $x['k'], Foo::$p, $this->p->q.
func pathKey(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.Variable:
		return "$" + e.Name, true
	case *ast.Paren:
		return pathKey(e.Expr)
	case *ast.PropertyFetch:
		base, ok := pathKey(e.Object)
		if !ok {
			return "", false
		}
		id, ok := e.Name.(*ast.Ident)
		if !ok {
			return "", false
		}
		return base + "->" + id.Name, true // ?-> addresses the same member
	case *ast.MethodCall:
		// `$e->getPrevious()`, `$envelope->last(Stamp::class)`: a call whose
		// arguments are constants is a path PHPStan narrows too
		// (assertNotNull($envelope->last(X::class))); it holds until the
		// receiver is used otherwise.
		if e.Args == nil || e.Args.FirstClassCallable {
			return "", false
		}
		argKeys, ok := argPathKeys(e.Args)
		if !ok {
			return "", false
		}
		base, ok := pathKey(e.Object)
		if !ok {
			return "", false
		}
		id, ok := e.Name.(*ast.Ident)
		if !ok {
			return "", false
		}
		return base + "->" + id.Name + "(" + strings.Join(argKeys, ",") + ")", true
	case *ast.StaticCall:
		// `self::anchoring()`, `Foo::bar($x)`: a static call with constant
		// or variable arguments is a path as a method call is.
		if e.Args == nil || e.Args.FirstClassCallable {
			return "", false
		}
		n, ok := e.Class.(*ast.Name)
		if !ok {
			return "", false
		}
		id, ok := e.Name.(*ast.Ident)
		if !ok {
			return "", false
		}
		argKeys, ok := argPathKeys(e.Args)
		if !ok {
			return "", false
		}
		return strings.ToLower(strings.Join(n.Parts, "\\")) + "::" + id.Name + "(" + strings.Join(argKeys, ",") + ")", true
	case *ast.StaticPropertyFetch:
		n, ok := e.Class.(*ast.Name)
		if !ok {
			return "", false
		}
		v, ok := e.Name.(*ast.Variable)
		if !ok {
			return "", false
		}
		return strings.ToLower(strings.Join(n.Parts, "\\")) + "::$" + v.Name, true
	case *ast.Index:
		base, ok := pathKey(e.Array)
		if !ok || e.Index == nil {
			return "", false
		}
		switch i := e.Index.(type) {
		case *ast.Variable:
			return base + "[$" + i.Name + "]", true // until $i is assigned again
		case *ast.StringLit:
			return base + "['" + program.StringValue(i) + "']", true
		case *ast.IntLit:
			return base + "[" + i.Raw + "]", true
		case *ast.ClassConstFetch:
			// A constant key names one offset, whatever its value.
			n, ok := i.Class.(*ast.Name)
			id, isIdent := i.Name.(*ast.Ident)
			if ok && isIdent {
				return base + "[" + strings.ToLower(strings.Join(n.Parts, "\\")) + "::" + id.Name + "]", true
			}
		}
		return "", false
	}
	return "", false
}
