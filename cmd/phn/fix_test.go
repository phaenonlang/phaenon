package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestMain doubles as the PHP tool phn fix runs: with PHN_FAKE_TOOL set,
// the test binary rewrites the outputs under .src as the variable says.
func TestMain(m *testing.M) {
	if spec := os.Getenv("PHN_FAKE_TOOL"); spec != "" {
		fakeTool(spec)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeSpec struct {
	Replace [][2]string `json:"replace"`
	Create  []string    `json:"create"`
	// Rules, with the options that ask for a report (--format=json,
	// --output-format=json), are reported as applied to every file the
	// tool changes: php-cs-fixer's fixers, or Rector's rectors with the
	// line each names.
	Rules []fakeRule `json:"rules"`
}

type fakeRule struct {
	Name string `json:"name"`
	Line int    `json:"line"`
}

func fakeTool(spec string) {
	var s fakeSpec
	if err := json.Unmarshal([]byte(spec), &s); err != nil {
		panic(err)
	}
	var changed []string
	filepath.WalkDir(".src", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, _ := os.ReadFile(path)
		text := string(data)
		for _, r := range s.Replace {
			text = strings.ReplaceAll(text, r[0], r[1])
		}
		if text != string(data) {
			os.WriteFile(path, []byte(text), 0o644)
			changed = append(changed, path)
		}
		return nil
	})
	for _, path := range s.Create {
		os.WriteFile(path, []byte("<?php\n"), 0o644)
	}
	args := strings.Join(os.Args[1:], " ")
	switch {
	case strings.Contains(args, "--output-format=json"):
		type change struct {
			Rector string `json:"rector"`
			Line   int    `json:"line"`
		}
		type diff struct {
			File    string   `json:"file"`
			Changes []change `json:"changes"`
		}
		var doc struct {
			FileDiffs []diff `json:"file_diffs"`
		}
		for _, path := range changed {
			d := diff{File: path}
			for _, r := range s.Rules {
				d.Changes = append(d.Changes, change{Rector: `Rector\\Fake\\` + r.Name, Line: r.Line})
			}
			doc.FileDiffs = append(doc.FileDiffs, d)
		}
		json.NewEncoder(os.Stdout).Encode(doc)
	case strings.Contains(args, "--format=json"):
		type file struct {
			Name          string   `json:"name"`
			AppliedFixers []string `json:"appliedFixers"`
		}
		var doc struct {
			Files []file `json:"files"`
		}
		for _, path := range changed {
			f := file{Name: path}
			for _, r := range s.Rules {
				f.AppliedFixers = append(f.AppliedFixers, r.Name)
			}
			doc.Files = append(doc.Files, f)
		}
		json.NewEncoder(os.Stdout).Encode(doc)
	}
}

// fixTree is a project with a .phn and a plain PHP file the build copies.
func fixTree(t *testing.T) string {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"composer.json":                     `{"name": "acme/app", "require": {"php": ">=8.5"}}`,
		"phaenon.json":                      `{"roots": {"src": ".src"}}`,
		"vendor/composer/installed.json":    `{"packages": []}`,
		"vendor/composer/autoload_psr4.php": "<?php\n\nreturn array();\n",
		"src/Box.phn":                       boxSource,
		"src/data.php":                      "<?php\nreturn array(1,2);\n",
	})
	return root
}

const boxSource = "namespace App;\n\nfinal class Box {\n    __construct(private int $n) {}\n\n    add(int $a,int $b): int => $a+$b;\n}\n"

// fakeTool is the test binary under a tool's name, with what it changes.
func fakeToolCommand(t *testing.T, spec fakeSpec) string {
	t.Helper()
	return fakeNamedTool(t, "php-cs-fixer", spec)
}

func fakeNamedTool(t *testing.T, name string, spec fakeSpec) string {
	t.Helper()
	data, _ := json.Marshal(spec)
	t.Setenv("PHN_FAKE_TOOL", string(data))
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(t.TempDir(), name)
	if err := os.Symlink(self, tool); err != nil {
		t.Fatal(err)
	}
	return tool
}

