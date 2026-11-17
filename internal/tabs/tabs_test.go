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
		{"indentation", "class A {\n\tf() {\n\t\treturn 1;\n\t}\n}\n",
			"class A {\n    f() {\n        return 1;\n    }\n}\n"},
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
		// PHP refuses the next three (text left of the closing indentation,
		// tabs mixed with spaces), and the parser reads them: Expand must
		// still settle in one run.
		{"heredoc: a tab after text left of the closing indentation is text", "$s = <<<EOT\n000\t\n  \tEOT;\n",
			"$s = <<<EOT\n000\t\n    EOT;\n"},
		{"heredoc: the prefix reaches as far as the closing indentation's columns", "$s = <<<EOT\n \t\tx\n\t\tEOT;\n",
			"$s = <<<EOT\n        x\n        EOT;\n"},
		{"heredoc: a tab past the closing indentation's columns is text", "$s = <<<EOT\n  \t\tx\n  \tEOT;\n",
			"$s = <<<EOT\n    \tx\n    EOT;\n"},
		// PHP's scanner ends a line at \r\n, \n and a \r alone: a column
		// starts again after each, and a heredoc's body lines start there.
		{"a lone \\r ends a line", "$a = 1;\r\t$b = <<<EOT\r\t\tx\r\t\ty\r\t\tEOT;\r",
			"$a = 1;\r    $b = <<<EOT\r        x\r        y\r        EOT;\r"},
		{"\\r\\n ends a line", "$a = 1;\r\n\t$b = <<<EOT\r\n\t\tx\r\n\t\ty\r\n\t\tEOT;\r\n",
			"$a = 1;\r\n    $b = <<<EOT\r\n        x\r\n        y\r\n        EOT;\r\n"},
		{"a docblock's lines after \\r", "\t/**\r\t * a\tb\r\t */\r",
			"    /**\r     * a\tb\r     */\r"},
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
	src := []byte("class A {\n\tf() {\t}\n\t$s = \"\t\";\n}\n")
	offs := Find(src)
	if len(offs) != 3 || offs[0] != 10 {
		t.Fatalf("Find = %v, want three tabs, the first at 10", offs)
	}
	if n := Lines(src, offs); n != 2 {
		t.Errorf("Lines = %d, want 2", n)
	}
	if n := Lines([]byte("\ta;\r\tb;\r\n\tc;\n"), []int{0, 4, 9}); n != 3 {
		t.Errorf("Lines over \\r, \\r\\n and \\n = %d, want 3", n)
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
	for _, eol := range []string{"\n", "\r", "\r\n"} {
		code := strings.ReplaceAll(src, "\n", eol)
		before, after := run(code), run(string(Expand([]byte(code))))
		if before != after {
			t.Errorf("%q line breaks: values changed:\nbefore %s\nafter  %s", eol, before, after)
		}
	}
	before := run(src)
	if !strings.Contains(before, `deeper X`) {
		t.Errorf("unexpected output %s", before)
	}
}
