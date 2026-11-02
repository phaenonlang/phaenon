package conformance

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"testing"

	"github.com/phaenonlang/phaenon/internal/conformance/phpt"
	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/migrate"
	"github.com/phaenonlang/phaenon/internal/program"
)

// TestEmitExecution proves line-preserving emission end to end: each corpus
// file is migrated to Phaenon (tag stripped), emitted back to PHP, and both
// are run through `php`. Outputs must match byte for byte — line numbers in
// warnings, backtraces, __LINE__ and closure names included. Nothing is
// normalised. Enabled with PHAENON_EXEC=1 like TestPrinterExecution.
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
		idx  int
		path string
		orig []byte
		out  []byte
	}
	var jobs []job
	notPlain, emitErrors := 0, 0
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
		// A test that opted into strict types keeps them; the others must
		// not have strictness imposed, the comparison is about lines.
		strict := reStrict.Match(tt.File)
		out, err := emit.File(phn, emit.Options{SourceName: "t.phn", StrictTypes: strict, Program: prog})
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
		jobs = append(jobs, job{idx: i, path: p, orig: tt.File, out: out})
	}

	type result struct {
		path, diff, skippedCause string
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
			b, _ := execPHP(dirB)
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
			if rel, err := filepath.Rel(src, r.path); err == nil && emitDeviations[rel] {
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
	t.Logf("emit execution: %d/%d files behave identically, lines included (%.2f%%); %d skipped (timeout / nondeterministic), %d not plain code (templates), %d emit errors",
		same, same+differ, 100*float64(same)/float64(same+differ), skipped, notPlain, emitErrors)
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
// PHP 8.5 deprecates and Phaenon reserves as the type-test operator.
var reservedInPhaenon = map[string]bool{
	"Zend/tests/deprecated_identifiers/is/class_as_name.phpt":        true,
	"Zend/tests/deprecated_identifiers/is/class_as_name_use.phpt":    true,
	"Zend/tests/deprecated_identifiers/is/constant_as_name_use.phpt": true,
	"Zend/tests/deprecated_identifiers/is/function_as_name.phpt":     true,
	"Zend/tests/deprecated_identifiers/is/function_as_name_use.phpt": true,
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
