package distribution

import (
	"path/filepath"
	"strings"
	"testing"
)

// A checkout of the compiler, installed through a symlinked path repository,
// has the developer's `go build -o bin/phn ./cmd/phn` in the very bin/ the
// pinned binary would be installed into. These tests pin a release and serve
// it, so that a plugin or launcher that wrote into the checkout would be
// caught downloading.

// TestCheckoutBuildIsUsed: the plugin and the launcher run the checkout's own
// build as it is, and leave it as it is.
func TestCheckoutBuildIsUsed(t *testing.T) {
	w := newWorld(t, "")
	m := releasedOnMirror(t, w)
	w.checkout(true)
	p := w.newProject("app", project{phaenonJSON: true, mirror: m, symlinkPlugin: true})

	p.mustComposer("install")
	w.expectRuns(run{p.dir, []string{"build"}})

	stdout, stderr, status := p.launch(nil, "version")
	if stdout != "phn fake\n" || status != 0 {
		t.Errorf("vendor/bin/phn version: %q, status %d, stderr %q; want the checkout's build", stdout, status, stderr)
	}
	expectFile(t, filepath.Join(w.plugin, "bin", "phn"), w.fakeBytes(), 0o755)
	expectServed(t, m, 0)
}

// TestCheckoutWithoutBuild: a checkout without a build fails the install and
// the launcher with a message naming both ways out, and nothing is downloaded
// or written into it.
func TestCheckoutWithoutBuild(t *testing.T) {
	w := newWorld(t, "")
	m := releasedOnMirror(t, w)
	w.checkout(false)
	p := w.newProject("app", project{phaenonJSON: true, mirror: m, symlinkPlugin: true})

	out, err := p.composer("install")
	if err == nil {
		t.Errorf("composer install passed in a checkout without a build:\n%s", out)
	}
	expectWaysOut(t, "composer's output", out)
	w.expectRuns()

	_, stderr, status := p.launch(nil, "version")
	if status != 127 {
		t.Errorf("vendor/bin/phn version: status %d, want 127", status)
	}
	expectWaysOut(t, "the launcher's message", stderr)

	expectAbsent(t, filepath.Join(w.plugin, "bin"))
	expectServed(t, m, 0)
}

// releasedOnMirror pins a release in the package copy and serves it, a
// binary other than the fake.
func releasedOnMirror(t *testing.T, w *world) *mirror {
	t.Helper()
	m := newMirror(t)
	released := []byte("#!/bin/sh\necho released\n")
	m.serve("0.1.0", assetFile(), released)
	w.pin("0.1.0", sha256Hex(released))
	return m
}

func expectWaysOut(t *testing.T, what, text string) {
	t.Helper()
	for _, want := range []string{"set PHN_BINARY", "go build -o bin/phn ./cmd/phn"} {
		if !strings.Contains(text, want) {
			t.Errorf("%s lacks %q:\n%s", what, want, text)
		}
	}
}
