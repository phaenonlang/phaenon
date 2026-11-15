package parser

import (
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
)

// TestShape: a shape stands in the right-hand side of a `type`
// declaration, as the whole type or nested anywhere in it
// (design/collections.md §6.1).
func TestShape(t *testing.T) {
	src := `type Row = array{id: int, name?: string};
type Order = array{id: int, lines: list<array{sku: string, qty: int}>};
type Page<T> = array{items: list<T>, total: int, next?: string,};
type Keys = array{class: string, default?: int, list: bool, 'a-b': int, "plain"?: int, 0: int, -1: string, '7': float};
type Open = array{id: int, ...};
type Any = array{...,};
type None = array{};
type Nested = array{pair: (int, ?array{x: int}), f: Closure(array{a: int}): array{b: int}}|list<array{}>|null;
type Lines = array{
    id: int,
    name?: ?string,
};
`
	f, err := Parse([]byte(src), lexer.ModePhaenon)
	if err != nil {
		t.Fatal(err)
	}
	shape := func(i int) *ast.ShapeType {
		st, ok := f.Stmts[i].(*ast.TypeAlias).Type.(*ast.ShapeType)
		if !ok {
			t.Fatalf("statement %d: %T", i, f.Stmts[i].(*ast.TypeAlias).Type)
		}
		return st
	}
	row := shape(0)
	if len(row.Entries) != 2 || row.Unsealed || row.Entries[0].Name != "id" || row.Entries[0].Optional || row.Entries[1].Name != "name" || !row.Entries[1].Optional {
		t.Errorf("Row: %+v", row)
	}
	if got := src[row.S.Start:row.S.End]; got != "array{id: int, name?: string}" {
		t.Errorf("Row's span: %q", got)
	}
	if got := src[row.Entries[1].Key.Start:row.Entries[1].Key.End]; got != "name" {
		t.Errorf("the key's span: %q", got)
	}
	keys := shape(3)
	var got []string
	for _, e := range keys.Entries {
		switch {
		case e.IsInt:
			got = append(got, "int "+e.Raw)
		default:
			got = append(got, e.Name)
		}
	}
	if want := "class|default|list|a-b|plain|int 0|int -1|7"; strings.Join(got, "|") != want {
		t.Errorf("keys: %s, want %s", strings.Join(got, "|"), want)
	}
	if keys.Entries[6].Int != -1 || keys.Entries[3].Raw != "'a-b'" {
		t.Errorf("keys: %+v", keys.Entries)
	}
	if open := shape(4); !open.Unsealed || len(open.Entries) != 1 {
		t.Errorf("Open: %+v", open)
	}
	if any := shape(5); !any.Unsealed || len(any.Entries) != 0 {
		t.Errorf("Any: %+v", any)
	}
	if none := shape(6); none.Unsealed || len(none.Entries) != 0 {
		t.Errorf("None: %+v", none)
	}
	shapes := 0
	ast.Inspect(f.Stmts[7], func(n ast.Node) bool {
		if _, ok := n.(*ast.ShapeType); ok {
			shapes++
		}
		return true
	})
	if shapes != 5 {
		t.Errorf("Nested holds %d shapes, want 5", shapes)
	}
}

