package parser

import (
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
)

// TestMemberByPosition: in a class body, after the attributes and
// modifiers, a name followed by `(`, or by a balanced `<…>` and then `(`,
// is a method declared without a keyword (roadmap D27); anything else
// begins a property, a constant, a case or a trait use
// (design/functions.md §1.1).
func TestMemberByPosition(t *testing.T) {
	cases := []struct {
		member string
		want   string // method name, "property", "const", "case", "use"
	}{
		{"title(): string => 'x';", "title"},
		{"protected static create(): static => new static();", "create"},
		{"final public &cached(): array { return []; }", "cached"},
		{"abstract render(): string;", "render"},
		{"first<T>(list<T> $xs): ?T => null;", "first"},
		{"map<K, V of list<int>>(K $k): V;", "map"},
		{"#[A] /** d */ list(): array => [];", "list"},
		{"use(int $x): void {}", "use"},
		{"case(): void {}", "case"},
		{"use<T>(T $x): T => $x;", "use"},
		{"case<T of object>(T $x): T => $x;", "case"},
		{"new(): static => new static();", "new"},
		{"closure(): int => 1;", "closure"},
		{"fun(): int => 1;", "fun"},
		{"public function static(): void {}", "static"},
		{"public function function(): void {}", "function"},
		{"final function var(): void {}", "var"},
		{"function Closure(): void {}", "Closure"},
		{"public static (A&B)|null $x = null;", "property"},
		{"var (A&B)|null $y = null;", "property"},
		{"private Closure(int): string $f;", "property"},
		{"list<int> $xs = [];", "property"},
		{"map<string, int>|null $m = null;", "property"},
		{"class<Foo> $c;", "property"},
		{"private (int, string) $pair;", "property"},
		{"public string $s { get => 'x'; set(string $v) { } }", "property"},
		{"const (A&B)|null C = null;", "const"},
		{"case Hearts;", "case"},
		{"use Greets;", "use"},
	}
	for _, c := range cases {
		src := "enum E {\n    " + c.member + "\n}\n"
		f, err := Parse([]byte(src), lexer.ModePhaenon)
		if err != nil {
			t.Errorf("%s: %v", c.member, err)
			continue
		}
		var got string
		switch m := f.Stmts[0].(*ast.ClassDecl).Members[0].(type) {
		case *ast.MethodDecl:
			got = m.Name.Name
		case *ast.PropertyDecl:
			got = "property"
		case *ast.ClassConstDecl:
			got = "const"
		case *ast.EnumCase:
			got = "case"
		case *ast.TraitUse:
			got = "use"
		}
		if got != c.want {
			t.Errorf("%s: read as %q, want %q", c.member, got, c.want)
		}
	}
}

// TestWithdrawnFun: `fun` is a name (roadmap D27), and a declaration still
// spelled with it parses as the function or method it declared, marked
// Fun, for the checker to name (P1001) and phn fmt to respell. At
// statement level only a declaration's shape is one.
func TestWithdrawnFun(t *testing.T) {
	for src, want := range map[string]int{
		"fun f(): int { return 1; }":                       1,
		"fun &f(): array { return []; }":                   1,
		"fun f() throws X {}":                              1,
		"fun f(): int => 1;":                               1,
		"class A { fun f() {} static fun g() {} }":         2,
		"class A { fun static() {} fun list() {} }":        2,
		"class A { fun() {} }":                             0,
		"function g() { fun inner() {} }":                  1,
		"fun();":                                           0,
		"fun & f();":                                       0,
		"$x = fun;":                                        0,
		"function fun(): int { return fun(); }":            0,
		"$y = $x is Foo; $z = $x as int; $w = $x as? int;": 0,
	} {
		f, err := Parse([]byte(src), lexer.ModePhaenon)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		got := 0
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FunctionDecl:
				if n.Fun {
					got++
				}
			case *ast.MethodDecl:
				if n.Fun {
					got++
				}
			}
			return true
		})
		if got != want {
			t.Errorf("%s: %d declarations spelled fun, want %d", src, got, want)
		}
	}
}

// TestWithdrawnTests: after an operand, `is` and a type, `as` and `as?`
// parse as the withdrawn tests (roadmap D26); `is` is a name everywhere
// else.
func TestWithdrawnTests(t *testing.T) {
	for src, want := range map[string]int{
		"$y = $x is Foo;":                         1,
		"$y = $x is ?int || $x is (int, string);": 2,
		"$y = $x as int; $z = $x as? Foo;":        2,
		"foreach ($xs as $x) {}":                  0,
		"is($a); $o->is(); Foo::is();":            0,
		"function is(): bool { return true; }":    0,
		"const is = 1;":                           0,
	} {
		f, err := Parse([]byte(src), lexer.ModePhaenon)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		got := 0
		ast.Inspect(f, func(n ast.Node) bool {
			switch n.(type) {
			case *ast.IsExpr, *ast.AsExpr:
				got++
			}
			return true
		})
		if got != want {
			t.Errorf("%s: %d withdrawn tests, want %d", src, got, want)
		}
	}
}
