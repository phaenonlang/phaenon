package emit

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
)

// TestLineMapTree runs the line map property over a project's sources:
// PHAENON_LINEMAP_TREE names a directory of .phn files (a dogfood tree's
// src), read with the Composer maps above it as phn build reads them. Every
// file is compiled whatever the checker reports, as --emit-on-error does,
// and its output is held to TestOutputsDependOnPHPOnly's promise too: it
// names nothing under Phaenon\ (design/roadmap.md D24).
func TestLineMapTree(t *testing.T) {
	root := os.Getenv("PHAENON_LINEMAP_TREE")
	if root == "" {
		t.Skip("PHAENON_LINEMAP_TREE not set")
	}
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	if l, err := program.FindComposer(root); err == nil {
		p.AddLoader(l)
	}
	var paths []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".phn") {
			paths = append(paths, path)
		}
		return nil
	})
	sources := map[string][]byte{}
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.AddFile(path, src, lexer.ModePhaenon, true); err != nil {
			t.Logf("%s: %v", path, err)
			continue
		}
		sources[path] = src
	}
	checker.New(p, checker.Options{}).Check()
	compiled := 0
	for _, path := range paths {
		src, ok := sources[path]
		if !ok {
			continue
		}
		out, w, err := compileRecovered(t, path, src, p, p.File(path))
		if err != nil || out == nil {
			t.Logf("%s: not compiled: %v", path, err)
			continue
		}
		compiled++
		checkLineMap(t, path, src, out, w)
		expectPHPOnly(t, path, out)
		if dir := os.Getenv("PHAENON_LINEMAP_OUT"); dir != "" {
			rel, _ := filepath.Rel(root, path)
			dst := filepath.Join(dir, strings.TrimSuffix(rel, ".phn")+".php")
			os.MkdirAll(filepath.Dir(dst), 0o755)
			os.WriteFile(dst, out, 0o644)
		}
	}
	t.Logf("%d of %d files compiled, their maps and their dependencies checked", compiled, len(paths))
}
