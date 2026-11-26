package checker

import (
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

// TestRewrittenNamesTheWrite: a value of a type parameter held to that
// type after a write made it the array written is a finding with the
// write as a related position, which phn lsp shows beside it; the golden
// corpus holds the messages (bounded-writes.phn), not the positions. The
// write named is the one every way to the return passes, where there is
// one; an argument's finding names the parameter's declaration too, and
// a closure the bound of the function it is written in.
func TestRewrittenNamesTheWrite(t *testing.T) {
	src := `namespace App;

type AnB = array<string, int>;

function withFlag<T of AnB>(T $a, bool $c): T
{
    if ($c) {
        $a['x'] = 1;
    }
    $a['y'] = 2;

    return $a;
}

final class Sink<T of AnB>
{
    take(T $t): void {}

    pass(T $a): void {
        $a['x'] = 1;
        $this->take($a);
    }
}

function outer<T of AnB>(T $a): \Closure
{
    return function (T $b): T {
        $b['x'] = 1;

        return $b;
    };
}
`
	p := newProgram(t)
	if _, err := p.AddFile("rewritten.phn", []byte(src), lexer.ModePhaenon, true); err != nil {
		t.Fatal(err)
	}
	byLine := map[int]Diagnostic{}
	for _, d := range New(p, Options{}).Check() {
		if _, seen := byLine[d.Line]; seen {
			t.Errorf("line %d: a second finding, %s", d.Line, d)
		}
		byLine[d.Line] = d
	}
	if len(byLine) != 3 {
		t.Fatalf("%d findings, want 3: %v", len(byLine), byLine)
	}
	for _, want := range []struct {
		line    int
		code    string
		message string
		related []Related
	}{
		{12, "P2202", "function App\\withFlag() should return T but returns non-empty-array<string, int>: $a is no longer T after it was written on line 10, " +
			"since the caller's T may be narrower than AnB; declare the return type as AnB",
			[]Related{{File: "rewritten.phn", Line: 10, Col: 5, EndLine: 10, EndCol: 12, Message: "written here"}}},
		{21, "P2103", "argument 1 ($t) of App\\Sink::take() expects T, non-empty-array<string, int> given: $a is no longer T after it was written on line 20, " +
			"since the caller's T may be narrower than AnB",
			[]Related{{File: "rewritten.phn", Line: 17, Col: 10, EndLine: 17, EndCol: 14, Message: "parameter $t of App\\Sink::take() declared here"},
				{File: "rewritten.phn", Line: 20, Col: 9, EndLine: 20, EndCol: 16, Message: "written here"}}},
		// A closure names the bound of the function it is written in.
		{30, "P2202", "closure should return T but returns non-empty-array<string, int>: $b is no longer T after it was written on line 28, " +
			"since the caller's T may be narrower than AnB; declare the return type as AnB",
			[]Related{{File: "rewritten.phn", Line: 28, Col: 9, EndLine: 28, EndCol: 16, Message: "written here"}}},
	} {
		d, ok := byLine[want.line]
		if !ok || d.Code != want.code || d.Message != want.message {
			t.Errorf("line %d: %s %q, want %s %q", want.line, d.Code, d.Message, want.code, want.message)
			continue
		}
		if len(d.Related) != len(want.related) {
			t.Errorf("line %d: related %+v, want %+v", want.line, d.Related, want.related)
			continue
		}
		for i, r := range want.related {
			if got := d.Related[i]; got != r {
				t.Errorf("line %d: related %d is %+v, want %+v", want.line, i, d.Related[i], r)
			}
		}
	}
}
