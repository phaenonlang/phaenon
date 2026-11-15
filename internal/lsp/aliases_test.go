package lsp

import (
	"strings"
	"testing"
)

const aliasTypesSrc = `namespace App\Orders;

/** A row of the orders table. */
type Row = map<string, int|string>;
type Page<T, N of int = int> = (list<T>, N);
`

const aliasUserSrc = `namespace App\Http;

use App\Orders\Row;

function show(Row $row, \App\Orders\Page<Row> $page): int {
    return count($row) + $page[1];
}
`

// A type alias is found from any file by its qualified name: hover prints
// the type it stands for, and go to definition leads to its declaration.
func TestAliasHoverAndDefinition(t *testing.T) {
	c := start(t, map[string]string{"src/Orders/Types.phn": aliasTypesSrc, "src/Http/show.phn": aliasUserSrc}, nil)
	c.open("src/Http/show.phn", aliasUserSrc)
	c.diagnostics("src/Http/show.phn", 1)
	hovers := []struct {
		line, char int
		want       string
	}{
		{4, 15, `type App\Orders\Row = map<string, int|string>`},          // the parameter's type
		{4, 15, "A row of the orders table."},                             // its docblock
		{2, 17, `type App\Orders\Row = map<string, int|string>`},          // its import
		{4, 37, `type App\Orders\Page<T, N of int = int> = (list<T>, N)`}, // a qualified name
	}
	for _, h := range hovers {
		if got := hoverText(c.at("textDocument/hover", "src/Http/show.phn", h.line, h.char)); !strings.Contains(got, h.want) {
			t.Errorf("hover at %d:%d = %q, want %q", h.line, h.char, got, h.want)
		}
	}
	for _, at := range [][2]int{{4, 15}, {2, 17}, {4, 43}} {
		def, _ := c.at("textDocument/definition", "src/Http/show.phn", at[0], at[1]).(map[string]any)
		if def["uri"] != c.uri("src/Orders/Types.phn") || jsonOf(def["range"]) != `{"end":{"character":8,"line":3},"start":{"character":5,"line":3}}` {
			t.Errorf("definition of Row at %d:%d: %s", at[0], at[1], jsonOf(def))
		}
	}
	// The declaration's own name is the alias too.
	c.open("src/Orders/Types.phn", aliasTypesSrc)
	c.diagnostics("src/Orders/Types.phn", 1)
	if got := hoverText(c.at("textDocument/hover", "src/Orders/Types.phn", 4, 6)); !strings.Contains(got, `type App\Orders\Page<T`) {
		t.Errorf("hover on the declaration: %q", got)
	}
}

// hoverText is the markdown of a hover's answer, "" without one.
func hoverText(res any) string {
	m, _ := res.(map[string]any)
	contents, _ := m["contents"].(map[string]any)
	text, _ := contents["value"].(string)
	return text
}

// Completion offers an alias with its import, as it offers a class.
func TestAliasCompletion(t *testing.T) {
	c := start(t, map[string]string{"src/Orders/Types.phn": aliasTypesSrc, "src/Http/show.phn": aliasUserSrc}, nil)
	src := "namespace App\\Http;\n\nfunction pages(Pa $p): void {}\n"
	c.open("src/Http/pages.phn", src)
	res := c.at("textDocument/completion", "src/Http/pages.phn", 2, 17).(map[string]any)
	var item map[string]any
	for _, it := range res["items"].([]any) {
		if m := it.(map[string]any); m["label"] == "Page" {
			item = m
		}
	}
	if item == nil {
		t.Fatalf("no alias among the items: %s", jsonOf(res))
	}
	if detail, _ := item["detail"].(string); !strings.HasPrefix(detail, `type App\Orders\Page<T, N of int = int> = `) {
		t.Errorf("detail: %q", detail)
	}
	if edits := jsonOf(item["additionalTextEdits"]); !strings.Contains(edits, `use App\\Orders\\Page;`) {
		t.Errorf("the import of Page: %s", edits)
	}
}
