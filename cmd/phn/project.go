package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/checker"
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
	Roots map[string]string `json:"roots"`
	// Committed names the source roots whose outputs the project's
	// repository tracks, each a key of Roots (design/roadmap.md D28,
	// design/tooling.md §3.7). It says something only of an installed
	// package: `phn build --vendor` leaves those roots as installed
	// (vendor.go). In the project's own directory every command treats a
	// committed root as any other.
	Committed []string `json:"committed"`
	Exclude   []string `json:"exclude"`
	PHP       string   `json:"php"`
	// Fmt is the command of phn fmt's second layer, the project's fixer run
	// through phn fix (fmt.go); an empty list turns the layer off.
	Fmt *[]string `json:"fmt"`
	// Unused tunes the unused private member findings (P2406–P2408).
	Unused *unusedSettings `json:"unused"`
	data   []byte          // the file as read, to place diagnostics
	path   string          // the file's path, for diagnostics
}

// unusedSettings is phaenon.json's "unused".
type unusedSettings struct {
	// ReflectedAttributes names the attribute classes that mark a private
	// property as read and written through reflection, their subclasses and
	// implementations included; without it, any attribute but PHP's own
	// does, and an empty list lets none (design/tooling.md §9.2).
	ReflectedAttributes *[]string `json:"reflectedAttributes"`
}

// checkerOptions are the checker's settings the project file states; a nil
// project states none.
func (p *project) checkerOptions(opts checker.Options) checker.Options {
	if p != nil && p.Unused != nil && p.Unused.ReflectedAttributes != nil {
		opts.ReflectedAttributes = append([]string{}, *p.Unused.ReflectedAttributes...)
	}
	return opts
}

// loadProject reads phaenon.json from the working directory; nil when there is none.
func loadProject() *project {
	p, err := readProject(ProjectFile)
	if err != nil {
		fail("%v", err)
	}
	return p
}

// readProject reads and validates a project file; nil, nil when there is
// none. Its roots and excludes are relative to the file's directory.
func readProject(path string) (*project, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := project{data: data, path: path}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	// encoding/json reads the first value and stops, and matches a key to
	// a field whatever its letter case. A project file is one object whose
	// keys are spelt as documented: the Composer plugin's reader of "roots"
	// and "committed" (composer/src/ProjectFile.php) reads no other, and
	// the two must give one answer.
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%s: the project file is one JSON object, and text follows it", path)
	}
	if key, want := misspeltKey(data, reflect.TypeFor[project]()); key != "" {
		return nil, fmt.Errorf("%s: the key %q is written %q", path, key, want)
	}
	roots := make(map[string]string, len(p.Roots))
	for _, key := range slices.Sorted(maps.Keys(p.Roots)) {
		src, out := filepath.Clean(key), filepath.Clean(p.Roots[key])
		switch {
		case src == "." || out == "." || filepath.IsAbs(src) || filepath.IsAbs(out) ||
			src == ".." || out == ".." || strings.HasPrefix(src, "../") || strings.HasPrefix(out, "../"):
			return nil, fmt.Errorf("%s: roots and their outputs are directories inside the project; %q → %q is not", path, src, out)
		case src == out:
			return nil, fmt.Errorf("%s: the output of %q must be another directory", path, src)
		case filepath.Dir(src) != filepath.Dir(out):
			return nil, fmt.Errorf("%s: the output of %q must be its sibling, a directory with the same parent, so that __DIR__ keeps its depth; %q is not", path, src, out)
		}
		// Two spellings of one root ("src", "./src") are one root when
		// they name one output.
		if other, twice := roots[src]; twice && other != out {
			return nil, fmt.Errorf("%s: the root %q is named twice, with the outputs %q and %q", path, src, other, out)
		}
		roots[src] = out
	}
	if err := separateDirectories(roots); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	p.Roots = roots
	committed := make([]string, 0, len(p.Committed))
	for _, entry := range p.Committed {
		src := filepath.Clean(entry)
		if _, ok := roots[src]; !ok {
			return nil, fmt.Errorf(`%s: "committed" lists source roots, each of them a key of "roots"; %q is not one`, path, entry)
		}
		if !slices.Contains(committed, src) {
			committed = append(committed, src)
		}
	}
	p.Committed = committed
	for i, e := range p.Exclude {
		p.Exclude[i] = filepath.Clean(e)
	}
	if p.Unused != nil && p.Unused.ReflectedAttributes != nil {
		for _, name := range *p.Unused.ReflectedAttributes {
			if !isClassName(strings.TrimPrefix(name, "\\")) {
				return nil, fmt.Errorf(`%s: "unused": {"reflectedAttributes": [...]} lists attribute classes by their full name, as in "Doctrine\\ORM\\Mapping\\MappingAttribute"; %q is not one`, path, name)
			}
		}
	}
	return &p, nil
}

