// Package types is Phaenon's type representation.
//
// A type is a set of runtime values, after PHPStan: mixed is every value,
// never is none, a union is a set union, subtyping is inclusion. Values of
// this package are immutable once built; the constructors in this file
// normalise (flatten unions, merge literals, drop duplicates) so that two
// equivalent types usually have the same representation and printing is
// canonical. Class hierarchies live elsewhere; operations that need them
// take a Hierarchy.
package types

import (
	"math"
	"sort"
	"strings"
)

// Type is implemented by every type.
type Type interface {
	typeNode()
}

// Hierarchy answers the class-level questions subtyping needs. The program
// model implements it; tests use a map.
type Hierarchy interface {
	// IsSubclassOf reports whether class sub is super or inherits from /
	// implements it. Names are fully qualified without a leading backslash
	// and compared case-insensitively.
	IsSubclassOf(sub, super string) bool
	// IsFinal reports whether the class cannot be extended (final classes,
	// enums, anonymous classes).
	IsFinal(name string) bool
	// Variances returns the variance of each type parameter of a class,
	// nil when the class is unknown or not generic.
	Variances(name string) []Variance
	// Exists reports whether the class is known at all.
	Exists(name string) bool
}

// Variance of a class type parameter.
type Variance uint8

const (
	Invariant Variance = iota
	Covariant
	Contravariant
)

// ------------------------------------------------------------------ the kinds

type (
	// Mixed is the top type.
	Mixed struct{}
	// Never is the bottom type.
	Never struct{}
	// Void is the type of a function that returns nothing; it is not a value type.
	Void struct{}
	// Null is the null type.
	Null struct{}
	// Bool is true|false; BoolLit is one of them.
	Bool    struct{}
	BoolLit struct{ V bool }
	// Int is an integer range [Lo, Hi]; the full range is plain int, Lo == Hi
	// is a literal.
	Int struct{ Lo, Hi int64 }
	// Float is any float; FloatLit a literal.
	Float    struct{}
	FloatLit struct{ V float64 }
	// String is a string with optional refinements; StringLit a literal.
	String struct {
		NonEmpty  bool
		Numeric   bool
		Literal   bool // literal-string
		Callable  bool // callable-string
		Lowercase bool
	}
	StringLit struct{ V string }
	// ClassString is class-string<Of>; Of == nil is any class-string.
	ClassString struct{ Of Type }
	// Array is array<Key, Value>; List forces integer keys 0..n-1.
	Array struct {
		Key, Value Type
		List       bool
		NonEmpty   bool
	}
	// Shape is array{k: T, k2?: T}; Unsealed shapes accept further entries of
	// the Rest key/value types (nil means array-key/mixed).
	Shape struct {
		Entries  []ShapeEntry
		Unsealed bool
		List     bool // list{int, string}
	}
	ShapeEntry struct {
		Key      string // "" with IntKey for integer keys
		IntKey   int64
		IsInt    bool
		Type     Type
		Optional bool
	}
	// Object is any object.
	Object struct{}
	// Class is an instance of a named class, interface or enum, with type
	// arguments. Name is fully qualified without a leading backslash in the
	// declared case.
	Class struct {
		Name string
		Args []Type
	}
	// Static is `static` resolved in class Of; This is `$this` there.
	Static struct{ Of *Class }
	This   struct{ Of *Class }
	// EnumCase is the singleton type of one enum case.
	EnumCase struct{ Class, Case string }
	// Callable is callable or Closure, with an optional signature. Params ==
	// nil && Return == nil is the bare `callable`.
	Callable struct {
		Params  []CallableParam
		Return  Type
		Closure bool // \Closure rather than callable
		Pure    bool
		HasSig  bool
	}
	CallableParam struct {
		Name     string
		Type     Type
		Optional bool
		Variadic bool
		ByRef    bool
	}
	// Iterable is iterable<Key, Value>.
	Iterable struct{ Key, Value Type }
	// Resource is the resource type.
	Resource struct{}
	// Union and Intersection are normalised: flat, no duplicates, at least two members.
	Union        struct{ Types []Type }
	Intersection struct{ Types []Type }
	// Param is a type parameter (template) with its bound. Owner is the
	// declaring class or function, for printing and identity.
	Param struct {
		Name  string
		Bound Type
		Owner string
	}
	// Unknown is a type the checker could not resolve (an unknown class in a
	// docblock); it behaves as mixed but prints as itself so diagnostics
	// stay informative.
	Unknown struct{ Name string }
)

