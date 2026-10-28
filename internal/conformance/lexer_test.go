package conformance

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phaenon/phaenon/internal/conformance/phpt"
	"github.com/phaenon/phaenon/internal/lexer"
	"github.com/phaenon/phaenon/internal/token"
)

// phpSrc returns the php-src checkout to test against, or skips.
func phpSrc(t *testing.T) string {
	dir := os.Getenv("PHAENON_PHP_SRC")
	if dir == "" {
		t.Skip("PHAENON_PHP_SRC not set")
	}
	return dir
}

// collectPhpt lists every .phpt under dir (optionally restricted by PHAENON_PHPT_FILTER).
func collectPhpt(t *testing.T, dir string) []string {
	filter := os.Getenv("PHAENON_PHPT_FILTER")
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".phpt") && (filter == "" || strings.Contains(path, filter)) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

type oracleLine struct {
	File   string      `json:"file"`
	Tokens [][2]string `json:"tokens"`
}

// runOracle tokenizes files with PHP and returns the result per path.
func runOracle(t *testing.T, files []string) map[string][][2]string {
	_, self, _, _ := runtimeCaller()
	oracle := filepath.Join(filepath.Dir(self), "oracle.php")
	cmd := exec.Command("php", "-n", "-d", "memory_limit=-1", "-d", "display_errors=0", oracle)
	cmd.Stdin = strings.NewReader(strings.Join(files, "\n") + "\n")
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("php oracle: %v", err)
	}
	res := map[string][][2]string{}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<28)
	for sc.Scan() {
		var l oracleLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("oracle output: %v", err)
		}
		res[l.File] = l.Tokens
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("php oracle: %v", err)
	}
	return res
}

// TestLexerAgainstPHP lexes the --FILE-- section of every .phpt in php-src
// and compares (token name, text) sequences with PhpToken::tokenize().
func TestLexerAgainstPHP(t *testing.T) {
	src := phpSrc(t)
	tmp := t.TempDir()
	var paths []string
	for i, p := range collectPhpt(t, src) {
		tt, err := phpt.Parse(p)
		if err != nil {
			continue // no --FILE-- (e.g. --FILE_EXTERNAL--)
		}
		out := filepath.Join(tmp, fmt.Sprintf("%06d.php", i))
		if err := os.WriteFile(out, tt.File, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, out)
	}
	t.Logf("%d test files", len(paths))

	want := runOracle(t, paths)
	var failed []string
	shown := 0
	for _, p := range paths {
		srcBytes, _ := os.ReadFile(p)
		got := lexer.Tokenize(srcBytes, lexer.ModePHP)
		if diff := compare(srcBytes, got, want[p]); diff != "" {
			failed = append(failed, p)
			if shown < 15 {
				t.Errorf("%s:\n%s", p, diff)
				shown++
			}
		}
	}
	t.Logf("lexer conformance: %d/%d files match PHP (%.2f%%)",
		len(paths)-len(failed), len(paths), 100*float64(len(paths)-len(failed))/float64(len(paths)))
}

// compare returns "" when our tokens equal PHP's, else a description of the
// first divergence.
func compare(src []byte, got []token.Token, want [][2]string) string {
	var all bytes.Buffer
	for _, g := range got {
		all.Write(src[g.Start:g.End])
	}
	if !bytes.Equal(all.Bytes(), src) {
		return "lexer is not lossless"
	}
	n := len(got) - 1 // drop EOF
	for i := 0; i < n || i < len(want); i++ {
		if i >= n {
			wantText, _ := base64.StdEncoding.DecodeString(want[i][1])
			return fmt.Sprintf("token %d: missing, PHP has %s %q", i, want[i][0], wantText)
		}
		if i >= len(want) {
			return fmt.Sprintf("token %d: extra %s %q", i, got[i].Kind, got[i].Text(src))
		}
		wantText, _ := base64.StdEncoding.DecodeString(want[i][1])
		if got[i].Kind.String() != want[i][0] || got[i].Text(src) != string(wantText) {
			return fmt.Sprintf("token %d (line %d): got %s %q, PHP has %s %q",
				i, got[i].Line, got[i].Kind, got[i].Text(src), want[i][0], wantText)
		}
	}
	return ""
}
