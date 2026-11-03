// Package trace rewrites the positions in text — a stack trace, PHPUnit's
// failures, PHPStan's raw findings, a log — that point into compiled files
// carrying a line map, so that they point into the .phn sources instead
// (design/line-map.md §4.1, `phn trace`).
//
// A position is a path ending in .php followed by `:42`, `(42)`
// (Throwable::getTraceAsString()) or ` on line 42` (PHP's warnings and
// uncaught exceptions). The file is read — through the roots, which map a
// production path prefix to the local checkout — and when its last line is
// a valid map, the path becomes the source's and the line the forward
// lookup's. Everything else, positions in files without a map included,
// stays byte for byte as it was.
//
// The CI formats are read too (design/tooling.md §7.1): a GitHub workflow
// command that annotates a compiled file (Command, PHPStan's
// --error-format=github), and the XML reports of Report — Checkstyle,
// JUnit, Clover and Cobertura — whose files, lines and coverage it carries
// over to the sources.
package trace

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/linemap"
)

// Root maps a path prefix as the text spells it (a production deployment's
// `/app`) to a local directory (`.`).
type Root struct {
	Prefix, Local string
}

// ParseRoot reads a `PREFIX=LOCAL` flag value.
func ParseRoot(s string) (Root, bool) {
	prefix, local, ok := strings.Cut(s, "=")
	if !ok || prefix == "" || local == "" {
		return Root{}, false
	}
	return Root{Prefix: filepath.Clean(prefix), Local: filepath.Clean(local)}, true
}

// Tracer rewrites positions, reading each compiled file once.
type Tracer struct {
	roots []Root
	// ReadFile reads a compiled file; os.ReadFile unless a test sets it.
	ReadFile func(string) ([]byte, error)
	maps     map[string]*linemap.Map // by local path; nil: no map
}

// New returns a tracer with the given roots, the longest prefix tried first.
func New(roots []Root) *Tracer {
	rs := append([]Root(nil), roots...)
	sort.SliceStable(rs, func(i, j int) bool { return len(rs[i].Prefix) > len(rs[j].Prefix) })
	return &Tracer{roots: rs, ReadFile: os.ReadFile, maps: map[string]*linemap.Map{}}
}

// position matches a compiled file's path and the line after it. The path
// holds no whitespace, quotes, brackets, commas, semicolons or colons (a
// Windows drive letter aside), so that the text around it stays out.
var position = regexp.MustCompile(`((?:[A-Za-z]:)?[^\s"'()<>\[\],;:]*\.php)(?::(\d+)|\((\d+)\)|( on line )(\d+))`)

// Rewrite returns text with every position in a mapped compiled file
// rewritten to its source position. A line that is a GitHub workflow
// command annotating a mapped file (PHPStan's --error-format=github) has its
// file rewritten to the source's AnnotationPath and its lines to the
// source's, its columns dropped: the map holds lines, and a compiled line's
// column is not the source's.
func (t *Tracer) Rewrite(text string) string {
	if !strings.Contains(text, "::") {
		return t.rewritePositions(text)
	}
	lines := strings.SplitAfter(text, "\n")
	for i, l := range lines {
		body := strings.TrimSuffix(l, "\n")
		brk := l[len(body):]
		if strings.HasSuffix(body, "\r") {
			body, brk = body[:len(body)-1], "\r"+brk
		}
		if c, ok := ParseCommand(body); ok {
			if r, ok := t.rewriteCommand(c); ok {
				lines[i] = r + brk
				continue
			}
		}
		lines[i] = t.rewritePositions(l)
	}
	return strings.Join(lines, "")
}

// rewritePositions rewrites the positions of free text.
func (t *Tracer) rewritePositions(text string) string {
	return position.ReplaceAllStringFunc(text, func(match string) string {
		g := position.FindStringSubmatch(match)
		path := g[1]
		var digits, before, after string
		switch {
		case g[2] != "":
			digits, before = g[2], ":"
		case g[3] != "":
			digits, before, after = g[3], "(", ")"
		default:
			digits, before = g[5], g[4]
		}
		n, err := strconv.Atoi(digits)
		if err != nil {
			return match
		}
		local := t.local(path)
		m := t.mapOf(local)
		if m == nil {
			return match
		}
		src, ok := m.Forward(n)
		if !ok {
			return match
		}
		return source(local, m) + before + strconv.Itoa(src) + after
	})
}

// rewriteCommand is the annotation c points at the source, false when its
// file has no map.
func (t *Tracer) rewriteCommand(c Command) (string, bool) {
	file, ok := c.Get("file")
	if !ok {
		return "", false
	}
	local := t.local(file)
	m := t.mapOf(local)
	if m == nil {
		return "", false
	}
	out := Command{Name: c.Name, Message: t.rewritePositions(c.Message)}
	for _, p := range c.Props {
		switch p.Key {
		case "file":
			p.Value = AnnotationPath(source(local, m))
		case "line", "endLine":
			p.Value = forward(m, p.Value)
		case "col", "endColumn":
			continue
		}
		out.Props = append(out.Props, p)
	}
	return out.String(), true
}

// source is the path of the source a compiled file's map names, beside the
// compiled file at local.
func source(local string, m *linemap.Map) string {
	return filepath.Clean(filepath.Join(filepath.Dir(local), filepath.FromSlash(m.Source)))
}

// forward is the source line of the compiled line the decimal digits name,
// the digits themselves when they name no line.
func forward(m *linemap.Map, digits string) string {
	n, err := strconv.Atoi(digits)
	if err != nil {
		return digits
	}
	if src, ok := m.Forward(n); ok {
		return strconv.Itoa(src)
	}
	return digits
}

// local is where a path the text names lies on this machine: under the
// local directory of the longest root whose prefix it starts with, a whole
// segment at a time; as written otherwise.
func (t *Tracer) local(path string) string {
	for _, r := range t.roots {
		if path == r.Prefix {
			return r.Local
		}
		prefix := r.Prefix
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		if rest, ok := strings.CutPrefix(path, prefix); ok {
			return filepath.Join(r.Local, rest)
		}
	}
	return path
}

// mapOf is the line map of the compiled file at path, nil when it cannot be
// read or carries no valid map.
func (t *Tracer) mapOf(path string) *linemap.Map {
	if m, seen := t.maps[path]; seen {
		return m
	}
	var m *linemap.Map
	if data, err := t.ReadFile(path); err == nil {
		m, _ = linemap.Find(data)
	}
	t.maps[path] = m
	return m
}
