package checker

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/phpversion"
)

// TestComparisonMirrored: a comparison narrows alike whichever side each
// operand is written on, and no operand shape makes the checker panic.
// Every operand pair is checked as `A op B` and as `B op' A`, op' the
// mirrored operator, each in a function whose branches show what the
// condition left of $a through current() (the first element of a
// non-empty list, false for an empty array); the diagnostics of the two
// spellings must be the same, line for line. The operands are count()'s
// spellings (\count, sizeof, named and spread arguments, a first-class
// callable, partial applications), calls that are not count() (a method,
// a static method), and plain ints.
func TestComparisonMirrored(t *testing.T) {
	calls := []string{
		`count($a)`, `\count($a)`, `sizeof($a)`, `\sizeof($a)`, `(count($a))`,
		`count($a, COUNT_RECURSIVE)`, `count(value: $a)`, `count(value: $a, mode: 0)`,
		`count(mode: 0, value: $a)`, `count(...$l)`, `count(...)`, `count(?)`,
		`count($a, ?)`, `count(value: ?)`, `$box->count()`, `$box?->count()`,
		`Box::countOf($a)`, `$n`, `count($a) + 0`, `[count($a)][0]`,
	}
	others := []string{`0`, `1`, `2`, `5`, `0x0`, `1_000`, `$n`, `count($a)`, `null`}
	mirror := map[string]string{
		"<": ">", ">": "<", "<=": ">=", ">=": "<=",
		"===": "===", "!==": "!==", "==": "==", "!=": "!=",
	}
	ops := []string{"<", ">", "<=", ">=", "===", "!==", "==", "!="}
	var written, mirrored []string
	for _, call := range calls {
		for _, other := range others {
			for _, op := range ops {
				written = append(written, fmt.Sprintf("%s %s %s", call, op, other))
				mirrored = append(mirrored, fmt.Sprintf("%s %s %s", other, mirror[op], call))
			}
		}
	}
	a := mirrorProbe(t, written)
	b := mirrorProbe(t, mirrored)
	for i := range written {
		if !slices.Equal(a[i], b[i]) {
			t.Errorf("%s and %s narrow differently:\n  %s\n  %s", written[i], mirrored[i], strings.Join(a[i], "; "), strings.Join(b[i], "; "))
		}
	}
}

// TestCountNarrowing: what each branch of a count() comparison knows of
// the counted list, in both spellings: not empty where the count is at
// least 1, empty where it can only be 0, either where it may be both or
// where the comparison never holds. count() is int<0, max> to PHP, as
// PHPStan's function map has it, so a comparison its range decides is
// reported (P2402), and the branch it rules out with it (P2401).
func TestCountNarrowing(t *testing.T) {
	const (
		nonEmpty = "non-empty"
		empty    = "empty"
		either   = "either"
	)
	cases := []struct {
		cond, mirrored string
		then, els      string
		always         string // "true" or "false" where the range decides the comparison
	}{
		{"count($a) > 0", "0 < count($a)", nonEmpty, empty, ""},
		{"count($a) >= 1", "1 <= count($a)", nonEmpty, empty, ""},
		{"count($a) < 1", "1 > count($a)", empty, nonEmpty, ""},
		{"count($a) <= 0", "0 >= count($a)", empty, nonEmpty, ""},
		{"count($a) > 2", "2 < count($a)", nonEmpty, either, ""},
		{"count($a) >= 0", "0 <= count($a)", either, either, "true"},
		{"count($a) < 3", "3 > count($a)", either, nonEmpty, ""},
		{"count($a) <= 2", "2 >= count($a)", either, nonEmpty, ""},
		{"count($a) < 0", "0 > count($a)", either, either, "false"},
		{"count($a) === 0", "0 === count($a)", empty, nonEmpty, ""},
		{"count($a) !== 0", "0 !== count($a)", nonEmpty, empty, ""},
		{"count($a) === 2", "2 === count($a)", nonEmpty, either, ""},
		{"count($a) != 2", "2 != count($a)", either, nonEmpty, ""},
	}
	// The probe's lines: takesInt() and takesFalse() of current($a) in
	// the then branch, then in the else branch.
	fails := func(branch string, intLine, falseLine int) []string {
		switch branch {
		case nonEmpty:
			return []string{fmt.Sprintf("%d:P2103", falseLine)}
		case empty:
			return []string{fmt.Sprintf("%d:P2103", intLine)}
		}
		return []string{fmt.Sprintf("%d:P2103", intLine), fmt.Sprintf("%d:P2103", falseLine)}
	}
	var conds []string
	for _, c := range cases {
		conds = append(conds, c.cond, c.mirrored)
	}
	got := mirrorProbe(t, conds)
	for i, c := range cases {
		want := append(fails(c.then, 8, 9), fails(c.els, 11, 12)...)
		switch c.always {
		case "true":
			want = append(want, "7:P2402", "11:P2401")
		case "false":
			want = append(want, "7:P2402", "8:P2401")
		}
		slices.Sort(want)
		for j, cond := range []string{c.cond, c.mirrored} {
			if g := got[2*i+j]; !slices.Equal(g, want) {
				t.Errorf("%s: then %s, else %s: want %v, got %v", cond, c.then, c.els, want, g)
			}
		}
	}
}

