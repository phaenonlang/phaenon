package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// plainTree is a project whose root holds plain PHP beside Phaenon: a .phn
// that uses a class a .php declares, and a .php that uses the .phn's.
func plainTree(t *testing.T) string {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"composer.json":                     `{"name": "acme/app", "require": {"php": ">=8.5"}, "autoload": {"psr-4": {"App\\": ".src/"}}}`,
		"phaenon.json":                      `{"roots": {"src": ".src"}}`,
		"vendor/composer/installed.json":    `{"packages": []}`,
		"vendor/composer/autoload_psr4.php": "<?php\n\n$vendorDir = dirname(__DIR__);\n$baseDir = dirname($vendorDir);\n\nreturn array(\n    'App\\\\' => array($baseDir . '/.src'),\n);\n",
		"src/Helper.php":                    "<?php\n\ndeclare(strict_types=1);\n\nnamespace App;\n\nfinal class Helper\n{\n    public static function twice(int $n): int\n    {\n        return $n * 2;\n    }\n}\n",
		"src/Uses.phn":                      "namespace App;\n\nfinal class Uses {\n    run(): int => Helper::twice(2);\n}\n",
		"src/Caller.php":                    "<?php\n\ndeclare(strict_types=1);\n\nnamespace App;\n\nfunction call(): int\n{\n    return (new Uses())->run();\n}\n",
	})
	return root
}

