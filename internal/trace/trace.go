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
// rewritten to its source position.
func (t *Tracer) Rewrite(text string) string {
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
		source := filepath.Clean(filepath.Join(filepath.Dir(local), filepath.FromSlash(m.Source)))
		return source + before + strconv.Itoa(src) + after
	})
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