const mirrorPrelude = `namespace Mirror;

final class Box implements \Countable
{
    public function count(): int
    {
        return 0;
    }

    /** @param list<int> $a */
    public static function countOf(array $a): int
    {
        return \count($a);
    }
}

function takesInt(int $i): void
{
}

function takesFalse(false $f): void
{
}
`

// mirrorProbeLines is the length of one probe function below.
const mirrorProbeLines = 15

const mirrorProbeFunc = `
/**
 * @param list<int> $a
 * @param list<list<int>> $l
 */
function probe%d(array $a, array $l, ?Box $box, int $n): void
{
    if (%s) {
        takesInt(current($a));
        takesFalse(current($a));
    } else {
        takesInt(current($a));
        takesFalse(current($a));
    }
}
`

// mirrorProbe checks each condition in a probe function of its own and
// returns each one's diagnostics as line-in-function:code, sorted. A
// condition that makes the checker panic fails the test, named.
func mirrorProbe(t *testing.T, conds []string) [][]string {
	t.Helper()
	var b strings.Builder
	b.WriteString(mirrorPrelude)
	for i, c := range conds {
		fmt.Fprintf(&b, mirrorProbeFunc, i, c)
	}
	first := strings.Count(mirrorPrelude, "\n") + 1 // the first probe's first line
	out := make([][]string, len(conds))
	ok := func(src string) (diags []Diagnostic, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		p := newProgram(t)
		p.SetTarget(phpversion.PHP86) // partial applications
		if _, err := p.AddFile("mirror.phn", []byte(src), lexer.ModePhaenon, true); err != nil {
			return nil, fmt.Errorf("parse: %v", err)
		}
		return New(p, Options{}).Check(), nil
	}
	diags, err := ok(b.String())
	if err != nil {
		// Name the conditions: check them in groups, and each of a group
		// that fails on its own.
		const group = 64
		for start := 0; start < len(conds); start += group {
			var g strings.Builder
			g.WriteString(mirrorPrelude)
			for i, c := range conds[start:min(start+group, len(conds))] {
				fmt.Fprintf(&g, mirrorProbeFunc, i, c)
			}
			if _, err := ok(g.String()); err == nil {
				continue
			}
			for _, c := range conds[start:min(start+group, len(conds))] {
				if _, err := ok(mirrorPrelude + fmt.Sprintf(mirrorProbeFunc, 0, c)); err != nil {
					t.Errorf("%s: %v", c, err)
				}
			}
		}
		t.FailNow()
	}
	for _, d := range diags {
		i := (d.Line - first) / mirrorProbeLines
		if i < 0 || i >= len(conds) {
			t.Fatalf("diagnostic outside the probes: %d: %s %s", d.Line, d.Code, d.Message)
		}
		out[i] = append(out[i], fmt.Sprintf("%d:%s", (d.Line-first)%mirrorProbeLines, d.Code))
	}
	for i := range out {
		slices.Sort(out[i])
	}
	return out
}
