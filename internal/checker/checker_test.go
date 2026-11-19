package checker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
)

// Golden corpus: every testdata/*.phn is checked on its own, with
// testdata/vendor as a PSR-4 root for the Acme\ namespace. Expected
// diagnostics are `// want P2103` comments on the offending line (several
// codes separated by commas; an optional "substring" must appear in the
// message). An extra check (design/roadmap.md D33) is marked after its
// code: `// want P2301 extra`, `// want P2103 extra "substring"`. The set
// of (line, code, kind) triples must match exactly, the kind being whether
// the finding is an extra check; and checked a second time with the extra
// checks off, the file gives the same findings less the marked ones.

var reWant = regexp.MustCompile(`//\s*want\s+(.*)$`)
var reWantItem = regexp.MustCompile(`(P\d{4})(\s+extra\b)?(?:\s+"([^"]*)")?`)

// The comment lines that open a golden file may set how it is checked:
//
//	// target: 8.6
//	// deprecations: on
//	// reflected-attributes: ["Acme\\Mapping"]
//
// target is the PHP version the file compiles for, the floor (PHP 8.5)
// without one; deprecations: on reports deprecated constructs and library
// symbols, as phn check does unless --no-deprecations; reflected-attributes
// is phaenon.json's unused.reflected-attributes, a JSON list.
var reHeader = regexp.MustCompile(`^//\s*(target|deprecations|reflected-attributes):\s*(\d+\.\d+|on|\[.*\])\s*$`)

// unfolded is the lowering's answer for Options.Unfolded, as phn check
// asks it.
func unfolded(p *program.Program, f *program.File) []ast.Expr {
	return emit.UnfoldedBound(f.Src, emit.Options{SourceName: f.Path, StrictTypes: true, Program: p, File: f})
}

func goldenSetup(t *testing.T, src []byte) (phpversion.Version, Options) {
	t.Helper()
	target, opts := phpversion.Floor, Options{Unfolded: unfolded}
	for _, line := range strings.Split(string(src), "\n") {
		if !strings.HasPrefix(line, "//") {
			break
		}
		m := reHeader.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch m[1] {
		case "target":
			v, err := phpversion.Parse(m[2])
			if err != nil || !v.Supported() {
				t.Fatalf("golden: bad target %q", m[2])
			}
			target = v
		case "deprecations":
			if m[2] != "on" {
				t.Fatalf("golden: deprecations: %q, expected on", m[2])
			}
			opts.Deprecations = true
		case "reflected-attributes":
			opts.ReflectedAttributes = []string{}
			if err := json.Unmarshal([]byte(m[2]), &opts.ReflectedAttributes); err != nil {
				t.Fatalf("golden: reflected-attributes: %v", err)
			}
		}
	}
	return target, opts
}

type want struct {
	file  string // the file the comment is in, where a program has several
	line  int
	code  string
	extra bool // marked `extra`: an extra check
	msg   string
}

func parseWants(src string) []want {
	var out []want
	for i, line := range strings.Split(src, "\n") {
		m := reWant.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		for _, item := range reWantItem.FindAllStringSubmatch(m[1], -1) {
			out = append(out, want{line: i + 1, code: item[1], extra: item[2] != "", msg: item[3]})
		}
	}
	return out
}

