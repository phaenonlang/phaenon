package checker

import (
	"math"
	"slices"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/types"
)

// What a function of PHP's own leaves in an argument it takes by
// reference, as PHPStan types it once the call returns. PHPStan's
// signature map tells two kinds of such parameters apart, and the
// checker follows it (byRefReads names the second kind):
//
//   - one that creates its variable is assigned what the function
//     writes: the type of its stubs' @param-out or of its extension, and
//     mixed where it has neither, whatever the map says PHP writes. The
//     overlay says @param-out for each of them (stdlib/overlay/writes.php),
//     so the argument holds that type afterwards, and a parameter
//     documented @param-out passed there is held to its tag at the call
//     (paramout.go). openssl_encrypt()'s $tag is narrowed by the cipher
//     named at the call (openSSLTagType), preg_match()'s $matches by the
//     pattern and the flags (pregMatchesType). php-src's test extension,
//     which PHPStan does not know, has no tags: its writes hand a
//     parameter over unchecked;
//   - one that reads the argument is not written at all: the argument
//     keeps its type, and the end of a body is held to what such a
//     parameter then holds. PHPStan writes a few of them back all the
//     same, and so does the checker: sort(), rsort(), usort() and
//     shuffle() through the overlay's tags, the sorts that keep the keys
//     (sortKeepingKeys), array_pop() and array_shift() (poppedArray),
//     array_splice() (splicedArray) and array_walk() whose callback is a
//     closure written at the call that takes the value by reference
//     (walkedArray).
//
// Two of the second kind hand a parameter documented @param-out over
// unchecked, as PHPStan leaves it unheld: settype(), whose type-specifying
// extension retypes it, and a function with side effects given an
// argument that may be an object or a resource (sideEffectReads), which
// PHPStan invalidates. What the checker does not follow: the type
// settype() leaves, which the caller reads as the type the argument had;
// array_splice() on a shape with optional or further entries, and
// array_shift() on one with optional entries, which it reads as the array
// its keys and values make where PHPStan keeps the entries; an offset
// past the end of a shape, where array_splice() appends the replacement
// as PHP does and PHPStan leaves it out; and a cipher PHPStan's OpenSSL
// does not support, whose tag PHPStan types null.

// refFirst reports a closure whose first parameter is taken by reference,
// the callback array_walk() writes the array back through.
func refFirst(e *ast.Closure) bool {
	return len(e.Params) > 0 && e.Params[0].ByRef
}

// recordRefExit records what the first parameter of e, taken by
// reference, holds where the closure exits, as PHPStan gathers it for
// array_walk() from the exit points of the closure's body: its returns,
// its statements of type never (a throw, an exit, a call of a never
// function; nevers), but for a throw statement in a try statement's body
// or catch blocks, which PHPStan's try statement drops, and its end
// unless the body always ends before it. An exit in a try statement with
// a finally block leaves what the block writes (finallyRan).
// An exit where the parameter is not set adds nothing.
func (x *fnCtx) recordRefExit(e *ast.Closure, end *state, returns, nevers []*state) {
	name := e.Params[0].Var.Name
	var held, php []types.Type
	exits := append(slices.Concat(returns, nevers), end)
	for _, r := range exits {
		if r == nil || r.dead {
			continue
		}
		if info, ok := r.get(name); ok {
			held = append(held, info.t)
			if info.native != nil {
				php = append(php, info.native)
			} else {
				php = append(php, info.t)
			}
		}
	}
	if x.refExits == nil {
		x.refExits, x.refExitsPHP = map[*ast.Closure]types.Type{}, map[*ast.Closure]types.Type{}
	}
	if len(held) == 0 {
		delete(x.refExits, e)
		delete(x.refExitsPHP, e)
		return
	}
	x.refExits[e], x.refExitsPHP[e] = types.MakeUnion(held...), types.MakeUnion(php...)
}

