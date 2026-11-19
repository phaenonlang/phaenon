package main

import (
	"strings"
	"testing"
)

// TestIgnoreCodesOfTheRun: without a vendor directory, P4005 is printed
// at the first line of the first file checked, about the run and not that
// line. An ignore comment there that names it alone silences it not, and
// is told that no comment silences it and to go, not that no P4005 is
// reported on its line; a -line form there, with nothing else on the
// line, silences nothing and says so in words that hold beside the P4005.
func TestIgnoreCodesOfTheRun(t *testing.T) {
	bin := phnBinary(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"phaenon.json": `{"php": "8.5", "roots": {"src": ".src"}}`,
		"src/A.phn":    "namespace App; // @phaenon-ignore P4005 (no vendor here)\n\nfunction a(): int => 1;\n",
	})
	check := func() string {
		t.Helper()
		stdout, stderr, _ := run(t, root, bin, "check")
		return stdout + stderr
	}
	out := check()
	for _, want := range []string{
		"src/A.phn:1:1: warning P4005: no vendor/composer directory found",
		"src/A.phn:1:19: warning P4015: @phaenon-ignore: P4005 is no finding of a line, and no comment silences it: remove the comment\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("phn check lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "no P4005 is reported") {
		t.Errorf("phn check says that P4005 is not reported on the line it prints it on:\n%s", out)
	}
	writeTree(t, root, map[string]string{
		"src/A.phn": "namespace App; // @phaenon-ignore-line\n\nfunction a(): int => 1;\n",
	})
	out = check()
	for _, want := range []string{
		"src/A.phn:1:1: warning P4005: no vendor/composer directory found",
		"src/A.phn:1:19: warning P4015: @phaenon-ignore-line: no finding a comment silences is reported on line 1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("phn check lacks %q:\n%s", want, out)
		}
	}
}
