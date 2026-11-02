package conformance

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/conformance/phpt"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// runParseOracle asks PHP whether each file parses.
func runParseOracle(t *testing.T, files []string) map[string]string {
	_, self, _, _ := runtimeCaller()
	oracle := filepath.Join(filepath.Dir(self), "parse_oracle.php")
	cmd := exec.Command(oraclePHP(), "-n", "-d", "memory_limit=-1", "-d", "display_errors=0", oracle)
	cmd.Stdin = strings.NewReader(strings.Join(files, "\n") + "\n")
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	res := map[string]string{}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 3)
		if len(parts) < 2 {
			continue
		}
		if parts[1] == "ok" {
			res[parts[0]] = ""
		} else {
			res[parts[0]] = parts[1] + ": " + parts[2]
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("php oracle: %v", err)
	}
	return res
}

// TestParserAgainstPHP parses every .phpt --FILE-- and compares accept/reject
// with PHP's parser. Files PHP accepts must parse; files PHP rejects should
// be rejected too (reported, but not yet required).
func TestParserAgainstPHP(t *testing.T) {
	src := phpSrc(t)
	tmp := t.TempDir()
	var paths []string
	srcOf := map[string]string{}
	for i, p := range collectPhpt(t, src) {
		tt, err := phpt.Parse(p)
		if err != nil {
			continue
		}
		out := filepath.Join(tmp, fmt.Sprintf("%06d.php", i))
		if err := os.WriteFile(out, tt.File, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, out)
		srcOf[out] = p
	}
	want := runParseOracle(t, paths)

	var falseReject, falseAccept []string
	shown := 0
	for _, p := range paths {
		b, _ := os.ReadFile(p)
		_, err := parser.Parse(b, lexer.ModePHP)
		phpErr, known := want[p]
		if !known {
			continue
		}
		switch {
		case phpErr == "" && err != nil:
			falseReject = append(falseReject, p)
			if shown < 25 {
				t.Errorf("%s: PHP accepts, we reject: %v", srcOf[p], err)
				shown++
			}
		case phpErr != "" && err == nil:
			falseAccept = append(falseAccept, p)
		}
	}
	accepted := 0
	for _, e := range want {
		if e == "" {
			accepted++
		}
	}
	t.Logf("parser conformance: %d/%d PHP-accepted files parse (%.2f%%); %d PHP-rejected files wrongly accepted (of %d)",
		accepted-len(falseReject), accepted, 100*float64(accepted-len(falseReject))/float64(accepted),
		len(falseAccept), len(want)-accepted)
	if os.Getenv("PHAENON_SHOW_FALSE_ACCEPT") != "" {
		for i, p := range falseAccept {
			if i >= 30 {
				break
			}
			t.Logf("false accept: %s (%s)", srcOf[p], want[p])
		}
	}
}
