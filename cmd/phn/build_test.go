package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMapSource pins the path a compiled file's line map names: the source
// relative to the output's directory, forward slashes.
func TestMapSource(t *testing.T) {
	cases := []struct{ src, dst, want string }{
		{"src/Domain/Foo.phn", ".src/Domain/Foo.php", "../../src/Domain/Foo.phn"},
		{"src/Foo.phn", "src/Foo.php", "Foo.phn"},
		{"lib/Foo.phn", "out/lib/Foo.php", "../../lib/Foo.phn"},
	}
	for _, c := range cases {
		if got := mapSource(c.src, c.dst); got != c.want {
			t.Errorf("mapSource(%q, %q) = %q, want %q", c.src, c.dst, got, c.want)
		}
	}
}

// TestBuildCheckEmitOnError: --check with --emit-on-error asks whether the
// outputs are what `phn build --emit-on-error` writes, so a file with errors
// whose output is current passes, its findings still printed; without the
// flag the same file fails, and a stale output fails either way.
func TestBuildCheckEmitOnError(t *testing.T) {
	bin := phnBinary(t)
	root := formatTree(t)
	writeTree(t, root, map[string]string{"src/Good.phn": "namespace App;\n\nfunction good(): int => 1;\n"})

	if _, _, err := run(t, root, bin, "build", "--check", "--emit-on-error"); err == nil {
		t.Error("--check passed before anything was built")
	}
	if _, _, err := run(t, root, bin, "build", "--emit-on-error"); err == nil {
		t.Error("build --emit-on-error should still fail on the errors")
	}
	stdout, stderr, err := run(t, root, bin, "build", "--check", "--emit-on-error")
	if err != nil || !strings.Contains(stdout, "2 files up to date, 1 of them built with their errors") || !strings.Contains(stderr, "error P2202") {
		t.Errorf("--check --emit-on-error on current outputs (%v):\n%s%s", err, stdout, stderr)
	}
	if _, _, err := run(t, root, bin, "build", "--check"); err == nil {
		t.Error("--check without --emit-on-error passed a file with errors")
	}
	writeTree(t, root, map[string]string{".src/Bad.php": "<?php\n"})
	if stdout, _, err := run(t, root, bin, "build", "--check", "--emit-on-error"); err == nil {
		t.Errorf("a stale output passed:\n%s", stdout)
	}
}

