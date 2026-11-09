// Package fix carries the changes a PHP rewriting tool — php-cs-fixer,
// Rector, phpcbf — makes to compiled outputs back to the .phn sources they
// were compiled from (design/tooling.md §4, `phn fix`).
//
// The emitter copies most of a source to its output byte for byte, and
// those verbatim spans (emit.Verbatim) are where an output and its source
// are the same text. Diff compares an output as compiled with the output the
// tool left, anchored on the tokens both keep, and returns the changes as
// hunks. Carry applies to the source every hunk that lies within one
// verbatim span, at the offset the span maps it to, and every deletion
// whose ends do, and refuses the others:
// text the compiler wrote — a docblock, the `function` of a method declared
// without it, an erased type, an import it added — is the compiler's to
// lay out, and the rebuild writes it anew from the source. Plain PHP copied
// as written is one span, so every change of it is carried.
//
// A hunk is never split: one change of the tool is carried whole or not at
// all, so the source never holds half of an edit.
package fix

import (
	"bytes"
	"sort"

	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/token"
)

// Hunk is one change of an output: the bytes [Start, End) became Text.
type Hunk struct {
	Start, End int
	Text       string
}

// Diff returns the hunks that turn old into new, in order. The two are
// lexed as PHP and aligned on their tokens other than whitespace, so a
// change is the text between two tokens both keep: a respaced gap, a
// rewritten token or a run of them. Each hunk is narrowed to the bytes that
// differ, the common prefix taken first.
func Diff(old, new []byte) []Hunk {
	if bytes.Equal(old, new) {
		return nil
	}
	a, b := anchors(old), anchors(new)
	var hunks []Hunk
	pa, pb := 0, 0
	region := func(ea, eb int) {
		if !bytes.Equal(old[pa:ea], new[pb:eb]) {
			hunks = append(hunks, narrow(old[pa:ea], new[pb:eb], pa))
		}
	}
	for _, p := range match(old, new, a, b) {
		region(a[p[0]].Start, b[p[1]].Start)
		pa, pb = a[p[0]].End, b[p[1]].End
	}
	region(len(old), len(new))
	return hunks
}

// anchors are the tokens of a PHP file that may anchor the alignment:
// everything but whitespace and the end of the file.
func anchors(src []byte) []token.Token {
	var out []token.Token
	for _, t := range lexer.Tokenize(src, lexer.ModePHP) {
		if t.Kind != token.T_WHITESPACE && t.Kind != token.EOF {
			out = append(out, t)
		}
	}
	return out
}

// narrow is the hunk turning old into new at offset at, without the bytes
// the two begin or end with alike.
func narrow(old, new []byte, at int) Hunk {
	p := 0
	for p < len(old) && p < len(new) && old[p] == new[p] {
		p++
	}
	s := 0
	for s < len(old)-p && s < len(new)-p && old[len(old)-1-s] == new[len(new)-1-s] {
		s++
	}
	return Hunk{Start: at + p, End: at + len(old) - s, Text: string(new[p : len(new)-s])}
}

// maxEdits bounds the alignment's work: two token sequences that differ in
// more places are aligned on their common ends alone, and the middle is one
// hunk.
const maxEdits = 2000

// match aligns the anchor tokens of old and new, returning the index pairs
// of the tokens they keep, in order: a longest common subsequence of their
// texts, by Myers' algorithm.
func match(old, new []byte, a, b []token.Token) [][2]int {
	ta, tb := texts(old, a), texts(new, b)
	pre := 0
	for pre < len(ta) && pre < len(tb) && ta[pre] == tb[pre] {
		pre++
	}
	suf := 0
	for suf < len(ta)-pre && suf < len(tb)-pre && ta[len(ta)-1-suf] == tb[len(tb)-1-suf] {
		suf++
	}
	var pairs [][2]int
	for i := range pre {
		pairs = append(pairs, [2]int{i, i})
	}
	for _, p := range myers(ta[pre:len(ta)-suf], tb[pre:len(tb)-suf]) {
		pairs = append(pairs, [2]int{p[0] + pre, p[1] + pre})
	}
	for i := suf; i > 0; i-- {
		pairs = append(pairs, [2]int{len(ta) - i, len(tb) - i})
	}
	return pairs
}