// TestShapeEntries: what a shape's entries may not be.
func TestShapeEntries(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"type T = array{int, string};", "every entry of a shape has a key"},
		{"type T = array{?int};", "every entry of a shape has a key"},
		{"type T = array{Foo\\Bar: int};", "every entry of a shape has a key"},
		{"type T = array{a-b: int};", "every entry of a shape has a key"},
		{"type T = array{..., id: int};", "`...` is a shape's last entry"},
		{"type T = array{id: int, ...<string, int>};", "the typed form, `...<K, V>`, is not read"},
		{"type T = array{\"a$b\": int};", "holds no interpolation"},
		{"type T = array{'it\\'s': int};", "holds no escape sequence"},
		{"type T = array{\"a\\n\": int};", "holds no escape sequence"},
		{"type T = array{0x10: int};", "written in decimal"},
		{"type T = array{007: int};", "written in decimal"},
		{"type T = array{1_000: int};", "written in decimal"},
		{"type T = list{int, string};", "list{…} stays docblock vocabulary"},
		{"type T = object{id: int};", "object{…} stays docblock vocabulary"},
		{"type T = array{id: int;", "expecting"},
		{"type T = array{id int};", "every entry of a shape has a key"},
	} {
		_, err := Parse([]byte(c.src), lexer.ModePhaenon)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want one saying %q", c.src, err, c.want)
		}
	}
	// A key that needs no quotes may have them, and a backslash that
	// escapes nothing is itself.
	if _, err := Parse([]byte(`type T = array{'App\Entity\User': int, "id": int};`), lexer.ModePhaenon); err != nil {
		t.Error(err)
	}
}

// TestShapeOutsideATypeDeclaration: in every other type position a shape
// is a syntax error whose message says to name it, the return type before
// a body included, where the brace is PHP's unless an entry follows it.
func TestShapeOutsideATypeDeclaration(t *testing.T) {
	for _, src := range []string{
		"function f(array{id: int} $row): void {}",
		"function f(?array{id: int} $row): void {}",
		"function f(int|array{} $row): void {}",
		"function f(): array{id: int} { return ['id' => 1]; }",
		"function f(): ?array{id: int} { return null; }",
		"function f(): array{id?: int} { return []; }",
		"function f(): array{'id': int} { return []; }",
		"function f(): array{0: int} { return [1]; }",
		"function f(): array{-1: int} { return []; }",
		"function f(): array{...} { return []; }",
		"function f(): array{} { return []; }",
		"function f(): array{id: int} => ['id' => 1];",
		"function f(): array{} => [];",
		"function f(): list<array{id: int}> { return []; }",
		"function f(): (int, array{id: int}) { return [1, ['id' => 1]]; }",
		"function f(): Closure(array{id: int}): void { return fn($r) => null; }",
		"function f(): Closure(): array{id: int} { return fn() => ['id' => 1]; }",
		"$f = function (): array{id: int} { return ['id' => 1]; };",
		"$f = fn(): array{id: int} => ['id' => 1];",
		"$f = fn(array{} $x) => $x;",
		"class C { private array{id: int} $row; }",
		"class C { const array{id: int} ROW = ['id' => 1]; }",
		"class C { find(int $id): array{id: int} { return ['id' => $id]; } }",
		"class C { find(int $id): array{id: int} => ['id' => $id]; }",
		"interface I { find(int $id): array{id: int}; }",
		"interface I { find(int $id): array{}; }",
		"class C { __construct(private array{id: int} $row) {} }",
		"function f(list<array{id: int}> $rows): void {}",
		"$x = make<array{id: int}>();",
		"class Box<T of array{id: int}> {}",
		"class Box<T = array{id: int}> {}",
		"type Page<T of array{id: int}> = list<T>;",
		"type Page<T = array{id: int}> = list<T>;",
	} {
		_, err := Parse([]byte(src), lexer.ModePhaenon)
		if err == nil || !strings.Contains(err.Error(), "a shape is written in a `type` declaration: name it") {
			t.Errorf("%s: error %v, want the one that says to name the shape", src, err)
		}
	}
	// The error is the shape's, on its line, whatever follows it.
	_, err := Parse([]byte("namespace A;\n\nfunction f(): array{id: int} {\n    return 1 +;\n}\n"), lexer.ModePhaenon)
	if pe, ok := err.(*Error); !ok || pe.Line != 3 || !strings.Contains(pe.Msg, "name it") {
		t.Errorf("a shape before another error: %v", err)
	}
	// A syntax error before the shape is the first one.
	_, err = Parse([]byte("function g( {}\n\nfunction f(): array{id: int} { return []; }\n"), lexer.ModePhaenon)
	if pe, ok := err.(*Error); !ok || pe.Line != 1 {
		t.Errorf("an error before a shape: %v", err)
	}
}

