package distribution

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildsInstalledPackagesThenTheProject: at the autoload dump, the plugin
// builds the installed packages and then the root project, which has a
// phaenon.json, both in the root's directory, and passes phn's streams on.
func TestBuildsInstalledPackagesThenTheProject(t *testing.T) {
	w := newWorld(t, "")
	p := w.newProject("app", project{phaenonJSON: true, libraries: []string{"acme/lib"}, override: true})

	out := p.mustComposer("install")

	w.expectRuns(
		run{p.dir, []string{"build", "--vendor"}},
		run{p.dir, []string{"build"}},
	)
	for _, want := range []string{
		"> phn build --vendor", "phn build --vendor: done", "phn build --vendor: warning",
		"> phn build", "phn build: done", "phn build: warning",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("composer's output lacks %q:\n%s", want, out)
		}
	}
}

// TestAutoloadMapsAreCurrentWhenPhnRuns: phn reads vendor code through
// vendor/composer's autoload maps, which Composer writes only after the
// pre-autoload-dump event. The plugin writes them first, for the packages
// installed now: on a fresh install, where there are none yet, and after an
// update, where they would be stale.
func TestAutoloadMapsAreCurrentWhenPhnRuns(t *testing.T) {
	w := newWorld(t, "")
	p := w.newProject("app", project{libraries: []string{"acme/lib"}, override: true})

	p.mustComposer("install")
	if maps := w.mapsAtRun(1); !strings.Contains(maps, `'Acme\\Lib\\'`) {
		t.Errorf("on a fresh install, phn found autoload_psr4.php without the installed package:\n%s", maps)
	}

	p.libraries = append(p.libraries, "acme/extra")
	p.write()
	p.mustComposer("update")
	if maps := w.mapsAtRun(2); !strings.Contains(maps, `'Acme\\Extra\\'`) {
		t.Errorf("after an update, phn found autoload_psr4.php without the added package:\n%s", maps)
	}
	w.expectRuns(
		run{p.dir, []string{"build", "--vendor"}},
		run{p.dir, []string{"build", "--vendor"}},
	)
}

// TestNoProjectBuildWithoutPhaenonJSON: a root project without a phaenon.json
// has no Phaenon code of its own; only the installed packages are built.
func TestNoProjectBuildWithoutPhaenonJSON(t *testing.T) {
	w := newWorld(t, "")
	p := w.newProject("app", project{override: true})

	p.mustComposer("install")

	w.expectRuns(run{p.dir, []string{"build", "--vendor"}})
}

// TestProjectBuildTurnedOff: extra.phaenon.build false leaves the root
// project's build to its own workflow (T4).
func TestProjectBuildTurnedOff(t *testing.T) {
	w := newWorld(t, "")
	p := w.newProject("app", project{
		phaenonJSON: true,
		extra:       map[string]any{"phaenon": map[string]any{"build": false}},
		override:    true,
	})

	p.mustComposer("install")

	w.expectRuns(run{p.dir, []string{"build", "--vendor"}})
}

// TestFailingBuildFailsTheCommand: a root project that does not build fails
// `composer install` with phn's exit status and a message naming the
// command, after phn's own diagnostics.
func TestFailingBuildFailsTheCommand(t *testing.T) {
	w := newWorld(t, "build")
	p := w.newProject("app", project{phaenonJSON: true, override: true})

	out, err := p.composer("install")

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("composer install: %v, want exit status 2 (phn's)\n%s", err, out)
	}
	finding := strings.Index(out, "error P2103: a finding of the fake")
	message := strings.Index(out, "phaenon/phn: `phn build` exited with status 2 in "+p.dir)
	if finding < 0 || message < 0 || message < finding {
		t.Errorf("want phn's finding, then the plugin's message naming `phn build`:\n%s", out)
	}
}

// TestBuildsAtTheDumpAfterNoAutoloader is the Dockerfile's order (§7.3): an
// install without the autoloader builds nothing, and the dump-autoload that
// follows builds before Composer scans, so that an authoritative class map
// holds the classes phn wrote.
func TestBuildsAtTheDumpAfterNoAutoloader(t *testing.T) {
	w := newWorld(t, "")
	p := w.newProject("app", project{phaenonJSON: true, override: true})

	p.mustComposer("install", "--no-dev", "--no-scripts", "--no-autoloader")
	w.expectRuns()

	p.mustComposer("dump-autoload", "--classmap-authoritative", "--no-dev")
	w.expectRuns(
		run{p.dir, []string{"build", "--vendor"}},
		run{p.dir, []string{"build"}},
	)
	classmap, err := os.ReadFile(filepath.Join(p.dir, "vendor", "composer", "autoload_classmap.php"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(classmap), `'App\\Greeter'`) {
		t.Errorf("the authoritative class map lacks the class phn wrote:\n%s", classmap)
	}
}

// TestNoReleasedBinary: with no release pinned, as in the repository until
// the first one, the install fails and the launcher refuses, both naming
// PHN_BINARY.
func TestNoReleasedBinary(t *testing.T) {
	w := newWorld(t, "")
	p := w.newProject("app", project{})

	out, err := p.composer("install")
	if err == nil || !strings.Contains(out, "pins no released phn binary") || !strings.Contains(out, "set PHN_BINARY") {
		t.Errorf("composer install: %v, want a failure naming PHN_BINARY\n%s", err, out)
	}
	w.expectRuns()

	_, stderr, status := p.launch(nil, "version")
	if status != 127 || !strings.Contains(stderr, "set PHN_BINARY") {
		t.Errorf("vendor/bin/phn: status %d, %q; want 127 and a message naming PHN_BINARY", status, stderr)
	}
}