// walkedArray is what array_walk() leaves in the array t when its callback
// is a closure written at the call that takes the value by reference: each
// value is what the closure leaves in that parameter, the keys and the
// shape stay (PHPStan's mapValueType). nil for any other callback, which
// writes nothing back PHPStan follows.
func (x *fnCtx) walkedArray(callback ast.Expr, t types.Type) types.Type {
	return x.walked(callback, t, x.refExits)
}

// walkedArrayPHP is walkedArray of PHP's type t of the array: each value
// is what PHP's type of the closure's parameter is where it exits, as
// PHPStan types the walked array natively.
func (x *fnCtx) walkedArrayPHP(callback ast.Expr, t types.Type) types.Type {
	return x.walked(callback, t, x.refExitsPHP)
}

func (x *fnCtx) walked(callback ast.Expr, t types.Type, exits map[*ast.Closure]types.Type) types.Type {
	cl, ok := unparen(callback).(*ast.Closure)
	if !ok || !refFirst(cl) {
		return nil
	}
	v, ok := exits[cl]
	if !ok {
		return nil
	}
	return types.Map(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Array:
			m.Value = v
			return m
		case types.Shape:
			entries := make([]types.ShapeEntry, len(m.Entries))
			for i, e := range m.Entries {
				e.Type = v
				entries[i] = e
			}
			m.Entries = entries
			return m
		}
		return m
	})
}

// sortKeepingKeys is t, an array a sort reorders keeping its keys
// (asort(), ksort(), uasort() and their kin): a list is then an array of
// int<0, max> keys, as PHPStan has it (makeListMaybe), and any other array
// keeps its type.
func sortKeepingKeys(t types.Type) types.Type {
	return types.Map(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Array:
			if m.List {
				m.List = false
				m.Key = types.Int{Lo: 0, Hi: math.MaxInt64}
			}
			return m
		case types.Shape:
			m.List = false
			return m
		}
		return m
	})
}

// sideEffectReads names the functions of PHP's own that PHPStan says have
// side effects (a void return, or its function metadata) and whose
// by-reference parameters it reads: given an argument that may be an
// object or a resource, it invalidates the argument, and a parameter
// documented @param-out passed there is no longer held. The rest of
// byRefReads has none, or keeps the argument as it was.
var sideEffectReads = map[string]bool{
	"mysqli_poll":            true,
	"mysqli_stmt_bind_param": true,
	"sodium_add":             true,
	"sodium_crypto_secretstream_xchacha20poly1305_rekey": true,
}

// invalidatesRead reports a by-reference argument of type t that PHPStan
// invalidates for sig, a function of PHP's own that reads it
// (sideEffectReads).
func invalidatesRead(sig *signature, t types.Type, h types.Hierarchy) bool {
	if sig.fn == nil || !sideEffectReads[strings.ToLower(sig.fn.Name)] || t == nil {
		return false
	}
	return types.IsMixed(t) || types.Overlaps(t, types.Object{}, h) || types.Overlaps(t, types.TResource, h)
}

// ownArrayWrite is what one of PHP's own array functions, a sort, the
// push or the pop of an entry, array_splice() or array_walk(), does to
// PHP's type of the array it takes by reference, where it writes it back:
// before, the PHP type of the array before the call, and certain where it
// is the checker's type itself; out, what the function leaves of before,
// computed as its out type is (nil where nothing is computed of it); and
// keepsTruth for a function that keeps any value's truthiness, a sort that
// keeps the keys.
type ownArrayWrite struct {
	before, out types.Type
	certain     bool
	keepsTruth  bool
	keepsFalsy  bool // array_walk(): an empty array stays empty, and it takes no other falsy value
	exact       bool // out is PHP's type itself, not the checker's type computed on PHP's
	truth       int  // what a test of truthiness found of the array before (foundBefore)
}

