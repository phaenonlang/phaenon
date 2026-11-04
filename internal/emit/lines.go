package emit

import (
	"bytes"
	"sort"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/linemap"
)

// This file writes the output and classifies its lines for the line map
// (design/line-map.md §3). Every output byte has an origin: the source,
// passed through; an in-place edit, whose line breaks stand for the line
// breaks of the span it replaces, so its text is the rewritten form of the
// source lines it sits on; or a free edit, text the compiler lays out
// itself, whose line breaks are its own. An output line then stands for
//
//   - the source line of the first code byte on it that is source or
//     in-place text, and is that line's reverse target (a run line) when no
//     earlier output line was;
//   - otherwise, when it holds free text only, the source line the free
//     edit is anchored at (where it starts), as a hold;
//   - otherwise, blank, the source line that a source line break opens
//     there (a run line), or the anchor of the free line break that opened
//     it (a hold).
//
// So a docblock or an import the compiler writes, a brace it moves to the
// next line and the lines of an expanded expression body are holds, and a
// breakpoint set on their source line goes to the line that starts with
// its code.

// outLine is what the writer knows about one output line.
type outLine struct {
	code   int  // source line of the first source or in-place code byte, 0: none
	free   int  // anchor line of the first free code byte, 0: none
	opened int  // source line a source or in-place line break opened, 0: a free one
	anchor int  // anchor line of the free line break that opened the line
	isMap  bool // the line the map itself takes (before __halt_compiler)
}

// segment is a run of source bytes copied to the output unchanged: source
// offsets [Start, End) at output offset Out.
type segment struct {
	ast.Span
	Out int
}

// indentReq adds Text after every source line break in the span that
// does not end a blank line: the continuation lines of an expression body
// moved one level in.
type indentReq struct {
	ast.Span
	Text string
}

type lineWriter struct {
	src        []byte
	lineStarts []int
	indents    []indentReq
	out        bytes.Buffer
	lines      []outLine
	segments   []segment
	// moved are the source runs of free edits (edit.Pieces): verbatim, but
	// on the compiler's lines, so no line of their source holds them.
	moved []segment
	mapAt      int // output offset of the map line before __halt_compiler, -1: none
	// inserted bytes, the map line before __halt_compiler, went in at
	// output offset insertedAt after the segments were written.
	insertedAt, inserted int
}

func newLineWriter(src []byte, indents []indentReq) *lineWriter {
	w := &lineWriter{src: src, lineStarts: []int{0}, indents: indents, mapAt: -1}
	for i, c := range src {
		if c == '\n' {
			w.lineStarts = append(w.lineStarts, i+1)
		}
	}
	w.out.Grow(len(src) + 256)
	w.lines = []outLine{{anchor: 1}}
	return w
}

// lineOf is the 1-based source line holding offset off.
func (w *lineWriter) lineOf(off int) int {
	return sort.Search(len(w.lineStarts), func(i int) bool { return w.lineStarts[i] > off })
}

func (w *lineWriter) cur() *outLine { return &w.lines[len(w.lines)-1] }

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v' }

// source copies src[a:b), adding the indentation an indent request asks
// for after each line break it covers.
func (w *lineWriter) source(a, b int) {
	line := w.lineOf(a)
	from := a
	for i := a; i < b; i++ {
		c := w.src[i]
		switch {
		case c == '\n':
			line++
			w.lines = append(w.lines, outLine{opened: line})
			if add := w.indentAfter(i); add != "" {
				w.copySource(from, i+1)
				w.out.WriteString(add)
				from = i + 1
			}
		case !isBlank(c):
			if l := w.cur(); l.code == 0 {
				l.code = line
			}
		}
	}
	w.copySource(from, b)
}

func (w *lineWriter) copySource(a, b int) {
	if a >= b {
		return
	}
	w.segments = append(w.segments, segment{Span: ast.Span{Start: a, End: b}, Out: w.out.Len()})
	w.out.Write(w.src[a:b])
}

// indentAfter is the indentation to add after the source line break at i.
func (w *lineWriter) indentAfter(i int) string {
	if i+1 < len(w.src) && (w.src[i+1] == '\n' || w.src[i+1] == '\r') {
		return "" // a blank line stays blank
	}
	return w.indentFor(i)
}

