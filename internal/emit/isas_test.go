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
	"github.com/phaenonlang/phaenon/internal/linemap"
	"github.com/phaenonlang/phaenon/internal/program"
)

// TestIsAsBehaviour runs the tests `is` and `as` compile to under php
// (design/generics.md §11.4): each type against every value, each `as`
// against a value that fails it, and the UTF-8 check of bytes tested as a
// string (design/strings.md §6). want has a digit per value, 1 where the
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
	src.WriteString("namespace App;\n\ninterface Model\n{\n}\n\nfinal class User implements Model\n{\n}\n\nenum Suit\n{\n    case Hearts;\n}\n\nfinal class Blob\n{\n    public function __construct(public ?bytes $data)\n    {\n    }\n}\n\n")
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
	// A declaration PHP gives no type guarantees nothing, whatever its
	// docblock says.
	src.WriteString("/**\n * @param list<int> $xs\n * @return list<int>\n */\nfunction docList($xs)\n{\n    return $xs;\n}\n\nfun docPair(list<int> $xs): bool\n{\n    $x = docList($xs);\n\n    return $x is (int, int);\n}\n\n")
	src.WriteString("final class DocIds\n{\n    /** @var list<int> */\n    public static $ids = [];\n\n    public static fun pair(): bool => self::$ids is (int, int);\n}\n\n")
	// A class name among a tuple's members is a test of that member.
	src.WriteString("fun classPair(mixed $m): bool => $m is (int, class|int);\n\n")
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
	// On bytes, `as string` is the checked conversion to text
	// (design/strings.md §6): the value when it is UTF-8, a ValueError when
	// the encoding is all that can fail, null from `as?`; null on ?bytes
	// is a TypeError, and where anything else can fail too, one TypeError
	// expects valid UTF-8.
	text := []struct{ param, expr, value, want string }{
		{"bytes", "$b as string", `(bytes) "\xFF"`, "ValueError: not valid UTF-8"},
		{"bytes", "$b as string", `(bytes) "h\xC3\xA9llo"`, "'héllo'"},
		{"bytes", "$b as string", "(bytes) ''", "''"},
		{"bytes", "$b as? string", `(bytes) "\xFF"`, "NULL"},
		{"bytes", "$b as? string", "(bytes) 'ok'", "'ok'"},
		{"bytes", "$b is string", `(bytes) "a\xC0\x80"`, "false"},
		{"bytes", "$b is string", "(bytes) 'ok'", "true"},
		{"?bytes", "$b as string", "null", "TypeError: expected string, got null"},
		{"?bytes", "$b as string", `(bytes) "\xFF"`, "ValueError: not valid UTF-8"},
		{"?bytes", "$b as string|null", "null", "NULL"},
		{"?bytes", "$b as string|null", `(bytes) "\xED\xA0\x80"`, "ValueError: not valid UTF-8"},
		{"?bytes", "$b as? string", "null", "NULL"},
		{"?bytes", "$b is ?string", "null", "true"},
		{"?bytes", "$b is ?string", `(bytes) "\xFF"`, "false"},
		{"?bytes", "$b is string", "null", "false"},
		{"int|bytes", "$b as string", "5", "TypeError: expected string with valid UTF-8, got int"},
		{"int|bytes", "$b as string", `(bytes) "\xFF"`, "TypeError: expected string with valid UTF-8, got string"},
		{"(bytes, int)", "$b as (string, int)", `[(bytes) "\xFF", 1]`, "TypeError: expected (string, int) with valid UTF-8, got array"},
		{"string", "((bytes) $b) as string", `"\xFF"`, "ValueError: not valid UTF-8"},
		{"string", "((bytes) $b) as string", "'ok'", "'ok'"},
		// A property guarded against null is bytes to the checker and
		// ?string to PHP: null is the one other failure, as unguarded.
		{"Blob", "$b->data === null ? 'none' : $b->data as string", `new Blob((bytes) "\xFF")`, "ValueError: not valid UTF-8"},
		{"Blob", "$b->data === null ? 'none' : $b->data as string", "new Blob(null)", "'none'"},
		{"int|bytes", "$b as int|string", `(bytes) "\xFF"`, "ValueError: not valid UTF-8"},
		{"int|bytes", "$b as int|string", "7", "7"},
	}
	for i, c := range text {
		fmt.Fprintf(&src, "fun text%d(%s $b): string\n{\n    try {\n        $v = %s;\n    } catch (\\ValueError|\\TypeError $e) {\n        return $e::class . ': ' . $e->getMessage();\n    }\n\n    return show($v);\n}\n\n", i, c.param, c.expr)
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
	src.WriteString("echo \\call_user_func('App\\\\docPair', ['a' => 'x', 'b' => 'y']) ? '1' : '0', docPair([3, 4]) ? '1' : '0';\n")
	src.WriteString("(new \\ReflectionProperty(DocIds::class, 'ids'))->setValue(null, ['a' => 1, 'b' => 2]);\n")
	src.WriteString("echo DocIds::pair() ? '1' : '0';\nDocIds::$ids = [1, 2];\necho DocIds::pair() ? '1' : '0', \"\\n\";\n")
	src.WriteString("echo classPair([1, User::class]) ? '1' : '0', classPair([1, 's']) ? '1' : '0', classPair([1, 'nope']) ? '1' : '0', classPair([1, 5]) ? '1' : '0', \"\\n\";\n")
	for i, c := range as {
		fmt.Fprintf(&src, "echo as%d(%s), \"\\n\";\n", i, c.value)
	}
	for i, c := range text {
		fmt.Fprintf(&src, "echo text%d(%s), \"\\n\";\n", i, c.value)
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
	args := append([]string{"-n", "-d", "display_errors=1", "-d", "error_reporting=-1"}, mbstring(t, php)...)
	ran, err := exec.Command(php, append(args, path)...).CombinedOutput()
	if err != nil {
		t.Fatalf("php: %v\n%s\n%s", err, ran, out)
	}
	lines := strings.Split(strings.TrimSuffix(string(ran), "\n"), "\n")
	var want []string
	for _, c := range tests {
		want = append(want, c.want)
	}
	want = append(want, "0001", "100", "0101", "0101", "1001")
	for _, c := range as {
		want = append(want, c.want)
	}
	for _, c := range text {
		want = append(want, c.want)
	}
	if len(lines) != len(want) {
		t.Fatalf("php printed %d lines, want %d:\n%s", len(lines), len(want), ran)
	}
	for i, w := range want {
		if lines[i] == w {
			continue
		}
		what := "pair, named, fromValues and Ids, docPair and DocIds, classPair"
		switch {
		case i < len(tests):
			what = "is " + tests[i].typ
		case i >= len(tests)+5+len(as):
			c := text[i-len(tests)-5-len(as)]
			what = c.expr + " on " + c.param + " " + c.value
		case i >= len(tests)+5:
			what = as[i-len(tests)-5].expr
		}
		t.Errorf("%s: got %s, want %s", what, lines[i], w)
	}
	if t.Failed() {
		t.Logf("compiled:\n%s", out)
	}
}

// mbstring is the option that loads ext-mbstring into php -n, nothing when
// it is built in: the UTF-8 test of bytes calls mb_check_encoding()
// (design/strings.md §6), and a php that shares the extension, as
// setup-php's does, loads it only from an ini file. A php without it
// skips the test, as one missing from PATH does.
func mbstring(t *testing.T, php string) []string {
	t.Helper()
	probe := func(args ...string) bool {
		args = append(append([]string{"-n"}, args...), "-r", "exit(extension_loaded('mbstring') ? 0 : 1);")
		return exec.Command(php, args...).Run() == nil
	}
	if probe() {
		return nil
	}
	if probe("-d", "extension=mbstring") {
		return []string{"-d", "extension=mbstring"}
	}
	t.Skipf("%s has no ext-mbstring, which the UTF-8 test of bytes needs", php)
	return nil
}

// TestIsAsTwiceRefused: an operand a test writes more than once must read
// the same twice (P3012). The checker reports it whether or not the test is
// settled, and the emitter refuses to write such a test even where the
// build goes on despite the findings (phn build --emit-on-error), rather
// than call the function three times.
func TestIsAsTwiceRefused(t *testing.T) {
	src := []byte("fun f(): int\n{\n    return \\strlen('a') as int;\n}\n\nfun g(): bool\n{\n    return \\strlen('a') is int|string;\n}\n")
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	f, err := p.AddFile("twice.phn", src, lexer.ModePhaenon, true)
	if err != nil {
		t.Fatal(err)
	}
	found := map[int]bool{}
	for _, d := range checker.New(p, checker.Options{}).Check() {
		if d.Code == checker.P3012.ID {
			found[d.Line] = true
		}
	}
	if !found[3] || !found[8] {
		t.Errorf("P3012 on lines %v, want 3 and 8", found)
	}
	if out, err := File(src, Options{SourceName: "twice.phn", StrictTypes: true, Program: p, File: f}); err == nil || !strings.Contains(err.Error(), "evaluated twice") {
		t.Errorf("compiled despite the operand read twice (%v):\n%s", err, out)
	}
}

// TestIsAsMultiLineOperand: an operand written over several lines and
// copied by its test is laid out as text of the compiler's own, a hold in
// the line map, never an in-place edit whose copies would sit on lines
// that stand for other source lines.
func TestIsAsMultiLineOperand(t *testing.T) {
	src := []byte("final class Holder\n{\n    public mixed $item = null;\n}\n\nfinal class User\n{\n}\n\nfun f(Holder $h): ?User\n{\n    return $h\n        ->item as?\n        User;\n}\n")
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	f, err := p.AddFile("multi.phn", src, lexer.ModePhaenon, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range checker.New(p, checker.Options{}).Check() {
		if d.Severity == checker.Error {
			t.Fatalf("checker: %d: %s: %s", d.Line, d.Code, d.Message)
		}
	}
	out, w, err := compile(src, f.AST, Options{SourceName: "multi.phn", StrictTypes: true, Program: p, File: f})
	if err != nil {
		t.Fatal(err)
	}
	checkLineMap(t, "multi.phn", src, out, w)
	m, err := linemap.Find(out)
	if err != nil || m == nil {
		t.Fatalf("no line map (%v):\n%s", err, out)
	}
	copied := 0
	for i, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "instanceof User ? $h") {
			continue
		}
		copied = i + 1
	}
	if copied == 0 {
		t.Fatalf("no test written:\n%s", out)
	}
	for _, e := range m.Entries {
		if e.Hold && e.Out <= copied && copied <= e.End {
			return
		}
	}
	t.Errorf("output line %d copies the operand in place:\n%s", copied, out)
}
