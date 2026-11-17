package checker

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

// TestGoldenPrograms is the corpus of the rules that take several files:
// every directory of testdata/programs is one program, all of its .phn
// files checked together, with testdata/vendor as TestGolden has it. The
// expected diagnostics are TestGolden's `// want` comments, in whichever
// file the diagnostic is reported in; the (file, line, code) triples must
// match exactly, with their kinds and in both modes, as there. The opening comment lines of the directory's first file,
// in path order, set how the program is checked (`// target: 8.6`).
func TestGoldenPrograms(t *testing.T) {
	dirs, _ := filepath.Glob(filepath.Join("testdata", "programs", "*"))
	if len(dirs) == 0 {
		t.Fatal("no golden programs")
	}
	only := os.Getenv("PHAENON_GOLDEN")
	for _, dir := range dirs {
		name := filepath.Base(dir)
		if only != "" && !strings.Contains(name, only) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			var files []string
			filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() && filepath.Ext(path) == ".phn" {
					files = append(files, path)
				}
				return nil
			})
			sort.Strings(files)
			if len(files) == 0 {
				t.Fatal("no .phn files")
			}
			// Twice, as TestGolden checks a file: with the extra checks, and
			// without them.
			var both [2][]Diagnostic
			for mode, extraChecks := range []bool{true, false} {
				p := newProgram(t)
				var wants []want
				opts := Options{}
				for i, path := range files {
					src, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if i == 0 {
						target, o := goldenSetup(t, src)
						p.SetTarget(target)
						opts = o
					}
					if _, err := p.AddFile(path, src, lexer.ModePhaenon, true); err != nil {
						t.Fatalf("%s: parse: %v", path, err)
					}
					for _, w := range parseWants(string(src)) {
						w.file = path
						wants = append(wants, w)
					}
				}
				opts.NoExtraChecks = !extraChecks
				both[mode] = New(p, opts).Check()
				holdToWants(t, wants, both[mode], extraChecks)
			}
			lessTheExtraChecks(t, both[0], both[1])
		})
	}
}
