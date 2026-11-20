package checker

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

// TestSelfReferenceEnds holds the check of values that read other values
// to an end in bounded time. A cycle of enum cases once overflowed the
// stack; a value read twice was typed twice, so that cases reading each
// other densely, or a chain of cases each reading the one before twice,
// were typed as many times as there are paths through them. Here every
// case and constant of a dense cycle reads all the others, and a chain of
// sixty cases doubles the one before.
func TestSelfReferenceEnds(t *testing.T) {
	const n = 40
	var b strings.Builder
	b.WriteString("namespace Dense;\n\nenum Cases: int {\n")
	for i := range n {
		fmt.Fprintf(&b, "    case C%d = ", i)
		for j := range n {
			if j != i {
				fmt.Fprintf(&b, "self::C%d->value + Consts::K%d + ", j, j)
			}
		}
		b.WriteString("0;\n")
	}
	b.WriteString("}\n\nfinal class Consts {\n")
	for i := range n {
		fmt.Fprintf(&b, "    const K%d = ", i)
		for j := range n {
			if j != i {
				fmt.Fprintf(&b, "self::K%d + Cases::C%d->value + ", j, j)
			}
		}
		b.WriteString("0;\n")
	}
	b.WriteString("}\n\nenum Doubling: int {\n    case D0 = 1;\n")
	for i := 1; i < 60; i++ {
		fmt.Fprintf(&b, "    case D%d = self::D%d->value + self::D%d->value;\n", i, i-1, i-1)
	}
	b.WriteString("}\n\nfunction last(): int {\n    return Doubling::D59->value;\n}\n")
	p := newProgram(t)
	if _, err := p.AddFile("dense.phn", []byte(b.String()), lexer.ModePhaenon, true); err != nil {
		t.Fatal(err)
	}
	done := make(chan []Diagnostic, 1)
	go func() { done <- New(p, Options{}).Check() }()
	select {
	case diags := <-done:
		cycles := 0
		for _, d := range diags {
			if d.Code == P2016.ID {
				cycles++
				continue
			}
			t.Errorf("line %d: %s %s", d.Line, d.Code, d.Message)
		}
		if cycles != 2*n {
			t.Errorf("%d findings of P2016, want one for each of the %d cases and %d constants", cycles, n, n)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the check did not end in 30 seconds")
	}
}
