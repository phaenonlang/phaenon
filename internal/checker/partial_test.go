package checker

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/phpversion"
)

// A partial application is written as such for PHP 8.6 and lowered to a
// closure for 8.5 (design/php-versions.md §5.3); its type is the same for
// either. partial-generic.phn is checked for 8.5 by TestGolden; checked for
// 8.6 it has the same findings, messages included.
func TestPartialGenericTargets(t *testing.T) {
	path := filepath.Join("testdata", "partial-generic.phn")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	findings := func(target phpversion.Version) []string {
		p := newProgram(t)
		p.SetTarget(target)
		if _, err := p.AddFile(path, src, lexer.ModePhaenon, true); err != nil {
			t.Fatalf("parse: %v", err)
		}
		var out []string
		for _, d := range New(p, Options{}).Check() {
			out = append(out, fmt.Sprintf("%d:%d: %s: %s", d.Line, d.Col, d.Code, d.Message))
			// A type parameter is named as declared, an open closure's
			// too: its identity for one call, the name with a NUL and a
			// number, is no text.
			if strings.ContainsRune(d.Message, 0) {
				t.Errorf("line %d: %s message holds a NUL: %q", d.Line, d.Code, d.Message)
			}
		}
		slices.Sort(out)
		return out
	}
	floor, next := findings(phpversion.Floor), findings(phpversion.PHP86)
	if len(floor) == 0 {
		t.Fatal("no findings: the file holds the types it expects as findings")
	}
	if !slices.Equal(floor, next) {
		t.Errorf("findings differ between the targets:\n8.5:\n%v\n8.6:\n%v", floor, next)
	}
}