func (Mixed) typeNode()        {}
func (Never) typeNode()        {}
func (Void) typeNode()         {}
func (Null) typeNode()         {}
func (Bool) typeNode()         {}
func (BoolLit) typeNode()      {}
func (Int) typeNode()          {}
func (Float) typeNode()        {}
func (FloatLit) typeNode()     {}
func (String) typeNode()       {}
func (StringLit) typeNode()    {}
func (ClassString) typeNode()  {}
func (Array) typeNode()        {}
func (Shape) typeNode()        {}
func (Object) typeNode()       {}
func (Class) typeNode()        {}
func (Static) typeNode()       {}
func (This) typeNode()         {}
func (EnumCase) typeNode()     {}
func (Callable) typeNode()     {}
func (Iterable) typeNode()     {}
func (Resource) typeNode()     {}
func (Union) typeNode()        {}
func (Intersection) typeNode() {}
func (Param) typeNode()        {}
func (Unknown) typeNode()      {}

// ------------------------------------------------------------------ singletons and helpers

var (
	TMixed    Type = Mixed{}
	TNever    Type = Never{}
	TVoid     Type = Void{}
	TNull     Type = Null{}
	TBool     Type = Bool{}
	TTrue     Type = BoolLit{true}
	TFalse    Type = BoolLit{false}
	TInt      Type = Int{math.MinInt64, math.MaxInt64}
	TFloat    Type = Float{}
	TString   Type = String{}
	TObject   Type = Object{}
	TResource Type = Resource{}
	TCallable Type = Callable{}
	TClosure  Type = Callable{Closure: true}
	// TArray is array<array-key, mixed>.
	TArray Type = Array{Key: TArrayKey, Value: TMixed}
	// TArrayKey is int|string.
	TArrayKey Type = Union{[]Type{TInt, TString}}
	// TScalar is bool|int|float|string.
	TScalar Type = Union{[]Type{TBool, TInt, TFloat, TString}}
	// TNumeric is int|float|numeric-string.
	TNumeric Type = Union{[]Type{TInt, TFloat, String{Numeric: true}}}
	// TIterable is iterable<mixed, mixed>.
	TIterable Type = Iterable{TMixed, TMixed}
)

// IntLit is the literal integer type n.
func IntLit(n int64) Type { return Int{n, n} }

// IsIntLit reports whether t is a single integer.
func IsIntLit(t Type) (int64, bool) {
	if i, ok := t.(Int); ok && i.Lo == i.Hi {
		return i.Lo, true
	}
	return 0, false
}

// Nullable is T|null.
func Nullable(t Type) Type { return MakeUnion(t, TNull) }

// NewClass is a class instance type without arguments.
func NewClass(name string) Type { return Class{Name: name} }

// ------------------------------------------------------------------ equality

