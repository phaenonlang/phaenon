package main

import (
	"os"
	"testing"
)

// TestFresh fails when the embedded stubs or the version table differ from
// what the php-src clone's release branches give (PHAENON_PHP_SRC; skipped
// without it). constants.php is left out: its values are the local php's.
func TestFresh(t *testing.T) {
	src := os.Getenv("PHAENON_PHP_SRC")
	if src == "" {
		t.Skip("PHAENON_PHP_SRC not set")
	}
	g := &generator{phpSrc: src, remote: "origin", dir: "..", check: true}
	if err := g.run(""); err != nil {
		t.Fatal(err)
	}
	if g.stale > 0 {
		t.Errorf("%d generated file(s) are stale: run go generate ./stdlib", g.stale)
	}
}