// misspeltKey finds, in the JSON object data read into a struct of type t,
// a key that names a field in another letter case than the field's tag,
// and returns it with the tag; the objects of the fields that are structs
// are looked into as well. It returns "", "" when every key is a tag, or
// none (which the decoder has refused already).
func misspeltKey(data []byte, t reflect.Type) (key, want string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var object map[string]json.RawMessage
	if t.Kind() != reflect.Struct || json.Unmarshal(data, &object) != nil {
		return "", ""
	}
	for _, key := range slices.Sorted(maps.Keys(object)) {
		for i := range t.NumField() {
			tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
			switch {
			case tag == "" || !strings.EqualFold(tag, key):
				continue
			case tag != key:
				return key, tag
			}
			if key, want := misspeltKey(object[key], t.Field(i).Type); key != "" {
				return key, want
			}
		}
	}
	return "", ""
}

// separateDirectories fails unless the roots and their outputs are separate
// directories, none of them another or inside another. With a root inside a
// root, or two roots compiling into one directory, a source has two places
// to compile into and an output two roots to be mirrored from: which one
// the build took depended on the order of a map, and an output a package
// commits could be rewritten or removed by the build of the root beside it.
func separateDirectories(roots map[string]string) error {
	type directory struct {
		path string
		root string // the root it is the output of; "" for a root
	}
	// The roots first, then their outputs, each by name: the error names
	// the same pair at every run, and two roots before a root's output.
	var dirs []directory
	sources := slices.Sorted(maps.Keys(roots))
	for _, src := range sources {
		dirs = append(dirs, directory{path: src})
	}
	for _, src := range sources {
		dirs = append(dirs, directory{path: roots[src], root: src})
	}
	for i, a := range dirs {
		for _, b := range dirs[i+1:] {
			inner, outer := b.path, a.path
			if _, ok := under(a.path, b.path); ok {
				inner, outer = a.path, b.path
			}
			_, inside := under(inner, outer)
			switch {
			case a.path == b.path && a.root != "":
				return fmt.Errorf("the roots %q and %q have one output, %q: each root compiles into a directory of its own", a.root, b.root, a.path)
			case a.path == b.path:
				return fmt.Errorf("%q is a root and the output of %q: roots and their outputs are separate directories", a.path, b.root)
			case inside:
				return fmt.Errorf("%q lies inside %q: roots and their outputs are separate directories, none inside another, so that every file compiles into one place", inner, outer)
			}
		}
	}
	return nil
}

// isClassName reports whether s is a fully qualified class name: identifiers
// joined by backslashes.
func isClassName(s string) bool {
	for _, part := range strings.Split(s, "\\") {
		if part == "" {
			return false
		}
		for i, r := range part {
			if !(r == '_' || r >= 0x80 || 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || i > 0 && '0' <= r && r <= '9') {
				return false
			}
		}
	}
	return true
}

// rebased is the project with its roots, committed roots and excludes made
// relative to the working directory, for a project file in dir: an
// installed package's.
func (p *project) rebased(dir string) *project {
	q := *p
	q.Roots = make(map[string]string, len(p.Roots))
	for src, out := range p.Roots {
		q.Roots[filepath.Join(dir, src)] = filepath.Join(dir, out)
	}
	q.Committed = make([]string, len(p.Committed))
	for i, src := range p.Committed {
		q.Committed[i] = filepath.Join(dir, src)
	}
	q.Exclude = make([]string, len(p.Exclude))
	for i, e := range p.Exclude {
		q.Exclude[i] = filepath.Join(dir, e)
	}
	return &q
}

// commits reports whether src is a root whose outputs the project commits.
func (p *project) commits(src string) bool {
	return slices.Contains(p.Committed, src)
}

// missingOutputs lists what the output directory of the root src lacks of
// its compiled files, sorted: the directory itself when there is none, else
// the .php of each .phn the build would compile. It asks for presence only,
// so it needs no program: whether an output is current is for the project's
// own gate to say (design/tooling.md §3.7).
func (p *project) missingOutputs(src string) []string {
	out := p.Roots[src]
	if st, err := os.Stat(out); err != nil || !st.IsDir() {
		return []string{out}
	}
	var missing []string
	for _, in := range collect([]string{src}, p.isOutput, ".phn") {
		dst := p.outputPath(in)
		if st, err := os.Stat(dst); err != nil || st.IsDir() {
			missing = append(missing, dst)
		}
	}
	sort.Strings(missing)
	return missing
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

// roots lists the source roots present in this tree, in a stable order. A
// root the project file names but the tree lacks is left out rather than
// failed on: a dist archive, or an image's build context, leaves out tests/
// and is still the same project.
func (p *project) roots() []string {
	roots := make([]string, 0, len(p.Roots))
	for src := range p.Roots {
		if st, err := os.Stat(src); err == nil && st.IsDir() {
			roots = append(roots, src)
		}
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
		if rel == stampFile {
			return nil // the build's own record (vendor.go)
		}
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
