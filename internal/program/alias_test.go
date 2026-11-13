package program

import (
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

// TestClassAliasCycle: two class_alias() calls that name each other name no
// class. Class() follows an alias of an alias and comes back from a cycle
// with nothing, as Canonical() does, where it used to recurse until the
// stack ran out.
func TestClassAliasCycle(t *testing.T) {
	p := New()
	src := `namespace X;
class Real {}
class_alias('X\A', 'X\B');
class_alias('X\B', 'X\A');
class_alias('X\Real', 'X\First');
class_alias('X\First', 'X\Second');
`
	if _, err := p.AddFile("src/aliases.phn", []byte(src), lexer.ModePhaenon, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{`X\A`, `X\B`} {
		if c := p.Class(name); c != nil {
			t.Errorf("Class(%s) = %s, want no class: the aliases form a cycle", name, c.Name)
		}
		if p.Exists(name) || p.IsFinal(name) || p.IsSubclassOf(name, `X\Real`) {
			t.Errorf("%s names no class", name)
		}
	}
	if c := p.Class(`X\Second`); c == nil || c.Name != `X\Real` {
		t.Errorf(`Class(X\Second) = %v, want X\Real through X\First`, c)
	}
}
