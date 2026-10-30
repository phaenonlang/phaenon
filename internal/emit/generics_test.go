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
	// PHPStan reads the runtime package's declarations beside the output;
	// the stubs written in Phaenon are compiled first, as the package is.
	var runtimeStubs []string
	if phpstan != "" || php != "" {
		dir := t.TempDir()
		compiled, err := Stdlib()
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range stdlib.Runtime() {
			name, src := filepath.Base(s.Path), s.Src
			if strings.HasSuffix(name, ".phn") {
				name = strings.TrimSuffix(name, ".phn") + ".php"
				src = compiled[name]
			}
			path := filepath.Join(dir, name)
			os.WriteFile(path, src, 0o644)
			runtimeStubs = append(runtimeStubs, path)
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
			got, err := File(src, Options{SourceName: path, StrictTypes: true, Program: p, File: f})
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
				os.WriteFile(cfg, []byte("parameters:\n    level: max\n    scanFiles:\n        - "+strings.Join(runtimeStubs, "\n        - ")+"\n"), 0o644)
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
