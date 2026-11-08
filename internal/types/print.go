package types

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// String prints t in PHPStan vocabulary: array<string, Foo>, list<int>,
// 'a'|'b', Foo<T>, callable(int): string.
func String_(t Type) string { return Print(t) }

// Print prints t in PHPStan vocabulary.
func Print(t Type) string {
	var b strings.Builder
	(&printer{b: &b}).print(t, false)
	return b.String()
}

// PrintQualified prints t for a docblock in emitted code: like Print, but
// class names carry a leading backslash so they resolve regardless of the
// file's imports.
func PrintQualified(t Type) string {
	var b strings.Builder
	(&printer{b: &b, qualified: true}).print(t, false)
	return b.String()
}

// PrintPhaenon prints t as a .phn would write it (design/generics.md §11's
// spelling): `map<K, V>` for a keyed array, `class<T>` for a class-string,
// `?X` for X or null, `(int, string)` for a list shape; what Phaenon cannot
// spell (literals, `int<a, b>`, shapes with keys) stays PHPStan's. The
// editor's hover prints with it.
func PrintPhaenon(t Type) string {
	var b strings.Builder
	(&printer{b: &b, phaenon: true}).print(t, false)
	return b.String()
}

type printer struct {
	b         *strings.Builder
	qualified bool
	phaenon   bool // PrintPhaenon's spelling
}

func (pr *printer) class(name string) {
	if pr.qualified && name != "" && name[0] != '\\' {
		pr.b.WriteByte('\\')
	}
	pr.b.WriteString(name)
}

