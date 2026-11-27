package checker

import (
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
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
	// guaranteed says PHP itself guarantees what the variable holds: a
	// parameter as PHP declares it (native), or a value assigned from
	// literals, constants of a fixed value and other such variables. Any
	// other write takes it away, and so does a meet with a way that lacks
	// it. A loop's entry is decided on such variables alone (entersOnce).
	guaranteed bool
	// rewritten is the write that made a value of a type parameter bounded
	// by an array the array written (assignIndex): the variable holds no
	// value of the parameter's type since, and a finding where that type
	// is expected says where it stopped being one. An assignment of the
	// whole variable forgets it, as it forgets the rest.
	rewritten *paramWrite
}

// paramWrite is a write into a value of a type parameter: the parameter,
// the span of the offset written (or unset, or iterated by reference),
// and what the variable held after it and the writes into the same value
// since, the parameter's own values left out (writtenPart).
type paramWrite struct {
	param   types.Param
	at      ast.Span
	written types.Type
}

// earlierWrite is the write of the two that came first in the source, the
// one there is where only one is: after a branch that wrote the variable
// and one that did not, it may hold the array written. The same write met
// from two ways holds what either way wrote.
func earlierWrite(a, b *paramWrite) *paramWrite {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case a.at == b.at:
		if types.Equal(a.written, b.written) {
			return a
		}
		return &paramWrite{param: a.param, at: a.at, written: types.MakeUnion(a.written, b.written)}
	case b.at.Start < a.at.Start:
		return b
	}
	return a
}

// rewrite records w on the variable and on each variable it is a
// reference to, which hold the one value.
func (e *state) rewrite(name string, w *paramWrite) {
	for _, n := range append([]string{name}, slices.Collect(maps.Keys(e.refs[name]))...) {
		if info, ok := e.vars[n]; ok {
			info.rewritten = w
			e.vars[n] = info
		}
	}
}

// condHolder is one branch of a condition a variable was assigned from:
// when the variable's narrowed type fits whenType, the branch was taken,
// and narrowed says what the variables mentioned were in it, offsets
// says what the constant offsets of local arrays were (`$n = $a['k'] ??
// null` leaves `$a['k']` set where $n is not null).
type condHolder struct {
	whenType types.Type
	narrowed map[string]types.Type
	offsets  map[string]types.Type
	// exact holds the branch only where the variable is narrowed to
	// whenType itself, not to a part of it, as PHPStan's holders of an
	// assigned `??` are matched (coalesceConds).
	exact bool
	// unless, set in place of whenType, is the type of the other branch's
	// value where this branch's is mixed: no type says "mixed but not
	// null", so the branch is held where the variable is narrowed to a
	// type that shares no value with unless, or where a test rules out
	// every value of unless though the variable stays mixed
	// (applyExclusion).
	unless types.Type
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
	offsets := map[string]types.Type{}
	for k, v := range branch.paths {
		if _, ok := offsetRoot(k); !ok {
			continue
		}
		if b, ok := before.paths[k]; !ok || !types.Equal(b, v) {
			offsets[k] = v
		}
	}
	return &condHolder{whenType: whenType, narrowed: narrowed, offsets: offsets}
}

// offsetRoot is the variable a path of constant offsets is rooted at,
// `a` for `$a['k'][0]`; false for any other path: a member, a call, an
// offset another variable names, `$this`.
func offsetRoot(path string) (string, bool) {
	name, rest, found := strings.Cut(strings.TrimPrefix(path, "$"), "[")
	if !found || !strings.HasPrefix(path, "$") || name == "this" || strings.ContainsAny(rest, "$:") || strings.Contains(rest, "->") {
		return "", false
	}
	return name, true
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
			any = any || len(h.narrowed) > 0 || len(h.offsets) > 0
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
		switch {
		case c.unless != nil:
			if !types.IsMixed(t) && !isNeverType(t) && !types.Overlaps(t, c.unless, p) {
				taken = append(taken, c)
			}
		case c.exact && types.Equal(t, c.whenType) || !c.exact && types.IsSubtype(t, c.whenType, p):
			taken = append(taken, c)
		}
	}
	if len(taken) != 1 {
		return
	}
	e.applyHolder(taken[0], p)
}

