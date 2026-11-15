package checker

import (
	"fmt"
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
// match exactly. The opening comment lines of the directory's first file,
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
			p := newProgram(t)
			type key struct {
				file string
				line int
				code string
			}
			var wants []key
			wantMsg := map[key][]string{}
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
					k := key{path, w.line, w.code}
					wants = append(wants, k)
					wantMsg[k] = append(wantMsg[k], w.msg)
				}
			}
			got := map[key][]Diagnostic{}
			for _, d := range New(p, opts).Check() {
				k := key{d.File, d.Line, d.Code}
				got[k] = append(got[k], d)
			}
			matched := map[key]int{}
			for _, k := range wants {
				ds := got[k]
				i := matched[k]
				if i >= len(ds) {
					t.Errorf("%s:%d: expected %s, not reported", k.file, k.line, k.code)
					continue
				}
				matched[k]++
				if msg := wantMsg[k][i]; msg != "" && !strings.Contains(ds[i].Message, msg) {
					t.Errorf("%s:%d: %s message %q does not contain %q", k.file, k.line, k.code, ds[i].Message, msg)
				}
			}
			var unexpected []string
			for k, ds := range got {
				for i := matched[k]; i < len(ds); i++ {
					unexpected = append(unexpected, fmt.Sprintf("%s:%d: unexpected %s: %s", k.file, k.line, k.code, ds[i].Message))
				}
			}
			sort.Strings(unexpected)
			for _, u := range unexpected {
				t.Error(u)
			}
		})
	}
}
