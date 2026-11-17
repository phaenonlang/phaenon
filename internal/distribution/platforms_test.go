package distribution

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestBinariesOfSeveralPlatforms: the plugin installs the binary under its
// release asset's name, and vendor/bin/phn runs the one for the platform it
// runs on. Another platform's binary beside it, as a vendor/ shared with a
// container holds, is left alone; when the running platform's is missing,
// the launcher puts it back as at the first run, from Composer's cache here,
// checked against the same pinned checksum.
func TestBinariesOfSeveralPlatforms(t *testing.T) {
	w := newWorld(t, "")
	m := newMirror(t)
	binary := w.fakeBytes()
	m.serve("0.1.0", assetFile(), binary)
	w.pin("0.1.0", sha256Hex(binary))

	p := w.newProject("app", project{phaenonJSON: true, mirror: m})
	p.mustComposer("install")
	expectFile(t, p.installed(), binary, 0o755)

	// Another platform's binary, which this one cannot run, beside it.
	other := filepath.Join(filepath.Dir(p.installed()), "phn-other-platform")
	if err := os.WriteFile(other, []byte("not this platform's"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p.installed()); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, status := p.launch(nil, "version")
	if stdout != "phn fake\n" || status != 0 {
		t.Errorf("vendor/bin/phn version: %q, status %d, stderr %q; want the platform's binary put back, then its answer", stdout, status, stderr)
	}
	expectFile(t, p.installed(), binary, 0o755)
	expectServed(t, m, 1) // the install's download; the launcher took the cached copy
	if data, err := os.ReadFile(other); err != nil || string(data) != "not this platform's" {
		t.Errorf("the other platform's binary was touched: %v %q", err, data)
	}
}

// TestLauncherWithoutTheBinaryDirectory: a runtime image that leaves the
// compiler out removes vendor/phaenon/phn/bin, or cannot write it. Run there,
// vendor/bin/phn fetches the binary again where the directory can be written,
// and where it cannot it says so and what to do, with no PHP warning.
func TestLauncherWithoutTheBinaryDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a directory's mode does not keep this user from writing it")
	}
	w := newWorld(t, "")
	m := newMirror(t)
	binary := w.fakeBytes()
	m.serve("0.1.0", assetFile(), binary)
	w.pin("0.1.0", sha256Hex(binary))
	p := w.newProject("app", project{phaenonJSON: true, mirror: m})
	p.mustComposer("install")
	bin := filepath.Dir(p.installed())
	pkg := filepath.Dir(bin)
	t.Cleanup(func() {
		os.Chmod(pkg, 0o755)
		os.Chmod(bin, 0o755)
	})

	if err := os.RemoveAll(bin); err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, status := p.launch(nil, "version"); stdout != "phn fake\n" || status != 0 {
		t.Errorf("vendor/bin/phn version, the directory removed: %q, status %d, stderr %q; want the binary fetched again", stdout, status, stderr)
	}
	expectFile(t, p.installed(), binary, 0o755)

	refused := func(what string) {
		t.Helper()
		stdout, stderr, status := p.launch(nil, "version")
		want := filepath.Base(p.installed()) + ", the phn binary for this platform, is not installed, and " + bin + " cannot be written"
		if status != 127 || stdout != "" || !strings.Contains(stderr, want) || !strings.Contains(stderr, "set PHN_BINARY") ||
			strings.Contains(stdout+stderr, "Warning") {
			t.Errorf("vendor/bin/phn version, %s: %q, status %d, stderr %q; want status 127 and %q", what, stdout, status, stderr, want)
		}
	}
	if err := os.RemoveAll(bin); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(pkg, 0o555); err != nil {
		t.Fatal(err)
	}
	refused("the directory removed under one that cannot be written")
	if err := os.Chmod(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(bin, 0o555); err != nil {
		t.Fatal(err)
	}
	refused("the directory read-only")
}
