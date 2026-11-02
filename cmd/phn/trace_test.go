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
