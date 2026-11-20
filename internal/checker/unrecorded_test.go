package checker

import (
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/phpversion"
)

// TestUnrecordedPartialRefused: a partial application whose callee the
// checker cannot type has no record to write the closure for PHP 8.5
// from, and the build refuses it; phn check refuses it too, at its
// place, and the build gives the checker's reason. Both passed and failed
// apart: check said nothing, and the build said "or the checker's types
// to be lowered".
func TestUnrecordedPartialRefused(t *testing.T) {
	forms := map[string]string{
		"callable":       "$c('c', ...)",
		"Closure":        "$cl('cl', ...)",
		"placeholder":    "$c('c', ?)",
		"class string":   "$name::sm('named', ...)",
		"method name":    "$obj->$method('dyn', ...)",
		"function name":  "'strtoupper'(?)",
		"method array":   "[$obj, 'sm']('a', ...)",
		"pipe of a call": "'x' |> $cl('a', ?, ?)",
	}
	for name, form := range forms {
		t.Run(name, func(t *testing.T) {
			src := []byte("final class Base {\n    static sm(string $a, string $z = 'z'): string => $a . $z;\n}\n\nfunction forms(callable $c, Closure $cl, string $name, Base $obj, string $method): mixed {\n    return " + form + ";\n}\n")
			p := newProgram(t)
			p.SetTarget(phpversion.PHP85)
			f, err := p.AddFile("A.phn", src, lexer.ModePhaenon, true)
			if err != nil {
				t.Fatal(err)
			}
			var refused []string
			for _, d := range New(p, Options{}).Check() {
				if d.Severity != Error {
					continue
				}
				if d.Code != P1002.ID || d.Line != 6 {
					t.Errorf("%d: %s: %s", d.Line, d.Code, d.Message)
					continue
				}
				refused = append(refused, d.Message)
			}
			if len(refused) != 1 {
				t.Fatalf("phn check refuses %d times: %v", len(refused), refused)
			}
			_, err = emit.File(src, emit.Options{SourceName: "A.phn", StrictTypes: true, Program: p, File: f})
			if err == nil {
				t.Fatalf("phn build writes it")
			}
			why := strings.TrimSuffix(refused[0], ", or target PHP 8.6")
			if !strings.HasSuffix(err.Error(), why) {
				t.Errorf("phn build: %v\nphn check: %s", err, refused[0])
			}
		})
	}
}

// TestUntypedStageLowered: a pipe's stage through a value the checker
// cannot type is the call PHP makes of it, `$cl('x')`, and where its run
// does not move, a closure that passes the piped value on; neither is
// refused.
func TestUntypedStageLowered(t *testing.T) {
	src := []byte("function stages(Closure $cl): string {\n    $a = 'x' |> $cl(?, 'b');\n    $b = 'x'\n        |> $cl(?) // a comment keeps the pipe\n        |> gettype(...);\n    return gettype($a) . $b;\n}\n")
	p := newProgram(t)
	p.SetTarget(phpversion.PHP85)
	f, err := p.AddFile("A.phn", src, lexer.ModePhaenon, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range New(p, Options{}).Check() {
		if d.Severity == Error {
			t.Errorf("%d: %s: %s", d.Line, d.Code, d.Message)
		}
	}
	out, err := emit.File(src, emit.Options{SourceName: "A.phn", StrictTypes: true, Program: p, File: f})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"$a = $cl('x', 'b');", "mixed $piped) => $cl($piped)) // a comment keeps the pipe"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
