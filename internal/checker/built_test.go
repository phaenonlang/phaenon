package checker

import (
	"fmt"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

// TestBuiltArraysStayBounded: writes under keys of several literals and
// array_merge() of unions of shapes give one shape, PHPStan's, whose keys
// are optional where a write or an argument may lack them, however many
// there are; an array joins made into shapes past the limit of their
// entries, ten independent optional writes or ten if/else, is one shape at
// the next write, its keys optional where a shape lacks them, PHPStan's
// type for the options (boundedShapes), and below the limit keeps its
// shapes. The type is read from the message of a call that refuses it.
func TestBuiltArraysStayBounded(t *testing.T) {
	var keys, merges strings.Builder
	keys.WriteString("$o = [];\n")
	var args []string
	for i := 1; i <= 16; i++ {
		fmt.Fprintf(&keys, "$o[($c[%d] ?? false) ? 'a%d' : 'b%d'] = %d;\n", i, i, i, i)
		fmt.Fprintf(&merges, "$a%d = ($c[%d] ?? false) ? ['a%d' => %d] : [];\n", i, i, i, i)
		args = append(args, fmt.Sprintf("$a%d", i))
	}
	fmt.Fprintf(&merges, "$o = array_merge(%s);\n", strings.Join(args, ", "))
	joins := func(n int) string {
		var b strings.Builder
		b.WriteString("$o = [];\n")
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "if ($c[%d] ?? false) { $o['a%d'] = 1; } else { $o['b%d'] = 2; }\n", i, i, i)
		}
		b.WriteString("$o['z'] = 3;\n")
		return b.String()
	}
	options := func(n int) string {
		var b strings.Builder
		b.WriteString("$o = ['id' => 0];\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "if ($c[%d] ?? false) { $o['opt%d'] = %d; }\n", i, i, i)
		}
		b.WriteString("$o['done'] = true;\n")
		return b.String()
	}
	for name, c := range map[string]struct {
		body     string
		min, max int
	}{
		"writes":         {keys.String(), 1, 1},
		"merges":         {merges.String(), 1, 1},
		"few joins":      {joins(6), 64, 64},
		"many joins":     {joins(10), 1, 2},
		"nine options":   {options(9), 512, 512},
		"ten options":    {options(10), 1, 1},
		"twelve options": {options(12), 1, 1},
	} {
		t.Run(name, func(t *testing.T) {
			src := "function takesInt(int $i): void {}\n\n/** @param list<bool> $c */\nfunction f(array $c): void\n{\n" + c.body + "takesInt($o);\n}\n"
			p := newProgram(t)
			if _, err := p.AddFile("bounded.phn", []byte(src), lexer.ModePhaenon, true); err != nil {
				t.Fatal(err)
			}
			var msg string
			for _, d := range New(p, Options{}).Check() {
				if d.Code == "P2103" {
					msg = d.Message
				}
			}
			if msg == "" {
				t.Fatal("no P2103 for takesInt($o)")
			}
			if n := strings.Count(msg, "array{"); n < c.min || n > c.max {
				t.Errorf("the array is %d shapes, want %d to %d: %s", n, c.min, c.max, msg)
			}
		})
	}
}
