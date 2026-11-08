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
// sources below stdlib/runtime, which is the debug package's src/ and its
// PSR-4 namespace (Phaenon\Debug\Monolog\LineMapProcessor in
// Debug/Monolog/LineMapProcessor.php).
func TestStdlibPackagePaths(t *testing.T) {
	compiled, err := Stdlib()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Debug/LineMap.php", "Debug/Monolog/LineMapProcessor.php", "Debug/Sentry/LineMapIntegration.php", "Debug/ThrowableRewriter.php"}
	var got []string
	for name := range compiled {
		got = append(got, name)
	}
	sort.Strings(got)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Stdlib() compiles\n%v\nwant\n%v", got, want)
	}
	for stub, want := range map[string]string{
		"runtime/Debug/LineMap.phn":                  "Debug/LineMap.php",
		"runtime/Debug/Monolog/LineMapProcessor.phn": "Debug/Monolog/LineMapProcessor.php",
	} {
		if got := packagePath(stub); got != want {
			t.Errorf("packagePath(%q) = %q, want %q", stub, got, want)
		}
	}
}

// TestDebugPackageFresh compares the compiled stdlib with what the
// phaenon/debug package ships in the sibling checkout (../debug/src): the
// .phn sources here are the one source, `phn stdlib --out ../debug/src`
// materialises them. Skipped when the checkout is absent.
func TestDebugPackageFresh(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "debug", "src")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("no ../debug checkout")
	}
	compiled, err := Stdlib()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range compiled {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s is stale or missing in the debug package: run `phn stdlib --out ../debug/src`", name)
		}
	}
}
