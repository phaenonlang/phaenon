package program

import (
	"os"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/types"
)

func TestStubs(t *testing.T) {
	p := New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	fn := p.Function("strlen")
	if fn == nil {
		t.Fatal("strlen missing")
	}
	fn.ensure()
	if got := types.Print(fn.Return); got != "int<0, max>" {
		t.Errorf("strlen returns %s (overlay should win)", got)
	}
	if len(fn.Params) != 1 || fn.Params[0].Name != "string" || types.Print(fn.Params[0].Type) != "string|bytes" {
		t.Errorf("strlen params %+v", fn.Params[0])
	}
	am := p.Function("\\array_map").Signature()
	if len(am.TypeParams) != 3 || types.Print(am.Return) != "($array is list ? list<R> : array<K, R>)" || types.Print(am.Params[0].Type) != "callable(V): R" {
		t.Errorf("array_map: %d params, returns %s, callback %s", len(am.TypeParams), types.Print(am.Return), types.Print(am.Params[0].Type))
	}
	rc := p.Class("ReflectionClass")
	if rc == nil {
		t.Fatal("ReflectionClass missing")
	}
	ni := rc.FindMethod("newInstance")
	if ni == nil || types.Print(ni.Return) != "T" || !ni.Params[0].Variadic {
		t.Errorf("ReflectionClass::newInstance %+v", ni)
	}
	if !rc.IsSubclassOf("Reflector") || !rc.IsSubclassOf("Stringable") {
		t.Error("ReflectionClass should implement Reflector and Stringable")
	}
	if p.Class("ArrayIterator") == nil || !p.IsSubclassOf("ArrayIterator", "Traversable") {
		t.Error("ArrayIterator should be Traversable")
	}
	if !p.IsFinal("Closure") {
		t.Error("Closure is final in php-src")
	}
	ex := p.Class("Exception")
	if ex == nil || ex.FindMethod("getMessage") == nil || ex.FindProperty("message") == nil {
		t.Error("Exception members")
	}
	if ex.FindProperty("message").Vis != Protected {
		t.Error("Exception::$message is protected")
	}
	if k := p.Constant("PHP_INT_MAX"); k == nil {
		t.Error("PHP_INT_MAX missing")
	}
	if k := p.Constant("E_ALL"); k == nil {
		t.Error("E_ALL missing")
	}
	be := p.Class("BackedEnum")
	if be == nil {
		t.Error("BackedEnum missing")
	}
	if m := p.Class("Countable").FindMethod("count"); m == nil || !m.Abstract {
		t.Error("Countable::count should be abstract")
	}
}

const fixture = `namespace App\Model;

use App\Contract\HasId as Identified;
use function strlen;

/**
 * @template T of object
 * @phpstan-type Row = array{id: int, name: string}
 */
abstract class Base implements Identified
{
    use Loggable { log as protected traced; }

    public const string KIND = 'base';
    protected const int LIMIT = 10;

    /** @var list<T> */
    protected array $items = [];

    public function __construct(
        public readonly int $id,
        private(set) string $name = '',
    ) {
    }

    /** @return T|null */
    public function first(): ?object { return $this->items[0] ?? null; }

    /**
     * @param Row $row
     * @return static
     */
    abstract public function hydrate(array $row): static;

    public int $virtual { get => $this->id * 2; }
    public int $backed { get => $this->backed; set => $this->backed = $value; }
}

trait Loggable
{
    public function log(string $msg): void {}
}

/** @extends Base<User> */
final class Users extends Base
{
    public function hydrate(array $row): static { return $this; }
}

final class User {}

enum Suit: string
{
    case Hearts = 'H';
    case Spades = 'S';
    const Wild = self::Spades;
}
`

const contract = `namespace App\Contract;

interface HasId
{
    public function first(): ?object;
}
`

