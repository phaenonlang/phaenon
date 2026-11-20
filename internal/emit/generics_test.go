package emit

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/linemap"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
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
// target (PHAENON_PHP86 names one for 8.6) and, when the file declares
// only classes, functions and constants, load under it: php links a class
// as it declares it, so a signature `php -l` accepts and PHP rejects (an
// override narrowing a parameter) fails here, without PHPStan. Nothing is
// autoloaded beside the output, and PHPStan reads nothing else: the output
// depends on PHP alone (design/roadmap.md D24). PHPStan runs at level max
// when PHAENON_PHPSTAN names the binary and that PHPStan parses the output. A file compiled for several targets runs every
// output under PHP 8.6 and the 8.5 one under the php on PATH too: they must
// print the same. PHAENON_UPDATE=1 rewrites the expected files.
func TestGenerics(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.phn")
	if len(files) == 0 {
		t.Skip("no testdata")
	}
	phpstan := os.Getenv("PHAENON_PHPSTAN")
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
				compileGolden(t, path, src, target, wantPath, phpstan)
			}
			if len(targets) > 1 {
				sameBehaviour(t, targets, outputs)
			}
		})
	}
}

// compileGolden compiles one golden file for a target and checks the
// output against wantPath, php -l, loading it under php where it declares
// only, and PHPStan.
func compileGolden(t *testing.T, path string, src []byte, target phpversion.Version, wantPath, phpstan string) {
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
		} else if declarationsOnly(f.AST.Stmts) {
			loadOutput(t, php, target, path, wantPath, got)
		}
	}
	if phpstan != "" && target.AtLeast(phpversion.PHP86) && f.Partials != nil {
		// The parity PHPStan (2.2) does not parse PHP 8.6's placeholders.
		t.Logf("%s: PHPStan skipped: it does not parse PHP 8.6 placeholders", wantPath)
		phpstan = ""
	}
	if phpstan != "" {
		cfg := filepath.Join(t.TempDir(), "phpstan.neon")
		os.WriteFile(cfg, []byte("parameters:\n    level: max\n"), 0o644)
		out, err := exec.Command(phpstan, "analyse", "--no-progress", "--error-format=raw", "-c", cfg, wantPath).CombinedOutput()
		if err != nil {
			// Raw findings are `path.php:N:message`; the map turns N into the
			// golden's line.
			m, _ := linemap.Find(got)
			var findings []string
			for _, line := range strings.Split(string(out), "\n") {
				i := strings.Index(line, ".php:")
				if i < 0 {
					continue
				}
				rest := line[i+len(".php:"):]
				if j := strings.IndexByte(rest, ':'); j > 0 && m != nil {
					if n, err := strconv.Atoi(rest[:j]); err == nil {
						if src, ok := m.Forward(n); ok {
							line = fmt.Sprintf("%s:%d (output line %d):%s", path, src, n, rest[j+1:])
						}
					}
				}
				findings = append(findings, line)
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

// declarationsOnly reports whether the statements declare and nothing
// else: classes, functions, constants and type aliases, under declare,
// namespace and use. Loading such a file runs no code of its own, only
// PHP's linking of its classes.
func declarationsOnly(stmts []ast.Stmt) bool {
	for _, st := range stmts {
		switch st := st.(type) {
		case *ast.Declare, *ast.Use, *ast.ClassDecl, *ast.FunctionDecl, *ast.ConstDecl, *ast.TypeAlias, *ast.Nop:
		case *ast.Namespace:
			if !declarationsOnly(st.Body) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// loader is the script that loads one output: `php loader.php output.php`.
// It requires the output, which declares its classes and links them, with
// no autoloader: an output that needed a class from elsewhere fails here.
const loader = `<?php
require $_SERVER['argv'][1];
`

// reErrorLine finds the output line in php's "in path on line N".
var reErrorLine = regexp.MustCompile(` in (\S+) on line (\d+)`)

// loadOutput requires the compiled file under php and reports what php
// prints, which must be nothing, with its output lines mapped back to the
// golden's.
func loadOutput(t *testing.T, php string, target phpversion.Version, path, wantPath string, got []byte) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "loader.php")
	if err := os.WriteFile(script, []byte(loader), 0o644); err != nil {
		t.Fatal(err)
	}
	// php names the file as it was given; the absolute path makes the
	// message's path the one the map is matched against.
	absPath, err := filepath.Abs(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(php, "-n", "-d", "display_errors=1", "-d", "error_reporting=-1", script, absPath).CombinedOutput()
	if err == nil && len(bytes.TrimSpace(out)) == 0 {
		return
	}
	text := strings.TrimSpace(string(out))
	// The stack trace below a fatal error names the loader alone.
	if i := strings.Index(text, "\nStack trace:"); i >= 0 {
		text = text[:i]
	}
	if m, _ := linemap.Find(got); m != nil {
		text = reErrorLine.ReplaceAllStringFunc(text, func(s string) string {
			sub := reErrorLine.FindStringSubmatch(s)
			n, _ := strconv.Atoi(sub[2])
			if src, ok := m.Forward(n); ok && sub[1] == absPath {
				return fmt.Sprintf(" in %s:%d (output line %d)", path, src, n)
			}
			return s
		})
	}
	t.Errorf("php loads the output (target %s): %v\n%s", target, err, text)
}

// TestBoundOfAnUnmovedFold: a pipe that folds computes the arguments after
// its placeholder where they stand, and nothing is refused for them; where
// the lowering does not write the run as nested calls, the stage is a
// closure after all, in which an assignment would write the closure's copy
// and __FUNCTION__ name the closure. phn check asks the lowering
// (UnfoldedBound, checker.Options.Unfolded), so that it reports the stage
// (P1002) where phn build refuses it, and neither where the run folds:
// whatever in the source or in the lowering's own edits decides that.
func TestBoundOfAnUnmovedFold(t *testing.T) {
	cases := map[string]struct {
		body  string
		moves bool
	}{
		"one line":           {"return ($s |> str_replace(?, 'b', ($t = 'xa'))) . $t;", true},
		"over lines":         {"return ($s\n        |> str_replace(?, 'b', 'c')\n        |> str_replace(?, 'd', ($t = 'xa'))) . $t;", true},
		"comment in args":    {"return ($s |> str_replace(?, 'b', /* why */ ($t = 'xa'))) . $t;", false},
		"comment before |>":  {"return ($s /* the subject */ |> str_replace(?, 'b', ($t = 'xa'))) . $t;", false},
		"heredoc":            {"return ($s |> str_replace(?, <<<EOT\n        b\n        EOT, ($t = 'xa'))) . $t;", false},
		"string over lines":  {"return ($s |> str_replace(?, 'b\nc', ($t = 'xa'))) . $t;", false},
		"callee over lines":  {"return ($s |> $r\n        ->rep(?, ($t = 'xa'))) . $t;", false},
		"comment on a stage": {"return ($s\n        |> str_replace(?, 'b', 'c') // first\n        |> str_replace(?, 'd', __FUNCTION__)) . $t;", false},
	}
	files := map[string]struct {
		src   string
		moves bool
	}{
		"block closure in an argument":            {"/** @param list<int> $xs */\nfunction tot(array $xs): int => array_sum($xs);\nfunction three(string $a, Closure $f, string $c): string => $a . gettype($f([1, 2])) . $c;\nfunction c(string $s): string {\n    $r = $s\n        |> three(?, tot(?), __FUNCTION__);\n    return $r;\n}\necho c('a'), \"\\n\";\n", true},
		"crlf":                                    {"function three(string $a, string $b, string $c): string => $a . $b . $c;\r\nfunction c(string $s): string {\r\n    return $s\r\n        |> three(?, 'b', __FUNCTION__);\r\n}\r\necho c('a'), \"\\n\";\r\n", true},
		"erased generic":                          {"/** @return list<T> */\nfunction make<T>(): array => [];\n/** @param list<int> $xs */\nfunction three(string $a, array $xs, string $c): string => $a . count($xs) . $c;\nfunction c(string $s): string => $s |> three(?, make<int>(), __FUNCTION__);\necho c('a'), \"\\n\";\n", true},
		"heredoc piped into an expression body":   {"function three(string $a, string $b, string $c): string => $a . $b . $c;\nfunction c(): string => <<<EOT\n    left\n    EOT |> three(?, 'b', __FUNCTION__);\necho c(), \"\\n\";\n", false},
		"match over lines":                        {"function three(string $a, string $b, string $c): string => $a . $b . $c;\nfunction c(string $s, int $n): string {\n    return $s |> three(?, match ($n) {\n        1 => 'one',\n        default => 'many',\n    }, __FUNCTION__);\n}\necho c('a', 1), \"\\n\";\n", true},
		"match on a stage's line":                 {"function three(string $a, string $b, string $c): string => $a . $b . $c;\nfunction c(string $s, int $n): string {\n    return $s\n        |> three(?, match ($n) {\n            1 => 'one',\n            default => 'many',\n        }, __FUNCTION__);\n}\necho c('a', 1), \"\\n\";\n", true},
		"pipe in an argument":                     {"function three(string $a, string $b, string $c): string => $a . $b . $c;\nfunction c(string $s): string => $s |> three(?, ('x' |> three(?, 'y', __FUNCTION__)), __FUNCTION__);\necho c('a'), \"\\n\";\n", true},
		"attribute in an argument":                {"function three(string $a, Closure $f, string $c): string => $a . gettype($f('k')) . $c;\nfunction c(string $s): string => $s |> three(?, fn(#[\\SensitiveParameter] string $k): string => $k, __FUNCTION__);\necho c('a'), \"\\n\";\n", true},
		"trailing comma":                          {"function three(string $a, string $b, string $c): string => $a . $b . $c;\nfunction c(string $s): string => $s |> three(?, 'b', __FUNCTION__,);\necho c('a'), \"\\n\";\n", true},
		"tuple type":                              {"/** @param callable((int, int)): int $f */\nfunction three(string $a, callable $f, string $c): string => $a . $f([1, 2]) . $c;\nfunction c(string $s): string => $s |> three(?, fn((int, int) $p): int => $p[0] + $p[1], __FUNCTION__);\necho c('a'), \"\\n\";\n", true},
		"yield and arrow function":                {"function three(string $a, string $b, string $c): string => $a . $b . $c;\n/** @return Generator<int, string> */\nfunction g(string $s): Generator {\n    yield $s |> three(?, 'b', __FUNCTION__);\n}\n$f = fn(string $s): string => $s |> three(?, 'c', __FUNCTION__);\necho implode(',', iterator_to_array(g('a'))), $f('z'), \"\\n\";\n", true},
		"comment in an inner call":                {"function pair(string $a, string $b): string => $a . '|' . $b;\n\nfunction inner(string $s, string $x): string => $s |> pair(?, strval(/* how many */ func_num_args()));\n", true},
		"string over lines in an expression body": {"function pair(string $a, string $b): string => $a . '|' . $b;\n\nfunction multi(string $s): string => $s |> pair(?, 'two\nlines' . __FUNCTION__);\n", false},
		"comment in a call passing by reference":  {"function pair(string $a, string $b): string => $a . '|' . $b;\n\nfunction byRef(string $s): string {\n    $r = $s |> pair(?, (string) preg_match(/* subject */ '/(a)/', $s, $m));\n    return $r . json_encode($m);\n}\n", true},
	}
	for name, c := range cases {
		files[name] = struct {
			src   string
			moves bool
		}{"final class R {\n    rep(string $s, string $t): string => $s . $t;\n}\n\nfunction c(string $s, R $r): string {\n    $t = 'o';\n    " + c.body + "\n}\n", c.moves}
	}
	for name, c := range files {
		t.Run(name, func(t *testing.T) {
			src := []byte(c.src)
			p := program.New()
			if err := p.LoadStubs(); err != nil {
				t.Fatal(err)
			}
			p.SetTarget(phpversion.PHP85)
			f, err := p.AddFile("A.phn", src, lexer.ModePhaenon, true)
			if err != nil {
				t.Fatal(err)
			}
			unfolded := func(p *program.Program, f *program.File) []ast.Expr {
				return UnfoldedBound(f.Src, Options{SourceName: f.Path, StrictTypes: true, Program: p, File: f})
			}
			refused := false
			for _, d := range checker.New(p, checker.Options{Unfolded: unfolded}).Check() {
				switch {
				case d.Code == "P1002":
					refused = true
				case d.Severity == checker.Error:
					t.Errorf("checker: %d: %s: %s", d.Line, d.Code, d.Message)
				}
			}
			_, err = File(src, Options{SourceName: "A.phn", StrictTypes: true, Program: p, File: f})
			switch {
			case refused == c.moves:
				t.Errorf("phn check refuses the stage: %v; its run folds: %v", refused, c.moves)
			case (err != nil) != refused:
				t.Errorf("phn check refuses the stage: %v; phn build: %v", refused, err)
			}
		})
	}
}

// TestUnfoldedBoundRecovers: a panic of the lowering that UnfoldedBound
// runs for the checker does not end phn check. Its answer is every stage
// still pending, folded or not, so that the check refuses each (P1002)
// rather than let through one the build might write as a closure.
func TestUnfoldedBoundRecovers(t *testing.T) {
	src := []byte("function c(string $s): string {\n    $t = 'o';\n" +
		"    $a = ($s |> str_replace(?, 'b', ($t = 'xa'))) . $t;\n" +
		"    $b = ($s |> str_replace(?, 'b', /* why */ ($t = 'xa'))) . $t;\n" +
		"    $c = $s |> str_replace(?, 'b', 'c');\n" +
		"    return $a . $b . $c;\n}\n")
	unfoldedHook = func() { panic("emit: a lowering's mistake") }
	t.Cleanup(func() { unfoldedHook = nil })
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	p.SetTarget(phpversion.PHP85)
	if _, err := p.AddFile("A.phn", src, lexer.ModePhaenon, true); err != nil {
		t.Fatal(err)
	}
	unfolded := func(p *program.Program, f *program.File) []ast.Expr {
		return UnfoldedBound(f.Src, Options{SourceName: f.Path, StrictTypes: true, Program: p, File: f})
	}
	var lines []int
	for _, d := range checker.New(p, checker.Options{Unfolded: unfolded}).Check() {
		switch {
		case d.Code == "P1002":
			lines = append(lines, d.Line)
		case d.Severity == checker.Error:
			t.Errorf("checker: %d: %s: %s", d.Line, d.Code, d.Message)
		}
	}
	if !slices.Equal(lines, []int{3, 4}) {
		t.Errorf("P1002 on lines %v, want the two stages that bind an assignment, [3 4]", lines)
	}
}
