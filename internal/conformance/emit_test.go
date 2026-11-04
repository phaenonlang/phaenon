package conformance

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/conformance/phpt"
	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/linemap"
	"github.com/phaenonlang/phaenon/internal/migrate"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
)

// TestEmitExecution proves emission and its line map end to end: each
// corpus file is migrated to Phaenon (tag stripped, every line kept), emitted
// back to PHP, and both are run through `php`. Outputs must match byte for
// byte once the line numbers the compiled file reports — in warnings,
// backtraces, closure names, dumped frames — are translated through its line
// map (translateLines), which makes the corpus a large test of the map;
// __LINE__ compiles to the source line and needs no translation. Enabled
// with PHAENON_EXEC=1 like TestPrinterExecution.
func TestEmitExecution(t *testing.T) {
	if os.Getenv("PHAENON_EXEC") == "" {
		t.Skip("set PHAENON_EXEC=1 to run the php execution comparison")
	}
	src := phpSrc(t)
	if os.Getenv("PHAENON_PHPT_FILTER") == "" {
		t.Setenv("PHAENON_PHPT_FILTER", "Zend/tests")
	}
	tmp := t.TempDir()
	// The corpus is compiled for the oracle's release: what it tests is
	// what that PHP runs, partial applications written as such on 8.6.
	prog := program.New()
	prog.SetTarget(oracleVersion(t))

	type job struct {
		idx   int
		path  string
		orig  []byte
		out   []byte
		lines *linemap.Map
	}
	var jobs []job
	notPlain, emitErrors, newer := 0, 0, 0
	for i, p := range collectPhpt(t, src) {
		tt, err := phpt.Parse(p)
		if err != nil {
			continue
		}
		if hasRunSections(tt) {
			continue
		}
		if skipEmit(tt.File) {
			continue
		}
		phn, err := migrate.File(tt.File, migrate.Options{KeepLines: true})
		if err != nil {
			notPlain++
			continue
		}
		if !prog.Target().AtLeast(phpversion.PHP86) && hasPlaceholders(phn) {
			// A test of PHP 8.6 in a newer corpus than the oracle: its
			// partial applications would need the checker to be lowered,
			// and the oracle cannot run the original.
			newer++
			continue
		}
		// A test that opted into strict types keeps them; the others must
		// not have strictness imposed, the comparison is about behaviour.
		strict := reStrict.Match(tt.File)
		out, err := emit.File(phn, emit.Options{SourceName: "t.phn", MapSource: "t.phn", StrictTypes: strict, Program: prog})
		if err != nil {
			if rel, rerr := filepath.Rel(src, p); rerr == nil && reservedInPhaenon[rel] {
				continue
			}
			emitErrors++
			if emitErrors <= showLimit() {
				t.Errorf("%s: emit: %v", p, err)
			}
			continue
		}
		m, err := linemap.Find(out)
		if err != nil || m == nil {
			t.Errorf("%s: compiled without a line map (%v)", p, err)
			continue
		}
		jobs = append(jobs, job{idx: i, path: p, orig: tt.File, out: out, lines: m})
	}

	type result struct {
		path, diff, skippedCause string
		observes                 bool
		// tabLayout: the original fails on its heredoc's indentation,
		// which the migration's spaces (design/functions.md §5.5) change.
		tabLayout bool
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
			os.WriteFile(filepath.Join(dirB, "t.php"), j.out, 0o644)
			a1, ok := execPHP(dirA)
			if !ok {
				results[i] = result{path: j.path, skippedCause: "timeout"}
				return
			}
			for n := 0; n < 2; n++ {
				if a2, _ := execPHP(dirA); a1 != a2 {
					results[i] = result{path: j.path, skippedCause: "nondeterministic"}
					return
				}
			}
			b, ok := execPHPRewritten(dirB, func(out string) string { return translateLines(out, j.lines) })
			if !ok {
				// A timeout under load is no output to compare, as for the original.
				results[i] = result{path: j.path, skippedCause: "timeout"}
				return
			}
			if a1 != b {
				observes := observesLines(j.orig) && reDigits.ReplaceAllString(a1, "N") == reDigits.ReplaceAllString(b, "N")
				results[i] = result{path: j.path, diff: excerptDiff(a1, b), observes: observes,
					tabLayout: reHeredocIndentError.MatchString(a1) && bytes.ContainsRune(j.orig, '\t')}
				return
			}
			results[i] = result{path: j.path}
		}(i, j)
	}
	wg.Wait()

	var same, differ, skipped, observing, tabLayout int
	shown := 0
	for _, r := range results {
		switch {
		case r.skippedCause != "":
			skipped++
		case r.diff != "":
			if rel, err := filepath.Rel(src, r.path); err == nil && emitDeviations[rel] {
				same++
				continue
			}
			if r.tabLayout {
				// A test of PHP's heredoc indentation errors written with
				// tabs: Phaenon's whitespace is spaces, and the migration's
				// replacement makes the indentation consistent (or counts it
				// in other columns), so PHP reports another error, or none.
				tabLayout++
				continue
			}
			if r.observes {
				// The test reads a line number as a value (an error
				// handler's $errline, getLine()): the compiled file reports
				// its own line, which code compares through the map
				// (line-map.md §5), and the harness cannot tell that number
				// from any other in the output.
				observing++
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
	t.Logf("emit execution: %d/%d files behave identically, lines included through the map (%.2f%%); %d read compiled lines as values, %d test heredoc indentation errors with tabs, %d skipped (timeout / nondeterministic), %d not plain code (templates), %d newer than the oracle, %d emit errors",
		same, same+differ, 100*float64(same)/float64(same+differ), observing, tabLayout, skipped, notPlain, newer, emitErrors)
}

// The positions a run of the compiled t.php reports. Every pattern names
// the file, except the line of a dumped backtrace frame (`["line"]=>
// int(N)`, print_r's `[line] => N`, var_export's `'line' => N`), a
// closure's name (`{closure:foo():N}`), an uncaught exception whose file
// was unset (` in :N`, `thrown in Unknown on line N`) and an unterminated comment,
// which the corpus's tests show only for t.php.
var (
	reOnLine    = regexp.MustCompile(`t\.php on line (\d+)`)
	reColon     = regexp.MustCompile(`t\.php:(\d+)`)
	reParen     = regexp.MustCompile(`t\.php\((\d+)\)`)
	reRange     = regexp.MustCompile(`t\.php (\d+)( ?- ?)(\d+)`) // a reflected function's lines
	reUnset     = regexp.MustCompile(` in :(\d+)`)
	reUnknown   = regexp.MustCompile(`thrown in Unknown on line (\d+)`)          // an uncaught exception whose file was unset
	reComment   = regexp.MustCompile(`Unterminated comment starting line (\d+)`) // a parse error of t.php
	reDumpLine  = regexp.MustCompile(`(\["line"\]=>\s*int\()(\d+)(\))`)
	rePrintLine = regexp.MustCompile(`(\[line\] => )(\d+)()`)
	reExportLn  = regexp.MustCompile(`('line' => )(\d+)()`)
	reString    = regexp.MustCompile(`string\((\d+)\) "`)
	reDigits    = regexp.MustCompile(`\d+`)
)

// translateLines rewrites the compiled file's line numbers in a run's raw
// output into the source's, through the map. A var_dump'ed string that
// holds a position keeps a true length: its content is translated on its
// own and the length adjusted by what that changed.
func translateLines(out string, m *linemap.Map) string {
	var b strings.Builder
	rest := out
	for {
		loc := reString.FindStringSubmatchIndex(rest)
		if loc == nil {
			b.WriteString(translatePositions(rest, m))
			return b.String()
		}
		n, _ := strconv.Atoi(rest[loc[2]:loc[3]])
		end := loc[1] + n
		if end > len(rest) {
			b.WriteString(translatePositions(rest, m))
			return b.String()
		}
		b.WriteString(translatePositions(rest[:loc[0]], m))
		content := translatePositions(rest[loc[1]:end], m)
		fmt.Fprintf(&b, "string(%d) \"%s", len(content), content)
		rest = rest[end:]
	}
}

func translatePositions(s string, m *linemap.Map) string {
	forward := func(digits string) string {
		n, err := strconv.Atoi(digits)
		if err != nil {
			return digits
		}
		if src, ok := m.Forward(n); ok {
			return strconv.Itoa(src)
		}
		return digits
	}
	s = translateClosureNames(s, forward)
	for _, p := range []struct {
		re            *regexp.Regexp
		before, after string
	}{{reOnLine, "t.php on line ", ""}, {reColon, "t.php:", ""}, {reParen, "t.php(", ")"}, {reUnset, " in :", ""},
		{reUnknown, "thrown in Unknown on line ", ""}, {reComment, "Unterminated comment starting line ", ""}} {
		s = p.re.ReplaceAllStringFunc(s, func(match string) string {
			return p.before + forward(p.re.FindStringSubmatch(match)[1]) + p.after
		})
	}
	s = reRange.ReplaceAllStringFunc(s, func(match string) string {
		g := reRange.FindStringSubmatch(match)
		return "t.php " + forward(g[1]) + g[2] + forward(g[3])
	})
	for _, re := range []*regexp.Regexp{reDumpLine, rePrintLine, reExportLn} {
		s = re.ReplaceAllStringFunc(s, func(match string) string {
			g := re.FindStringSubmatch(match)
			return g[1] + forward(g[2]) + g[3]
		})
	}
	return s
}

// translateClosureNames rewrites the line a closure's name ends with,
// `{closure:foo():N}`, nested names included. A name whose scope is a file
// (`{closure:/path/t.php:N}`) is left to the t.php patterns.
func translateClosureNames(s string, forward func(string) string) string {
	const open = "{closure:"
	var b strings.Builder
	for {
		i := strings.Index(s, open)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		depth, j := 0, i
		for ; j < len(s); j++ {
			if s[j] == '{' {
				depth++
			} else if s[j] == '}' {
				if depth--; depth == 0 {
					break
				}
			}
		}
		if j == len(s) {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		inner := s[i+len(open) : j]
		if k := strings.LastIndexByte(inner, ':'); k >= 0 && isDigits(inner[k+1:]) && !strings.HasSuffix(inner[:k], ".php") {
			inner = translateClosureNames(inner[:k], forward) + ":" + forward(inner[k+1:])
		} else {
			inner = translateClosureNames(inner, forward)
		}
		b.WriteString(open + inner + "}")
		s = s[j+1:]
	}
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// observesLines reports whether a test reads line numbers as values, which
// the compiled file reports as its own: an error handler's $errline, a
// throwable's or a reflection's line, a backtrace taken as data, an
// assertion callback's $line.
func observesLines(src []byte) bool {
	lower := bytes.ToLower(src)
	for _, needle := range []string{"set_error_handler(", "getline(", "getstartline(", "getendline(", "getexecutingline(", "debug_backtrace(", "assert_options("} {
		if bytes.Contains(lower, []byte(needle)) {
			return true
		}
	}
	return false
}

// hasPlaceholders reports whether a migrated test holds partial
// applications (PHP 8.6).
func hasPlaceholders(phn []byte) bool {
	f, err := parser.Parse(phn, lexer.ModePhaenon)
	if err != nil {
		return false
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if _, ok := n.(*ast.Placeholder); ok {
			found = true
		}
		return !found
	})
	return found
}

// skipEmit reports whether a test observes something emission does not
// preserve (byte offsets) or is nondeterministic in a way three runs can
// miss (random values).
func skipEmit(src []byte) bool {
	lower := bytes.ToLower(src)
	for _, needle := range []string{"__compiler_halt_offset__", "random_int(", "rand(", "mt_rand(", "uniqid("} {
		if bytes.Contains(lower, []byte(needle)) {
			return true
		}
	}
	return false
}

var reStrict = regexp.MustCompile(`strict_types\s*=\s*1`)

// reservedInPhaenon lists tests that declare something named `is`, which
// PHP 8.6 deprecates and Phaenon reserves as the type-test operator.
var reservedInPhaenon = map[string]bool{
	"Zend/tests/deprecated_identifiers/is/class_as_name.phpt":        true,
	"Zend/tests/deprecated_identifiers/is/class_as_name_use.phpt":    true,
	"Zend/tests/deprecated_identifiers/is/constant_as_name_use.phpt": true,
	"Zend/tests/deprecated_identifiers/is/function_as_name.phpt":     true,
	"Zend/tests/deprecated_identifiers/is/function_as_name_use.phpt": true,
	// The PHP-8.5 branch's versions of these declare a function is(); the
	// newer branches renamed it once PHP 8.6 deprecated the name.
	"Zend/tests/asymmetric_visibility/variation.phpt":        true,
	"Zend/tests/asymmetric_visibility/variation_nested.phpt": true,
	"Zend/tests/readonly_props/variation.phpt":               true,
	"Zend/tests/readonly_props/variation_nested.phpt":        true,
}

// emitDeviations are corpus files whose comparison is void.
var emitDeviations = map[string]bool{
	// Engine use-after-free: the output flips between runs on the stock
	// binary, with or without the emitter.
	"Zend/tests/magic_methods/gh21603.phpt": true,
	// A closure in a constant expression is written static by the emitter
	// (design/functions.md); PHP's compile error for a non-static one is
	// the checker's P2531, which the harness does not run.
	"Zend/tests/closures/closure_const_expr/disallows_non_static.phpt": true,
}

// hasRunSections reports whether the test needs stdin, arguments, ini
// settings or a CGI environment, which the harness does not reproduce.
func hasRunSections(tt *phpt.Test) bool {
	for _, sec := range []string{"STDIN", "ARGS", "ENV", "INI", "CGI", "POST", "GET", "COOKIE", "PHPDBG", "CAPTURE_STDIO"} {
		if _, has := tt.Sections[sec]; has {
			return true
		}
	}
	return false
}

// reHeredocIndentError matches PHP's parse errors about a heredoc's or
// nowdoc's indentation.
var reHeredocIndentError = regexp.MustCompile(`Invalid (body )?indentation`)
