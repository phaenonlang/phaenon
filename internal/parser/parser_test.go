package parser

import (
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
)

// placeholders describes an argument list as a string: one letter per
// argument — v value, u unpacking, ? placeholder, . variadic placeholder —
// with n: before a named one; "fcc" for f(...).
func placeholders(a *ast.ArgList) string {
	if a.FirstClassCallable {
		return "fcc"
	}
	s := ""
	for _, arg := range a.Args {
		if arg.Name != nil {
			s += arg.Name.Name + ":"
		}
		switch {
		case arg.Placeholder != nil && arg.Placeholder.Variadic:
			s += "."
		case arg.Placeholder != nil:
			s += "?"
		case arg.Unpack:
			s += "u"
		default:
			s += "v"
		}
	}
	return s
}

func argsOf(t *testing.T, e ast.Expr) *ast.ArgList {
	t.Helper()
	switch e := e.(type) {
	case *ast.Call:
		return e.Args
	case *ast.MethodCall:
		return e.Args
	case *ast.StaticCall:
		return e.Args
	case *ast.New:
		return e.Args
	case *ast.Clone:
		return e.Args
	case *ast.Binary:
		return argsOf(t, e.Right) // the right side of a pipe
	}
	t.Fatalf("%T has no argument list", e)
	return nil
}

