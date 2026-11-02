package printer

import (
	"os"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// TestFormat pins the canonical layout: PSR-12 braces, one statement per
// line, blank lines around declarations, heredocs and inline HTML verbatim.
// Regenerate the golden file with `phn print --php testdata/sample.php`
// after an intentional formatting change.
func TestFormat(t *testing.T) {
	src, err := os.ReadFile("testdata/sample.php")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/sample.golden")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.Parse(src, lexer.ModePHP)
	if err != nil {
		t.Fatal(err)
	}
	if got := File(f); got != string(want) {
		t.Errorf("formatting changed:\n--- got\n%s\n--- want\n%s", got, want)
	}
}

// TestExprSpacing covers spellings where whitespace is semantic or where
// the tree must reproduce a specific form.
func TestExprSpacing(t *testing.T) {
	cases := map[string]string{
		`-(-$x)`:         `-(-$x)`,
		`- -$x`:          `- -$x`,
		`-$x--`:          `-$x--`,
		`- --$x`:         `- --$x`,
		`$a - -$b`:       `$a - -$b`,
		`1 . 2`:          `1 . 2`,
		`(int) $x`:       `(int) $x`,
		`$a ?: $b`:       `$a ?: $b`,
		`$f(...)`:        `$f(...)`,
		`f(?, 1)`:        `f(?, 1)`,
		`f(1, ...)`:      `f(1, ...)`,
		`f(a: ?, ...)`:   `f(a: ?, ...)`,
		`f(...,)`:        `f(...)`,
		`$x?->y::z()`:    `$x?->y::z()`,
		`new ($c)(1)`:    `new ($c)(1)`,
		`$a->{$b}`:       `$a->{$b}`,
		`A::{$b}()`:      `A::{$b}()`,
		`"${ FOO}"`:      `"${ FOO}"`,
		`"${FOO}"`:       `"${FOO}"`,
		`"$a[-1] $b[c]"`: `"$a[-1] $b[c]"`,
		`clone($x, [1])`: `clone($x, [1])`,
		`yield $k => $v`: `yield $k => $v`,
		`[, $b] = $x`:    `[, $b] = $x`,
	}
	for in, want := range cases {
		e, err := parser.ParseExpr([]byte(in), lexer.ModePhaenon)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got := Expr(e); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

// TestFunctionSyntax pins the printing of expression bodies and `throws`
// (design/functions.md §3–4): the declaration keeps its arrow and its
// clause, and printing the printed text again changes nothing.
func TestFunctionSyntax(t *testing.T) {
	src := "final class C\n{\n    fun name(): string\n        => $this->name;\n    abstract protected fun run(): void throws A|\\B\\C;\n    fun log(string $m): void => print $m;\n}\n\nfun f(): int throws X => 1;\n"
	want := "final class C\n{\n    fun name(): string => $this->name;\n\n    abstract protected fun run(): void throws A|\\B\\C;\n\n    fun log(string $m): void => print $m;\n}\n\nfun f(): int throws X => 1;\n"
	f, err := parser.Parse([]byte(src), lexer.ModePhaenon)
	if err != nil {
		t.Fatal(err)
	}
	got := File(f)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	again, err := parser.Parse([]byte(got), lexer.ModePhaenon)
	if err != nil {
		t.Fatal(err)
	}
	if File(again) != got {
		t.Errorf("printing the printed text changed it:\n%s", File(again))
	}
}
