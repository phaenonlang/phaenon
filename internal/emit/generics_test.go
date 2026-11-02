package emit

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/stdlib"
)

// reTargets reads the targets a golden file is compiled for from its first
// line, `// targets: 8.5, 8.6` (or `// target: 8.6`); without one it is
// compiled for the floor, PHP 8.5. The first target's output is X.php,
// another's X.<version>.php.
var reTargets = regexp.MustCompile(`^//\s*targets?:\s*([0-9., ]+)`)

func goldenTargets(t *testing.T, src []byte) []phpversion.Version {
	m := reTargets.FindSubmatch(src)
	if m == nil {
		return []phpversion.Version{phpversion.Floor}
	}
	var out []phpversion.Version
	for _, f := range strings.FieldsFunc(string(m[1]), func(r rune) bool { return r == ',' || r == ' ' }) {
		v, err := phpversion.Parse(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// phpFor returns a php binary that runs code for the target: the php on
// PATH for 8.5, PHAENON_PHP86 for 8.6; "" when there is none.
func phpFor(v phpversion.Version) string {
	if v == phpversion.PHP86 {
		return os.Getenv("PHAENON_PHP86")
	}
	php, _ := exec.LookPath("php")
	return php
}

// TestGenerics compiles each testdata/*.phn through the checker and the
// emitter and compares the output with the .php next to it, once per target
// the file names. The output must also pass `php -l` under a php of its
// target (PHAENON_PHP86 names one for 8.6), and PHPStan at level max when
// PHAENON_PHPSTAN names the binary and that PHPStan parses the output. A
// file compiled for several targets runs every output under PHP 8.6 and
// the 8.5 one under the php on PATH too: they must print the same.
// PHAENON_UPDATE=1 rewrites the expected files.
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
		concurrent, err := Concurrent()
		if err != nil {
			t.Fatal(err)
		}
		for name, src := range concurrent {
			compiled[name] = src
		}
		for _, s := range append(stdlib.Runtime(), stdlib.Concurrent()...) {
			name, src := packagePath(s.Path), s.Src
			if strings.HasSuffix(s.Path, ".phn") {
				src = compiled[name]
			}
			path := filepath.Join(dir, filepath.FromSlash(name))
			os.MkdirAll(filepath.Dir(path), 0o755)
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
			targets := goldenTargets(t, src)
			outputs := map[phpversion.Version]string{}
			for i, target := range targets {
				wantPath := strings.TrimSuffix(path, ".phn") + ".php"
				if i > 0 {
					wantPath = strings.TrimSuffix(path, ".phn") + "." + target.String() + ".php"
				}
				outputs[target] = wantPath
				compileGolden(t, path, src, target, wantPath, phpstan, runtimeStubs)
			}
			if len(targets) > 1 {
				sameBehaviour(t, targets, outputs)
			}
		})
	}
}

// compileGolden compiles one golden file for a target and checks the
// output against wantPath, php -l and PHPStan.
func compileGolden(t *testing.T, path string, src []byte, target phpversion.Version, wantPath, phpstan string, runtimeStubs []string) {
	t.Helper()
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	p.SetTarget(target)
	f, err := p.AddFile(path, src, lexer.ModePhaenon, true)
	if err != nil {
		t.Fatal(err)
	}
	c := checker.New(p, checker.Options{})
	for _, d := range c.Check() {
		if d.Severity == checker.Error {
			t.Errorf("checker (target %s): %s:%d: %s: %s", target, d.File, d.Line, d.Code, d.Message)
		}
	}
	got, err := File(src, Options{SourceName: path, StrictTypes: true, Program: p, File: f})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("PHAENON_UPDATE") != "" {
		os.WriteFile(wantPath, got, 0o644)
	}
	want, _ := os.ReadFile(wantPath)
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s (PHAENON_UPDATE=1 to rewrite):\n%s", wantPath, got)
	}
	if php := phpFor(target); php != "" {
		out, err := exec.Command(php, "-l", wantPath).CombinedOutput()
		if err != nil {
			t.Errorf("php -l (target %s): %s", target, out)
		}
	}
	if phpstan != "" && target.AtLeast(phpversion.PHP86) && f.Partials != nil {
		// The parity PHPStan (2.2) does not parse PHP 8.6's placeholders.
		t.Logf("%s: PHPStan skipped: it does not parse PHP 8.6 placeholders", wantPath)
		phpstan = ""
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
}

// sameBehaviour runs the outputs of one golden file compiled for several
// targets: under PHP 8.6 every output, under the php on PATH the 8.5 one.
// They must print the same, so a lowering for 8.5 behaves as PHP 8.6 runs
// the code written for it, evaluation order included.
func sameBehaviour(t *testing.T, targets []phpversion.Version, outputs map[phpversion.Version]string) {
	t.Helper()
	run := func(php, path string) string {
		out, err := exec.Command(php, "-n", "-d", "display_errors=1", "-d", "error_reporting=-1", path).CombinedOutput()
		if err != nil {
			t.Errorf("%s %s: %v\n%s", php, path, err, out)
		}
		return string(out)
	}
	var want, wantFrom string
	compare := func(php, path string) {
		got := run(php, path)
		if wantFrom == "" {
			want, wantFrom = got, php+" "+path
			return
		}
		if got != want {
			t.Errorf("%s %s prints differently from %s:\n--- got\n%s\n--- want\n%s", php, path, wantFrom, got, want)
		}
	}
	if php86 := phpFor(phpversion.PHP86); php86 != "" {
		for _, v := range targets {
			compare(php86, outputs[v])
		}
	}
	if php85 := phpFor(phpversion.PHP85); php85 != "" {
		if path, ok := outputs[phpversion.PHP85]; ok {
			compare(php85, path)
		}
	}
}