// TestBraceAfterArrayIsABody: `): array {` stays PHP's block wherever a
// shape's entry does not follow, in Phaenon as in PHP, and PHP mode reads
// no shape at all.
func TestBraceAfterArrayIsABody(t *testing.T) {
	for _, src := range []string{
		"function f(): array { return []; }",
		"function f(): array {}",
		"function f(): array {} function g(): array { return f(); }",
		"function f(): ?array { return null; }",
		"function f(): int|array { return 1; }",
		"function f(): array { FOO ?: fill(); return []; }",
		"function f(): array { static $cache = []; return $cache; }",
		"function f(): array { parent::f(); return []; }",
		"function f(): array { 'a' . 'b'; return []; }",
		"function f(): array { -1 + 2; return []; }",
		"function f(): array { [$a, $b] = [1, 2]; return [$a, $b]; }",
		"function f(): array { { return []; } }",
		"function f(): array { return match (true) { default => [] }; }",
		"function f(): array { switch (1) { case 1: default: return []; } }",
		"function f(): array { if (true): return []; endif; return []; }",
		"$f = function (): array { return []; };",
		"class C { f(): array { return []; } }",
		"class C { f(): array {} g(): array { return []; } }",
		"class C { public array $xs = [] { get => $this->xs; } }",
		"enum E: string { case A = 'a'; }",
		"type Named = array{id: int};\nfunction f(): Named { return ['id' => 1]; }",
	} {
		if _, err := Parse([]byte(src), lexer.ModePhaenon); err != nil {
			t.Errorf("%s: %v", src, err)
		}
	}
	for _, src := range []string{
		"<?php function f(): array { return []; }",
		"<?php function f(): array { retry: return []; }", // a label, in PHP
		"<?php function type(): array { $type = type(); return $type; }",
	} {
		if _, err := Parse([]byte(src), lexer.ModePHP); err != nil {
			t.Errorf("%s: %v", src, err)
		}
	}
	// PHP has no shapes: its error is PHP's own.
	if _, err := Parse([]byte("<?php function f(array{id: int} $x) {}"), lexer.ModePHP); err == nil || strings.Contains(err.Error(), "name it") {
		t.Errorf("PHP mode: %v", err)
	}
}

// TestTypeDeclarationTakesNoAttribute: an alias is erased, so an attribute
// on its declaration has nothing to stand on in the compiled PHP, where it
// would land on the declaration that comes next. It is a syntax error, at
// the attribute.
func TestTypeDeclarationTakesNoAttribute(t *testing.T) {
	for _, src := range []string{
		"#[\\Deprecated] type Old = int;",
		"namespace A;\n\n#[Tag]\n#[Other(1)]\ntype Row = array{id: int};\n",
		"#[A, B] type Page<T> = list<T>;",
	} {
		_, err := Parse([]byte(src), lexer.ModePhaenon)
		pe, ok := err.(*Error)
		if !ok || !strings.Contains(pe.Msg, "a `type` declaration takes no attribute") {
			t.Errorf("%q: error %v, want the one that says an alias takes no attribute", src, err)
			continue
		}
		if want := strings.Count(src[:strings.Index(src, "#[")], "\n") + 1; pe.Line != want {
			t.Errorf("%q: reported on line %d, want the attribute's, %d", src, pe.Line, want)
		}
	}
	// An attribute before a statement that only begins with the word.
	for _, src := range []string{
		"#[A] function type(): int { return 1; }",
		"$f = #[A] fn(int $type): int => $type;",
	} {
		if _, err := Parse([]byte(src), lexer.ModePhaenon); err != nil {
			t.Errorf("%q: %v", src, err)
		}
	}
}
