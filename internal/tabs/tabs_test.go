package tabs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"indentation", "class A {\n\tfun f() {\n\t\treturn 1;\n\t}\n}\n",
			"class A {\n    fun f() {\n        return 1;\n    }\n}\n"},
		{"tab stops", "$a = 1;\t// x\n  \t$b;\n",
			"$a = 1; // x\n    $b;\n"},
		{"string content stays", "$s = \"a\tb\";\t$t = 'c\td';\n",
			"$s = \"a\tb\"; $t = 'c\td';\n"},
		{"comment text stays, its indentation goes", "\t/**\n\t * a\tb\n\t */\n\t// c\td\n",
			"    /**\n     * a\tb\n     */\n    // c\td\n"},
		{"inside a token", "$x = (\tint) $y;\n",
			"$x = (  int) $y;\n"},
		{"after multi-byte text", "$é\t= 1;\n",
			"$é  = 1;\n"},
		{"heredoc: indentation and the body's prefix", "\t$s = <<<EOT\n\t\ta\tb\n\n\t\t\tc {$x}\n\t\t\td\n\t\tEOT;\n",
			"    $s = <<<EOT\n        a\tb\n\n        \tc {$x}\n        \td\n        EOT;\n"},
		{"nowdoc", "$s = <<<'EOT'\n\tx\n\tEOT;\n",
			"$s = <<<'EOT'\n    x\n    EOT;\n"},
		{"heredoc at column 0 keeps its text", "$s = <<<EOT\n\tx\nEOT;\n",
			"$s = <<<EOT\n\tx\nEOT;\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(Expand([]byte(c.in))); got != c.want {
				t.Errorf("Expand:\n got %q\nwant %q", got, c.want)
			}
			if again := string(Expand([]byte(c.want))); again != c.want {
				t.Errorf("Expand is not idempotent: %q", again)
			}
			if offs := Find([]byte(c.want)); offs != nil {
				t.Errorf("Find on the result: %v", offs)
			}
		})
	}
}

func TestFindLines(t *testing.T) {
	src := []byte("class A {\n\tfun f() {\t}\n\t$s = \"\t\";\n}\n")
	offs := Find(src)
	if len(offs) != 3 || offs[0] != 10 {
		t.Fatalf("Find = %v, want three tabs, the first at 10", offs)
	}
	if n := Lines(src, offs); n != 2 {
		t.Errorf("Lines = %d, want 2", n)
	}
	if Find([]byte("$s = 'a\tb';\n")) != nil {
		t.Error("a tab in a string is allowed")
	}
}

// TestHeredocValues runs php on heredocs and nowdocs before and after
// Expand: the strings must not change. php is the oracle.
func TestHeredocValues(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php in PATH")
	}
	src := "$x = 'X';\n" +
		"function f(string $x) {\n" +
		"\t$a = <<<EOT\n\t\tone\ttab\n\n\t\t\tdeeper {$x}\n\t\t\t$x line\n\t\tEOT;\n" +
		"\t$b = <<<'EOT'\n\t\tnow\tdoc\n\t\t\tin\n\t\tEOT;\n" +
		"\t$c = <<<EOT\n\tcolumn\t0\nEOT;\n" +
		"\t$d = \"q\tq\";\n" +
		"\treturn [$a, $b, $c, $d];\n" +
		"}\n" +
		"echo json_encode(f($x)), \"\\n\";\n"
	run := func(code string) string {
		dir := t.TempDir()
		path := filepath.Join(dir, "t.php")
		if err := os.WriteFile(path, []byte("<?php\n"+code), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(php, path).CombinedOutput()
		if err != nil {
			t.Fatalf("php: %v\n%s", err, out)
		}
		return string(out)
	}
	before, after := run(src), run(string(Expand([]byte(src))))
	if before != after {
		t.Errorf("values changed:\nbefore %s\nafter  %s", before, after)
	}
	if !strings.Contains(before, `deeper X`) {
		t.Errorf("unexpected output %s", before)
	}
}
