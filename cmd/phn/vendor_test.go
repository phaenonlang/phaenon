package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// phnBinary builds the command into a temporary directory, once per test.
func phnBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "phn")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// writeTree writes files under dir, creating directories.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for path, text := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// vendorTree is a root project that installed acme/lib, a library written
// in Phaenon: a generic class, a file with a type error, and a tests root
// its dist archive left out.
func vendorTree(t *testing.T) string {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"composer.json": `{"name": "acme/root", "require": {"php": ">=8.5", "acme/lib": "*"}, "autoload": {"psr-4": {"App\\": ".src/"}}}`,
		"phaenon.json":  `{"roots": {"src": ".src"}}`,
		"src/Use.phn":   "namespace App;\n\nuse Acme\\Lib\\Box;\n\nfunction unbox(Box<int> $b): int => $b->get();\n",
		"vendor/composer/installed.json": `{"packages": [
			{"name": "acme/lib", "version": "1.2.0", "install-path": "../acme/lib"},
			{"name": "acme/plain", "version": "2.0.0", "install-path": "../acme/plain"}
		], "dev": true, "dev-package-names": []}`,
		"vendor/composer/autoload_psr4.php": "<?php\n\n$vendorDir = dirname(__DIR__);\n$baseDir = dirname($vendorDir);\n\nreturn array(\n    'App\\\\' => array($baseDir . '/.src'),\n    'Acme\\\\Lib\\\\' => array($vendorDir . '/acme/lib/.src'),\n);\n",
		"vendor/acme/lib/composer.json":     `{"name": "acme/lib", "require": {"php": ">=8.5"}, "autoload": {"psr-4": {"Acme\\Lib\\": ".src/"}}}`,
		"vendor/acme/lib/phaenon.json":      `{"roots": {"src": ".src", "tests": ".tests"}}`,
		"vendor/acme/lib/src/Box.phn":       "namespace Acme\\Lib;\n\nfinal class Box<T> {\n    __construct(private T $value) {}\n\n    get(): T => $this->value;\n}\n",
		"vendor/acme/lib/src/Bad.phn":       "namespace Acme\\Lib;\n\nfunction bad(): int => 'not an int';\n",
		"vendor/acme/lib/src/data.json":     `{"copied": true}`,
		"vendor/acme/plain/composer.json":   `{"name": "acme/plain"}`,
	})
	return root
}

func run(t *testing.T, dir, bin string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// An installed package with a phaenon.json is compiled into its own output
// directory, its type error a warning that withholds nothing, and skipped
// while nothing changed; the root project reads it from its sources, so
// that its generic signature is seen as written.
func TestBuildVendor(t *testing.T) {
	bin := phnBinary(t)
	root := vendorTree(t)
	// Before any output exists, only the sources can answer for Box.
	stdout, stderr, err := run(t, root, bin, "check")
	if err != nil || !strings.Contains(stdout, "0 errors") {
		t.Errorf("the root project should read Box<T> from the package's sources (%v):\n%s%s", err, stdout, stderr)
	}
	stdout, stderr, err = run(t, root, bin, "build", "--vendor")
	if err != nil {
		t.Fatalf("phn build --vendor: %v\n%s%s", err, stdout, stderr)
	}
	for _, f := range []string{"vendor/acme/lib/.src/Box.php", "vendor/acme/lib/.src/Bad.php", "vendor/acme/lib/.src/data.json", "vendor/acme/lib/.src/" + stampFile} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	if !strings.Contains(stderr, "Bad.phn:3") || !strings.Contains(stderr, "warning P2") {
		t.Errorf("the type error should be a warning:\n%s", stderr)
	}
	if !strings.Contains(stdout, "1 installed packages built, 0 up to date") {
		t.Errorf("summary:\n%s", stdout)
	}
	stdout, _, err = run(t, root, bin, "build", "--vendor")
	if err != nil || !strings.Contains(stdout, "0 installed packages built, 1 up to date") {
		t.Errorf("a second build should skip the package (%v):\n%s", err, stdout)
	}
	writeTree(t, root, map[string]string{"vendor/acme/lib/src/data.json": `{"copied": "again"}`})
	stdout, _, _ = run(t, root, bin, "build", "--vendor")
	if !strings.Contains(stdout, "1 installed packages built") {
		t.Errorf("a changed source should rebuild the package:\n%s", stdout)
	}
	stdout, stderr, err = run(t, root, bin, "check")
	if err != nil || !strings.Contains(stdout, "0 errors") {
		t.Errorf("the root project should see Box<T> from the package's sources (%v):\n%s%s", err, stdout, stderr)
	}
}