// TestAsNullable: `as?` is the operator only when its `?` touches `as`.
// With anything between them the `?` begins the type, so `$x as ?int` is
// `as` with the nullable type ?int, throwing on a string, where `$x as?
// int` gives null (design/generics.md §11.4).
func TestAsNullable(t *testing.T) {
	for in, want := range map[string]string{
		`$x as? int`:      "as? int",
		`$x as?int`:       "as? int",
		`$x as? ?int`:     "as? ?int",
		`$x as ?int`:      "as ?int",
		`$x as ? int`:     "as ?int",
		`$x as/**/?int`:   "as ?int",
		"$x as\n    ?int": "as ?int",
		`$x as int`:       "as int",
	} {
		e, err := ParseExpr([]byte(in), lexer.ModePhaenon)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		as, ok := e.(*ast.AsExpr)
		if !ok {
			t.Errorf("%s: parsed as %T", in, e)
			continue
		}
		got := "as "
		if as.Nullable {
			got = "as? "
		}
		if _, ok := as.Type.(*ast.NullableType); ok {
			got += "?"
		}
		got += "int"
		if got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

// TestPartialApplication covers PHP 8.6's placeholders (argument_no_expr in
// zend_language_parser.y) beside Phaenon's own uses of `?`, `...` and `<…>`.
func TestPartialApplication(t *testing.T) {
	cases := map[string]string{
		`f(?)`:                     "?",
		`f(?, 1)`:                  "?v",
		`f(1, ?)`:                  "v?",
		`f(1, ...)`:                "v.",
		`f(?, ...)`:                "?.",
		`f(a: ?)`:                  "a:?",
		`f(1, b: ?, ...)`:          "vb:?.",
		`f(a: 1, ...)`:             "a:v.",
		`f(...)`:                   "fcc",
		`f(...,)`:                  "fcc",
		`f(?,)`:                    "?",
		`f(...$xs)`:                "u",
		`f(1, ...$xs)`:             "vu",
		`f(..., 1)`:                ".v", // a compile error, reported by the checker
		`$o->m(?, 2)`:              "?v",
		`$o?->m(?)`:                "?", // a compile error, reported by the checker
		`C::m(?)`:                  "?",
		`$f(?)`:                    "?",
		`new Foo(?)`:               "?", // a compile error, reported by the checker
		`clone(?)`:                 "?",
		`clone($x, ...)`:           "v.",
		`$x |> f(?, 1)`:            "?v",
		`f($a ? $b : $c, ?)`:       "v?",
		`f($a ?: $b, ?)`:           "v?",
		`f($a ?? $b, ?)`:           "v?",
		`f(fn (?int $x) => $x, ?)`: "v?",
		`f(fn (?(int, string) $t): ?int => null, ?)`: "v?",
		`f($x is ?Foo, ?)`:                           "v?",
		`f($x as ?Foo, ?)`:                           "v?",
		`f<int>(?)`:                                  "?",
		`f<?int>(?, ...)`:                            "?.",
		`$o->m<Closure(?int): ?string>(?)`:           "?",
		`new Box<?int>(1)`:                           "v",
		`f(x: ?, y: 2)`:                              "x:?y:v",
	}
	for in, want := range cases {
		e, err := ParseExpr([]byte(in), lexer.ModePhaenon)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got := placeholders(argsOf(t, e)); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{`f(? 1)`, `f(?x)`, `f(a: ? 1)`, `f(? ...)`} {
		if _, err := ParseExpr([]byte(bad), lexer.ModePhaenon); err == nil {
			t.Errorf("%s: parsed, want a syntax error", bad)
		}
	}
}

// TestPartialApplicationInDeclarations covers placeholders in constant
// expressions, which PHP 8.6 admits as partial applications.
func TestPartialApplicationInDeclarations(t *testing.T) {
	src := `
const F = str_replace(' ', '-', ?);
#[Attr(strlen(...), cb: substr(?, 1))]
function f(Closure $g = strtoupper(?)): ?int {
    return null;
}
class C {
    const G = self::h(?, 2);
    public function __construct(private Closure $k = C::h(...)) {}
    static h(int $a, int $b): int { return $a + $b; }
}
`
	if _, err := Parse([]byte(src), lexer.ModePhaenon); err != nil {
		t.Fatal(err)
	}
}

// TestConstantObjectWrite covers PHP 8.6's writes through a constant's
// object, which the grammar always admitted (the 8.5 compiler refused them).
func TestConstantObjectWrite(t *testing.T) {
	src := "<?php\nC->x = 1;\nC->y->z ??= 2;\nC[0]->p++;\nK::O->q .= 'a';\nunset(C->x);\n"
	if _, err := Parse([]byte(src), lexer.ModePHP); err != nil {
		t.Fatal(err)
	}
}

// TestErrorSpans: a syntax error carries the span of the token the parser
// stopped at, so that a caret can underline it; PHP's tags, which a .phn
// never holds, are named.
func TestErrorSpans(t *testing.T) {
	cases := []struct {
		src, at, msg string
	}{
		{"function f(): int {\n    return 1 +;\n}\n", ";", `unexpected token ";"`},
		{"<?php\nfunction f(): int => 1;\n", "<?php", "do not start with <?php"},
		{"function f(): int => 1;\n?>\n<p>html</p>\n", "?>", "no closing tag ?>"},
		{"function f(): int {\n    return 1;\n?>\n", "?>", "no closing tag ?>"},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.src), lexer.ModePhaenon)
		pe, ok := err.(*Error)
		if !ok {
			t.Errorf("%q: want a syntax error, got %v", c.src, err)
			continue
		}
		if got := c.src[pe.Offset:pe.End]; got != c.at || !strings.Contains(pe.Msg, c.msg) {
			t.Errorf("%q: error at %q (%s), want at %q containing %q", c.src, got, pe.Msg, c.at, c.msg)
		}
	}
	// In PHP mode `?>` is the closing tag it is.
	if _, err := Parse([]byte("<?php\necho 1;\n?>\n<p>html</p>\n"), lexer.ModePHP); err != nil {
		t.Errorf("PHP mode: %v", err)
	}
	// The end of a file that ends with line breaks lies past its last line:
	// an error there is put just after the last text, on the line it ends.
	for _, src := range []string{"function g(): int {\n    return 1;\n", "function g(): int {\r\n    return 1;\r\n\r\n", "function g(): int {\r    return 1;\r"} {
		_, err := Parse([]byte(src), lexer.ModePhaenon)
		pe, ok := err.(*Error)
		if !ok || pe.Line != 2 || src[:pe.Offset] != strings.TrimRight(src, "\r\n") || pe.End != pe.Offset {
			t.Errorf("%q: %#v, want the end of `return 1;` on line 2", src, err)
		}
	}
}
