package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildNoDev: phn build --no-dev builds the roots but those whose
// output composer.json maps under autoload-dev alone, and says which it
// left out; paths are not chosen among.
func TestBuildNoDev(t *testing.T) {
	bin := phnBinary(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"composer.json":                     `{"name": "acme/app", "require": {"php": ">=8.5"}, "autoload": {"psr-4": {"App\\": ".src/"}}, "autoload-dev": {"psr-4": {"App\\Tests\\": ".tests/"}}}`,
		"phaenon.json":                      `{"roots": {"src": ".src", "tests": ".tests"}}`,
		"vendor/composer/installed.json":    `{"packages": []}`,
		"vendor/composer/autoload_psr4.php": "<?php\n\nreturn array();\n",
		"src/F.phn":                         "namespace App;\n\nfunction f(): int => 1;\n",
		"tests/FTest.phn":                   "namespace App\\Tests;\n\nfinal class FTest extends \\PHPUnit\\Framework\\TestCase {}\n",
	})
	stdout, stderr, err := run(t, root, bin, "build", "--no-dev")
	if err != nil || !strings.Contains(stdout, "phn build: --no-dev leaves out tests, whose output .tests composer.json maps under autoload-dev alone\n") {
		t.Errorf("build --no-dev (%v):\n%s%s", err, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, ".src", "F.php")); err != nil {
		t.Errorf("the source root was not built: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".tests")); err == nil {
		t.Error("the development root was built")
	}
	if _, _, err := run(t, root, bin, "build"); err == nil {
		t.Error("a build of every root should fail on the tests' PHPUnit")
	}
	if _, stderr, err := run(t, root, bin, "build", "--no-dev", "src"); err == nil || !strings.Contains(stderr, "give no paths") {
		t.Errorf("--no-dev with paths (%v):\n%s", err, stderr)
	}
}

// TestDevRoots: a root is a development root when autoload-dev maps its
// output and autoload does not, by a PSR-4, PSR-0 or class-map path, a
// file, or a directory above it.
func TestDevRoots(t *testing.T) {
	cases := []struct {
		composer string
		want     string
	}{
		{`{"autoload": {"psr-4": {"App\\": ".src/"}}, "autoload-dev": {"psr-4": {"T\\": ".tests/"}}}`, "tests"},
		{`{"autoload": {"psr-4": {"App\\": [".src/", ".tests/"]}}, "autoload-dev": {"psr-4": {"T\\": ".tests/"}}}`, ""},
		{`{"autoload-dev": {"classmap": [".tests/Support"]}}`, "tests"},
		{`{"autoload-dev": {"files": [".tests/bootstrap.php"]}}`, "tests"},
		{`{"autoload": {"classmap": ["./"]}, "autoload-dev": {"psr-4": {"T\\": ".tests/"}}}`, ""},
		{`{"autoload": {"psr-4": {"App\\": ".src/"}}}`, ""},
		{`not json`, ""},
	}
	for _, c := range cases {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{"composer.json": c.composer})
		p := &project{Roots: map[string]string{"src": ".src", "tests": ".tests"}, path: filepath.Join(dir, "phaenon.json")}
		var got []string
		for r := range p.devRoots() {
			got = append(got, r)
		}
		if strings.Join(got, ",") != c.want {
			t.Errorf("%s: dev roots %v, want %q", c.composer, got, c.want)
		}
	}
}
