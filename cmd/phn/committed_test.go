package main

// phaenon.json's "committed" (design/roadmap.md D28, design/tooling.md §3.7,
// T18): the roots whose outputs a library's repository tracks. Where the
// library is installed, `phn build --vendor` leaves them as installed and
// fails when they came without their outputs; in the library's own project
// every command treats them as any root, but that a build keeps no stamp
// there.

import (
	"bufio"
	"encoding/json"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// projectFileVectors is testdata/project-files.json, the reference of how
// "roots" and "committed" are read.
type projectFileVectors struct {
	Vectors []struct {
		Name      string            `json:"name"`
		File      string            `json:"file"`
		Refused   bool              `json:"refused"`
		Roots     map[string]string `json:"roots"`
		Committed []string          `json:"committed"`
		Compiler  string            `json:"compiler"`
	} `json:"vectors"`
}

// The key lists source roots, each a key of "roots"; anything else is an
// error of the project file, and no key or an empty list is a project that
// commits nothing.
func TestProjectFileVectors(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "project-files.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors projectFileVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	for _, v := range vectors.Vectors {
		path := filepath.Join(t.TempDir(), ProjectFile)
		writeTree(t, filepath.Dir(path), map[string]string{ProjectFile: v.File})
		p, err := readProject(path)
		switch {
		case v.Compiler != "":
			if err == nil || !strings.Contains(err.Error(), v.Compiler) {
				t.Errorf("%s: error %v, want one saying %q", v.Name, err, v.Compiler)
			}
		case v.Refused:
			if err == nil {
				t.Errorf("%s: read as roots %v, committed %v; want an error", v.Name, p.Roots, p.Committed)
			} else if !strings.HasPrefix(err.Error(), path+": ") {
				t.Errorf("%s: the error does not name the file: %v", v.Name, err)
			}
		case err != nil:
			t.Errorf("%s: %v", v.Name, err)
		default:
			roots := map[string]string{}
			for src, out := range v.Roots {
				roots[filepath.FromSlash(src)] = filepath.FromSlash(out)
			}
			committed := []string{}
			for _, src := range v.Committed {
				committed = append(committed, filepath.FromSlash(src))
			}
			if !maps.Equal(p.Roots, roots) || !slices.Equal(p.Committed, committed) {
				t.Errorf("%s: roots %v, committed %v; want %v, %v", v.Name, p.Roots, p.Committed, roots, committed)
			}
		}
	}
}

// An entry that is no root is named, as written, in an error that says what
// the key lists; every command of the project fails on it.
func TestCommittedEntryThatIsNoRoot(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	writeTree(t, root, map[string]string{"phaenon.json": `{"roots": {"src": ".src"}, "committed": ["lib/"]}`})
	const want = `phaenon.json: "committed" lists source roots, each of them a key of "roots"; "lib/" is not one`
	for _, cmd := range []string{"build", "check"} {
		_, stderr, err := run(t, root, bin, cmd)
		var exit *exec.ExitError
		if !asExit(err, &exit) || exit.ExitCode() != 1 || strings.TrimSpace(stderr) != want {
			t.Errorf("phn %s: %v, stderr %q; want status 1 and %q", cmd, err, stderr, want)
		}
	}
}

func asExit(err error, exit **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	*exit = e
	return ok
}

// snapshot reads every file under dir, by its path relative to dir.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// expectSnapshot fails the test unless dir holds exactly the files of want,
// byte for byte.
func expectSnapshot(t *testing.T, what, dir string, want map[string]string) {
	t.Helper()
	got := snapshot(t, dir)
	for _, path := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[path]; !ok {
			t.Errorf("%s: %s was written", what, path)
		}
	}
	for _, path := range slices.Sorted(maps.Keys(want)) {
		switch text, ok := got[path]; {
		case !ok:
			t.Errorf("%s: %s was removed", what, path)
		case text != want[path]:
			t.Errorf("%s: %s was changed:\n%s", what, path, text)
		}
	}
}

