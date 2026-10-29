package emit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/stdlib"
)

// TestGenerics compiles each testdata/*.phn through the checker and the
// emitter and compares the output with the .php next to it. The output
// must also pass `php -l` when php is installed, and PHPStan at level max
// when PHAENON_PHPSTAN names the binary. PHAENON_UPDATE=1 rewrites the
// expected files.
func TestGenerics(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.phn")
	if len(files) == 0 {
		t.Skip("no testdata")
	}
	phpstan := os.Getenv("PHAENON_PHPSTAN")
	php, _ := exec.LookPath("php")
	var runtimeStub string
	if phpstan != "" || php != "" {
		dir := t.TempDir()
		for _, s := range stdlib.Runtime() {
			runtimeStub = filepath.Join(dir, filepath.Base(s.Path))
			os.WriteFile(runtimeStub, s.Src, 0o644)
		}
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			p := program.New()
			if err := p.LoadStubs(); err != nil {
				t.Fatal(err)
			}
			f, err := p.AddFile(path, src, lexer.ModePhaenon, true)
			if err != nil {
				t.Fatal(err)
			}
			c := checker.New(p, checker.Options{})
			for _, d := range c.Check() {
				if d.Severity == checker.Error {
					t.Errorf("checker: %s:%d: %s: %s", d.File, d.Line, d.Code, d.Message)
				}
			}
			got, err := Tree(src, f.AST, Options{SourceName: path, StrictTypes: true, Program: p, File: f})
			if err != nil {
				t.Fatal(err)
			}
			wantPath := strings.TrimSuffix(path, ".phn") + ".php"
			if os.Getenv("PHAENON_UPDATE") != "" {
				os.WriteFile(wantPath, got, 0o644)
			}
			want, _ := os.ReadFile(wantPath)
			if !bytes.Equal(got, want) {
				t.Errorf("output differs from %s (PHAENON_UPDATE=1 to rewrite):\n%s", wantPath, got)
			}
			if php != "" {
				out, err := exec.Command(php, "-l", wantPath).CombinedOutput()
				if err != nil {
					t.Errorf("php -l: %s", out)
				}
			}
			if phpstan != "" {
				cfg := filepath.Join(t.TempDir(), "phpstan.neon")
				os.WriteFile(cfg, []byte("parameters:\n    level: max\n    scanFiles:\n        - "+runtimeStub+"\n"), 0o644)
				out, err := exec.Command(phpstan, "analyse", "--no-progress", "--error-format=raw", "-c", cfg, wantPath).CombinedOutput()
				if err != nil {
					var findings []string
					for _, line := range strings.Split(string(out), "\n") {
						if strings.Contains(line, ".php:") {
							findings = append(findings, line)
						}
					}
					t.Errorf("phpstan:\n%s", strings.Join(findings, "\n"))
				}
			}
		})
	}
}
