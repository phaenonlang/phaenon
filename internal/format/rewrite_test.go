package format

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
)

// TestRewrittenTestsBehaviour runs under php the PHP tests phn fmt writes
// for the withdrawn `is` and `as` (design/tooling.md §5.6), each of which
// must do at run time what its spelling did (design/generics.md §11.4):
// each type against every value, each `as` against a value that fails it,
// and the UTF-8 check of bytes tested as a string (design/strings.md §6).
// want has a digit per value, 1 where the test holds.
func TestRewrittenTestsBehaviour(t *testing.T) {
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
		fmt.Fprintf(&src, "function is%d(mixed $m): bool => $m is %s;\n\n", i, c.typ)
	}
	// On an array, a tuple's test starts at array_is_list(); on a string,
	// class<User>'s is is_a() alone.
	src.WriteString("function pair(array<mixed> $a): bool => $a is (int, string);\n\n")
	src.WriteString("function named(string $s): bool => $s is class<User>;\n\n")
	// What a docblock or the overlay refines is no PHP guarantee: a PHP
	// caller hands any array through an erased signature, and anything
	// can land in a static property, so the checks stay.
	src.WriteString("function fromValues(map<int|string, int> $ints): bool\n{\n    $list = \\array_values($ints);\n\n    return $list is (int, int);\n}\n\n")
	src.WriteString("final class Ids\n{\n    /** @var list<int> */\n    public static array $ids = [];\n\n    public static pair(): bool => self::$ids is (int, int);\n}\n\n")
	// A declaration PHP gives no type guarantees nothing, whatever its
	// docblock says.
	src.WriteString("/**\n * @param list<int> $xs\n * @return list<int>\n */\nfunction docList($xs)\n{\n    return $xs;\n}\n\nfunction docPair(list<int> $xs): bool\n{\n    $x = docList($xs);\n\n    return $x is (int, int);\n}\n\n")
	src.WriteString("final class DocIds\n{\n    /** @var list<int> */\n    public static $ids = [];\n\n    public static pair(): bool => self::$ids is (int, int);\n}\n\n")
	// A class name among a tuple's members is a test of that member.
	src.WriteString("function classPair(mixed $m): bool => $m is (int, class|int);\n\n")
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
		// `as ?int` is `as` with a nullable type, `as?` only where the `?`
		// touches `as`: null passes, a string throws.
		{"$m as ?int", "'s'", "expected ?int, got string"},
		{"$m as ? int", "'s'", "expected ?int, got string"},
		{"$m as ?int", "null", "NULL"},
	}
	for i, c := range as {
		fmt.Fprintf(&src, "function as%d(mixed $m): string\n{\n    try {\n        $v = %s;\n    } catch (\\TypeError $e) {\n        return $e->getMessage();\n    }\n\n    return show($v);\n}\n\n", i, c.expr)
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
		fmt.Fprintf(&src, "function text%d(%s $b): string\n{\n    try {\n        $v = %s;\n    } catch (\\ValueError|\\TypeError $e) {\n        return $e::class . ': ' . $e->getMessage();\n    }\n\n    return show($v);\n}\n\n", i, c.param, c.expr)
	}
	src.WriteString("function show(mixed $v): string => $v instanceof Suit ? $v->name : \\var_export($v, true);\n\n")
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

	out := rewriteAndCompile(t, "isas.phn", []byte(src.String()))
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