// kitBox is the generic class of acme/kit, and shippedBox the output the
// package ships for it: not what this compiler writes, and a class that is
// not generic, so that a test can tell which of the two was read.
const (
	kitBox     = "namespace Acme\\Kit;\n\nfinal class Box<T> {\n    __construct(private T $value) {}\n\n    get(): T => $this->value;\n}\n"
	shippedBox = "<?php\n\n// As the package's own compiler wrote it.\n\nnamespace Acme\\Kit;\n\nfinal class Box\n{\n    public function __construct(private string $value)\n    {\n    }\n\n    public function get(): string\n    {\n        return $this->value;\n    }\n}\n"
)

// committedTree is a root project that installed acme/kit, a library that
// commits the outputs of src: the package holds them as its release shipped
// them, with a copy that is stale and a file no source stands behind, which
// a build would rewrite and remove. Its dist archive left tests/ out.
// acme/lib beside it is a library that ships its sources alone.
func committedTree(t *testing.T) string {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"composer.json": `{"name": "acme/root", "require": {"php": ">=8.5", "acme/kit": "*", "acme/lib": "*"}, "autoload": {"psr-4": {"App\\": ".src/"}}}`,
		"phaenon.json":  `{"roots": {"src": ".src"}}`,
		"src/Use.phn":   "namespace App;\n\nuse Acme\\Kit\\Box;\n\nfunction unbox(Box<int> $b): int => $b->get();\n",
		"vendor/composer/installed.json": `{"packages": [
			{"name": "acme/kit", "version": "1.0.0", "install-path": "../acme/kit"},
			{"name": "acme/lib", "version": "1.2.0", "install-path": "../acme/lib"}
		], "dev": true, "dev-package-names": []}`,
		"vendor/composer/autoload_psr4.php": "<?php\n\n$vendorDir = dirname(__DIR__);\n$baseDir = dirname($vendorDir);\n\nreturn array(\n    'App\\\\' => array($baseDir . '/.src'),\n    'Acme\\\\Kit\\\\Tests\\\\' => array($vendorDir . '/acme/kit/.tests'),\n    'Acme\\\\Kit\\\\' => array($vendorDir . '/acme/kit/.src'),\n    'Acme\\\\Lib\\\\' => array($vendorDir . '/acme/lib/.src'),\n);\n",
		"vendor/acme/kit/composer.json":     `{"name": "acme/kit", "require": {"php": ">=8.5"}, "autoload": {"psr-4": {"Acme\\Kit\\": ".src/"}}}`,
		"vendor/acme/kit/phaenon.json":      `{"roots": {"src": ".src", "tests": ".tests"}, "committed": ["src"]}`,
		"vendor/acme/kit/src/Box.phn":       kitBox,
		"vendor/acme/kit/src/Sub/Leaf.phn":  "namespace Acme\\Kit\\Sub;\n\nfinal class Leaf {}\n",
		"vendor/acme/kit/src/data.json":     `{"source": true}`,
		"vendor/acme/kit/.src/Box.php":      shippedBox,
		"vendor/acme/kit/.src/Sub/Leaf.php": "<?php\n\nnamespace Acme\\Kit\\Sub;\n\nfinal class Leaf\n{\n}\n",
		"vendor/acme/kit/.src/data.json":    `{"shipped": true}`,
		"vendor/acme/kit/.src/Orphan.php":   "<?php\n",
		"vendor/acme/lib/composer.json":     `{"name": "acme/lib", "require": {"php": ">=8.5"}, "autoload": {"psr-4": {"Acme\\Lib\\": ".src/"}}}`,
		"vendor/acme/lib/phaenon.json":      `{"roots": {"src": ".src"}}`,
		"vendor/acme/lib/src/Thing.phn":     "namespace Acme\\Lib;\n\nfinal class Thing {}\n",
	})
	return root
}

