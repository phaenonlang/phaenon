package types

import "strings"

// IsSubtype reports whether every value of a is a value of b. h may be nil,
// in which case classes are related only by name.
func IsSubtype(a, b Type, h Hierarchy) bool {
	return subtype(a, b, h, 0)
}

// IsAcceptable reports whether a value of type arg may be passed where
// param is declared, under strict_types=1: subtyping plus PHP's int → float
// widening.
func IsAcceptable(arg, param Type, h Hierarchy) bool {
	if subtype(arg, param, h, 0) {
		return true
	}
	// mixed is accepted where a type parameter without a bound is expected:
	// the parameter can be anything, including mixed (PHPStan's
	// TemplateMixedType).
	if IsMixed(arg) {
		if p, ok := param.(Param); ok && (p.Bound == nil || IsMixed(p.Bound)) {
			return true
		}
	}
	// int → float widening applies to every int member of the argument.
	widened := Map(arg, func(m Type) Type {
		if _, ok := m.(Int); ok {
			return TFloat
		}
		return m
	})
	if !Equal(widened, arg) && subtype(widened, param, h, 0) {
		return true
	}
	return acceptsMaybe(arg, param, h, 0)
}

// acceptsMaybe is the acceptance PHPStan grants a general array against an
// array shape: `array<string, string>` for `array{spec: string}` passes when
// the keys and values can be the shape's, since the array may well be one.
// Everything else is decided by subtyping; mixed values are not accepted.
func acceptsMaybe(arg, param Type, h Hierarchy, depth int) bool {
	if depth > 8 {
		return false
	}
	if subtype(arg, param, h, depth) {
		return true
	}
	for _, a := range Members(arg) {
		ok := false
		for _, p := range Members(param) {
			if subtype(a, p, h, depth) || arrayMaybe(a, p, h, depth) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func arrayMaybe(a, p Type, h Hierarchy, depth int) bool {
	arr, ok := a.(Array)
	if !ok {
		return false
	}
	switch p := p.(type) {
	case Array:
		if p.List && !arr.List {
			return false
		}
		return acceptsMaybe(arr.Key, p.Key, h, depth+1) && !IsMixed(arr.Value) && acceptsMaybe(arr.Value, p.Value, h, depth+1)
	case Shape:
		for _, e := range p.Entries {
			if !Overlaps(arr.Key, shapeKeyType(e), h) {
				if e.Optional {
					continue
				}
				return false
			}
			if !acceptsMaybe(arr.Value, e.Type, h, depth+1) && !Overlaps(arr.Value, e.Type, h) {
				return false
			}
		}
		return true
	}
	return false
}

// Overlaps reports whether a and b can share a value (used for `===`
// always-false and `instanceof` always-false diagnostics).
func Overlaps(a, b Type, h Hierarchy) bool {
	for _, x := range Members(a) {
		for _, y := range Members(b) {
			if overlap2(x, y, h) {
				return true
			}
		}
	}
	return false
}

func overlap2(x, y Type, h Hierarchy) bool {
	// A type parameter overlaps what its bound overlaps.
	if p, ok := x.(Param); ok {
		if p.Bound == nil {
			return true
		}
		return Overlaps(p.Bound, y, h)
	}
	if p, ok := y.(Param); ok {
		if p.Bound == nil {
			return true
		}
		return Overlaps(x, p.Bound, h)
	}
	if subtype(x, y, h, 0) || subtype(y, x, h, 0) {
		return true
	}
	if IsMixed(x) || IsMixed(y) {
		return true
	}
	// 'github.client' is no class-string: a literal decides by itself.
	if lit, cs, ok := literalAndClassString(x, y); ok {
		return h != nil && h.Exists(lit.V) && (cs.Of == nil || subtype(Class{Name: lit.V}, cs.Of, h, 0))
	}
	if ax, ok := x.(Array); ok {
		if ay, ok := y.(Array); ok {
			// array<string, V> is never a list (PHPStan says the same): the
			// empty array they share does not make `$a is list` undecided.
			if ax.List && !ay.List && !Overlaps(ay.Key, TInt, h) || ay.List && !ax.List && !Overlaps(ax.Key, TInt, h) {
				return false
			}
		}
	}
	kx, ky := kindOf(x), kindOf(y)
	if kx == kindAny || ky == kindAny {
		// Params, intersections: be permissive.
		return true
	}
	if kx == kindObject && ky == kindObject {
		// Two class types overlap unless one is final and unrelated, or
		// both are classes (not interfaces) on different branches; we only
		// know finality here.
		cx, okx := x.(Class)
		cy, oky := y.(Class)
		if okx && oky && h != nil {
			if h.IsFinal(cx.Name) || h.IsFinal(cy.Name) {
				return false
			}
		}
		return true
	}
	if kx == kindCallable || ky == kindCallable {
		return ky == kindObject || kx == kindObject || kx == kindString || ky == kindString || kx == kindArray || ky == kindArray
	}
	if kx == kindIterable || ky == kindIterable {
		other := ky
		if ky == kindIterable {
			other = kx
		}
		return other == kindArray || other == kindObject || other == kindIterable
	}
	if kx != ky {
		return false
	}
	// Same kind, neither a subtype of the other: literals differ, ranges disjoint…
	switch x := x.(type) {
	case Int:
		y := y.(Int)
		return x.Lo <= y.Hi && y.Lo <= x.Hi
	case StringLit, FloatLit, BoolLit, EnumCase:
		return false
	case String:
		// A literal the refinement does not admit ('all' against a
		// decimal-int-string): the subtype test above already said no.
		if _, ok := y.(StringLit); ok {
			return false
		}
	}
	return true
}

func subtype(a, b Type, h Hierarchy, depth int) bool {
	if depth > 32 {
		return true
	}
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if Equal(a, b) {
		return true
	}
	if _, ok := a.(Void); ok {
		return false // void is not a value type
	}
	// An unevaluated conditional is the union of its branches.
	if c, ok := a.(Conditional); ok {
		return subtype(MakeUnion(c.Then, c.Else), b, h, depth+1)
	}
	if c, ok := b.(Conditional); ok {
		return subtype(a, MakeUnion(c.Then, c.Else), h, depth+1)
	}
	switch b := b.(type) {
	case Mixed:
		return true
	case Unknown:
		return true
	case Union:
		if au, ok := a.(Union); ok {
			for _, m := range au.Types {
				if !subtype(m, b, h, depth+1) {
					return false
				}
			}
			return true
		}
		for _, m := range b.Types {
			if subtype(a, m, h, depth+1) {
				return true
			}
		}
		if ap, ok := a.(Param); ok && ap.Bound != nil {
			// A type parameter fits a union its bound fits (K of int|string
			// against int|string), which no single member shows.
			return subtype(ap.Bound, b, h, depth+1)
		}
		if it, ok := a.(Iterable); ok {
			// iterable<K, V> is array<K, V>|Traversable<K, V>; an array's keys
			// are array-key whatever the iterable's say.
			k := it.Key
			if IsMixed(k) {
				k = TArrayKey
			}
			return subtype(Array{Key: k, Value: it.Value}, b, h, depth+1) && subtype(NewClass("Traversable"), b, h, depth+1)
		}
		// A bool is true|false, an int range may split across members.
		switch a := a.(type) {
		case Bool:
			return subtype(TTrue, b, h, depth+1) && subtype(TFalse, b, h, depth+1)
		case Int:
			return intCovered(a, b.Types)
		}
		return false
	case Intersection:
		for _, m := range b.Types {
			if !subtype(a, m, h, depth+1) {
				return false
			}
		}
		return true
	case Param:
		// A value fits a type parameter only if it is that parameter (or never).
		if ap, ok := a.(Param); ok {
			return ap.Name == b.Name
		}
		_, isNever := a.(Never)
		return isNever
	}
	switch a := a.(type) {
	case Never:
		return true
	case Mixed:
		return false
	case Unknown:
		// The error type fits everywhere: whatever produced it has already
		// been reported, and cascading errors would only add noise.
		return true
	case Union:
		for _, m := range a.Types {
			if !subtype(m, b, h, depth+1) {
				return false
			}
		}
		return true
	case Intersection:
		for _, m := range a.Types {
			if subtype(m, b, h, depth+1) {
				return true
			}
		}
		return false
	case Param:
		if a.Bound == nil {
			return false
		}
		return subtype(a.Bound, b, h, depth+1)
	case Bytes:
		_, ok := b.(Bytes)
		return ok
	case BoolLit:
		_, ok := b.(Bool)
		return ok
	case Int:
		switch b := b.(type) {
		case Int:
			return b.Lo <= a.Lo && a.Hi <= b.Hi
		}
		return false
	case FloatLit:
		_, ok := b.(Float)
		return ok
	case StringLit:
		switch b := b.(type) {
		case Callable:
			// 'strval' and 'Foo::bar' are callables when they name one.
			return !b.Closure && h != nil && h.IsCallableName(a.V)
		case String:
			return stringLitFits(a.V, b)
		case ClassString:
			if h == nil {
				return b.Of == nil
			}
			if !h.Exists(a.V) {
				return false
			}
			if b.Of == nil {
				return true
			}
			return subtype(NewClass(a.V), b.Of, h, depth+1)
		}
		return false
	case String:
		switch b := b.(type) {
		case String:
			return stringRefines(a, b)
		}
		return false
	case ClassString:
		switch b := b.(type) {
		case String:
			return !b.Numeric && !b.Lowercase
		case ClassString:
			if b.Of == nil {
				return true
			}
			aOf := a.Of
			if aOf == nil {
				aOf = TObject // class-string is class-string<object>
			}
			return subtype(aOf, b.Of, h, depth+1)
		}
		return false
	case Array:
		switch b := b.(type) {
		case Array:
			if b.NonEmpty && !a.NonEmpty {
				return false
			}
			if b.List && !a.List {
				return false
			}
			return subtype(a.Key, b.Key, h, depth+1) && subtype(a.Value, b.Value, h, depth+1)
		case Shape:
			// A general array fits only an unsealed shape with all-optional entries.
			if !b.Unsealed {
				return false
			}
			for _, e := range b.Entries {
				if !e.Optional {
					return false
				}
			}
			return true
		case Iterable:
			return subtype(a.Key, b.Key, h, depth+1) && subtype(a.Value, b.Value, h, depth+1)
		}
		return false
	case Shape:
		switch b := b.(type) {
		case Array:
			if b.NonEmpty && !shapeNonEmpty(a) {
				return false
			}
			if b.List && !a.List && !shapeIsList(a) {
				return false
			}
			for _, e := range a.Entries {
				if !subtype(shapeKeyType(e), b.Key, h, depth+1) || !subtype(e.Type, b.Value, h, depth+1) {
					return false
				}
			}
			return true
		case Shape:
			for _, be := range b.Entries {
				ae, found := shapeLookup(a, be)
				if !found {
					if be.Optional {
						continue
					}
					return false
				}
				if ae.Optional && !be.Optional {
					return false
				}
				if !subtype(ae.Type, be.Type, h, depth+1) {
					return false
				}
			}
			// Keys b does not name are fine: `array{a: int, b: int}` is an
			// `array{a: int}`, and `list{int, string, string}` a
			// `list{int, string}`, as PHPStan has it. Only `array{}` names
			// its whole content: nothing with a key is the empty array.
			if len(b.Entries) == 0 && !b.Unsealed && len(a.Entries) > 0 {
				return false
			}
			if b.List && !a.List && !shapeIsList(a) {
				return false
			}
			return true
		case Iterable:
			for _, e := range a.Entries {
				if !subtype(shapeKeyType(e), b.Key, h, depth+1) || !subtype(e.Type, b.Value, h, depth+1) {
					return false
				}
			}
			return true
		case Callable:
			if a.Callable {
				return true // is_array($callable): the array form of a callable is still callable
			}
			// [$object, 'method'] / ['Class', 'method'] is a callable array;
			// whether the method exists is the call's business (PHPStan
			// accepts the shape too).
			if b.Closure || len(a.Entries) != 2 || !a.List && !shapeIsList(a) {
				return false
			}
			first, second := a.Entries[0].Type, a.Entries[1].Type
			_, isStr := second.(StringLit)
			return (IsObjectLike(first) || subtype(first, TString, h, depth+1)) && (isStr || subtype(second, TString, h, depth+1))
		}
		return false
	case Iterable:
		switch b := b.(type) {
		case Iterable:
			return subtype(a.Key, b.Key, h, depth+1) && subtype(a.Value, b.Value, h, depth+1)
		}
		return false
	case Object:
		return false
	case ObjectShape:
		switch b := b.(type) {
		case Object:
			return true
		case ObjectShape:
			for _, want := range b.Entries {
				found := false
				for _, have := range a.Entries {
					if have.Key == want.Key {
						found = subtype(have.Type, want.Type, h, depth+1)
						break
					}
				}
				if !found && !want.Optional {
					return false
				}
			}
			return true
		}
		return false
	case Class:
		switch b := b.(type) {
		case Object:
			return true
		case ObjectShape:
			// A class is the shape when it declares every required property
			// with a fitting type (an anonymous logger with $records).
			if h == nil {
				return false
			}
			for _, e := range b.Entries {
				pt, ok := h.PropertyType(a.Name, e.Key)
				if !ok {
					if e.Optional {
						continue
					}
					return false
				}
				if pt != nil && !subtype(pt, e.Type, h, depth+1) {
					return false
				}
			}
			return true
		case Class:
			return classSub(a, b, h, depth)
		case Iterable:
			if h != nil && h.IsSubclassOf(a.Name, "Traversable") {
				// What the class iterates is what its Traversable ancestor is
				// given (Generator<K, V, S, R> iterates K => V), as foreach
				// reads it.
				k, v, found := h.IteratorArgs(a)
				if !found {
					k, v = classIterableKV(a)
				}
				return subtype(k, b.Key, h, depth+1) && subtype(v, b.Value, h, depth+1)
			}
			return false
		case Callable:
			if h != nil && (h.IsSubclassOf(a.Name, "Closure") || h.IsInvokable(a.Name)) {
				return true
			}
			return false
		case Static:
			// `return new self()` where static is declared: in a final class
			// self is static (PHPStan accepts it there and only there).
			return h != nil && b.Of != nil && h.IsFinal(a.Name) && classSub(a, *b.Of, h, depth)
		}
		return false
	case Static:
		if a.Of == nil {
			return false
		}
		switch bb := b.(type) {
		case Static:
			return bb.Of != nil && classSub(*a.Of, *bb.Of, h, depth)
		}
		return subtype(*a.Of, b, h, depth+1)
	case This:
		if a.Of == nil {
			return false
		}
		switch bb := b.(type) {
		case Static:
			return bb.Of != nil && classSub(*a.Of, *bb.Of, h, depth)
		case This:
			return bb.Of != nil && classSub(*a.Of, *bb.Of, h, depth)
		}
		return subtype(*a.Of, b, h, depth+1)
	case EnumCase:
		switch b := b.(type) {
		case Object:
			return true
		case Class:
			return classSub(Class{Name: a.Class}, b, h, depth)
		}
		return false
	case Callable:
		switch b := b.(type) {
		case Callable:
			if b.Closure && !a.Closure {
				return false
			}
			if !b.HasSig || !a.HasSig {
				// A callable of unknown signature is accepted for any
				// signature (PHPStan does the same); the mismatch, if
				// any, surfaces at the call.
				return true
			}
			return callableSub(a, b, h, depth)
		case Class:
			return a.Closure && strings.EqualFold(b.Name, "Closure")
		case Object:
			return a.Closure
		}
		return false
	case Resource, Null, Bool, Float:
		return false
	}
	return false
}

func classIterableKV(c Class) (Type, Type) {
	if len(c.Args) == 2 {
		return c.Args[0], c.Args[1]
	}
	if len(c.Args) == 1 {
		return TMixed, c.Args[0]
	}
	return TMixed, TMixed
}

func classSub(a, b Class, h Hierarchy, depth int) bool {
	if !strings.EqualFold(a.Name, b.Name) {
		if h == nil || !h.IsSubclassOf(a.Name, b.Name) {
			return false
		}
		// Type arguments across an inheritance step are not mapped without
		// the program model; accept when b has no arguments and otherwise
		// defer to the (M3) generic mapping. Until then be permissive.
		return true
	}
	if len(b.Args) == 0 {
		return true
	}
	if len(a.Args) != len(b.Args) {
		return len(a.Args) == 0
	}
	var vars []Variance
	if h != nil {
		vars = h.Variances(a.Name)
	}
	for i := range a.Args {
		if _, never := a.Args[i].(Never); never {
			continue // `new ArrayObject([])` is ArrayObject<never, never>: fits any argument
		}
		v := Invariant
		if i < len(vars) {
			v = vars[i]
		}
		switch v {
		case Covariant:
			if !subtype(a.Args[i], b.Args[i], h, depth+1) {
				return false
			}
		case Contravariant:
			if !subtype(b.Args[i], a.Args[i], h, depth+1) {
				return false
			}
		default:
			if IsMixed(b.Args[i]) {
				continue
			}
			if !subtype(a.Args[i], b.Args[i], h, depth+1) || !subtype(b.Args[i], a.Args[i], h, depth+1) {
				return false
			}
		}
	}
	return true
}

func callableSub(a, b Callable, h Hierarchy, depth int) bool {
	// b's parameters must be accepted by a: for each parameter b declares,
	// a must have a corresponding one whose type is wider.
	for i, bp := range b.Params {
		if i >= len(a.Params) {
			if bp.Optional {
				continue
			}
			last := len(a.Params) - 1
			if last >= 0 && a.Params[last].Variadic {
				if !subtype(bp.Type, a.Params[last].Type, h, depth+1) {
					return false
				}
				continue
			}
			// a takes fewer arguments: PHP ignores extras, so it is fine.
			continue
		}
		ap := a.Params[i]
		if bp.Type != nil && ap.Type != nil && !paramAccepts(bp.Type, ap.Type, h, depth+1) {
			return false
		}
		if (bp.Optional || bp.Variadic) && !ap.Optional && !ap.Variadic {
			// The caller may leave this argument out; a requires it
			// (PHPStan: `Closure(string...)` does not accept `fn (string $s)`).
			return false
		}
	}
	for i, ap := range a.Params {
		if i >= len(b.Params) && !ap.Optional && !ap.Variadic {
			return false
		}
	}
	if a.Return == nil || b.Return == nil {
		return true
	}
	if _, ok := b.Return.(Void); ok {
		return true
	}
	if IsMixed(b.Return) {
		return true // `callable(): mixed` takes a void closure too, as PHPStan does
	}
	return subtype(a.Return, b.Return, h, depth+1)
}

// literalAndClassString pairs a string literal with a class-string, in
// either order.
func literalAndClassString(x, y Type) (StringLit, ClassString, bool) {
	if l, ok := x.(StringLit); ok {
		if c, ok := y.(ClassString); ok {
			return l, c, true
		}
	}
	if l, ok := y.(StringLit); ok {
		if c, ok := x.(ClassString); ok {
			return l, c, true
		}
	}
	return StringLit{}, ClassString{}, false
}

// paramAccepts reports whether a callable's parameter of type inner takes
// every value the expected signature passes (outer): acceptance rather
// than subtyping, so int flows into float (PHP widens it) as PHPStan
// allows.
func paramAccepts(outer, inner Type, h Hierarchy, depth int) bool {
	return IsAcceptable(outer, inner, h)
}

// intCovered reports whether range a is covered by the int members of ms.
func intCovered(a Int, ms []Type) bool {
	var ranges []Int
	for _, m := range ms {
		if i, ok := m.(Int); ok {
			ranges = append(ranges, i)
		}
	}
	if len(ranges) == 0 {
		return false
	}
	lo := a.Lo
	for lo <= a.Hi {
		progressed := false
		for _, r := range ranges {
			if r.Lo <= lo && lo <= r.Hi {
				if r.Hi >= a.Hi {
					return true
				}
				lo = r.Hi + 1
				progressed = true
				break
			}
		}
		if !progressed {
			return false
		}
	}
	return true
}

func shapeKeyType(e ShapeEntry) Type {
	if e.IsInt {
		return IntLit(e.IntKey)
	}
	return StringLit{e.Key}
}

func shapeNonEmpty(s Shape) bool {
	for _, e := range s.Entries {
		if !e.Optional {
			return true
		}
	}
	return false
}

func shapeIsList(s Shape) bool {
	for i, e := range s.Entries {
		if !e.IsInt || e.IntKey != int64(i) {
			return false
		}
	}
	return true
}

func shapeLookup(s Shape, key ShapeEntry) (ShapeEntry, bool) {
	for _, e := range s.Entries {
		if e.IsInt == key.IsInt && e.IntKey == key.IntKey && e.Key == key.Key {
			return e, true
		}
	}
	return ShapeEntry{}, false
}
