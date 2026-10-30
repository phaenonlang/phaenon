package emit

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestRuntimePackageFresh compares the compiled stdlib with what the
// phaenon/runtime package ships in the sibling checkout (../runtime/src):
// the .phn sources here are the one source, `phn stdlib --out
// ../runtime/src` materialises them. Skipped when the checkout is absent.
func TestRuntimePackageFresh(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "runtime", "src")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no ../runtime checkout")
	}
	compiled, err := Stdlib()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range compiled {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s is stale or missing in the runtime package: run `phn stdlib --out ../runtime/src`", name)
		}
	}
}
