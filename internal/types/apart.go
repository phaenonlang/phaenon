package types

// NeverSubtype reports whether a is certainly no subtype of b: the two are
// apart, part by part, where IsSubtype saying no only means that a is not
// known to fit. It is the "no" of PHPStan's three-valued isSuperTypeOf(),
// which its method compatibility rules report while a "maybe" passes:
//
//   - `list<string>` is never a `list<int>`, nor `array{a: string}` an
//     `array{a: int}`: arrays are compared by their keys and values, the
//     empty array they share aside;
//   - an `array` may be a `map<string, mixed>`, a `class-string` a
//     `class-string<User>` and an `'a'|'b'` an `'a'`: the wider type is not
//     apart from the narrower one;
//   - a union is apart from a type when each of its members is.
//
// Type parameters stand for their bounds, as they do in PHPStan's rules: a
// method's `T` and the `TDoc` its override names are the same `object`.
// Whatever the function cannot tell is not apart: it answers false for
// `static`, intersections and object shapes.
func NeverSubtype(a, b Type, h Hierarchy) bool {
	if a == nil || b == nil {
		return false
	}
	return apart(toBounds(a), toBounds(b), h, 0)
}

// toBounds replaces every type parameter by its bound, mixed without one.
func toBounds(t Type) Type {
	return MapDeep(t, func(n Type) Type {
		p, ok := n.(Param)
		if !ok {
			return n
		}
		if p.Bound == nil {
			return TMixed
		}
		return toBounds(p.Bound)
	})
}

// apart is NeverSubtype on types without parameters.
func apart(a, b Type, h Hierarchy, depth int) bool {
	if depth > 16 || a == nil || b == nil {
		return false
	}
	if c, ok := a.(Conditional); ok {
		a = MakeUnion(c.Then, c.Else)
	}
	if c, ok := b.(Conditional); ok {
		b = MakeUnion(c.Then, c.Else)
	}
	if subtype(a, b, h, 0) {
		return false
	}
	as, bs := Members(a), Members(b)
	if len(as) > 1 || len(bs) > 1 {
		for _, x := range as {
			for _, y := range bs {
				if !apart(x, y, h, depth+1) {
					return false
				}
			}
		}
		return true
	}
	a, b = classOfCase(a), classOfCase(b)
	if !decidable(a) || !decidable(b) {
		return false
	}
	ka, kb := kindOf(a), kindOf(b)
	switch {
	case ka == kindArray && kb == kindArray:
		return arraysApart(a, b, h, depth)
	case ka == kindIterable && (kb == kindArray || kb == kindIterable), ka == kindArray && kb == kindIterable:
		ak, av := keyValue(a)
		bk, bv := keyValue(b)
		return apart(ak, bk, h, depth+1) || apart(av, bv, h, depth+1)
	case ka == kindObject && kb == kindObject:
		return classesApart(a, b, h, depth)
	case ka == kindCallable && kb == kindCallable:
		return callablesApart(a.(Callable), b.(Callable), h, depth)
	case ka == kindBytes && kb == kindString, ka == kindString && kb == kindBytes:
		return false // bytes is a PHP string: the two are told apart elsewhere
	}
	if ca, ok := a.(ClassString); ok {
		if cb, ok := b.(ClassString); ok {
			return ca.Of != nil && cb.Of != nil && apart(ca.Of, cb.Of, h, depth+1)
		}
	}
	return !overlap2(a, b, h)
}

// decidable tells the types apart can decide on: a single type of a known
// kind, or a callable signature.
func decidable(t Type) bool {
	switch t.(type) {
	case Static, This, ObjectShape:
		return false
	}
	return kindOf(t) != kindAny
}

// classOfCase widens an enum case to its enum.
func classOfCase(t Type) Type {
	if c, ok := t.(EnumCase); ok {
		return Class{Name: c.Class}
	}
	return t
}

// keyValue is what an array, a shape or an iterable holds; a shape's keys
// and values are the union of its entries'.
func keyValue(t Type) (key, value Type) {
	switch t := t.(type) {
	case Array:
		return t.Key, t.Value
	case Iterable:
		return t.Key, t.Value
	case Shape:
		if t.Unsealed || len(t.Entries) == 0 {
			return TMixed, TMixed
		}
		keys := make([]Type, len(t.Entries))
		values := make([]Type, len(t.Entries))
		for i, e := range t.Entries {
			keys[i], values[i] = shapeKeyType(e), e.Type
		}
		return MakeUnion(keys...), MakeUnion(values...)
	}
	return TMixed, TMixed
}