// Equal reports structural equality after normalisation.
func Equal(a, b Type) bool {
	switch a := a.(type) {
	case Mixed, Never, Void, Null, Bool, Float, Object, Resource:
		return a == b
	case BoolLit:
		b, ok := b.(BoolLit)
		return ok && a == b
	case Int:
		b, ok := b.(Int)
		return ok && a == b
	case FloatLit:
		b, ok := b.(FloatLit)
		return ok && a == b
	case String:
		b, ok := b.(String)
		return ok && a == b
	case StringLit:
		b, ok := b.(StringLit)
		return ok && a == b
	case ClassString:
		b, ok := b.(ClassString)
		return ok && equalOrNil(a.Of, b.Of)
	case Array:
		b, ok := b.(Array)
		return ok && a.List == b.List && a.NonEmpty == b.NonEmpty && Equal(a.Key, b.Key) && Equal(a.Value, b.Value)
	case Shape:
		b, ok := b.(Shape)
		if !ok || a.Unsealed != b.Unsealed || a.List != b.List || len(a.Entries) != len(b.Entries) {
			return false
		}
		for i := range a.Entries {
			x, y := a.Entries[i], b.Entries[i]
			if x.Key != y.Key || x.IntKey != y.IntKey || x.IsInt != y.IsInt || x.Optional != y.Optional || !Equal(x.Type, y.Type) {
				return false
			}
		}
		return true
	case Class:
		b, ok := b.(Class)
		return ok && strings.EqualFold(a.Name, b.Name) && equalList(a.Args, b.Args)
	case Static:
		b, ok := b.(Static)
		return ok && equalOrNil(classOrNil(a.Of), classOrNil(b.Of))
	case This:
		b, ok := b.(This)
		return ok && equalOrNil(classOrNil(a.Of), classOrNil(b.Of))
	case EnumCase:
		b, ok := b.(EnumCase)
		return ok && strings.EqualFold(a.Class, b.Class) && a.Case == b.Case
	case Callable:
		b, ok := b.(Callable)
		if !ok || a.Closure != b.Closure || a.Pure != b.Pure || a.HasSig != b.HasSig || len(a.Params) != len(b.Params) {
			return false
		}
		for i := range a.Params {
			x, y := a.Params[i], b.Params[i]
			if x.Optional != y.Optional || x.Variadic != y.Variadic || x.ByRef != y.ByRef || !equalOrNil(x.Type, y.Type) {
				return false
			}
		}
		return equalOrNil(a.Return, b.Return)
	case Iterable:
		b, ok := b.(Iterable)
		return ok && Equal(a.Key, b.Key) && Equal(a.Value, b.Value)
	case Union:
		b, ok := b.(Union)
		return ok && equalSet(a.Types, b.Types)
	case Intersection:
		b, ok := b.(Intersection)
		return ok && equalSet(a.Types, b.Types)
	case Param:
		b, ok := b.(Param)
		return ok && a.Name == b.Name && strings.EqualFold(a.Owner, b.Owner)
	case Unknown:
		b, ok := b.(Unknown)
		return ok && strings.EqualFold(a.Name, b.Name)
	}
	return false
}

func classOrNil(c *Class) Type {
	if c == nil {
		return nil
	}
	return *c
}

func equalOrNil(a, b Type) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return Equal(a, b)
}

