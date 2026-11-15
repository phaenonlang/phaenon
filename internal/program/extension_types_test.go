package program

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// TestStubTypeOf: the stub generator writes a reflected type as PHP reads
// it back. An intersection inside a union keeps its parentheses: PHP has
// no `A&B|null`, and a stub that holds one does not parse, which loses the
// whole extension.
func TestStubTypeOf(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not on PATH")
	}
	const declarations = `
interface A {}
interface B {}
interface C {}
function f(
    int $plain,
    ?A $nullable,
    A|int|null $union,
    A&B $intersection,
    (A&B)|null $dnf,
    (A&B)|(A&C)|string $dnfs,
): void {}
foreach ((new ReflectionFunction('f'))->getParameters() as $p) {
    echo typeOf($p->getType()), "\n";
}
`
	out, err := exec.Command(php, "-r", typeOfScript+declarations).CombinedOutput()
	if err != nil {
		t.Fatalf("php: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	want := []string{
		`int`,
		`?\A`,
		`\A|int|null`,
		`\A&\B`,
		`(\A&\B)|null`,
		`(\A&\B)|(\A&\C)|string`,
	}
	if len(got) != len(want) {
		t.Fatalf("typeOf wrote %d types, want %d:\n%s", len(got), len(want), out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("typeOf wrote %s, want %s", got[i], want[i])
		}
		stub := "<?php\nfunction g(" + got[i] + " $x) {}\n"
		if _, err := parser.Parse([]byte(stub), lexer.ModePHP); err != nil {
			t.Errorf("%s does not parse as a parameter type: %v", got[i], err)
		}
	}
}