// holdToWants holds the findings of one check to the corpus's comments.
// With the extra checks on, they are the wants and nothing else, each of
// its kind: a finding is an extra check exactly where its want is marked
// `extra`. With them off (Options.NoExtraChecks), they are the same
// findings less the marked ones: every other one stays, and none comes.
func holdToWants(t *testing.T, wants []want, diags []Diagnostic, extraChecks bool) {
	t.Helper()
	mode := ""
	if !extraChecks {
		mode = "with the extra checks off, "
	}
	type key struct {
		file  string
		line  int
		code  string
		extra bool
	}
	kind := func(extra bool) string {
		if extra {
			return " as an extra check"
		}
		return ""
	}
	got := map[key][]Diagnostic{}
	for _, d := range diags {
		if IsLanguageRule(d.Code) != (d.Kind == LanguageRule) {
			t.Errorf("%s%s:%d: %s is of the kind %s, and its code %s a language rule", mode, d.File, d.Line, d.Code, d.Kind,
				map[bool]string{true: "is", false: "is not"}[IsLanguageRule(d.Code)])
		}
		k := key{d.File, d.Line, d.Code, d.Kind == Extra}
		got[k] = append(got[k], d)
	}
	matched := map[key]int{}
	for _, w := range wants {
		if w.extra && !extraChecks {
			continue
		}
		k := key{w.file, w.line, w.code, w.extra}
		ds := got[k]
		if matched[k] >= len(ds) {
			if other := (key{w.file, w.line, w.code, !w.extra}); matched[other] < len(got[other]) {
				matched[other]++
				if w.extra {
					t.Errorf("%s%s:%d: %s is marked extra, and is reported as a finding that is none", mode, w.file, w.line, w.code)
				} else {
					t.Errorf("%s%s:%d: %s is reported as an extra check, and the line does not mark it `extra`", mode, w.file, w.line, w.code)
				}
				continue
			}
			t.Errorf("%s%s:%d: expected %s%s, not reported", mode, w.file, w.line, w.code, kind(w.extra))
			continue
		}
		d := ds[matched[k]]
		matched[k]++
		if w.msg != "" && !strings.Contains(d.Message, w.msg) {
			t.Errorf("%s%s:%d: %s message %q does not contain %q", mode, w.file, w.line, w.code, d.Message, w.msg)
		}
	}
	var unexpected []string
	for k, ds := range got {
		for i := matched[k]; i < len(ds); i++ {
			unexpected = append(unexpected, fmt.Sprintf("%s%s:%d: unexpected %s%s: %s", mode, ds[i].File, ds[i].Line, ds[i].Code, kind(k.extra), ds[i].Message))
		}
	}
	sort.Strings(unexpected)
	for _, u := range unexpected {
		t.Error(u)
	}
}

var (
	stubProgramOnce sync.Once
)

func newProgram(t *testing.T) *program.Program {
	t.Helper()
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	p.AddLoader(program.DirLoader{Prefix: "Acme\\", Dir: filepath.Join("testdata", "vendor", "Acme")})
	return p
}

func TestGolden(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.phn"))
	if len(files) == 0 {
		t.Fatal("no golden files")
	}
	only := os.Getenv("PHAENON_GOLDEN")
	for _, path := range files {
		name := filepath.Base(path)
		if only != "" && !strings.Contains(name, only) {
			continue
		}
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			wants := parseWants(string(src))
			for i := range wants {
				wants[i].file = path
			}
			// Twice, each time a program of its own: with the extra checks,
			// and without them.
			var both [2][]Diagnostic
			for i, extraChecks := range []bool{true, false} {
				p := newProgram(t)
				target, opts := goldenSetup(t, src)
				p.SetTarget(target)
				if _, err := p.AddFile(path, src, lexer.ModePhaenon, true); err != nil {
					t.Fatalf("parse: %v", err)
				}
				opts.NoExtraChecks = !extraChecks
				both[i] = New(p, opts).Check()
				holdToWants(t, wants, both[i], extraChecks)
			}
			lessTheExtraChecks(t, both[0], both[1])
		})
	}
}

// lessTheExtraChecks holds what a check returns with the extra checks off
// to what it returns with them on: the same findings, word for word and in
// their order, less the extra checks.
func lessTheExtraChecks(t *testing.T, on, off []Diagnostic) {
	t.Helper()
	var want, got []string
	for _, d := range on {
		if d.Kind != Extra {
			want = append(want, d.String())
		}
	}
	for _, d := range off {
		got = append(got, d.String())
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("with the extra checks off the findings are not the others as they were:\n got %q\nwant %q", got, want)
	}
}
