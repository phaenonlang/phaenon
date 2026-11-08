package checker

import (
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// This file lowers the type of an `is`, `as` or `as?` to the tests PHP makes
// itself (design/generics.md §11.4, roadmap D23): `\is_int($x)`, `$x
// instanceof Foo`, `$x === null`, `\is_array($x) && \array_is_list($x)`, a
// tuple's count and members. A type PHP has no O(1) test for is P3011, and a
// test that writes its operand twice takes only an operand that reads the
// same twice (P3012). A check the operand's PHP type already settles is left
// out, as PHPStan would report it on the output: on an array, `is list` is
// `\array_is_list($x)` alone. The lowering is recorded on the file
// (program.Test), and the emitter writes it as recorded.

// checkKind says what a node of a test is.
type checkKind uint8

const (
	checkLeaf checkKind = iota
	checkAnd
	checkOr
)

// check is a node of a test under construction.
type check struct {
	kind checkKind
	args []*check
	// A leaf is before, the operand's element at path, after: `\is_int(`
	// $x[0] `)`. noOperand marks a leaf that does not read it (`true`).
	before, after string
	path          []int
	noOperand     bool
	prec          program.Prec
	// accepts is the type of the values a leaf is true for, given the
	// checks before it. An And or Or with accepts is simplified as a leaf
	// (`\class_exists($x) || \interface_exists($x)`). narrow, when set,
	// is what a true leaf leaves of a type, where intersecting it with
	// accepts would lose what the type knows.
	accepts types.Type
	narrow  func(types.Type) types.Type
}

// joinChecks joins checks with && or ||, flattening nested ones of the
// kind.
func joinChecks(kind checkKind, parts ...*check) *check {
	var args []*check
	for _, p := range parts {
		if p.kind == kind && p.accepts == nil {
			args = append(args, p.args...)
			continue
		}
		args = append(args, p)
	}
	if len(args) == 1 {
		return args[0]
	}
	return &check{kind: kind, args: args}
}

// testBuilder builds the test of one `is`, `as` or `as?`.
type testBuilder struct {
	x  *fnCtx
	op string // "is", "as", "as?", for the diagnostics
	// refined is the operand's type, which may fix the arguments of a
	// generic class (`Repository<User>|null` tested with `is
	// Repository<User>`).
	refined types.Type
	// failed is set when a part of the type has no test; the part is
	// reported where it was found.
	failed bool
}

func (b *testBuilder) src(s ast.Span) string { return string(b.x.file.Src[s.Start:s.End]) }

// fn is the leaf `\name(operand)`.
func fn(name string, path []int, accepts types.Type) *check {
	return &check{before: "\\" + name + "(", after: ")", path: path, prec: program.PrecAtom, accepts: accepts}
}

// identical is the leaf `operand === value`.
func identical(value string, path []int, accepts types.Type) *check {
	return &check{after: " === " + value, path: path, prec: program.PrecEquality, accepts: accepts}
}

// instanceOf is the leaf `operand instanceof class`.
func instanceOf(class string, path []int, accepts types.Type) *check {
	return &check{after: " instanceof " + class, path: path, prec: program.PrecInstanceof, accepts: accepts}
}

var (
	anyArray = types.Array{Key: types.TArrayKey, Value: types.TMixed}
	anyList  = types.Array{Key: types.TInt, Value: types.TMixed, List: true}
)

func listTest(path []int) *check {
	return joinChecks(checkAnd, fn("is_array", path, anyArray), fn("array_is_list", path, anyList))
}

// tuple tests a tuple of n members: an array, a list, its count, then each
// member through elem.
func (b *testBuilder) tuple(n int, path []int, elem func(i int, path []int) *check) *check {
	shape := types.Shape{List: true}
	for i := 0; i < n; i++ {
		shape.Entries = append(shape.Entries, types.ShapeEntry{IsInt: true, IntKey: int64(i), Type: types.TMixed})
	}
	parts := []*check{
		fn("is_array", path, anyArray),
		fn("array_is_list", path, anyList),
		{before: "\\count(", after: ") === " + strconv.Itoa(n), path: path, prec: program.PrecEquality, accepts: shape, narrow: counted(n)},
	}
	ok := true
	for i := 0; i < n; i++ {
		p := append(append([]int{}, path...), i)
		c := elem(i, p)
		if c == nil {
			ok = false
			continue
		}
		parts = append(parts, c)
	}
	if !ok {
		return nil
	}
	return joinChecks(checkAnd, parts...)
}

// counted narrows a list to the tuple of n of its elements.
func counted(n int) func(types.Type) types.Type {
	return func(t types.Type) types.Type {
		var out []types.Type
		for _, m := range types.Members(t) {
			elem := types.TMixed
			switch m := m.(type) {
			case types.Shape:
				if len(m.Entries) == n {
					out = append(out, m)
				}
				continue
			case types.Array:
				elem = m.Value
			}
			sh := types.Shape{List: true}
			for i := 0; i < n; i++ {
				sh.Entries = append(sh.Entries, types.ShapeEntry{IsInt: true, IntKey: int64(i), Type: elem})
			}
			out = append(out, sh)
		}
		return types.MakeUnion(out...)
	}
}

// classTest tests for a class name: `class`, or `class<Foo>` when name is
// Foo's PHP spelling.
func classTest(name string, of types.Type, path []int) *check {
	if name == "" {
		names := &check{kind: checkOr, args: []*check{
			fn("class_exists", path, types.ClassString{}),
			fn("interface_exists", path, types.ClassString{}),
		}, accepts: types.ClassString{}}
		return joinChecks(checkAnd, fn("is_string", path, types.TString), names)
	}
	return joinChecks(checkAnd, fn("is_string", path, types.TString),
		&check{before: "\\is_a(", after: ", " + name + "::class, true)", path: path, prec: program.PrecAtom, accepts: types.ClassString{Of: of}})
}

// cannot reports a part of the type that has no test (P3011); written is
// how the source spells it.
func (b *testBuilder) cannot(span ast.Span, written, why string) *check {
	b.x.report(span, P3011, "%s cannot be tested at runtime%s", written, why)
	b.failed = true
	return nil
}

const (
	whyList     = " in O(1): test `is list` and build the typed value, or check the elements yourself"
	whyArray    = " in O(1): test `is array` and build the typed value, or check the entries yourself"
	whyIterable = " in O(1): test `is iterable` and check the values where they are read"
	whyClosure  = ": test `is Closure`; PHP checks a closure's parameters and result when it is called"
	whyBytes    = ": bytes is a PHP string; test `is string` and convert it with (bytes)"
	whyClass    = " in O(1): test `is class` and check the class with is_a()"
	whyNoValue  = ": no value has this type"
	whyOther    = " in O(1): test a type PHP checks, and check the rest yourself"
)

// build tests a type as written.
func (b *testBuilder) build(t ast.TypeExpr, path []int) *check {
	switch t := t.(type) {
	case *ast.NamedType:
		return b.named(t, path)
	case *ast.NullableType:
		inner := b.build(t.Type, path)
		if inner == nil {
			return nil
		}
		return joinChecks(checkOr, identical("null", path, types.TNull), inner)
	case *ast.UnionType:
		return b.all(checkOr, t.Types, path)
	case *ast.IntersectionType:
		return b.all(checkAnd, t.Types, path)
	case *ast.ClosureType:
		return b.cannot(t.S, quoted(b.src(t.S)), whyClosure)
	case *ast.TupleType:
		return b.tuple(len(t.Elems), path, func(i int, p []int) *check { return b.build(t.Elems[i], p) })
	}
	return nil
}

// all joins the tests of the members of a union or an intersection; every
// member without one is reported.
func (b *testBuilder) all(kind checkKind, members []ast.TypeExpr, path []int) *check {
	var parts []*check
	ok := true
	for _, m := range members {
		c := b.build(m, path)
		if c == nil {
			ok = false
			continue
		}
		parts = append(parts, c)
	}
	if !ok {
		return nil
	}
	return joinChecks(kind, parts...)
}

// anyEntries reports whether the arguments of array, map or iterable leave
// the key and the value open, so that the type is the bare one: a key of
// mixed or int|string (every key a PHP array has), a value of mixed.
func anyEntries(args []types.Type) bool {
	switch len(args) {
	case 0:
		return true
	case 1:
		return types.IsMixed(args[0])
	}
	return (types.IsMixed(args[0]) || types.IsSubtype(types.TArrayKey, args[0], nil)) && types.IsMixed(args[1])
}

func (b *testBuilder) named(t *ast.NamedType, path []int) *check {
	x := b.x
	given := len(t.TypeArgs)
	if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified {
		name := t.Name.Parts[0]
		if tp := x.res.TypeParam(name); tp != nil {
			x.paramValue(tp, t.S, "`"+b.op+"`")
			b.failed = true
			return nil
		}
		lower := strings.ToLower(name)
		switch lower {
		case "self", "static", "parent":
		default:
			if ar, ok := keywordArity[lower]; ok && (given > ar[1] || given < ar[0] && lower != "list") {
				b.failed = true // P3001, reported with the type
				return nil
			}
		}
		switch lower {
		case "int", "float", "string", "bool", "object", "callable", "scalar", "numeric", "resource":
			return fn("is_"+lower, path, x.res.Native(t))
		case "number":
			return joinChecks(checkOr, fn("is_int", path, types.TInt), fn("is_float", path, types.TFloat))
		case "null", "true", "false":
			return identical(lower, path, x.res.Native(t))
		case "mixed":
			return &check{before: "true", noOperand: true, prec: program.PrecAtom, accepts: types.TMixed}
		case "void", "never":
			return b.cannot(t.S, quoted(b.src(t.S)), whyNoValue)
		case "bytes":
			return b.cannot(t.S, quoted(b.src(t.S)), whyBytes)
		case "closure":
			return instanceOf("\\Closure", path, types.TClosure)
		case "class":
			return b.classString(t, path)
		case "list":
			if given == 1 && !types.IsMixed(x.res.Native(t.TypeArgs[0])) {
				return b.cannot(t.S, quoted(b.src(t.S)), whyList)
			}
			return listTest(path)
		case "array", "map":
			if args := x.res.NativeArgs(t.TypeArgs); lower == "map" && !mapKeyOK(args[0], x) {
				b.failed = true // P2518, reported with the type
				return nil
			} else if !anyEntries(args) {
				return b.cannot(t.S, quoted(b.src(t.S)), whyArray)
			}
			return fn("is_array", path, anyArray)
		case "iterable":
			if !anyEntries(x.res.NativeArgs(t.TypeArgs)) {
				return b.cannot(t.S, quoted(b.src(t.S)), whyIterable)
			}
			return fn("is_iterable", path, types.TIterable)
		}
		if x.res.FileAlias(name) != nil {
			return b.typed(x.res.Native(t), path, t.S, b.src(t.S))
		}
	}
	// A class, an interface, an enum, self, static or parent, as written.
	resolved := x.res.Native(t)
	if c, ok := resolved.(types.Callable); ok && c.Closure {
		return instanceOf(b.src(t.Name.S), path, types.TClosure)
	}
	var accepts types.Type
	switch r := resolved.(type) {
	case types.Class:
		if given > 0 && !b.fixes(r, path) {
			return b.cannot(t.S, quoted(b.src(t.S)), b.whyArgs(b.src(t.Name.S), path))
		}
		accepts = types.Class{Name: r.Name}
	case types.Static:
		if given > 0 && r.Of != nil && !b.fixes(*r.Of, path) {
			return b.cannot(t.S, quoted(b.src(t.S)), b.whyArgs(b.src(t.Name.S), path))
		}
		accepts = types.Static{Of: &types.Class{Name: r.Of.Name}}
	default:
		accepts = resolved // self or static outside a class: object, reported
	}
	return instanceOf(b.src(t.Name.S), path, accepts)
}

// mapKeyOK reports a map key the checker accepts (P2518 otherwise).
func mapKeyOK(k types.Type, x *fnCtx) bool {
	if k == nil || types.IsMixed(k) {
		return true
	}
	if p, ok := k.(types.Param); ok {
		if p.Bound == nil {
			return true
		}
		k = p.Bound
	}
	_, unknown := k.(types.Unknown)
	return unknown || types.IsSubtype(k, types.TArrayKey, x.p())
}

func (b *testBuilder) whyArgs(class string, path []int) string {
	return ": instanceof does not see type arguments, and " + b.x.typeName(b.refinedAt(path)) +
		" does not fix them; test `is " + class + "` and read the arguments from the value, or hold a class<T>"
}

// classString tests `class` and `class<Foo>`.
func (b *testBuilder) classString(t *ast.NamedType, path []int) *check {
	x := b.x
	if len(t.TypeArgs) == 0 {
		return classTest("", nil, path)
	}
	arg := t.TypeArgs[0]
	if n, ok := arg.(*ast.NamedType); ok && len(n.Name.Parts) == 1 && n.Name.Kind == ast.NameUnqualified {
		if tp := x.res.TypeParam(n.Name.Parts[0]); tp != nil {
			x.paramValue(tp, n.S, "`"+b.op+"`")
			b.failed = true
			return nil
		}
	}
	of := x.res.Native(arg)
	if _, ok := of.(types.Object); ok {
		return classTest("", nil, path)
	}
	n, ok := arg.(*ast.NamedType)
	if !ok || len(n.TypeArgs) > 0 {
		return b.cannot(t.S, quoted(b.src(t.S)), whyClass)
	}
	name := b.src(n.Name.S)
	if len(n.Name.Parts) == 1 && n.Name.Kind == ast.NameUnqualified && x.res.FileAlias(n.Name.Parts[0]) != nil {
		name = "" // an alias of a class: named as PHP knows it below
	}
	switch o := of.(type) {
	case types.Class:
		if name == "" {
			name = "\\" + o.Name
		}
		return classTest(name, types.Class{Name: o.Name}, path)
	case types.Callable:
		if o.Closure && !o.HasSig {
			return classTest("\\Closure", types.TClosure, path)
		}
	}
	return b.cannot(t.S, quoted(b.src(t.S)), whyClass)
}

// typed tests a resolved type: what an alias expands to. A class is named
// fully qualified. A part without a test is reported at the alias, span,
// written as the source has it.
func (b *testBuilder) typed(t types.Type, path []int, span ast.Span, written string) *check {
	x := b.x
	cannot := func(why string) *check {
		w := quoted(written)
		if p := types.PrintPhaenon(t); p != written {
			w += " (" + quoted(p) + ")"
		}
		return b.cannot(span, w, why)
	}
	switch v := t.(type) {
	case types.Mixed:
		return &check{before: "true", noOperand: true, prec: program.PrecAtom, accepts: types.TMixed}
	case types.Null:
		return identical("null", path, v)
	case types.Bool:
		return fn("is_bool", path, v)
	case types.BoolLit:
		return identical(strconv.FormatBool(v.V), path, v)
	case types.Int:
		if lit, ok := types.IsIntLit(v); ok {
			return identical(strconv.FormatInt(lit, 10), path, v)
		}
		if types.Equal(v, types.TInt) {
			return fn("is_int", path, v)
		}
	case types.Float:
		return fn("is_float", path, v)
	case types.String:
		if v == (types.String{}) {
			return fn("is_string", path, v)
		}
	case types.StringLit:
		return identical(program.PHPString(v.V), path, v)
	case types.Bytes:
		return cannot(whyBytes)
	case types.Object:
		return fn("is_object", path, v)
	case types.Resource:
		return fn("is_resource", path, v)
	case types.Iterable:
		if anyEntries([]types.Type{v.Key, v.Value}) {
			return fn("is_iterable", path, types.TIterable)
		}
		return cannot(whyIterable)
	case types.Callable:
		switch {
		case v.Closure && !v.HasSig:
			return instanceOf("\\Closure", path, types.TClosure)
		case !v.Closure && !v.HasSig && !v.Pure:
			return fn("is_callable", path, v)
		case v.Closure:
			return cannot(whyClosure)
		}
	case types.Array:
		if v.List {
			if types.IsMixed(v.Value) && !v.NonEmpty {
				return listTest(path)
			}
			return cannot(whyList)
		}
		if anyEntries([]types.Type{v.Key, v.Value}) && !v.NonEmpty {
			return fn("is_array", path, anyArray)
		}
		return cannot(whyArray)
	case types.Shape:
		if tuple(v) {
			return b.tuple(len(v.Entries), path, func(i int, p []int) *check {
				return b.typed(v.Entries[i].Type, p, span, written)
			})
		}
		return cannot(whyArray)
	case types.Class:
		if len(v.Args) > 0 && !b.fixes(v, path) {
			return cannot(b.whyArgs("\\"+v.Name, path))
		}
		return instanceOf("\\"+v.Name, path, types.Class{Name: v.Name})
	case types.Static:
		return instanceOf("static", path, v)
	case types.EnumCase:
		return identical("\\"+v.Class+"::"+v.Case, path, v)
	case types.ClassString:
		switch of := v.Of.(type) {
		case nil, types.Object:
			return classTest("", nil, path)
		case types.Class:
			if len(of.Args) == 0 {
				return classTest("\\"+of.Name, of, path)
			}
		}
		return cannot(whyClass)
	case types.Union:
		var parts []*check
		ok := true
		// `?T` reads as `$x === null || …`.
		members := make([]types.Type, 0, len(v.Types))
		for _, m := range v.Types {
			if _, isNull := m.(types.Null); isNull {
				members = append([]types.Type{m}, members...)
			} else {
				members = append(members, m)
			}
		}
		for _, m := range members {
			c := b.typed(m, path, span, written)
			if c == nil {
				ok = false
				continue
			}
			parts = append(parts, c)
		}
		if !ok {
			return nil
		}
		return joinChecks(checkOr, parts...)
	case types.Intersection:
		var parts []*check
		for _, m := range v.Types {
			c := b.typed(m, path, span, written)
			if c == nil {
				return nil
			}
			parts = append(parts, c)
		}
		return joinChecks(checkAnd, parts...)
	case types.Param:
		if tp := x.res.TypeParam(types.ParamName(v.Name)); tp != nil {
			x.paramValue(tp, span, "`"+b.op+"`")
			b.failed = true
			return nil
		}
	case types.Unknown:
		b.failed = true // an unknown class, reported where it is named
		return nil
	}
	return cannot(whyOther)
}

// tuple reports a shape that is a tuple: a sealed list of required
// entries 0..n-1.
func tuple(s types.Shape) bool {
	if !s.List || s.Unsealed || s.Callable {
		return false
	}
	for i, e := range s.Entries {
		if !e.IsInt || e.IntKey != int64(i) || e.Optional {
			return false
		}
	}
	return true
}

func quoted(s string) string { return "`" + s + "`" }

// refinedAt is the operand's type at an element path, as the checker
// infers it.
func (b *testBuilder) refinedAt(path []int) types.Type {
	t := b.refined
	for _, i := range path {
		t = elementAt(t, i)
	}
	return t
}

// fixes reports whether the operand's type fixes the arguments of a
// generic class it is tested against: every member that may be an
// instance of the class is one with these arguments, or with arguments
// that make it a subtype (`Repository<User>|null` against
// `Repository<User>`). Then instanceof tests the whole type, as erasure
// loses nothing (Kotlin's rule).
func (b *testBuilder) fixes(c types.Class, path []int) bool {
	bare := types.Class{Name: c.Name}
	for _, m := range types.Members(b.refinedAt(path)) {
		if types.Overlaps(m, bare, b.x.p()) && !b.memberFixes(m, c) {
			return false
		}
	}
	return true
}

func (b *testBuilder) memberFixes(m types.Type, c types.Class) bool {
	switch m := m.(type) {
	case types.Param:
		if m.Bound == nil {
			return false
		}
		for _, bm := range types.Members(m.Bound) {
			if !b.memberFixes(bm, c) {
				return false
			}
		}
		return true
	case types.Static:
		return m.Of != nil && b.memberFixes(*m.Of, c)
	case types.This:
		return m.Of != nil && b.memberFixes(*m.Of, c)
	case types.Intersection:
		for _, part := range m.Types {
			if b.memberFixes(part, c) {
				return true
			}
		}
	case types.Class:
		cl := b.x.p().Class(m.Name)
		if cl == nil {
			return false
		}
		args, ok := cl.SuperArgs(c.Name, m.Args)
		return ok && len(args) > 0 && types.IsSubtype(types.Class{Name: c.Name, Args: args}, c, b.x.p())
	}
	return false
}

// elementAt is the type of element i of an array of type t.
func elementAt(t types.Type, i int) types.Type {
	var out []types.Type
	for _, m := range types.Members(t) {
		switch m := m.(type) {
		case types.Shape:
			found := false
			for _, e := range m.Entries {
				if e.IsInt && e.IntKey == int64(i) {
					out = append(out, e.Type)
					found = true
				}
			}
			if !found {
				out = append(out, types.TMixed)
			}
		case types.Array:
			out = append(out, m.Value)
		default:
			out = append(out, types.TMixed)
		}
	}
	if len(out) == 0 {
		return types.TMixed
	}
	return types.MakeUnion(out...)
}

// ------------------------------------------------------------------ simplification

// testState is what a point of a test knows about the operand and the
// elements it has narrowed, by path.
type testState map[string]stateEntry

type stateEntry struct {
	path []int
	t    types.Type
}

func elementKey(path []int) string {
	var sb strings.Builder
	for _, i := range path {
		sb.WriteByte('.')
		sb.WriteString(strconv.Itoa(i))
	}
	return sb.String()
}

func (s testState) at(path []int) types.Type {
	if e, ok := s[elementKey(path)]; ok {
		return e.t
	}
	if len(path) == 0 {
		return types.TMixed
	}
	return elementAt(s.at(path[:len(path)-1]), path[len(path)-1])
}

func (s testState) with(path []int, t types.Type) testState {
	out := make(testState, len(s)+1)
	for k, v := range s {
		out[k] = v
	}
	out[elementKey(path)] = stateEntry{path: path, t: t}
	return out
}

func joinStates(states ...testState) testState {
	if len(states) == 1 {
		return states[0]
	}
	paths := map[string][]int{}
	for _, s := range states {
		for k, e := range s {
			paths[k] = e.path
		}
	}
	out := testState{}
	for k, p := range paths {
		var ts []types.Type
		for _, s := range states {
			ts = append(ts, s.at(p))
		}
		out[k] = stateEntry{path: p, t: types.MakeUnion(ts...)}
	}
	return out
}

// simplify drops the checks the operand's type settles: a check always
// true in a conjunction, one always false in a disjunction. It never
// drops the last one; a test settled as a whole is the caller's finding
// (P2306). then and els are what holds after the check is true or false.
func (x *fnCtx) simplify(c *check, s testState) (out *check, then, els testState, always, never bool) {
	h := x.p()
	if c.kind == checkLeaf || c.accepts != nil {
		t := s.at(c.path)
		if c.noOperand {
			return c, s, s, true, false
		}
		always = types.IsSubtype(t, c.accepts, h)
		never = !types.Overlaps(t, c.accepts, h)
		then := types.Intersect(t, c.accepts, h)
		if c.narrow != nil {
			then = c.narrow(t)
		}
		return c, s.with(c.path, then), s.with(c.path, types.Subtract(t, c.accepts, h)), always, never
	}
	var kept []*check
	var last *check
	var branches []testState
	cur := s
	if c.kind == checkAnd {
		always = true
		for _, a := range c.args {
			o, th, el, alw, nev := x.simplify(a, cur)
			branches = append(branches, el)
			last = o
			if !alw {
				kept = append(kept, o)
				always = false
			}
			never = never || nev
			cur = th
		}
		if len(kept) == 0 {
			kept = []*check{last}
		}
		return joinChecks(checkAnd, kept...), cur, joinStates(branches...), always, never
	}
	never = true
	for _, a := range c.args {
		o, th, el, alw, nev := x.simplify(a, cur)
		branches = append(branches, th)
		last = o
		if !nev {
			kept = append(kept, o)
			never = false
		}
		always = always || alw
		cur = el
	}
	if len(kept) == 0 {
		kept = []*check{last}
	}
	return joinChecks(checkOr, kept...), joinStates(branches...), cur, always, never
}

// render writes a check as PHP, with program.TestOperand for the operand.
func render(c *check) (string, program.Prec) {
	if c.kind == checkLeaf {
		if c.noOperand {
			return c.before, c.prec
		}
		var sb strings.Builder
		sb.WriteString(c.before)
		sb.WriteString(program.TestOperand)
		for _, i := range c.path {
			sb.WriteString("[" + strconv.Itoa(i) + "]")
		}
		sb.WriteString(c.after)
		return sb.String(), c.prec
	}
	prec, sep := program.PrecAnd, " && "
	if c.kind == checkOr {
		prec, sep = program.PrecOr, " || "
	}
	parts := make([]string, len(c.args))
	for i, a := range c.args {
		s, p := render(a)
		// `$x === null || (\is_array($x) && \array_is_list($x))`: the
		// parentheses PHP does not need are the ones a reader does.
		if p < prec || p == program.PrecAnd && prec == program.PrecOr {
			s = "(" + s + ")"
		}
		parts[i] = s
	}
	return strings.Join(parts, sep), prec
}

// ------------------------------------------------------------------ the test

// testSeen accumulates, per test, the operand types of every visit: a
// loop's body is checked more than once, and the lowering must hold for
// each pass.
type testSeen struct{ t, nt types.Type }

// lowerTest lowers the test of node (an `is`, `as` or `as?`) of operand
// e against typ, records it for the emitter and reports P3011 and P3012.
// t is the operand's type, nt what PHP guarantees of it. ok is false when
// the type has no test.
func (x *fnCtx) lowerTest(node, e ast.Expr, typ ast.TypeExpr, op string, t, nt, target types.Type) (ok bool) {
	if x.c.testSeen == nil {
		x.c.testSeen = map[ast.Expr]testSeen{}
	}
	if prev, ok := x.c.testSeen[node]; ok {
		t, nt = types.MakeUnion(prev.t, t), types.MakeUnion(prev.nt, nt)
	}
	x.c.testSeen[node] = testSeen{t, nt}
	rec := &program.Test{Expected: types.PrintPhaenon(target)}
	if x.c.file.Tests == nil {
		x.c.file.Tests = map[ast.Expr]*program.Test{}
	}
	x.c.file.Tests[node] = rec
	b := &testBuilder{x: x, op: op, refined: t}
	c := b.build(typ, nil)
	if c == nil || b.failed {
		return false
	}
	c, _, _, _, _ = x.simplify(c, testState{"": {t: nt}})
	rec.Cond, rec.Prec = render(c)
	// Reported whether or not the test is settled: `phn build
	// --emit-on-error` writes the test all the same.
	twice := strings.Count(rec.Cond, program.TestOperand) > 1 || op != "is"
	if twice && !program.ReadsTwice(e) {
		x.report(e.Span(), P3012, "the operand of `%s` is evaluated twice; assign it to a variable first", op)
	}
	return true
}
