package emit

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestStdlibPackagePaths: the compiled stdlib keeps the layout of its
// sources below stdlib/runtime, which is the runtime package's src/ and its
// PSR-4 namespace (Phaenon\Debug\Monolog\LineMapProcessor in
// Debug/Monolog/LineMapProcessor.php).
func TestStdlibPackagePaths(t *testing.T) {
	compiled, err := Stdlib()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Bytes.php", "Debug/LineMap.php", "Debug/Monolog/LineMapProcessor.php", "Debug/Sentry/LineMapIntegration.php", "Debug/ThrowableRewriter.php", "Map.php", "Seq.php", "Str.php"}
	var got []string
	for name := range compiled {
		got = append(got, name)
	}
	sort.Strings(got)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Stdlib() compiles\n%v\nwant\n%v", got, want)
	}
	for stub, want := range map[string]string{
		"runtime/Seq.phn":           "Seq.php",
		"runtime/Type.php":          "Type.php",
		"runtime/Debug/LineMap.phn": "Debug/LineMap.php",
		"concurrent/Scope.phn":      "Scope.php",
		"concurrent/amp/Amp.php":    "amp/Amp.php",
	} {
		if got := packagePath(stub); got != want {
			t.Errorf("packagePath(%q) = %q, want %q", stub, got, want)
		}
	}
}

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

// TestConcurrentPackageFresh compares the compiled Scope.php with the copy
// the phaenon/concurrent package ships in the sibling checkout:
// `phn stdlib --concurrent-out ../concurrent/src` materialises it. Skipped
// when the checkout is absent.
func TestConcurrentPackageFresh(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "concurrent", "src")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no ../concurrent checkout")
	}
	compiled, err := Concurrent()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range compiled {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s is stale or missing in the concurrent package: run `phn stdlib --concurrent-out ../concurrent/src`", name)
		}
	}
}
