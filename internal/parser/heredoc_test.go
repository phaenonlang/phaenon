package parser

import (
	"fmt"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

// TestHeredocIndentation: a heredoc or nowdoc whose body lines do not
// start with the closing marker's indentation, or whose indentation mixes
// tabs and spaces, is refused as PHP's scanner refuses it; the verdicts,
// messages and lines are php's (8.5).
func TestHeredocIndentation(t *testing.T) {
	cases := []struct{ name, src, msg string }{
		{"text left of the indentation", "$s = <<<EOT\n000\t\n    EOT;\n", "at least 4) line 2"},
		{"a later line short", "$s = <<<EOT\n  a\n x\n  EOT;\n", "at least 2) line 3"},
		{"a variable starting a line", "$s = <<<EOT\n  a\n$x\n  EOT;\n", "at least 2) line 3"},
		{"a variable starting the body", "$s = <<<EOT\n$x\n  EOT;\n", "at least 2) line 2"},
		{"an interpolation short", "$s = <<<EOT\n  {$x}\n {$x}\n  EOT;\n", "at least 2) line 3"},
		{"text after an interpolation's line", "$s = <<<EOT\n  a {$x}\nb\n  EOT;\n", "at least 2) line 3"},
		{"a nowdoc", "$s = <<<'EOT'\n0\n  EOT;\n", "at least 2) line 2"},
		{"\\r line breaks", "$s = <<<EOT\r  a\rb\r  EOT;\r", "at least 2) line 3"},
		{"tabs in a body of spaces", "$s = <<<EOT\n  a\n \tb\n  EOT;\n", "cannot be mixed line 3"},
		{"a body of tabs, spaces closing", "$s = <<<EOT\n\ta\n  EOT;\n", "cannot be mixed line 2"},
		{"a mixed closing indentation", "$s = <<<EOT\n \ta\n \tEOT;\n", "cannot be mixed line 2"},
		{"a mixed closing indentation after an interpolation", "$s = <<<EOT\n \ta {$x}\n \tb\n \tEOT;\n", "cannot be mixed line 2"},
		{"a mixed whitespace-only line", "$s = <<<EOT\n \t\n  EOT;\n", "cannot be mixed line 2"},
		{"an empty heredoc, mixed", "$s = <<<EOT\n \tEOT;\n", "cannot be mixed line 2"},
		{"an indented variable", "$s = <<<EOT\n  $x\n  EOT;\n", ""},
		{"empty and short whitespace lines", "$s = <<<EOT\n  a\n\n \n  EOT;\n", ""},
		{"deeper lines", "$s = <<<EOT\n    a\n  b\n  EOT;\n", ""},
		{"no indentation", "$s = <<<EOT\nx\nEOT;\n", ""},
		{"interpolations past the indentation", "$s = <<<EOT\n  a $x b\n  {$x}\n  EOT;\n", ""},
	}
	for _, c := range cases {
		for _, mode := range []lexer.Mode{lexer.ModePhaenon, lexer.ModePHP} {
			src := c.src
			if mode == lexer.ModePHP {
				src = "<?php\n" + src
			}
			_, err := Parse([]byte(src), mode)
			got := ""
			if pe, ok := err.(*Error); ok {
				line := pe.Line
				if mode == lexer.ModePHP {
					line-- // the open tag's line
				}
				got = fmt.Sprintf("%s line %d", pe.Msg, line)
			} else if err != nil {
				got = err.Error()
			}
			if c.msg == "" && got != "" || !strings.Contains(got, c.msg) {
				t.Errorf("%s (mode %v): %q gives %q, want %q", c.name, mode, c.src, got, c.msg)
			}
		}
	}
}
