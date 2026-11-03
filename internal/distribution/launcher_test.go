package distribution

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// TestLauncherPassesThrough: vendor/bin/phn runs phn with its arguments,
// standard streams and exit status, by replacing its process and, where
// pcntl_exec is not available, as a child process.
func TestLauncherPassesThrough(t *testing.T) {
	w := newWorld(t, "")
	p := w.newProject("app", project{override: true})
	p.mustComposer("install")
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php on PATH")
	}

	for _, c := range []struct {
		name string
		php  []string
	}{
		{"pcntl_exec", nil},
		{"proc_open", []string{php, "-d", "disable_functions=pcntl_exec"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			args := []string{"exit", "3", "two words", "--flag=x"}
			stdout, stderr, status := p.launch(c.php, args...)
			if stdout != "out\n" || stderr != "err\n" || status != 3 {
				t.Errorf("vendor/bin/phn: stdout %q, stderr %q, status %d; want \"out\\n\", \"err\\n\", 3", stdout, stderr, status)
			}
			runs := w.runs()
			last := runs[len(runs)-1]
			if fmt.Sprint(last) != fmt.Sprint(run{p.dir, args}) {
				t.Errorf("phn ran as %v, want %v", last, run{p.dir, args})
			}
		})
	}
}

// TestLauncherFetchesAfterNoPlugins: under --no-plugins nothing is built or
// installed, and vendor/bin/phn fetches the binary on first use as the plugin
// would: the pinned checksum, the root composer.json's binary-url, the
// project's credentials, Composer's cache.
func TestLauncherFetchesAfterNoPlugins(t *testing.T) {
	w := newWorld(t, "")
	m := newMirror(t)
	binary := w.fakeBytes()
	m.serve("0.1.0", assetFile(), binary)
	w.pin("0.1.0", sha256Hex(binary))

	p := w.newProject("app", project{phaenonJSON: true, mirror: m})
	p.mustComposer("install", "--no-plugins")
	w.expectRuns()
	expectAbsent(t, p.installed())

	stdout, stderr, status := p.launch(nil, "version")
	if stdout != "phn fake\n" || status != 0 || !strings.Contains(stderr, "Downloading phn 0.1.0") {
		t.Errorf("vendor/bin/phn version: %q, status %d, stderr %q; want a download, then the binary's answer", stdout, status, stderr)
	}
	expectFile(t, p.installed(), binary, 0o755)
	expectServed(t, m, 1)

	stdout, stderr, status = p.launch(nil, "version")
	if stdout != "phn fake\n" || status != 0 || stderr != "" {
		t.Errorf("vendor/bin/phn version, again: %q, status %d, stderr %q; want the installed binary, quietly", stdout, status, stderr)
	}
	expectServed(t, m, 1)
}
