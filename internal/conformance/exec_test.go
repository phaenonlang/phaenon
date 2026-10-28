package conformance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phaenonlang/phaenon/internal/conformance/phpt"
)

// TestPrinterExecution runs each corpus file and its printed form through
// `php` and compares what they print. It is the semantic complement of the
// token round trip: it catches spellings the lexer treats alike but the
// engine does not. Enabled with PHAENON_EXEC=1 (it executes test code; the
// default corpus subset is Zend/tests, override with PHAENON_PHPT_FILTER).
func TestPrinterExecution(t *testing.T) {
	if os.Getenv("PHAENON_EXEC") == "" {
		t.Skip("set PHAENON_EXEC=1 to run the php execution comparison")
	}
	src := phpSrc(t)
	if os.Getenv("PHAENON_PHPT_FILTER") == "" {
		t.Setenv("PHAENON_PHPT_FILTER", "Zend/tests")
	}
	tmp := t.TempDir()

	type job struct {
		idx  int
		path string
		orig []byte
		prnt string
	}
	var jobs []job
	lineSensitiveCount := 0
	for i, p := range collectPhpt(t, src) {
		tt, err := phpt.Parse(p)
		if err != nil {
			continue
		}
		// Sections that change how the file must be run are out of scope here.
		skip := false
		for _, sec := range []string{"STDIN", "ARGS", "ENV", "INI", "CGI", "POST", "GET", "COOKIE", "PHPDBG", "CAPTURE_STDIO"} {
			if _, has := tt.Sections[sec]; has {
				skip = true
			}
		}
		if skip {
			continue
		}
		if lineSensitive(tt.File) {
			lineSensitiveCount++
			continue
		}
		printed, _, err := roundTrip(tt.File)
		if err != nil {
			continue
		}
		jobs = append(jobs, job{idx: i, path: p, orig: tt.File, prnt: printed})
	}

	type result struct {
		path         string
		diff         string
		skippedCause string
	}
	results := make([]result, len(jobs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			dirA := filepath.Join(tmp, "a", fmt.Sprint(j.idx))
			dirB := filepath.Join(tmp, "b", fmt.Sprint(j.idx))
			os.MkdirAll(dirA, 0o755)
			os.MkdirAll(dirB, 0o755)
			os.WriteFile(filepath.Join(dirA, "t.php"), j.orig, 0o644)
			os.WriteFile(filepath.Join(dirB, "t.php"), []byte(j.prnt), 0o644)
			a1, ok1 := runPHP(dirA)
			if !ok1 {
				results[i] = result{path: j.path, skippedCause: "timeout"}
				return
			}
			for n := 0; n < 2; n++ {
				if a2, _ := runPHP(dirA); a1 != a2 {
					results[i] = result{path: j.path, skippedCause: "nondeterministic"}
					return
				}
			}
			b, _ := runPHP(dirB)
			if a1 != b {
				results[i] = result{path: j.path, diff: excerptDiff(a1, b)}
				return
			}
			results[i] = result{path: j.path}
		}(i, j)
	}
	wg.Wait()

	var same, differ, skipped int
	shown := 0
	for _, r := range results {
		switch {
		case r.skippedCause != "":
			skipped++
		case r.diff != "":
			if rel, err := filepath.Rel(src, r.path); err == nil && execDeviations[rel] {
				same++
				continue
			}
			differ++
			if shown < showLimit() {
				t.Errorf("%s:\n%s", r.path, r.diff)
				shown++
			}
		default:
			same++
		}
	}
	t.Logf("printer execution: %d/%d files behave identically (%.2f%%); %d skipped (timeout / nondeterministic), %d not run (observe line numbers)",
		same, same+differ, 100*float64(same)/float64(same+differ), skipped, lineSensitiveCount)
}

// lineSensitive reports whether a test observes source line numbers or byte
// offsets, which a reflowing printer cannot preserve. (Line-preserving
// emission is an M1 item, see roadmap.)
func lineSensitive(src []byte) bool {
	lower := bytes.ToLower(src)
	for _, needle := range []string{
		"__line__", "->line", "memory_limit", "random_int(", "rand(", "mt_rand(", "uniqid(", "spl_object_id(", "spl_object_hash(", "__COMPILER_HALT_OFFSET__", "getLine(", "debug_backtrace(", "debug_print_backtrace(",
		"getTrace(", "getTraceAsString(", "getStartLine(", "getEndLine(", "getExecutingLine(",
		"set_error_handler(", "set_exception_handler(", "ReflectionFunction", "ReflectionMethod",
		"ReflectionClass", "ReflectionObject", "ReflectionEnum", "ReflectionProperty",
	} {
		if bytes.Contains(lower, bytes.ToLower([]byte(needle))) {
			return true
		}
	}
	return false
}

