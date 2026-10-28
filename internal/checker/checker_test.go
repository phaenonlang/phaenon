package checker

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
)

// Golden corpus: every testdata/*.phn is checked on its own, with
// testdata/vendor as a PSR-4 root for the Acme\ namespace. Expected
// diagnostics are `// want P2103` comments on the offending line (several
// codes separated by commas; an optional "substring" must appear in the
// message). The set of (line, code) pairs must match exactly.

var reWant = regexp.MustCompile(`//\s*want\s+(.*)$`)
var reWantItem = regexp.MustCompile(`(P\d{4})(?:\s+"([^"]*)")?`)

type want struct {
	line int
	code string
	msg  string
}

func parseWants(src string) []want {
	var out []want
	for i, line := range strings.Split(src, "\n") {
		m := reWant.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		for _, item := range reWantItem.FindAllStringSubmatch(m[1], -1) {
			out = append(out, want{line: i + 1, code: item[1], msg: item[2]})
		}
	}
	return out
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
			p := newProgram(t)
			if _, err := p.AddFile(path, src, lexer.ModePhaenon, true); err != nil {
				t.Fatalf("parse: %v", err)
			}
			diags := New(p, Options{}).Check()
			wants := parseWants(string(src))
			type key struct {
				line int
				code string
			}
			got := map[key][]Diagnostic{}
			for _, d := range diags {
				k := key{d.Line, d.Code}
				got[k] = append(got[k], d)
			}
			matched := map[key]int{}
			for _, w := range wants {
				k := key{w.line, w.code}
				ds := got[k]
				if matched[k] >= len(ds) {
					t.Errorf("line %d: expected %s, not reported", w.line, w.code)
					continue
				}
				d := ds[matched[k]]
				matched[k]++
				if w.msg != "" && !strings.Contains(d.Message, w.msg) {
					t.Errorf("line %d: %s message %q does not contain %q", w.line, w.code, d.Message, w.msg)
				}
			}
			var unexpected []string
			for k, ds := range got {
				for i := matched[k]; i < len(ds); i++ {
					unexpected = append(unexpected, fmt.Sprintf("line %d: unexpected %s: %s", ds[i].Line, ds[i].Code, ds[i].Message))
				}
			}
			sort.Strings(unexpected)
			for _, u := range unexpected {
				t.Error(u)
			}
		})
	}
}
