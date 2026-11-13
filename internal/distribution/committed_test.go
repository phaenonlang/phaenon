package distribution

// A library that commits its outputs (design/roadmap.md D28,
// design/tooling.md §3.7, T18), installed under a real Composer: by a
// project of plain PHP, which gets no plugin, no binary and no build, and
// by a project with the plugin, which leaves the committed root as
// installed and fails the dump when the package came without its outputs.

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// kit is acme/kit, a library that commits the outputs of its root src and
// not those of tests: a git repository whose commit holds .src/ as the real
// phn wrote it, beside the sources.
type kit struct {
	w   *world
	dir string
}

// kitFiles is the library before it is built. Its .gitattributes is what
// §3.7 asks for: the outputs marked as generated, tests/ left out of the
// archive, so that a project that installs a release has no root to build.
var kitFiles = map[string]string{
	"composer.json": `{
  "name": "acme/kit",
  "version": "1.0.0",
  "require": {"php": ">=8.5"},
  "autoload": {"psr-4": {"Acme\\Kit\\": ".src/"}},
  "autoload-dev": {"psr-4": {"Acme\\Kit\\Tests\\": ".tests/"}}
}
`,
	"phaenon.json":   `{"roots": {"src": ".src", "tests": ".tests"}, "committed": ["src"]}` + "\n",
	".gitignore":     "/.tests/\n.phn-build\n/vendor/\n",
	".gitattributes": "/.src/** linguist-generated\n/tests export-ignore\n/.gitattributes export-ignore\n/.gitignore export-ignore\n",
	"src/Box.phn": `namespace Acme\Kit;

final class Box<T>
{
    __construct(private T $value) {}

    get(): T => $this->value;
}
`,
	// key() returns bytes, which the output can only call string: a project
	// written in Phaenon knows the difference when it reads the sources.
	"src/Greeter.phn": `namespace Acme\Kit;

final class Greeter
{
    /** The greeting, from the file the build copies beside the class. */
    greet(Box<string> $name): string {
        $greetings = json_decode((string) file_get_contents(__DIR__ . '/greetings.json'), true);
        $greeting = is_array($greetings) && is_string($greetings['en'] ?? null) ? $greetings['en'] : 'Hi';

        return $greeting . ', ' . $name->get();
    }

    key(): bytes => (bytes) "\x00kit";
}
`,
	"src/greetings.json": `{"en": "Hello"}` + "\n",
	"tests/GreeterTest.phn": `namespace Acme\Kit\Tests;

use Acme\Kit\Box;
use Acme\Kit\Greeter;

final class GreeterTest
{
    greets(): bool => (new Greeter())->greet(new Box('Ada')) === 'Hello, Ada';
}
`,
}

// buildPhn builds the real phn of this commit into the world.
func (w *world) buildPhn() string {
	w.t.Helper()
	phn := filepath.Join(w.base, "phn")
	if _, err := os.Stat(phn); err == nil {
		return phn
	}
	build := exec.Command("go", "build", "-o", phn, "./cmd/phn")
	build.Dir = repositoryRoot(w.t)
	if out, err := build.CombinedOutput(); err != nil {
		w.t.Fatalf("go build ./cmd/phn: %v\n%s", err, out)
	}
	return phn
}

// newKit writes the library into a directory of its own under the world,
// builds it there as its own project is built (`phn build`, every root: T4)
// and commits the tree, the outputs of tests ignored. release, when given,
// changes the built tree before the commit: a release that is broken.
func (w *world) newKit(name string, release func(dir string)) *kit {
	w.t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		w.t.Skip("no git on PATH")
	}
	k := &kit{w: w, dir: filepath.Join(w.base, "kits", name)}
	for path, text := range kitFiles {
		writeFile(w.t, filepath.Join(k.dir, path), []byte(text), 0o644)
	}
	build := exec.Command(w.buildPhn(), "build")
	build.Dir = k.dir
	if out, err := build.CombinedOutput(); err != nil {
		w.t.Fatalf("phn build in the library: %v\n%s", err, out)
	}
	if release != nil {
		release(k.dir)
	}
	k.git("init", "--quiet")
	k.git("add", "--all")
	k.git("commit", "--quiet", "--message", "acme/kit 1.0.0")
	return k
}

// git runs git in the library with no configuration but the test's.
func (k *kit) git(args ...string) string {
	k.w.t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "user.name=Acme", "-c", "user.email=kit@acme.example",
		"-c", "init.defaultBranch=main", "-c", "core.autocrlf=false", "-c", "commit.gpgsign=false",
	}, args...)...)
	cmd.Dir = k.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		k.w.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// archive is the library as a release ships it: the dist archive git makes
