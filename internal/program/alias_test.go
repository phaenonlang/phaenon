package program

import (
	"fmt"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

// TestClassAliasCycle: class_alias() calls that name each other name no
// class, nor does a name aliased into such a cycle. Class() comes back from
// a cycle with nil, where it used to recurse until the stack ran out, and
// Canonical() with the name as given, the class it stands for being none.
func TestClassAliasCycle(t *testing.T) {
	p := New()
	src := `namespace X;
class Real {}
class_alias('X\A', 'X\B');
class_alias('X\B', 'X\A');
class_alias('X\A', 'X\Before');
class_alias('X\Real', 'X\First');
class_alias('X\First', 'X\Second');
`
	if _, err := p.AddFile("src/aliases.phn", []byte(src), lexer.ModePhaenon, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{`X\A`, `X\B`, `X\Before`} {
		if c := p.Class(name); c != nil {
			t.Errorf("Class(%s) = %s, want no class: the aliases form a cycle", name, c.Name)
		}
		if got := p.Canonical(name); got != name {
			t.Errorf("Canonical(%s) = %s, want the name as given", name, got)
		}
		if p.Exists(name) || p.IsFinal(name) || p.IsSubclassOf(name, `X\Real`) {
			t.Errorf("%s names no class", name)
		}
	}
	if p.IsSubclassOf(`X\Before`, `X\A`) || p.IsSubclassOf(`X\A`, `X\B`) {
		t.Error("names that stand for no class are no subclass of one another")
	}
	if c := p.Class(`X\Second`); c == nil || c.Name != `X\Real` {
		t.Errorf(`Class(X\Second) = %v, want X\Real through X\First`, c)
	}
}

// TestClassAliasChain: an alias of an alias is the class at the end of the
// chain, however many class_alias() steps it takes, as in PHP, and Class()
// and Canonical() follow it alike. Eight steps, and nine, are where a cap
// of eight turns once parted them: Class() spent a turn on the class at the
// end and gave up on the eighth step that Canonical() still followed.
func TestClassAliasChain(t *testing.T) {
	for _, steps := range []int{1, 7, 8, 9, 40} {
		t.Run(fmt.Sprintf("%d steps", steps), func(t *testing.T) {
			var src strings.Builder
			src.WriteString("namespace X;\nclass Real {}\n")
			last := `X\Real`
			for i := 1; i <= steps; i++ {
				alias := fmt.Sprintf(`X\A%d`, i)
				fmt.Fprintf(&src, "class_alias('%s', '%s');\n", last, alias)
				last = alias
			}
			p := New()
			if _, err := p.AddFile("src/aliases.phn", []byte(src.String()), lexer.ModePhaenon, true); err != nil {
				t.Fatal(err)
			}
			if c := p.Class(last); c == nil || c.Name != `X\Real` {
				t.Errorf(`Class(%s) = %v, want X\Real`, last, c)
			}
			if got := p.Canonical(last); got != `X\Real` {
				t.Errorf(`Canonical(%s) = %s, want X\Real`, last, got)
			}
			if !p.IsSubclassOf(last, `X\Real`) || !p.IsSubclassOf(`X\Real`, last) {
				t.Errorf(`%s and X\Real are one class`, last)
			}
		})
	}
}