func texts(src []byte, toks []token.Token) []string {
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = string(src[t.Start:t.End])
	}
	return out
}

// myers returns the index pairs of a longest common subsequence of a and b,
// none when they differ in more than maxEdits places.
func myers(a, b []string) [][2]int {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return nil
	}
	limit := min(n+m, maxEdits)
	off := limit + 1
	v := make([]int32, 2*off+1)
	var trace [][]int32 // v[off-d .. off+d] before step d
	for d := 0; d <= limit; d++ {
		trace = append(trace, append([]int32(nil), v[off-d:off+d+1]...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || k != d && v[off+k-1] < v[off+k+1] {
				x = int(v[off+k+1])
			} else {
				x = int(v[off+k-1]) + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x, y = x+1, y+1
			}
			v[off+k] = int32(x)
			if x >= n && y >= m {
				return backtrack(a, b, trace)
			}
		}
	}
	return nil
}

// backtrack walks the trace back from the end to the start, collecting
// the diagonal moves: the matched pairs. trace[d] is the furthest x of each
// diagonal before step d, diagonal k at index k+d.
func backtrack(a, b []string, trace [][]int32) [][2]int {
	x, y := len(a), len(b)
	var pairs [][2]int
	for d := len(trace) - 1; d > 0; d-- {
		vd := trace[d]
		at := func(k int) int { return int(vd[k+d]) }
		k := x - y
		pk := k - 1
		if k == -d || k != d && at(k-1) < at(k+1) {
			pk = k + 1
		}
		px := at(pk)
		py := px - pk
		for x > px && y > py {
			x, y = x-1, y-1
			pairs = append(pairs, [2]int{x, y})
		}
		x, y = px, py
	}
	for x > 0 && y > 0 {
		x, y = x-1, y-1
		pairs = append(pairs, [2]int{x, y})
	}
	for i, j := 0, len(pairs)-1; i < j; i, j = i+1, j-1 {
		pairs[i], pairs[j] = pairs[j], pairs[i]
	}
	return pairs
}