func equalList(a, b []Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

func equalSet(a, b []Type) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		found := false
		for _, y := range b {
			if Equal(x, y) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------------ union

// MakeUnion builds the normalised union of the given types.
func MakeUnion(ts ...Type) Type {
	var members []Type
	var add func(t Type)
	add = func(t Type) {
		switch t := t.(type) {
		case nil:
		case Union:
			for _, m := range t.Types {
				add(m)
			}
		case Never:
		default:
			members = append(members, t)
		}
	}
	for _, t := range ts {
		add(t)
	}
	members = simplifyUnion(members)
	switch len(members) {
	case 0:
		return TNever
	case 1:
		return members[0]
	}
	sort.SliceStable(members, func(i, j int) bool { return unionOrder(members[i]) < unionOrder(members[j]) })
	return Union{members}
}

// unionOrder gives a stable, readable member order: null last, scalars by
// kind, then arrays, objects, callables.
func unionOrder(t Type) int {
	switch t := t.(type) {
	case Mixed:
		return 0
	case BoolLit, Bool:
		return 10
	case Int:
		return 20
	case Float, FloatLit:
		return 30
	case StringLit, String, ClassString:
		return 40
	case Array, Shape:
		return 50
	case Class, Static, This, EnumCase, Object, Param, Unknown:
		return 60
	case Callable:
		return 70
	case Iterable:
		return 80
	case Intersection:
		return 90
	case Resource:
		return 95
	case Null:
		return 100
	case Void:
		return 110
	case Never:
		return 120
	default:
		_ = t
		return 200
	}
}

// simplifyUnion removes members absorbed by others and merges literals.
func simplifyUnion(ms []Type) []Type {
	// mixed absorbs everything but keeps nothing else.
	for _, m := range ms {
		if _, ok := m.(Mixed); ok {
			return []Type{TMixed}
		}
	}
	var out []Type
	hasTrue, hasFalse := false, false
	for _, m := range ms {
		switch m := m.(type) {
		case BoolLit:
			if m.V {
				hasTrue = true
			} else {
				hasFalse = true
			}
			continue
		case Bool:
			hasTrue, hasFalse = true, true
			continue
		}
		out = append(out, m)
	}
	switch {
	case hasTrue && hasFalse:
		out = append(out, TBool)
	case hasTrue:
		out = append(out, TTrue)
	case hasFalse:
		out = append(out, TFalse)
	}
	// Merge integer ranges that touch or overlap.
	var ints []Int
	var rest []Type
	for _, m := range out {
		if i, ok := m.(Int); ok {
			ints = append(ints, i)
		} else {
			rest = append(rest, m)
		}
	}
	if len(ints) > 1 {
		sort.Slice(ints, func(i, j int) bool { return ints[i].Lo < ints[j].Lo })
		merged := []Int{ints[0]}
		for _, i := range ints[1:] {
			last := &merged[len(merged)-1]
			if i.Lo <= last.Hi || (last.Hi != math.MaxInt64 && i.Lo == last.Hi+1) {
				if i.Hi > last.Hi {
					last.Hi = i.Hi
				}
			} else {
				merged = append(merged, i)
			}
		}
		ints = merged
	}
	out = rest
	for _, i := range ints {
		out = append(out, i)
	}
	// Too many literals widen to their primitive.
	if countLiterals(out) > 8 {
		out = widenLiterals(out)
	}
	// Drop members that are plain subtypes of another member (literal-level
	// reasoning only; class hierarchies are handled by callers that have one).
	var res []Type
	for i, m := range out {
		absorbed := false
		for j, o := range out {
			if i == j {
				continue
			}
			if Equal(m, o) {
				absorbed = j < i // keep the first duplicate
			} else if absorbs(o, m) {
				absorbed = true
			}
			if absorbed {
				break
			}
		}
		if !absorbed {
			res = append(res, m)
		}
	}
	return res
}

func countLiterals(ms []Type) int {
	n := 0
	for _, m := range ms {
		switch m := m.(type) {
		case StringLit, FloatLit:
			n++
		case Int:
			if m.Lo == m.Hi {
				n++
			}
		}
	}
	return n
}

func widenLiterals(ms []Type) []Type {
	var out []Type
	seenInt, seenStr, seenFloat := false, false, false
	for _, m := range ms {
		switch x := m.(type) {
		case StringLit:
			if !seenStr {
				seenStr = true
				out = append(out, TString)
			}
			continue
		case FloatLit:
			if !seenFloat {
				seenFloat = true
				out = append(out, TFloat)
			}
			continue
		case Int:
			if x.Lo == x.Hi {
				if !seenInt {
					seenInt = true
					out = append(out, TInt)
				}
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// absorbs reports whether a union member o makes m redundant, using only
// structural facts (no hierarchy).
func absorbs(o, m Type) bool {
	switch o := o.(type) {
	case Int:
		if m, ok := m.(Int); ok {
			return o.Lo <= m.Lo && m.Hi <= o.Hi
		}
	case Float:
		_, ok := m.(FloatLit)
		return ok
	case String:
		switch m := m.(type) {
		case StringLit:
			return stringLitFits(m.V, o)
		case String:
			return stringRefines(m, o)
		case ClassString:
			return !o.Numeric && !o.Lowercase
		}
	case Object:
		switch m.(type) {
		case Class, Static, This, EnumCase:
			return true
		}
	case Class:
		if m, ok := m.(EnumCase); ok && strings.EqualFold(m.Class, o.Name) {
			return true
		}
		if m, ok := m.(Static); ok && m.Of != nil && strings.EqualFold(m.Of.Name, o.Name) {
			return true
		}
		if m, ok := m.(This); ok && m.Of != nil && strings.EqualFold(m.Of.Name, o.Name) {
			return true
		}
	case Static:
		if m, ok := m.(This); ok && o.Of != nil && m.Of != nil && strings.EqualFold(m.Of.Name, o.Of.Name) {
			return true
		}
	case Array:
		switch m := m.(type) {
		case Array:
			if o.NonEmpty && !m.NonEmpty {
				return false
			}
			if o.List && !m.List {
				return false
			}
			return structuralSub(m.Key, o.Key) && structuralSub(m.Value, o.Value)
		case Shape:
			return !o.NonEmpty || len(m.Entries) > 0
		}
	case Callable:
		if m, ok := m.(Callable); ok && !o.HasSig && (!o.Closure || m.Closure) {
			return true
		}
	case Iterable:
		switch m := m.(type) {
		case Array:
			return structuralSub(m.Key, o.Key) && structuralSub(m.Value, o.Value)
		case Shape:
			return true
		case Iterable:
			return structuralSub(m.Key, o.Key) && structuralSub(m.Value, o.Value)
		}
	case ClassString:
		if _, ok := m.(ClassString); ok && o.Of == nil {
			return true
		}
	}
	return false
}

// structuralSub is a hierarchy-free subtype test used while normalising.
func structuralSub(a, b Type) bool {
	if Equal(a, b) {
		return true
	}
	if _, ok := b.(Mixed); ok {
		return true
	}
	if _, ok := a.(Never); ok {
		return true
	}
	if u, ok := b.(Union); ok {
		if au, ok := a.(Union); ok {
			for _, m := range au.Types {
				if !structuralSub(m, b) {
					return false
				}
			}
			return true
		}
		for _, m := range u.Types {
			if structuralSub(a, m) {
				return true
			}
		}
		return false
	}
	if au, ok := a.(Union); ok {
		for _, m := range au.Types {
			if !structuralSub(m, b) {
				return false
			}
		}
		return true
	}
	if _, ok := a.(BoolLit); ok {
		if _, ok := b.(Bool); ok {
			return true
		}
	}
	return absorbs(b, a)
}

func stringLitFits(v string, s String) bool {
	if s.NonEmpty && v == "" {
		return false
	}
	if s.Numeric && !isNumericString(v) {
		return false
	}
	if s.Lowercase && strings.ToLower(v) != v {
		return false
	}
	return true
}

// stringRefines reports whether every m string is an o string.
func stringRefines(m, o String) bool {
	return (!o.NonEmpty || m.NonEmpty || m.Numeric) && (!o.Numeric || m.Numeric) &&
		(!o.Literal || m.Literal) && (!o.Callable || m.Callable) && (!o.Lowercase || m.Lowercase)
}

func isNumericString(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	i := 0
	if s[0] == '+' || s[0] == '-' {
		i++
	}
	digits, dot, exp := 0, false, false
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '.' && !dot && !exp:
			dot = true
		case (c == 'e' || c == 'E') && digits > 0 && !exp:
			exp = true
			digits = 0
			if i+1 < len(s) && (s[i+1] == '+' || s[i+1] == '-') {
				i++
			}
		default:
			return false
		}
	}
	return digits > 0
}

// ------------------------------------------------------------------ intersection

// MakeIntersection builds a normalised intersection. Non-object members are
// resolved structurally where possible; the result is never for obviously
// disjoint members.
func MakeIntersection(ts ...Type) Type {
	var members []Type
	for _, t := range ts {
		switch t := t.(type) {
		case nil:
		case Intersection:
			members = append(members, t.Types...)
		case Mixed:
		default:
			members = append(members, t)
		}
	}
	// Distribute over unions: (A|B) & C = (A&C) | (B&C).
	for i, m := range members {
		if u, ok := m.(Union); ok {
			var alts []Type
			for _, alt := range u.Types {
				rest := append(append([]Type{}, members[:i]...), members[i+1:]...)
				alts = append(alts, MakeIntersection(append(rest, alt)...))
			}
			return MakeUnion(alts...)
		}
	}
	var out []Type
	for _, m := range members {
		dup := false
		for _, o := range out {
			if Equal(m, o) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, m)
		}
	}
	switch len(out) {
	case 0:
		return TMixed
	case 1:
		return out[0]
	}
	// Structural meets for non-object kinds.
	if r, ok := structuralMeet(out); ok {
		return r
	}
	return Intersection{out}
}

// structuralMeet handles intersections the lattice can decide without a
// hierarchy (scalars, arrays, callables). ok is false when the members are
// all object-like and must stay an intersection.
func structuralMeet(ms []Type) (Type, bool) {
	allObject := true
	for _, m := range ms {
		switch m.(type) {
		case Class, Object, Param, Unknown, Static, This:
		default:
			allObject = false
		}
	}
	if allObject {
		return nil, false
	}
	// Pairwise reduce.
	acc := ms[0]
	for _, m := range ms[1:] {
		acc = meet2(acc, m)
	}
	return acc, true
}

func meet2(a, b Type) Type {
	if Equal(a, b) {
		return a
	}
	if structuralSub(a, b) {
		return a
	}
	if structuralSub(b, a) {
		return b
	}
	switch a := a.(type) {
	case String:
		switch b := b.(type) {
		case String:
			return String{NonEmpty: a.NonEmpty || b.NonEmpty, Numeric: a.Numeric || b.Numeric,
				Literal: a.Literal || b.Literal, Callable: a.Callable || b.Callable, Lowercase: a.Lowercase || b.Lowercase}
		case ClassString:
			return b
		}
	case ClassString:
		if _, ok := b.(String); ok {
			return a
		}
	case Int:
		if b, ok := b.(Int); ok {
			lo, hi := max(a.Lo, b.Lo), min(a.Hi, b.Hi)
			if lo > hi {
				return TNever
			}
			return Int{lo, hi}
		}
	case Array:
		switch b := b.(type) {
		case Array:
			return Array{Key: meet2(a.Key, b.Key), Value: meet2(a.Value, b.Value), List: a.List || b.List, NonEmpty: a.NonEmpty || b.NonEmpty}
		case Shape:
			return b
		}
	case Shape:
		if _, ok := b.(Array); ok {
			return a
		}
	case Iterable:
		switch b := b.(type) {
		case Array:
			return Array{Key: meet2(a.Key, b.Key), Value: meet2(a.Value, b.Value), List: b.List, NonEmpty: b.NonEmpty}
		case Iterable:
			return Iterable{meet2(a.Key, b.Key), meet2(a.Value, b.Value)}
		case Class, Object:
			return MakeIntersection(NewClass("Traversable"), b)
		}
	case Callable:
		switch b := b.(type) {
		case Callable:
			if a.HasSig {
				return a
			}
			return b
		case Class, Object:
			return Intersection{[]Type{a, b}}
		}
	case Class, Object:
		switch b.(type) {
		case Callable:
			return Intersection{[]Type{a, b}}
		case Iterable:
			return meet2(b, a)
		}
	}
	// Disjoint primitive kinds.
	if kindOf(a) != kindOf(b) && kindOf(a) != kindAny && kindOf(b) != kindAny {
		return TNever
	}
	return Intersection{[]Type{a, b}}
}

type kind int

const (
	kindAny kind = iota
	kindNull
	kindBool
	kindInt
	kindFloat
	kindString
	kindArray
	kindObject
	kindResource
	kindCallable
	kindIterable
)

func kindOf(t Type) kind {
	switch t.(type) {
	case Null:
		return kindNull
	case Bool, BoolLit:
		return kindBool
	case Int:
		return kindInt
	case Float, FloatLit:
		return kindFloat
	case String, StringLit, ClassString:
		return kindString
	case Array, Shape:
		return kindArray
	case Class, Object, Static, This, EnumCase:
		return kindObject
	case Resource:
		return kindResource
	case Callable:
		return kindCallable
	case Iterable:
		return kindIterable
	}
	return kindAny
}

// ------------------------------------------------------------------ queries

// Members returns the union members of t (t itself when not a union).
func Members(t Type) []Type {
	if u, ok := t.(Union); ok {
		return u.Types
	}
	return []Type{t}
}

// IsNullable reports whether null is a member of t.
func IsNullable(t Type) bool {
	switch t := t.(type) {
	case Null, Mixed:
		return true
	case Union:
		for _, m := range t.Types {
			if IsNullable(m) {
				return true
			}
		}
	}
	return false
}

// StripNull removes null from t.
func StripNull(t Type) Type {
	switch t := t.(type) {
	case Null:
		return TNever
	case Union:
		var keep []Type
		for _, m := range t.Types {
			if _, ok := m.(Null); !ok {
				keep = append(keep, m)
			}
		}
		return MakeUnion(keep...)
	}
	return t
}

// IsMixed reports whether t is mixed (or an unknown treated as mixed).
func IsMixed(t Type) bool {
	switch t.(type) {
	case Mixed, Unknown:
		return true
	}
	return false
}

// Filter keeps the union members for which keep returns true.
func Filter(t Type, keep func(Type) bool) Type {
	var out []Type
	for _, m := range Members(t) {
		if keep(m) {
			out = append(out, m)
		}
	}
	return MakeUnion(out...)
}

// Map applies f to every union member and re-unions the results.
func Map(t Type, f func(Type) Type) Type {
	ms := Members(t)
	out := make([]Type, 0, len(ms))
	for _, m := range ms {
		out = append(out, f(m))
	}
	return MakeUnion(out...)
}

// Substitute replaces type parameters by name using subst and returns the
// new type. Unmapped parameters are kept.
func Substitute(t Type, subst map[string]Type) Type {
	if len(subst) == 0 || t == nil {
		return t
	}
	switch t := t.(type) {
	case Param:
		if r, ok := subst[t.Name]; ok {
			return r
		}
		return t
	case Union:
		return Map(t, func(m Type) Type { return Substitute(m, subst) })
	case Intersection:
		ms := make([]Type, len(t.Types))
		for i, m := range t.Types {
			ms[i] = Substitute(m, subst)
		}
		return MakeIntersection(ms...)
	case Array:
		return Array{Key: Substitute(t.Key, subst), Value: Substitute(t.Value, subst), List: t.List, NonEmpty: t.NonEmpty}
	case Shape:
		es := make([]ShapeEntry, len(t.Entries))
		for i, e := range t.Entries {
			e.Type = Substitute(e.Type, subst)
			es[i] = e
		}
		return Shape{Entries: es, Unsealed: t.Unsealed, List: t.List}
	case Iterable:
		return Iterable{Substitute(t.Key, subst), Substitute(t.Value, subst)}
	case ClassString:
		if t.Of == nil {
			return t
		}
		return ClassString{Substitute(t.Of, subst)}
	case Class:
		if len(t.Args) == 0 {
			return t
		}
		args := make([]Type, len(t.Args))
		for i, a := range t.Args {
			args[i] = Substitute(a, subst)
		}
		return Class{Name: t.Name, Args: args}
	case Callable:
		if !t.HasSig {
			return t
		}
		ps := make([]CallableParam, len(t.Params))
		for i, p := range t.Params {
			p.Type = Substitute(p.Type, subst)
			ps[i] = p
		}
		return Callable{Params: ps, Return: Substitute(t.Return, subst), Closure: t.Closure, Pure: t.Pure, HasSig: true}
	}
	return t
}

// Contains reports whether t mentions the type parameter name anywhere.
func ContainsParam(t Type, name string) bool {
	found := false
	Walk(t, func(x Type) {
		if p, ok := x.(Param); ok && p.Name == name {
			found = true
		}
	})
	return found
}

// Walk calls f on t and every type nested in it.
func Walk(t Type, f func(Type)) {
	if t == nil {
		return
	}
	f(t)
	switch t := t.(type) {
	case Union:
		for _, m := range t.Types {
			Walk(m, f)
		}
	case Intersection:
		for _, m := range t.Types {
			Walk(m, f)
		}
	case Array:
		Walk(t.Key, f)
		Walk(t.Value, f)
	case Shape:
		for _, e := range t.Entries {
			Walk(e.Type, f)
		}
	case Iterable:
		Walk(t.Key, f)
		Walk(t.Value, f)
	case ClassString:
		Walk(t.Of, f)
	case Class:
		for _, a := range t.Args {
			Walk(a, f)
		}
	case Callable:
		for _, p := range t.Params {
			Walk(p.Type, f)
		}
		Walk(t.Return, f)
	}
}
