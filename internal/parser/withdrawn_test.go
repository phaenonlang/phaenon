package parser

import (
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
)

func countWithdrawn(f *ast.File) int {
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		if _, ok := node.(*ast.Withdrawn); ok {
			n++
		}
		return true
	})
	return n
}

// TestWithdrawnConcurrency: the shapes of the syntax that left the language
// (design/roadmap.md D25) parse in Phaenon mode as ast.Withdrawn, for the
// checker to name (P1001), and stay syntax errors in PHP mode.
func TestWithdrawnConcurrency(t *testing.T) {
	for src, want := range map[string]int{
		"concurrently { $a = 1; }":                                             1,
		"concurrently { $a = 1; };":                                            1,
		"Concurrently (timeout: 5) {}":                                         1,
		"$x = concurrently { return 1; };":                                     1,
		"concurrently { $a = spawn $this->load(); $b = spawn { return 2; }; }": 3,
		"$f = spawn fn () => 1;":                                               1,
		"$s = spawn new Foo();":                                                1,
		"$t = spawn foo();":                                                    1,
		"$u = spawn !$x;":                                                      1,
	} {
		f, err := Parse([]byte(src), lexer.ModePhaenon)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if got := countWithdrawn(f); got != want {
			t.Errorf("%s: %d withdrawn constructs, want %d", src, got, want)
		}
		if _, err := Parse([]byte("<?php "+src), lexer.ModePHP); err == nil {
			t.Errorf("%s parses as PHP", src)
		}
	}
}

// TestConcurrencyWordsStayNames: nothing is reserved. PHP code that names
// something `concurrently` or `spawn` parses as before, in Phaenon mode as
// in PHP mode, without a withdrawn construct.
func TestConcurrencyWordsStayNames(t *testing.T) {
	php := `<?php
namespace spawn;
use Foo\spawn as spawn2;
const spawn = [1];
const concurrently = 2;
function spawn(spawn $spawn): spawn { return spawn; }
function concurrently(int ...$x): int { return 0; }
class spawn extends Base implements concurrently\I {
    const spawn = spawn;
    public spawn $spawn;
    public function concurrently(): spawn { return $this->spawn instanceof spawn ? spawn::spawn : spawn(spawn); }
}
$a = spawn[0] + spawn - 1 + spawn + concurrently;
$b = spawn(...);
$c = concurrently(1, 2) . concurrently;
if (concurrently()) {}
while (concurrently(concurrently())) {}
switch (spawn) { case spawn: break; }
$d = match (spawn) { spawn => concurrently };
foreach (spawn as $spawn) {}
$f = fn (spawn $s): spawn => spawn::concurrently($s);
$g = new spawn;
spawn::concurrently();
concurrently();
goto spawn;
spawn:
`
	for _, mode := range []lexer.Mode{lexer.ModePHP, lexer.ModePhaenon} {
		src := php
		if mode == lexer.ModePhaenon {
			src = strings.TrimPrefix(php, "<?php\n")
		}
		f, err := Parse([]byte(src), mode)
		if err != nil {
			t.Errorf("mode %v: %v", mode, err)
			continue
		}
		if n := countWithdrawn(f); n != 0 {
			t.Errorf("mode %v: %d withdrawn constructs in code that names the words", mode, n)
		}
	}
}
