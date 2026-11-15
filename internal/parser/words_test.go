package parser

import (
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

// TestWords holds the parser to design/roadmap.md D30: a word Phaenon adds,
// written in another letter case where only the word can stand, is read as
// the word and found, spanning it as written; a name of that spelling is
// none, by the position rules that decided it before.
func TestWords(t *testing.T) {
	cases := []struct {
		src  string
		want string // the words found, as written, joined by a space
	}{
		// `type` before an alias, and names spelled so.
		{"type Row = int;", ""},
		{"TYPE Row = int;", "TYPE"},
		{"Type Row<T OF \\Countable> = list<T>;", "Type OF"},
		{"/** A row. */\nTYPE Row = int;", "TYPE"},
		{"Type::make();\ntype();\necho TYPE;\nclass C { Type(): int => 1; const TYPE = 1; }", ""},
		// `of`, `in` and `out`, and type parameters named so.
		{"class Box<T Of X, in U, OUT V> {}", "Of OUT"},
		{"interface I<In T, Out U> {}", "In Out"},
		{"interface I<In, Out, Of of X, IN of X> {}", ""},
		{"interface I<IN Of X> {}", "Of"}, // a parameter named IN, bounded by `Of`
		{"function f<T OF X>(T $x): T => $x;", "OF"},
		{"$f = fn<T OF X>(T $x): T => $x;", "OF"},
		// `throws` and `nothrow` after a signature, and names spelled so.
		{"function f(): int THROWS X { return 1; }", "THROWS"},
		{"class C { f() Throws A|B {} g(): int NoThrow => 1; abstract h(): void\n    NOTHROW; }", "Throws NoThrow NOTHROW"},
		{"function f(): int throwſ X { return 1; }", "throwſ"}, // folds to `throws`
		{"class C { f(): Throws => new Throws(); nothrow(): int => 1; throws(): Throws => Throws(); }", ""},
		{"class C { f(): Throws throws Throws => throw new Throws(); }", ""},
		{"const NOTHROW = 1;\nfunction nothrow(): int { return NOTHROW; }", ""},
		{"enum E { case Type; case Throws; case Nothrow; case In; case Of; }", ""},
		// `class` and `list` in a type position.
		{"function f(List<int> $a, Class<X> $b, CLASS $c, list<int> $d, class<X> $e): ?LIST<int> {}", "List Class CLASS LIST"},
		{"class C { private LIST<int> $xs = []; }", "LIST"},
		{"type Pairs = List<(int, string)>;", "List"},
		{"function f(Closure(List<int>): int $g) {}", "List"},
		{"wrap<List<int>>([]);", "List"},
		{"list($a, $b) = [1, 2];\n[$a] = LIST(1);", ""},
		// A qualified name is never the word: `\Acme\List<int>` names a
		// class, which the checker resolves.
		{"function f(\\Acme\\List<int> $a, Acme\\Class $b, namespace\\LIST $c) {}", ""},
		{"$x = new class {};\n$y = Foo::class;", ""},
		// The cast `(bytes)`, which PHP's own casts are not.
		{"$b = (bytes) $s;", ""},
		{"$b = (Bytes) $s;\n$c = ( BYTES ) $s;\n$d = (INT) $s;", "(Bytes) ( BYTES )"},
		// `Map` and `Bytes` are class names: the checker resolves them.
		{"function f(Map<string, int> $m, Bytes $b): MAP {}", ""},
		// PHP's own words keep PHP's rule.
		{"FUNCTION f(Int $a, ARRAY $b, ?Self $c): MIXED { RETURN $a; }", ""},
		// A withdrawn test is reported whole, its type with it.
		{"$y = $x IS List;", ""},
	}
	for _, c := range cases {
		f, err := Parse([]byte(c.src), lexer.ModePhaenon)
		if err != nil {
			t.Errorf("%q: %v", c.src, err)
			continue
		}
		var got []string
		for _, w := range Words(f, []byte(c.src)) {
			if text := c.src[w.S.Start:w.S.End]; text != w.Text {
				t.Errorf("%q: the span of %q holds %q", c.src, w.Text, text)
			}
			if strings.ToLower(w.Want) != w.Want || !strings.EqualFold(strings.ReplaceAll(w.Text, "ſ", "s"), w.Want) {
				t.Errorf("%q: %q is spelled %q", c.src, w.Text, w.Want)
			}
			got = append(got, w.Text)
		}
		if g := strings.Join(got, " "); g != c.want {
			t.Errorf("%q: words %q, want %q", c.src, g, c.want)
		}
	}
}