// of the commit with the library's own .gitattributes, served by an
// artifact repository. It returns what the archive holds beside the
// requirement.
func (k *kit) archive() (dependency, map[string]string) {
	k.w.t.Helper()
	dir := filepath.Join(k.dir+"-artifacts", "acme-kit-1.0.0.zip")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		k.w.t.Fatal(err)
	}
	k.git("archive", "--format=zip", "--output", dir, "HEAD")
	reader, err := zip.OpenReader(dir)
	if err != nil {
		k.w.t.Fatal(err)
	}
	defer reader.Close()
	shipped := map[string]string{}
	for _, f := range reader.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			k.w.t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			k.w.t.Fatal(err)
		}
		shipped[f.Name] = string(data)
	}
	return dependency{
		name:       "acme/kit",
		constraint: "1.0.0",
		repository: map[string]any{"type": "artifact", "url": filepath.Dir(dir)},
	}, shipped
}

// checkout is the library as a path repository: the project's vendor
// directory links to the working tree, tests/ and all.
func (k *kit) checkout() dependency {
	return dependency{name: "acme/kit", constraint: "*", repository: pathRepository(k.dir, "acme/kit", "1.0.0", true)}
}

// expectCommitted fails the test unless the checkout's .src/ is what the
// commit holds, which is the question the library's own gate asks of git
// (design/tooling.md §3.7): nothing changed or removed, nothing added.
func (k *kit) expectCommitted() {
	k.w.t.Helper()
	if status := k.git("status", "--porcelain", "--ignored", "--", ".src"); status != "" {
		k.w.t.Errorf("the library's .src/ is not as committed:\n%s", status)
	}
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
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// expectShipped fails the test unless the package installed in the project
// holds exactly the files its archive shipped, byte for byte: nothing
// compiled over them, nothing removed, no stamp beside them.
func (p *project) expectShipped(shipped map[string]string) {
	t := p.w.t
	t.Helper()
	got := snapshot(t, filepath.Join(p.dir, "vendor", "acme", "kit"))
	for _, path := range slices.Sorted(maps.Keys(got)) {
		if _, ok := shipped[path]; !ok {
			t.Errorf("vendor/acme/kit/%s is not of the archive", path)
		}
	}
	for _, path := range slices.Sorted(maps.Keys(shipped)) {
		switch text, ok := got[path]; {
		case !ok:
			t.Errorf("vendor/acme/kit/%s is gone", path)
		case text != shipped[path]:
			t.Errorf("vendor/acme/kit/%s is not as shipped:\n%s", path, text)
		}
	}
}

// greet runs the library in the project through Composer's autoloader and
// expects its greeting, from a class loaded out of .src/.
func (p *project) greet() {
	t := p.w.t
	t.Helper()
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php on PATH")
	}
	script := `<?php
require 'vendor/autoload.php';
$greeter = new Acme\Kit\Greeter();
echo $greeter->greet(new Acme\Kit\Box('Ada')), ' from ', basename(dirname((string) (new ReflectionClass($greeter))->getFileName())), "\n";
`
	writeFile(t, filepath.Join(p.dir, "greet.php"), []byte(script), 0o644)
	run := exec.Command(php, "greet.php")
	run.Dir = p.dir
	out, err := run.CombinedOutput()
	if want := "Hello, Ada from .src\n"; err != nil || string(out) != want {
		t.Errorf("php greet.php: %v\n%s\nwant\n%s", err, out, want)
	}
}

