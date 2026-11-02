package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ProjectFile is the project file build and check read from the working
// directory when they are given no paths.
const ProjectFile = "phaenon.json"

// project is phaenon.json: the source roots with the directory each compiles
// into, the paths the checker leaves alone, and the PHP version the code is
// compiled for (target.go).
//
// An output directory is a sibling of its root (src → .src), never a
// subdirectory or a deeper mirror, so that a compiled file's __DIR__ sits at
// the same depth as its source's: a path written against the source
// (__DIR__.'/../config/services.yaml', \dirname(__DIR__, 3)) resolves to the
// same place from the output, and the emitted PHP needs no rewriting. Files
// under a root that are not Phaenon sources (fixtures, YAML, data) are copied
// into the output for the same reason: __DIR__.'/Fixture' must exist there.
type project struct {
	Roots   map[string]string `json:"roots"`
	Exclude []string          `json:"exclude"`
	PHP     string            `json:"php"`
	data    []byte            // the file as read, to place diagnostics
}

// loadProject reads phaenon.json from the working directory; nil when there is none.
func loadProject() *project {
	data, err := os.ReadFile(ProjectFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		fail("%v", err)
	}
	p := project{data: data}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		fail("%s: %v", ProjectFile, err)
	}
	roots := make(map[string]string, len(p.Roots))
	for src, out := range p.Roots {
		src, out = filepath.Clean(src), filepath.Clean(out)
		switch {
		case src == "." || out == "." || filepath.IsAbs(src) || filepath.IsAbs(out) ||
			src == ".." || out == ".." || strings.HasPrefix(src, "../") || strings.HasPrefix(out, "../"):
			fail("%s: roots and their outputs are directories inside the project; %q → %q is not", ProjectFile, src, out)
		case src == out:
			fail("%s: the output of %q must be another directory", ProjectFile, src)
		case filepath.Dir(src) != filepath.Dir(out):
			fail("%s: the output of %q must be its sibling, a directory with the same parent, so that __DIR__ keeps its depth; %q is not", ProjectFile, src, out)
		}
		roots[src] = out
	}
	p.Roots = roots
	for i, e := range p.Exclude {
		p.Exclude[i] = filepath.Clean(e)
	}
	return &p
}

// requireRoots fails unless the project file names roots: build and check
// without paths compile them. A project file may state only the target
// (`phn migrate --php` creates one that does).
func (p *project) requireRoots(cmd string) {
	if p == nil {
		fail("phn %s: give paths, or a %s naming the roots", cmd, ProjectFile)
	}
	if len(p.Roots) == 0 {
		fail(`phn %s: give paths, or name the roots in %s: "roots" maps each source directory to its output directory, as in {"roots": {"src": ".src"}}`, cmd, ProjectFile)
	}
}

// roots lists the source roots in a stable order.
func (p *project) roots() []string {
	roots := make([]string, 0, len(p.Roots))
	for src := range p.Roots {
		roots = append(roots, src)
	}
	sort.Strings(roots)
	return roots
}

// sources maps each output directory to its root, both absolute, for the
// Composer loader: the autoload maps name the outputs.
func (p *project) sources() map[string]string {
	m := map[string]string{}
	for src, out := range p.Roots {
		absOut, err1 := filepath.Abs(out)
		absSrc, err2 := filepath.Abs(src)
		if err1 == nil && err2 == nil {
			m[filepath.Clean(absOut)] = filepath.Clean(absSrc)
		}
	}
	return m
}

// isOutput reports whether path is an output directory or lies under one.
func (p *project) isOutput(path string) bool {
	path = filepath.Clean(path)
	for _, out := range p.Roots {
		if path == out || strings.HasPrefix(path, out+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// outputPath maps a source path under one of the roots to its output: the
// mirrored path with .phn turned into .php. A path under no root compiles
// beside itself, as without a project file.
func (p *project) outputPath(in string) string {
	dst := strings.TrimSuffix(in, ".phn") + ".php"
	for src, out := range p.Roots {
		if rel, ok := under(dst, src); ok {
			return filepath.Join(out, rel)
		}
	}
	return dst
}

// under returns path relative to dir when path lies under it.
func under(path, dir string) (string, bool) {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	if !strings.HasPrefix(path, dir+string(filepath.Separator)) {
		return "", false
	}
	return path[len(dir)+1:], true
}

// mirror brings a root's output directory in step with the root beyond the
// compiled files: every file that is not a Phaenon source is copied over
// (fixtures and resources that __DIR__-relative code reads), and every file
// in the output with no source behind it any more — the .php of a deleted or
// renamed .phn, a removed fixture — is deleted, since a stale .php would
// declare a class a second time. With check nothing is written; the return
// values count what was (or would be) copied and removed.
func (p *project) mirror(src string, check bool) (copied, removed int) {
	out := p.Roots[src]
	filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			fail("%v", err)
		}
		if d.IsDir() {
			if d.Name() == "vendor" && path != src {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".phn" {
			return nil
		}
		rel, _ := under(path, src)
		dst := filepath.Join(out, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			fail("%v", err)
		}
		if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, data) {
			return nil
		}
		copied++
		if check {
			fmt.Fprintf(os.Stderr, "%s: stale or missing\n", dst)
			return nil
		}
		writeFile(dst, data)
		return nil
	})
	if _, err := os.Stat(out); err != nil {
		return copied, removed
	}
	var dirs []string
	filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			fail("%v", err)
		}
		if d.IsDir() {
			if path != out {
				dirs = append(dirs, path)
			}
			return nil
		}
		rel, _ := under(path, out)
		source := filepath.Join(src, rel)
		if filepath.Ext(rel) == ".php" {
			if _, err := os.Stat(strings.TrimSuffix(source, ".php") + ".phn"); err == nil {
				return nil
			}
		}
		if _, err := os.Stat(source); err == nil {
			return nil
		}
		removed++
		if check {
			fmt.Fprintf(os.Stderr, "%s: no source behind it\n", path)
			return nil
		}
		if err := os.Remove(path); err != nil {
			fail("%v", err)
		}
		return nil
	})
	if !check {
		// Deepest first, so that a directory emptied by its children goes too.
		sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
		for _, dir := range dirs {
			os.Remove(dir) // fails, harmlessly, while the directory holds files
		}
	}
	return copied, removed
}

func writeFile(dst string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		fail("%v", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		fail("%v", err)
	}
}