// indentFor is the indentation an indent request adds after the line break
// at source offset i.
func (w *lineWriter) indentFor(i int) string {
	add := ""
	for _, r := range w.indents {
		if r.Start <= i && i < r.End {
			add += r.Text // an expression body inside another moves in twice
		}
	}
	return add
}

// inPlace writes the text of an in-place edit of span s: its line breaks,
// as many as the span's, stand for the span's own, in order.
func (w *lineWriter) inPlace(text string, s ast.Span) {
	line := w.lineOf(s.Start)
	nl := s.Start
	from := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\n':
			nl += bytes.IndexByte(w.src[nl:s.End], '\n') + 1
			line = w.lineOf(nl)
			w.lines = append(w.lines, outLine{opened: line})
			if i+1 < len(text) && text[i+1] != '\n' && text[i+1] != '\r' {
				if add := w.indentFor(nl - 1); add != "" {
					w.out.WriteString(text[from : i+1])
					w.out.WriteString(add)
					from = i + 1
				}
			}
		case !isBlank(c):
			if l := w.cur(); l.code == 0 {
				l.code = line
			}
		}
	}
	w.out.WriteString(text[from:])
}

// free writes compiler-laid-out text anchored at source offset at.
func (w *lineWriter) free(text string, at int) {
	w.freeWith(text, at, nil)
}

// freeWith is free for text holding source runs at pieces.
func (w *lineWriter) freeWith(text string, at int, pieces []piece) {
	for _, p := range pieces {
		w.moved = append(w.moved, segment{Span: p.Span, Out: w.out.Len() + p.Off})
	}
	w.out.WriteString(text)
	anchor := w.lineOf(at)
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\n':
			w.lines = append(w.lines, outLine{anchor: anchor})
		case !isBlank(c):
			if l := w.cur(); l.free == 0 {
				l.free = anchor
			}
		}
	}
}

// markMap reserves the current line, which must be empty, for the map
// (before __halt_compiler): what follows starts the next line.
func (w *lineWriter) markMap(at int) {
	w.mapAt = w.out.Len()
	w.cur().isMap = true
	w.lines = append(w.lines, outLine{anchor: w.lineOf(at)})
}

// classify turns the output lines into the map's lines. A final line that
// the output's last line break opened and nothing filled is not a line.
func (w *lineWriter) classify() []linemap.Line {
	lines := w.lines
	if n := len(lines); n > 1 && bytes.HasSuffix(w.out.Bytes(), []byte("\n")) {
		lines = lines[:n-1]
	}
	targeted := map[int]bool{}
	out := make([]linemap.Line, len(lines))
	for i, l := range lines {
		var ml linemap.Line
		switch {
		case l.isMap:
			ml = linemap.Line{Src: 1}
			if i > 0 {
				ml.Src = out[i-1].Src
			}
		case l.code > 0:
			ml = linemap.Line{Src: l.code, Target: !targeted[l.code]}
		case l.free > 0:
			ml = linemap.Line{Src: l.free}
		case l.opened > 0:
			ml = linemap.Line{Src: l.opened, Target: !targeted[l.opened]}
		default:
			ml = linemap.Line{Src: max(l.anchor, 1)}
		}
		if ml.Target {
			targeted[ml.Src] = true
		}
		out[i] = ml
	}
	return out
}

// verbatim is the segments as the final output holds them: past the map
// line written before __halt_compiler, and merged where one continues the
// other on both sides.
func (w *lineWriter) verbatim() []Verbatim {
	var vs []Verbatim
	all := w.segments
	if len(w.moved) > 0 {
		all = append(append([]segment{}, w.segments...), w.moved...)
		sort.SliceStable(all, func(i, j int) bool { return all[i].Out < all[j].Out })
	}
	for _, s := range all {
		v := Verbatim{Src: s.Start, Out: s.Out, Len: s.End - s.Start}
		if w.inserted > 0 && v.Out >= w.insertedAt {
			v.Out += w.inserted
		}
		if n := len(vs); n > 0 && vs[n-1].Out+vs[n-1].Len == v.Out && vs[n-1].Src+vs[n-1].Len == v.Src {
			vs[n-1].Len += v.Len
			continue
		}
		vs = append(vs, v)
	}
	return vs
}