var boxFixes = fakeSpec{
	Replace: [][2]string{
		{"int $a,int $b", "int $a, int $b"},
		{"$a+$b", "$a + $b"},
		{"): int\n    {", "): int {"}, // the compiler's brace of an expression body
		{"array(1,2)", "[1, 2]"},
	},
	Create: []string{".src/New.php"},
}

// The changes of the source's text are carried back, the copied file takes
// them all, and what the tool still changes after the rebuild — the
// compiler's brace — is reported, with exit status 1.
func TestFix(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	tool := fakeToolCommand(t, boxFixes)
	stdout, stderr, err := run(t, root, bin, "fix", "--", tool, "fix")
	if err == nil {
		t.Error("phn fix passed though the tool still changes the compiler's brace")
	}
	if want := "php-cs-fixer changed 3 files: 6 changes carried back to 2 sources, 1 in text the compiler wrote, 1 files outside the build\n" +
		"after the rebuild, php-cs-fixer still changes 2 files: 0 changes in 0 sources, 1 in text the compiler wrote, 1 files outside the build\n"; stdout != want {
		t.Errorf("stdout:\n%s\nwant\n%s", stdout, want)
	}
	if !strings.Contains(stderr, `src/Box.phn:6: php-cs-fixer changes text the compiler wrote: "\n   " → ""`) {
		t.Errorf("stderr:\n%s", stderr)
	}
	box, _ := os.ReadFile(filepath.Join(root, "src/Box.phn"))
	if want := strings.NewReplacer("int $a,int $b", "int $a, int $b", "$a+$b", "$a + $b").Replace(boxSource); string(box) != want {
		t.Errorf("Box.phn:\n%s", box)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "src/data.php")); string(data) != "<?php\nreturn [1, 2];\n" {
		t.Errorf("data.php: %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, ".src/New.php")); err == nil {
		t.Error("the file the tool created is still there")
	}
	if _, stderr, err := run(t, root, bin, "build", "--check"); err != nil {
		t.Errorf("the outputs are not the build's: %s", stderr)
	}
}

// --check writes no source, reports every change at its source line, and
// puts the outputs back.
func TestFixCheck(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	tool := fakeToolCommand(t, boxFixes)
	stdout, stderr, err := run(t, root, bin, "fix", "--check", "--", tool, "fix")
	if err == nil {
		t.Error("phn fix --check passed though the tool changes files")
	}
	if want := "php-cs-fixer would change 3 files: 6 changes in 2 sources, 1 in text the compiler wrote, 1 files outside the build\n"; stdout != want {
		t.Errorf("stdout:\n%s\nwant\n%s", stdout, want)
	}
	for _, want := range []string{
		`src/Box.phn:6: php-cs-fixer changes: "" → " "`,
		`src/data.php:2: php-cs-fixer changes: "array(" → "["`,
		`.src/New.php: created by php-cs-fixer`,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %s:\n%s", want, stderr)
		}
	}
	if box, _ := os.ReadFile(filepath.Join(root, "src/Box.phn")); string(box) != boxSource {
		t.Errorf("--check wrote Box.phn:\n%s", box)
	}
	if _, stderr, err := run(t, root, bin, "build", "--check"); err != nil {
		t.Errorf("the outputs are not the build's: %s", stderr)
	}

	tool = fakeToolCommand(t, fakeSpec{})
	if stdout, _, err := run(t, root, bin, "fix", "--", tool, "fix"); err != nil || stdout != "php-cs-fixer changed nothing\n" {
		t.Errorf("a tool that changes nothing: %v %q", err, stdout)
	}
}

