package lsp

import (
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
)

// text is a document's content with its line starts. LSP positions count a
// line's characters in UTF-16 code units; the compiler counts bytes.
type text struct {
	src    []byte
	starts []int
}

func newText(src []byte) *text {
	t := &text{src: src, starts: []int{0}}
	for i, b := range src {
		if b == '\n' {
			t.starts = append(t.starts, i+1)
		}
	}
	return t
}

// offset is the byte offset of a position; a position past a line's end is
// the line's end, past the last line the text's end.
func (t *text) offset(p Position) int {
	if p.Line < 0 {
		return 0
	}
	if p.Line >= len(t.starts) {
		return len(t.src)
	}
	off, end := t.starts[p.Line], len(t.src)
	if p.Line+1 < len(t.starts) {
		end = t.starts[p.Line+1] - 1 // the line break
	}
	for units := 0; off < end && units < p.Character; {
		r, size := utf8.DecodeRune(t.src[off:end])
		units += utf16Len(r)
		off += size
	}
	return off
}

// position is the position of a byte offset.
func (t *text) position(off int) Position {
	if off > len(t.src) {
		off = len(t.src)
	}
	if off < 0 {
		off = 0
	}
	line := sort.Search(len(t.starts), func(i int) bool { return t.starts[i] > off }) - 1
	units := 0
	for i := t.starts[line]; i < off; {
		r, size := utf8.DecodeRune(t.src[i:off])
		units += utf16Len(r)
		i += size
	}
	return Position{Line: line, Character: units}
}

func (t *text) rangeOf(start, end int) Range {
	return Range{Start: t.position(start), End: t.position(end)}
}

// lineCol is the offset of a 1-based line and byte column, as the
// checker's diagnostics give them.
func (t *text) lineCol(line, col int) int {
	if line < 1 {
		return 0
	}
	if line > len(t.starts) {
		return len(t.src)
	}
	off := t.starts[line-1] + col - 1
	if off > len(t.src) {
		return len(t.src)
	}
	return off
}

func (t *text) whole() Range { return t.rangeOf(0, len(t.src)) }

// utf16Len is the number of UTF-16 code units r takes.
func utf16Len(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

// utf16Count is the number of UTF-16 code units of s.
func utf16Count(s string) int {
	n := 0
	for _, r := range s {
		n += utf16Len(r)
	}
	return n
}

// pathOf is the file path of a file: URI.
func pathOf(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.FromSlash(p), true
}

// uriOf is the file: URI of an absolute path.
func uriOf(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // a Windows drive letter
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