// findings are the lines of phn's text output that report a finding, as
// file:line:col: severity code.
func findings(output string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^(\S+:\d+:\d+: (?:error|warning) P\d+)`).FindAllStringSubmatch(output, -1) {
		out = append(out, m[1])
	}
	slices.Sort(out)
	return out
}

// TestBuildChecksPlainPHP: phn build reads and checks a plain .php under a
// root as phn check does, so that a .phn may use the classes it declares
// and the reverse, and the two report the same findings. The .php itself
// is copied as it is (design D12): no line map, nothing for phn trace.
func TestBuildChecksPlainPHP(t *testing.T) {
	bin := phnBinary(t)
	root := plainTree(t)
	_, checkErr, err := run(t, root, bin, "check")
	if err != nil {
		t.Fatalf("phn check: %v\n%s", err, checkErr)
	}
	stdout, buildErr, err := run(t, root, bin, "build")
	if err != nil {
		t.Fatalf("phn build: %v\n%s%s", err, stdout, buildErr)
	}
	if c, b := findings(checkErr), findings(buildErr); !slices.Equal(c, b) {
		t.Errorf("phn check found %v, phn build %v", c, b)
	}
	for _, f := range []string{"Helper.php", "Caller.php"} {
		src, _ := os.ReadFile(filepath.Join(root, "src", f))
		out, err := os.ReadFile(filepath.Join(root, ".src", f))
		if err != nil || !bytes.Equal(src, out) {
			t.Errorf(".src/%s is not src/%s as it is: %v\n%s", f, f, err, out)
		}
	}
	if out, _ := os.ReadFile(filepath.Join(root, ".src", "Uses.php")); !bytes.Contains(out, []byte("// phaenon-map v1")) {
		t.Errorf(".src/Uses.php is not compiled:\n%s", out)
	}
	stdout, _, err = run(t, root, bin, "trace", ".src/Helper.php:11")
	if err != nil || stdout != ".src/Helper.php:11\n" {
		t.Errorf("phn trace of a copied .php: %v %q, want it left as it is", err, stdout)
	}
	stdout, _, err = run(t, root, bin, "build", "--check")
	if err != nil {
		t.Errorf("phn build --check after a build: %v\n%s", err, stdout)
	}
}

// TestBuildPlainPHPWithErrors: an error in a plain .php, a syntax error or
// a type error, stops the build as one in a .phn does: phn check and phn
// build report the same findings, the copy is withheld and the build
// fails; under --emit-on-error the copy is made and the build still fails.
func TestBuildPlainPHPWithErrors(t *testing.T) {
	bin := phnBinary(t)
	root := plainTree(t)
	writeTree(t, root, map[string]string{
		"src/Broken.php": "<?php\n\nnamespace App;\n\nfunction broken( {\n}\n",
		"src/Wrong.php":  "<?php\n\nnamespace App;\n\nfunction wrong(): int\n{\n    return 'not an int';\n}\n",
	})
	_, checkErr, checkStatus := run(t, root, bin, "check")
	stdout, buildErr, buildStatus := run(t, root, bin, "build")
	if checkStatus == nil || buildStatus == nil {
		t.Fatalf("phn check (%v) and phn build (%v) should both fail", checkStatus, buildStatus)
	}
	c, b := findings(checkErr), findings(buildErr)
	if !slices.Equal(c, b) {
		t.Errorf("phn check found %v, phn build %v", c, b)
	}
	syntax := slices.IndexFunc(b, func(f string) bool {
		return strings.HasPrefix(f, "src/Broken.php:5:") && strings.HasSuffix(f, ": error P1000")
	})
	if syntax < 0 || !slices.Contains(b, "src/Wrong.php:7:12: error P2202") {
		t.Errorf("findings %v, want a P1000 on line 5 of Broken.php and a P2202 in Wrong.php", b)
	}
	const withheld = "0 of 1 files failed to compile, and 2 of 4 plain .php files have errors and were not copied"
	if !strings.Contains(buildErr, withheld) {
		t.Errorf("phn build's last line:\n%s%s\nwant %q", stdout, buildErr, withheld)
	}
	for _, f := range []string{"Broken.php", "Wrong.php"} {
		if _, err := os.Stat(filepath.Join(root, ".src", f)); err == nil {
			t.Errorf(".src/%s was copied with its error", f)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".src", "Helper.php")); err != nil {
		t.Errorf("the plain .php without errors: %v", err)
	}

	_, buildErr, buildStatus = run(t, root, bin, "build", "--emit-on-error")
	if buildStatus == nil || !strings.Contains(buildErr, "2 of 4 plain .php files have errors and were copied with them") {
		t.Errorf("phn build --emit-on-error (%v):\n%s", buildStatus, buildErr)
	}
	for _, f := range []string{"Broken.php", "Wrong.php"} {
		src, _ := os.ReadFile(filepath.Join(root, "src", f))
		if out, err := os.ReadFile(filepath.Join(root, ".src", f)); err != nil || !bytes.Equal(src, out) {
			t.Errorf(".src/%s under --emit-on-error: %v", f, err)
		}
	}
	// --check passes the outputs that build wrote, and counts the compiled
	// files and the plain .php apart.
	const upToDate = "1 files up to date, 0 of them built with their errors (--emit-on-error), and 2 of 4 plain .php files copied with theirs\n"
	if stdout, stderr, err := run(t, root, bin, "build", "--check", "--emit-on-error"); err != nil || !strings.HasSuffix(stdout, upToDate) {
		t.Errorf("phn build --check --emit-on-error (%v):\n%s%s\nwant %q", err, stdout, stderr, upToDate)
	}
}

// TestExcludedPlainPHPStaysOut: a plain .php under an excluded path is
// copied by the build and never enters the program, in the build as in the
// check: a fixture that declares a class of the project's own name does
// not stand against it. Naming a path under "exclude" is what a project
// does with a plain .php it does not want checked.
func TestExcludedPlainPHPStaysOut(t *testing.T) {
	bin := phnBinary(t)
	root := plainTree(t)
	const fixture = "<?php\n\nnamespace App;\n\nfinal class Uses\n{\n    public function other(): string\n    {\n        return 1;\n    }\n}\n"
	writeTree(t, root, map[string]string{
		"phaenon.json":          `{"roots": {"src": ".src"}, "exclude": ["src/Fixtures"]}`,
		"src/Fixtures/uses.php": fixture,
	})
	if _, stderr, err := run(t, root, bin, "check"); err != nil || len(findings(stderr)) > 0 {
		t.Errorf("phn check: %v\n%s", err, stderr)
	}
	stdout, stderr, err := run(t, root, bin, "build")
	if err != nil || len(findings(stderr)) > 0 {
		t.Errorf("phn build: %v\n%s%s", err, stdout, stderr)
	}
	if out, err := os.ReadFile(filepath.Join(root, ".src", "Fixtures", "uses.php")); err != nil || string(out) != fixture {
		t.Errorf("the excluded .php is not copied as it is: %v", err)
	}
	if out, _ := os.ReadFile(filepath.Join(root, ".src", "Uses.php")); !bytes.Contains(out, []byte("// phaenon-map v1")) {
		t.Errorf(".src/Uses.php is not compiled:\n%s", out)
	}
}

// TestWatchCountsWithheldCopies: phn watch's build, which is the language
// server's on save, counts a plain .php whose copy is withheld among the
// files whose output was not written.
func TestWatchCountsWithheldCopies(t *testing.T) {
	bin := phnBinary(t)
	root := plainTree(t)
	writeTree(t, root, map[string]string{
		"src/Wrong.php": "<?php\n\nnamespace App;\n\nfunction wrong(): int\n{\n    return 'not an int';\n}\n",
	})
	wait := startWatch(t, bin, root)
	wait("1 files compiled, 3 outputs written, 1 withheld")
	if _, err := os.Stat(filepath.Join(root, ".src", "Wrong.php")); err == nil {
		t.Error(".src/Wrong.php was copied with its error")
	}
}

// TestOutputLeftBehind: without a project file the outputs are written
// beside the sources, and one whose .phn was renamed stays there, declaring
// the renamed file's class a second time under the name an autoloader
// looks for. It is plain PHP to phn check and phn build alike, which
// report the second declaration, where the first is, and what the file
// looks like.
func TestOutputLeftBehind(t *testing.T) {
	bin := phnBinary(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{"src/Foo.phn": "namespace App;\n\nfinal class Foo {\n    run(): int => 1;\n}\n"})
	if stdout, stderr, err := run(t, root, bin, "build", "src"); err != nil {
		t.Fatalf("phn build src: %v\n%s%s", err, stdout, stderr)
	}
	if err := os.Rename(filepath.Join(root, "src", "Foo.phn"), filepath.Join(root, "src", "Renamed.phn")); err != nil {
		t.Fatal(err)
	}
	const want = "src/Renamed.phn:3:1: error P2511: class App\\Foo is already declared (src/Foo.php:9): that file looks like a compiled output left behind, its .phn renamed or removed"
	for _, cmd := range []string{"check", "build"} {
		if stdout, stderr, err := run(t, root, bin, cmd, "src"); err == nil || !strings.Contains(stderr, want) {
			t.Errorf("phn %s src after the rename: %v\n%s%s\nwant %q", cmd, err, stdout, stderr, want)
		}
	}
}

// TestPlainPHPBesideItsPHN: a .php beside a .phn of the same name is not
// checked, the .phn's output being at its path, and the build does not copy
// it over that output: it says so, and the output stays the .phn's.
func TestPlainPHPBesideItsPHN(t *testing.T) {
	bin := phnBinary(t)
	root := plainTree(t)
	writeTree(t, root, map[string]string{
		"src/Foo.phn": "namespace App;\n\nfinal class Foo {\n    compiled(): int => 1;\n}\n",
		"src/Foo.php": "<?php\n\nnamespace App;\n\nfinal class Foo\n{\n    public function copied(): int\n    {\n        return 2;\n    }\n}\n",
	})
	if _, stderr, err := run(t, root, bin, "check"); err != nil || len(findings(stderr)) > 0 {
		t.Errorf("phn check: %v\n%s", err, stderr)
	}
	_, stderr, err := run(t, root, bin, "build")
	if err != nil || !strings.Contains(stderr, "src/Foo.php: not copied: src/Foo.phn beside it compiles to .src/Foo.php") {
		t.Errorf("phn build: %v\n%s", err, stderr)
	}
	if out, _ := os.ReadFile(filepath.Join(root, ".src", "Foo.php")); !bytes.Contains(out, []byte("function compiled()")) {
		t.Errorf(".src/Foo.php is not the .phn's output:\n%s", out)
	}
	if stdout, stderr, err := run(t, root, bin, "build", "--check"); err != nil {
		t.Errorf("phn build --check after a build: %v\n%s%s", err, stdout, stderr)
	}
}

// TestOutputsAreNotChecked: what lies in an output directory is never
// checked, by phn check given the whole tree or by phn build, which removes
// a file there that no source stands behind.
func TestOutputsAreNotChecked(t *testing.T) {
	bin := phnBinary(t)
	root := plainTree(t)
	writeTree(t, root, map[string]string{".src/Stray.php": "<?php\n\nnamespace App;\n\nfunction stray(): int\n{\n    return 'not an int';\n}\n"})
	if _, stderr, err := run(t, root, bin, "check", "."); err != nil || strings.Contains(stderr, "Stray") {
		t.Errorf("phn check .: %v\n%s", err, stderr)
	}
	stdout, stderr, err := run(t, root, bin, "build")
	if err != nil || strings.Contains(stderr, "Stray") || !strings.Contains(stdout, "1 stale outputs removed") {
		t.Errorf("phn build: %v\n%s%s", err, stdout, stderr)
	}
}

// TestNoDevLeavesPlainPHPOfADevRoot: under --no-dev a development root is
// left out whole, so a plain .php in it is neither checked nor copied,
// while one in the other roots is checked and copied as ever. Without the
// switch its error fails the build.
func TestNoDevLeavesPlainPHPOfADevRoot(t *testing.T) {
	bin := phnBinary(t)
	root := plainTree(t)
	writeTree(t, root, map[string]string{
		"composer.json":   `{"name": "acme/app", "require": {"php": ">=8.5"}, "autoload": {"psr-4": {"App\\": ".src/"}}, "autoload-dev": {"psr-4": {"App\\Tests\\": ".tests/"}}}`,
		"phaenon.json":    `{"roots": {"src": ".src", "tests": ".tests"}}`,
		"tests/Wrong.php": "<?php\n\nnamespace App\\Tests;\n\nfunction wrong(): int\n{\n    return 'not an int';\n}\n",
	})
	stdout, stderr, err := run(t, root, bin, "build", "--no-dev")
	if err != nil || len(findings(stderr)) > 0 || !strings.Contains(stdout, "--no-dev leaves out tests") {
		t.Errorf("phn build --no-dev (%v):\n%s%s", err, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, ".src", "Helper.php")); err != nil {
		t.Errorf("the plain .php of the source root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".tests")); err == nil {
		t.Error("the development root was built")
	}
	_, stderr, err = run(t, root, bin, "build")
	if err == nil || !slices.Contains(findings(stderr), "tests/Wrong.php:7:12: error P2202") {
		t.Errorf("phn build of every root (%v):\n%s", err, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, ".tests", "Wrong.php")); err == nil {
		t.Error(".tests/Wrong.php was copied with its error")
	}
}

// TestInstalledPackagePlainPHP: an installed package's plain .php under a
// root is read for what it declares, so that its .phn may use it, and is
// not checked: the package's own tools judge it, as the findings of its
// .phn are only warnings. It is copied as it is, one that does not parse
// too, of which nothing is said; one under a path the package excludes
// stays out of the program as in a project.
func TestInstalledPackagePlainPHP(t *testing.T) {
	bin := phnBinary(t)
	root := vendorTree(t)
	writeTree(t, root, map[string]string{
		"vendor/acme/lib/src/Plain.php":      "<?php\n\nnamespace Acme\\Lib;\n\nfinal class Plain\n{\n    public function size(): int\n    {\n        return 'not an int';\n    }\n}\n",
		"vendor/acme/lib/src/UsesPlain.phn":  "namespace Acme\\Lib;\n\nfunction sized(Plain $p): int => $p->size();\n",
		"vendor/acme/lib/src/Unparsed.php":   "<?php\n\nfunction unparsed( {\n",
		"vendor/acme/lib/phaenon.json":       `{"roots": {"src": ".src", "tests": ".tests"}, "exclude": ["src/Fixtures"]}`,
		"vendor/acme/lib/src/Fixtures/b.php": "<?php\n\nnamespace Acme\\Lib;\n\nfinal class Box\n{\n}\n",
	})
	stdout, stderr, err := run(t, root, bin, "build", "--vendor")
	if err != nil {
		t.Fatalf("phn build --vendor: %v\n%s%s", err, stdout, stderr)
	}
	if strings.Contains(stderr, "Plain") || strings.Contains(stderr, "Unparsed") || strings.Contains(stderr, "P2511") {
		t.Errorf("a finding about a plain .php or what uses it:\n%s", stderr)
	}
	for _, f := range []string{"Unparsed.php", "Fixtures/b.php"} {
		if _, err := os.Stat(filepath.Join(root, "vendor/acme/lib/.src", f)); err != nil {
			t.Errorf("the package's %s is not copied: %v", f, err)
		}
	}
	src, _ := os.ReadFile(filepath.Join(root, "vendor/acme/lib/src/Plain.php"))
	if out, err := os.ReadFile(filepath.Join(root, "vendor/acme/lib/.src/Plain.php")); err != nil || !bytes.Equal(src, out) {
		t.Errorf("the package's plain .php is not copied as it is: %v", err)
	}
}

// TestBuildCountsAFailedFileOnce: under --emit-on-error a file that does
// not parse is one file that failed, reported once. It was counted as a
// file with errors and again when the emitter could not write it ("3 of 3
// files failed to compile" for two), and its syntax error was printed a
// second time without its code.
func TestBuildCountsAFailedFileOnce(t *testing.T) {
	bin := phnBinary(t)
	root := plainTree(t)
	writeTree(t, root, map[string]string{
		"src/Bad.phn":   "namespace App;\n\nfunction bad(: int => 1;\n",
		"src/Typed.phn": "namespace App;\n\nfunction typed(): int => 'x';\n",
	})
	for _, args := range [][]string{{"build"}, {"build", "--emit-on-error"}} {
		_, stderr, status := run(t, root, bin, args...)
		if status == nil {
			t.Errorf("phn %s should fail", strings.Join(args, " "))
		}
		if !strings.Contains(stderr, "\n2 of 3 files failed to compile\n") {
			t.Errorf("phn %s:\n%s\nwant 2 of 3 files failed to compile", strings.Join(args, " "), stderr)
		}
		if n := strings.Count(stderr, "unexpected token"); n != 1 {
			t.Errorf("phn %s says the syntax error %d times:\n%s", strings.Join(args, " "), n, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".src", "Typed.php")); err != nil {
		t.Errorf("--emit-on-error writes the file with a type error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".src", "Bad.php")); err == nil {
		t.Errorf("a file that does not parse was written")
	}
	_, stderr, status := run(t, root, bin, "build", "--check", "--emit-on-error")
	if status == nil || !strings.Contains(stderr, "\n1 of 3 files failed to compile\n") {
		t.Errorf("phn build --check --emit-on-error (%v):\n%s\nwant the unparsed file alone to fail", status, stderr)
	}
}

// TestBuildCountsARefusalOnce: the emitter refuses what the checker
// refuses, a construct that left the language (P1001) and a partial
// application with no exact lowering for PHP 8.5 (P1002); under
// --emit-on-error such a file was counted as a file with errors and again
// when the emitter refused it ("4 of 4 files failed to compile" for two),
// and the refusal was printed a second time without its code.
func TestBuildCountsARefusalOnce(t *testing.T) {
	bin := phnBinary(t)
	root := plainTree(t)
	writeTree(t, root, map[string]string{
		"src/Left.phn":    "namespace App;\n\nfinal class Left {\n    fun gone(): int => 1;\n}\n",
		"src/Refused.phn": "namespace App;\n\nfunction pair(string $a, string $b): string => $a . $b;\n\nfunction bound(): \\Closure => pair(strtoupper('x'), ?);\n",
	})
	for _, args := range [][]string{{"build"}, {"build", "--emit-on-error"}, {"build", "--check", "--emit-on-error"}} {
		_, stderr, status := run(t, root, bin, args...)
		if status == nil {
			t.Errorf("phn %s should fail", strings.Join(args, " "))
		}
		if !strings.Contains(stderr, "\n2 of 3 files failed to compile\n") {
			t.Errorf("phn %s:\n%s\nwant 2 of 3 files failed to compile", strings.Join(args, " "), stderr)
		}
		for _, said := range []string{"left the language", "needs PHP 8.6 (target 8.5)"} {
			if n := strings.Count(stderr, said); n != 1 {
				t.Errorf("phn %s says %q %d times:\n%s", strings.Join(args, " "), said, n, stderr)
			}
		}
	}
}
