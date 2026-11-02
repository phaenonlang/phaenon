// Package linemap reads and writes the line map every compiled file ends
// with (design/line-map.md §3): a comment line of runs and holds that
// tells, for each line of the .php, the line of the .phn it came from.
//
//	// phaenon-map v1 ../../src/Domain/Foo.phn 1-6=1 7:1 15-17=9 18:9
//
// A run `O:S` maps output line O to source line S and each following line
// to the next source line, up to the next entry; a hold `O-P=S` maps lines
// O to P to S. Runs are the targets of the reverse lookup, holds never are:
// they are the lines the compiler wrote (the header, an import, a docblock,
// a brace) or continuations of a source line, where a breakpoint must not
// land.
//
// The format has three readers — this package, the PhpStorm plugin and
// phaenon/runtime — and one writer, the emitter. testdata/vectors.json is
// the reference all three test against; a map this package refuses is one
// the others must refuse too.
package linemap

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Version is the format this package reads and writes.
const Version = "v1"

const (
	prefix     = "// phaenon-map "
	htmlPrefix = "<?php // phaenon-map "
)

// ErrUnsupported marks a map line of a version this package does not know.
// A reader treats it as no map at all: the file's positions stay as they are.
var ErrUnsupported = errors.New("linemap: unsupported version")

// Entry is a run (Hold false: output line Out is source line Src, the lines
// after it the next source lines) or a hold (Hold true: output lines Out to
// End are all source line Src).
type Entry struct {
	Out  int
	End  int // last output line of a hold; equal to Out for a run
	Src  int
	Hold bool
}

func (e Entry) String() string {
	if e.Hold {
		return fmt.Sprintf("%d-%d=%d", e.Out, e.End, e.Src)
	}
	return fmt.Sprintf("%d:%d", e.Out, e.Src)
}

// Map is a parsed line map.
type Map struct {
	// Source is the .phn path relative to the compiled file's directory,
	// with `/` separators, decoded.
	Source  string
	Entries []Entry
}

// Line is one output line as the emitter classifies it: the source line it
// stands for, and whether it is a reverse target (a line holding source
// code the line starts with) or a hold.
type Line struct {
	Src    int
	Target bool
}

// Build encodes classified output lines (line 1 first) as a map for source.
func Build(source string, lines []Line) *Map {
	m := &Map{Source: source}
	for i, l := range lines {
		o := i + 1
		n := len(m.Entries)
		if l.Target {
			if n > 0 && !m.Entries[n-1].Hold && m.Entries[n-1].Src+(o-m.Entries[n-1].Out) == l.Src {
				continue // the run goes on
			}
			m.Entries = append(m.Entries, Entry{Out: o, End: o, Src: l.Src})
			continue
		}
		if n > 0 && m.Entries[n-1].Hold && m.Entries[n-1].Src == l.Src && m.Entries[n-1].End == o-1 {
			m.Entries[n-1].End = o
			continue
		}
		m.Entries = append(m.Entries, Entry{Out: o, End: o, Src: l.Src, Hold: true})
	}
	return m
}

// String is the map's comment line, without a line break.
func (m *Map) String() string {
	var b strings.Builder
	b.WriteString(prefix + Version + " " + EncodePath(m.Source))
	for _, e := range m.Entries {
		b.WriteByte(' ')
		b.WriteString(e.String())
	}
	return b.String()
}

// EncodePath writes a source path for the map: a percent sign as %25, a
// space as %20, so that the path is one token.
func EncodePath(p string) string {
	return strings.ReplaceAll(strings.ReplaceAll(p, "%", "%25"), " ", "%20")
}

// decodePath reverses EncodePath; any other percent sequence is invalid,
// since the writer never produces one.
func decodePath(p string) (string, error) {
	if !strings.Contains(p, "%") {
		return p, nil
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] != '%' {
			b.WriteByte(p[i])
			continue
		}
		switch {
		case strings.HasPrefix(p[i:], "%20"):
			b.WriteByte(' ')
		case strings.HasPrefix(p[i:], "%25"):
			b.WriteByte('%')
		default:
			return "", fmt.Errorf("linemap: invalid escape in path %q", p)
		}
		i += 2
	}
	return b.String(), nil
}

// IsMapLine reports whether line is spelled as a map comment, whatever its
// version or validity.
func IsMapLine(line string) bool {
	return strings.HasPrefix(line, prefix) || strings.HasPrefix(line, htmlPrefix)
}

