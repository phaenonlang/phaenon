package checker

import (
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

// TestMissingOffsetOnce: a key no value of the type has is P2313 once, on
// the arrays that lack it, however many shapes the array may be; one
// mistake, one finding. The golden corpus holds lines and codes, not how
// often a code is reported on a line, so the count is held here.
func TestMissingOffsetOnce(t *testing.T) {
	src := `/** @param list<bool> $c */
function f(array $c): int
{
    $o = ['id' => 0];
    if ($c[1] ?? false) { $o['a1'] = 1; } else { $o['b1'] = 'x'; }
    if ($c[2] ?? false) { $o['a2'] = 2; } else { $o['b2'] = 'x'; }
    if ($c[3] ?? false) { $o['a3'] = 3; } else { $o['b3'] = 'x'; }
    return $o['nope'];
}

/**
 * @param array{a: int}|array{b: int}|array<int, int> $r
 */
function g(array $r): int
{
    return $r['z'];
}
`
	p := newProgram(t)
	if _, err := p.AddFile("once.phn", []byte(src), lexer.ModePhaenon, true); err != nil {
		t.Fatal(err)
	}
	perLine := map[int][]string{}
	for _, d := range New(p, Options{}).Check() {
		if d.Code == "P2313" {
			perLine[d.Line] = append(perLine[d.Line], d.Message)
		}
	}
	for _, want := range []struct {
		line int
		on   string
	}{
		{8, "array{id: 0, a1: 1, a2: 2, a3: 3}|"},
		{16, "array{a: int}|array{b: int}|array<int, int>"},
	} {
		msgs := perLine[want.line]
		if len(msgs) != 1 {
			t.Errorf("line %d: %d P2313, want 1: %v", want.line, len(msgs), msgs)
			continue
		}
		if !strings.Contains(msgs[0], want.on) {
			t.Errorf("line %d: %q does not name %s", want.line, msgs[0], want.on)
		}
	}
}
