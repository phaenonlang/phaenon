package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
)

// checkLifted checks sources as one program, with the vendor fixtures, and
// fails on an error of the checker's.
func checkLifted(t *testing.T, srcs map[string][]byte) {
	t.Helper()
	checked := map[string]bool{}
	for path := range srcs {
		checked[path] = true
	}
	p, err := pipeLoad(srcs, checked)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range checker.New(p, checker.Options{}).Check() {
		if d.Severity == checker.Error {
			t.Errorf("the lifted sources do not check: %s:%d: %s: %s", d.File, d.Line, d.Code, d.Message)
		}
	}
}

// The aliases fixture, lifted, is a program the checker accepts: the
// `type` declarations it wrote are found by the docblocks and signatures
// that name them, and the tags it kept are read as before.
func TestLiftedAliasesCheck(t *testing.T) {
	path := filepath.Join("testdata", "lift", "aliases.phn")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	checkLifted(t, map[string][]byte{path: src})
}

// An alias and its import are lifted together when their files are
// migrated together, each file deciding on its own lift from the model
// read before the run: the import becomes a `use` of the alias's
// namespace. Migrated alone, the importing file keeps its tag: the class
// it imports from still declares a @phpstan-type.
func TestLiftAliasesAcrossFiles(t *testing.T) {
	orders := `namespace App\Orders;

/**
 * @phpstan-type Row array{id: int, name?: string}
 * @phpstan-type Page array{items: list<Row>, total: int}
 */
final class Orders
{
    /**
     * @return Page
     */
    public function page(): array
    {
        return ['items' => [], 'total' => 0];
    }
}
`
	show := `namespace App\Http;

use App\Orders\Orders;

/**
 * @phpstan-import-type Row from Orders
 * @phpstan-import-type Page from Orders as Paged
 */
final class Show
{
    /**
     * @param Paged $page
     * @return Row|null
     */
    public function first(array $page): ?array
    {
        return $page['items'][0] ?? null;
    }
}
`
	const ordersPath, showPath = "src/Orders/Orders.phn", "src/Http/Show.phn"
	model := func() (*program.Program, map[string]*program.File) {
		p := program.New()
		if err := p.LoadStubs(); err != nil {
			t.Fatal(err)
		}
		files := map[string]*program.File{}
		for path, src := range map[string]string{ordersPath: orders, showPath: show} {
			f, err := p.AddFile(path, []byte(src), lexer.ModePhaenon, false)
			if err != nil {
				t.Fatal(err)
			}
			files[path] = f
		}
		return p, files
	}

	p, files := model()
	together := map[*program.File]bool{files[ordersPath]: true, files[showPath]: true}
	liftedOrders, notes, err := Lift([]byte(orders), Options{Generics: true, Program: p, File: files[ordersPath], Migrating: together})
	if err != nil || len(notes) != 0 {
		t.Fatalf("Orders: %v, notes %v", err, notes)
	}
	liftedShow, notes, err := Lift([]byte(show), Options{Generics: true, Program: p, File: files[showPath], Migrating: together})
	if err != nil || len(notes) != 0 {
		t.Fatalf("Show: %v, notes %v", err, notes)
	}
	for _, want := range []string{
		"type Row = array{id: int, name?: string};\ntype Page = array{items: list<Row>, total: int};\n\nfinal class Orders",
		"page(): Page",
	} {
		if !strings.Contains(string(liftedOrders), want) {
			t.Errorf("Orders lacks %q:\n%s", want, liftedOrders)
		}
	}
	for _, want := range []string{
		"use App\\Orders\\Orders;\nuse App\\Orders\\Row;\nuse App\\Orders\\Page as Paged;\n",
		"first(Paged $page): ?Row",
	} {
		if !strings.Contains(string(liftedShow), want) {
			t.Errorf("Show lacks %q:\n%s", want, liftedShow)
		}
	}
	if strings.Contains(string(liftedOrders)+string(liftedShow), "@phpstan-") {
		t.Errorf("a tag was left:\n%s\n%s", liftedOrders, liftedShow)
	}
	checkLifted(t, map[string][]byte{ordersPath: liftedOrders, showPath: liftedShow})

	// Alone, the importing file is not rewritten: lifting its imports would
	// name aliases that are still tags of Orders.
	p, files = model()
	alone, notes, err := Lift([]byte(show), Options{Generics: true, Program: p, File: files[showPath]})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(alone), "@phpstan-import-type Row from Orders") || strings.Contains(string(alone), "use App\\Orders\\Row;") {
		t.Errorf("Show, migrated alone, lost its import:\n%s", alone)
	}
	kept := 0
	for _, n := range notes {
		if strings.Contains(n.Msg, "kept @phpstan-import-type") && strings.Contains(n.Msg, "App\\Orders\\Orders is not among the files being migrated") {
			kept++
		}
	}
	if kept != 2 {
		t.Errorf("notes: %v", notes)
	}
	checkLifted(t, map[string][]byte{ordersPath: []byte(orders), showPath: alone})

	// And the other way: the declaring file migrated alone keeps the
	// aliases the other file still imports, and what names them, so that
	// the tags it leaves there still find their aliases.
	p, files = model()
	alone, notes, err = Lift([]byte(orders), Options{Generics: true, Program: p, File: files[ordersPath]})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(alone), "\ntype Row") || strings.Contains(string(alone), "\ntype Page") ||
		!strings.Contains(string(alone), "@phpstan-type Row array{id: int, name?: string}") {
		t.Errorf("Orders, migrated alone, lost an alias Show imports:\n%s", alone)
	}
	kept = 0
	for _, n := range notes {
		if strings.Contains(n.Msg, "kept @phpstan-type") && strings.Contains(n.Msg, "App\\Http\\Show imports it and is not among the files being migrated") {
			kept++
		}
	}
	if kept != 2 {
		t.Errorf("notes: %v", notes)
	}
	checkLifted(t, map[string][]byte{ordersPath: alone, showPath: []byte(show)})
}
