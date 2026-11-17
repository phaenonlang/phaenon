package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
)

// TestPrintedLineIsTheCheckedSource: a finding is printed with the line of
// the source the program that found it was given, as phn watch's passes
// over a file that stops parsing and parses again need: a file that did
// not parse in an earlier pass is not shown as it was then.
func TestPrintedLineIsTheCheckedSource(t *testing.T) {
	const path = "src/a.phn"
	earlier := program.New()
	earlier.NoteUnparsed(path, []byte("namespace App;\n\nfunction f(): int {\n    return 1 +;\n    // the earlier line five\n}\n"))
	now := program.New()
	if _, err := now.AddFile(path, []byte("namespace App;\n\nfunction f(): int {\n    $x = 1;\n    return 'five';\n}\n"), lexer.ModePhaenon, true); err != nil {
		t.Fatal(err)
	}
	print := func(p *program.Program) string {
		t.Helper()
		loadedProgram = p
		defer func() { loadedProgram = nil }()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		saved := os.Stderr
		os.Stderr = w
		printDiag(checker.Diagnostic{File: path, Line: 5, Col: 12, EndLine: 5, EndCol: 18, Severity: checker.Error, Code: "P2202", Message: "a finding on line 5"})
		os.Stderr = saved
		w.Close()
		out, _ := io.ReadAll(r)
		return string(out)
	}
	if out := print(earlier); !strings.Contains(out, "the earlier line five") {
		t.Errorf("the earlier pass's file, which did not parse:\n%s", out)
	}
	if out := print(now); !strings.Contains(out, "return 'five';") || strings.Contains(out, "earlier") {
		t.Errorf("the file as this pass read it:\n%s", out)
	}
}
