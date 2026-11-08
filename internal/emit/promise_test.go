package emit

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestOutputsDependOnPHPOnly holds the promise of design/roadmap.md D24:
// the compiled output depends on nothing but PHP, so no output names a
// class, function or constant under Phaenon\ (PhaenonReferences says what
// counts). It scans the emitter goldens as committed, which TestGenerics
// keeps current; TestLineMapProperty scans what it compiles, the checker's
// corpus among it, and TestLineMapTree a dogfood tree. The one exception is
// a namespace under Phaenon\, which the stdlib's Debug sources have and no
// golden may: the files exempted must be exactly those sources.
func TestOutputsDependOnPHPOnly(t *testing.T) {
	goldens, _ := filepath.Glob(filepath.Join("testdata", "*.php"))
	if len(goldens) == 0 {
		t.Fatal("no compiled goldens")
	}
	for _, path := range goldens {
		out, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if expectPHPOnly(t, path, out) {
			t.Errorf("%s: a golden in a namespace under Phaenon\\ goes unscanned", path)
		}
	}
	stdlib, err := Stdlib()
	if err != nil {
		t.Fatal(err)
	}
	var exempted []string
	for _, name := range slices.Sorted(maps.Keys(stdlib)) {
		if expectPHPOnly(t, name, stdlib[name]) {
			exempted = append(exempted, name)
		}
	}
	t.Logf("exempted, in a namespace under Phaenon\\: %s", strings.Join(exempted, ", "))
	if want := slices.Sorted(maps.Keys(stdlib)); !slices.Equal(exempted, want) {
		t.Errorf("exempted %v, want the stdlib's Debug sources %v", exempted, want)
	}
}

// expectPHPOnly fails the test when out, the output compiled from path,
// names anything under Phaenon\ outside a namespace of its own, and reports
// whether it has such a namespace.
func expectPHPOnly(t *testing.T, path string, out []byte) (exempt bool) {
	t.Helper()
	refs, exempt := PhaenonReferences(out)
	if len(refs) > 0 {
		t.Errorf("%s: the output depends on more than PHP (design/roadmap.md D24):\n\t%s", path, strings.Join(refs, "\n\t"))
	}
	return exempt
}

// TestPhaenonReferences pins down what the promise's scan counts as a
// reference, and what it leaves alone.
func TestPhaenonReferences(t *testing.T) {
	for _, c := range []struct {
		name, src string
		want      []string
		exempt    bool
	}{
		{"an import", `<?php

namespace App;

use Phaenon\Debug\LineMap;
`, []string{`line 5: \Phaenon\Debug\LineMap`}, false},
		{"a group import", `<?php
namespace App;
use Phaenon\{Type, Debug\LineMap as M}, Other\X;
`, []string{`line 3: \Phaenon\Type`, `line 3: \Phaenon\Debug\LineMap`}, false},
		{"function and const imports", `<?php
namespace App;
use function Phaenon\f;
use const \Phaenon\C;
`, []string{`line 3: \Phaenon\f`, `line 4: \Phaenon\C`}, false},
		{"a fully qualified call", `<?php
namespace App;
$t = \Phaenon\Type::of('int');
`, []string{`line 3: \Phaenon\Type`}, false},
		{"qualified in the global namespace", `<?php
$t = Phaenon\Type::of('int');
`, []string{`line 2: \Phaenon\Type`}, false},
		{"qualified in a namespace", `<?php
namespace App;
$t = Phaenon\Type::of('int');
`, nil, false},
		{"qualified through an import", `<?php
namespace App;
use Phaenon as P;
$t = P\Type::of('int');
`, []string{`line 4: \Phaenon\Type`}, false},
		{"relative in the global namespace", `<?php
$t = namespace\Phaenon\Type::class;
`, []string{`line 2: \Phaenon\Type`}, false},
		{"a class-name string", `<?php
namespace App;
$c = 'Phaenon\\Bytes';
`, []string{`line 3: \Phaenon\Bytes`}, false},
		{"a callable string", `<?php
namespace App;
$f = '\Phaenon\Type::of';
`, []string{`line 3: \Phaenon\Type`}, false},
		{"an interpolated string", `<?php
namespace App;
$c = "{$ns}Phaenon\\Type";
$d = "$x \\Phaenon\\Type::of";
`, []string{`line 3: \Phaenon\Type`, `line 4: \Phaenon\Type`}, false},
		{"a heredoc", `<?php
namespace App;
$c = <<<EOT
    Phaenon\\Bytes
    EOT;
`, []string{`line 4: \Phaenon\Bytes`}, false},
		{"a nowdoc", `<?php
namespace App;
$c = <<<'EOT'

    Phaenon\Bytes
    EOT;
`, []string{`line 5: \Phaenon\Bytes`}, false},
		{"a docblock", `<?php
namespace App;
/**
 * @param \Phaenon\Type<int> $t
 */
function f($t) {}
`, []string{`line 4: \Phaenon\Type`}, false},
		{"comments and the map", `<?php
// \Phaenon\Type
/* Phaenon\Bytes */
// phaenon-map v1 a.phn 1-6=1 7:1
`, nil, false},
		{"a trait's use", `<?php
namespace App;
final class A
{
    use Phaenon\T;
}
`, nil, false},
		{"a closure's use", `<?php
namespace App;
$f = function () use ($x) { return \strlen($x); };
`, nil, false},
		{"text that only mentions it", `<?php
namespace App;
$s = 'see Phaenon\\Debug for more';
$t = "see {$x} Phaenon\\Debug for more";
`, nil, false},
		{"a braced namespace", `<?php
namespace App {
    use Phaenon\X;
}
namespace {
    $y = Phaenon\Y::class;
}
`, []string{`line 3: \Phaenon\X`, `line 6: \Phaenon\Y`}, false},
		{"its own namespace", `<?php
namespace Phaenon\Debug;

use Phaenon\Debug\LineMap;
`, nil, true},
		{"its own namespace, then another", `<?php
namespace Phaenon\Debug;

use Phaenon\Debug\LineMap;

namespace App;

use Phaenon\Debug\LineMap;
`, []string{`line 8: \Phaenon\Debug\LineMap`}, true},
		{"braced, its own namespace first", `<?php
namespace Phaenon\Debug {
    $a = \Phaenon\X::class;
}
namespace {
    $b = \Phaenon\Y::class;
}
`, []string{`line 6: \Phaenon\Y`}, true},
	} {
		refs, exempt := PhaenonReferences([]byte(c.src))
		if fmt.Sprint(refs) != fmt.Sprint(c.want) || exempt != c.exempt {
			t.Errorf("%s: got %q, exempt %v; want %q, exempt %v", c.name, refs, exempt, c.want, c.exempt)
		}
	}
}
