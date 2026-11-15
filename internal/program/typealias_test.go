package program

import (
	"os"
	"path/filepath"
	"strings"
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

// The aliases of sources outside the program are read on demand. A name
// under a prefix that holds Phaenon sources is loaded as a class is, from
// the file the loader gives it, which may be where its alias was put; a
// name that is neither a class nor an alias there reads the `type`
// declarations of the prefix's directory, parsing only the files that may
// declare one.
func TestTypeAliasReadOnDemand(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	box := write("Box.phn", "namespace Acme;\n\nfinal class Box {}\n")
	crate := write("Sub/Crate.phn", "namespace Acme\\Sub;\n\nfinal class Crate\n{\n    public string $type = 'x';\n}\n")
	aliases := write("Sub/shapes.phn", "namespace Acme\\Sub;\n\ntype Row = map<string, int>;\n\nfinal class Beside {}\n")
	broken := write("Sub/broken.phn", "namespace Acme\\Sub;\n\ntype Half = ;\n")
	write(".hidden/skipped.phn", "namespace Acme\\Hidden;\n\ntype Nope = int;\n")

	p := New()
	p.AddLoader(DirLoader{Prefix: "Acme\\", Dir: dir})
	f, err := p.AddFile("use.phn", []byte("namespace App;\n\nuse Acme\\Box;\nuse Acme\\Sub\\Row;\n"), lexer.ModePhaenon, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := types.Print(typeOf(t, p, f, "Box")); got != `Acme\Box` {
		t.Errorf("Box: %s", got)
	}
	if p.File(box) == nil || p.File(aliases) != nil {
		t.Error("a name that is a class loads its file, and reads no other")
	}
	if got := types.Print(typeOf(t, p, f, "Row")); got != "array<string, int>" {
		t.Errorf("Row: %s", got)
	}
	if p.File(aliases) == nil {
		t.Error("the file that declares the alias was not read")
	}
	if len(p.files) != 3 || p.File(broken) != nil || p.File(crate) != nil {
		t.Errorf("%d files held: beside the program's own, the class's and the one that declares an alias", len(p.files))
	}
	if p.Class(`Acme\Sub\Beside`) == nil {
		t.Error("the classes of the file read are declared with its aliases")
	}
	if p.TypeAlias(`Acme\Hidden\Nope`) != nil {
		t.Error("a directory Composer's maps would not serve was read")
	}
	if p.TypeAlias(`Other\Row`) != nil {
		t.Error("a name outside the loader's prefix found an alias")
	}
}

// An alias is found whatever its file is called, and whichever name is
// met first: in the file PSR-4 gives its name, where a class would be, in
// a file named in another letter case, and in a file named after nothing.
func TestTypeAliasInTheFileOfItsName(t *testing.T) {
	sources := map[string]string{
		"Orders/Row.phn":   "namespace Acme\\Orders;\n\ntype Row = array<int>;\n",
		"Orders/plain.phn": "namespace Acme\\Orders;\n\ntype Plain = list<int>;\n",
		"Orders/types.phn": "namespace Acme\\Orders;\n\ntype Page = (Row, int);\n",
		"Orders/Odd.phn":   "namespace Acme\\Orders;\n\nfinal class Even {}\n\ntype Elsewhere = int;\n",
		"Orders/Else.phn":  "namespace Acme\\Orders;\n\ntype Odd = string;\n",
	}
	want := map[string]string{
		"Row": "array<int>", "Plain": "list<int>", "Page": "list{array<int>, int}",
		"Odd":       "string", // its own file declares neither a class nor an alias of the name
		"Elsewhere": "int",
	}
	orders := [][]string{
		{"Row", "Plain", "Page", "Odd", "Elsewhere"},
		{"Elsewhere", "Odd", "Page", "Plain", "Row"},
		{"Page", "Row", "Odd", "Plain", "Elsewhere"},
		{"Plain", "Odd", "Row", "Elsewhere", "Page"},
	}
	for _, order := range orders {
		dir := t.TempDir()
		for name, src := range sources {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		p := New()
		p.AddLoader(DirLoader{Prefix: "Acme\\", Dir: dir})
		f, err := p.AddFile("use.phn", []byte("namespace Acme\\Orders;\n"), lexer.ModePhaenon, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range order {
			if got := types.Print(typeOf(t, p, f, name)); got != want[name] {
				t.Errorf("in the order %v, %s: %s, want %s", order, name, got, want[name])
			}
		}
	}
}

// declaresType is the scan that decides which sources are parsed for their
// aliases: it may report a file that declares none, never miss one that
// does.
func TestDeclaresType(t *testing.T) {
	for src, want := range map[string]bool{
		"type Row = int;":                             true,
		"namespace A;\n\ntype Page<T> = list<T>;":     true,
		"type\n    Row\n    = int;":                   true,
		"type Row=int;":                               true,
		"type /* the row */ Commented = int;":         true,
		"type/**/Tight/**/= int;":                     true,
		"type Commented2 /* why */ = int;":            true,
		"type // the row\n    Commented3 = int;":      true,
		"type # the row\n    Commented4 # why\n = 1;": true,
		"Type Upper = int;":                           true,
		"TYPE Shout<T> = list<T>;":                    true,
		"type Page /* of */ <T> = list<T>;":           true,
		"type type = int;":                            true,
		"}type After = int;":                          true,
		"// the type Row = what a row is":             true, // a parse, and no more
		"$type = 1;":                                  false,
		"$this->type = $x;":                           false,
		"public string $type = 'x';":                  false,
		"function type(): int { return 1; }":          false,
		"const TYPE = 1; $a = TYPE;":                  false,
		"return $subtype == $type;":                   false,
		"type = 3;":                                   false,
		"prototype Foo = 1;":                          false,
		"type #[A] Foo = 1;":                          false, // an attribute is no comment, and no declaration has one there
		"type /* never closed":                        false,
		"":                                            false,
		"type":                                        false,
	} {
		if got := declaresType([]byte(src)); got != want {
			t.Errorf("%q: %v, want %v", src, got, want)
		}
	}
}

// The scan is held to the parser: every source of the checker's and the
// emitter's corpora that the parser reads a `type` declaration in is
// reported, as written and with each gap between the declaration's first
// tokens respelled as the lexer allows — a comment of each kind, no blank
// at all where a comment separates the tokens — and the keyword in another
// letter case.
func TestDeclaresTypeAgainstTheParser(t *testing.T) {
	var files []string
	for _, pattern := range []string{"../checker/testdata/*.phn", "../checker/testdata/programs/*/*/*.phn", "../emit/testdata/*.phn", "../emit/testdata/programs/*/*/*.phn", "../format/testdata/*.phn"} {
		m, _ := filepath.Glob(pattern)
		files = append(files, m...)
	}
	declarations := 0
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		tree, err := parser.Parse(src, lexer.ModePhaenon)
		if err != nil {
			continue
		}
		var aliases []*ast.TypeAlias
		ast.Inspect(tree, func(n ast.Node) bool {
			if a, ok := n.(*ast.TypeAlias); ok {
				aliases = append(aliases, a)
			}
			return true
		})
		if len(aliases) == 0 {
			continue
		}
		if !declaresType(src) {
			t.Errorf("%s declares a type the scan does not see", path)
		}
		for _, a := range aliases {
			declarations++
			// The declaration alone, from its keyword: `type`, the gap,
			// the name, and the rest.
			text := string(src[a.S.Start:a.S.End])
			kw := strings.Index(strings.ToLower(text), "type")
			name := a.Name.S.Start - a.S.Start
			if kw < 0 || name <= kw+4 {
				t.Fatalf("%s: no keyword before the name in %q", path, text)
			}
			rest := text[a.Name.S.End-a.S.Start:]
			for _, word := range []string{"type", "Type", "TYPE", "tYpE"} {
				for _, gap := range []string{" ", "\n\t", "/**/", " /* c */ ", "// c\n", " # c\r\n  ", "/* a */ // b\n /** c */"} {
					for _, after := range []string{"", " ", "/* d */", " // e\n"} {
						variant := text[:kw] + word + gap + a.Name.Name + after + strings.TrimLeft(rest, " \t\r\n")
						if _, err := parser.Parse([]byte(variant), lexer.ModePhaenon); err != nil {
							t.Fatalf("%s: the variant does not parse (%v): %q", path, err, variant)
						}
						if !declaresType([]byte(variant)) {
							t.Errorf("%s: the scan misses %q", path, variant)
						}
					}
				}
			}
		}
	}
	if declarations < 40 {
		t.Errorf("only %d declarations in the corpora: the comparison needs more", declarations)
	}
}
