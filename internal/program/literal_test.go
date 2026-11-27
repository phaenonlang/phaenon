package program

import (
	"encoding/hex"
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// TestLiteralValues holds string literals to the values php 8.5 gives
// them (var_dump of each literal, as hex): the escapes of a double-quoted
// string and of a heredoc, the closing marker's indentation, and what
// stays as written.
func TestLiteralValues(t *testing.T) {
	for _, c := range []struct{ src, hex string }{
		{`'plain'`, "706c61696e"},
		{`'it\'s \\ \n \x41'`, "69742773205c205c6e205c783431"},
		{`'\\\\'`, "5c5c"},
		{`b'x\''`, "7827"},
		{`"plain"`, "706c61696e"},
		{`"\x41"`, "41"},
		{`"\X41\x4\x4G\xZ\x"`, "410404475c785a5c78"},
		{`"\101\0\08\1234\400\777"`, "41000038533400ff"},
		{`"\n\r\t\v\e\f"`, "0a0d090b1b0c"},
		{`"\\ \$ \" \' ` + "\\`" + `"`, "5c20242022205c27205c60"},
		{`"\q \{ \["`, "5c71205c7b205c5b"},
		{`"\u{41}\u{1F600}\u{e9}\u{D800}\u{0}"`, "41f09f9880c3a9eda08000"},
		{`"\u \uZ \u004"`, "5c75205c755a205c75303034"},
		{`B"\x41"`, "41"},
		{`"a\\"`, "615c"},
		{`"$"`, "24"},
		{`"\\x41"`, "5c783431"},
		{`"{\x41}"`, "7b417d"},
		{"<<<EOT\nA\\x41\\\"\\$\\101\nEOT", "41415c222441"},
		{"<<<EOT\n    a\n      b\\tc\n\n  \n    EOT", "610a20206209630a0a"},
		{"<<<\"EOT\"\n\tx\\n\n\tEOT", "780a"},
		{"<<<'EOT'\n  \\x41\\\\ \\'\n  EOT", "5c7834315c5c205c27"},
		{"<<<EOT\nEOT", ""},
		{"<<<EOT\n\nEOT", ""},
		{"<<<EOT\r\nA\r\nB\r\nEOT", "410d0a42"},
		{"b<<<EOT\n  \\u{1F600}\n  EOT", "f09f9880"},
		{"<<< EOT\n\\e\nEOT", "1b"},
	} {
		e, err := parser.ParseExpr([]byte(c.src), lexer.ModePhaenon)
		if err != nil {
			t.Errorf("%q: %v", c.src, err)
			continue
		}
		var got string
		switch e := e.(type) {
		case *ast.StringLit:
			got = StringValue(e)
		case *ast.InterpolatedString:
			v, ok := HeredocValue(e)
			if !ok {
				t.Errorf("%q: no value", c.src)
				continue
			}
			got = v
		default:
			t.Errorf("%q: parsed as %T", c.src, e)
			continue
		}
		if h := hex.EncodeToString([]byte(got)); h != c.hex {
			t.Errorf("%q: value %s, php's %s", c.src, h, c.hex)
		}
	}
}

// TestHeredocValueInterpolated: a heredoc with an interpolation has no
// value known before it runs.
func TestHeredocValueInterpolated(t *testing.T) {
	e, err := parser.ParseExpr([]byte("<<<EOT\na $b\nEOT"), lexer.ModePhaenon)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := HeredocValue(e.(*ast.InterpolatedString)); ok {
		t.Errorf("value %q, want none", v)
	}
}
