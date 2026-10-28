package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// TestLift converts every testdata/lift/*.php and compares the result with
// the .phn beside it and the notes with the .notes file. PHAENON_UPDATE=1
// rewrites the expectations.
func TestLift(t *testing.T) {
	inputs, _ := filepath.Glob("testdata/lift/*.php")
	if len(inputs) == 0 {
		t.Fatal("no fixtures")
	}
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".php")
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			got, notes, err := Convert(src, Options{Generics: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parser.Parse(got, lexer.ModePhaenon); err != nil {
				t.Fatalf("output does not parse: %v\n%s", err, got)
			}
			var b strings.Builder
			for _, n := range notes {
				fmt.Fprintf(&b, "%d: %s\n", n.Line, n.Msg)
			}
			wantFile := strings.TrimSuffix(in, ".php") + ".phn"
			notesFile := strings.TrimSuffix(in, ".php") + ".notes"
			if os.Getenv("PHAENON_UPDATE") != "" {
				os.WriteFile(wantFile, got, 0o644)
				os.WriteFile(notesFile, []byte(b.String()), 0o644)
				return
			}
			want, _ := os.ReadFile(wantFile)
			if string(want) != string(got) {
				t.Errorf("output differs from %s (PHAENON_UPDATE=1 to rewrite):\n%s", wantFile, got)
			}
			wantNotes, _ := os.ReadFile(notesFile)
			if string(wantNotes) != b.String() {
				t.Errorf("notes differ from %s (PHAENON_UPDATE=1 to rewrite):\n%s", notesFile, b.String())
			}
			// Lifting is idempotent: the output has nothing left to lift.
			again, notes2, err := Lift(got)
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != string(got) {
				t.Errorf("second lift changed the output:\n%s", again)
			}
			if len(notes2) != len(notes) {
				t.Errorf("second lift gave %d notes, first %d", len(notes2), len(notes))
			}
		})
	}
}
