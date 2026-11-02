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
	"sync"
	"testing"

	"github.com/phaenonlang/phaenon/internal/conformance/phpt"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/token"
)

// phpSrc returns the php-src tree whose .phpt files are the corpus, or
// skips: a checkout, or a release branch extracted with git archive (the
// vault's testing note).
func phpSrc(t *testing.T) string {
	dir := os.Getenv("PHAENON_PHP_SRC")
	if dir == "" {
		t.Skip("PHAENON_PHP_SRC not set")
	}
	return dir
}

// oraclePHP is the php binary the suites take as the oracle: PHAENON_PHP,
// or the php on PATH. It should be the release the corpus comes from — the
// PHP-8.6 corpus under PHP 8.6 — since a test of a newer release fails to
// parse or run under an older php.
func oraclePHP() string {
	if php := os.Getenv("PHAENON_PHP"); php != "" {
		return php
	}
	return "php"
}

var (
	oracleOnce sync.Once
	oracleVer  phpversion.Version
	oracleErr  error
)

// oracleVersion is the oracle's release line, which the emitter targets
// when it compiles the corpus.
func oracleVersion(t *testing.T) phpversion.Version {
	oracleOnce.Do(func() {
		out, err := exec.Command(oraclePHP(), "-n", "-r", `echo PHP_MAJOR_VERSION, ".", PHP_MINOR_VERSION;`).Output()
		if err != nil {
			oracleErr = err
			return
		}
		oracleVer, oracleErr = phpversion.Parse(string(out))
	})
	if oracleErr != nil {
		t.Fatalf("php oracle %s: %v", oraclePHP(), oracleErr)
	}
	return oracleVer
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
	cmd := exec.Command(oraclePHP(), "-n", "-d", "memory_limit=-1", "-d", "display_errors=0", oracle)
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