func (pr *printer) print(t Type, nested bool) {
	b := pr.b
	switch t := t.(type) {
	case nil:
		b.WriteString("<nil>")
	case Mixed:
		b.WriteString("mixed")
	case Never:
		b.WriteString("never")
	case Void:
		b.WriteString("void")
	case Null:
		b.WriteString("null")
	case Bool:
		b.WriteString("bool")
	case BoolLit:
		if t.V {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case Int:
		switch {
		case t.Lo == math.MinInt64 && t.Hi == math.MaxInt64:
			b.WriteString("int")
		case t.Lo == t.Hi:
			b.WriteString(strconv.FormatInt(t.Lo, 10))
		default:
			b.WriteString("int<")
			if t.Lo == math.MinInt64 {
				b.WriteString("min")
			} else {
				b.WriteString(strconv.FormatInt(t.Lo, 10))
			}
			b.WriteString(", ")
			if t.Hi == math.MaxInt64 {
				b.WriteString("max")
			} else {
				b.WriteString(strconv.FormatInt(t.Hi, 10))
			}
			b.WriteString(">")
		}
	case Float:
		b.WriteString("float")
	case FloatLit:
		s := strconv.FormatFloat(t.V, 'f', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0"
		}
		b.WriteString(s)
	case String:
		switch {
		case t.Callable:
			b.WriteString("callable-string")
		case t.DecimalInt:
			b.WriteString("decimal-int-string")
		case t.Numeric:
			b.WriteString("numeric-string")
		case t.Literal:
			b.WriteString("literal-string")
		case t.Lowercase && t.NonEmpty:
			b.WriteString("non-empty-lowercase-string")
		case t.Lowercase:
			b.WriteString("lowercase-string")
		case t.NonEmpty:
			b.WriteString("non-empty-string")
		default:
			b.WriteString("string")
		}
	case StringLit:
		b.WriteString("'")
		b.WriteString(strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(t.V))
		b.WriteString("'")
	case ClassString:
		if pr.phaenon {
			b.WriteString("class")
		} else {
			b.WriteString("class-string")
		}
		if t.Of != nil {
			b.WriteString("<")
			pr.print(t.Of, false)
			b.WriteString(">")
		}
	case Array:
		switch {
		case t.List:
			if t.NonEmpty {
				b.WriteString("non-empty-")
			}
			b.WriteString("list")
			if !IsMixed(t.Value) {
				b.WriteString("<")
				pr.print(t.Value, false)
				b.WriteString(">")
			}
		default:
			if t.NonEmpty {
				b.WriteString("non-empty-")
			}
			keyIsAny := Equal(t.Key, TArrayKey)
			if pr.phaenon && !keyIsAny {
				b.WriteString("map")
			} else {
				b.WriteString("array")
			}
			if !keyIsAny || !IsMixed(t.Value) {
				b.WriteString("<")
				if !keyIsAny {
					pr.print(t.Key, false)
					b.WriteString(", ")
				}
				pr.print(t.Value, false)
				b.WriteString(">")
			}
		}
	case Shape:
		if pr.phaenon && t.List && !t.Unsealed && !shapeOptional(t) {
			b.WriteString("(")
			for i, e := range t.Entries {
				if i > 0 {
					b.WriteString(", ")
				}
				pr.print(e.Type, false)
			}
			b.WriteString(")")
			return
		}
		if t.List {
			b.WriteString("list{")
		} else {
			b.WriteString("array{")
		}
		for i, e := range t.Entries {
			if i > 0 {
				b.WriteString(", ")
			}
			if !t.List {
				if e.IsInt {
					b.WriteString(strconv.FormatInt(e.IntKey, 10))
				} else if isIdent(e.Key) {
					b.WriteString(e.Key)
				} else {
					b.WriteString(strconv.Quote(e.Key))
				}
				if e.Optional {
					b.WriteString("?")
				}
				b.WriteString(": ")
			}
			pr.print(e.Type, false)
		}
		if t.Unsealed {
			if len(t.Entries) > 0 {
				b.WriteString(", ")
			}
			b.WriteString("...")
		}
		b.WriteString("}")
	case Object:
		b.WriteString("object")
	case ObjectShape:
		b.WriteString("object{")
		for i, e := range t.Entries {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(e.Key)
			if e.Optional {
				b.WriteString("?")
			}
			b.WriteString(": ")
			pr.print(e.Type, false)
		}
		b.WriteString("}")
	case Class:
		pr.class(t.Name)
		if len(t.Args) > 0 {
			b.WriteString("<")
			for i, a := range t.Args {
				if i > 0 {
					b.WriteString(", ")
				}
				pr.print(a, false)
			}
			b.WriteString(">")
		}
	case Static:
		b.WriteString("static")
		if t.Of != nil {
			b.WriteString("(")
			pr.print(*t.Of, false)
			b.WriteString(")")
		}
	case This:
		if pr.phaenon {
			// A .phn spells the type static; the class says more.
			if t.Of != nil {
				pr.print(*t.Of, nested)
			} else {
				b.WriteString("static")
			}
			return
		}
		b.WriteString("$this")
		if t.Of != nil {
			b.WriteString("(")
			pr.print(*t.Of, false)
			b.WriteString(")")
		}
	case EnumCase:
		pr.class(t.Class)
		b.WriteString("::")
		b.WriteString(t.Case)
	case Callable:
		if t.Pure {
			b.WriteString("pure-")
		}
		if t.Closure {
			b.WriteString("Closure")
		} else {
			b.WriteString("callable")
		}
		if t.HasSig {
			b.WriteString("(")
			for i, p := range t.Params {
				if i > 0 {
					b.WriteString(", ")
				}
				if p.Type == nil {
					b.WriteString("mixed")
				} else {
					pr.print(p.Type, false)
				}
				if p.Variadic {
					b.WriteString("...")
				}
				if p.ByRef {
					b.WriteString(" &")
				}
				if p.Name != "" {
					b.WriteString(" $" + p.Name)
				}
				if p.Optional {
					b.WriteString("=")
				}
			}
			b.WriteString("): ")
			if t.Return == nil {
				b.WriteString("mixed")
			} else {
				pr.print(t.Return, false)
			}
		}
	case Iterable:
		b.WriteString("iterable")
		keyIsAny := IsMixed(t.Key)
		if !keyIsAny || !IsMixed(t.Value) {
			b.WriteString("<")
			if !keyIsAny {
				pr.print(t.Key, false)
				b.WriteString(", ")
			}
			pr.print(t.Value, false)
			b.WriteString(">")
		}
	case Resource:
		b.WriteString("resource")
	case Bytes:
		b.WriteString("bytes")
	case Union:
		if pr.phaenon {
			if x, ok := nullable(t); ok {
				b.WriteString("?")
				pr.print(x, true)
				return
			}
		}
		if nested {
			b.WriteString("(")
		}
		for i, m := range t.Types {
			if i > 0 {
				b.WriteString("|")
			}
			pr.print(m, true)
		}
		if nested {
			b.WriteString(")")
		}
	case Intersection:
		if nested {
			b.WriteString("(")
		}
		for i, m := range t.Types {
			if i > 0 {
				b.WriteString("&")
			}
			pr.print(m, true)
		}
		if nested {
			b.WriteString(")")
		}
	case Param:
		b.WriteString(ParamName(t.Name))
	case Conditional:
		b.WriteString("(")
		if t.ParamName != "" {
			b.WriteString("$" + t.ParamName)
		} else {
			pr.print(t.Subject, false)
		}
		if t.Negated {
			b.WriteString(" is not ")
		} else {
			b.WriteString(" is ")
		}
		pr.print(t.Target, false)
		b.WriteString(" ? ")
		pr.print(t.Then, false)
		b.WriteString(" : ")
		pr.print(t.Else, false)
		b.WriteString(")")
	case Unknown:
		pr.class(t.Name)
	default:
		fmt.Fprintf(b, "%T", t)
	}
}

// ParamName strips the per-call identity suffix a type parameter name may
// carry (after a NUL byte), giving the name as declared.
func ParamName(name string) string {
	if i := strings.IndexByte(name, 0); i >= 0 {
		return name[:i]
	}
	return name
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= 0x80:
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// Native prints the closest PHP-native type for t: the type that goes into
// an emitted signature. Refinements are dropped (non-empty-string → string,
// list<int> → array, Foo<T> → Foo, 3 → int). ok is false when t has no
// native spelling at all (a bare type parameter, a shape of nothing).
func Native(t Type) (string, bool) {
	t = Erase(t)
	switch t := t.(type) {
	case Union:
		hasNull := false
		var parts []string
		for _, m := range t.Types {
			if _, ok := m.(Null); ok {
				hasNull = true
				continue
			}
			s, ok := Native(m)
			if !ok {
				return "", false
			}
			parts = append(parts, s)
		}
		if hasNull && len(parts) == 1 && !strings.ContainsAny(parts[0], "|&") && parts[0] != "mixed" {
			return "?" + parts[0], true
		}
		if hasNull {
			parts = append(parts, "null")
		}
		return strings.Join(parts, "|"), true
	case Intersection:
		if IsTextBytes(t) {
			return "string", true
		}
		var parts []string
		for _, m := range t.Types {
			s, ok := Native(m)
			if !ok {
				return "", false
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, "&"), true
	case Class:
		return "\\" + t.Name, true
	case Static:
		return "static", true
	case This:
		return "static", true
	case EnumCase:
		return "\\" + t.Class, true
	case Unknown:
		return "\\" + t.Name, true
	}
	return Print(t), true
}

// Erase maps t to the type PHP can express natively: literals widen,
// refinements drop, arrays and shapes become array, closures become
// Closure, callables callable, type parameters their bound.
func Erase(t Type) Type {
	switch t := t.(type) {
	case BoolLit:
		return TBool
	case Int:
		return TInt
	case FloatLit:
		return TFloat
	case String, StringLit, ClassString, Bytes:
		return TString // bytes is a PHP string with a Phaenon type
	case Array, Shape:
		return TArray
	case Iterable:
		return TIterable
	case EnumCase:
		return NewClass(t.Class)
	case Conditional:
		return Erase(MakeUnion(t.Then, t.Else))
	case ObjectShape:
		return TObject
	case Class:
		return NewClass(t.Name)
	case Callable:
		if t.Closure {
			return TClosure
		}
		return TCallable
	case Param:
		if t.Bound == nil {
			return TMixed
		}
		return Erase(t.Bound)
	case Union:
		return Map(t, Erase)
	case Intersection:
		ms := make([]Type, len(t.Types))
		for i, m := range t.Types {
			ms[i] = Erase(m)
		}
		return MakeIntersection(ms...)
	}
	return t
}

// nullable returns X of a union X|null whose X is one type that `?X` can
// prefix: not a union, an intersection or mixed.
func nullable(u Union) (Type, bool) {
	if len(u.Types) != 2 {
		return nil, false
	}
	var x Type
	null := false
	for _, m := range u.Types {
		if _, ok := m.(Null); ok {
			null = true
		} else {
			x = m
		}
	}
	switch x.(type) {
	case nil, Union, Intersection, Mixed, Callable:
		return nil, false
	}
	return x, null
}

func shapeOptional(s Shape) bool {
	for _, e := range s.Entries {
		if e.Optional {
			return true
		}
	}
	return false
}