// writtenOwnArray gives the array w was written back into the PHP type the
// function leaves of the one it had, as PHPStan reads the out types of
// its own function map as native and computes them on the native type:
// sort() leaves a non-empty array non-empty, array_push() makes one, and
// array_pop() may leave none. A PHP type the checker takes from its own
// (certain) is the type written, as for any write. A PHP type that is not
// only arrays, a mixed one an inline @var leaves, holds the array the
// function declares afterwards, PHPStan's list after sort(), and keeps
// what a test of truthiness found of it only through a sort that keeps
// the keys, which leaves it as it was to PHPStan: `if ($xs) { asort($xs);
// if ($xs) … }` decides the inner test, `if ($xs) { sort($xs); … }` does
// not, under `/** @var list<string> $xs */`.
func (x *fnCtx) writtenOwnArray(env *state, e ast.Expr, w ownArrayWrite, declared types.Type) {
	var native types.Type
	keep := false
	switch {
	case w.certain:
	case w.out != nil && onlyArrays(w.before) && w.exact:
		native = w.out
	case w.out != nil && onlyArrays(w.before):
		native = phpArrayOf(w.out)
	case w.keepsTruth || w.keepsFalsy && w.truth < 0:
		native, keep = w.before, true
	default:
		native = declared
	}
	if v, ok := unparen(e).(*ast.Variable); ok {
		env.setNative(v.Name, native)
		if info, ok := env.vars[v.Name]; ok && keep && w.truth != 0 {
			info.found, info.foundTruth = info.t, w.truth
			env.vars[v.Name] = info
		}
		return
	}
	key, ok := x.narrowedPathKey(e)
	if !ok || w.certain {
		return // a certain path holds the type written (keepWritten)
	}
	env.keepNative(key, native)
	if t, ok := env.getPath(key); ok && keep && w.truth != 0 {
		env.paths[foundPathKey(key, w.truth)] = t
	}
}

// settypeKeeps reports a settype() of e after which what a test of its
// truthiness found, truth, still holds, as PHP's cast of each value PHP's
// type of it may hold keeps it (settypeCasts): an int made a string keeps
// both, a truthy string made an int may be 0, a falsy one made an array is
// a non-empty one. PHP's type of a variable an inline @var declares is
// what PHP guarantees of the value it was assigned (varInfo.held),
// `random_int()`'s int; mixed where it guarantees nothing, whose truthiness
// only 'bool' keeps: an object made an array may be empty. PHPStan types
// the value by the tag there, and decides the test after it.
func (x *fnCtx) settypeKeeps(e ast.Expr, args *ast.ArgList, env *state, truth int) bool {
	if truth == 0 || args == nil || len(args.Args) != 2 || args.Args[1].Unpack {
		return false
	}
	name, ok := x.pathType(args.Args[1].Value, env).(types.StringLit)
	if !ok {
		return false
	}
	target, ok := settypeTargets[strings.ToLower(name.V)]
	if !ok {
		return false
	}
	php := x.nativeOr(e, x.pathType(e, env), env)
	if v, ok := variableOf(e); ok && types.IsMixed(php) {
		if info, ok := env.get(v.Name); ok && info.held != nil {
			php = info.held
		}
	}
	// The values the test left: those of the truthiness it found.
	if truth > 0 {
		php = types.Truthy(php)
	} else {
		php = types.Falsy(php)
	}
	keeps := 0 // truthy keeps for 0, falsy for 1
	if truth < 0 {
		keeps = 1
	}
	for _, m := range types.Members(php) {
		source := settypeSource(m)
		if source == "" {
			if target != "bool" || mentionsParam(m) {
				return false
			}
			continue
		}
		if !settypeCasts[source][target][keeps] {
			return false
		}
	}
	return !isNeverType(php)
}

// settypeTargets are the types settype() takes, as it spells them.
var settypeTargets = map[string]string{
	"int": "int", "integer": "int", "float": "float", "double": "float", "string": "string",
	"bool": "bool", "boolean": "bool", "array": "array", "null": "null",
}

// settypeSource is the kind of value of a member of PHP's type settype()
// casts, "" for one settypeCasts says nothing of: an object, a resource,
// mixed.
func settypeSource(m types.Type) string {
	switch m.(type) {
	case types.Int:
		return "int"
	case types.Float, types.FloatLit:
		return "float"
	case types.String, types.StringLit:
		return "string"
	case types.Bool, types.BoolLit:
		return "bool"
	case types.Array, types.Shape:
		return "array"
	case types.Null:
		return "null"
	}
	return ""
}

