package program

import (
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/types"
)

// typeOf resolves a type written in file f, in its first namespace.
func typeOf(t *testing.T, p *Program, f *File, src string) types.Type {
	t.Helper()
	tree, err := parser.Parse([]byte("function f("+src+" $x) {}"), lexer.ModePhaenon)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	te := tree.Stmts[0].(*ast.FunctionDecl).Params[0].Type
	return p.NewResolver(f, f.ScopeAt(len(f.Src)-1), nil, ast.Span{}).Native(te)
}

// A type alias is a name of its namespace: found from another file by its
// qualified name or through an import, whatever the case, and declared
// once, the first declaration standing.
func TestTypeAliasIsItsNamespaces(t *testing.T) {
	p := New()
	add := func(path, src string) *File {
		f, err := p.AddFile(path, []byte(src), lexer.ModePhaenon, true)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	add("types.phn", "namespace App\\Orders;\n\ntype Row = map<string, int>;\ntype Page<T, N = int> = (list<T>, N);\ntype Tree = list<Tree>;\ntype Ping = list<Pong>;\ntype Pong = ?Ping;\ntype Wood = list<Tree>;\n")
	add("again.phn", "namespace App\\Orders;\n\ntype ROW = string;\n")
	same := add("same.phn", "namespace App\\Orders;\n")
	other := add("other.phn", "namespace App\\Http;\n\nuse App\\Orders\\Row;\nuse App\\Orders\\{Page as Paged};\nuse App\\Orders;\n")
	php, err := p.AddFile("plain.php", []byte("<?php\nnamespace App\\Orders;\n"), lexer.ModePHP, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		f         *File
		src, want string
	}{
		{same, "Row", "array<string, int>"},
		{same, "row", "array<string, int>"},
		{same, `\App\Orders\Row`, "array<string, int>"},
		{same, `namespace\Row`, "array<string, int>"},
		{same, "Page<Row>", "list{list<array<string, int>>, int}"},
		{other, "Row", "array<string, int>"},
		{other, "Paged<int>", "list{list<int>, int}"},
		{other, `Orders\Row`, "array<string, int>"},
		{other, "Page", `App\Http\Page`}, // no import: a class of this namespace, which does not exist
		{php, "Row", `App\Orders\Row`},   // PHP has no aliases: a class, as PHP itself would look for
	} {
		if got := types.Print(typeOf(t, p, c.f, c.src)); got != c.want {
			t.Errorf("%s in %s: %s, want %s", c.src, c.f.Path, got, c.want)
		}
	}
	if a := p.TypeAlias(`app\orders\row`); a == nil || a.Name != `App\Orders\Row` || a.File.Path != "types.phn" {
		t.Errorf("the first declaration of Row stands: %+v", a)
	}
	for name, cyclic := range map[string]bool{"Tree": true, "Ping": true, "Pong": true, "Wood": false, "Row": false} {
		a := p.TypeAlias(`App\Orders\` + name)
		if a == nil || a.Cyclic != cyclic {
			t.Errorf("%s: cyclic %v, want %v", name, a != nil && a.Cyclic, cyclic)
		}
	}
	if got := types.Print(p.TypeAlias(`App\Orders\Wood`).Type); got != "list<list>" {
		t.Errorf("an alias's own mention reads as mixed: %s", got)
	}
}
