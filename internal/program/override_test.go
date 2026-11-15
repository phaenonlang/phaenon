package program

import (
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/types"
)

// TestOverridden: the declarations a method is held to are the one in
// its parent's method table, the nearest up the parent chain however far
// it is (an interface's only when no class declares the method), then the
// methods of the interfaces its class implements itself; a constructor's
// are the nearest abstract one up the chain and every interface's.
func TestOverridden(t *testing.T) {
	p := New()
	src := `namespace X;
class Base<T> { take(T $x): void {} }
class Mid extends Base<int> {}
interface Takes { take(int $x): void; }
interface MoreTakes extends Takes {}
abstract class Impl extends Mid implements Takes {}
class Leaf extends Impl { take(int $x): void {} }
class TakesItself extends Mid implements MoreTakes { take(int $x): void {} }
abstract class OnlyInterface implements Takes {}
class FromInterface extends OnlyInterface { take(int $x): void {} }
class Hides { private take(int $x): void {} }
class AfterPrivate extends Hides { take(int $x): void {} }
class AfterPrivateTakes extends Hides implements Takes { take(int $x): void {} }
abstract class Command { abstract __construct(int $x); }
class Greet extends Command { __construct(int $x) {} }
class LoudGreet extends Greet { __construct(int $x) { parent::__construct($x); } }
class Plain { __construct(int $x) {} }
class AfterPlain extends Plain { __construct(string $x) {} }
interface Made { __construct(); }
class MadeBase implements Made { __construct() {} }
class MadeLeaf extends MadeBase { __construct() {} }
`
	if _, err := p.AddFile("src/override.phn", []byte(src), lexer.ModePhaenon, true); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		class, method string
		want          []string
	}{
		{`X\Leaf`, "take", []string{`X\Base::take`}},
		{`X\TakesItself`, "take", []string{`X\Base::take`, `X\Takes::take`}},
		{`X\FromInterface`, "take", []string{`X\Takes::take`}},
		{`X\AfterPrivate`, "take", nil},
		{`X\AfterPrivateTakes`, "take", []string{`X\Takes::take`}},
		{`X\Greet`, "__construct", []string{`X\Command::__construct`}},
		{`X\LoudGreet`, "__construct", []string{`X\Command::__construct`}},
		{`X\AfterPlain`, "__construct", nil},
		{`X\MadeLeaf`, "__construct", []string{`X\Made::__construct`}},
	}
	for _, c := range cases {
		cl := p.Class(c.class)
		if cl == nil {
			t.Fatalf("no class %s", c.class)
		}
		m := cl.FindMethod(c.method)
		if m == nil || m.Class != cl {
			t.Fatalf("%s declares no %s()", c.class, c.method)
		}
		var got []string
		for _, d := range m.Overridden() {
			got = append(got, d.Class.Name+"::"+d.Name)
		}
		if strings.Join(got, ", ") != strings.Join(c.want, ", ") {
			t.Errorf("%s::%s() overrides %v, want %v", c.class, c.method, got, c.want)
		}
	}
}

// TestErasureAt: the rule widens a parameter until it accepts what every
// overridden declaration accepts, the parent's and the class's own
// interfaces' alike, and narrows a return until it is within each one's.
func TestErasureAt(t *testing.T) {
	p := New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	src := `namespace X;
final class Name implements \Stringable { __toString(): string => 'name'; }
class Labels<T of \Stringable> { add(T $label): void {} }
interface Counts { add(int|Name $label): void; }
final class NameCounts extends Labels<Name> implements Counts { add(int|Name $label): void {} }
class Source<T> { get(): T { throw new \LogicException(); } }
interface Ints { get(): int; }
final class IntSource extends Source<int> implements Ints { get(): int => 1; }
`
	if _, err := p.AddFile("src/erasure.phn", []byte(src), lexer.ModePhaenon, true); err != nil {
		t.Fatal(err)
	}
	add := p.Class(`X\NameCounts`).FindMethod("add")
	got, ok := add.ErasureAt(0, types.MakeUnion(types.TInt, types.NewClass(`X\Name`)))
	if s, _ := types.Native(got); !ok || s != `int|\Stringable` {
		t.Errorf("NameCounts::add() widens int|Name to %q (%v), want int|\\Stringable", s, ok)
	}
	if _, ok := add.ErasureAt(0, types.MakeUnion(types.TInt, types.NewClass("Stringable"))); ok {
		t.Error("int|Stringable meets both declarations and is left alone")
	}
	get := p.Class(`X\IntSource`).FindMethod("get")
	got, ok = get.ErasureAt(-1, types.TMixed)
	if s, _ := types.Native(got); !ok || s != "int" {
		t.Errorf("IntSource::get() narrows mixed to %q (%v), want int", s, ok)
	}
}
