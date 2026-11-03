package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/trace"
)

// TestTraceStream: stdin passes through line by line, byte for byte where
// there is nothing to rewrite, the last line's missing line break and CRLF
// included.
func TestTraceStream(t *testing.T) {
	tr := trace.New(nil)
	tr.ReadFile = func(path string) ([]byte, error) {
		return []byte("<?php\n\necho 1;\n// phaenon-map v1 Foo.phn 1-2=1 3:1\n"), nil
	}
	in := "first\r\nat Foo.php:3\r\n\tno position: Foo.php\nlast without a break"
	want := "first\r\nat Foo.phn:1\r\n\tno position: Foo.php\nlast without a break"
	var out bytes.Buffer
	if err := traceStream(tr, strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != want {
		t.Errorf("got %q\nwant %q", out.String(), want)
	}
}

// TestTraceStreamReport: stdin that opens as an XML report is read whole and
// rewritten as one; a PHP error written as HTML stays text, rewritten a line
// at a time.
func TestTraceStreamReport(t *testing.T) {
	tr := trace.New(nil)
	tr.ReadFile = func(path string) ([]byte, error) {
		return []byte("<?php\n\necho 1;\n// phaenon-map v1 Foo.phn 1-2=1 3:1\n"), nil
	}
	cases := []struct{ in, want string }{
		{"<?xml version=\"1.0\"?>\n<checkstyle>\n<file name=\"Foo.php\">\n  <error line=\"3\" column=\"1\" />\n</file>\n</checkstyle>\n",
			"<?xml version=\"1.0\"?>\n<checkstyle>\n<file name=\"Foo.phn\">\n  <error line=\"1\" />\n</file>\n</checkstyle>\n"},
		{"<testsuite name=\"phpstan\"><testcase name=\"Foo.php:3\"/></testsuite>",
			"<testsuite name=\"phpstan\"><testcase name=\"Foo.phn:1\"/></testsuite>"},
		{"<br />\n<b>Fatal error</b>:  Uncaught Exception in Foo.php:3\n",
			"<br />\n<b>Fatal error</b>:  Uncaught Exception in Foo.phn:1\n"},
		{"<?xml version=\"1.0\"?>\n<log>at Foo.php:3</log>\n", "<?xml version=\"1.0\"?>\n<log>at Foo.phn:1</log>\n"},
	}
	for _, c := range cases {
		var out bytes.Buffer
		if err := traceStream(tr, strings.NewReader(c.in), &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != c.want {
			t.Errorf("got %q\nwant %q", out.String(), c.want)
		}
	}
}
