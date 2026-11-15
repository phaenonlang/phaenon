package program

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// TestExtensionStubCache: a cached stub is read by the generator that wrote
// it alone. One an earlier generator cached, under the name phn gave the
// file before it named the generator or under another generator's hash, is
// left alone and the extension reflected again: a generator fix reaches a
// machine that cached what the old generator wrote (a DNF type without its
// parentheses, which lost the whole extension).
func TestExtensionStubCache(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not on PATH")
	}
	exts, version, err := LoadedExtensions(php)
	if err != nil {
		t.Fatal(err)
	}
	var date Extension // ext-date cannot be left out of a PHP build
	for _, e := range exts {
		if strings.EqualFold(e.Name, "date") {
			date = e
		}
	}
	if date.Name == "" {
		t.Fatal("php reports no ext-date")
	}
	dir := t.TempDir()
	name := fmt.Sprintf("%s-%s-php%s", fileSafe(date.Name), fileSafe(date.Version), fileSafe(version))
	stale := []byte("<?php\nfunction stale(\\Countable&\\Traversable|null $x): void {}\n")
	for _, path := range []string{
		filepath.Join(dir, "phaenon", "ext", name+".php"),
		cachePath(dir, name, scriptHash("// an earlier generator\n"+generateStubScript)),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, stale, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	src, err := cachedExtensionStub(php, dir, date, version)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(src, []byte("function stale(")) {
		t.Fatal("the stub an earlier generator cached was read")
	}
	if !bytes.Contains(src, []byte("class DateTime ")) {
		t.Errorf("the stub of ext-date declares no DateTime:\n%s", src)
	}
	if _, err := parser.Parse(src, lexer.ModePHP); err != nil {
		t.Errorf("the stub does not parse: %v", err)
	}

	// What this generator cached is what the next run reads.
	mine := []byte("<?php\nfunction mine(): void {}\n")
	if err := os.WriteFile(cachePath(dir, name, stubGenerator), mine, 0o644); err != nil {
		t.Fatal(err)
	}
	if src, err := cachedExtensionStub(php, dir, date, version); err != nil || !bytes.Equal(src, mine) {
		t.Errorf("the cached stub was not read: %v\n%s", err, src)
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
