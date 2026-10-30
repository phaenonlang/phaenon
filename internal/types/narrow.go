package types

import "strings"

// Narrowing helpers: given a type and a predicate outcome, compute the
// narrowed type. They are hierarchy-aware where class relations matter.

// Truthy removes the falsy values from t: null, false, 0, 0.0, ”, '0', [].
func Truthy(t Type) Type {
	return Map(t, func(m Type) Type {
		switch m := m.(type) {
		case Null:
			return TNever
		case Bool:
			return TTrue
		case BoolLit:
			if !m.V {
				return TNever
			}
		case Int:
			if m.Lo == 0 && m.Hi == 0 {
				return TNever
			}
			if m.Lo == 0 {
				return Int{1, m.Hi}
			}
			if m.Hi == 0 {
				return Int{m.Lo, -1}
			}
			if m.Lo < 0 && 0 < m.Hi {
				return MakeUnion(Int{m.Lo, -1}, Int{1, m.Hi})
			}
		case FloatLit:
			if m.V == 0 {
				return TNever
			}
		case StringLit:
			if m.V == "" || m.V == "0" {
				return TNever
			}
		case String:
			m.NonEmpty = true
			return m
		case Array:
			m.NonEmpty = true
			return m
		case Shape:
			if !shapeNonEmpty(m) && len(m.Entries) == 0 && !m.Unsealed {
				return TNever
			}
		case Mixed:
			return m
		}
		return m
	})
}

// Falsy keeps the falsy values of t.
func Falsy(t Type) Type {
	return Map(t, func(m Type) Type {
		switch m := m.(type) {
		case Null:
			return m
		case Bool:
			return TFalse
		case BoolLit:
			if m.V {
				return TNever
			}
			return m
		case Int:
			if m.Lo <= 0 && 0 <= m.Hi {
				return IntLit(0)
			}
			return TNever
		case Float:
			return FloatLit{0}
		case FloatLit:
			if m.V != 0 {
				return TNever
			}
			return m
		case StringLit:
			if m.V == "" || m.V == "0" {
				return m
			}
			return TNever
		case String:
			if m.NonEmpty {
				if m.Numeric {
					return StringLit{"0"}
				}
				return StringLit{"0"}
			}
			return MakeUnion(StringLit{""}, StringLit{"0"})
		case ClassString:
			return TNever
		case Array:
			if m.NonEmpty {
				return TNever
			}
			return Shape{}
		case Shape:
			if shapeNonEmpty(m) {
				return TNever
			}
			return Shape{}
		case Mixed:
			return MakeUnion(TNull, TFalse, IntLit(0), FloatLit{0}, StringLit{""}, StringLit{"0"}, Shape{})
		case Class, Object, Static, This, EnumCase, Callable, Resource, Param, Unknown, Intersection, Iterable:
			if _, isIterable := m.(Iterable); isIterable {
				return Shape{}
			}
			if _, isCallable := m.(Callable); isCallable {
				return TNever
			}
			return TNever
		}
		return m
	})
}

// Intersect narrows t to the members compatible with target (the "then"
// branch of instanceof / is_*): members that are subtypes of target stay,
// members target is a subtype of become target, mixed becomes target, and
// the rest are dropped unless they could overlap (interfaces), in which
// case the intersection is kept.
func Intersect(t, target Type, h Hierarchy) Type {
	if IsMixed(t) {
		return target
	}
	var out []Type
	for _, m := range Members(t) {
		for _, tg := range Members(target) {
			if c, ok := m.(Callable); ok && !c.Closure {
				// is_array($callable): the array form; is_string: the name.
				switch tg.(type) {
				case Array, Shape:
					out = append(out, CallableArray)
					continue
				case String:
					out = append(out, String{Callable: true})
					continue
				}
			}
			switch {
			case subtype(m, tg, h, 0):
				out = append(out, m)
			case subtype(tg, m, h, 0):
				out = append(out, tg)
			case overlap2(m, tg, h):
				out = append(out, MakeIntersection(m, tg))
			}
		}
	}
	return MakeUnion(out...)
}

// CallableArray is the array form of a callable: [$object|'Class', 'method'].
var CallableArray Type = Shape{List: true, Entries: []ShapeEntry{
	{IsInt: true, IntKey: 0, Type: MakeUnion(TObject, TString)},
	{IsInt: true, IntKey: 1, Type: TString},
}}

