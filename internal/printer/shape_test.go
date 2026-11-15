package printer

import (
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// TestShape pins the printing of a shape in a `type` declaration
// (design/collections.md §6.1): the keys as written, `?` against an
// optional one, `...` last, and printing the printed text again changes
// nothing.
func TestShape(t *testing.T) {
	src := "type Row=array { id :int ,name ? :string };\n" +
		"type Page<T> = array{\n    items: list<T>,\n    total: int,\n    next?: ?string,\n};\n" +
		"type Keys = array{class: string, 'a-b': int, \"plain\"?: bool, 0: int, -1: string, '7': float, ...};\n" +
		"type Nested = array{pair: (int, array{x: int}), f: Closure(array{a: int}): array{b: int}}|list<array{}>|array{...}|null;\n"
	want := "type Row = array{id: int, name?: string};\n\n" +
		"type Page<T> = array{items: list<T>, total: int, next?: ?string};\n\n" +
		"type Keys = array{class: string, 'a-b': int, \"plain\"?: bool, 0: int, -1: string, '7': float, ...};\n\n" +
		"type Nested = array{pair: (int, array{x: int}), f: Closure(array{a: int}): array{b: int}}|list<array{}>|array{...}|null;\n"
	f, err := parser.Parse([]byte(src), lexer.ModePhaenon)
	if err != nil {
		t.Fatal(err)
	}
	got := File(f)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	again, err := parser.Parse([]byte(got), lexer.ModePhaenon)
	if err != nil {
		t.Fatal(err)
	}
	if File(again) != got {
		t.Errorf("printing the printed text changed it:\n%s", File(again))
	}
}
