package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/program"
)

// fakeHost is a project on disk under a temporary root, built from the
// stubs alone: no Composer, no php.
type fakeHost struct {
	root   string
	cache  *program.ParseCache
	mu     sync.Mutex
	builds int
	built  []string
	// vendor, when set, is a loader every program shares, as phn's programs
	// share a project's Composer maps.
	vendor program.Loader
}

var stubCache = program.NewParseCache() // the stubs parse once for every test

func newFakeHost(t *testing.T, files map[string]string) *fakeHost {
	t.Helper()
	root := t.TempDir()
	for path, src := range files {
		full := filepath.Join(root, path)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &fakeHost{root: root, cache: stubCache}
}

func (h *fakeHost) Root() string { return h.root }

func (h *fakeHost) Files() []string {
	var out []string
	filepath.WalkDir(filepath.Join(h.root, "src"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".phn") {
			rel, _ := filepath.Rel(h.root, path)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func (h *fakeHost) Program(files []string, read func(string) ([]byte, error), check func(string) bool) (*program.Program, []checker.Diagnostic, error) {
	p := program.New()
	p.Parses = h.cache
	if err := p.LoadStubs(); err != nil {
		return nil, nil, err
	}
	if h.vendor != nil {
		p.AddLoader(h.vendor)
	}
	var diags []checker.Diagnostic
	for _, f := range files {
		src, err := read(f)
		if err != nil {
			continue
		}
		if _, err := p.AddFile(f, src, lexer.ModePhaenon, check(f)); err != nil {
			line := 1
			if pe, ok := err.(*parser.Error); ok {
				line = pe.Line
			}
			diags = append(diags, checker.Diagnostic{File: f, Line: line, Col: 1, Severity: checker.Error, Code: "P1000", Message: err.Error()})
		}
	}
	return p, diags, nil
}

func (h *fakeHost) Options(opts checker.Options) checker.Options { return opts }

func (h *fakeHost) ProjectDiagnostics(*program.Program, []string) []checker.Diagnostic { return nil }

func (h *fakeHost) Build(p *program.Program, diags []checker.Diagnostic) (BuildParams, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.builds++
	broken := map[string]bool{}
	for _, d := range diags {
		if d.Severity == checker.Error {
			broken[d.File] = true
		}
	}
	res := BuildParams{}
	for _, f := range h.Files() {
		res.Files++
		if broken[f] {
			res.Withheld++
		} else {
			res.Written++
			h.built = append(h.built, f)
		}
	}
	return res, nil
}

func (h *fakeHost) ClassNames() []string { return []string{"Vendor\\Lib\\Widget"} }

// client drives a server over pipes.
type client struct {
	t      *testing.T
	in     io.WriteCloser
	out    *bufio.Reader
	msgs   chan map[string]any
	id     int
	host   *fakeHost
	server *Server
	done   chan error
	// pending are the messages wait passed over, for a later wait.
	pending []map[string]any
}

func start(t *testing.T, files map[string]string, opts map[string]any) *client {
	t.Helper()
	return startAt(t, files, opts, func(root string) string { return root })
}

// startAt starts a server whose client names rootOf(the project's root) as
// its rootUri.
func startAt(t *testing.T, files map[string]string, opts map[string]any, rootOf func(string) string) *client {
	t.Helper()
	return startHost(t, newFakeHost(t, files), nil, opts, rootOf)
}

// startHost starts srv on host's project, or, with srv nil, a server of
// host itself that pauses 10 ms after an edit.
func startHost(t *testing.T, host *fakeHost, srv *Server, opts map[string]any, rootOf func(string) string) *client {
	t.Helper()
	if srv == nil {
		srv = New(host, io.Discard)
		srv.Debounce = 10 * time.Millisecond
	}
	if rootOf == nil {
		rootOf = func(root string) string { return root }
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, out: bufio.NewReader(outR), msgs: make(chan map[string]any, 100), host: host, server: srv, done: make(chan error, 1)}
	go func() { c.done <- srv.Run(inR, outW); outW.Close() }()
	go func() {
		conn := newConn(outR, io.Discard)
		for {
			m, err := conn.read()
			if err != nil {
				close(c.msgs)
				return
			}
			var v map[string]any
			b, _ := json.Marshal(m)
			json.Unmarshal(b, &v)
			c.msgs <- v
		}
	}()
	if opts == nil {
		opts = map[string]any{}
	}
	c.call("initialize", map[string]any{"rootUri": uriOf(rootOf(host.root)), "initializationOptions": opts,
		"capabilities": map[string]any{"textDocument": map[string]any{"completion": map[string]any{"completionItem": map[string]any{"snippetSupport": true}}}}})
	c.notify("initialized", map[string]any{})
	t.Cleanup(func() {
		c.call("shutdown", nil)
		c.notify("exit", nil)
		inW.Close()
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
			t.Error("the server did not exit")
		}
	})
	return c
}

func (c *client) send(v any) {
	body, _ := json.Marshal(v)
	fmt.Fprintf(c.in, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

func (c *client) notify(method string, params any) {
	c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// call sends a request and returns its result, keeping the notifications
// that arrive meanwhile for wait.
func (c *client) call(method string, params any) any {
	c.t.Helper()
	c.id++
	id := c.id
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	m := c.wait(func(m map[string]any) bool { return m["method"] == nil && m["id"] == float64(id) })
	if m == nil {
		c.t.Fatalf("%s: no response", method)
	}
	if e := m["error"]; e != nil {
		c.t.Fatalf("%s: %v", method, e)
	}
	return m["result"]
}

func (c *client) wait(pred func(map[string]any) bool) map[string]any {
	c.t.Helper()
	for i, m := range c.pending {
		if pred(m) {
			c.pending = append(c.pending[:i:i], c.pending[i+1:]...)
			return m
		}
	}
	timeout := time.After(15 * time.Second)
	for {
		select {
		case m, ok := <-c.msgs:
			if !ok {
				return nil
			}
			if pred(m) {
				return m
			}
			c.pending = append(c.pending, m)
		case <-timeout:
			return nil
		}
	}
}

func (c *client) uri(path string) string { return uriOf(filepath.Join(c.host.root, path)) }

func (c *client) open(path, text string) {
	c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": c.uri(path), "languageId": "phaenon", "version": 1, "text": text}})
}

// diagnostics waits for the diagnostics of a file, of the version given
// when it is not 0.
func (c *client) diagnostics(path string, version int) []any {
	c.t.Helper()
	uri := c.uri(path)
	m := c.wait(func(m map[string]any) bool {
		if m["method"] != "textDocument/publishDiagnostics" {
			return false
		}
		p := m["params"].(map[string]any)
		if p["uri"] != uri {
			return false
		}
		return version == 0 || p["version"] == float64(version)
	})
	if m == nil {
		c.t.Fatalf("no diagnostics for %s", path)
	}
	return m["params"].(map[string]any)["diagnostics"].([]any)
}

func codes(diags []any) []string {
	var out []string
	for _, d := range diags {
		out = append(out, d.(map[string]any)["code"].(string))
	}
	return out
}

func pos(line, char int) map[string]any { return map[string]any{"line": line, "character": char} }

func (c *client) at(method, path string, line, char int) any {
	return c.call(method, map[string]any{"textDocument": map[string]any{"uri": c.uri(path)}, "position": pos(line, char)})
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ------------------------------------------------------------------ the tests

const userSrc = `namespace App;

/** A user of the shop. */
final class User
{
    __construct(private string $name, public int $age) {}

    /** The user's name. */
    name(): string => $this->name;

    rename(string $to, bool $loud = false): User => new User($to, $this->age);
}
`

const shopSrc = `namespace App;

final class Shop
{
    greet(User $u): string {
        $n = $u->name();
        return $n;
    }
}
`

func TestDiagnosticsOnOpenAndChange(t *testing.T) {
	c := start(t, map[string]string{"src/User.phn": userSrc, "src/Shop.phn": shopSrc}, nil)
	c.open("src/Shop.phn", shopSrc)
	if d := c.diagnostics("src/Shop.phn", 1); len(d) != 0 {
		t.Fatalf("diagnostics of a clean file: %s", jsonOf(d))
	}
	broken := strings.Replace(shopSrc, "return $n;", "return $u->age;", 1)
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": c.uri("src/Shop.phn"), "version": 2},
		"contentChanges": []any{map[string]any{"text": broken}}})
	d := c.diagnostics("src/Shop.phn", 2)
	if got := codes(d); len(got) != 1 || got[0] != "P2202" {
		t.Fatalf("diagnostics after the edit: %s", jsonOf(d))
	}
	if r := d[0].(map[string]any)["range"].(map[string]any)["start"].(map[string]any); r["line"] != float64(6) {
		t.Errorf("range: %s", jsonOf(d[0]))
	}
	// The edit of another open buffer counts before it is saved.
	c.open("src/User.phn", strings.Replace(userSrc, "name(): string", "name(): int", 1))
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": c.uri("src/Shop.phn"), "version": 3},
		"contentChanges": []any{map[string]any{"text": shopSrc}}})
	if got := codes(c.diagnostics("src/Shop.phn", 3)); len(got) != 1 || got[0] != "P2202" {
		t.Errorf("Shop against the unsaved User: %v", got)
	}
}

// A client may name a directory around the project as its root, the
// workspace the project is a part of. The paths stay the project's: read
// under that directory, the open file would be a second copy of its own,
// declared twice (P2511).
func TestAClientRootAroundTheProject(t *testing.T) {
	c := startAt(t, map[string]string{"src/User.phn": userSrc, "src/Shop.phn": shopSrc}, nil, filepath.Dir)
	c.open("src/Shop.phn", shopSrc)
	if d := c.diagnostics("src/Shop.phn", 1); len(d) != 0 {
		t.Fatalf("diagnostics of a clean file: %s", jsonOf(d))
	}
}

func TestProjectCheckAndBuildOnSave(t *testing.T) {
	broken := strings.Replace(shopSrc, "return $n;", "return 1;", 1)
	c := start(t, map[string]string{"src/User.phn": userSrc, "src/Shop.phn": broken}, nil)
	// initialized checks the whole project: a closed file's findings show.
	if got := codes(c.diagnostics("src/Shop.phn", 0)); len(got) != 1 || got[0] != "P2202" {
		t.Fatalf("project check: %v", got)
	}
	time.Sleep(50 * time.Millisecond)
	c.host.mu.Lock()
	if c.host.builds != 0 {
		t.Errorf("opening the project built it")
	}
	c.host.mu.Unlock()
	c.notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": c.uri("src/Shop.phn")}})
	m := c.wait(func(m map[string]any) bool { return m["method"] == "phaenon/build" })
	if m == nil {
		t.Fatal("no phaenon/build after the save")
	}
	p := m["params"].(map[string]any)
	if p["files"] != float64(2) || p["written"] != float64(1) || p["withheld"] != float64(1) {
		t.Errorf("build: %s", jsonOf(p))
	}
	// Fixed on disk, the next save clears the file's diagnostics.
	os.WriteFile(filepath.Join(c.host.root, "src/Shop.phn"), []byte(shopSrc), 0o644)
	c.notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": c.uri("src/Shop.phn")}})
	uri := c.uri("src/Shop.phn")
	cleared := c.wait(func(m map[string]any) bool {
		if m["method"] != "textDocument/publishDiagnostics" {
			return false
		}
		p := m["params"].(map[string]any)
		return p["uri"] == uri && len(p["diagnostics"].([]any)) == 0
	})
	if cleared == nil {
		t.Error("the fixed file's diagnostics were not cleared")
	}
}

// A finding about a declaration elsewhere says where: the parameter an
// argument does not fit.
func TestRelatedDeclaration(t *testing.T) {
	src := strings.Replace(shopSrc, "$n = $u->name();", "$n = $u->rename(1);", 1)
	c := start(t, map[string]string{"src/User.phn": userSrc, "src/Shop.phn": src}, nil)
	c.open("src/Shop.phn", src)
	d := c.diagnostics("src/Shop.phn", 1)
	var p2103 map[string]any
	for _, x := range d {
		if m := x.(map[string]any); m["code"] == "P2103" {
			p2103 = m
		}
	}
	if p2103 == nil {
		t.Fatalf("no P2103: %s", jsonOf(d))
	}
	rel, _ := p2103["relatedInformation"].([]any)
	if len(rel) != 1 {
		t.Fatalf("related: %s", jsonOf(p2103))
	}
	r := rel[0].(map[string]any)
	loc := r["location"].(map[string]any)
	if loc["uri"] != c.uri("src/User.phn") || loc["range"].(map[string]any)["start"].(map[string]any)["line"] != float64(10) ||
		!strings.Contains(r["message"].(string), "parameter $to of App\\User::rename() declared here") {
		t.Errorf("related: %s", jsonOf(r))
	}
}

func TestBuildOnSaveOff(t *testing.T) {
	broken := strings.Replace(userSrc, "name(): string", "name(): int", 1)
	c := start(t, map[string]string{"src/User.phn": broken}, map[string]any{"buildOnSave": false})
	c.diagnostics("src/User.phn", 0) // the check at initialized
	c.notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": c.uri("src/User.phn")}})
	c.diagnostics("src/User.phn", 0) // the save's
	time.Sleep(100 * time.Millisecond)
	c.host.mu.Lock()
	defer c.host.mu.Unlock()
	if c.host.builds != 0 {
		t.Errorf("built %d times with build on save off", c.host.builds)
	}
}

func TestDiagnosticsOnlyCapabilities(t *testing.T) {
	c := start(t, map[string]string{"src/User.phn": userSrc}, map[string]any{"features": "diagnostics"})
	caps := c.call("initialize", map[string]any{"initializationOptions": map[string]any{"features": "diagnostics"}}).(map[string]any)["capabilities"].(map[string]any)
	for _, k := range []string{"hoverProvider", "definitionProvider", "completionProvider", "documentFormattingProvider", "signatureHelpProvider", "documentSymbolProvider"} {
		if caps[k] != nil {
			t.Errorf("PhpStorm's server advertises %s", k)
		}
	}
	if caps["textDocumentSync"] == nil || caps["codeActionProvider"] == nil {
		t.Errorf("capabilities: %s", jsonOf(caps))
	}
}

func TestHoverAndDefinition(t *testing.T) {
	c := start(t, map[string]string{"src/User.phn": userSrc, "src/Shop.phn": shopSrc}, nil)
	c.open("src/Shop.phn", shopSrc)
	c.diagnostics("src/Shop.phn", 1)
	cases := []struct {
		line, char int
		want       string
	}{
		{5, 18, "App\\User::name(): string"}, // name()
		{5, 18, "The user's name."},
		{5, 9, "$n: string"},
		{4, 11, "final class App\\User"}, // the parameter's type
		{4, 11, "A user of the shop."},
		{5, 14, "$u: App\\User"},
	}
	for _, cs := range cases {
		h := jsonOf(c.at("textDocument/hover", "src/Shop.phn", cs.line, cs.char))
		if !strings.Contains(h, strings.ReplaceAll(cs.want, `\`, `\\`)) {
			t.Errorf("hover at %d:%d = %s, want %q", cs.line, cs.char, h, cs.want)
		}
	}
	def := c.at("textDocument/definition", "src/Shop.phn", 5, 18).(map[string]any)
	if def["uri"] != c.uri("src/User.phn") || jsonOf(def["range"]) != `{"end":{"character":8,"line":8},"start":{"character":4,"line":8}}` {
		t.Errorf("definition of name(): %s", jsonOf(def))
	}
	def = c.at("textDocument/definition", "src/Shop.phn", 6, 16).(map[string]any) // return $n
	if def["uri"] != c.uri("src/Shop.phn") || def["range"].(map[string]any)["start"].(map[string]any)["line"] != float64(5) {
		t.Errorf("definition of $n: %s", jsonOf(def))
	}
	// A function of the stubs opens their file, written to the cache.
	src := "namespace App;\n\nfunction f(string $s): int => strlen($s);\n"
	c.open("src/F.phn", src)
	c.diagnostics("src/F.phn", 1)
	def = c.at("textDocument/definition", "src/F.phn", 2, 32).(map[string]any)
	if u, _ := def["uri"].(string); !strings.Contains(u, "phaenon/lsp/stub/") {
		t.Errorf("definition of strlen: %s", jsonOf(def))
	}
}

func TestDocumentSymbols(t *testing.T) {
	c := start(t, map[string]string{"src/User.phn": userSrc}, nil)
	c.open("src/User.phn", userSrc)
	got := c.call("textDocument/documentSymbol", map[string]any{"textDocument": map[string]any{"uri": c.uri("src/User.phn")}})
	var names []string
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		for _, s := range v.([]any) {
			m := s.(map[string]any)
			names = append(names, strings.Repeat(" ", depth)+m["name"].(string))
			if ch, ok := m["children"]; ok {
				walk(ch, depth+1)
			}
		}
	}
	walk(got, 0)
	want := "App|User| __construct| $name| $age| name| rename"
	if strings.Join(names, "|") != want {
		t.Errorf("symbols: %q, want %q", strings.Join(names, "|"), want)
	}
}

func TestCompletion(t *testing.T) {
	c := start(t, map[string]string{"src/User.phn": userSrc, "src/Shop.phn": shopSrc}, nil)
	labels := func(text string, line, char int) []string {
		c.open("src/Shop.phn", text)
		res := c.at("textDocument/completion", "src/Shop.phn", line, char).(map[string]any)
		var out []string
		for _, it := range res["items"].([]any) {
			out = append(out, it.(map[string]any)["label"].(string))
		}
		return out
	}
	member := strings.Replace(shopSrc, "$n = $u->name();", "$n = $u->", 1)
	if got := strings.Join(labels(member, 5, 17), ","); got != "age,name,rename" {
		t.Errorf("members after ->: %s", got)
	}
	prefixed := strings.Replace(shopSrc, "$n = $u->name();", "$n = $u->re", 1)
	if got := strings.Join(labels(prefixed, 5, 19), ","); got != "rename" {
		t.Errorf("members after ->re: %s", got)
	}
	vars := strings.Replace(shopSrc, "return $n;", "return $", 1)
	if got := strings.Join(labels(vars, 6, 16), ","); got != "n,this,u" {
		t.Errorf("variables: %s", got)
	}
	names := strings.Replace(shopSrc, "return $n;", "return new Wid", 1)
	items := c.at("textDocument/completion", "src/Shop.phn", 6, 22)
	_ = items
	got := labels(names, 6, 22)
	if strings.Join(got, ",") != "Widget" {
		t.Errorf("classes: %v", got)
	}
	res := c.at("textDocument/completion", "src/Shop.phn", 6, 22).(map[string]any)
	it := res["items"].([]any)[0].(map[string]any)
	if edits := jsonOf(it["additionalTextEdits"]); !strings.Contains(edits, `use Vendor\\Lib\\Widget;`) {
		t.Errorf("the import of Widget: %s", edits)
	}
	fns := strings.Replace(shopSrc, "return $n;", "return str_rep", 1)
	if got := labels(fns, 6, 22); strings.Join(got, ",") != "str_repeat,str_replace" {
		t.Errorf("functions: %v", got)
	}
}

func TestPipeCompletion(t *testing.T) {
	src := "namespace App;\n\nfunction f(list<int> $xs): int {\n    return $xs |> array_su\n}\n"
	c := start(t, map[string]string{"src/F.phn": src}, nil)
	c.open("src/F.phn", src)
	res := c.at("textDocument/completion", "src/F.phn", 3, 26).(map[string]any)
	var got []string
	for _, it := range res["items"].([]any) {
		m := it.(map[string]any)
		got = append(got, m["label"].(string)+"="+m["textEdit"].(map[string]any)["newText"].(string))
	}
	if strings.Join(got, ",") != "array_sum=array_sum(...)" {
		t.Errorf("pipe stages: %v", got)
	}
}

func TestSignatureHelp(t *testing.T) {
	src := strings.Replace(shopSrc, "$n = $u->name();", "$n = $u->rename('x', ", 1)
	c := start(t, map[string]string{"src/User.phn": userSrc, "src/Shop.phn": src}, nil)
	c.open("src/Shop.phn", src)
	res := c.at("textDocument/signatureHelp", "src/Shop.phn", 5, 29).(map[string]any)
	sig := res["signatures"].([]any)[0].(map[string]any)
	if sig["label"] != "App\\User::rename(string $to, bool $loud = …): App\\User" && sig["label"] != "rename(string $to, bool $loud = …): App\\User" {
		t.Errorf("label: %v", sig["label"])
	}
	if res["activeParameter"] != float64(1) {
		t.Errorf("active parameter: %v", res["activeParameter"])
	}
}

func TestFormattingAndTabFix(t *testing.T) {
	src := "namespace App;\n\nfinal class Box\n{\n\tget(): list < int >\n\t{\n\t\treturn [];\n\t}\n}\n"
	c := start(t, map[string]string{"src/Box.phn": src}, nil)
	c.open("src/Box.phn", src)
	d := c.diagnostics("src/Box.phn", 1)
	if got := codes(d); len(got) != 1 || got[0] != "P1005" {
		t.Fatalf("diagnostics: %s", jsonOf(d))
	}
	actions := c.call("textDocument/codeAction", map[string]any{"textDocument": map[string]any{"uri": c.uri("src/Box.phn")},
		"range": d[0].(map[string]any)["range"], "context": map[string]any{"diagnostics": d}}).([]any)
	fixed := ""
	if len(actions) == 1 {
		edit := actions[0].(map[string]any)["edit"].(map[string]any)["changes"].(map[string]any)[c.uri("src/Box.phn")].([]any)[0]
		fixed = edit.(map[string]any)["newText"].(string)
	}
	if want := strings.ReplaceAll(src, "\t", "    "); fixed != want {
		t.Errorf("the tab fix: %s", jsonOf(actions))
	}
	edits := c.call("textDocument/formatting", map[string]any{"textDocument": map[string]any{"uri": c.uri("src/Box.phn")}}).([]any)
	want := "namespace App;\n\nfinal class Box\n{\n    get(): list<int> {\n        return [];\n    }\n}\n"
	if len(edits) != 1 || edits[0].(map[string]any)["newText"] != want {
		t.Errorf("formatting: %s", jsonOf(edits))
	}
}

func TestPositions(t *testing.T) {
	tx := newText([]byte("a\n€é😀x\n"))
	for _, c := range []struct {
		off int
		pos Position
	}{{0, Position{0, 0}}, {2, Position{1, 0}}, {5, Position{1, 1}}, {7, Position{1, 2}}, {11, Position{1, 4}}, {12, Position{1, 5}}, {13, Position{2, 0}}} {
		if got := tx.position(c.off); got != c.pos {
			t.Errorf("position(%d) = %v, want %v", c.off, got, c.pos)
		}
		if got := tx.offset(c.pos); got != c.off {
			t.Errorf("offset(%v) = %d, want %d", c.pos, got, c.off)
		}
	}
}