// applyExclusion applies the branch of a condition assigned to name whose
// other branch gives only values a test ruled out, excluded, where name's
// type cannot show it: after `null !== $v` a mixed $v is still mixed.
func (e *state) applyExclusion(name string, excluded types.Type, p *program.Program) {
	info, ok := e.vars[name]
	if !ok || len(info.conds) == 0 {
		return
	}
	var taken []condHolder
	for _, c := range info.conds {
		if c.unless != nil && types.IsSubtype(c.unless, excluded, p) {
			taken = append(taken, c)
		}
	}
	if len(taken) == 1 {
		e.applyHolder(taken[0], p)
	}
}

// applyHolder narrows the variables and offsets one branch of a condition
// narrowed, the branch known to be taken.
func (e *state) applyHolder(h condHolder, p *program.Program) {
	for k, nt := range h.narrowed {
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
	for k, nt := range h.offsets {
		if cur, ok := e.paths[k]; ok {
			if r := types.Intersect(cur, nt, p); !isNeverType(r) {
				e.paths[k] = r
			}
			continue
		}
		e.paths[k] = nt
	}
}

// dropConds forgets what the conditions say of name, a variable they
// narrowed or an array whose offsets they set: it has another value now.
// What a condition says of other variables stands: `$ok = isset($a['k'])
// && $x !== null` still says $x is not null where $ok is true once $a is
// sorted.
func (e *state) dropConds(name string) {
	e.forgetInConds(func(k string) bool { return k == name }, func(k string) bool {
		root, _ := offsetRoot(k)
		return root == name
	})
}

// written forgets what the conditions say of name and what name holds of
// one, as an assignment does, where name was written in a way that leaves
// its type as it was: a by-reference argument of a function, a `@var` tag.
// The variables it is a reference to are written too.
func (e *state) written(name string) {
	e.writtenOne(name)
	for o := range e.refs[name] {
		e.writtenOne(o)
	}
}

func (e *state) writtenOne(name string) {
	if info, ok := e.vars[name]; ok && info.conds != nil {
		info.conds = nil
		e.vars[name] = info
	}
	e.dropConds(name)
}

// reordered is written for an array whose int keys were renumbered and
// whose string keys kept their values (array_multisort()): what the
// conditions say of its string offsets stands.
func (e *state) reordered(name string) {
	e.reorderedOne(name)
	for o := range e.refs[name] {
		e.reorderedOne(o)
	}
}

func (e *state) reorderedOne(name string) {
	if info, ok := e.vars[name]; ok && info.conds != nil {
		info.conds = nil
		e.vars[name] = info
	}
	e.forgetInConds(func(k string) bool { return k == name }, func(k string) bool {
		root, _ := offsetRoot(k)
		_, key, _ := strings.Cut(k, "[")
		return root == name && !strings.HasPrefix(key, "'")
	})
}

// forgetConds forgets every condition: include, extract() and a variable
// variable may write any variable.
func (e *state) forgetConds() {
	for name, info := range e.vars {
		if info.conds != nil {
			info.conds = nil
			e.vars[name] = info
		}
	}
}

// forgetInConds takes out of every condition branch the variables it
// narrowed that narrowed reports and the offsets it set that offset
// reports, and keeps the rest of the branch, a branch still where nothing
// is left. A branch is shared between states, so a changed one is a copy.
func (e *state) forgetInConds(narrowed, offset func(key string) bool) {
	for name, v := range e.vars {
		if len(v.conds) == 0 {
			continue
		}
		var conds []condHolder
		for i, c := range v.conds {
			kept, changed := c, false
			for k := range c.narrowed {
				if narrowed(k) {
					if !changed {
						kept, changed = c.copy(), true
					}
					delete(kept.narrowed, k)
				}
			}
			for k := range c.offsets {
				if offset(k) {
					if !changed {
						kept, changed = c.copy(), true
					}
					delete(kept.offsets, k)
				}
			}
			if changed && conds == nil {
				conds = append([]condHolder{}, v.conds...)
			}
			if changed {
				conds[i] = kept
			}
		}
		if conds != nil {
			v.conds = conds
			e.vars[name] = v
		}
	}
}

// copy is the branch with maps of its own.
func (c condHolder) copy() condHolder {
	n := c
	n.narrowed = make(map[string]types.Type, len(c.narrowed))
	for k, t := range c.narrowed {
		n.narrowed[k] = t
	}
	n.offsets = make(map[string]types.Type, len(c.offsets))
	for k, t := range c.offsets {
		n.offsets[k] = t
	}
	return n
}

// state is the abstract state at one program point: the type of every
// variable, narrowed member paths, and whether the point is reachable.
type state struct {
	vars  map[string]varInfo
	paths map[string]types.Type // "$this->x", "$a['k']" → narrowed type
	dead  bool
	// silenced holds the variables ("$item") and paths a test reported as
	// always true or always false narrowed to never in this branch, which
	// cannot run: there such a value draws no second finding
	// (silencedNever). The set is shared between clones and never changed
	// in place.
	silenced map[string]bool
	// inits are the properties of $this the body has certainly assigned on
	// the way here (readonly.go): unlike a path, nothing a call does takes
	// one away, and a meet keeps those assigned on every way in.
	inits map[string]bool
	// outs are the parameters documented @param-out the body has written
	// on the way here (paramout.go): true on every way in, false on some.
	outs map[string]bool
	// refs are the variables each variable is a reference to, `$alias = &$r`
	// both ways: a write to one is a write to the others (set), as
	// PHPStan's IntertwinedVariableByReferenceWithExpr has it, and as it has
	// it the link outlives an unset() of either and a new reference, which
	// in PHP would break it; a meet keeps what every way in has.
	refs map[string]map[string]bool
}

func newEnv() *state {
	return &state{vars: map[string]varInfo{}, paths: map[string]types.Type{}}
}

func (e *state) clone() *state {
	n := &state{vars: make(map[string]varInfo, len(e.vars)), paths: make(map[string]types.Type, len(e.paths)), dead: e.dead, silenced: e.silenced}
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
	if e.outs != nil {
		n.outs = make(map[string]bool, len(e.outs))
		for k, every := range e.outs {
			n.outs[k] = every
		}
	}
	for k, others := range e.refs {
		for o := range others {
			n.addRef(k, o)
		}
	}
	return n
}

// set defines or redefines a variable and drops narrowings under it, and
// so of each variable it is a reference to: PHP writes the one value.
func (e *state) set(name string, t types.Type) {
	e.setOne(name, t)
	for o := range e.refs[name] {
		e.setOne(o, t)
	}
}

func (e *state) setOne(name string, t types.Type) {
	e.vars[name] = varInfo{t: t}
	if !isNeverType(t) {
		e.unsilence("$" + name) // a value again: a later never is another test's
	}
	e.invalidatePrefix("$" + name)
	e.invalidateMentions(name)
	e.dropConds(name)
}

// link makes a and b references to one value, `$a = &$b`, and a to every
// variable b already is one to.
func (e *state) link(a, b string) {
	for o := range e.refs[b] {
		if o != a {
			e.addRef(a, o)
		}
	}
	e.addRef(a, b)
}

func (e *state) addRef(a, b string) {
	if e.refs == nil {
		e.refs = map[string]map[string]bool{}
	}
	for _, p := range [][2]string{{a, b}, {b, a}} {
		if e.refs[p[0]] == nil {
			e.refs[p[0]] = map[string]bool{}
		}
		e.refs[p[0]][p[1]] = true
	}
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

// guarantee marks a variable as one whose type PHP itself guarantees
// (varInfo.guaranteed).
func (e *state) guarantee(name string) {
	if v, ok := e.vars[name]; ok {
		v.guaranteed = true
		e.vars[name] = v
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
	e.dropConds(name)
}

func (e *state) setPath(key string, t types.Type) {
	e.paths[key] = t
}

func (e *state) getPath(key string) (types.Type, bool) {
	t, ok := e.paths[key]
	return t, ok
}

// invalidatePrefix drops every narrowed path that starts with prefix
// followed by a member or offset separator (or equals it), and the
// condition branches that would set one.
func (e *state) invalidatePrefix(prefix string) {
	e.invalidate(func(k string) bool {
		return k == prefix || pathUnder(k, prefix)
	})
}

// pathUnder reports a path strictly under prefix: a member or an offset
// of it.
func pathUnder(k, prefix string) bool {
	if len(k) <= len(prefix) || !strings.HasPrefix(k, prefix) {
		return false
	}
	rest := k[len(prefix):]
	return strings.HasPrefix(rest, "->") || strings.HasPrefix(rest, "[") || strings.HasPrefix(rest, "::")
}

// invalidate drops the narrowed paths drop names, and takes them out of
// the condition branches that would set them, which hold what was known of
// the value before it changed: `$k = array_key_first($r)` knows `$r[$k]`
// is there where $k is not null only while $r is the array it was. What a
// branch says of anything else stands.
func (e *state) invalidate(drop func(key string) bool) {
	for k := range e.paths {
		if drop(k) {
			delete(e.paths, k)
		}
	}
	e.forgetInConds(func(string) bool { return false }, drop)
}

// invalidateUnder drops the narrowed paths strictly under a receiver —
// `$this->calls` once `$this->record()` ran, which may have changed it —
// and keeps the receiver's own narrowing: the object is the same one.
func (e *state) invalidateUnder(prefix string) {
	e.invalidate(func(k string) bool { return pathUnder(k, prefix) })
}

// invalidateOwnScope drops what was narrowed about the current object and
// the static members of its class hierarchy — `$this->…`, and paths rooted
// at `self::`, `static::`, `parent::` or a class name own accepts — unless
// keep spares the path.
func (e *state) invalidateOwnScope(own func(class string) bool, keep func(key string) bool) {
	e.invalidate(func(k string) bool {
		drop := strings.HasPrefix(k, "$this->") || strings.HasPrefix(k, "$this[")
		if i := strings.Index(k, "::"); !drop && i > 0 && !strings.HasPrefix(k, "$") {
			drop = own(k[:i])
		}
		return drop && !keep(k)
	})
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
	e.invalidate(func(k string) bool {
		return strings.Contains(k, "->") || strings.Contains(k, "::")
	})
}

// bothSilenced is what two ways into a meet both hold silenced.
func bothSilenced(a, b map[string]bool) map[string]bool {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	var n map[string]bool
	for k := range a {
		if b[k] {
			if n == nil {
				n = map[string]bool{}
			}
			n[k] = true
		}
	}
	return n
}

// silence adds keys to the silenced values of e.
func (e *state) silence(keys []string) {
	if len(keys) == 0 {
		return
	}
	n := make(map[string]bool, len(e.silenced)+len(keys))
	for k := range e.silenced {
		n[k] = true
	}
	for _, k := range keys {
		n[k] = true
	}
	e.silenced = n
}

// unsilence drops what a write of the variable or path key ends: the key
// and the paths under it.
func (e *state) unsilence(key string) {
	var n map[string]bool
	for k := range e.silenced {
		if k == key || strings.HasPrefix(k, key) && len(k) > len(key) && strings.ContainsRune("-:[", rune(k[len(key)])) {
			if n == nil {
				n = make(map[string]bool, len(e.silenced))
				for k := range e.silenced {
					n[k] = true
				}
			}
			delete(n, k)
		}
	}
	if n != nil {
		e.silenced = n
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
	n.silenced = bothSilenced(a.silenced, b.silenced)
	for k, va := range a.vars {
		if vb, ok := b.vars[k]; ok {
			n.vars[k] = varInfo{t: joinTypes(va.t, vb.t), maybe: va.maybe || vb.maybe, native: joinNative(va, vb), conds: joinConds(va.conds, vb.conds),
				guaranteed: va.guaranteed && vb.guaranteed, rewritten: earlierWrite(va.rewritten, vb.rewritten)}
		} else {
			n.vars[k] = varInfo{t: va.t, maybe: true, native: va.native, rewritten: va.rewritten}
		}
	}
	for k, vb := range b.vars {
		if _, ok := a.vars[k]; !ok {
			n.vars[k] = varInfo{t: vb.t, maybe: true, native: vb.native, rewritten: vb.rewritten}
		}
	}
	for k, ta := range a.paths {
		if tb, ok := b.paths[k]; ok {
			n.paths[k] = joinTypes(ta, tb)
		}
	}
	for k := range a.inits {
		if b.inits[k] {
			n.initialise(k)
		}
	}
	joinOuts(n, a, b)
	for k, others := range a.refs {
		for o := range others {
			if b.refs[k][o] {
				n.addRef(k, o)
			}
		}
	}
	return n
}

// joinTypes is what a variable or a path holds where two ways meet: the
// union of both. A benevolent int|float stays one only where no way holds
// an ordinary union: PHPStan's merge of two scopes takes a benevolent
// union for equal to the ordinary one of the same members, and may keep
// that one (`$y = $n; if ($c) { $y = $a / $b; }`, a loop that divides a
// benevolent value by a plain int), where its ternary keeps the mark.
func joinTypes(a, b types.Type) types.Type {
	u := types.MakeUnion(a, b)
	if types.IsBenevolent(u) && (ordinaryUnion(a) || ordinaryUnion(b)) {
		return types.MakeUnion(types.Members(u)...)
	}
	return u
}

// ordinaryUnion reports a union that is not benevolent.
func ordinaryUnion(t types.Type) bool {
	u, ok := t.(types.Union)
	return ok && !u.Benevolent
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
// loop fixed point), the conditions remembered with it included: a body
// that assigns a variable a condition narrowed changes what the condition
// says at the loop head, though no type changes.
func (e *state) equal(o *state) bool {
	if e.dead != o.dead || len(e.vars) != len(o.vars) {
		return false
	}
	for k, v := range e.vars {
		w, ok := o.vars[k]
		if !ok || v.maybe != w.maybe || !types.Equal(v.t, w.t) || !equalConds(v.conds, w.conds) {
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

// equalConds reports two lists of condition branches that say the same.
func equalConds(a, b []condHolder) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !equalOrNil(a[i].whenType, b[i].whenType) || a[i].exact != b[i].exact || !equalOrNil(a[i].unless, b[i].unless) ||
			!equalTypeMaps(a[i].narrowed, b[i].narrowed) || !equalTypeMaps(a[i].offsets, b[i].offsets) {
			return false
		}
	}
	return true
}

func equalTypeMaps(a, b map[string]types.Type) bool {
	if len(a) != len(b) {
		return false
	}
	for k, t := range a {
		if u, ok := b[k]; !ok || !types.Equal(t, u) {
			return false
		}
	}
	return true
}

// equalOrNil reports two types equal, or both absent.
func equalOrNil(a, b types.Type) bool {
	return a == nil && b == nil || a != nil && b != nil && types.Equal(a, b)
}

// widen generalises the variables that changed between prev and e so loop
// iteration converges: only what moved is widened, and only as far as it
// moved (PHPStan's MutatingScope::generalizeType).
func (e *state) widen(prev *state) {
	for k, v := range e.vars {
		if pv, ok := prev.vars[k]; ok && !types.Equal(pv.t, v.t) {
			e.vars[k] = varInfo{t: generalize(pv.t, v.t, 0), maybe: v.maybe, rewritten: v.rewritten}
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

// generalizeShapes: the same keys on both sides generalise entry by entry,
// shape by shape where each shape of one side has the one of the other
// with its keys (a union built in a loop, `array{a: 0}|array{a: 0, b: int}`,
// stays one), and otherwise into one shape whose keys are the union's,
// optional where a shape lacks them (`array{a: 0, b?: int}`, as PHPStan
// has it); other keys make one array of their generalised keys and values
// (a list when every shape is one, non-empty when every shape is).
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
			if pairs := pairShapes(a, b); pairs != nil {
				var out []types.Type
				for i, p := range pairs {
					out = append(out, generalizeShapes([]types.Shape{a[i]}, []types.Shape{p}, depth)...)
				}
				return out
			}
			merged := types.Shape{List: shapesAreLists(a) && shapesAreLists(b)}
			// Entries in the order the shapes first name them, so the
			// result is stable.
			for _, e := range shapesKeyOrder(append(append([]types.Shape{}, a...), b...)) {
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

// pairShapes gives each shape of a the shape of b with the same keys,
// where a and b are unions of as many shapes whose key sets match one to
// one; nil otherwise, and for a single shape on each side.
func pairShapes(a, b []types.Shape) []types.Shape {
	if len(a) < 2 || len(a) != len(b) {
		return nil
	}
	pairs := make([]types.Shape, len(a))
	used := make([]bool, len(b))
	for i, sa := range a {
		ka := shapesKeySet([]types.Shape{sa})
		found := false
		for j, sb := range b {
			if used[j] {
				continue
			}
			if kb := shapesKeySet([]types.Shape{sb}); len(kb) == len(ka) && sameKeys(ka, kb) {
				pairs[i], used[j], found = sb, true, true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return pairs
}

func sameKeys(a, b map[types.ShapeEntry]bool) bool {
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// shapesKeyOrder lists the keys of the shapes once each, in the order they
// are first named.
func shapesKeyOrder(ms []types.Shape) []types.ShapeEntry {
	seen := map[types.ShapeEntry]bool{}
	var keys []types.ShapeEntry
	for _, m := range ms {
		for _, e := range m.Entries {
			k := types.ShapeEntry{Key: e.Key, IntKey: e.IntKey, IsInt: e.IsInt}
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	return keys
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
	w := types.Map(t, func(m types.Type) types.Type {
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
	if types.IsBenevolent(t) && types.Equal(types.MakeUnion(types.Members(w)...), types.MakeUnion(types.Members(t)...)) {
		return types.Benevolent(w) // `$a / $b` read through a reference stays benevolent
	}
	return w
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
// stable path: $x, $x->p, $x['k'], Foo::$p, $this->p->q. An offset is
// keyed by the key PHP stores, whatever its spelling: `$a['1']`, `$a[1]`
// and `$a[0x1]` are one path, and so are `$a['name']` and the `$a[name]`
// of a string. A class constant is keyed as written; the checker's own
// pathKey keys it by its value.
func pathKey(e ast.Expr) (string, bool) {
	return pathKeyWith(e, nil)
}

// pathKeyWith is pathKey with constKey, when not nil, giving the key of an
// offset a class constant names.
func pathKeyWith(e ast.Expr, constKey func(*ast.ClassConstFetch) (string, bool)) (string, bool) {
	switch e := e.(type) {
	case *ast.Variable:
		return "$" + e.Name, true
	case *ast.Paren:
		return pathKeyWith(e.Expr, constKey)
	case *ast.PropertyFetch:
		base, ok := pathKeyWith(e.Object, constKey)
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
		base, ok := pathKeyWith(e.Object, constKey)
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
		base, ok := pathKeyWith(e.Array, constKey)
		if !ok || e.Index == nil {
			return "", false
		}
		if i, ok := e.Index.(*ast.Variable); ok {
			return base + "[$" + i.Name + "]", true // until $i is assigned again
		}
		if c, ok := e.Index.(*ast.ClassConstFetch); ok && constKey != nil {
			if k, ok := constKey(c); ok {
				return base + "[" + k + "]", true
			}
		}
		if k, ok := offsetKey(e.Index); ok {
			return base + "[" + k + "]", true
		}
		return "", false
	}
	return "", false
}

// offsetKey is the key of an offset written as a literal, as PHP stores
// it: an int for an int literal and for a string that is a canonical
// decimal integer, `'name'` for any other string, the bare word of a
// string's `$a[name]` included. A class constant is keyed as written: it
// names one offset, whatever its value.
func offsetKey(idx ast.Expr) (string, bool) {
	switch i := idx.(type) {
	case *ast.StringLit:
		return storedKey(program.StringValue(i)), true
	case *ast.Ident:
		return storedKey(i.Name), true
	case *ast.IntLit:
		if n, ok := types.IsIntLit(intLitType(i.Raw)); ok {
			return strconv.FormatInt(n, 10), true
		}
	case *ast.Unary:
		if lit, ok := i.Expr.(*ast.IntLit); ok && i.Op == token.Kind('-') {
			if n, ok := types.IsIntLit(intLitType(lit.Raw)); ok {
				return strconv.FormatInt(-n, 10), true
			}
		}
	case *ast.ClassConstFetch:
		n, ok := i.Class.(*ast.Name)
		id, isIdent := i.Name.(*ast.Ident)
		if ok && isIdent {
			return strings.ToLower(strings.Join(n.Parts, "\\")) + "::" + id.Name, true
		}
	}
	return "", false
}

// storedKey is the key of a string offset: the int a canonical decimal
// integer is, the quoted string otherwise.
func storedKey(v string) string {
	if n, ok := types.IntKey(v); ok {
		return strconv.FormatInt(n, 10)
	}
	return "'" + v + "'"
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
// with what both sides still say of each branch: a side that changed `$a`
// since says nothing of it, and the other variables the branch narrowed
// stand. Holders that disagree on when they hold are forgotten.
func joinConds(a, b []condHolder) []condHolder {
	if len(a) == 0 || len(a) != len(b) {
		return nil
	}
	var out []condHolder
	for i := range a {
		if !equalOrNil(a[i].whenType, b[i].whenType) || a[i].exact != b[i].exact || !equalOrNil(a[i].unless, b[i].unless) {
			return nil
		}
		same := len(a[i].narrowed) == len(b[i].narrowed) && len(a[i].offsets) == len(b[i].offsets)
		common := condHolder{whenType: a[i].whenType, exact: a[i].exact, unless: a[i].unless, narrowed: map[string]types.Type{}, offsets: map[string]types.Type{}}
		for k, t := range a[i].narrowed {
			if u, ok := b[i].narrowed[k]; ok && types.Equal(t, u) {
				common.narrowed[k] = t
			} else {
				same = false
			}
		}
		for k, t := range a[i].offsets {
			if u, ok := b[i].offsets[k]; ok && types.Equal(t, u) {
				common.offsets[k] = t
			} else {
				same = false
			}
		}
		if same {
			common = a[i]
		}
		out = append(out, common)
	}
	return out
}