// A folded pipe writes its operands into the call it makes: they are the
// source's text where they land, and a change of them is carried back.
func TestFixCarriesAFoldedPipesOperands(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	src := "namespace App;\n\nfinal class Names {\n    /** @param list<string> $names */\n" +
		"    shout(array $names): string => $names\n" +
		"        |> array_map(fn (string $n): string => $n.'!', ?)\n" +
		"        |> implode(',', ?);\n}\n"
	writeTree(t, root, map[string]string{"src/Names.phn": src})
	if _, stderr, err := run(t, root, bin, "build"); err != nil {
		t.Fatalf("phn build: %v\n%s", err, stderr)
	}
	out, _ := os.ReadFile(filepath.Join(root, ".src/Names.php"))
	if !strings.Contains(string(out), "return implode(',', array_map(fn (string $n): string => $n.'!', $names));") {
		t.Fatalf("the pipe did not fold:\n%s", out)
	}
	tool := fakeToolCommand(t, fakeSpec{Replace: [][2]string{
		{"fn (string $n)", "fn(string $n)"},
		{"$n.'!'", "$n . '!'"},
	}})
	stdout, stderr, err := run(t, root, bin, "fix", "--", tool, "fix")
	if err != nil {
		t.Errorf("phn fix: %v\n%s%s", err, stdout, stderr)
	}
	want := strings.NewReplacer("fn (string $n)", "fn(string $n)", "$n.'!'", "$n . '!'").Replace(src)
	if got, _ := os.ReadFile(filepath.Join(root, "src/Names.phn")); string(got) != want {
		t.Errorf("Names.phn:\n%s\nwant\n%s\n%s", got, want, stderr)
	}
}

// phn fmt lays the .phn out, then runs the project's fixer through phn fix;
// --check then finds nothing left. With "fmt": [] only the first layer runs.
func TestFmt(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	tool := fakeToolCommand(t, fakeSpec{Replace: [][2]string{{"int $a,int $b", "int $a, int $b"}}})
	writeTree(t, root, map[string]string{
		"phaenon.json": `{"roots": {"src": ".src"}, "fmt": [` + strconvQuote(tool) + `, "fix"]}`,
		"src/Box.phn":  "\n\nnamespace App;\n\nfinal class Box {\n    add(int $a,int $b): list < int > {\n        return [$a];\n    }\n}\n",
	})
	if _, stderr, err := run(t, root, bin, "fmt", "--check"); err == nil || !strings.Contains(stderr, "src/Box.phn: not in Phaenon's layout") {
		t.Errorf("--check passed on an unformatted file (%v): %s", err, stderr)
	}
	if box, _ := os.ReadFile(filepath.Join(root, "src/Box.phn")); !strings.HasPrefix(string(box), "\n\n") {
		t.Error("--check wrote Box.phn")
	}
	stdout, stderr, err := run(t, root, bin, "fmt")
	if err != nil {
		t.Fatalf("phn fmt: %v\n%s%s", err, stdout, stderr)
	}
	want := "namespace App;\n\nfinal class Box\n{\n    add(int $a, int $b): list<int> {\n        return [$a];\n    }\n}\n"
	if box, _ := os.ReadFile(filepath.Join(root, "src/Box.phn")); string(box) != want {
		t.Errorf("Box.phn:\n%s\nwant\n%s", box, want)
	}
	if stdout, stderr, err := run(t, root, bin, "fmt", "--check"); err != nil {
		t.Errorf("--check after phn fmt: %v\n%s%s", err, stdout, stderr)
	}

	writeTree(t, root, map[string]string{
		"phaenon.json": `{"roots": {"src": ".src"}, "fmt": []}`,
		"src/Box.phn":  "namespace App;\n\nfinal class Box\n{\n    add(int $a,int $b): int => $a;\n}\n",
	})
	if stdout, _, err := run(t, root, bin, "fmt", "--check"); err != nil || strings.Contains(stdout, "php-cs-fixer") {
		t.Errorf(`"fmt": [] still ran the fixer, or the layout failed (%v):\n%s`, err, stdout)
	}
}