// phn runs the real phn in the project, as someone would by hand.
func (p *project) phn(args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(p.w.buildPhn(), args...)
	cmd.Dir = p.dir
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

// TestTheArchiveHoldsWhatAConsumerNeeds: the dist archive made with the
// library's .gitattributes lists the project file, the sources and the
// outputs, and no tests/. A habit of PHP libraries drops dot-directories
// from the archive, and with tests/ in it every Phaenon consumer would
// compile the library's tests: the library's gate looks at the archive, and
// the tests below install this one.
func TestTheArchiveHoldsWhatAConsumerNeeds(t *testing.T) {
	w := newWorld(t, "")
	_, shipped := w.newKit("kit", nil).archive()

	want := []string{".src/Box.php", ".src/Greeter.php", ".src/greetings.json", "composer.json", "phaenon.json", "src/Box.phn", "src/Greeter.phn", "src/greetings.json"}
	if got := slices.Sorted(maps.Keys(shipped)); !slices.Equal(got, want) {
		t.Errorf("the archive holds\n\t%v\nwant\n\t%v", got, want)
	}
}

// TestCommittedLibraryIsPlainPHP: a project that has never heard of Phaenon
// requires the library as any PHP library, from a release's archive and
// from a path repository. It gets no plugin and no binary, nothing is
// built, and the classes load from the committed .src/.
func TestCommittedLibraryIsPlainPHP(t *testing.T) {
	w := newWorld(t, "")
	k := w.newKit("kit", nil)
	release, shipped := k.archive()

	fromArchive := w.newProject("from-archive", project{withoutPlugin: true, dependencies: []dependency{release}})
	out := fromArchive.mustComposer("install")
	fromArchive.expectShipped(shipped)
	fromArchive.greet()

	fromCheckout := w.newProject("from-checkout", project{withoutPlugin: true, dependencies: []dependency{k.checkout()}})
	out += fromCheckout.mustComposer("install")
	k.expectCommitted()
	fromCheckout.greet()

	for _, p := range []*project{fromArchive, fromCheckout} {
		if got := p.installedPackages(); !slices.Equal(got, []string{"acme/kit"}) {
			t.Errorf("%s installed %v, want acme/kit alone", filepath.Base(p.dir), got)
		}
		expectAbsent(t, filepath.Join(p.dir, "vendor", "bin", "phn"))
		expectAbsent(t, filepath.Join(p.dir, "vendor", "phaenon"))
	}
	w.expectRuns()
	if strings.Contains(out, "phaenon") {
		t.Errorf("Composer spoke of Phaenon to a project of plain PHP:\n%s", out)
	}
}

// TestCommittedLibraryIsLeftAsInstalled: in a project with the plugin, a
// release of the library is nothing to build. With no release of phn pinned
// and no PHN_BINARY, an install that reached for a binary would fail: it
// passes, fetches nothing and runs nothing, and the package is byte for
// byte what its archive shipped, with no stamp. The real phn, run by hand,
// gives the plugin's answer.
func TestCommittedLibraryIsLeftAsInstalled(t *testing.T) {
	w := newWorld(t, "")
	release, shipped := w.newKit("kit", nil).archive()
	p := w.newProject("app", project{dependencies: []dependency{release}})

	out := p.mustComposer("install")

	w.expectRuns()
	expectAbsent(t, p.installed())
	if strings.Contains(out, "phaenon/phn:") {
		t.Errorf("the plugin spoke with nothing to build:\n%s", out)
	}
	p.expectShipped(shipped)
	p.greet()

	p.mustComposer("dump-autoload", "--classmap-authoritative")
	w.expectRuns()
	p.expectShipped(shipped)

	stdout, stderr, err := p.phn("build", "--vendor")
	if err != nil || stdout != "0 installed packages built, 0 up to date, 1 left as installed with their committed outputs\n" {
		t.Errorf("phn build --vendor: %v\n%s%s", err, stdout, stderr)
	}
	p.expectShipped(shipped)
}

// TestCommittedLibraryBesideWhatIsBuilt: only a package says its outputs
// are committed, about itself. The root project is built as before, and so
// is an installed library that ships its sources alone, while the library
// beside them stays as shipped.
func TestCommittedLibraryBesideWhatIsBuilt(t *testing.T) {
	w := newWorld(t, "")
	release, shipped := w.newKit("kit", nil).archive()
	own := w.newProject("own", project{phaenonJSON: true, dependencies: []dependency{release}, override: true})
	both := w.newProject("both", project{phaenonJSON: true, dependencies: []dependency{release}, libraries: []string{"acme/lib"}, override: true})

	own.mustComposer("install")
	both.mustComposer("install")

	w.expectRuns(
		run{own.dir, []string{"build"}},
		run{both.dir, []string{"build", "--vendor"}},
		run{both.dir, []string{"build"}},
	)
	own.expectShipped(shipped)
	both.expectShipped(shipped)
}

// TestCommittedLibraryAsACheckout: a path repository's checkout is the
// library's working tree, and it has tests/, a root the key does not name.
// The plugin builds that root with the real phn, as it builds any installed
// root, and leaves .src/ as committed, without a stamp. The project, itself
// written in Phaenon, reads the library from its sources: its code holds
// Greeter::key() to be bytes, which the committed output declares a string.
func TestCommittedLibraryAsACheckout(t *testing.T) {
	w := newWorld(t, "")
	k := w.newKit("kit", nil)
	p := w.newProject("app", project{phaenonJSON: true, dependencies: []dependency{k.checkout()}, binary: w.buildPhn()})
	writeFile(t, filepath.Join(p.dir, "src", "Greeter.phn"), []byte(`namespace App;

use Acme\Kit\Box;
use Acme\Kit\Greeter;

final class Keys
{
    key(Greeter $kit): bytes => $kit->key();

    greet(Greeter $kit, Box<string> $name): string => $kit->greet($name);
}
`), 0o644)
	// The library's developer built it, and its tests with it; the plugin
	// finds those outputs stale.
	writeFile(t, filepath.Join(k.dir, ".tests", "GreeterTest.php"), []byte("<?php\n"), 0o644)

	out := p.mustComposer("install")

	k.expectCommitted()
	expectAbsent(t, filepath.Join(k.dir, ".src", ".phn-build"))
	tests, err := os.ReadFile(filepath.Join(k.dir, ".tests", "GreeterTest.php"))
	if err != nil || !strings.Contains(string(tests), "final class GreeterTest") {
		t.Errorf("the root the library does not commit was not built (%v):\n%s", err, tests)
	}
	if _, err := os.Stat(filepath.Join(k.dir, ".tests", ".phn-build")); err != nil {
		t.Errorf("the root the library does not commit has no stamp: %v", err)
	}
	for _, want := range []string{"acme/kit: 1 files compiled, 1 written", "1 installed packages built, 0 up to date"} {
		if !strings.Contains(out, want) {
			t.Errorf("composer's output lacks %q:\n%s", want, out)
		}
	}
	// A finding of the types is P2xxx or P3xxx; the project states no PHP
	// target, which is a warning of its own.
	if strings.Contains(out, " P2") || strings.Contains(out, " P3") {
		t.Errorf("the project and the library's tests should check against the library's sources:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(p.dir, ".src", "Greeter.php")); err != nil {
		t.Errorf("the project was not built: %v", err)
	}
	p.greet()
}

// TestCommittedLibraryWithoutItsOutputs: a release that names a root under
// "committed" and ships no output for it fails the dump as a failed build
// does, with an error naming the package and the root, before a binary is
// fetched or run: PHN_BINARY names one, and it stays unused. The plugin
// does not build the root instead. The real phn, run by hand on the same
// install, fails with the same error.
func TestCommittedLibraryWithoutItsOutputs(t *testing.T) {
	const summary = "1 installed packages lack outputs they commit, and nothing was built"
	for name, c := range map[string]struct {
		release func(dir string) error
		want    string
	}{
		"no output directory": {
			release: func(dir string) error { return os.RemoveAll(filepath.Join(dir, ".src")) },
			want:    "acme/kit: its phaenon.json commits the outputs of the root src, but the package is installed without them: .src is missing",
		},
		"one output": {
			release: func(dir string) error { return os.Remove(filepath.Join(dir, ".src", "Greeter.php")) },
			want:    "acme/kit: its phaenon.json commits the outputs of the root src, but the package is installed without them: .src/Greeter.php is missing",
		},
		"every compiled output": {
			release: func(dir string) error {
				return errors.Join(os.Remove(filepath.Join(dir, ".src", "Greeter.php")), os.Remove(filepath.Join(dir, ".src", "Box.php")))
			},
			want: "acme/kit: its phaenon.json commits the outputs of the root src, but the package is installed without them: .src/Box.php is missing, and 1 more",
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, "")
			release, shipped := w.newKit("kit", func(dir string) {
				if err := c.release(dir); err != nil {
					t.Fatal(err)
				}
			}).archive()
			p := w.newProject("app", project{phaenonJSON: true, dependencies: []dependency{release}, libraries: []string{"acme/lib"}, override: true})

			out, err := p.composer("install")

			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("composer install: %v, want exit status 1\n%s", err, out)
			}
			if !strings.Contains(out, "phaenon/phn: "+c.want+"\n") || !strings.Contains(out, "phaenon/phn: "+summary) {
				t.Errorf("composer's output lacks the plugin's error:\n%s\nwant\n%s", out, c.want)
			}
			w.expectRuns()
			p.expectShipped(shipped)

			stdout, stderr, err := p.phn("build", "--vendor")
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || stdout != "" {
				t.Fatalf("phn build --vendor: %v, want exit status 1\n%s%s", err, stdout, stderr)
			}
			if !strings.HasPrefix(stderr, c.want+"\n"+summary) {
				t.Errorf("phn's error is not the plugin's:\n%s\nwant\n%s", stderr, c.want)
			}
			p.expectShipped(shipped)
			expectAbsent(t, filepath.Join(p.dir, "vendor", "acme", "lib", ".src"))
		})
	}
}
