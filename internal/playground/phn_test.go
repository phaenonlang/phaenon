package playground

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The playground answers what phn build writes and phn check reports for
// the same files in a directory without phaenon.json or Composer. The
// binary is built from this checkout and run on a temporary tree.
func TestMatchesPhn(t *testing.T) {
	if testing.Short() {
		t.Skip("builds phn")
	}
	bin := filepath.Join(t.TempDir(), "phn")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/phn").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	greeter := "namespace App;\n\nfinal class Greeter\n{\n    hello(string $n): string => \"Hello, {$n}\";\n\n    count(): int => 'one';\n}\n"
	cases := map[string]string{
		"clean":      clean,
		"broken":     broken,
		"support":    "namespace App;\n\nfunction run(): string => (new Greeter())->hello('x');\n",
		"deprecated": "namespace App;\n\nfunction latin(string $s): string => utf8_encode($s);\n",
		"generic":    "namespace App;\n\nfinal class Box<T>\n{\n    __construct(private T $value) {}\n\n    get(): T => $this->value;\n}\n\nfunction unbox(Box<int> $b): string => $b->get();\n",
	}
	for name, main := range cases {
		for _, emitOnError := range []bool{false, true} {
			dir := t.TempDir()
			files := map[string]string{"main.phn": main, "lib/Greeter.phn": greeter}
			for path, src := range files {
				os.MkdirAll(filepath.Join(dir, filepath.Dir(path)), 0o755)
				if err := os.WriteFile(filepath.Join(dir, path), []byte(src), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			res := Compile(Request{Files: files, Main: "main.phn", EmitOnError: emitOnError})

			check := exec.Command(bin, "check", "--format=json", "main.phn", "lib/Greeter.phn")
			check.Dir = dir
			out, err := check.Output()
			var exit *exec.ExitError
			if err != nil && !errors.As(err, &exit) {
				t.Fatal(err)
			}
			if got, want := sorted(res.Findings), sorted(phnFindings(t, out)); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: findings\n got %v\nwant %v (phn check)", name, got, want)
			}

			args := []string{"build", "main.phn", "lib/Greeter.phn"}
			if emitOnError {
				args = []string{"build", "--emit-on-error", "main.phn", "lib/Greeter.phn"}
			}
			build := exec.Command(bin, args...)
			build.Dir = dir
			build.Run() // the exit status reports the errors; the outputs are what is compared
			written, err := os.ReadFile(filepath.Join(dir, "main.php"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if res.PHP != string(written) {
				t.Errorf("%s, emitOnError %v: the output differs from phn build's\n got %q\nwant %q", name, emitOnError, res.PHP, written)
			}
		}
	}
}

// phnFindings reads phn check --format=json, leaving out the warnings
// about the environment the playground does not have: no vendor
// directory (P4005), the local php's extensions (P4007, P4012), no
// stated target (P4010).
func phnFindings(t *testing.T, out []byte) []Finding {
	t.Helper()
	var fs []Finding
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		var d struct {
			File                       string
			Line, Col, EndLine, EndCol int
			Severity, Code, Message    string
		}
		if err := json.Unmarshal(sc.Bytes(), &d); err != nil {
			t.Fatalf("%v: %s", err, sc.Bytes())
		}
		switch d.Code {
		case "P4005", "P4007", "P4010", "P4012":
			continue
		}
		fs = append(fs, Finding{File: d.File, Line: d.Line, Column: d.Col, EndLine: d.EndLine, EndColumn: d.EndCol,
			Code: d.Code, Severity: d.Severity, Message: d.Message})
	}
	return fs
}

func sorted(fs []Finding) []Finding {
	out := append([]Finding{}, fs...)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.Code < b.Code
	})
	return out
}
