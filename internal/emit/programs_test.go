package emit

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
)

// goldenProgram is one directory of testdata/programs: a program of
// several files, checked together.
type goldenProgram struct {
	p     *program.Program
	files []string // the .phn paths, sorted
	srcs  map[string][]byte
}

// loadGoldenProgram reads every .phn under dir into one program and checks
// it: the goldens compile without a finding of the checker's.
func loadGoldenProgram(t *testing.T, dir string) *goldenProgram {
	t.Helper()
	g := &goldenProgram{p: program.New(), srcs: map[string][]byte{}}
	if err := g.p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(path) == ".phn" {
			g.files = append(g.files, path)
		}
		return nil
	})
	sort.Strings(g.files)
	if len(g.files) == 0 {
		t.Fatalf("%s: no .phn files", dir)
	}
	for _, path := range g.files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		g.srcs[path] = src
		if _, err := g.p.AddFile(path, src, lexer.ModePhaenon, true); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	for _, d := range checker.New(g.p, checker.Options{}).Check() {
		if d.Severity == checker.Error {
			t.Errorf("checker: %s:%d: %s: %s", d.File, d.Line, d.Code, d.Message)
		}
	}
	return g
}

// TestPrograms compiles the programs of several files: every directory of
// testdata/programs is one, each X.phn in it compiled to the X.php beside
// it with the whole directory as its program, as phn build compiles a
// project. It is where a declaration of one file shapes the output of
// another: a type alias, which belongs to its namespace
// (design/generics.md §11.6). Each output is held to TestGenerics's
// checks, `php -l` and the line map's property among them, and PHPStan
// analyses the directory's outputs together at level max when
// PHAENON_PHPSTAN names the binary. PHAENON_UPDATE=1 rewrites the expected
// files.
func TestPrograms(t *testing.T) {
	dirs, _ := filepath.Glob(filepath.Join("testdata", "programs", "*"))
	if len(dirs) == 0 {
		t.Fatal("no golden programs")
	}
	php, _ := exec.LookPath("php")
	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			g := loadGoldenProgram(t, dir)
			for _, path := range g.files {
				src := g.srcs[path]
				f := g.p.File(path)
				got, w, err := compile(src, f.AST, Options{SourceName: path, StrictTypes: true, Program: g.p, File: f})
				if err != nil {
					t.Fatalf("%s: %v", path, err)
				}
				wantPath := strings.TrimSuffix(path, ".phn") + ".php"
				if os.Getenv("PHAENON_UPDATE") != "" {
					os.WriteFile(wantPath, got, 0o644)
				}
				want, _ := os.ReadFile(wantPath)
				if !bytes.Equal(got, want) {
					t.Errorf("output differs from %s (PHAENON_UPDATE=1 to rewrite):\n%s", wantPath, got)
					continue
				}
				checkLineMap(t, path, src, got, w)
				checkVerbatim(t, path, src, got, w.verbatim())
				if expectPHPOnly(t, path, got) {
					t.Errorf("%s: a golden in a namespace under Phaenon\\ goes unscanned", path)
				}
				if php != "" {
					if out, err := exec.Command(php, "-l", wantPath).CombinedOutput(); err != nil {
						t.Errorf("php -l: %s", out)
					}
				}
			}
			if phpstan := os.Getenv("PHAENON_PHPSTAN"); phpstan != "" && !t.Failed() {
				cfg := filepath.Join(t.TempDir(), "phpstan.neon")
				os.WriteFile(cfg, []byte("parameters:\n    level: max\n"), 0o644)
				abs, err := filepath.Abs(dir)
				if err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command(phpstan, "analyse", "--no-progress", "--error-format=raw", "-c", cfg, abs).CombinedOutput(); err != nil {
					t.Errorf("phpstan:\n%s", out)
				}
			}
		})
	}
}
