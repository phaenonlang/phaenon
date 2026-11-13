package docblock

import (
	"fmt"
	"strings"
	"testing"
)

// TestMethodTagParams: the parameter list of an @method tag ends at its own
// closing parenthesis, and a default value is skipped as PHP reads it: a
// comma or a parenthesis inside a string is text, inside brackets it
// belongs to the value. A by-reference variadic is written `&...$name`.
func TestMethodTagParams(t *testing.T) {
	cases := []struct {
		tag  string
		want string // name(type $name, …) with `=` for a default, `...` and `&` as written
	}{
		{`void foo(string $s = 'a, b)', int $n = 1)`, `foo(string $s =, int $n =)`},
		{`void foo(string $s = "a, (b", int $n = 1)`, `foo(string $s =, int $n =)`},
		{`void foo(string $s = 'it\'s, so', int $n = 1)`, `foo(string $s =, int $n =)`},
		{`void foo(string $s = '\\', int $n = 1)`, `foo(string $s =, int $n =)`},
		{`void foo(array $a = ['x' => ')', 'y' => [1, 2]], int $n = 1)`, `foo(array $a =, int $n =)`},
		{`void foo(array $a = array(1, 2), int $n = PHP_INT_MAX)`, `foo(array $a =, int $n =)`},
		{`void foo(?Foo $f = new Foo(1, 'a)'), int $n = 1)`, `foo(?Foo $f =, int $n =)`},
		{`int count() the number of items (cached)`, `count()`},
		{`int count(string $s = ')') the number of items (cached, mostly)`, `count(string $s =)`},
		{`void bar(int &...$xs)`, `bar(int &...$xs)`},
		{`void bar(int ...$xs)`, `bar(int ...$xs)`},
		{`void bar(array &$x, callable(int, string): bool $f)`, `bar(array &$x, callable(int, string): bool $f)`},
		{`static Foo make(int $a, string ...$b)`, `make(int $a, string ...$b)`},
		{`foo($a, $b = 1)`, `foo($a, $b =)`},
	}
	for _, c := range cases {
		m, err := parseMethodTag(c.tag)
		if err != nil {
			t.Errorf("@method %s: %v", c.tag, err)
			continue
		}
		var params []string
		for _, p := range m.Params {
			s := ""
			if p.Type != nil {
				s = p.Type.String() + " "
			}
			if p.ByRef {
				s += "&"
			}
			if p.Variadic {
				s += "..."
			}
			s += "$" + p.Name
			if p.Optional {
				s += " ="
			}
			params = append(params, s)
		}
		if got := fmt.Sprintf("%s(%s)", m.Name, strings.Join(params, ", ")); got != c.want {
			t.Errorf("@method %s\n got %s\nwant %s", c.tag, got, c.want)
		}
	}
	if _, err := parseMethodTag(`void foo(string $s = 'a`); err == nil {
		t.Error("an unclosed parameter list is an error")
	}
}
