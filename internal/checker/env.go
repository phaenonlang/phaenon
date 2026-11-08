package checker

import (
	"math"
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
	// conds remembers what a condition the variable was assigned from
	// narrowed: `$ok = is_string($v) && …` records that $v is a string
	// when $ok is true, so `if (!$ok) { return; }` leaves $v a string, as
	// PHPStan's conditional expressions do. Dropped when the variable or
	// one the condition narrowed is assigned again.
	conds []condHolder
	// binary is the bytes type of a variable `$b is string` narrowed to
	// text (design/strings.md §6): the value is still those bytes, so where
	// the branch meets one that holds the untested bytes, it gives them
	// back as binary data. Dropped when the variable is assigned.
	binary types.Type
}

// condHolder is one branch of a condition a variable was assigned from:
// when the variable's narrowed type fits whenType, the branch was taken,
// and narrowed says what the variables mentioned were in it.
type condHolder struct {
	whenType types.Type
	narrowed map[string]types.Type
}

// condBranch records the narrowings of one branch of a condition against
// the environment before it. A branch that narrowed nothing is still a
// branch the value may come from, so it is kept with nothing to apply;
// only an unreachable branch is nil.
func condBranch(whenType types.Type, branch, before *state) *condHolder {
	if branch.dead {
		return nil
	}
	narrowed := map[string]types.Type{}
	for k, v := range branch.vars {
		if b, ok := before.vars[k]; ok && !types.Equal(b.t, v.t) {
			narrowed[k] = v.t
		}
	}
	return &condHolder{whenType: whenType, narrowed: narrowed}
}

// setConds attaches the condition branches to a variable just assigned;
// nothing is kept when no branch narrowed anything.
func (e *state) setConds(name string, holders ...*condHolder) {
	info, ok := e.vars[name]
	if !ok {
		return
	}
	info.conds = nil
	any := false
	for _, h := range holders {
		if h != nil {
			info.conds = append(info.conds, *h)
			any = any || len(h.narrowed) > 0
		}
	}
	if !any {
		info.conds = nil
	}
	e.vars[name] = info
}

// applyConds narrows the variables a condition assigned to name mentioned,
// once name itself is narrowed to t: the branches whose type t fits were
// the ones taken. Both branches fitting says nothing.
func (e *state) applyConds(name string, t types.Type, p *program.Program) {
	info, ok := e.vars[name]
	if !ok || len(info.conds) == 0 {
		return
	}
	var taken []condHolder
	for _, c := range info.conds {
		if types.IsSubtype(t, c.whenType, p) {
			taken = append(taken, c)
		}
	}
	if len(taken) != 1 {
		return
	}
	for k, nt := range taken[0].narrowed {
		cur, ok := e.vars[k]
		if !ok {
			continue
		}
		r := types.Intersect(cur.t, nt, p)
		if _, never := r.(types.Never); never {
			continue
		}
		cur.t = r
		cur.native = narrowNative(cur.native, r, p)
		e.vars[k] = cur
	}
}

// dropConds forgets the conditions that mention name: the variable it
// narrowed has another value now.
func (e *state) dropConds(name string) {
	for k, v := range e.vars {
		if len(v.conds) == 0 {
			continue
		}
		kept := v.conds[:0:0]
		for _, c := range v.conds {
			if _, mentions := c.narrowed[name]; !mentions {
				kept = append(kept, c)
			}
		}
		if len(kept) != len(v.conds) {
			v.conds = kept
			e.vars[k] = v
		}
	}
}

// state is the abstract state at one program point: the type of every
// variable, narrowed member paths, and whether the point is reachable.
type state struct {
	vars  map[string]varInfo
	paths map[string]types.Type // "$this->x", "$a['k']" → narrowed type
	dead  bool
	// inits are the properties of $this the body has certainly assigned on
	// the way here (readonly.go): unlike a path, nothing a call does takes
	// one away, and a meet keeps those assigned on every way in.
	inits map[string]bool
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
	if e.inits != nil {
		n.inits = make(map[string]bool, len(e.inits))
		for k := range e.inits {
			n.inits[k] = true
		}
	}
	return n
}

