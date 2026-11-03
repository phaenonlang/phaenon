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
}

func fakeTool(spec string) {
	var s fakeSpec
	if err := json.Unmarshal([]byte(spec), &s); err != nil {
		panic(err)
	}
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
		}
		return nil
	})
	for _, path := range s.Create {
		os.WriteFile(path, []byte("<?php\n"), 0o644)
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
	data, _ := json.Marshal(spec)
	t.Setenv("PHN_FAKE_TOOL", string(data))
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(t.TempDir(), "php-cs-fixer")
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