// phn fmt rewrites the spellings roadmap D26 and D27 withdrew
// (design/tooling.md §5.6): in a project the files holding a type test are
// checked, so that a test whose PHP depends on the operand's type gets it
// (`is string` on bytes is mb_check_encoding()); --stdin, which has no
// program, makes the rewrites that need no type and reports the rest.
func TestFmtRewritesWithdrawnSpellings(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	src := "namespace App;\n\nfinal class Text\n{\n    public static fun of(bytes $b, mixed $m): string\n    {\n" +
		"        if ($m is Text && $b is string) {\n            return $b;\n        }\n\n        return $b as string;\n    }\n}\n"
	writeTree(t, root, map[string]string{
		"phaenon.json": `{"roots": {"src": ".src"}, "fmt": []}`,
		"src/Text.phn": src,
	})
	if _, stderr, err := run(t, root, bin, "fmt", "--check"); err == nil || !strings.Contains(stderr, "src/Text.phn: not in Phaenon's layout") {
		t.Errorf("--check passed on withdrawn spellings (%v): %s", err, stderr)
	}
	if stdout, stderr, err := run(t, root, bin, "fmt"); err != nil {
		t.Fatalf("phn fmt: %v\n%s%s", err, stdout, stderr)
	}
	want := "namespace App;\n\nfinal class Text\n{\n    public static of(bytes $b, mixed $m): string {\n" +
		"        if ($m instanceof Text && mb_check_encoding($b, 'UTF-8')) {\n            return $b;\n        }\n\n" +
		"        return mb_check_encoding($b, 'UTF-8') ? $b : throw new \\ValueError('not valid UTF-8');\n    }\n}\n"
	if got, _ := os.ReadFile(filepath.Join(root, "src/Text.phn")); string(got) != want {
		t.Errorf("Text.phn:\n%s\nwant\n%s", got, want)
	}
	if stdout, stderr, err := run(t, root, bin, "check"); err != nil {
		t.Errorf("phn check after phn fmt: %v\n%s%s", err, stdout, stderr)
	}

	cmd := exec.Command(bin, "fmt", "--stdin")
	cmd.Stdin = strings.NewReader(src)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Run()
	if got := stdout.String(); !strings.Contains(got, "public static of(") || !strings.Contains(got, "$m instanceof Text && $b is string") ||
		!strings.Contains(got, "return $b as string;") || !strings.Contains(stderr.String(), "<stdin>:7:") {
		t.Errorf("--stdin:\n%s\n%s", got, stderr.String())
	}
}

// A fixer whose change the first layer undoes — @PSR12 expanding an empty
// body {} — is named: the sources keep Phaenon's layout, the outputs are
// rebuilt from them, and phn fmt fails.
func TestFmtNamesAFixerThatUndoesTheLayout(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	tool := fakeToolCommand(t, fakeSpec{Replace: [][2]string{{"private int $n) {}", "private int $n)\n    {\n    }"}}})
	writeTree(t, root, map[string]string{
		"phaenon.json": `{"roots": {"src": ".src"}, "fmt": [` + strconvQuote(tool) + `, "fix"]}`,
	})
	stdout, stderr, err := run(t, root, bin, "fmt")
	if err == nil || !strings.Contains(stdout, "1 files laid out again after the fixer") ||
		!strings.Contains(stderr, "src/Box.phn: the fixer's change undoes Phaenon's layout") {
		t.Errorf("the conflict was not named (%v):\n%s%s", err, stdout, stderr)
	}
	if box, _ := os.ReadFile(filepath.Join(root, "src/Box.phn")); !strings.Contains(string(box), "__construct(private int $n) {}") {
		t.Errorf("Box.phn lost its {}:\n%s", box)
	}
	if _, stderr, err := run(t, root, bin, "build", "--check"); err != nil {
		t.Errorf("the outputs are not the sources' build: %s", stderr)
	}
}