// An installed package's committed root is left byte for byte as installed:
// nothing compiled, no copy refreshed, no orphan removed, no stamp. A
// package whose installed roots are all committed is nothing to build,
// while the package beside it, which commits nothing, is built as before.
func TestBuildVendorLeavesCommittedRoots(t *testing.T) {
	bin := phnBinary(t)
	root := committedTree(t)
	kit := filepath.Join(root, "vendor/acme/kit")
	shipped := snapshot(t, kit)

	for _, want := range []string{
		"1 installed packages built, 0 up to date, 1 left as installed with their committed outputs\n",
		"0 installed packages built, 1 up to date, 1 left as installed with their committed outputs\n",
	} {
		stdout, stderr, err := run(t, root, bin, "build", "--vendor")
		if err != nil {
			t.Fatalf("phn build --vendor: %v\n%s%s", err, stdout, stderr)
		}
		if !strings.HasSuffix(stdout, want) || strings.Contains(stdout, "acme/kit") {
			t.Errorf("stdout:\n%s\nwant no word of acme/kit, and the summary\n%s", stdout, want)
		}
		expectSnapshot(t, "acme/kit", kit, shipped)
	}
	for _, f := range []string{"vendor/acme/lib/.src/Thing.php", "vendor/acme/lib/.src/" + stampFile} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
}