func TestProjectModel(t *testing.T) {
	p := New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AddFile("src/Model/Base.phn", []byte(fixture), lexer.ModePhaenon, true); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AddFile("src/Contract/HasId.phn", []byte(contract), lexer.ModePhaenon, true); err != nil {
		t.Fatal(err)
	}
	base := p.Class("App\\Model\\Base")
	if base == nil {
		t.Fatal("Base missing")
	}
	base.ensure()
	if base.Parent != "" || len(base.Interfaces) != 1 || base.Interfaces[0] != "App\\Contract\\HasId" {
		t.Errorf("Base header: parent %q interfaces %v", base.Parent, base.Interfaces)
	}
	if len(base.TypeParams) != 1 || base.TypeParams[0].Name != "T" || types.Print(base.TypeParams[0].Bound) != "object" {
		t.Errorf("Base type params %+v", base.TypeParams)
	}
	if got := types.Print(base.Aliases["Row"]); got != "array{id: int, name: string}" {
		t.Errorf("Row alias %s", got)
	}
	items := base.FindProperty("items")
	if items == nil || types.Print(items.Type) != "list<T>" || items.Vis != Protected {
		t.Errorf("items %+v", items)
	}
	id := base.FindProperty("id")
	if id == nil || !id.Promoted || !id.Readonly || id.Vis != Public || types.Print(id.Type) != "int" {
		t.Errorf("promoted id %+v", id)
	}
	name := base.FindProperty("name")
	if name == nil || name.SetVis != Private || name.Vis != Public {
		t.Errorf("name set visibility %+v", name)
	}
	if v := base.FindProperty("virtual"); v == nil || !v.Virtual {
		t.Error("virtual property should be virtual")
	}
	if b := base.FindProperty("backed"); b == nil || b.Virtual {
		t.Error("backed property should not be virtual")
	}
	first := base.FindMethod("first")
	if first == nil || types.Print(first.Return) != "T|null" {
		t.Errorf("first %v", first)
	}
	hyd := base.FindMethod("hydrate")
	if hyd == nil || !hyd.Abstract || types.Print(hyd.Params[0].Type) != "array{id: int, name: string}" || types.Print(hyd.Return) != "static(App\\Model\\Base<T>)" {
		t.Errorf("hydrate %+v", hyd)
	}
	if k := base.FindConst("KIND"); k == nil || types.Print(k.Type) != "string" {
		t.Error("KIND")
	}
	log := base.FindMethod("log")
	if log == nil || log.FromTrait == nil || log.Vis != Public {
		t.Errorf("trait method log %+v", log)
	}
	if tr := base.FindMethod("traced"); tr == nil || tr.Vis != Protected {
		t.Errorf("trait alias traced %+v", tr)
	}
	users := p.Class("app\\model\\users")
	if users == nil || !users.IsSubclassOf("App\\Contract\\HasId") || !users.Final {
		t.Fatal("Users")
	}
	args, ok := users.SuperArgs("App\\Model\\Base", nil)
	if !ok || len(args) != 1 || types.Print(args[0]) != "App\\Model\\User" {
		t.Errorf("SuperArgs %v %v", args, ok)
	}
	if un := users.Unimplemented(); len(un) != 0 {
		t.Errorf("Users should implement everything, missing %v", un[0].Name)
	}
	if un := base.Unimplemented(); len(un) != 1 || un[0].Name != "hydrate" {
		t.Errorf("Base unimplemented %v", un)
	}
	suit := p.Class("App\\Model\\Suit")
	if suit == nil || suit.FindCase("Hearts") == nil || types.Print(suit.BackingType) != "string" || !suit.IsSubclassOf("BackedEnum") {
		t.Error("Suit enum")
	}
	// Name resolution in the file scope.
	f := p.File("src/Model/Base.phn")
	sc := f.ScopeAt(base.Span.Start)
	if sc.Namespace != "App\\Model" {
		t.Errorf("scope %q", sc.Namespace)
	}
	if got := sc.ResolveClassString("Identified"); got != "App\\Contract\\HasId" {
		t.Errorf("alias resolution %q", got)
	}
	if got := sc.ResolveClassString("\\Foo"); got != "Foo" {
		t.Errorf("fq resolution %q", got)
	}
	if got := sc.ResolveClassString("Other\\Thing"); got != "App\\Model\\Other\\Thing" {
		t.Errorf("qualified resolution %q", got)
	}
	line, col := f.Position(strings.Index(fixture, "abstract class"))
	if line != 10 || col != 1 {
		t.Errorf("position %d:%d", line, col)
	}
}

func TestComposer(t *testing.T) {
	dir := os.Getenv("PHAENON_COMPOSER_PROJECT")
	if dir == "" {
		t.Skip("PHAENON_COMPOSER_PROJECT not set")
	}
	l, err := FindComposer(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	p.AddLoader(l)
	c := p.Class("Symfony\\Component\\DependencyInjection\\ContainerBuilder")
	if c == nil {
		t.Fatal("ContainerBuilder not found through PSR-4")
	}
	if m := c.FindMethod("getDefinition"); m == nil || types.Print(m.Return) != "Symfony\\Component\\DependencyInjection\\Definition" {
		t.Errorf("getDefinition %+v", m)
	}
	if p.Function("trigger_deprecation") == nil {
		t.Error("trigger_deprecation from autoload_files missing")
	}
	t.Logf("%d problems while reading vendor", len(p.Problems))
	for i, pr := range p.Problems {
		if i > 10 {
			break
		}
		line, _ := pr.File.Position(pr.Span.Start)
		t.Logf("  %s:%d: %s", pr.File.Path, line, pr.Msg)
	}
}
