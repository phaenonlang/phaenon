package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

const boxSource = "namespace App;\n\nfinal class Box {\n    fun __construct(private int $n) {}\n\n    fun add(int $a,int $b): int => $a+$b;\n}\n"

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
		{"private int $n)\n    {\n    }", "private int $n) {}"}, // the compiler's braces
		{"array(1,2)", "[1, 2]"},
	},
	Create: []string{".src/New.php"},
}

// The changes of the source's text are carried back, the copied file takes
// them all, and what the tool still changes after the rebuild — the
// compiler's braces — is reported, with exit status 1.
func TestFix(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	tool := fakeToolCommand(t, boxFixes)
	stdout, stderr, err := run(t, root, bin, "fix", "--", tool, "fix")
	if err == nil {
		t.Error("phn fix passed though the tool still changes the compiler's braces")
	}
	if want := "php-cs-fixer changed 3 files: 6 changes carried back to 2 sources, 2 in text the compiler wrote, 1 files outside the build\n" +
		"after the rebuild, php-cs-fixer still changes 2 files: 0 changes in 0 sources, 2 in text the compiler wrote, 1 files outside the build\n"; stdout != want {
		t.Errorf("stdout:\n%s\nwant\n%s", stdout, want)
	}
	if !strings.Contains(stderr, `src/Box.phn:4: php-cs-fixer changes text the compiler wrote: "\n   " → ""`) {
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
	if want := "php-cs-fixer would change 3 files: 6 changes in 2 sources, 2 in text the compiler wrote, 1 files outside the build\n"; stdout != want {
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

// phn fmt lays the .phn out, then runs the project's fixer through phn fix;
// --check then finds nothing left. With "fmt": [] only the first layer runs.
func TestFmt(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	tool := fakeToolCommand(t, fakeSpec{Replace: [][2]string{{"int $a,int $b", "int $a, int $b"}}})
	writeTree(t, root, map[string]string{
		"phaenon.json": `{"roots": {"src": ".src"}, "fmt": [` + strconvQuote(tool) + `, "fix"]}`,
		"src/Box.phn":  "\n\nnamespace App;\n\nfinal class Box {\n    fun add(int $a,int $b): list < int > {\n        return [$a];\n    }\n}\n",
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
	want := "namespace App;\n\nfinal class Box\n{\n    fun add(int $a, int $b): list<int> {\n        return [$a];\n    }\n}\n"
	if box, _ := os.ReadFile(filepath.Join(root, "src/Box.phn")); string(box) != want {
		t.Errorf("Box.phn:\n%s\nwant\n%s", box, want)
	}
	if stdout, stderr, err := run(t, root, bin, "fmt", "--check"); err != nil {
		t.Errorf("--check after phn fmt: %v\n%s%s", err, stdout, stderr)
	}

	writeTree(t, root, map[string]string{
		"phaenon.json": `{"roots": {"src": ".src"}, "fmt": []}`,
		"src/Box.phn":  "namespace App;\n\nfinal class Box\n{\n    fun add(int $a,int $b): int => $a;\n}\n",
	})
	if stdout, _, err := run(t, root, bin, "fmt", "--check"); err != nil || strings.Contains(stdout, "php-cs-fixer") {
		t.Errorf(`"fmt": [] still ran the fixer, or the layout failed (%v):\n%s`, err, stdout)
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
	braces := fakeSpec{Replace: [][2]string{{"private int $n)\n    {\n    }", "private int $n) {}"}}}

	root := fixTree(t)
	braces.Rules = []fakeRule{{Name: "braces_position"}}
	_, stderr, _ := run(t, root, bin, "fix", "--check", "--", fakeToolCommand(t, braces), "fix")
	if want := `src/Box.phn:4: php-cs-fixer (braces_position) changes text the compiler wrote: "\n   " → ""`; !strings.Contains(stderr, want) {
		t.Errorf("one fixer: stderr lacks %s:\n%s", want, stderr)
	}
	braces.Rules = []fakeRule{{Name: "single_line_empty_body"}, {Name: "braces_position"}}
	_, stderr, _ = run(t, root, bin, "fix", "--check", "--", fakeToolCommand(t, braces), "fix")
	if want := `php-cs-fixer (one of braces_position, single_line_empty_body) changes`; !strings.Contains(stderr, want) {
		t.Errorf("two fixers: stderr lacks %s:\n%s", want, stderr)
	}
	// A command that chooses its own format gets no report from phn.
	_, stderr, _ = run(t, root, bin, "fix", "--check", "--", fakeToolCommand(t, braces), "fix", "--format=txt")
	if strings.Contains(stderr, "php-cs-fixer (") {
		t.Errorf("--format=txt: a rule was named:\n%s", stderr)
	}

	// Rector names the line of the node each rector rewrote: the
	// constructor's for the one, the class's for the other. The change in
	// the constructor goes to the constructor's rector, the innermost.
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
		{Name: "ConstructorRector", Line: lineOf("public function __construct")},
	}}
	_, stderr, _ = run(t, root, bin, "fix", "--check", "--", fakeNamedTool(t, "rector", rectors), "process")
	if want := `src/Box.phn:4: rector (ConstructorRector) changes text the compiler wrote`; !strings.Contains(stderr, want) {
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
