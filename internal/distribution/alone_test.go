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

	"github.com/phaenonlang/phaenon/internal/emit"
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

// TestCompiledOutputRunsAlone builds a project with the real phn: the tests
// that once called phaenon/runtime (of a list and a tuple, the UTF-8 of
// bytes), written as PHP's own since roadmap D26, and a generic class name
// nothing under Phaenon\ (emit.PhaenonReferences) and run through
// Composer's autoloader with the plugin installed alone, which
// TestInstallsAlone holds.
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
    greet(mixed $names, bytes $greeting): string {
        $list = is_array($names) && array_is_list($names) ? $names : throw new \TypeError('expected list, got ' . get_debug_type($names));
        $first = $list[0] ?? null;
        $who = is_string($first) ? $first : null;
        $text = mb_check_encoding($greeting, 'UTF-8') ? $greeting : throw new \ValueError('not valid UTF-8');

        return $text . ', ' . ($who ?? 'nobody');
    }

    pair(mixed $p): string => is_array($p) && array_is_list($p) && count($p) === 2 && is_int($p[0]) && is_string($p[1]) ? $p[1] . ' #' . $p[0] : 'no pair';
}
`), 0o644)
	writeFile(t, filepath.Join(p.dir, "src", "Box.phn"), []byte(`namespace App;

final class Box<T>
{
    __construct(private T $value) {}

    get(): T => $this->value;
}
`), 0o644)

	p.mustComposer("install")

	compiled := 0
	filepath.WalkDir(filepath.Join(p.dir, ".src"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		compiled++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if refs, _ := emit.PhaenonReferences(data); len(refs) > 0 {
			t.Errorf("%s depends on more than PHP:\n\t%s", path, strings.Join(refs, "\n\t"))
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

// TestRefusesTheRuntime: phaenon/debug declares the classes phaenon/runtime
// did, Phaenon\Debug\…, so a project never holds both. The plugin conflicts
// with the old name below 0.4, its every release: a project that still
// requires phaenon/runtime is refused, alone or beside phaenon/debug.
// phaenon/debug replaces phaenon/runtime at its own version, 0.4 on, and
// Composer matches a conflict against a replacement too, so the bound is
// what lets the plugin and phaenon/debug install together.
func TestRefusesTheRuntime(t *testing.T) {
	w := newWorld(t, "")
	classes := map[string]any{"autoload": map[string]any{"psr-4": map[string]string{`Phaenon\`: "src/"}}}
	runtime := plainPackage{"phaenon/runtime", "0.3.0", classes}
	debug := plainPackage{"phaenon/debug", "0.4.0", merge(classes, map[string]any{"replace": map[string]string{"phaenon/runtime": "self.version"}})}

	for name, packages := range map[string][]plainPackage{"alone": {runtime}, "beside": {runtime, debug}} {
		p := w.newProject(name, project{packages: packages})
		out, err := p.composer("install")
		// Beside phaenon/debug, Composer may name its replacement instead.
		if err == nil || !strings.Contains(out, "conflicts with phaenon/runtime") && !strings.Contains(out, "replaces phaenon/runtime and thus cannot coexist") {
			t.Errorf("%s: composer install with phaenon/runtime 0.3.0: %v, want a refusal\n%s", name, err, out)
		}
	}

	renamed := w.newProject("renamed", project{packages: []plainPackage{debug}})
	renamed.mustComposer("install")
	if got := renamed.installedPackages(); !slices.Equal(got, []string{"phaenon/debug", "phaenon/phn"}) {
		t.Errorf("installed %v, want phaenon/debug and phaenon/phn", got)
	}
}
