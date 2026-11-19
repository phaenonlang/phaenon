package main

import (
	"encoding/json"
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
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
		"src/Bad.phn":                       "namespace App;\n\nfunction bad(): int => \"50%, a: <b> & 'c'\";\n\nfunction alsoBad(): string => 5 + 1;\n",
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
	want := map[string]string{"file": "src/Bad.phn", "line": "3", "endLine": "3", "col": "24", "title": "P2202"}
	for k, v := range want {
		if got, _ := c.Get(k); got != v {
			t.Errorf("%s=%q, want %q in %q", k, got, v, out[0])
		}
	}
	// The end column is inclusive, and only the message's % is escaped: , and
	// : are escaped in properties alone.
	if want := `::error file=src/Bad.phn,line=3,endLine=3,col=24,endColumn=42,title=P2202::function App\bad() should return int but returns '50%25, a: <b> & \'c\''`; out[0] != want {
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
	if e.Line != 3 || e.Column != 24 || e.Severity != "error" || e.Message != message || e.Source != "P2202" {
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

// TestFormatsEscape: a finding's message and path reach every format as a
// document of that format, whatever they hold: a NUL, a newline, a quote, a
// backslash, bytes that are no UTF-8, a character beyond the basic plane
// and the characters each format gives a meaning. Each format writes what
// it can of them: JSON all but the invalid bytes, a workflow command all of
// them on its one line, XML all but the NUL, which no XML document holds.
func TestFormatsEscape(t *testing.T) {
	const message = "a\x00b\nc\"d\\e\xff f\x1b g\U0001F600 <h> & 50%, i: j\r "
	const file = "src/odd \"name\"\x01.phn"
	diags := []checker.Diagnostic{
		{File: file, Line: 3, Col: 5, EndLine: 3, EndCol: 9, Severity: checker.Error, Code: "P2103", Message: message},
		{File: file, Line: 4, Col: 1, EndLine: 5, EndCol: 2, Severity: checker.Warning, Code: "P2013", Message: "plain"},
	}
	// What a reader gets back where the bytes are no UTF-8, or no character
	// of the format.
	replaced := func(s string, also ...string) string {
		s = strings.ToValidUTF8(s, "�")
		for _, c := range also {
			s = strings.ReplaceAll(s, c, "�")
		}
		return s
	}

	t.Run("json", func(t *testing.T) {
		var b strings.Builder
		writeJSON(&b, diags)
		lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
		if len(lines) != len(diags) {
			t.Fatalf("want a line per finding:\n%q", b.String())
		}
		for i, l := range lines {
			var v struct {
				File, Severity, Code, Message string
				Line, Col, EndLine, EndCol    int
			}
			if !json.Valid([]byte(l)) {
				t.Fatalf("not JSON: %q", l)
			}
			if err := json.Unmarshal([]byte(l), &v); err != nil {
				t.Fatalf("%v: %q", err, l)
			}
			d := diags[i]
			if v.File != replaced(d.File) || v.Message != replaced(d.Message) || v.Code != d.Code || v.Severity != d.Severity.String() ||
				v.Line != d.Line || v.Col != d.Col || v.EndLine != d.EndLine || v.EndCol != d.EndCol {
				t.Errorf("read back %+v from %q", v, l)
			}
		}
		if !strings.Contains(lines[0], `a\u0000b\nc\"d\\e`) || !strings.Contains(lines[0], "<h> & 50%") {
			t.Errorf("a control character is a \\u escape, and <, > and & stay as they are: %q", lines[0])
		}
	})

	t.Run("github", func(t *testing.T) {
		var b strings.Builder
		writeGitHub(&b, diags)
		lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
		if len(lines) != len(diags) {
			t.Fatalf("want a line per finding:\n%q", b.String())
		}
		for i, l := range lines {
			c, ok := trace.ParseCommand(l)
			if !ok || c.Message != diags[i].Message {
				t.Errorf("read back %q (%v) from %q", c.Message, ok, l)
			}
			if got, _ := c.Get("file"); got != trace.AnnotationPath(diags[i].File) {
				t.Errorf("file %q from %q", got, l)
			}
		}
	})

	t.Run("checkstyle", func(t *testing.T) {
		var b strings.Builder
		writeCheckstyle(&b, diags)
		var doc struct {
			Files []struct {
				Name   string `xml:"name,attr"`
				Errors []struct {
					Message string `xml:"message,attr"`
					Source  string `xml:"source,attr"`
				} `xml:"error"`
			} `xml:"file"`
		}
		if err := xml.Unmarshal([]byte(b.String()), &doc); err != nil {
			t.Fatalf("not XML: %v\n%q", err, b.String())
		}
		// An attribute's line breaks are written as references, and read
		// back as they were.
		if len(doc.Files) != 1 || doc.Files[0].Name != replaced(file, "\x01") || len(doc.Files[0].Errors) != 2 ||
			doc.Files[0].Errors[0].Message != replaced(message, "\x00", "\x1b") || doc.Files[0].Errors[0].Source != "P2103" {
			t.Errorf("read back %+v from %q", doc, b.String())
		}
	})
}
