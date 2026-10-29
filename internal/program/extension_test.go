package program

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/stdlib"
)

// Every loaded extension's generated stub must parse: the generator is
// exercised on the whole of the local php, not only on what php-src lacks.
func TestExtensionStubsParse(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not on PATH")
	}
	exts, version, err := LoadedExtensions(php)
	if err != nil {
		t.Fatal(err)
	}
	if version == "" || len(exts) == 0 {
		t.Fatalf("php reported version %q and %d extensions", version, len(exts))
	}
	for _, e := range exts {
		src, err := GenerateExtensionStub(php, e.Name)
		if err != nil {
			t.Errorf("%s: %v", e.Name, err)
			continue
		}
		if len(strings.TrimSpace(string(src))) == 0 {
			continue
		}
		if _, err := parser.Parse(src, lexer.ModePHP); err != nil {
			t.Errorf("%s: generated stub does not parse: %v", e.Name, err)
		}
	}
}

func TestLoadExtensions(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not on PATH")
	}
	embedded := stdlib.EmbeddedExtensions()
	p := New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	loaded, err := p.LoadExtensions(php, t.TempDir(), func(lower string) bool { return embedded[lower] })
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range loaded {
		if embedded[strings.ToLower(name)] {
			t.Errorf("%s is embedded and was generated anyway", name)
		}
	}
	hasMongo := false
	for _, name := range loaded {
		if strings.EqualFold(name, "mongodb") {
			hasMongo = true
		}
	}
	if !hasMongo {
		t.Skip("ext-mongodb is not loaded")
	}
	c := p.Class("MongoDB\\Driver\\Manager")
	if c == nil {
		t.Fatal("MongoDB\\Driver\\Manager not declared")
	}
	c.Signature()
	if m := c.FindMethod("selectServer"); m == nil {
		t.Error("Manager::selectServer missing")
	}
	if p.Class("MongoDB\\Driver\\CursorInterface") == nil || !p.IsSubclassOf("MongoDB\\Driver\\CursorInterface", "Traversable") {
		t.Error("CursorInterface should extend Traversable")
	}
}
