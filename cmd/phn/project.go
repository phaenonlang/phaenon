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
	"strconv"
	"strings"
	"unicode/utf8"

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
	// committed root as any other, but that a build keeps no stamp there
	// (mirror).
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
	// ignored lists, sorted, the keys of an installed package's file that
	// this compiler does not know and has passed over (readInstalledProject).
	ignored []string
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

// readProject reads and validates the project file of the project the
// compiler works on; nil, nil when there is none. Its roots and excludes
// are relative to the file's directory. A key it does not know is an
// error: the file is its reader's own, a key misspelt there would
// otherwise be passed over in silence, and its author can put it right.
func readProject(path string) (*project, error) {
	return readProjectFile(path, false)
}

// readInstalledProject reads the project file of an installed package,
// which differs from readProject in one thing (design/tooling.md T19): a
// key of the file's object that this compiler does not know is passed
// over, and listed in the project's ignored. The project that installed
// the package cannot change its file, and a refusal there would be a
// warning at each build and a library that is not built at all; that an
// older compiler does not meet a key it must not ignore is for the
// library's Composer conflict on phaenon/phn to see to, as it is for
// syntax an older compiler cannot parse.
//
// Everything else is refused as in a project's own file: a known key with
// a value it cannot have, a key under a known key that is not known (a
// change that must never be ignored is made there, as a new shape of a
// known key's value), a known key in another letter case, a key written
// twice, text after the object, roots that overlap.
func readInstalledProject(path string) (*project, error) {
	return readProjectFile(path, true)
}

