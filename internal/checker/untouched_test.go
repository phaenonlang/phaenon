package checker

import (
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

// A parameter is untouched when the read a finding is about is its only
// occurrence in the body, in one of the plain positions untouched.go
// lists. Each body below is that of
//
//	function f(int $i, bool $c, array $a, object $o, array $t): void
//
// where $t is documented a list{int, string, bool}. It holds one finding
// whose kind says whether the parameter is untouched: `$i ** -1` given for
// an int, or $t given for a list{int, string}; every P2103 the body draws
// has to be an extra check, or none. The plain positions are those PHPStan
// was given (the golden file untouched.phn holds them to it); the others
// are a second occurrence, or a place the list leaves out.
func TestUntouchedPositions(t *testing.T) {
	for _, c := range []struct {
		body  string
		plain bool
	}{
		// The one occurrence, an operand in an argument.
		{`takesInt($i ** -1);`, true},
		{`takesInt(($i) ** -1);`, true},
		{`echo passesInt($i ** -1);`, true},
		{`print passesInt($i ** -1);`, true},
		{`$x = passesInt($i ** -1); echo $x;`, true},
		{`$o->p = passesInt($i ** -1);`, true},
		{`takes(1, [passesInt($i ** -1)], n: 2);`, true},
		{`if ($c) { takesInt($i ** -1); }`, true},
		{`while ($c) { takesInt($i ** -1); $c = false; }`, true},
		{`foreach ($a as $v) { echo $v; takesInt($i ** -1); }`, true},
		{`for ($k = 0; $k < 3; $k++) { takesInt($i ** -1); }`, true},
		{`do { takesInt($i ** -1); } while ($c);`, true},
		{`switch ($c) { case true: takesInt($i ** -1); }`, true},
		{`try { takesInt($i ** -1); } catch (\Throwable $e) { echo 1; }`, true},
		{`try { echo 1; } catch (\Throwable $e) { takesInt($i ** -1); }`, true},
		{`try { echo 1; } finally { takesInt($i ** -1); }`, true},
		{`$c ? takesInt($i ** -1) : takesInt(0);`, true},
		{`echo match ($c) { true => passesInt($i ** -1), false => 0 };`, true},
		{`$f = fn (): int => passesInt($i ** -1); echo $f();`, true},
		{`echo \count(\func_get_args()); takesInt($i ** -1);`, true},
		// The one occurrence, the variable itself as an argument.
		{`takesPair($t);`, true},
		{`takesPair(pair: $t);`, true},
		{`if ($c) { takesPair($t); }`, true},
		{`$c ? takesPair($t) : takesInt(0);`, true},
		{`try { takesPair($t); } finally { echo 1; }`, true},
		// In a loop the variable itself is no plain read as an argument: a
		// parameter that takes it by reference leaves its type for the
		// second pass.
		{`while ($c) { takesPair($t); $c = false; }`, false},
		{`foreach ($a as $v) { echo $v; takesPair($t); }`, false},
		{`for ($k = 0; $k < 3; $k++) { takesPair($t); }`, false},
		{`do { takesPair($t); } while ($c);`, false},
		{`while ($c) { $f = fn (): mixed => takesPair($t); $f(); $c = false; }`, false},
		// Nor in an array literal there.
		{`takesPairs([$t]);`, false},
		// A second occurrence, whatever it is.
		{`takesInt($i ** -1); echo $i;`, false},
		{`echo $i; takesInt($i ** -1);`, false},
		{`takesInt($i ** -1); takesInt($i ** -1);`, false},
		{`\assert(\is_float($i ** -1)); takesInt($i ** -1);`, false},
		{`$ok = \is_float($i ** -1); if ($ok) { takesInt($i ** -1); }`, false},
		{`takesInt($i ** -1); $i = 3;`, false},
		{`takesInt($i ** -1); $r = &$i; echo $r;`, false},
		{`takesInt($i ** -1); unset($i);`, false},
		{`takesInt($i ** -1); foreach ($a as $i) { echo 1; }`, false},
		{`takesPair($t); echo \count($t);`, false},
		{`takesPair($t); takesPair($t);`, false},
		// What names the variable without being one.
		{"/** @var int $i */\n    takesInt($i ** -1);", false},
		{"// $i is the count\n    takesInt($i ** -1);", false},
		{`echo '$i'; takesInt($i ** -1);`, false},
		{`$f = function (int $i): void { echo 1; }; $f(1); takesInt($i ** -1);`, false},
		{`$f = fn (int $i): int => 1; echo $f(1); takesInt($i ** -1);`, false},
		// The one occurrence in a condition, or in what narrows.
		{`if (passesInt($i ** -1) > 0) { echo 1; }`, false},
		{`if ($c) { echo 1; } elseif (passesInt($i ** -1)) { echo 2; }`, false},
		{`while (passesInt($i ** -1) > 0) { break; }`, false},
		{`for ($k = 0; $k < passesInt($i ** -1); $k++) { echo $k; }`, false},
		{`switch (passesInt($i ** -1)) { default: echo 1; }`, false},
		{`echo match (passesInt($i ** -1)) { default => 1 };`, false},
		{`echo match ($c) { passesInt($i ** -1) > 0 => 1, default => 0 };`, false},
		{`echo passesInt($i ** -1) ? 1 : 2;`, false},
		{`echo passesInt($i ** -1) ?: 1;`, false},
		{`echo passesInt($i ** -1) ?? 1;`, false},
		{`echo $c && passesInt($i ** -1);`, false},
		{`echo passesInt($i ** -1) === 1;`, false},
		{`echo isset($a[passesInt($i ** -1)]);`, false},
		{`\assert(passesInt($i ** -1) > 0);`, false},
		// Other places the list does not have.
		{`echo $a[passesInt($i ** -1)];`, false},
		{`echo (string) passesInt($i ** -1);`, false},
		{`echo ~passesInt($i ** -1);`, false},
		{`echo passesInt($i ** -1) | 1;`, false},
		{`$f = function () use ($i): void { takesInt($i ** -1); }; $f();`, false},
		{`takes($o->p = passesInt($i ** -1));`, false},
		// What writes or reads a variable without naming it.
		{`$n = 'c'; echo $$n; takesInt($i ** -1);`, false},
		{`echo "${c}"; takesInt($i ** -1);`, false},
		{`extract($a); takesInt($i ** -1);`, false},
		{`echo \count(\compact('c')); takesInt($i ** -1);`, false},
		{`echo \count(\get_defined_vars()); takesInt($i ** -1);`, false},
		{`eval(''); takesInt($i ** -1);`, false},
		{`include 'x.php'; takesInt($i ** -1);`, false},
	} {
		src := "namespace U;\n" +
			"function takesInt(int $x): void {}\n" +
			"function passesInt(int $x): int { return $x; }\n" +
			"function takes(mixed ...$x): void {}\n" +
			"/** @param list{int, string} $pair */\nfunction takesPair(array $pair): void {}\n" +
			"/** @param list<list{int, string}> $pairs */\nfunction takesPairs(array $pairs): void {}\n" +
			"/** @param list{int, string, bool} $t */\n" +
			"function f(int $i, bool $c, array $a, object $o, array $t): void\n{\n" +
			"    " + c.body + "\n}\n"
		p := newProgram(t)
		if _, err := p.AddFile("untouched.phn", []byte(src), lexer.ModePhaenon, true); err != nil {
			t.Errorf("%s: %v", c.body, err)
			continue
		}
		found := false
		for _, d := range New(p, Options{}).Check() {
			if d.Code != "P2103" || d.Line < 12 {
				continue
			}
			found = true
			if plain := d.Kind == Extra; plain != c.plain {
				t.Errorf("%q: the finding is an extra check: %v, want %v (%s)", c.body, plain, c.plain, d.Message)
			}
		}
		if !found {
			t.Errorf("%q: no P2103 in the body", c.body)
		}
	}
}