// rewriteAndCompile checks src, whose withdrawn tests must each be P1001
// and nothing else an error, formats it with the checked program's
// rewrites, and compiles what the first layer wrote, which must check
// clean.
func rewriteAndCompile(t *testing.T, name string, src []byte) []byte {
	t.Helper()
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	f, err := p.AddFile(name, src, lexer.ModePhaenon, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range checker.New(p, checker.Options{}).Check() {
		if d.Severity == checker.Error && d.Code != checker.P1001.ID {
			t.Errorf("checker: %d: %s: %s\n%s", d.Line, d.Code, d.Message, f.Line(d.Line))
		}
	}
	tests := map[ast.Span]string{}
	for n, rec := range f.Tests {
		if rec.Rewritten != "" {
			tests[n.Span()] = rec.Rewritten
		}
	}
	formatted, left, err := SourceWith(src, Options{Tests: tests, Program: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range left {
		t.Errorf("left at %d: %s", l.Span.Start, l.Msg)
	}
	q := program.New()
	if err := q.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	g, err := q.AddFile(name, formatted, lexer.ModePhaenon, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range checker.New(q, checker.Options{}).Check() {
		// A test the types settle is reported on its PHP as on any PHP
		// test (P2402): the rewrite keeps what the test did at run time.
		if d.Severity == checker.Error && d.Code != checker.P2402.ID {
			t.Errorf("checker after phn fmt: %d: %s: %s\n%s", d.Line, d.Code, d.Message, g.Line(d.Line))
		}
	}
	out, err := emit.File(formatted, emit.Options{SourceName: name, StrictTypes: true, Program: q, File: g})
	if err != nil {
		t.Fatalf("%v\n%s", err, formatted)
	}
	return out
}

// TestRewriteRefusedTwice: an operand a rewrite would write more than once
// must read the same twice (P3012 until roadmap D26). Such a test has no
// rewrite: it stays P1001, with the reason, and phn fmt leaves it.
func TestRewriteRefusedTwice(t *testing.T) {
	src := []byte("function f(): int\n{\n    return \\strlen('a') as int;\n}\n\nfunction g(): bool\n{\n    return \\strlen('a') is int|string;\n}\n")
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
		if d.Code == checker.P1001.ID && strings.Contains(d.Message, "evaluated twice") {
			found[d.Line] = true
		}
	}
	if !found[3] || !found[8] {
		t.Errorf("P1001 with the reason on lines %v, want 3 and 8", found)
	}
	tests := map[ast.Span]string{}
	for n, rec := range f.Tests {
		if rec.Rewritten != "" {
			tests[n.Span()] = rec.Rewritten
		}
	}
	out, left, err := SourceWith(src, Options{Tests: tests, Program: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 || !strings.Contains(string(out), "\\strlen('a') as int;") || !strings.Contains(string(out), "\\strlen('a') is int|string;") {
		t.Errorf("left %v, wrote:\n%s", left, out)
	}
}

// TestRewriteStringCastCopies: a (string) cast operand copied before an
// index keeps its parentheses, and one written once keeps the source's.
func TestRewriteStringCastCopies(t *testing.T) {
	src := []byte("function pair(string $s): bool\n{\n    return ((string) $s) is (int, string);\n}\n\nfunction counted(string $s): bool\n{\n    return ((string) $s) is \\Countable&\\Traversable;\n}\n")
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	f, err := p.AddFile("cast.phn", src, lexer.ModePhaenon, true)
	if err != nil {
		t.Fatal(err)
	}
	checker.New(p, checker.Options{}).Check()
	tests := map[ast.Span]string{}
	for n, rec := range f.Tests {
		tests[n.Span()] = rec.Rewritten
	}
	out, _, err := SourceWith(src, Options{Tests: tests, Program: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"is_int(((string) $s)[0])", "is_string(((string) $s)[1])", "is_array((string) $s)", "return ((string) $s) instanceof \\Countable"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("no %s in:\n%s", want, out)
		}
	}
}

// TestRewriteNested: a test in the operand of another is rewritten in the
// same run, so that phn fmt formats to itself (design/tooling.md §5.6).
func TestRewriteNested(t *testing.T) {
	src := []byte("interface Shape\n{\n}\n\nfinal class Circle implements Shape\n{\n}\n\n" +
		"function f(mixed $x): bool\n{\n    return ($x as Shape) is Circle;\n}\n\n" +
		"function g(mixed $x): bool\n{\n    return ($x as? Shape) is Circle;\n}\n")
	out := rewriteAndCompile(t, "nested.phn", src)
	formatted, _, _ := SourceWith(src, Options{Program: true, Tests: rewrites(t, src)})
	for _, want := range []string{
		"return ($x instanceof Shape ? $x : throw new \\TypeError('expected Shape, got ' . get_debug_type($x))) instanceof Circle;",
		"return ($x instanceof Shape ? $x : null) instanceof Circle;",
	} {
		if !strings.Contains(string(formatted), want) {
			t.Errorf("no %s in:\n%s\ncompiled:\n%s", want, formatted, out)
		}
	}
}

// rewrites are the checked program's rewrites of src's withdrawn tests.
func rewrites(t *testing.T, src []byte) map[ast.Span]string {
	t.Helper()
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	f, err := p.AddFile("rewrites.phn", src, lexer.ModePhaenon, true)
	if err != nil {
		t.Fatal(err)
	}
	checker.New(p, checker.Options{}).Check()
	out := map[ast.Span]string{}
	for n, rec := range f.Tests {
		if rec.Rewritten != "" {
			out[n.Span()] = rec.Rewritten
		}
	}
	return out
}