func readProjectFile(path string, installed bool) (*project, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := project{data: data, path: path}
	// A project file is one JSON object in UTF-8 whose keys are spelt as
	// documented, each written once. encoding/json takes more: bytes that
	// are no UTF-8 and half a surrogate pair, read as U+FFFD; the text
	// null, read as an empty object, and null in a list of strings, read
	// as ""; a key in another letter case, or written twice, the later
	// taken or the two merged. Each is refused here, by name. The Composer
	// plugin reads the file of an installed package too
	// (composer/src/ProjectFile.php), with PHP's decoder, which refuses
	// most of these and more: a key that begins with U+0000, and a value
	// nested 512 deep. The two must give one answer, so those are refused
	// as well; they are nothing a project file has reason to hold.
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%s: the project file is not UTF-8 text", path)
	}
	first := json.NewDecoder(bytes.NewReader(data))
	var object json.RawMessage
	if err := first.Decode(&object); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if _, err := first.Token(); err != io.EOF {
		return nil, fmt.Errorf("%s: the project file is one JSON object, and text follows it", path)
	}
	if bytes.Equal(bytes.TrimSpace(object), []byte("null")) {
		return nil, fmt.Errorf("%s: the project file is one JSON object, and this is null", path)
	}
	if halfSurrogate(object) {
		return nil, fmt.Errorf(`%s: a string has half of a surrogate pair, written as a \u escape`, path)
	}
	if reason := unreadableShape(object); reason != "" {
		return nil, fmt.Errorf("%s: %s", path, reason)
	}
	text, unknown := withoutUnknownKeys(object, reflect.TypeFor[project]())
	switch {
	case installed:
		p.ignored = unknown
	case len(unknown) > 0:
		return nil, unknownKey(path, unknown[0])
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		// A key under a known key that the decoder does not know. It is
		// not passed over, in an installed package either, and the error
		// says where it is.
		if _, isKey := strings.CutPrefix(err.Error(), "json: unknown field "); isKey {
			if key := unknownNestedKey(text, reflect.TypeFor[project](), ""); key != "" {
				return nil, unknownKey(path, key)
			}
		}
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if key, want := misspeltKey(object, reflect.TypeFor[project]()); key != "" {
		return nil, fmt.Errorf("%s: the key %q is written %q", path, key, want)
	}
	var lists struct {
		Exclude []*string `json:"exclude"`
		Fmt     []*string `json:"fmt"`
	}
	if json.Unmarshal(text, &lists) == nil {
		for _, list := range []struct {
			key      string
			elements []*string
		}{{"exclude", lists.Exclude}, {"fmt", lists.Fmt}} {
			if slices.Contains(list.elements, nil) {
				return nil, fmt.Errorf("%s: %q lists strings, and null is not one", path, list.key)
			}
		}
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

// unknownKey is the error of a key the compiler does not know; a key under
// a known key is named with it, as in unused.later. It says which phn has
// read the file, since that is not always the one the project pins: an
// editor's language server from PATH, or PHN_BINARY, may be an older
// release, and to its reader a key of a newer one looks misspelt.
func unknownKey(path, key string) error {
	return fmt.Errorf("%s: unknown key %s (this is phn %s; a newer release may know it)", path, quoted(key), version)
}

// withoutUnknownKeys returns the JSON object without the members whose keys
// name no field of the struct type t, in whatever letter case, the others
// in the order of the file, and those keys, sorted. A key that names a
// field in another letter case stays, for misspeltKey to refuse. Text that
// is no object is returned as it is, for the decoder to refuse.
func withoutUnknownKeys(object []byte, t reflect.Type) (known []byte, unknown []string) {
	dec := json.NewDecoder(bytes.NewReader(object))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return object, nil
	}
	members := []byte{'{'}
	for dec.More() {
		name, err := dec.Token()
		key, isKey := name.(string)
		var value json.RawMessage
		if err != nil || !isKey || dec.Decode(&value) != nil {
			return object, nil
		}
		named := false
		for i := range t.NumField() {
			tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
			named = named || tag != "" && strings.EqualFold(tag, key)
		}
		if !named {
			unknown = append(unknown, key)
			continue
		}
		spelt, err := json.Marshal(key)
		if err != nil {
			return object, nil
		}
		if len(members) > 1 {
			members = append(members, ',')
		}
		members = append(append(append(members, spelt...), ':'), value...)
	}
	if len(unknown) == 0 {
		return object, nil
	}
	sort.Strings(unknown)
	return append(members, '}'), unknown
}

// unknownNestedKey finds, in the JSON object data read into a struct of
// type t, a key under a known key that names no field there, and returns
// it with the keys above it, as in unused.later; "" when there is none.
func unknownNestedKey(data []byte, t reflect.Type, above string) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var object map[string]json.RawMessage
	if t.Kind() != reflect.Struct || json.Unmarshal(data, &object) != nil {
		return ""
	}
	for _, key := range slices.Sorted(maps.Keys(object)) {
		named := false
		for i := range t.NumField() {
			tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
			if tag == "" || !strings.EqualFold(tag, key) {
				continue
			}
			named = true
			if nested := unknownNestedKey(object[key], t.Field(i).Type, above+key+"."); nested != "" {
				return nested
			}
		}
		if !named && above != "" {
			return above + key
		}
	}
	return ""
}

// unreadableShape finds, in JSON text the decoder has read, what a project
// file may not hold at any depth, and says it: a key that an object has
// twice, a key that begins with U+0000, a value nested too deep. The last
// two are what PHP's decoder refuses, at its own depth: 511 values, each
// inside the one before it, are read, and one more is not.
func unreadableShape(data []byte) string {
	const deepest = 511
	dec := json.NewDecoder(bytes.NewReader(data))
	var value func(depth int) string
	value = func(depth int) string {
		token, err := dec.Token()
		if err != nil {
			return ""
		}
		delim, opens := token.(json.Delim)
		if !opens {
			return ""
		}
		if depth > deepest {
			return fmt.Sprintf("the project file is nested deeper than %d values", deepest)
		}
		seen := map[string]bool{}
		for dec.More() {
			if delim == '{' {
				name, err := dec.Token()
				key, isKey := name.(string)
				switch {
				case err != nil || !isKey:
					return ""
				case seen[key]:
					return fmt.Sprintf("the key %s is written twice", quoted(key))
				case strings.HasPrefix(key, "\x00"):
					return "a key begins with U+0000"
				}
				seen[key] = true
			}
			if reason := value(depth + 1); reason != "" {
				return reason
			}
		}
		dec.Token()
		return ""
	}
	return value(1)
}