// A root the key does not name is built as before when the install has it:
// its outputs and its stamp are written, the committed root beside it left
// alone, and its program reads the committed root from the sources.
func TestBuildVendorBuildsTheRootsNotCommitted(t *testing.T) {
	bin := phnBinary(t)
	root := committedTree(t)
	kit := filepath.Join(root, "vendor/acme/kit")
	writeTree(t, kit, map[string]string{
		"tests/BoxTest.phn": "namespace Acme\\Kit\\Tests;\n\nuse Acme\\Kit\\Box;\n\nfunction unbox(Box<int> $b): int => $b->get();\n",
		"tests/fixture.txt": "copied\n",
	})
	shipped := snapshot(t, filepath.Join(kit, ".src"))

	stdout, stderr, err := run(t, root, bin, "build", "--vendor")
	if err != nil {
		t.Fatalf("phn build --vendor: %v\n%s%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "acme/kit: 1 files compiled, 1 written\n") || !strings.HasSuffix(stdout, "2 installed packages built, 0 up to date\n") {
		t.Errorf("stdout:\n%s", stdout)
	}
	if strings.Contains(stderr, "warning") {
		t.Errorf("the tests root should check against Box<T> as the sources declare it:\n%s", stderr)
	}
	for _, f := range []string{".tests/BoxTest.php", ".tests/fixture.txt", ".tests/" + stampFile} {
		if _, err := os.Stat(filepath.Join(kit, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	expectSnapshot(t, "acme/kit's committed root", filepath.Join(kit, ".src"), shipped)

	stdout, _, err = run(t, root, bin, "build", "--vendor")
	if err != nil || !strings.HasSuffix(stdout, "0 installed packages built, 2 up to date\n") {
		t.Errorf("a second build should skip both packages (%v):\n%s", err, stdout)
	}
	writeTree(t, kit, map[string]string{"tests/fixture.txt": "changed\n"})
	stdout, _, _ = run(t, root, bin, "build", "--vendor")
	if !strings.HasSuffix(stdout, "1 installed packages built, 1 up to date\n") {
		t.Errorf("a changed source of the tests root should rebuild the package:\n%s", stdout)
	}
	expectSnapshot(t, "acme/kit's committed root", filepath.Join(kit, ".src"), shipped)
}

// A committed root installed without its outputs is an error naming the
// package, its version and the root, and saying what the project can do
// about another package's release. The command fails before it builds
// anything, the broken root and the other packages included: a build would
// hide a release that is broken for every project without phn.
func TestBuildVendorMissingCommittedOutputs(t *testing.T) {
	bin := phnBinary(t)
	root := committedTree(t)
	if err := os.Remove(filepath.Join(root, "vendor/acme/kit/.src/Box.php")); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, filepath.Join(root, "vendor"))

	stdout, stderr, err := run(t, root, bin, "build", "--vendor")

	var exit *exec.ExitError
	if !asExit(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("phn build --vendor: %v, want exit status 1\n%s%s", err, stdout, stderr)
	}
	want := "acme/kit 1.0.0: its phaenon.json commits the outputs of the root src, but this release is installed without them: .src/Box.php is missing\n" +
		committedGapsSummary(1) + "\n"
	if stderr != want || stdout != "" {
		t.Errorf("stdout:\n%s\nstderr:\n%s\nwant on stderr\n%s", stdout, stderr, want)
	}
	expectSnapshot(t, "the vendor directory", filepath.Join(root, "vendor"), before)
}

// committedOutputVectors is testdata/committed-outputs.json, the reference
// of the check of an installed package's committed roots, which the
// Composer plugin's is held to as well.
type committedOutputVectors struct {
	Summary map[string]string `json:"summary"`
	Vectors []struct {
		Name       string            `json:"name"`
		Project    string            `json:"project"`
		Files      map[string]string `json:"files"`
		Links      map[string]string `json:"links"`
		Unreadable []string          `json:"unreadable"`
		Errors     []string          `json:"errors"`
		Builds     bool              `json:"builds"`
		Undecided  bool              `json:"undecided"`
	} `json:"vectors"`
}

// The check's vectors, each as a package installed in a project of its own:
// `phn build --vendor` prints the errors and the summary, whole, and fails;
// without errors it builds the roots the package does not commit, or finds
// nothing to build.
func TestCommittedOutputVectors(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "committed-outputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors committedOutputVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Vectors) == 0 || len(vectors.Summary) == 0 {
		t.Fatal("no vectors")
	}
	for n, want := range vectors.Summary {
		count, err := strconv.Atoi(n)
		if err != nil {
			t.Fatal(err)
		}
		if got := committedGapsSummary(count); got != want {
			t.Errorf("the summary for %d packages:\n%s\nwant\n%s", count, got, want)
		}
	}
	bin := phnBinary(t)
	for _, v := range vectors.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			if v.Undecided && os.Getuid() == 0 {
				t.Skip("root reads every directory")
			}
			root := t.TempDir()
			kit := filepath.Join(root, "vendor/acme/kit")
			writeTree(t, root, map[string]string{
				"composer.json":                     `{"name": "acme/root", "require": {"php": ">=8.5", "acme/kit": "*"}}`,
				"vendor/composer/installed.json":    `{"packages": [{"name": "acme/kit", "version": "1.0.0", "install-path": "../acme/kit"}], "dev": true, "dev-package-names": []}`,
				"vendor/composer/autoload_psr4.php": "<?php\n\nreturn array();\n",
				"vendor/acme/kit/composer.json":     `{"name": "acme/kit", "require": {"php": ">=8.5"}}`,
				"vendor/acme/kit/phaenon.json":      v.Project,
			})
			writeTree(t, kit, v.Files)
			for link, target := range v.Links {
				if err := os.Symlink(filepath.FromSlash(target), filepath.Join(kit, link)); err != nil {
					t.Fatal(err)
				}
			}
			for _, dir := range v.Unreadable {
				if err := os.Chmod(filepath.Join(kit, dir), 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(filepath.Join(kit, dir), 0o755) })
			}

			stdout, stderr, err := run(t, root, bin, "build", "--vendor")

			var exit *exec.ExitError
			switch {
			case v.Undecided:
				if !asExit(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(stderr, "permission denied") {
					t.Errorf("phn build --vendor: %v, want exit status 1 and the directory's error\n%s%s", err, stdout, stderr)
				}
			case len(v.Errors) > 0:
				want := strings.Join(v.Errors, "\n") + "\n" + vectors.Summary["1"] + "\n"
				if !asExit(err, &exit) || exit.ExitCode() != 1 || stderr != want || stdout != "" {
					t.Errorf("phn build --vendor: %v\nstdout:\n%s\nstderr:\n%s\nwant exit status 1 and on stderr\n%s", err, stdout, stderr, want)
				}
			case v.Builds:
				if err != nil || !strings.HasSuffix(stdout, "1 installed packages built, 0 up to date\n") {
					t.Errorf("phn build --vendor: %v, want the package built\n%s%s", err, stdout, stderr)
				}
			default:
				if want := "0 installed packages built, 0 up to date, 1 left as installed with their committed outputs\n"; err != nil || stdout != want || stderr != "" {
					t.Errorf("phn build --vendor: %v, want nothing to build\n%s%s", err, stdout, stderr)
				}
			}
		})
	}
}

