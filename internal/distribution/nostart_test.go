package distribution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildThatCannotStart: where phn is there and cannot be started, a
// binary of no platform this one runs, the install fails with the plugin's
// message, and no hint that `vendor/bin/phn check` shows findings: there are
// none to show.
func TestBuildThatCannotStart(t *testing.T) {
	w := newWorld(t, "")
	broken := filepath.Join(t.TempDir(), "phn")
	if err := os.WriteFile(broken, append([]byte("\x7fELF\x02\x01\x01"), make([]byte, 57)...), 0o755); err != nil {
		t.Fatal(err)
	}
	p := w.newProject("app", project{phaenonJSON: true, binary: broken})

	out, err := p.composer("install")
	if err == nil {
		t.Fatalf("composer install should fail:\n%s", out)
	}
	if strings.Contains(out, "shows the findings") {
		t.Errorf("a hint about findings where phn did not run:\n%s", out)
	}
	if !strings.Contains(out, "phaenon/phn: `phn build` exited with status") {
		t.Errorf("the plugin's message:\n%s", out)
	}
}
