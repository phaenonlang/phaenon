package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
)

// phaenon.json's unused.reflected-attributes: a missing key keeps the
// checker's default (nil), an empty list is kept as one, and names are
// checked to be class names. The key as it was spelt before 0.8.0 is read
// as the key and warned of on the file (design/roadmap.md D34); the two
// together are refused.
func TestReadProjectReflectedAttributes(t *testing.T) {
	const earlier = `"unused.reflectedAttributes" is written "unused.reflected-attributes" since phn 0.8.0: ` +
		`the keys of phaenon.json are lower-case words joined by dashes, and this spelling is read until the first beta`
	for _, c := range []struct {
		json string
		want []string // nil: the default
		err  string
		warn string // the position of the warning of an earlier spelling
	}{
		{json: `{"unused": {"reflectedAttributes": []}}`, want: []string{}, warn: "1:13"},
		{json: "{\n  \"roots\": {\"src\": \".src\"},\n  \"unused\": {\n    \"reflectedAttributes\": [\"App\\\\Mapping\"]\n  }\n}\n",
			want: []string{`App\Mapping`}, warn: "4:5"},
		{json: `{"unused": {"reflectedAttributes": null}}`, warn: "1:13"},
		{json: `{"unused": {"reflectedAttributes": ["App/Mapping"]}}`, err: `"unused": {"reflected-attributes": [...]} lists attribute classes by their full name`},
		{json: `{"unused": {"reflected-attributes": ["App\\A"], "reflectedAttributes": ["App\\B"]}}`,
			err: `"unused" has both "reflected-attributes" and "reflectedAttributes", one key in two spellings: write "reflected-attributes" alone`},
		{json: `{"unused": {"reflectedAttributes": null, "reflected-attributes": null}}`, err: `"unused" has both`},
		{json: `{"unused": {"reflectedattributes": []}}`, err: `the key "reflectedattributes" is written "reflected-attributes"`},
		{json: `{"unused": {"Reflected-Attributes": []}}`, err: `the key "Reflected-Attributes" is written "reflected-attributes"`},
		{json: `{"roots": {"src": ".src"}}`},
		{json: `{"roots": {"src": ".src"}, "unused": {}}`},
		{json: `{"roots": {"src": ".src"}, "unused": {"reflected-attributes": []}}`, want: []string{}},
		{json: `{"unused": {"reflected-attributes": ["App\\Mapping", "\\Doctrine\\ORM\\Mapping\\MappingAttribute"]}}`,
			want: []string{`App\Mapping`, `\Doctrine\ORM\Mapping\MappingAttribute`}},
		{json: `{"unused": {"reflected-attributes": ["App\\"]}}`, err: `"App\\" is not one`},
		{json: `{"unused": {"reflected-attributes": ["App/Mapping"]}}`, err: "is not one"},
		{json: `{"unused": {"reflected": []}}`, err: `unknown key "unused.reflected" (this is phn ` + version + `; a newer release may know it)`},
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
		var warned []string
		for _, d := range p.spellingDiags() {
			if d.File != path || d.Code != "P4016" || d.Severity != checker.Warning || d.Message != earlier {
				t.Errorf("%s: the warning is %s", c.json, d)
			}
			warned = append(warned, fmt.Sprintf("%d:%d", d.Line, d.Col))
		}
		if got := strings.Join(warned, " "); got != c.warn {
			t.Errorf("%s: warned of an earlier spelling at %q, want %q", c.json, got, c.warn)
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

// phn check reads unused.reflected-attributes, and so does phn build, which
// prints the same warnings.
func TestCheckReflectedAttributes(t *testing.T) {
	bin := phnBinary(t)
	for _, c := range []struct {
		unused string
		want   []string // the properties reported
	}{
		{"", []string{"plain"}},
		{`{"reflected-attributes": []}`, []string{"mapped", "marked", "plain"}},
		{`{"reflected-attributes": ["App\\Mapping"]}`, []string{"marked", "plain"}},
		// The key as it was spelt before 0.8.0 is read as the key.
		{`{"reflectedAttributes": ["App\\Mapping"]}`, []string{"marked", "plain"}},
	} {
		root := reflectedTree(t, c.unused)
		for _, cmd := range []string{"check", "build"} {
			stdout, stderr, err := run(t, root, bin, cmd)
			// And it is warned of, on the project file, by the commands
			// that print the file's findings; a warning fails nothing.
			const warning = `phaenon.json:1:39: warning P4016: "unused.reflectedAttributes" is written "unused.reflected-attributes" since phn 0.8.0`
			if earlier := strings.Contains(c.unused, "reflectedAttributes"); strings.Contains(stderr, warning) != earlier || strings.Contains(stdout+stderr, "P4016") != earlier || err != nil {
				t.Errorf("phn %s with unused %s (%v): the warning of the earlier spelling, want %v\n%s%s", cmd, c.unused, err, earlier, stdout, stderr)
			}
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
	root := reflectedTree(t, `{"reflected-attributes": []}`)
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

// phn lsp reads the key as it was spelt before 0.8.0, and shows the warning
// of it on the project file.
func TestLSPEarlierSpelling(t *testing.T) {
	bin := phnBinary(t)
	root := reflectedTree(t, `{"reflectedAttributes": []}`)
	s := startLSP(t, bin, root, `{}`)
	uri := fileURI(filepath.Join(root, ProjectFile))
	m := s.next(func(m map[string]any) bool {
		p, _ := m["params"].(map[string]any)
		return m["method"] == "textDocument/publishDiagnostics" && p["uri"] == uri
	})
	var got []string
	for _, d := range m["params"].(map[string]any)["diagnostics"].([]any) {
		code, _ := d.(map[string]any)["code"].(string)
		msg, _ := d.(map[string]any)["message"].(string)
		got = append(got, code+" "+msg)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], `P4016 "unused.reflectedAttributes" is written "unused.reflected-attributes" since phn 0.8.0`) {
		t.Errorf("phn lsp with the earlier spelling: %q on %s, want the one warning of it", got, ProjectFile)
	}
}
