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

// lintOracle asks `php -l` whether each file parses: "" for one that does,
// php's message for a syntax error. It is runParseOracle for a handful of
// files where no corpus is: the tokenizer that one asks is a shared
// extension on some builds of php, which `-n` leaves out, and `php -l`
// needs none.
func lintOracle(t *testing.T, files []string) map[string]string {
	res := map[string]string{}
	for _, f := range files {
		out, err := exec.Command(oraclePHP(), "-n", "-d", "display_errors=1", "-d", "log_errors=0", "-l", f).CombinedOutput()
		text := strings.TrimSpace(string(out))
		switch {
		case err == nil && strings.Contains(text, "No syntax errors detected"):
			res[f] = ""
		case strings.Contains(text, "Parse error:"):
			res[f], _, _ = strings.Cut(text, "\n")
		default:
			t.Fatalf("%s -l %s: %v\n%s", oraclePHP(), f, err, text)
		}
	}
	return res
}

// TestTypeArgumentGuessAgainstPHP: PHP code in which `<` follows a name and
// a `>>` or `>=` comes after it, where Phaenon's parser first tries a
// call's type arguments. No statement here is such a call, so each is
// accepted or refused as PHP's parser does, in a .phn as in a .php. It
// needs a php only, not the corpus.
func TestTypeArgumentGuessAgainstPHP(t *testing.T) {
	if _, err := exec.LookPath(oraclePHP()); err != nil {
		t.Skipf("no %s to ask", oraclePHP())
	}
	stmts := []string{
		"if (A < B >> 1) {}",
		"$x = A < B >> 1;",
		"$x = Foo < Bar >> 2; echo $x; echo 1;",
		"$x = \\A\\B < C\\D >> 2;",
		"$x = [A < B >> 1, 2];",
		"$x = [A < B, C >> 1];",
		"$x = [A < B, C >= 1];",
		"g(A < B >> 1);",
		"g(A < B, C >> 1, D >= 2);",
		"$x = $o->p < B >> 1;",
		"$x = X::Y < B >> 1;",
		"$x = X::y() < B >> 1 || C > 1;",
		"while (A < B >> 1) { $x = [A < B, C >= 1]; } return A < B >> 1;",
		"class C { const X = A < B >> 1; public bool $p = A < B >> 1; }",
		"$x = A < B >> 1; $y = A < B >> 1; $z = [A < B, C >= 1];",
		// Comparisons do not chain in PHP.
		"$x = A < B >= 1;",
		"$x = A < B > 1;",
		"$x = Foo < Bar >= $y;",
		"$x = A < B >> 1 > 2;",
		"if (A < B >= 1) {}",
	}
	tmp := t.TempDir()
	var paths []string
	for i, s := range stmts {
		p := filepath.Join(tmp, fmt.Sprintf("%02d.php", i))
		if err := os.WriteFile(p, []byte("<?php\n"+s+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	want := lintOracle(t, paths)
	accepted := 0
	for i, s := range stmts {
		phpErr, known := want[paths[i]]
		if !known {
			t.Errorf("%s: no answer from php", s)
			continue
		}
		if phpErr == "" {
			accepted++
		}
		for _, mode := range []lexer.Mode{lexer.ModePHP, lexer.ModePhaenon} {
			src := s + "\n"
			if mode == lexer.ModePHP {
				src = "<?php\n" + src
			}
			_, err := parser.Parse([]byte(src), mode)
			switch {
			case phpErr == "" && err != nil:
				t.Errorf("%s (mode %v): PHP accepts, we reject: %v", s, mode, err)
			case phpErr != "" && err == nil:
				t.Errorf("%s (mode %v): we accept, PHP rejects: %s", s, mode, phpErr)
			}
		}
	}
	// Both answers must be among them, or the list proves nothing.
	if accepted == 0 || accepted == len(stmts) {
		t.Errorf("php accepts %d of %d statements", accepted, len(stmts))
	}
}
