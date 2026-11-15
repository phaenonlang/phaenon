package checker

import (
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/program"
)

// checkWords reports each word Phaenon adds to PHP that a .phn writes in
// another letter case than lower case, at the word (P1006,
// design/roadmap.md D30). The parser read it as the word, so this is the
// one finding the mistake gets, and phn fmt makes the rewrite its message
// names.
func (c *Checker) checkWords(f *program.File) {
	if f.AST == nil {
		return
	}
	for _, w := range parser.Words(f.AST, f.Src) {
		c.report(w.S, P1006, "`%s` is Phaenon's `%s`, written in lower case; phn fmt rewrites it", w.Text, w.Want)
	}
}
