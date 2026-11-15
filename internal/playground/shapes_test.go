package playground

import (
	"strings"
	"testing"
)

// The playground follows the parser (design/collections.md §6): a shape is
// read in a `type` declaration, an alias of a support file is found from
// the main one through its import, and a shape written anywhere else is
// the syntax error that says to name it.
func TestNamedShapes(t *testing.T) {
	files := map[string]string{
		"main.phn":      "namespace App;\n\nuse App\\Orders\\Row;\n\nfunction name(Row $row): string => $row['name'] ?? 'nobody';\n",
		"lib/types.phn": "namespace App\\Orders;\n\ntype Row = array{id: int, name?: string};\n",
	}
	res := Compile(Request{Files: files, Main: "main.phn"})
	if !res.OK || len(res.Findings) != 0 {
		t.Fatalf("ok %v, findings %v", res.OK, res.Findings)
	}
	for _, want := range []string{"@param array{id: int, name?: string} $row", "function name(array $row): string"} {
		if !strings.Contains(res.PHP, want) {
			t.Errorf("output lacks %q:\n%s", want, res.PHP)
		}
	}
	if strings.Contains(res.PHP, "use App\\Orders\\Row;") {
		t.Errorf("the alias's import reached the output:\n%s", res.PHP)
	}
	res = Compile(Request{Files: map[string]string{"main.phn": "namespace App;\n\nfunction find(int $id): array{id: int} {\n    return ['id' => $id];\n}\n"}, Main: "main.phn"})
	if res.OK || len(res.Findings) != 1 || res.Findings[0].Code != "P1000" || res.Findings[0].Line != 3 ||
		!strings.Contains(res.Findings[0].Message, "a shape is written in a `type` declaration: name it") {
		t.Errorf("a shape in a return type: ok %v, findings %+v", res.OK, res.Findings)
	}
}
