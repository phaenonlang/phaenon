package distribution

import "testing"

// TestWithoutDevMode: Composer without dev mode (install --no-dev,
// dump-autoload --no-dev) loads nothing autoload-dev maps, and the plugin
// asks phn for the project's roots but the development ones, phn saying
// which it left out. The installed packages are built as in dev mode: a
// dependency's autoload-dev is never loaded, and its findings are warnings.
// A dump-autoload without either switch takes the last install's mode, so
// the second dump runs without dev mode too, and --dev builds every root.
func TestWithoutDevMode(t *testing.T) {
	w := newWorld(t, "")
	p := w.newProject("app", project{phaenonJSON: true, libraries: []string{"acme/lib"}, override: true})

	p.mustComposer("install", "--no-dev")
	p.mustComposer("dump-autoload")
	p.mustComposer("dump-autoload", "--dev")

	w.expectRuns(
		run{p.dir, []string{"build", "--vendor"}},
		run{p.dir, []string{"build", "--no-dev"}},
		run{p.dir, []string{"build", "--vendor"}},
		run{p.dir, []string{"build", "--no-dev"}},
		run{p.dir, []string{"build", "--vendor"}},
		run{p.dir, []string{"build"}},
	)
}