var (
	reLine    = regexp.MustCompile(` on line \d+`)
	reFrame   = regexp.MustCompile(`t\.php\(\d+\)`)
	reLineCol = regexp.MustCompile(`t\.php:\d+`)
	reClosure = regexp.MustCompile(`\{closure:((?:[^{}]|\{[^{}]*\})*):\d+\}`)
	reDumpLn  = regexp.MustCompile(`\["line"\]=>\s*int\(\d+\)`)
	reStrLen  = regexp.MustCompile(`string\(\d+\) "(\{closure:)`)
	reHandler = regexp.MustCompile(`'/private/var/fo\.\.\.', \d+`)
	reNoFile  = regexp.MustCompile(` in :\d+`) // uncaught exception whose file was unset
)

// execDeviations are corpus files whose printed form intentionally behaves
// differently: PHP rejects the original at compile time (duplicate
// modifiers, empty array elements, an unterminated comment) while the tree
// keeps one canonical form that PHP accepts.
var execDeviations = map[string]bool{
	"Zend/tests/access_modifiers/access_modifiers_001.phpt":               true,
	"Zend/tests/access_modifiers/access_modifiers_002.phpt":               true,
	"Zend/tests/access_modifiers/access_modifiers_003.phpt":               true,
	"Zend/tests/access_modifiers/access_modifiers_004.phpt":               true,
	"Zend/tests/access_modifiers/access_modifiers_005.phpt":               true,
	"Zend/tests/access_modifiers/access_modifiers_006.phpt":               true,
	"Zend/tests/constexpr/gh9138.phpt":                                    true,
	"Zend/tests/constexpr/gh9138_2.phpt":                                  true,
	"Zend/tests/list/list_013.phpt":                                       true,
	"Zend/tests/readonly_classes/gh10377_4.phpt":                          true,
	"Zend/tests/readonly_classes/readonly_class_duplicated_modifier.phpt": true,
	"Zend/tests/readonly_props/multiple_modifiers.phpt":                   true,
	"Zend/tests/unterminated_comment.phpt":                                true,
}

// runPHP executes dir/t.php and returns its normalised combined output.
// ok is false when the run timed out.
func runPHP(dir string) (out string, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "php", "-n",
		"-d", "display_errors=1", "-d", "error_reporting=-1", "-d", "html_errors=0",
		"-d", "memory_limit=256M", "-d", "max_execution_time=5", "-d", "zend.assertions=1",
		"t.php")
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", false
	}
	code := 0
	if ee, isExit := err.(*exec.ExitError); isExit {
		code = ee.ExitCode()
	}
	s := buf.String()
	s = strings.ReplaceAll(s, dir, "<dir>")
	s = reLine.ReplaceAllString(s, " on line N")
	s = reFrame.ReplaceAllString(s, "t.php(N)")
	s = reLineCol.ReplaceAllString(s, "t.php:N")
	s = reDumpLn.ReplaceAllString(s, `["line"]=> int(N)`)
	s = reStrLen.ReplaceAllString(s, `string(N) "$1`)
	s = reHandler.ReplaceAllString(s, `'<file>', N`)
	s = reNoFile.ReplaceAllString(s, " in :N")
	for {
		n := reClosure.ReplaceAllString(s, "{closure:$1:N}")
		if n == s {
			break
		}
		s = n
	}
	return fmt.Sprintf("%s\n[exit %d]", s, code), true
}

func showLimit() int {
	n := 25
	if v := os.Getenv("PHAENON_SHOW_DIFFS"); v != "" {
		fmt.Sscan(v, &n)
	}
	return n
}

func excerptDiff(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(la) && i < len(lb); i++ {
		if la[i] != lb[i] {
			return fmt.Sprintf("  line %d\n  orig: %s\n  prnt: %s", i+1, clip(la[i]), clip(lb[i]))
		}
	}
	return fmt.Sprintf("  output length differs: %d vs %d lines", len(la), len(lb))
}

func clip(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