// phn fmt takes directories as build and check take them: the .phn under
// them, the outputs left out as phn fmt without paths leaves them.
func TestFmtDirectories(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	odd := "namespace App;\n\nfinal class Odd {\n    get(): list < int > => [];\n}\n"
	writeTree(t, root, map[string]string{
		"phaenon.json":     `{"roots": {"src": ".src"}, "fmt": []}`,
		"src/Deep/Odd.phn": odd,
		".src/Stray.phn":   odd,
	})
	stdout, stderr, err := run(t, root, bin, "fmt", "src/")
	if err != nil || !strings.Contains(stdout, "2 files formatted, 2 changed") {
		t.Fatalf("phn fmt src/ (%v):\n%s%s", err, stdout, stderr)
	}
	want := "namespace App;\n\nfinal class Odd\n{\n    get(): list<int> => [];\n}\n"
	if got, _ := os.ReadFile(filepath.Join(root, "src/Deep/Odd.phn")); string(got) != want {
		t.Errorf("Odd.phn:\n%s\nwant\n%s", got, want)
	}
	stdout, stderr, err = run(t, root, bin, "fmt", "--check", ".")
	if err != nil || !strings.Contains(stdout, "2 files checked, 0 not in Phaenon's layout") || strings.Contains(stderr, "Stray") {
		t.Errorf("phn fmt --check . (%v):\n%s%s", err, stdout, stderr)
	}
	if got, _ := os.ReadFile(filepath.Join(root, ".src/Stray.phn")); string(got) != odd {
		t.Errorf("a file of an output directory was formatted:\n%s", got)
	}

	// A directory with no .phn hands the fixer nothing, and the fixer is
	// not run on the files its configuration names instead.
	tool := fakeToolCommand(t, fakeSpec{Replace: [][2]string{{"int $a,int $b", "int $a, int $b"}}})
	writeTree(t, root, map[string]string{
		"phaenon.json":       `{"roots": {"src": ".src"}, "fmt": [` + strconvQuote(tool) + `, "fix"]}`,
		"src/Fixture/a.json": "{}\n",
	})
	if stdout, stderr, err := run(t, root, bin, "fmt", "src/Fixture"); err != nil || !strings.Contains(stdout, "0 files formatted") {
		t.Errorf("phn fmt src/Fixture (%v):\n%s%s", err, stdout, stderr)
	}
	if box, _ := os.ReadFile(filepath.Join(root, "src/Box.phn")); !strings.Contains(string(box), "int $a,int $b") {
		t.Errorf("the fixer ran on a file outside the paths given:\n%s", box)
	}
}

// A file that does not parse is reported and left as it is, the others
// are formatted, and phn fmt fails, with --check too.
func TestFmtFailsOnAFileThatDoesNotParse(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	broken := "namespace App;\n\nfunction broken( {\n"
	writeTree(t, root, map[string]string{
		"phaenon.json":   `{"roots": {"src": ".src"}, "fmt": []}`,
		"src/Broken.phn": broken,
	})
	exitStatus := func(err error) int {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		return 0
	}
	stdout, stderr, err := run(t, root, bin, "fmt")
	if exitStatus(err) != 1 || !strings.Contains(stderr, "src/Broken.phn:") || !strings.Contains(stdout, "1 that do not parse") {
		t.Errorf("phn fmt should fail on Broken.phn (%v):\n%s%s", err, stdout, stderr)
	}
	if box, _ := os.ReadFile(filepath.Join(root, "src/Box.phn")); !strings.Contains(string(box), "final class Box\n{") {
		t.Errorf("the file that parses was not formatted:\n%s", box)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "src/Broken.phn")); string(got) != broken {
		t.Errorf("Broken.phn was written:\n%s", got)
	}
	stdout, stderr, err = run(t, root, bin, "fmt", "--check")
	if exitStatus(err) != 1 || !strings.Contains(stderr, "src/Broken.phn:") {
		t.Errorf("phn fmt --check should fail on Broken.phn (%v):\n%s%s", err, stdout, stderr)
	}
}

