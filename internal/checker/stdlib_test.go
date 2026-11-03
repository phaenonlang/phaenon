package checker_test

import (
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/stdlib"
)

// TestRuntimeStubs type-checks the stdlib written in Phaenon
// (stdlib/runtime/**/*.phn, stdlib/concurrent/*.phn): it is read as the
// compiler's own declaration of Bytes, the Phaenon\Debug classes and the
// concurrency scope, and compiled into the runtime and concurrent packages,
// so its bodies must satisfy the checker like any Phaenon source. The Debug integrations are checked against the embedded
// Monolog and Sentry declarations (stdlib.Suggested).
func TestRuntimeStubs(t *testing.T) {
	p := program.New()
	if err := p.LoadSuggested(); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range append(stdlib.Runtime(), stdlib.Concurrent()...) {
		if !strings.HasSuffix(s.Path, ".phn") {
			continue
		}
		f := p.File("<stub>/" + s.Path)
		if f == nil {
			t.Fatalf("%s not loaded", s.Path)
		}
		f.Checked = true
		n++
	}
	if n == 0 {
		t.Fatal("no .phn runtime stubs")
	}
	c := checker.New(p, checker.Options{})
	for _, d := range c.Check() {
		if d.Severity == checker.Error {
			t.Errorf("%s:%d: %s: %s", d.File, d.Line, d.Code, d.Message)
		}
	}
}