// Parse reads one map line (without its line break). It returns
// ErrUnsupported for a version other than v1 and an error for a line that
// breaks the format's rules (§3.1): tokens separated by single spaces, a
// path, at least one entry, the first entry at output line 1, entries in
// ascending order without overlap, the entry after a hold O-P=S at P+1,
// every number at least 1.
func Parse(line string) (*Map, error) {
	var rest string
	switch {
	case strings.HasPrefix(line, prefix):
		rest = line[len(prefix):]
	case strings.HasPrefix(line, htmlPrefix):
		rest = line[len(htmlPrefix):]
	default:
		return nil, errors.New("linemap: not a map line")
	}
	fields := strings.Split(rest, " ")
	for _, f := range fields {
		if f == "" {
			return nil, fmt.Errorf("linemap: tokens must be separated by single spaces: %q", line)
		}
	}
	if fields[0] != Version {
		return nil, ErrUnsupported
	}
	if len(fields) < 3 {
		return nil, fmt.Errorf("linemap: a map needs a path and at least one entry: %q", line)
	}
	src, err := decodePath(fields[1])
	if err != nil {
		return nil, err
	}
	m := &Map{Source: src}
	next := 1 // the line the next entry must start at (exactly, after a hold) or after
	exact := true
	for _, f := range fields[2:] {
		e, err := parseEntry(f)
		if err != nil {
			return nil, err
		}
		switch {
		case exact && e.Out != next:
			return nil, fmt.Errorf("linemap: entry %q must start at output line %d", f, next)
		case !exact && e.Out < next:
			return nil, fmt.Errorf("linemap: entry %q does not ascend", f)
		}
		if e.Hold {
			next, exact = e.End+1, true
		} else {
			next, exact = e.Out+1, false
		}
		m.Entries = append(m.Entries, e)
	}
	return m, nil
}

func parseEntry(f string) (Entry, error) {
	if i := strings.IndexByte(f, ':'); i >= 0 {
		o, err1 := number(f[:i])
		s, err2 := number(f[i+1:])
		if err1 != nil || err2 != nil {
			return Entry{}, fmt.Errorf("linemap: invalid run %q", f)
		}
		return Entry{Out: o, End: o, Src: s}, nil
	}
	dash, eq := strings.IndexByte(f, '-'), strings.IndexByte(f, '=')
	if dash < 0 || eq < dash {
		return Entry{}, fmt.Errorf("linemap: invalid entry %q", f)
	}
	o, err1 := number(f[:dash])
	p, err2 := number(f[dash+1 : eq])
	s, err3 := number(f[eq+1:])
	if err1 != nil || err2 != nil || err3 != nil || p < o {
		return Entry{}, fmt.Errorf("linemap: invalid hold %q", f)
	}
	return Entry{Out: o, End: p, Src: s, Hold: true}, nil
}

// number reads a line number: decimal digits only, at least 1.
func number(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("not a number")
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, errors.New("out of range")
	}
	return n, nil
}

// Find returns the map a compiled file carries: on its last line, or, in a
// file that uses __halt_compiler(), on the line before that statement. It
// returns nil and no error when the file has no map line, nil and
// ErrUnsupported for a map of another version, and nil and an error for a
// malformed map.
func Find(file []byte) (*Map, error) {
	var lastErr error
	if line := lastLine(file); IsMapLine(line) {
		m, err := Parse(line)
		if err == nil {
			return m, nil
		}
		lastErr = err // unless the last line is data after __halt_compiler()
	}
	// After __halt_compiler() comes data, so the map is the line before it.
	for i := 0; i < len(file); {
		end := bytes.IndexByte(file[i:], '\n')
		if end < 0 {
			break
		}
		line := strings.TrimSuffix(string(file[i:i+end]), "\r")
		next := i + end + 1
		if IsMapLine(line) && haltsNext(file[next:]) {
			return Parse(line)
		}
		i = next
	}
	return nil, lastErr
}

// lastLine is the file's last non-empty line, a trailing line break and
// carriage return removed.
func lastLine(file []byte) string {
	file = bytes.TrimRight(file, "\r\n")
	if i := bytes.LastIndexByte(file, '\n'); i >= 0 {
		file = file[i+1:]
	}
	return strings.TrimSuffix(string(file), "\r")
}

// haltsNext reports whether the text starts with a __halt_compiler call,
// leading whitespace aside.
func haltsNext(rest []byte) bool {
	rest = bytes.TrimLeft(rest, " \t")
	const kw = "__halt_compiler"
	return len(rest) >= len(kw) && strings.EqualFold(string(rest[:len(kw)]), kw)
}

// Forward is the source line of output line out: the entry with the largest
// start not above out decides; a run adds the distance, a hold does not.
// ok is false for a line before line 1.
func (m *Map) Forward(out int) (src int, ok bool) {
	if out < 1 || len(m.Entries) == 0 {
		return 0, false
	}
	i := m.entryAt(out)
	e := m.Entries[i]
	if e.Hold {
		return e.Src, true
	}
	return e.Src + out - e.Out, true
}

// entryAt is the index of the entry with the largest start not above out.
func (m *Map) entryAt(out int) int {
	lo, hi := 0, len(m.Entries)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if m.Entries[mid].Out <= out {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// Reverse is the output line a breakpoint on source line src goes to: the
// first output line of a run that covers src. A source line no run covers
// (one the compiler folded into another, or a line of generated text only)
// takes the next source line that has a target; ok is false when there is
// none.
func (m *Map) Reverse(src int) (out int, ok bool) {
	if src < 1 {
		return 0, false
	}
	best := 0
	for i, e := range m.Entries {
		if e.Hold {
			continue
		}
		last := -1 // the run's last source line; -1: it runs to the end
		if i+1 < len(m.Entries) {
			last = e.Src + m.Entries[i+1].Out - e.Out - 1
		}
		if src >= e.Src && (last < 0 || src <= last) {
			return e.Out + src - e.Src, true
		}
		if e.Src > src && (best == 0 || e.Src < best) {
			best = e.Src
		}
	}
	if best == 0 {
		return 0, false
	}
	return m.Reverse(best)
}
