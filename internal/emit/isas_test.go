package emit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
)

// TestIsAsBehaviour runs the tests `is` and `as` compile to under php
// (design/generics.md §11.4): each type against every value, each `as`
// against a value that fails it. want has a digit per value, 1 where the
// test holds.
func TestIsAsBehaviour(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php on PATH")
	}
	values := []string{
		"1", "1.5", "'s'", "true", "null", "[]", "[1, 2]", "['a' => 1]", "[1, 's']", "new User()",
		"Suit::Hearts", "fn () => 1", "User::class", "Model::class", "'12'", "new \\ArrayObject([])",
	}
	tests := []struct{ typ, want string }{
		{"int", "1000000000000000"},
		{"float", "0100000000000000"},
		{"string", "0010000000001110"},
		{"bool", "0001000000000000"},
		{"null", "0000100000000000"},
		{"true", "0001000000000000"},
		{"array", "0000011110000000"},
		{"map<mixed, mixed>", "0000011110000000"},
		{"list", "0000011010000000"},
		{"object", "0000000001110001"},
		{"callable", "0000000000010000"},
		{"iterable", "0000011110000001"},
		{"User", "0000000001000000"},
		{"Model", "0000000001000000"},
		{"Suit", "0000000000100000"},
		{"Closure", "0000000000010000"},
		{"?User", "0000100001000000"},
		{"int|string", "1010000000001110"},
		{"\\Countable&\\Traversable", "0000000000000001"},
		{"(int, string)", "0000000010000000"},
		{"?(int, ?string)", "0000100010000000"},
		{"class<User>", "0000000000001000"},
		{"class<Model>", "0000000000001100"},
		{"class", "0000000000001100"},
		{"number", "1100000000000000"},
		{"scalar", "1111000000001110"},
		{"numeric", "1100000000000010"},
	}
	var src strings.Builder
	src.WriteString("namespace App;\n\ninterface Model\n{\n}\n\nfinal class User implements Model\n{\n}\n\nenum Suit\n{\n    case Hearts;\n}\n\n")
	for i, c := range tests {
		fmt.Fprintf(&src, "fun is%d(mixed $m): bool => $m is %s;\n\n", i, c.typ)
	}
	// On an array, a tuple's test starts at array_is_list(); on a string,
	// class<User>'s is is_a() alone.
	src.WriteString("fun pair(array<mixed> $a): bool => $a is (int, string);\n\n")
	src.WriteString("fun named(string $s): bool => $s is class<User>;\n\n")
	// What a docblock or the overlay refines is no PHP guarantee: a PHP
	// caller hands any array through an erased signature, and anything
	// can land in a static property, so the checks stay.
	src.WriteString("fun fromValues(map<int|string, int> $ints): bool\n{\n    $list = \\array_values($ints);\n\n    return $list is (int, int);\n}\n\n")
	src.WriteString("final class Ids\n{\n    /** @var list<int> */\n    public static array $ids = [];\n\n    public static fun pair(): bool => self::$ids is (int, int);\n}\n\n")
	// `as` gives the value, or throws the TypeError PHP's own check would.
	as := []struct{ expr, value, want string }{
		{"$m as int", "'s'", "expected int, got string"},
		{"$m as User|null", "1", "expected ?App\\User, got int"},
		{"$m as list", "['a' => 1]", "expected list, got array"},
		{"$m as (int, string)", "[1, 2]", "expected (int, string), got array"},
		{"$m as class<User>", "'App\\\\Model'", "expected class<App\\User>, got string"},
		{"$m as int", "7", "7"},
		{"$m as? int", "'s'", "NULL"},
		{"$m as? Suit", "Suit::Hearts", "Hearts"},
	}
	for i, c := range as {
		fmt.Fprintf(&src, "fun as%d(mixed $m): string\n{\n    try {\n        $v = %s;\n    } catch (\\TypeError $e) {\n        return $e->getMessage();\n    }\n\n    return show($v);\n}\n\n", i, c.expr)
	}
	src.WriteString("fun show(mixed $v): string => $v instanceof Suit ? $v->name : \\var_export($v, true);\n\n")
	for i := range tests {
		fmt.Fprintf(&src, "echo")
		for j, v := range values {
			sep := ","
			if j == 0 {
				sep = ""
			}
			fmt.Fprintf(&src, "%s is%d(%s) ? '1' : '0'", sep, i, v)
		}
		src.WriteString(", \"\\n\";\n")
	}
	src.WriteString("echo pair([]) ? '1' : '0', pair([1, 2]) ? '1' : '0', pair(['a' => 1, 'b' => 's']) ? '1' : '0', pair([1, 's']) ? '1' : '0', \"\\n\";\n")
	src.WriteString("echo named('App\\\\User') ? '1' : '0', named('App\\\\Model') ? '1' : '0', named('s') ? '1' : '0', \"\\n\";\n")
	src.WriteString("echo \\call_user_func('App\\\\fromValues', ['a' => 'x', 'b' => 'y']) ? '1' : '0', fromValues([3, 4]) ? '1' : '0';\n")
	src.WriteString("(new \\ReflectionProperty(Ids::class, 'ids'))->setValue(null, ['a' => 1, 'b' => 2]);\n")
	src.WriteString("echo Ids::pair() ? '1' : '0';\nIds::$ids = [1, 2];\necho Ids::pair() ? '1' : '0', \"\\n\";\n")
	for i, c := range as {
		fmt.Fprintf(&src, "echo as%d(%s), \"\\n\";\n", i, c.value)
	}

	p := program.New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	f, err := p.AddFile("isas.phn", []byte(src.String()), lexer.ModePhaenon, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range checker.New(p, checker.Options{}).Check() {
		if d.Severity == checker.Error {
			t.Errorf("checker: %d: %s: %s\n%s", d.Line, d.Code, d.Message, f.Line(d.Line))
		}
	}
	out, err := File([]byte(src.String()), Options{SourceName: "isas.phn", StrictTypes: true, Program: p, File: f})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "isas.php")
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	ran, err := exec.Command(php, "-n", "-d", "display_errors=1", "-d", "error_reporting=-1", path).CombinedOutput()
	if err != nil {
		t.Fatalf("php: %v\n%s\n%s", err, ran, out)
	}
	lines := strings.Split(strings.TrimSuffix(string(ran), "\n"), "\n")
	var want []string
	for _, c := range tests {
		want = append(want, c.want)
	}
	want = append(want, "0001", "100", "0101")
	for _, c := range as {
		want = append(want, c.want)
	}
	if len(lines) != len(want) {
		t.Fatalf("php printed %d lines, want %d:\n%s", len(lines), len(want), ran)
	}
	for i, w := range want {
		if lines[i] == w {
			continue
		}
		what := "pair, named, fromValues and Ids"
		switch {
		case i < len(tests):
			what = "is " + tests[i].typ
		case i >= len(tests)+3:
			what = as[i-len(tests)-3].expr
		}
		t.Errorf("%s: got %s, want %s", what, lines[i], w)
	}
	if t.Failed() {
		t.Logf("compiled:\n%s", out)
	}
}
