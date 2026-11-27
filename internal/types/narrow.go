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
			return NonEmptyShape(m)
		case Mixed:
			return m
		}
		return m
	})
}

// NonEmptyShape is s where it holds an entry at least: never for a sealed
// shape with none, and a sealed shape whose one entry is optional has it,
// as PHPStan's non-empty `array{a?: int}` is `array{a: int}`. A list holds
// its first entry where it holds any: PHPStan's non-empty `list{0?: int,
// 1?: int}` is `list{0: int, 1?: int}`.
func NonEmptyShape(s Shape) Type {
	if shapeNonEmpty(s) || s.Unsealed {
		return s
	}
	switch {
	case len(s.Entries) == 0:
		return TNever
	case len(s.Entries) == 1 || s.List && s.Entries[0].IsInt && s.Entries[0].IntKey == 0:
		entries := append([]ShapeEntry(nil), s.Entries...)
		entries[0].Optional = false
		s.Entries = entries
	}
	return s
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
		case Class, Object, ObjectShape, Static, This, EnumCase, Callable, Resource, Param, Unknown, Intersection, Iterable:
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
					out = append(out, String{Callable: true, NonEmpty: true})
					continue
				}
			}
			// A shape that names no method its class has is no callable,
			// though subtype takes it for one.
			if s, ok := m.(Shape); ok {
				if c, ok := tg.(Callable); ok && !c.Closure && callableShape(s, h) == certainlyNot {
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
var CallableArray Type = Shape{List: true, Callable: true, Entries: []ShapeEntry{
	{IsInt: true, IntKey: 0, Type: MakeUnion(TObject, TString)},
	{IsInt: true, IntKey: 1, Type: TString},
}}

// callableForm is the meet of t with the callable c where t is a string,
// an array or an iterable, which a callable may be besides an object: what
// is_callable() leaves of t, as PHPStan meets them. ok is false for any
// other t. A Closure is an object and meets none of them.
//
//   - A string is a callable-string, the name of a function or of a static
//     method, which is never empty and never numeric: PHPStan has no
//     numeric callable-string, and no function's name is a number.
//   - A class-string stays one, which a class's name that is also a
//     function's is. PHPStan has the intersection with callable; the
//     lattice intersects objects alone (design/checker.md §3.1), so a
//     class-string the test found callable is not taken where a callable
//     is expected, a finding PHPStan does not make.
//   - array<K, V> is the array form, [class-or-object, method], each entry
//     V met with what it holds; none where V holds no string to name the
//     method, as PHPStan has it (ArrayType::isCallable() asks V alone, not
//     K).
//   - A shape is the array form where its keys are 0 and 1, and none
//     otherwise (callableShapeForm).
//   - iterable<K, V> is the array form of its arrays, or a Traversable that
//     is callable.
func callableForm(t Type, c Callable) (Type, bool) {
	switch t.(type) {
	case String, ClassString, Array, Shape, Iterable:
	default:
		return nil, false
	}
	if c.Closure {
		return TNever, true
	}
	switch t := t.(type) {
	case String:
		if t.Numeric {
			return TNever, true
		}
		t.Callable, t.NonEmpty = true, true
		return t, true
	case Array:
		return callableArrayOf(t.Value), true
	case Shape:
		return callableShapeForm(t), true
	case Iterable:
		return MakeUnion(callableArrayOf(t.Value), Intersection{[]Type{traversableOf(t), c}}), true
	}
	return t, true // a class-string
}

// callableArrayOf is the array form of a callable whose entries are values
// of an array<K, V>: CallableArray's entries met with v, none where either
// is none.
func callableArrayOf(v Type) Type {
	form := CallableArray.(Shape)
	entries := make([]ShapeEntry, len(form.Entries))
	for i, e := range form.Entries {
		e.Type = MakeIntersection(v, e.Type)
		if isNever(e.Type) {
			return TNever
		}
		entries[i] = e
	}
	form.Entries = entries
	return form
}

// callableShapeForm is the shape s met with a callable: the array form,
// marked callable, where every key s names is 0 or 1 and a sealed s names
// both, and none otherwise, which is PHPStan's
// ConstantArrayType::findTypeAndMethodNames(). What the entries hold is not
// asked, as PHPStan does not ask it of the meet: `array{int, int}` met with
// a callable is `list{int, int}&callable` there.
func callableShapeForm(s Shape) Type {
	var first, second *ShapeEntry
	for i := range s.Entries {
		e := &s.Entries[i]
		switch {
		case e.IsInt && e.IntKey == 0:
			first = e
		case e.IsInt && e.IntKey == 1:
			second = e
		default:
			return TNever
		}
	}
	if !s.Unsealed && (first == nil || second == nil) {
		return TNever
	}
	var entries []ShapeEntry
	for _, e := range []*ShapeEntry{first, second} {
		if e != nil {
			entries = append(entries, *e)
		}
	}
	return Shape{Entries: entries, Unsealed: s.Unsealed, List: first != nil, Callable: true}
}

func isNever(t Type) bool {
	_, ok := t.(Never)
	return ok
}

// certainty is what is known of a value: that it is of a type, that it is
// not, or that it may be either.
type certainty int8

const (
	certainlyNot certainty = iota
	perhaps
	certainly
)

// callableShape is what is known of the shape s as a callable, read as
// PHPStan's ConstantArrayType::isCallable() reads it. A shape whose keys
// callableShapeForm refuses is none. Where the method is named by literals
// and the first entry is an object of one class, or a class's name, each
// method is looked up: one the class has makes s a callable, but for a
// method that is not static named with the class's name, which counts for
// nothing; one a final class lacks counts against it, a class a docblock
// marks @final included, as ObjectType::hasMethod() reads it, and one a
// class that is not final lacks leaves it uncertain. With nothing counted for it, s is
// none. An optional entry or an unsealed shape leaves s uncertain at best,
// and so does a first entry or a method of any other type. A class with
// __call() (__callStatic() for a name) may answer any method: PHPStan
// counts it against, though PHP calls it, and here it leaves s uncertain.
func callableShape(s Shape, h Hierarchy) certainty {
	if s.Callable {
		return certainly
	}
	form, ok := callableShapeForm(s).(Shape)
	if !ok {
		return certainlyNot
	}
	var first, method Type = TMixed, TMixed
	uncertain := form.Unsealed
	for _, e := range form.Entries {
		if e.IntKey == 0 {
			first = e.Type
		} else {
			method = e.Type
		}
		uncertain = uncertain || e.Optional
	}
	var names []string
	for _, m := range Members(method) {
		lit, ok := m.(StringLit)
		if !ok {
			return perhaps
		}
		names = append(names, lit.V)
	}
	class, byName, ok := callableClass(first)
	if !ok || h == nil || !h.Exists(class) {
		return perhaps
	}
	magic := "__call"
	if byName {
		magic = "__callStatic"
	}
	result, counted, missing := certainly, false, false
	for _, name := range names {
		has, static := h.HasMethod(class, name)
		// A Closure is invoked through __invoke(), which its stub leaves
		// out as PHP's own class does.
		has = has || strings.EqualFold(class, "Closure") && strings.EqualFold(name, "__invoke")
		found := certainly
		switch {
		case has && byName && !static:
			continue
		case has:
		case !h.IsFinal(class):
			found = perhaps
		default:
			if answers, _ := h.HasMethod(class, magic); !answers {
				missing = true
				continue
			}
			found = perhaps
		}
		if uncertain {
			found = perhaps
		}
		counted = true
		result = min(result, found)
	}
	if !counted {
		return certainlyNot
	}
	if missing {
		result = min(result, perhaps)
	}
	return result
}

// callableClass is the class of the first entry of a callable's array form:
// an object's, or the one a literal names, byName then. A class-string is
// left uncertain, where PHPStan decides `[static::class, 'create']` to be
// a callable: which class a class-string names is known from a docblock as
// a rule, and the checker does not take the docblock as certain.
func callableClass(t Type) (class string, byName, ok bool) {
	switch t := t.(type) {
	case Class:
		return t.Name, false, true
	case Static:
		if t.Of != nil {
			return t.Of.Name, false, true
		}
	case This:
		if t.Of != nil {
			return t.Of.Name, false, true
		}
	case EnumCase:
		return t.Class, false, true
	case StringLit:
		return t.V, true, true
	case Callable:
		if t.Closure {
			return "Closure", false, true
		}
	}
	return "", false, false
}

// takenForCallable reports a shape that fits target only by being taken
// for a callable it may not be: subtype accepts a two-entry list where a
// callable is expected, as PHPStan accepts `[$this, 'method']`, but a test
// for a callable takes away from it only what certainly is one.
func takenForCallable(m, target Type, h Hierarchy) bool {
	s, ok := m.(Shape)
	if !ok || callableShape(s, h) == certainly {
		return false
	}
	callables := false
	rest := Filter(target, func(t Type) bool {
		c, isCallable := t.(Callable)
		callables = callables || isCallable && !c.Closure
		return !isCallable
	})
	return callables && !subtype(m, rest, h, 0)
}

// Subtract narrows t to the members not of type target (the "else" branch).
func Subtract(t, target Type, h Hierarchy) Type {
	if IsMixed(t) {
		return t
	}
	var out []Type
	for _, m := range Members(t) {
		if subtype(m, target, h, 0) && !takenForCallable(m, target, h) {
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