// A change's text in the report is cut between characters, never inside
// one: a string of PHP, a comment, holds any text.
func TestClipCutsBetweenCharacters(t *testing.T) {
	head := strings.Repeat("a", 56) + "€"
	if got, want := clip(head+strings.Repeat("b", 10)), strconv.Quote(head+"…"); got != want {
		t.Errorf("clip = %s, want %s", got, want)
	}
	short := strings.Repeat("é", 40) // 80 bytes, 40 characters
	if got, want := clip(short), strconv.Quote(short); got != want {
		t.Errorf("clip = %s, want %s", got, want)
	}
	raw := strings.Repeat("\xff", 70) // a PHP string of bytes keeps them
	if got, want := clip(raw), strconv.Quote(strings.Repeat("\xff", 57)+"…"); got != want {
		t.Errorf("clip = %s, want %s", got, want)
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The report names the rule behind a change when the tool's own report
// says: php-cs-fixer's fixers per file (exact when one fixer changed the
// file), Rector's rectors by the line of the node each rewrote.
func TestFixNamesRules(t *testing.T) {
	bin := phnBinary(t)
	braces := fakeSpec{Replace: [][2]string{{"): int\n    {", "): int {"}}}

	root := fixTree(t)
	braces.Rules = []fakeRule{{Name: "braces_position"}}
	_, stderr, _ := run(t, root, bin, "fix", "--check", "--", fakeToolCommand(t, braces), "fix")
	if want := `src/Box.phn:6: php-cs-fixer (braces_position) changes text the compiler wrote: "\n   " → ""`; !strings.Contains(stderr, want) {
		t.Errorf("one fixer: stderr lacks %s:\n%s", want, stderr)
	}
	braces.Rules = []fakeRule{{Name: "statement_indentation"}, {Name: "braces_position"}}
	_, stderr, _ = run(t, root, bin, "fix", "--check", "--", fakeToolCommand(t, braces), "fix")
	if want := `php-cs-fixer (one of braces_position, statement_indentation) changes`; !strings.Contains(stderr, want) {
		t.Errorf("two fixers: stderr lacks %s:\n%s", want, stderr)
	}
	// A command that chooses its own format gets no report from phn.
	_, stderr, _ = run(t, root, bin, "fix", "--check", "--", fakeToolCommand(t, braces), "fix", "--format=txt")
	if strings.Contains(stderr, "php-cs-fixer (") {
		t.Errorf("--format=txt: a rule was named:\n%s", stderr)
	}

	// Rector names the line of the node each rector rewrote: the method's
	// for the one, the class's for the other. The change in the method goes
	// to the method's rector, the innermost.
	built, err := os.ReadFile(filepath.Join(root, ".src/Box.php"))
	if err != nil {
		t.Fatal(err)
	}
	lineOf := func(text string) int {
		i := strings.Index(string(built), text)
		if i < 0 {
			t.Fatalf("%q not in the output:\n%s", text, built)
		}
		return strings.Count(string(built[:i]), "\n") + 1
	}
	rectors := fakeSpec{Replace: braces.Replace, Rules: []fakeRule{
		{Name: "ClassRector", Line: lineOf("final class Box")},
		{Name: "MethodRector", Line: lineOf("public function add")},
	}}
	_, stderr, _ = run(t, root, bin, "fix", "--check", "--", fakeNamedTool(t, "rector", rectors), "process")
	if want := `src/Box.phn:6: rector (MethodRector) changes text the compiler wrote`; !strings.Contains(stderr, want) {
		t.Errorf("rector: stderr lacks %s:\n%s", want, stderr)
	}
}

func TestWithRuleReport(t *testing.T) {
	for _, c := range []struct{ in, want, tool string }{
		{"tools/php-cs-fixer fix", "tools/php-cs-fixer fix --format=json -v", "php-cs-fixer"},
		{"php8.5 tools/php-cs-fixer.phar fix --dry-run -- a.php", "php8.5 tools/php-cs-fixer.phar fix --format=json -v --dry-run -- a.php", "php-cs-fixer"},
		{"php-cs-fixer fix --format=checkstyle", "php-cs-fixer fix --format=checkstyle", ""},
		{"php-cs-fixer check", "php-cs-fixer check", ""},
		{"vendor/bin/rector process src", "vendor/bin/rector process --output-format=json src", "rector"},
		{"vendor/bin/rector", "vendor/bin/rector --output-format=json", "rector"},
		{"vendor/bin/rector process --output-format=github", "vendor/bin/rector process --output-format=github", ""},
		{"vendor/bin/phpcbf src", "vendor/bin/phpcbf src", ""},
	} {
		got, tool := withRuleReport(strings.Fields(c.in))
		if strings.Join(got, " ") != c.want || tool != c.tool {
			t.Errorf("%s: got %q %q, want %q %q", c.in, strings.Join(got, " "), tool, c.want, c.tool)
		}
	}
}
