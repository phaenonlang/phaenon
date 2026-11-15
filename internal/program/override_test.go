package program

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/types"
)

// TestOverridden: the declarations a method is held to are the one in
// its parent's method table, as PHP fills it (the class's own, a concrete
// trait method, the inherited one, a trait's abstract one, then the first
// interface that has it, farthest class first), then the methods of the
// class's immediate interfaces in the order declared, each its own or
// inherited; a constructor's are the abstract one the parent's table
// holds or implements, and every interface's.
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
interface Wide<T> { take(T $x): void; }
interface Narrow { take(int $x): void; }
abstract class G implements Wide<int> {}
abstract class P extends G implements Narrow {}
class BelowBoth extends P { take(int $x): void {} }
trait Needs { abstract take(int $x): void; }
abstract class NeedsMid extends Base<int> { use Needs; }
class BelowNeeds extends NeedsMid { take(int $x): void {} }
abstract class OnlyNeeds { use Needs; }
class BelowOnlyNeeds extends OnlyNeeds implements Narrow { take(int $x): void {} }
trait Gives { take(int $x): void {} }
abstract class NeedsAndGives { use Needs, Gives; }
class BelowGives extends NeedsAndGives { take(int $x): void {} }
interface A0 { take(int $x): void; }
interface A extends A0 {}
interface B { take(int $x): void; }
class ImplementsAB implements A, B { take(int $x): void {} }
class ImplementsBA implements B, A { take(int $x): void {} }
class ImplementsA0A implements A0, A { take(int $x): void {} }
class Quiet { take(int $x): void {} }
class QuietTakes extends Quiet implements Takes {}
class RepeatsTakes extends QuietTakes implements Takes { take(int $x): void {} }
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
		{`X\BelowBoth`, "take", []string{`X\Wide::take`}},
		{`X\BelowNeeds`, "take", []string{`X\Base::take`}},
		{`X\BelowOnlyNeeds`, "take", []string{`X\OnlyNeeds::take`, `X\Narrow::take`}},
		{`X\BelowGives`, "take", []string{`X\Gives::take`}},
		{`X\ImplementsAB`, "take", []string{`X\A0::take`, `X\B::take`}},
		{`X\ImplementsBA`, "take", []string{`X\B::take`, `X\A0::take`}},
		{`X\ImplementsA0A`, "take", []string{`X\A0::take`}},
		{`X\RepeatsTakes`, "take", []string{`X\Quiet::take`}},
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

// TestErasedUnionsLoad: the parameter the rule widens to accepts both
// types it is made of, and php compiles it as written, whatever pair of
// types meets: none of the unions PHP refuses as redundant (iterable is
// Traversable|array to PHP; object beside a class; mixed beside anything;
// bool beside true or false; a type twice).
func TestErasedUnionsLoad(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php on PATH")
	}
	p := New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	decls := `namespace X;
class Foo {}
class Sub extends Foo {}
interface Iface {}
final class Name implements \Stringable { __toString(): string => 'name'; }
`
	if _, err := p.AddFile("src/unions.phn", []byte(decls), lexer.ModePhaenon, true); err != nil {
		t.Fatal(err)
	}
	class := types.NewClass
	values := []types.Type{
		types.TInt, types.TString, types.TFloat, types.TBool, types.TTrue, types.TFalse, types.TNull,
		types.TArray, types.TIterable, types.TObject, types.TCallable,
		class("Traversable"), class("ArrayIterator"), class("Countable"), class("Stringable"),
		class(`X\Foo`), class(`X\Sub`), class(`X\Iface`), class(`X\Name`),
		types.MakeUnion(class(`X\Foo`), types.TNull), types.MakeUnion(types.TInt, types.TString),
		types.MakeUnion(class("Traversable"), types.TInt), types.MakeUnion(types.TArray, types.TString),
		types.MakeUnion(types.TIterable, types.TInt), types.MakeUnion(types.TBool, types.TNull),
		types.MakeUnion(types.TFalse, types.TInt), types.MakeUnion(types.TObject, types.TInt),
	}
	var src strings.Builder
	src.WriteString("<?php\nnamespace X;\nclass Foo {}\nclass Sub extends Foo {}\ninterface Iface {}\nfinal class Name implements \\Stringable { public function __toString(): string { return 'name'; } }\n")
	var lines []string
	emit := func(what, decl string) {
		src.WriteString(decl + "\n")
		lines = append(lines, what)
	}
	n := 0
	for _, a := range values {
		for _, b := range values {
			if types.IsSubtype(b, a, p) {
				continue // the rule leaves a parameter that accepts b alone
			}
			w := widenParam(a, b, p)
			if !types.IsSubtype(a, w, p) || !types.IsSubtype(b, w, p) {
				t.Errorf("parameter %s widened for %s to %s, which refuses one of them", types.Print(a), types.Print(b), types.Print(w))
			}
			s, ok := types.Native(w)
			if !ok {
				t.Errorf("parameter %s widened for %s to %s, which PHP cannot spell", types.Print(a), types.Print(b), types.Print(w))
				continue
			}
			n++
			emit(types.Print(a)+" widened for "+types.Print(b), fmt.Sprintf("function p%d(%s $x): void {}", n, s))
		}
	}
	src.WriteString("echo 'loaded';\n")
	file := filepath.Join(t.TempDir(), "unions.php")
	if err := os.WriteFile(file, []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(php, "-n", "-d", "display_errors=1", file).CombinedOutput()
	if err == nil && string(out) == "loaded" {
		return
	}
	// Name the declaration php stopped at: the prelude is 6 lines.
	msg := string(out)
	if m := regexp.MustCompile(`on line (\d+)`).FindStringSubmatch(msg); m != nil {
		if line, _ := strconv.Atoi(m[1]); line > 6 && line-7 < len(lines) {
			t.Fatalf("php refuses the type of %s:\n%s", lines[line-7], msg)
		}
	}
	t.Fatalf("php: %v\n%s", err, msg)
}