// settypeCasts says, for a value of the source kind settype() makes the
// target type, whether a truthy one stays truthy ([0]) and a falsy one
// falsy ([1]), as php casts the edge values: 0.5 and NAN make the int 0,
// 'abc' the int 0, -0.0 the string '-0', 0, '0' and false a non-empty
// array, the empty array the string 'Array'; null makes every target
// falsy, and the null target any value.
var settypeCasts = map[string]map[string][2]bool{
	"int":    {"int": {true, true}, "float": {true, true}, "string": {true, true}, "bool": {true, true}, "array": {true, false}, "null": {false, true}},
	"float":  {"int": {false, true}, "float": {true, true}, "string": {true, false}, "bool": {true, true}, "array": {true, false}, "null": {false, true}},
	"string": {"int": {false, true}, "float": {false, true}, "string": {true, true}, "bool": {true, true}, "array": {true, false}, "null": {false, true}},
	"bool":   {"int": {true, true}, "float": {true, true}, "string": {true, true}, "bool": {true, true}, "array": {true, false}, "null": {false, true}},
	"array":  {"int": {true, true}, "float": {true, true}, "string": {true, false}, "bool": {true, true}, "array": {true, true}, "null": {false, true}},
	"null":   {"int": {false, true}, "float": {false, true}, "string": {false, true}, "bool": {false, true}, "array": {false, true}, "null": {false, true}},
}

// pushesNothing reports a call of array_push() or array_unshift() with no
// value to add, which leaves the array as it was.
func pushesNothing(sig *signature, args *ast.ArgList) bool {
	switch strings.ToLower(sig.fn.Name) {
	case "array_push", "array_unshift":
		return args != nil && len(args.Args) == 1 && !args.Args[0].Unpack
	}
	return false
}

// phpArrayOf is the PHP type of an array of type t: an array of any keys
// and values, non-empty where t is, and the empty array where t is,
// `array_splice($xs, 0)`'s.
func phpArrayOf(t types.Type) types.Type {
	return types.Map(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Array:
			return types.Array{Key: types.TArrayKey, Value: types.TMixed, NonEmpty: m.NonEmpty}
		case types.Shape:
			if len(m.Entries) == 0 && !m.Unsealed {
				return types.Shape{}
			}
			_, nonEmpty := types.Falsy(m).(types.Never)
			return types.Array{Key: types.TArrayKey, Value: types.TMixed, NonEmpty: nonEmpty}
		}
		return types.Erase(m)
	})
}

// openSSLTagType is what openssl_encrypt() leaves in $tag for a cipher
// named at the call, as PHPStan's extension has it: a non-empty string
// for a GCM or CCM mode, null for any other; nil when the cipher is not
// known there, and the overlay's non-empty-string|null applies. PHPStan
// also gives null for a cipher its OpenSSL does not support, which the
// checker does not tell apart.
func openSSLTagType(sigName, param string, args map[string]types.Type) types.Type {
	if param != "tag" || !strings.EqualFold(sigName, "openssl_encrypt()") {
		return nil
	}
	cipher, ok := args["cipher_algo"]
	if !ok {
		return nil
	}
	var tags []types.Type
	for _, m := range types.Members(cipher) {
		lit, ok := m.(types.StringLit)
		if !ok {
			return nil
		}
		if mode := strings.ToLower(lit.V); strings.HasSuffix(mode, "gcm") || strings.HasSuffix(mode, "ccm") {
			tags = append(tags, types.String{NonEmpty: true})
		} else {
			tags = append(tags, types.TNull)
		}
	}
	return types.MakeUnion(tags...)
}

