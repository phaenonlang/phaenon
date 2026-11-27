package types

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
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

// Describe prints t for a message: like Print, but a string literal
// longer than describeLimit characters is cut, as PHPStan's
// ConstantStringType::describe cuts it, so that a heredoc of a hundred
// kilobytes compared with 'x' is not written out whole. A type that is
// compared, keyed or written into code is printed whole (Print).
func Describe(t Type) string {
	var b strings.Builder
	(&printer{b: &b, message: true}).print(t, false)
	return b.String()
}

// describeLimit is where a literal is cut in a message, PHPStan's
// DESCRIBE_LIMIT. PHPStan keeps a class-string literal whole; the
// checker's literals do not say they name a class, and one shaped as a
// class's name stays whole (className).
const describeLimit = 20

// cutLiteral cuts s to limit characters, the last of them an ellipsis, at
// the end of a word where one ends within the limit, as Nette's
// Strings::truncate does for PHPStan.
func cutLiteral(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit || className(s) {
		return s
	}
	n := limit - 1
	for i := n; i >= 1; i-- {
		if wordEnds(r[i]) {
			return string(r[:i]) + "…"
		}
	}
	return string(r[:n]) + "…"
}

// className reports s shaped as a namespaced class's name: names
// separated by backslashes, a leading one allowed. A single long word, a
// hex digest, is cut as any other string.
func className(s string) bool {
	if !strings.Contains(strings.TrimPrefix(s, `\`), `\`) {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(s, `\`), `\`) {
		if !isIdent(part) {
			return false
		}
	}
	return true
}

// wordEnds reports a character Strings::truncate cuts before: white space
// or ASCII punctuation, \s\x00-/:-@\[-`{-~.
func wordEnds(c rune) bool {
	return unicode.IsSpace(c) || c <= '/' || c >= ':' && c <= '@' || c >= '[' && c <= '`' || c >= '{' && c <= '~'
}

// PrintQualified prints t for a docblock in emitted code: like Print, but
// class names carry a leading backslash so they resolve regardless of the
// file's imports.
//
// The text is PHPStan's to read, in a file of any namespace, so it holds no
// word PHPStan would take for a class of that namespace: Closure is
// \Closure, and bytes, which PHPStan has no type for, is string
// (design/strings.md §5), as the emitter writes both for a type spelled in
// a signature.
func PrintQualified(t Type) string {
	var b strings.Builder
	(&printer{b: &b, qualified: true}).print(forPHPStan(t), false)
	return b.String()
}

// PrintQualifiedValues is PrintQualified with the value type of every
// iterable type written out: array<mixed>, list<mixed> and iterable<mixed>
// where PrintQualified writes the bare word. It is for a type the compiler
// chose and writes into a docblock of its own (the @var of a lowered
// closure's parameter): PHPStan reads the bare word as an iterable type
// without its value type and reports it (missingType.iterableValue).
func PrintQualifiedValues(t Type) string {
	var b strings.Builder
	(&printer{b: &b, qualified: true, values: true}).print(forPHPStan(t), false)
	return b.String()
}

// PrintQualifiedMember is PrintQualified for a type that is written among
// the members of a union, or of an intersection: the text an alias expands
// to where the source wrote `Alias|null` or `A&Alias`. What would merge
// with its neighbours is bracketed: an intersection in a union
// (`(\A&\B)|null`), a union in an intersection, a callable's signature in
// either. A union stays bare among the members of a union, and an
// intersection among those of an intersection.
func PrintQualifiedMember(t Type, intersection bool) string {
	t = forPHPStan(t)
	nested := true
	switch t.(type) {
	case Union:
		nested = intersection
	case Intersection:
		nested = !intersection
	}
	var b strings.Builder
	(&printer{b: &b, qualified: true}).print(t, nested)
	return b.String()
}

// PrintQualifiedNullable is PrintQualified for the nullable of t, the text
// an alias expands to where the source wrote `?Alias`: `?` before a type
// that takes it (`?array{id: int}`, `?\App\Row`), and the union with null
// where `?` would bind to less than the type or meet another `?`: a
// union (`int|string|null`), a nullable type (`int|null`), an
// intersection (`(\A&\B)|null`), a callable's signature
// (`(\Closure(int): int)|null`).
func PrintQualifiedNullable(t Type) string {
	switch v := t.(type) {
	case Union, Intersection, Null, Mixed:
	case Callable:
		if !v.HasSig {
			return "?" + PrintQualified(t)
		}
	default:
		return "?" + PrintQualified(t)
	}
	return PrintQualified(Nullable(t))
}

// forPHPStan rewrites what PHPStan has no type for: bytes is string.
func forPHPStan(t Type) Type {
	return MapDeep(t, func(m Type) Type {
		switch v := m.(type) {
		case Bytes:
			return TString
		case Intersection:
			// string&bytes, text known to be both, is string twice now.
			var kept []Type
			text := false
			for _, member := range v.Types {
				if member == TString {
					if text {
						continue
					}
					text = true
				}
				kept = append(kept, member)
			}
			if len(kept) == 1 {
				return kept[0]
			}
			v.Types = kept
			return v
		}
		return m
	})
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
	values    bool // PrintQualifiedValues: no iterable type without its value type
	message   bool // Describe: a long literal cut
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
		v := t.V
		if pr.message {
			v = cutLiteral(v, describeLimit)
		}
		b.WriteString(quoteString(v))
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
			if !IsMixed(t.Value) || pr.values {
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
			if !keyIsAny || !IsMixed(t.Value) || pr.values {
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
		if pr.qualified && !t.List && t.Unsealed && len(t.Entries) == 0 {
			// `array{...}` names no key and admits every one: any array.
			// PHPStan reads the shape so and then asks for the value type
			// it does not state (missingType.iterableValue); array<mixed>
			// is the same type with it stated.
			b.WriteString("array<mixed>")
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
			if !t.List || e.Optional { // list{int, 1?: string}
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
		// A signature's return type takes what follows it. In the text
		// PHPStan reads, a callable among the members of a union or an
		// intersection stands in parentheses, `(\Closure(int): int)|null`,
		// and so does a return type that is one.
		if nested && t.HasSig && pr.qualified {
			b.WriteString("(")
			defer b.WriteString(")")
		}
		if t.Pure {
			b.WriteString("pure-")
		}
		if t.Closure {
			// Closure is a class: where class names are qualified it is
			// \Closure, which a file of any namespace reads as PHP's.
			// pure-Closure is PHPStan's word for one, and no name.
			if pr.qualified && !t.Pure {
				b.WriteByte('\\')
			}
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
				// `Closure(U): (A|B)`, not `(Closure(U): A)|B`.
				pr.print(t.Return, pr.qualified)
			}
		}
	case Iterable:
		b.WriteString("iterable")
		keyIsAny := IsMixed(t.Key)
		if !keyIsAny || !IsMixed(t.Value) || pr.values {
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

// quoteString writes a string literal's value as PHPStan describes it
// (ConstantStringType::export): in single quotes with \ and ' escaped, or,
// for a value that holds a control character, in double quotes as
// addcslashes() escapes them, \n, \t, … and the octal of the others, with
// \ and " escaped. A docblock reads either back to the value, as PHPStan
// and the checker read a docblock's string, but for \a and \b, which
// neither decodes.
func quoteString(s string) string {
	control := false
	for i := 0; i < len(s) && !control; i++ {
		control = s[i] < 0x20
	}
	if !control {
		return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\a':
			b.WriteString(`\a`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\v':
			b.WriteString(`\v`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			if c < 0x20 {
				fmt.Fprintf(&b, `\%03o`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
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
			if _, intersection := m.(Intersection); intersection {
				s = "(" + s + ")" // PHP's DNF: an intersection in a union is bracketed
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
	case Callable:
		if t.Closure {
			return "\\Closure", true // a class, named as PHP knows it from any namespace
		}
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
