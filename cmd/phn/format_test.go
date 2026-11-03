package main

import (
	"encoding/json"
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/trace"
)

// formatTree is a project with two findings on one file, their messages
// holding the characters the formats escape.
func formatTree(t *testing.T) string {
	root := t.TempDir()
	writeFormatTree(t, root)
	return root
}

func writeFormatTree(t *testing.T, root string) {
	writeTree(t, root, map[string]string{
		"composer.json":                     `{"name": "acme/app", "require": {"php": ">=8.5"}}`,
		"phaenon.json":                      `{"roots": {"src": ".src"}}`,
		"vendor/composer/installed.json":    `{"packages": []}`,
		"vendor/composer/autoload_psr4.php": "<?php\n\nreturn array();\n",
		"src/Bad.phn":                       "namespace App;\n\nfun bad(): int => \"50%, a: <b> & 'c'\";\n\nfun alsoBad(): string => 5 + 1;\n",
	})
}

// TestCheckFormats: every format carries the same findings, and the
// machine formats carry nothing else.
func TestCheckFormats(t *testing.T) {
	bin := phnBinary(t)
	root := formatTree(t)
	const message = `function App\bad() should return int but returns '50%, a: <b> & \'c\''`

	stdout, _, err := run(t, root, bin, "check", "--format=json")
	if err == nil {
		t.Error("check passed with two errors")
	}
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Fatalf("not a JSON line: %q", l)
		}
		lines = append(lines, v)
	}
	if len(lines) != 2 || lines[0]["message"] != message || lines[0]["line"] != 3.0 {
		t.Errorf("--format=json:\n%s", stdout)
	}
	if again, _, _ := run(t, root, bin, "check", "--json"); again != stdout {
		t.Errorf("--json differs from --format=json:\n%s", again)
	}

	stdout, _, _ = run(t, root, bin, "check", "--format=github")
	out := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(out) != 3 || !strings.HasPrefix(out[2], "1 files checked: 2 errors") {
		t.Fatalf("--format=github: want two commands and the summary:\n%s", stdout)
	}
	c, ok := trace.ParseCommand(out[0])
	if !ok || c.Name != "error" || c.Message != message {
		t.Errorf("not the first finding: %q (%+v)", out[0], c)
	}
	want := map[string]string{"file": "src/Bad.phn", "line": "3", "endLine": "3", "col": "19", "title": "P2202"}
	for k, v := range want {
		if got, _ := c.Get(k); got != v {
			t.Errorf("%s=%q, want %q in %q", k, got, v, out[0])
		}
	}
	// The end column is inclusive, and only the message's % is escaped: , and
	// : are escaped in properties alone.
	if want := `::error file=src/Bad.phn,line=3,endLine=3,col=19,endColumn=37,title=P2202::function App\bad() should return int but returns '50%25, a: <b> & \'c\''`; out[0] != want {
		t.Errorf("got  %s\nwant %s", out[0], want)
	}

	stdout, _, _ = run(t, root, bin, "check", "--format=checkstyle")
	var report struct {
		XMLName xml.Name `xml:"checkstyle"`
		Files   []struct {
			Name   string `xml:"name,attr"`
			Errors []struct {
				Line     int    `xml:"line,attr"`
				Column   int    `xml:"column,attr"`
				Severity string `xml:"severity,attr"`
				Message  string `xml:"message,attr"`
				Source   string `xml:"source,attr"`
			} `xml:"error"`
		} `xml:"file"`
	}
	if err := xml.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("--format=checkstyle is not XML (%v):\n%s", err, stdout)
	}
	if len(report.Files) != 1 || report.Files[0].Name != "src/Bad.phn" || len(report.Files[0].Errors) != 2 {
		t.Fatalf("--format=checkstyle:\n%s", stdout)
	}
	e := report.Files[0].Errors[0]
	if e.Line != 3 || e.Column != 19 || e.Severity != "error" || e.Message != message || e.Source != "P2202" {
		t.Errorf("first error %+v", e)
	}

	if _, stderr, err := run(t, root, bin, "check", "--format=sarif"); err == nil || !strings.Contains(stderr, "--format is one of") {
		t.Errorf("an unknown format should be refused (%v): %s", err, stderr)
	}
}

// In a project kept in a subdirectory of its repository, an annotation names
// the file by its path in the repository, which GitHub matches; Checkstyle
// keeps the path relative to the working directory.
func TestCheckFormatsInSubdirectory(t *testing.T) {
	bin := phnBinary(t)
	repo := t.TempDir()
	writeTree(t, repo, map[string]string{".git/HEAD": "ref: refs/heads/main\n"})
	root := filepath.Join(repo, "app")
	writeFormatTree(t, root)
	if stdout, _, _ := run(t, root, bin, "check", "--format=github"); !strings.HasPrefix(stdout, "::error file=app/src/Bad.phn,line=3,") {
		t.Errorf("--format=github:\n%s", stdout)
	}
	if stdout, _, _ := run(t, root, bin, "check", "--format=checkstyle"); !strings.Contains(stdout, `<file name="src/Bad.phn">`) {
		t.Errorf("--format=checkstyle:\n%s", stdout)
	}
}