// splicedArray is what array_splice() leaves in the array t it is given,
// length entries removed at offset and the values of replacement put in
// their place, as PHPStan's spliceArray() types it. A shape with constant
// offset and length keeps its entries, its string keys and its int keys
// renumbered; any other array becomes one of the union of the keys, int
// keys renumbered, and of the values, a list where every key was an int,
// non-empty where the replacement certainly is. offset is int and length
// null where the call leaves them out.
func splicedArray(t, offset, length, replacement types.Type, h types.Hierarchy) types.Type {
	repls := spliceReplacements(replacement)
	return types.Map(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Array:
			return spliceGeneral(m, offset, length, repls, h)
		case types.Shape:
			return spliceShape(m, offset, length, repls, h)
		}
		return m
	})
}

// poppedArray is what array_pop() (shift false) or array_shift() leaves
// in the array t, as PHPStan's popArray() and shiftArray() type it: a
// sealed shape loses its last entry or its first, array_shift()
// renumbering the int keys. Popped, a shape whose last entries are
// optional loses its last entry, and the entries from its last required
// one on become optional: any of them may have been the last. Shifted,
// a shape with optional entries is the array its keys and values make,
// where PHPStan keeps the entries. Any other array keeps its keys and
// values, and may be empty now.
func poppedArray(t types.Type, shift bool, h types.Hierarchy) types.Type {
	return types.Map(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Array:
			m.NonEmpty = false
			return m
		case types.Shape:
			if len(m.Entries) == 0 && !m.Unsealed {
				return m
			}
			optional := false
			for _, e := range m.Entries {
				optional = optional || e.Optional
			}
			if m.Unsealed || shift && optional {
				k, v, _ := arrayKV(m)
				if m.Unsealed {
					k, v = types.TArrayKey, types.TMixed
				}
				return types.Array{Key: k, Value: v, List: types.IsSubtype(m, types.Array{Key: types.TInt, Value: types.TMixed, List: true}, h)}
			}
			var entries []types.ShapeEntry
			if shift {
				next := int64(0)
				for _, e := range m.Entries[1:] {
					if e.IsInt {
						e.IntKey = next
						next++
					}
					entries = append(entries, e)
				}
			} else {
				entries = append(entries, m.Entries[:len(m.Entries)-1]...)
				if m.Entries[len(m.Entries)-1].Optional {
					from := 0
					for i, e := range entries {
						if !e.Optional {
							from = i
						}
					}
					for i := from; i < len(entries); i++ {
						entries[i].Optional = true
					}
				}
			}
			return types.Shape{Entries: entries, List: len(entries) > 0 && isListOrder(entries)}
		}
		return m
	})
}

// spliceReplacements is the replacement array_splice() puts in, as PHP's
// (array) cast makes it: an array stays, null is empty, a scalar is a list
// of itself; anything else any array.
func spliceReplacements(r types.Type) []types.Type {
	var out []types.Type
	for _, m := range types.Members(r) {
		switch m.(type) {
		case types.Array, types.Shape:
			out = append(out, m)
		case types.Null:
			out = append(out, types.Shape{List: true})
		case types.Int, types.Float, types.FloatLit, types.String, types.StringLit, types.Bool, types.BoolLit, types.ClassString:
			out = append(out, types.Shape{List: true, Entries: []types.ShapeEntry{{IsInt: true, IntKey: 0, Type: m}}})
		default:
			out = append(out, types.Array{Key: types.TArrayKey, Value: types.TMixed})
		}
	}
	return out
}

// spliceGeneral is PHPStan's ArrayType::spliceArray() with its list and
// non-empty accessories.
func spliceGeneral(a types.Array, offset, length types.Type, repls []types.Type, h types.Hierarchy) types.Type {
	var rKeys, rValues []types.Type
	every, none := len(repls) > 0, true // the replacement is non-empty on every way, on none
	for _, r := range repls {
		k, v, nonEmpty := arrayKV(r)
		if s, ok := r.(types.Shape); ok && len(s.Entries) > 0 {
			k = types.Int{Lo: 0, Hi: int64(len(s.Entries) - 1)} // the keys of its values, a list
		} else if !isNeverType(k) {
			k = types.Int{Lo: 0, Hi: math.MaxInt64}
		}
		rKeys, rValues = append(rKeys, k), append(rValues, v)
		every = every && nonEmpty
		none = none && isNeverType(v)
	}
	if isConstInt(offset, 0) && isNullType(length) && none {
		return types.Shape{}
	}
	key := types.Map(a.Key, func(k types.Type) types.Type {
		if types.IsSubtype(k, types.TInt, h) {
			return types.Int{Lo: 0, Hi: math.MaxInt64}
		}
		return k
	})
	return types.Array{
		Key:      types.MakeUnion(append(rKeys, key)...),
		Value:    types.MakeUnion(append(rValues, a.Value)...),
		List:     types.IsSubtype(a.Key, types.TInt, h),
		NonEmpty: every || (a.NonEmpty && isConstInt(length, 0)),
	}
}