// TestBuildAbsentRoot: a root the project file names but the tree lacks is
// skipped, not failed on. An image's build context, or a dist archive,
// leaves tests/ out and is still the same project; a tree with none of the
// roots is a mistake.
func TestBuildAbsentRoot(t *testing.T) {
	bin := phnBinary(t)
	root := formatTree(t)
	writeTree(t, root, map[string]string{
		"phaenon.json": `{"roots": {"src": ".src", "tests": ".tests"}}`,
		"src/Good.phn": "namespace App;\n\nfunction good(): int => 1;\n",
	})
	if err := os.Remove(filepath.Join(root, "src", "Bad.phn")); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := run(t, root, bin, "build")
	if err != nil || !strings.Contains(stdout, "1 files compiled") {
		t.Errorf("build with tests/ absent (%v):\n%s%s", err, stdout, stderr)
	}
	if _, _, err := run(t, root, bin, "check"); err != nil {
		t.Errorf("check with tests/ absent: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, "src")); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := run(t, root, bin, "build"); err == nil || !strings.Contains(stderr, "none of the roots") {
		t.Errorf("build with no root present should fail and say so (%v):\n%s", err, stderr)
	}
}

// TestFilesEntryOfAnOutput: a Composer `files` entry that names one of the
// project's outputs is read from the source behind it, as a class found
// through the maps is, and not besides it: every build, the ones after the
// first that wrote the output among them, declares the function once. A
// check of another file alone reads the function from the source, not
// from an output that is no longer current. A plain PHP file of a root
// that a `files` entry names is declared once as well.
func TestFilesEntryOfAnOutput(t *testing.T) {
	bin := phnBinary(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"composer.json":                     `{"name": "acme/app", "require": {"php": ">=8.5"}, "autoload": {"psr-4": {"App\\": ".src/"}, "files": [".src/functions.php", ".src/helpers.php"]}}`,
		"phaenon.json":                      `{"roots": {"src": ".src"}}`,
		"vendor/composer/installed.json":    `{"packages": []}`,
		"vendor/composer/autoload_psr4.php": "<?php\n\n$baseDir = dirname(dirname(__DIR__));\n\nreturn array(\n    'App\\\\' => array($baseDir . '/.src'),\n);\n",
		"vendor/composer/autoload_files.php": "<?php\n\n$baseDir = dirname(dirname(__DIR__));\n\nreturn array(\n" +
			"    'a1' => $baseDir . '/.src/functions.php',\n    'a2' => $baseDir . '/.src/helpers.php',\n);\n",
		"src/functions.phn": "namespace App;\n\nfunction greet(string $name): string => \"Hello, {$name}\";\n",
		"src/helpers.php":   "<?php\n\nnamespace App;\n\nfunction shout(string $s): string\n{\n    return \\strtoupper($s);\n}\n",
		"src/Greeter.phn":   "namespace App;\n\nfinal class Greeter\n{\n    hello(): string => shout(greet('world'));\n}\n",
	})
	for i := 1; i <= 2; i++ {
		if stdout, stderr, err := run(t, root, bin, "build"); err != nil {
			t.Fatalf("build %d (%v):\n%s%s", i, err, stdout, stderr)
		}
	}
	if stdout, stderr, err := run(t, root, bin, "check"); err != nil || !strings.Contains(stdout, "0 errors") {
		t.Errorf("check after the builds (%v):\n%s%s", err, stdout, stderr)
	}
	writeTree(t, root, map[string]string{"src/functions.phn": "namespace App;\n\nfunction greet(string $name): int => \\strlen($name);\n"})
	stdout, stderr, err := run(t, root, bin, "check", "src/Greeter.phn")
	if err == nil || !strings.Contains(stderr, "P2103") {
		t.Errorf("a check of Greeter alone should read greet() from its changed source (%v):\n%s%s", err, stdout, stderr)
	}
}

// TestFilesEntryThroughALink: a project reached under two spellings, through
// a symbolic link to its directory, declares a Composer `files` entry once:
// the working directory spelled one way, the paths given the other.
func TestFilesEntryThroughALink(t *testing.T) {
	bin := phnBinary(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"composer.json":                     `{"name": "acme/app", "require": {"php": ">=8.5"}, "autoload": {"psr-4": {"App\\": ".src/"}, "files": [".src/functions.php"]}}`,
		"phaenon.json":                      `{"roots": {"src": ".src"}}`,
		"vendor/composer/installed.json":    `{"packages": []}`,
		"vendor/composer/autoload_psr4.php": "<?php\n\n$baseDir = dirname(dirname(__DIR__));\n\nreturn array(\n    'App\\\\' => array($baseDir . '/.src'),\n);\n",
		"vendor/composer/autoload_files.php": "<?php\n\n$baseDir = dirname(dirname(__DIR__));\n\nreturn array(\n" +
			"    'a1' => $baseDir . '/.src/functions.php',\n);\n",
		"src/functions.phn": "namespace App;\n\nfunction greet(string $name): string => \"Hello, {$name}\";\n",
		"src/Greeter.phn":   "namespace App;\n\nfinal class Greeter\n{\n    hello(): string => greet('world');\n}\n",
	})
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if stdout, stderr, err := run(t, root, bin, "build"); err != nil {
		t.Fatalf("build (%v):\n%s%s", err, stdout, stderr)
	}
	for _, c := range []struct{ dir, path string }{
		{link, filepath.Join(root, "src")},
		{root, filepath.Join(link, "src")},
		{link, "src"},
	} {
		if stdout, stderr, err := run(t, c.dir, bin, "check", c.path); err != nil || !strings.Contains(stdout, "0 errors") {
			t.Errorf("check %s from %s (%v):\n%s%s", c.path, c.dir, err, stdout, stderr)
		}
	}
}