// halfSurrogate reports whether a string of the JSON text data, which the
// decoder has read, holds a \u escape of a surrogate that is not half of a
// pair: a high one with no low one after it, or a low one alone. The
// decoder reads it as U+FFFD, and PHP's refuses the text.
func halfSurrogate(data []byte) bool {
	escape := func(at int) (value uint64, ok bool) {
		if at+6 > len(data) || data[at] != '\\' || data[at+1] != 'u' {
			return 0, false
		}
		value, err := strconv.ParseUint(string(data[at+2:at+6]), 16, 32)
		return value, err == nil
	}
	inString := false
	for i := 0; i < len(data); i++ {
		switch c := data[i]; {
		case !inString:
			inString = c == '"'
		case c == '"':
			inString = false
		case c == '\\':
			value, isUnicode := escape(i)
			low, paired := escape(i + 6)
			switch {
			case !isUnicode:
				i++ // the character escaped, a quote or a backslash among them
			case value >= 0xD800 && value <= 0xDBFF && paired && low >= 0xDC00 && low <= 0xDFFF:
				i += 11
			case value >= 0xD800 && value <= 0xDFFF:
				return true
			default:
				i += 5
			}
		}
	}
	return false
}

// quoted writes a key for a message as both readers of a project file
// write it (the Composer plugin's ProjectFile::quoted): between quotes, a
// quote and a backslash after a backslash, and what would not show, or
// would move the cursor, as \u and four digits (unprintable).
func quoted(key string) string {
	return `"` + unprintable(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(key)) + `"`
}

// unprintable writes text for one line of a terminal: the control
// characters, the line and paragraph separators, and the characters that
// have no width or turn the direction of the text, as \u and four digits.
// Text that is not UTF-8, as a file's name may be, keeps its other bytes.
func unprintable(text string) string {
	hidden := func(r rune) bool {
		return r < 0x20 || r >= 0x7F && r <= 0x9F || r >= 0x200B && r <= 0x200F || r >= 0x2028 && r <= 0x202E || r >= 0x2060 && r <= 0x206F || r == 0xFEFF
	}
	var b strings.Builder
	if !utf8.ValidString(text) {
		for _, c := range []byte(text) {
			if c < 0x20 || c == 0x7F {
				fmt.Fprintf(&b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
		return b.String()
	}
	for _, r := range text {
		if hidden(r) {
			fmt.Fprintf(&b, `\u%04x`, r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
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
//
// The stamp of a build as an installed package (.phn-build, vendor.go) is
// spared, in a root the project does not commit: a project linked into
// another as a path repository is built there, and the stamp is that
// build's record. In a root the project commits it is removed with the
// files that have no source. No build writes one there: an installed
// package's committed roots are left as installed. One found there is what
// a build left before the root was committed, and were it to stay, a
// release could track it, and every project that installs that release as
// a checkout would take it for the mark of a build made there (earlier.go).
// Removed, a tracked stamp shows in `git status`, so the library's gate,
// which is the state of the tree (design/tooling.md §3.7), fails until the
// removal is committed.
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
		if rel == stampFile && !p.commits(src) {
			return nil // the record of a build as an installed package (vendor.go)
		}
		for _, source := range sourcesBehind(rel) {
			if _, err := os.Stat(filepath.Join(src, source)); err == nil {
				return nil
			}
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

// sourcesBehind names, relative to a root, the files that can stand behind
// the file rel of the root's output directory: the .phn a .php is compiled
// from, and the file of the same path, which the build copies. An output
// with neither is one no source stands behind. The build removes it
// (mirror), and so does `phn build --vendor` where an earlier build left
// it in a root the package now commits (earlier.go): the two ask this one
// function.
func sourcesBehind(rel string) []string {
	if filepath.Ext(rel) == ".php" {
		return []string{strings.TrimSuffix(rel, ".php") + ".phn", rel}
	}
	return []string{rel}
}

func writeFile(dst string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		fail("%v", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		fail("%v", err)
	}
}