// spliceShape is PHPStan's ConstantArrayType::spliceArray() for a sealed
// shape without optional entries, a constant offset and length, and
// replacements that are such shapes: the entries from offset on, length
// of them, go, the replacement's values come in there, and the int keys
// are renumbered in order. An offset past the end appends the
// replacement, as PHP does, where PHPStan leaves it out. Any other shape
// is spliced as the array its keys and values make.
func spliceShape(s types.Shape, offset, length types.Type, repls []types.Type, h types.Hierarchy) types.Type {
	n := int64(len(s.Entries))
	if n == 0 && !s.Unsealed {
		return s
	}
	off, offOK := constInt(offset)
	ln, lnOK := constInt(length)
	if isNullType(length) {
		ln, lnOK = n, true
	}
	exact := offOK && lnOK && !s.Unsealed
	for _, e := range s.Entries {
		exact = exact && !e.Optional
	}
	for _, r := range repls {
		rs, ok := r.(types.Shape)
		exact = exact && ok && !rs.Unsealed
		for _, e := range rs.Entries {
			exact = exact && !e.Optional
		}
	}
	if !exact {
		k, v, nonEmpty := arrayKV(s)
		if s.Unsealed {
			k, v = types.TArrayKey, types.TMixed
		}
		return spliceGeneral(types.Array{Key: k, Value: v, List: s.List || isListOrder(s.Entries), NonEmpty: nonEmpty}, offset, length, repls, h)
	}
	// PHP's reading of offset and length, held inside the array before any
	// sum is taken: a negative one counts from the end, an offset past the
	// end puts the replacement there (where PHPStan leaves it out), and a
	// length past the end removes the rest.
	switch {
	case off < -n:
		off = 0
	case off < 0:
		off += n
	case off > n:
		off = n
	}
	switch {
	case ln < -n:
		ln = 0
	case ln < 0:
		ln = max(0, n-off+ln)
	case ln > n-off:
		ln = n - off
	}
	var out []types.Type
	for _, r := range repls {
		var entries []types.ShapeEntry
		next := int64(0)
		add := func(e types.ShapeEntry) {
			if e.IsInt {
				e.IntKey = next
				next++
			}
			entries = append(entries, e)
		}
		for i := int64(0); ; i++ {
			if i == off {
				for _, re := range r.(types.Shape).Entries {
					add(types.ShapeEntry{IsInt: true, Type: re.Type})
				}
			}
			if i >= n {
				break
			}
			if i >= off && i < off+ln {
				continue
			}
			add(s.Entries[i])
		}
		out = append(out, types.Shape{Entries: entries, List: len(entries) > 0 && isListOrder(entries)})
	}
	return types.MakeUnion(out...)
}

// isListOrder reports entries keyed 0, 1, 2… in order, each one present.
func isListOrder(entries []types.ShapeEntry) bool {
	for i, e := range entries {
		if !e.IsInt || e.IntKey != int64(i) || e.Optional {
			return false
		}
	}
	return true
}

// constInt is the value of an int type of one value.
func constInt(t types.Type) (int64, bool) {
	if i, ok := t.(types.Int); ok && i.Lo == i.Hi {
		return i.Lo, true
	}
	return 0, false
}

func isConstInt(t types.Type, n int64) bool {
	v, ok := constInt(t)
	return ok && v == n
}