// Subtract narrows t to the members not of type target (the "else" branch).
func Subtract(t, target Type, h Hierarchy) Type {
	if IsMixed(t) {
		return t
	}
	var out []Type
	for _, m := range Members(t) {
		if subtype(m, target, h, 0) {
			continue
		}
		// bool minus true is false; int minus 0 splits the range.
		switch mm := m.(type) {
		case String:
			if lit, ok := target.(StringLit); ok && lit.V == "" && !mm.NonEmpty {
				mm.NonEmpty = true // `'' !== $s` leaves a non-empty-string
				out = append(out, mm)
				continue
			}
		case Bool:
			if subtype(TTrue, target, h, 0) {
				out = append(out, TFalse)
				continue
			}
			if subtype(TFalse, target, h, 0) {
				out = append(out, TTrue)
				continue
			}
		case Int:
			if lit, ok := IsIntLit(target); ok && mm.Lo <= lit && lit <= mm.Hi && mm.Lo != mm.Hi {
				if lit > mm.Lo {
					out = append(out, Int{mm.Lo, lit - 1})
				}
				if lit < mm.Hi {
					out = append(out, Int{lit + 1, mm.Hi})
				}
				continue
			}
		case Class:
			// Removing an enum case from its enum leaves the other cases;
			// the caller handles enums (needs the case list). Classes stay.
		}
		out = append(out, m)
	}
	return MakeUnion(out...)
}

// IterableKV returns the key and value types produced by iterating t.
// ok is false when t cannot be iterated at all.
func IterableKV(t Type, h Hierarchy) (key, value Type, ok bool) {
	var ks, vs []Type
	ok = true
	for _, m := range Members(t) {
		switch m := m.(type) {
		case Array:
			ks = append(ks, m.Key)
			vs = append(vs, m.Value)
		case Shape:
			for _, e := range m.Entries {
				ks = append(ks, shapeKeyType(e))
				vs = append(vs, e.Type)
			}
			if m.Unsealed {
				ks = append(ks, TArrayKey)
				vs = append(vs, TMixed)
			}
		case Iterable:
			ks = append(ks, m.Key)
			vs = append(vs, m.Value)
		case Mixed, Unknown:
			ks = append(ks, TMixed)
			vs = append(vs, TMixed)
		case Class:
			if h != nil {
				if k, v, found := h.IteratorArgs(m); found {
					ks = append(ks, k)
					vs = append(vs, v)
					continue
				}
				if !h.IsSubclassOf(m.Name, "Traversable") {
					ok = false
					continue
				}
			}
			k, v := classIterableKV(m)
			ks = append(ks, k)
			vs = append(vs, v)
		case Object, Param, Intersection, Static, This:
			ks = append(ks, TMixed)
			vs = append(vs, TMixed)
		default:
			ok = false
		}
	}
	if len(vs) == 0 {
		return TNever, TNever, ok
	}
	return MakeUnion(ks...), MakeUnion(vs...), ok
}

// ClassNames returns the class names of the object members of t (for member
// lookup). ok is false when t has non-object members (other than null when
// allowNull).
func ClassNames(t Type) []string {
	var names []string
	for _, m := range Members(t) {
		switch m := m.(type) {
		case Class:
			names = append(names, m.Name)
		case Static:
			if m.Of != nil {
				names = append(names, m.Of.Name)
			}
		case This:
			if m.Of != nil {
				names = append(names, m.Of.Name)
			}
		case EnumCase:
			names = append(names, m.Class)
		case Intersection:
			for _, x := range m.Types {
				if c, ok := x.(Class); ok {
					names = append(names, c.Name)
				}
			}
		case Param:
			if m.Bound != nil {
				names = append(names, ClassNames(m.Bound)...)
			}
		}
	}
	return names
}

// IsObjectLike reports whether every member of t is an object (or a type
// parameter bounded by one).
func IsObjectLike(t Type) bool {
	for _, m := range Members(t) {
		switch m := m.(type) {
		case Class, Object, Static, This, EnumCase, Intersection, Unknown, ObjectShape:
		case Callable:
			if !m.Closure {
				return false
			}
		case Param:
			if m.Bound == nil || !IsObjectLike(m.Bound) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// ClassArgs returns the type arguments of the single class member of t.
func ClassArgs(t Type) []Type {
	if c, ok := t.(Class); ok {
		return c.Args
	}
	return nil
}

// SameClass reports whether a and b name the same class.
func SameClass(a, b string) bool { return strings.EqualFold(a, b) }

// Lower is the canonical key of a class / function name.
func Lower(name string) string { return strings.ToLower(name) }
