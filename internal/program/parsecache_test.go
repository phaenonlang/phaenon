package program

import (
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

// Two programs with one cache share the stubs' trees and a project file's
// unchanged tree; a changed text is parsed anew.
func TestParseCacheShares(t *testing.T) {
	cache := NewParseCache()
	a, b := New(), New()
	a.Parses, b.Parses = cache, cache
	for _, p := range []*Program{a, b} {
		if err := p.LoadStubs(); err != nil {
			t.Fatal(err)
		}
	}
	if a.File("<stub>/constants.php").AST != b.File("<stub>/constants.php").AST {
		t.Error("the stubs were parsed twice")
	}
	fa, _ := a.AddFile("/p/A.phn", []byte("final class A {}\n"), lexer.ModePhaenon, true)
	fb, _ := b.AddFile("/p/A.phn", []byte("final class A {}\n"), lexer.ModePhaenon, true)
	if fa.AST != fb.AST {
		t.Error("the same text was parsed twice")
	}
	c := New()
	c.Parses = cache
	fc, _ := c.AddFile("/p/A.phn", []byte("final class B {}\n"), lexer.ModePhaenon, true)
	if fc.AST == fa.AST || c.Class("B") == nil {
		t.Error("a changed text was not parsed anew")
	}
	if _, err := New().AddFile("/p/A.phn", []byte("class {"), lexer.ModePhaenon, true); err == nil {
		t.Error("a file that does not parse parsed")
	}
}