// arraysApart compares arrays and shapes the way PHPStan does: by key and
// by value, and entry by entry where the expected type names them.
func arraysApart(a, b Type, h Hierarchy, depth int) bool {
	switch b := b.(type) {
	case Array:
		switch a := a.(type) {
		case Array:
			// array<string, V> is never a list, in either direction.
			if a.List && !b.List && !Overlaps(b.Key, TInt, h) || b.List && !a.List && !Overlaps(a.Key, TInt, h) {
				return true
			}
		case Shape:
			if len(a.Entries) == 0 && !a.Unsealed {
				return b.NonEmpty // array{} is never a non-empty-array
			}
		}
		ak, av := keyValue(a)
		return apart(ak, b.Key, h, depth+1) || apart(av, b.Value, h, depth+1)
	case Shape:
		switch a := a.(type) {
		case Array:
			for _, e := range b.Entries {
				if !Overlaps(a.Key, shapeKeyType(e), h) {
					if e.Optional {
						continue
					}
					return true // the array cannot have the key
				}
				if apart(a.Value, e.Type, h, depth+1) {
					return true
				}
			}
		case Shape:
			for _, be := range b.Entries {
				ae, found := shapeLookup(a, be)
				if !found {
					if !be.Optional && !a.Unsealed {
						return true // a lacks an entry b requires
					}
					continue
				}
				if apart(ae.Type, be.Type, h, depth+1) {
					return true
				}
			}
		}
	}
	return false
}

// classesApart tells two object types that share no instance, or name one
// generic class with arguments that do not go together. Unrelated classes
// share none; an interface and a class that does not implement it do in a
// subclass, unless the class is final. A class given where its subclass is
// expected may be one.
func classesApart(a, b Type, h Hierarchy, depth int) bool {
	ca, ok := a.(Class)
	if !ok || h == nil {
		return false
	}
	cb, ok := b.(Class)
	if !ok || !h.Exists(ca.Name) || !h.Exists(cb.Name) {
		return false
	}
	if SameClass(ca.Name, cb.Name) {
		return argsApart(ca, cb, h, depth)
	}
	if h.IsSubclassOf(ca.Name, cb.Name) || h.IsSubclassOf(cb.Name, ca.Name) {
		return false
	}
	if h.IsFinal(ca.Name) || h.IsFinal(cb.Name) {
		return true
	}
	return !h.IsInterface(ca.Name) && !h.IsInterface(cb.Name)
}

// argsApart compares the type arguments of one class by their variance. An
// invariant argument must be the same type, as PHPStan has it, but for a
// union: `array-key` there takes `int` and `string`, and it is the union
// Phaenon reads it as.
func argsApart(a, b Class, h Hierarchy, depth int) bool {
	if len(a.Args) == 0 || len(a.Args) != len(b.Args) {
		return false
	}
	vars := h.Variances(a.Name)
	for i := range a.Args {
		x, y := a.Args[i], b.Args[i]
		v := Invariant
		if i < len(vars) {
			v = vars[i]
		}
		switch v {
		case Covariant:
			if apart(x, y, h, depth+1) {
				return true
			}
		case Contravariant:
			if apart(y, x, h, depth+1) {
				return true
			}
		default:
			if !decidable(classOfCase(x)) && !isUnion(x) || !decidable(classOfCase(y)) && !isUnion(y) {
				continue // mixed, never, static: any argument goes with them
			}
			if apart(x, y, h, depth+1) || apart(y, x, h, depth+1) {
				return true
			}
			if isUnion(x) || isUnion(y) {
				continue
			}
			if !subtype(x, y, h, 0) || !subtype(y, x, h, 0) {
				return true
			}
		}
	}
	return false
}

func isUnion(t Type) bool {
	_, ok := t.(Union)
	return ok
}

// callablesApart compares two signatures: a is no b when it requires more
// arguments than b's callers pass, when a parameter of b's is one a's never
// takes, or when what it returns is never what b's does.
func callablesApart(a, b Callable, h Hierarchy, depth int) bool {
	if !a.HasSig || !b.HasSig {
		return false
	}
	variadic := len(b.Params) > 0 && b.Params[len(b.Params)-1].Variadic
	for i, ap := range a.Params {
		if i >= len(b.Params) {
			if !ap.Optional && !ap.Variadic && !variadic {
				return true
			}
			break
		}
		bp := b.Params[i]
		if ap.Type == nil || bp.Type == nil || IsAcceptable(bp.Type, ap.Type, h) {
			continue
		}
		if apart(bp.Type, ap.Type, h, depth+1) {
			return true
		}
	}
	if a.Return == nil || b.Return == nil {
		return false
	}
	if _, void := b.Return.(Void); void {
		return false
	}
	return apart(a.Return, b.Return, h, depth+1)
}
