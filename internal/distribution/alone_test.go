package distribution

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The compiled output depends on nothing but PHP (design/roadmap.md D24),
// so the plugin requires no package on the project's behalf
// (design/tooling.md T16): phaenon/debug is suggested, never installed.

// TestInstallsAlone: phaenon/phn requires nothing but PHP and Composer's
// plugin API, and a project that requires it gets it and nothing else.
func TestInstallsAlone(t *testing.T) {
	w := newWorld(t, "")
	var manifest struct {
		Require map[string]string `json:"require"`
	}
	data, err := os.ReadFile(filepath.Join(w.plugin, "composer.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for name := range manifest.Require {
		if name != "php" && name != "composer-plugin-api" && !strings.HasPrefix(name, "ext-") {
			t.Errorf("composer.json requires the package %s", name)
		}
	}
	p := w.newProject("app", project{})

	p.mustComposer("install")

	if got := p.installedPackages(); !slices.Equal(got, []string{"phaenon/phn"}) {
		t.Errorf("installed %v, want phaenon/phn alone", got)
	}
}

// TestCompiledOutputRunsAlone builds a project with the real phn: the output
// of the lowerings that once called phaenon/runtime (`is` and `as` on a list
// and a tuple, `as` on bytes, a generic class) loads through Composer's
// autoloader and runs with no phaenon/* package installed but the plugin,
// and names nothing under Phaenon\.
func TestCompiledOutputRunsAlone(t *testing.T) {
	w := newWorld(t, "")
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php on PATH")
	}
	phn := filepath.Join(w.base, "phn")
	build := exec.Command("go", "build", "-o", phn, "./cmd/phn")
	build.Dir = repositoryRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/phn: %v\n%s", err, out)
	}
	p := w.newProject("app", project{phaenonJSON: true, binary: phn})
	writeFile(t, filepath.Join(p.dir, "src", "Greeter.phn"), []byte(`namespace App;

final class Greeter
{
    fun greet(mixed $names, bytes $greeting): string {
        $list = $names as list;
        $first = $list[0] ?? null;
        $who = $first as? string;
        $text = $greeting as string;

        return $text . ', ' . ($who ?? 'nobody');
    }

    fun pair(mixed $p): string => $p is (int, string) ? $p[1] . ' #' . $p[0] : 'no pair';
}
`), 0o644)
	writeFile(t, filepath.Join(p.dir, "src", "Box.phn"), []byte(`namespace App;

final class Box<T>
{
    fun __construct(private T $value) {}

    fun get(): T => $this->value;
}
`), 0o644)

	p.mustComposer("install")

	if got := p.installedPackages(); !slices.Equal(got, []string{"phaenon/phn"}) {
		t.Errorf("installed %v, want phaenon/phn alone", got)
	}
	compiled := 0
	filepath.WalkDir(filepath.Join(p.dir, ".src"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		compiled++
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), `Phaenon\`) {
			t.Errorf("%s names Phaenon\\:\n%s", path, data)
		}
		return nil
	})
	if compiled != 2 {
		t.Fatalf("the build wrote %d files under .src, want 2", compiled)
	}
	script := `<?php
require 'vendor/autoload.php';
$g = new App\Greeter();
echo $g->greet(['Ada', 'Grace'], 'Hello'), "\n";
echo $g->greet([7], 'Hi'), "\n";
echo $g->pair([1, 'one']), ' / ', $g->pair([1, 2]), "\n";
echo (new App\Box(42))->get(), "\n";
foreach ([fn () => $g->greet(['k' => 'v'], 'Hello'), fn () => $g->greet([], "\xFF")] as $call) {
    try {
        $call();
    } catch (TypeError | ValueError $e) {
        echo $e::class, ': ', $e->getMessage(), "\n";
    }
}
`
	writeFile(t, filepath.Join(p.dir, "run.php"), []byte(script), 0o644)
	run := exec.Command(php, "run.php")
	run.Dir = p.dir
	out, err := run.CombinedOutput()
	want := `Hello, Ada
Hi, nobody
one #1 / no pair
42
TypeError: expected list, got array
ValueError: not valid UTF-8
`
	if err != nil || string(out) != want {
		t.Errorf("php run.php: %v\n%s\nwant\n%s", err, out, want)
	}
}

// installedPackages returns the names of the packages Composer installed in
// the project, from vendor/composer/installed.json, sorted.
func (p *project) installedPackages() []string {
	p.w.t.Helper()
	data, err := os.ReadFile(filepath.Join(p.dir, "vendor", "composer", "installed.json"))
	if err != nil {
		p.w.t.Fatal(err)
	}
	var installed struct {
		Packages []struct {
			Name string `json:"name"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &installed); err != nil {
		p.w.t.Fatal(err)
	}
	var names []string
	for _, pkg := range installed.Packages {
		names = append(names, pkg.Name)
	}
	slices.Sort(names)
	return names
}