// Carry applies to src, the source old was compiled from with the verbatim
// spans given, every hunk of old that lies within one span, and returns the
// new source with the hunks it refused, in order. A hunk that touches text
// the compiler wrote is refused, unless the same change can be made where
// it does not: an inserted or deleted run of a repeated byte, as in
// indentation, slides to the left until it lies within a span. The spans
// a folded pipe moves put source text out of order in the output, so the
// placed hunks are applied in the source's order, and one that would
// change source text another changes already is refused.
func Carry(src, old []byte, spans []emit.Verbatim, hunks []Hunk) ([]byte, []Hunk) {
	type edit struct {
		start, end int
		text       string
		hunk       Hunk
	}
	var edits []edit
	var refused []Hunk
	for _, h := range hunks {
		placed := false
		for _, across := range []bool{false, true} {
			for _, c := range placements(old, h) {
				if s, e, in := place(spans, c, across); in {
					edits = append(edits, edit{s, e, c.Text, h})
					placed = true
					break
				}
			}
			if placed {
				break
			}
		}
		if !placed {
			refused = append(refused, h)
		}
	}
	if len(edits) == 0 {
		return src, refused
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out bytes.Buffer
	pos := 0
	for i, e := range edits {
		if e.start < pos || i > 0 && e.start == pos && e.start == e.end && edits[i-1].start == edits[i-1].end {
			// Two changes of one source place, from two places in the output.
			refused = append(refused, e.hunk)
			continue
		}
		out.Write(src[pos:e.start])
		out.WriteString(e.text)
		pos = e.end
	}
	out.Write(src[pos:])
	sort.SliceStable(refused, func(i, j int) bool { return refused[i].Start < refused[j].Start })
	return out.Bytes(), refused
}

// placements are the places where h's change can be made, h's own first,
// then moved to the left, then to the right.
func placements(old []byte, h Hunk) []Hunk {
	out := []Hunk{h}
	for c, ok := slideLeft(old, h); ok; c, ok = slideLeft(old, c) {
		out = append(out, c)
	}
	for c, ok := slideRight(old, h); ok; c, ok = slideRight(old, c) {
		out = append(out, c)
	}
	return out
}

// slideRight is h moved one byte to the right where that makes the same
// change, false where it does not.
func slideRight(old []byte, h Hunk) (Hunk, bool) {
	if h.End >= len(old) {
		return h, false
	}
	c := old[h.End]
	switch {
	case h.Text != "" && h.Text[0] == old[h.Start]:
		return Hunk{Start: h.Start + 1, End: h.End + 1, Text: h.Text[1:] + string(c)}, true
	case h.Text == "" && h.End > h.Start && old[h.Start] == c:
		return Hunk{Start: h.Start + 1, End: h.End + 1}, true
	}
	return h, false
}

// slideLeft is h moved one byte to the left where that makes the same
// change, false where it does not.
func slideLeft(old []byte, h Hunk) (Hunk, bool) {
	if h.Start == 0 {
		return h, false
	}
	c := old[h.Start-1]
	switch {
	case h.Text != "" && h.Text[len(h.Text)-1] == old[h.End-1]:
		return Hunk{Start: h.Start - 1, End: h.End - 1, Text: string(c) + h.Text[:len(h.Text)-1]}, true
	case h.Text == "" && h.End > h.Start && old[h.End-1] == c:
		return Hunk{Start: h.Start - 1, End: h.End - 1}, true
	}
	return h, false
}

// place is the source span a hunk's bytes map to, when they lie within one
// verbatim span. An insertion where two spans meet that are apart in the
// source — the emitter dropped what lay between them — has no one place,
// and is refused. With across, a deletion whose two ends lie in verbatim
// text deletes the source between them, whatever the compiler wrote from
// it: a method a tool removes goes with its head and its docblock. It must
// delete some source: text the compiler wrote from none comes back with
// the rebuild. A replacement that spans text the compiler wrote is
// refused, since its new text is PHP the source may not take, and so is a
// deletion whose source range holds text that lands elsewhere in the
// output: an operand a folded pipe moved.
func place(spans []emit.Verbatim, h Hunk, across bool) (int, int, bool) {
	if !across {
		return placeWithin(spans, h)
	}
	if h.Text != "" || h.Start == h.End {
		return 0, 0, false
	}
	s, ok1 := sourceStart(spans, h.Start)
	e, ok2 := sourceEnd(spans, h.End)
	if !ok1 || !ok2 || s >= e {
		return 0, 0, false
	}
	for _, v := range spans {
		outside := v.Out+v.Len <= h.Start || v.Out >= h.End
		if outside && v.Src < e && v.Src+v.Len > s {
			return 0, 0, false
		}
	}
	return s, e, true
}

// sourceStart maps the output offset where a deletion starts: into the
// span holding the byte there, or after the span ending there.
func sourceStart(spans []emit.Verbatim, at int) (int, bool) {
	i := sort.Search(len(spans), func(i int) bool { return spans[i].Out+spans[i].Len > at })
	if i < len(spans) && spans[i].Out <= at {
		return spans[i].Src + at - spans[i].Out, true
	}
	if i > 0 && spans[i-1].Out+spans[i-1].Len == at {
		return spans[i-1].Src + spans[i-1].Len, true
	}
	return 0, false
}

// sourceEnd maps the output offset where a deletion ends: into the span
// holding the byte before it, or before the span starting there.
func sourceEnd(spans []emit.Verbatim, at int) (int, bool) {
	i := sort.Search(len(spans), func(i int) bool { return spans[i].Out+spans[i].Len >= at })
	if i < len(spans) && spans[i].Out < at {
		return spans[i].Src + at - spans[i].Out, true
	}
	if i < len(spans) && spans[i].Out == at {
		return spans[i].Src, true
	}
	return 0, false
}

// placeWithin is place for a hunk inside one span.
func placeWithin(spans []emit.Verbatim, h Hunk) (int, int, bool) {
	i := sort.Search(len(spans), func(i int) bool { return spans[i].Out+spans[i].Len >= h.Start })
	for ; i < len(spans) && spans[i].Out <= h.Start; i++ {
		v := spans[i]
		if h.End > v.Out+v.Len {
			continue
		}
		if h.Start == h.End && h.Start == v.Out+v.Len && i+1 < len(spans) && spans[i+1].Out == h.Start {
			return 0, 0, false
		}
		return v.Src + h.Start - v.Out, v.Src + h.End - v.Out, true
	}
	return 0, 0, false
}
