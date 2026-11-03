package main

import (
	"strings"
	"testing"
)

// TestMapSource pins the path a compiled file's line map names: the source
// relative to the output's directory, forward slashes.
func TestMapSource(t *testing.T) {
	cases := []struct{ src, dst, want string }{
		{"src/Domain/Foo.phn", ".src/Domain/Foo.php", "../../src/Domain/Foo.phn"},
		{"src/Foo.phn", "src/Foo.php", "Foo.phn"},
		{"lib/Foo.phn", "out/lib/Foo.php", "../../lib/Foo.phn"},
	}
	for _, c := range cases {
		if got := mapSource(c.src, c.dst); got != c.want {
			t.Errorf("mapSource(%q, %q) = %q, want %q", c.src, c.dst, got, c.want)
		}
	}
}

// TestBuildCheckEmitOnError: --check with --emit-on-error asks whether the
// outputs are what `phn build --emit-on-error` writes, so a file with errors
// whose output is current passes, its findings still printed; without the
// flag the same file fails, and a stale output fails either way.
func TestBuildCheckEmitOnError(t *testing.T) {
	bin := phnBinary(t)
	root := formatTree(t)
	writeTree(t, root, map[string]string{"src/Good.phn": "namespace App;\n\nfun good(): int => 1;\n"})

	if _, _, err := run(t, root, bin, "build", "--check", "--emit-on-error"); err == nil {
		t.Error("--check passed before anything was built")
	}
	if _, _, err := run(t, root, bin, "build", "--emit-on-error"); err == nil {
		t.Error("build --emit-on-error should still fail on the errors")
	}
	stdout, stderr, err := run(t, root, bin, "build", "--check", "--emit-on-error")
	if err != nil || !strings.Contains(stdout, "2 files up to date, 1 of them built with their errors") || !strings.Contains(stderr, "error P2202") {
		t.Errorf("--check --emit-on-error on current outputs (%v):\n%s%s", err, stdout, stderr)
	}
	if _, _, err := run(t, root, bin, "build", "--check"); err == nil {
		t.Error("--check without --emit-on-error passed a file with errors")
	}
	writeTree(t, root, map[string]string{".src/Bad.php": "<?php\n"})
	if stdout, _, err := run(t, root, bin, "build", "--check", "--emit-on-error"); err == nil {
		t.Errorf("a stale output passed:\n%s", stdout)
	}
}