// The roots a package does not commit are compiled with the ones it does:
// a change of a committed root's sources makes their outputs stale, and the
// package is built again.
func TestBuildVendorRebuildsWhenACommittedRootChanges(t *testing.T) {
	bin := phnBinary(t)
	root := committedTree(t)
	kit := filepath.Join(root, "vendor/acme/kit")
	writeTree(t, kit, map[string]string{
		"tests/BoxTest.phn": "namespace Acme\\Kit\\Tests;\n\nuse Acme\\Kit\\Box;\n\nfunction unbox(Box<int> $b): int => $b->get();\n",
	})
	if stdout, stderr, err := run(t, root, bin, "build", "--vendor"); err != nil || strings.Contains(stderr, "warning") {
		t.Fatalf("phn build --vendor: %v\n%s%s", err, stdout, stderr)
	}
	if stdout, _, _ := run(t, root, bin, "build", "--vendor"); !strings.HasSuffix(stdout, "0 installed packages built, 2 up to date\n") {
		t.Fatalf("a second build should skip both packages:\n%s", stdout)
	}
	shipped := snapshot(t, filepath.Join(kit, ".src"))

	writeTree(t, kit, map[string]string{"src/Box.phn": strings.Replace(kitBox, "get()", "value()", 1)})
	stdout, stderr, err := run(t, root, bin, "build", "--vendor")

	if err != nil || !strings.HasSuffix(stdout, "1 installed packages built, 1 up to date\n") {
		t.Errorf("a change in the committed root should rebuild the package (%v):\n%s%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "BoxTest.phn:5") || !strings.Contains(stderr, "warning P2") {
		t.Errorf("the rebuild should report what the change broke in the tests root, as a warning:\n%s", stderr)
	}
	expectSnapshot(t, "acme/kit's committed root", filepath.Join(kit, ".src"), shipped)
}

// A project's checker reads an installed package's committed root from its
// sources, as it reads any installed Phaenon package: Box<T> is generic
// there, while the output the package ships declares a class that is not.
// Without the package's project file the same tree is read as PHP, and the
// project's Box<int> is refused.
func TestCheckReadsACommittedPackageFromItsSources(t *testing.T) {
	bin := phnBinary(t)
	root := committedTree(t)
	for _, cmd := range []string{"check", "build"} {
		stdout, stderr, err := run(t, root, bin, cmd)
		if err != nil {
			t.Errorf("phn %s should read Box<T> from the package's sources (%v):\n%s%s", cmd, err, stdout, stderr)
		}
	}
	if err := os.Remove(filepath.Join(root, "vendor/acme/kit/phaenon.json")); err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, err := run(t, root, bin, "check"); err == nil {
		t.Errorf("the control passed: the shipped Box is not generic, and phn check read it as if it were:\n%s%s", stdout, stderr)
	}
}

// A project without the key is built as before, with the summary it had.
func TestBuildVendorWithoutTheKey(t *testing.T) {
	bin := phnBinary(t)
	root := vendorTree(t)
	for _, want := range []string{"1 installed packages built, 0 up to date\n", "0 installed packages built, 1 up to date\n"} {
		stdout, stderr, err := run(t, root, bin, "build", "--vendor")
		if err != nil || !strings.HasSuffix(stdout, want) {
			t.Errorf("phn build --vendor (%v):\n%s%swant the summary %s", err, stdout, stderr, want)
		}
	}
}

// ownTrees are two copies of one project, a library's own checkout: the
// second names its root under "committed". An output with no source and a
// stale copy wait in both for a build to remove and refresh.
func ownTrees(t *testing.T) (plain, committed string) {
	t.Helper()
	plain, committed = fixTree(t), fixTree(t)
	for _, root := range []string{plain, committed} {
		writeTree(t, root, map[string]string{
			".src/Orphan.php": "<?php\n",
			".src/data.php":   "<?php\nreturn array();\n",
		})
	}
	writeTree(t, committed, map[string]string{"phaenon.json": `{"roots": {"src": ".src"}, "committed": ["src"]}`})
	return plain, committed
}

// In the library's own project nothing changes (T4 stands) but what a build
// does with a stamp (TestBuildRemovesAStampFromItsOwnCommittedRoot): build,
// its --check, check, fmt and fix do in a project that commits its root
// exactly what they do in the same project without the key, to the same
// outputs.
func TestCommittedRootInItsOwnProject(t *testing.T) {
	bin := phnBinary(t)
	plain, committed := ownTrees(t)
	tool := fakeToolCommand(t, boxFixes)
	steps := [][]string{
		{"build", "--check"}, // stale: the orphan, the copy, the missing output
		{"check"},
		{"build"},
		{"build", "--check"},
		{"build", "--vendor"},
		{"fmt", "--check"},
		{"fix", "--check", "--", tool, "fix"},
		{"fix", "--", tool, "fix"},
		{"fmt"},
		{"build", "--check"},
	}
	for i, args := range steps {
		wantOut, wantErr, wantStatus := run(t, plain, bin, args...)
		stdout, stderr, status := run(t, committed, bin, args...)
		if stdout != wantOut || stderr != wantErr || (status == nil) != (wantStatus == nil) {
			t.Errorf("step %d, phn %s: with the key\n%s%s(%v)\nwithout it\n%s%s(%v)", i, strings.Join(args, " "), stdout, stderr, status, wantOut, wantErr, wantStatus)
		}
		got, want := snapshot(t, committed), snapshot(t, plain)
		delete(got, "phaenon.json")
		delete(want, "phaenon.json")
		if !maps.Equal(got, want) {
			t.Errorf("step %d, phn %s: the two trees differ:\nwith the key    %v\nwithout the key %v", i, strings.Join(args, " "), slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
		}
	}
	for _, f := range []string{".src/Orphan.php", ".src/" + stampFile} {
		if _, err := os.Stat(filepath.Join(committed, f)); err == nil {
			t.Errorf("%s is in the built tree", f)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(committed, ".src/Box.php")); !strings.Contains(string(data), "final class Box") {
		t.Errorf("the committed root was not built:\n%s", data)
	}
}

// In its own project a library's committed root keeps no stamp: one found
// there is what a build as an installed package left before the root was
// committed. `phn build --check` reports it as it reports an output with no
// source, and `phn build` removes it, so that a stamp git tracks shows as
// a change of the tree and the library's gate fails. A root the project
// does not commit keeps its stamp, which is the record of such a build.
func TestBuildRemovesAStampFromItsOwnCommittedRoot(t *testing.T) {
	bin := phnBinary(t)
	plain, committed := ownTrees(t)
	for _, root := range []string{plain, committed} {
		writeTree(t, root, map[string]string{".src/" + stampFile: "phn 0.4.1\n"})
		if stdout, stderr, err := run(t, root, bin, "build"); err != nil {
			t.Fatalf("phn build: %v\n%s%s", err, stdout, stderr)
		}
		writeTree(t, root, map[string]string{".src/" + stampFile: "phn 0.4.1\n"})
	}

	_, stderr, err := run(t, committed, bin, "build", "--check")
	if want := ".src/" + stampFile + ": no source behind it\n"; err == nil || !strings.Contains(stderr, want) {
		t.Errorf("phn build --check in the project that commits its root: %v\n%s\nwant a failure saying\n%s", err, stderr, want)
	}
	if stdout, stderr, err := run(t, committed, bin, "build"); err != nil || !strings.Contains(stdout, "1 stale outputs removed") {
		t.Errorf("phn build in the project that commits its root: %v\n%s%s", err, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(committed, ".src", stampFile)); err == nil {
		t.Error("the stamp is still in the committed root")
	}
	if stdout, stderr, err := run(t, committed, bin, "build", "--check"); err != nil {
		t.Errorf("phn build --check after the build: %v\n%s%s", err, stdout, stderr)
	}

	if stdout, stderr, err := run(t, plain, bin, "build", "--check"); err != nil {
		t.Errorf("phn build --check in the project that commits nothing: %v\n%s%s", err, stdout, stderr)
	}
	if stdout, stderr, err := run(t, plain, bin, "build"); err != nil || !strings.Contains(stdout, "0 stale outputs removed") {
		t.Errorf("phn build in the project that commits nothing: %v\n%s%s", err, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(plain, ".src", stampFile)); err != nil {
		t.Errorf("the stamp of a root the project does not commit: %v", err)
	}
}

// phn lsp builds a committed root on save, as the editor does for any root.
func TestLSPBuildsACommittedRoot(t *testing.T) {
	bin := phnBinary(t)
	_, root := ownTrees(t)
	s := startLSP(t, bin, root, `{"buildOnSave": true}`)
	uri := fileURI(filepath.Join(root, "src/Box.phn"))
	open, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": "phaenon", "version": 1, "text": boxSource}}})
	s.send(string(open))
	s.send(`{"jsonrpc":"2.0","method":"textDocument/didSave","params":{"textDocument":{"uri":"` + uri + `"}}}`)
	m := s.next(func(m map[string]any) bool { return m["method"] == "phaenon/build" })
	if p := m["params"].(map[string]any); p["files"] != float64(1) || p["withheld"] != float64(0) {
		t.Errorf("build: %v", p)
	}
	if _, err := os.Stat(filepath.Join(root, ".src/Box.php")); err != nil {
		t.Errorf("the output of Box.phn: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".src/Orphan.php")); err == nil {
		t.Error("the output with no source is still there")
	}
}

// phn watch builds a committed root at start and when a source changes.
func TestWatchBuildsACommittedRoot(t *testing.T) {
	bin := phnBinary(t)
	_, root := ownTrees(t)
	cmd := exec.Command(bin, "watch", "--interval", "50ms")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PHN_PHP=/nonexistent-php")
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	wait := func(want string) {
		t.Helper()
		timeout := time.After(20 * time.Second)
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("phn watch ended before printing %q", want)
				}
				if strings.Contains(line, want) {
					return
				}
			case <-timeout:
				t.Fatalf("phn watch never printed %q", want)
			}
		}
	}
	wait("waiting for changes")
	if _, err := os.Stat(filepath.Join(root, ".src/Box.php")); err != nil {
		t.Fatalf("the first build: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // a modification time the poll can tell apart
	writeTree(t, root, map[string]string{"src/Box.phn": strings.Replace(boxSource, "$a+$b", "$a-$b", 1)})
	wait("2 files checked")
	wait("waiting for changes")
	if data, _ := os.ReadFile(filepath.Join(root, ".src/Box.php")); !strings.Contains(string(data), "$a-$b") {
		t.Errorf("the change was not built:\n%s", data)
	}
}
