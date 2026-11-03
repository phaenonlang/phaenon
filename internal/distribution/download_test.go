package distribution

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDownloadInstallsAndCaches: without PHN_BINARY the plugin downloads the
// pinned binary from the root's binary-url with the project's credentials,
// verifies it, keeps it in Composer's cache and installs it into the package;
// a second project takes it from the cache, and a corrupt cache entry is
// downloaded again.
func TestDownloadInstallsAndCaches(t *testing.T) {
	w := newWorld(t, "")
	m := newMirror(t)
	binary := w.fakeBytes()
	m.serve("0.1.0", assetFile(), binary)
	w.pin("0.1.0", sha256Hex(binary))
	cached := filepath.Join(w.cache, "phaenon-phn", "0.1.0", assetFile())

	first := w.newProject("first", project{mirror: m})
	first.mustComposer("install")
	expectFile(t, first.installed(), binary, 0o755)
	expectFile(t, cached, binary, 0)
	expectServed(t, m, 1)
	w.expectRuns(run{first.dir, []string{"build", "--vendor"}})

	stdout, stderr, status := first.launch(nil, "version")
	if stdout != "phn fake\n" || status != 0 {
		t.Errorf("vendor/bin/phn version: %q, status %d, stderr %q; want the installed binary's answer", stdout, status, stderr)
	}

	second := w.newProject("second", project{mirror: m})
	second.mustComposer("install")
	expectFile(t, second.installed(), binary, 0o755)
	expectServed(t, m, 1)

	if err := os.WriteFile(cached, []byte("not phn"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The base-URL form of binary-url: the file name is appended.
	third := w.newProject("third", project{mirror: m, baseURL: true})
	out := third.mustComposer("install")
	if !strings.Contains(out, "does not match its checksum: downloading it again") {
		t.Errorf("composer's output does not say the cache entry was replaced:\n%s", out)
	}
	expectFile(t, third.installed(), binary, 0o755)
	expectFile(t, cached, binary, 0)
	expectServed(t, m, 2)
}

// TestDownloadChecksumMismatch: a downloaded binary that is not the pinned one
// fails the install with both checksums, and is neither installed nor cached.
func TestDownloadChecksumMismatch(t *testing.T) {
	w := newWorld(t, "")
	m := newMirror(t)
	binary := w.fakeBytes()
	m.serve("0.1.0", assetFile(), binary)
	pinned := strings.Repeat("ab", 32)
	w.pin("0.1.0", pinned)

	p := w.newProject("app", project{mirror: m})
	out, err := p.composer("install")

	if err == nil {
		t.Fatalf("composer install passed with a binary that is not the pinned one:\n%s", out)
	}
	for _, want := range []string{"has SHA-256 " + sha256Hex(binary), "phaenon/phn pins " + pinned} {
		if !strings.Contains(out, want) {
			t.Errorf("composer's output lacks %q:\n%s", want, out)
		}
	}
	expectAbsent(t, p.installed())
	expectAbsent(t, filepath.Join(w.cache, "phaenon-phn", "0.1.0", assetFile()))
	w.expectRuns()
}

func expectServed(t *testing.T, m *mirror, want int) {
	t.Helper()
	downloads, leaked := m.served()
	if downloads != want {
		t.Errorf("the mirror served %d downloads, want %d", downloads, want)
	}
	if leaked {
		t.Error("the storage host the registry redirects to received the registry's credentials")
	}
}

// expectFile fails the test unless path holds data, with the permission bits
// mode when mode is not 0.
func expectFile(t *testing.T, path string, data []byte, mode fs.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("%s: %v", path, err)
		return
	}
	if !bytes.Equal(got, data) {
		t.Errorf("%s does not hold the binary", path)
	}
	if mode == 0 {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Errorf("%s: %v", path, err)
	} else if info.Mode().Perm() != mode {
		t.Errorf("%s: mode %v, want %v", path, info.Mode().Perm(), mode)
	}
}

func expectAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s exists (%v)", path, err)
	}
}