// set defines or redefines a variable and drops narrowings under it.
func (e *state) set(name string, t types.Type) {
	e.vars[name] = varInfo{t: t}
	e.invalidatePrefix("$" + name)
	e.invalidateMentions(name)
	e.dropConds(name)
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

// invalidateUnder drops the narrowed paths strictly under a receiver —
// `$this->calls` once `$this->record()` ran, which may have changed it —
// and keeps the receiver's own narrowing: the object is the same one.
func (e *state) invalidateUnder(prefix string) {
	for k := range e.paths {
		if len(k) > len(prefix) && strings.HasPrefix(k, prefix) && (strings.HasPrefix(k[len(prefix):], "->") || strings.HasPrefix(k[len(prefix):], "[") || strings.HasPrefix(k[len(prefix):], "::")) {
			delete(e.paths, k)
		}
	}
}

// invalidateOwnScope drops what was narrowed about the current object and
// the static members of its class hierarchy — `$this->…`, and paths rooted
// at `self::`, `static::`, `parent::` or a class name own accepts — unless
// keep spares the path.
func (e *state) invalidateOwnScope(own func(class string) bool, keep func(key string) bool) {
	for k := range e.paths {
		drop := strings.HasPrefix(k, "$this->") || strings.HasPrefix(k, "$this[")
		if i := strings.Index(k, "::"); !drop && i > 0 && !strings.HasPrefix(k, "$") {
			drop = own(k[:i])
		}
		if drop && !keep(k) {
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
			info := varInfo{maybe: va.maybe || vb.maybe, native: joinNative(va, vb), conds: joinConds(va.conds, vb.conds)}
			// A side that saw the bytes as text gives them back to a side
			// that still holds them untested; meeting text the other side
			// assigned, it is text too.
			switch {
			case va.binary != nil && vb.binary != nil:
				info.t, info.binary = types.MakeUnion(va.t, vb.t), types.MakeUnion(va.binary, vb.binary)
			case va.binary != nil && hasBytes(vb.t):
				info.t = types.MakeUnion(va.binary, vb.t)
			case vb.binary != nil && hasBytes(va.t):
				info.t = types.MakeUnion(va.t, vb.binary)
			default:
				info.t = types.MakeUnion(va.t, vb.t)
			}
			n.vars[k] = info
		} else {
			n.vars[k] = varInfo{t: va.t, maybe: true, native: va.native, binary: va.binary}
		}
	}
	for k, vb := range b.vars {
		if _, ok := a.vars[k]; !ok {
			n.vars[k] = varInfo{t: vb.t, maybe: true, native: vb.native, binary: vb.binary}
		}
	}
	for k, ta := range a.paths {
		if tb, ok := b.paths[k]; ok {
			// A property or an offset seen as text on one side and as
			// bytes on the other is the same value a UTF-8 test narrowed
			// on one side: its declared type says what it is.
			if u := types.MakeUnion(ta, tb); !textAndBytes(u) {
				n.paths[k] = u
			}
		}
	}
	for k := range a.inits {
		if b.inits[k] {
			n.initialise(k)
		}
	}
	return n
}

// textAndBytes reports a type with both text and bytes among its members.
func textAndBytes(t types.Type) bool {
	var text, binary bool
	for _, m := range types.Members(t) {
		switch m.(type) {
		case types.Bytes:
			binary = true
		case types.String, types.StringLit, types.ClassString:
			text = true
		}
	}
	return text && binary
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
	if len(e.paths) != len(o.paths) {
		return false
	}
	for k, t := range e.paths {
		if w, ok := o.paths[k]; !ok || !types.Equal(t, w) {
			return false
		}
	}
	return true
}

// widen generalises the variables that changed between prev and e so loop
// iteration converges: only what moved is widened, and only as far as it
// moved (PHPStan's MutatingScope::generalizeType).
func (e *state) widen(prev *state) {
	for k, v := range e.vars {
		if pv, ok := prev.vars[k]; ok && !types.Equal(pv.t, v.t) {
			e.vars[k] = varInfo{t: generalize(pv.t, v.t, 0), maybe: v.maybe}
		}
	}
	for k, t := range e.paths {
		if pt, ok := prev.paths[k]; ok && !types.Equal(pt, t) {
			e.paths[k] = generalize(pt, t, 0) // `$this->rows[] = …` in a loop grows a path as a variable
		}
	}
}

type generalizeParts struct {
	strs, floats, bools, others []types.Type
	ints                        []types.Int
	shapes                      []types.Shape
	arrays                      []types.Array
}

func splitForGeneralize(t types.Type) generalizeParts {
	var p generalizeParts
	for _, m := range types.Members(t) {
		switch m := m.(type) {
		case types.StringLit:
			p.strs = append(p.strs, m)
		case types.FloatLit:
			p.floats = append(p.floats, m)
		case types.BoolLit:
			p.bools = append(p.bools, m)
		case types.Int:
			p.ints = append(p.ints, m)
		case types.Shape:
			p.shapes = append(p.shapes, m)
		case types.Array:
			p.arrays = append(p.arrays, m)
		default:
			p.others = append(p.others, m)
		}
	}
	return p
}

// generalizeLiterals keeps the literals when only one side has them or both
// have the same; different literals on both sides become the primitive.
func generalizeLiterals(a, b []types.Type, primitive types.Type) []types.Type {
	switch {
	case len(a) == 0:
		return b
	case len(b) == 0:
		return a
	}
	if types.Equal(types.MakeUnion(a...), types.MakeUnion(b...)) {
		return a
	}
	return []types.Type{primitive}
}

// generalizeInts opens the bound an integer moved past: a counter that grew
// is int<start, max>, one that shrank int<min, start>.
func generalizeInts(a, b []types.Int) []types.Type {
	toTypes := func(ms []types.Int) []types.Type {
		out := make([]types.Type, len(ms))
		for i, m := range ms {
			out[i] = m
		}
		return out
	}
	switch {
	case len(a) == 0:
		return toTypes(b)
	case len(b) == 0:
		return toTypes(a)
	}
	ua, ub := types.MakeUnion(toTypes(a)...), types.MakeUnion(toTypes(b)...)
	if types.Equal(ua, ub) {
		return []types.Type{ua}
	}
	lo, hi := a[0].Lo, a[0].Hi
	for _, m := range a[1:] {
		lo, hi = min(lo, m.Lo), max(hi, m.Hi)
	}
	for _, m := range b {
		if m.Lo < lo {
			lo = math.MinInt64
		}
		if m.Hi > hi {
			hi = math.MaxInt64
		}
	}
	return []types.Type{types.Int{Lo: lo, Hi: hi}}
}

// generalizeShapes: the same keys on both sides generalise entry by entry;
// otherwise the shapes become one array of their generalised keys and
// values (a list when every shape is one, non-empty when every shape is).
func generalizeShapes(a, b []types.Shape, depth int) []types.Type {
	toTypes := func(ms []types.Shape) []types.Type {
		out := make([]types.Type, len(ms))
		for i, m := range ms {
			out[i] = m
		}
		return out
	}
	switch {
	case len(a) == 0:
		return toTypes(b)
	case len(b) == 0:
		return toTypes(a)
	}
	ka, kb := shapesKeySet(a), shapesKeySet(b)
	if len(ka) == len(kb) && !shapesUnsealed(a) && !shapesUnsealed(b) {
		same := true
		for k := range ka {
			if _, ok := kb[k]; !ok {
				same = false
				break
			}
		}
		if same {
			merged := types.Shape{List: shapesAreLists(a) && shapesAreLists(b)}
			// Entries in the first shape's order, so the result is stable.
			for _, e := range a[0].Entries {
				ta, oa := shapesOffset(a, e)
				tb, ob := shapesOffset(b, e)
				merged.Entries = append(merged.Entries, types.ShapeEntry{Key: e.Key, IntKey: e.IntKey, IsInt: e.IsInt, Optional: oa || ob, Type: generalize(ta, tb, depth+1)})
			}
			return []types.Type{merged}
		}
	}
	keysA, valsA, neA := shapesKV(a)
	keysB, valsB, neB := shapesKV(b)
	list := shapesAreLists(a) && shapesAreLists(b)
	arr := types.Array{Value: generalize(valsA, valsB, depth+1), List: list, NonEmpty: neA && neB}
	if list {
		arr.Key = types.TInt
	} else {
		arr.Key = generalize(keysA, keysB, depth+1)
	}
	return []types.Type{arr}
}

func shapesKeySet(ms []types.Shape) map[types.ShapeEntry]bool {
	set := map[types.ShapeEntry]bool{}
	for _, m := range ms {
		for _, e := range m.Entries {
			set[types.ShapeEntry{Key: e.Key, IntKey: e.IntKey, IsInt: e.IsInt}] = true
		}
	}
	return set
}

func shapesUnsealed(ms []types.Shape) bool {
	for _, m := range ms {
		if m.Unsealed {
			return true
		}
	}
	return false
}

func shapesAreLists(ms []types.Shape) bool {
	for _, m := range ms {
		if !m.List && !shapeIsList(m) {
			return false
		}
	}
	return true
}

// shapesOffset is the type an offset has across several shapes, and
// whether some shape lacks it or marks it optional.
func shapesOffset(ms []types.Shape, key types.ShapeEntry) (types.Type, bool) {
	var ts []types.Type
	optional := false
	for _, m := range ms {
		found := false
		for _, e := range m.Entries {
			if e.IsInt == key.IsInt && e.IntKey == key.IntKey && e.Key == key.Key {
				ts = append(ts, e.Type)
				optional = optional || e.Optional
				found = true
			}
		}
		if !found {
			optional = true
		}
	}
	return types.MakeUnion(ts...), optional
}

// shapesKV is the union of several shapes' keys and values (never for the
// empty shape), and whether every one of them has a certain entry.
func shapesKV(ms []types.Shape) (keys, values types.Type, nonEmpty bool) {
	var ks, vs []types.Type
	nonEmpty = true
	for _, m := range ms {
		k, v, ne := arrayKV(m)
		ks, vs = append(ks, k), append(vs, v)
		nonEmpty = nonEmpty && ne
	}
	return types.MakeUnion(ks...), types.MakeUnion(vs...), nonEmpty
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
			// The shape stays, its values widen: a tuple appended by another
			// holder of the reference is still a tuple.
			entries := make([]types.ShapeEntry, len(m.Entries))
			for i, e := range m.Entries {
				e.Type = widenType(e.Type)
				entries[i] = e
			}
			m.Entries = entries
			return m
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

// generalize widens next, a loop variable's type after one more iteration,
// against prev, its type before: an unchanged type stays; literals that
// moved become the range or primitive they move in; two shapes with the
// same keys generalise entry by entry; shapes that gained keys become an
// array of their keys and values, which keeps a shape appended to a list
// intact (`$rows[] = ['a', $x]` is list<array{string, string}>, not
// list<list<string>>); arrays generalise key and value. A kind only one
// side has is kept as it is.
func generalize(prev, next types.Type, depth int) types.Type {
	if types.Equal(prev, next) {
		return next
	}
	if depth > 3 {
		return widenType(types.MakeUnion(prev, next))
	}
	a, b := splitForGeneralize(prev), splitForGeneralize(next)
	var out []types.Type
	out = append(out, generalizeLiterals(a.strs, b.strs, types.TString)...)
	out = append(out, generalizeLiterals(a.floats, b.floats, types.TFloat)...)
	out = append(out, generalizeLiterals(a.bools, b.bools, types.TBool)...)
	out = append(out, generalizeInts(a.ints, b.ints)...)
	out = append(out, generalizeShapes(a.shapes, b.shapes, depth)...)
	out = append(out, generalizeArrays(a.arrays, b.arrays, depth)...)
	out = append(out, a.others...)
	out = append(out, b.others...)
	return types.MakeUnion(out...)
}

func generalizeArrays(a, b []types.Array, depth int) []types.Type {
	toTypes := func(ms []types.Array) []types.Type {
		out := make([]types.Type, len(ms))
		for i, m := range ms {
			out[i] = m
		}
		return out
	}
	switch {
	case len(a) == 0:
		return toTypes(b)
	case len(b) == 0:
		return toTypes(a)
	}
	var keysA, valsA, keysB, valsB []types.Type
	list, nonEmpty := true, true
	for _, m := range a {
		keysA, valsA = append(keysA, m.Key), append(valsA, m.Value)
		list, nonEmpty = list && m.List, nonEmpty && m.NonEmpty
	}
	for _, m := range b {
		keysB, valsB = append(keysB, m.Key), append(valsB, m.Value)
		list, nonEmpty = list && m.List, nonEmpty && m.NonEmpty
	}
	return []types.Type{types.Array{
		Key:      generalize(types.MakeUnion(keysA...), types.MakeUnion(keysB...), depth+1),
		Value:    generalize(types.MakeUnion(valsA...), types.MakeUnion(valsB...), depth+1),
		List:     list,
		NonEmpty: nonEmpty,
	}}
}

// joinConds keeps the condition a variable carried into both branches of a
// join — the same holders, as a ternary testing the variable leaves them —
// and forgets it when the sides disagree.
func joinConds(a, b []condHolder) []condHolder {
	if len(a) == 0 || len(a) != len(b) {
		return nil
	}
	for i := range a {
		if !types.Equal(a[i].whenType, b[i].whenType) || len(a[i].narrowed) != len(b[i].narrowed) {
			return nil
		}
		for k, t := range a[i].narrowed {
			u, ok := b[i].narrowed[k]
			if !ok || !types.Equal(t, u) {
				return nil
			}
		}
	}
	return a
}
