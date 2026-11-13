package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
)

// phaenon.json's unused.reflectedAttributes: a missing key keeps the
// checker's default (nil), an empty list is kept as one, and names are
// checked to be class names.
func TestReadProjectReflectedAttributes(t *testing.T) {
	for _, c := range []struct {
		json string
		want []string // nil: the default
		err  string
	}{
		{json: `{"roots": {"src": ".src"}}`},
		{json: `{"roots": {"src": ".src"}, "unused": {}}`},
		{json: `{"roots": {"src": ".src"}, "unused": {"reflectedAttributes": []}}`, want: []string{}},
		{json: `{"unused": {"reflectedAttributes": ["App\\Mapping", "\\Doctrine\\ORM\\Mapping\\MappingAttribute"]}}`,
			want: []string{`App\Mapping`, `\Doctrine\ORM\Mapping\MappingAttribute`}},
		{json: `{"unused": {"reflectedAttributes": ["App\\"]}}`, err: `"App\\" is not one`},
		{json: `{"unused": {"reflectedAttributes": ["App/Mapping"]}}`, err: "is not one"},
		{json: `{"unused": {"reflected": []}}`, err: `unknown key "reflected" (this is phn ` + version + `; a newer release may know it)`},
	} {
		path := filepath.Join(t.TempDir(), ProjectFile)
		writeTree(t, filepath.Dir(path), map[string]string{ProjectFile: c.json})
		p, err := readProject(path)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: error %v, want one saying %q", c.json, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.json, err)
		}
		got := p.checkerOptions(checker.Options{Deprecations: true})
		if !got.Deprecations {
			t.Errorf("%s: the options given were dropped", c.json)
		}
		if (got.ReflectedAttributes == nil) != (c.want == nil) || !slices.Equal(got.ReflectedAttributes, c.want) {
			t.Errorf("%s: ReflectedAttributes %#v, want %#v", c.json, got.ReflectedAttributes, c.want)
		}
	}
	if got := (*project)(nil).checkerOptions(checker.Options{}); got.ReflectedAttributes != nil {
		t.Errorf("no project file: ReflectedAttributes %#v, want the default", got.ReflectedAttributes)
	}
}

// reflectedTree is a project whose private properties carry attributes: one
// that implements App\Mapping, one that does not, and PHP's own.
func reflectedTree(t *testing.T, unused string) string {
	root := formatTree(t)
	project := `{"roots": {"src": ".src"}}`
	if unused != "" {
		project = `{"roots": {"src": ".src"}, "unused": ` + unused + `}`
	}
	writeTree(t, root, map[string]string{
		"phaenon.json": project,
		"src/Bad.phn":  "namespace App;\n",
		"src/Entity.phn": `namespace App;

interface Mapping {}

#[\Attribute(\Attribute::TARGET_PROPERTY)]
final class Column implements Mapping {}

#[\Attribute(\Attribute::TARGET_PROPERTY)]
final class Marker {}

final class Entity {
    #[Column] private int $mapped = 0;

    #[Marker] private int $marked = 0;

    public function __construct(#[\SensitiveParameter] private int $plain) {}
}
`,
	})
	return root
}

// phn check reads unused.reflectedAttributes, and so does phn build, which
// prints the same warnings.
func TestCheckReflectedAttributes(t *testing.T) {
	bin := phnBinary(t)
	for _, c := range []struct {
		unused string
		want   []string // the properties reported
	}{
		{"", []string{"plain"}},
		{`{"reflectedAttributes": []}`, []string{"mapped", "marked", "plain"}},
		{`{"reflectedAttributes": ["App\\Mapping"]}`, []string{"marked", "plain"}},
	} {
		root := reflectedTree(t, c.unused)
		for _, cmd := range []string{"check", "build"} {
			stdout, stderr, _ := run(t, root, bin, cmd)
			var got []string
			for _, name := range []string{"mapped", "marked", "plain"} {
				if strings.Contains(stdout+stderr, "Entity::$"+name) {
					got = append(got, name)
				}
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("phn %s with unused %s: reported %v, want %v\n%s%s", cmd, c.unused, got, c.want, stdout, stderr)
			}
		}
	}
}

// phn lsp reads it too.
func TestLSPReflectedAttributes(t *testing.T) {
	bin := phnBinary(t)
	root := reflectedTree(t, `{"reflectedAttributes": []}`)
	s := startLSP(t, bin, root, `{}`)
	path := filepath.Join(root, "src/Entity.phn")
	uri := fileURI(path)
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	open, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": "phaenon", "version": 1, "text": string(text)}}})
	s.send(string(open))
	m := s.next(func(m map[string]any) bool {
		p, _ := m["params"].(map[string]any)
		return m["method"] == "textDocument/publishDiagnostics" && p["uri"] == uri && p["version"] == float64(1)
	})
	var got []string
	for _, d := range m["params"].(map[string]any)["diagnostics"].([]any) {
		msg, _ := d.(map[string]any)["message"].(string)
		if i := strings.Index(msg, "Entity::$"); i >= 0 {
			got = append(got, strings.Fields(msg[i+len("Entity::$"):])[0])
		}
	}
	if want := []string{"mapped", "marked", "plain"}; !slices.Equal(got, want) {
		t.Errorf("phn lsp with an empty list: reported %v, want %v", got, want)
	}
}
