package distribution

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectFileWithoutRoots: a project file that names no roots, as
// `phn migrate --php` writes one, has nothing for phn build to build without
// paths, so the plugin builds the installed packages and skips the project,
// saying so, where phn build would fail the dump.
func TestProjectFileWithoutRoots(t *testing.T) {
	w := newWorld(t, "")
	p := w.newProject("app", project{phaenonJSON: true, libraries: []string{"acme/lib"}, override: true})
	writeJSON(t, filepath.Join(p.dir, "phaenon.json"), map[string]any{"php": "8.5"})

	out := p.mustComposer("install")
	w.expectRuns(run{p.dir, []string{"build", "--vendor"}})
	const said = `phaenon/phn: phaenon.json names no roots, so the project is not built: ` +
		"`vendor/bin/phn build src` builds a path until it names them"
	if !strings.Contains(out, said) {
		t.Errorf("composer install:\n%s\nwant %q", out, said)
	}
}
