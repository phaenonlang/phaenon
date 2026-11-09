package emit

import (
	"bytes"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/token"
)

// A pipe that folds (program.File.Folds) is written for PHP 8.5 as the
// call PHP makes of it, and a run of them — a pipe whose left side is a
// pipe that folds too — as nested calls: the code the pipeline stands for,
// as it was written before pipes (design/php-versions.md §5.3).
//
//	$users |> array_map(fn(User $u) => $u->email, ?) |> array_values(...)
//	// →
//	array_values(array_map(fn(User $u) => $u->email, $users))
//
// The run's text is a free edit (lines.go): its lines are the compiler's
// and map to the run's first source line, whose reverse target is the line
// the run lands on unless an earlier one is, so that a breakpoint set on
// the pipeline stops on its call; the source after a run that joined
// lines, on its last line, moves with it. The source's runs in the text
// are verbatim spans, so phn fix carries a change of an operand back. It
// carries the edits made inside the run — erased generics, a lowered
// partial application, an inner run — rendered where they stand. It goes
// on one line when that fits in foldWidth columns (an argument that spans
// lines keeps its lines, moved with it), otherwise one argument per line,
// PER's multi-line argument list with a trailing comma (§2.6). A run whose
// text cannot move without changing what it says — a comment between its
// stages or arguments, an edit between them, a multi-line string that would
// move sideways, an attribute removed inside it — stays a pipeline, its
// partial applications arrow functions called at once.

const foldWidth = 120

// foldCall is a stage written as a call: its callee up to and with the
// opening parenthesis, and its arguments, one of them the piped value.
type foldCall struct {
	callee text
	args   []foldArg
}

// foldArg is an argument of a folded call: text rendered from the source
// span src, or the call the piped value folds into; name is a named
// argument's `name: `.
type foldArg struct {
	name  string
	text  text
	src   ast.Span
	inner *foldCall
}

// text is a fold's text as runs, each the source's bytes from offset src
// or, where src is -1, the compiler's.
type text []run

type run struct {
	s   string
	src int
}

// lit appends text the compiler writes.
func (t *text) lit(s string) {
	if s != "" {
		*t = append(*t, run{s, -1})
	}
}

// source appends the source's bytes [at, at+len(s)).
func (t *text) source(s string, at int) {
	if s != "" {
		*t = append(*t, run{s, at})
	}
}

func (t *text) add(o text) { *t = append(*t, o...) }

func (t text) String() string {
	var b strings.Builder
	for _, r := range t {
		b.WriteString(r.s)
	}
	return b.String()
}

// pieces are t's source runs, at their offsets in t.String().
func (t text) pieces() []piece {
	var ps []piece
	off := 0
	for _, r := range t {
		if r.src >= 0 {
			ps = append(ps, piece{Off: off, Span: ast.Span{Start: r.src, End: r.src + len(r.s)}})
		}
		off += len(r.s)
	}
	return ps
}

// textOf is an edit's text as runs: its pieces the source's.
func textOf(s string, pieces []piece) text {
	var t text
	pos := 0
	for _, p := range pieces {
		t.lit(s[pos:p.Off])
		t.source(s[p.Off:p.Off+p.End-p.Start], p.Start)
		pos = p.Off + p.End - p.Start
	}
	t.lit(s[pos:])
	return t
}

// lines splits t after each line break.
func (t text) lines() []text {
	out := []text{nil}
	for _, r := range t {
		for r.s != "" {
			i := strings.IndexByte(r.s, '\n')
			if i < 0 {
				out[len(out)-1] = append(out[len(out)-1], r)
				break
			}
			out[len(out)-1] = append(out[len(out)-1], run{r.s[:i+1], r.src})
			out = append(out, nil)
			if r.src >= 0 {
				r.src += i + 1
			}
			r.s = r.s[i+1:]
		}
	}
	return out
}

// trimPrefix is t without its first n bytes.
func (t text) trimPrefix(n int) text {
	for len(t) > 0 && n > 0 {
		r := t[0]
		if len(r.s) > n {
			if r.src >= 0 {
				r.src += n
			}
			r.s = r.s[n:]
			return append(text{r}, t[1:]...)
		}
		n -= len(r.s)
		t = t[1:]
	}
	return t
}

// fold writes the run of folding pipes that ends at top as nested calls,
// or, when its text cannot move, lowers its partial applications as any
// other pipe's.
func (l *targetLowering) fold(top *ast.Binary) error {
	span := top.Span()
	used := map[int]bool{}
	c, ok := l.foldCall(top, used)
	var t text
	if ok {
		t, ok = l.e.layoutFold(c, span)
	}
	for i, ed := range l.e.edits {
		ok = ok && (used[i] || !inside(ed, span))
	}
	for _, d := range l.e.drop {
		ok = ok && (d.End <= span.Start || d.Start >= span.End)
	}
	if !ok {
		for p := top; p != nil && l.asCall(p); {
			if args := callArgs(p.Right); args.Partial() {
				if err := l.pipe(p, args); err != nil {
					return err
				}
			}
			p, _ = p.Left.(*ast.Binary)
		}
		return nil
	}
	kept := make([]edit, 0, len(l.e.edits)-len(used))
	for i, ed := range l.e.edits {
		if !used[i] {
			kept = append(kept, ed)
		}
	}
	l.e.edits = kept
	l.e.replaceFree(span, t.String())
	l.e.edits[len(l.e.edits)-1].Pieces = t.pieces()
	return nil
}

// foldCall is the call a folding pipe makes, its left side folded in when
// that folds too. It marks the edits its text carries in used.
func (l *targetLowering) foldCall(p *ast.Binary, used map[int]bool) (*foldCall, bool) {
	e := l.e
	args := callArgs(p.Right)
	rs := p.Right.Span()
	if args == nil || e.hasComment(p.Left.Span().End, rs.Start) {
		return nil, false
	}
	callee, ok := e.renderSpan(ast.Span{Start: rs.Start, End: args.S.Start + 1}, used)
	if !ok || strings.Contains(callee.String(), "\n") {
		return nil, false
	}
	piped, ok := l.pipedArg(p.Left, used)
	if !ok {
		return nil, false
	}
	c := &foldCall{callee: callee}
	if args.FirstClassCallable {
		c.args = []foldArg{piped}
		return c, !e.hasComment(args.S.Start, args.S.End)
	}
	prev := args.S.Start + 1
	for _, a := range args.Args {
		if e.hasComment(prev, a.S.Start) {
			return nil, false
		}
		prev = a.S.End
		if a.Placeholder != nil {
			arg := piped
			if a.Name != nil {
				arg.name = a.Name.Name + ": "
			}
			c.args = append(c.args, arg)
			continue
		}
		t, ok := e.renderSpan(a.S, used)
		if !ok {
			return nil, false
		}
		c.args = append(c.args, foldArg{text: t, src: a.S})
	}
	return c, !e.hasComment(prev, args.S.End)
}

// pipedArg is the piped value as an argument: the call its pipe folds
// into, or its text, without parentheses of its own.
func (l *targetLowering) pipedArg(left ast.Expr, used map[int]bool) (foldArg, bool) {
	if lp, ok := left.(*ast.Binary); ok && lp.Op == token.T_PIPE && l.asCall(lp) {
		inner, ok := l.foldCall(lp, used)
		return foldArg{inner: inner, src: lp.Span()}, ok
	}
	s := left.Span()
	if par, ok := left.(*ast.Paren); ok {
		in := par.Expr.Span()
		if !l.e.hasComment(s.Start, in.Start) && !l.e.hasComment(in.End, s.End) {
			s = in
		}
	}
	t, ok := l.e.renderSpan(s, used)
	return foldArg{text: t, src: s}, ok
}

// renderSpan is the output text of src[s]: the source with the edits that
// belong to the span applied, in the order apply writes them. It marks
// them in used, and reports false when an edit straddles the span.
func (e *emitter) renderSpan(s ast.Span, used map[int]bool) (text, bool) {
	var idx []int
	for i, ed := range e.edits {
		switch {
		case inside(ed, s):
			idx = append(idx, i)
		case ed.Start < s.End && ed.End > s.Start:
			return nil, false
		}
	}
	sort.SliceStable(idx, func(a, b int) bool { return editLess(e.edits[idx[a]], e.edits[idx[b]]) })
	var t text
	pos := s.Start
	for _, i := range idx {
		ed := e.edits[i]
		if ed.Start < pos {
			return nil, false
		}
		t.source(string(e.src[pos:ed.Start]), pos)
		if rel := e.sourceRelative(ed); rel == ed.Text {
			t.add(textOf(ed.Text, ed.Pieces)) // an inner run keeps its source runs
		} else {
			t.lit(rel)
		}
		pos = ed.End
		used[i] = true
	}
	t.source(string(e.src[pos:s.End]), pos)
	return t, true
}

// sourceRelative is an edit's text with its lines indented as the source's
// are: a free edit lays out its lines for the output, in which an expanded
// expression body moves its lines in (indentReq); that indentation is taken
// out again, so that text rendered from a span is the source's throughout.
func (e *emitter) sourceRelative(ed edit) string {
	extra := ""
	for _, r := range e.indents {
		if r.Start <= ed.Start && ed.Start < r.End {
			extra += r.Text
		}
	}
	if !ed.Free || extra == "" || !strings.Contains(ed.Text, "\n") {
		return ed.Text
	}
	lines := strings.Split(ed.Text, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = strings.TrimPrefix(lines[i], extra)
	}
	return strings.Join(lines, "\n")
}

// inside reports an edit that belongs to the span: within it, an insertion
// at its start included, one at its end only when the node it belongs to
// lies in the span (edit.Owner) — otherwise that is a construct around the
// span closing.
func inside(ed edit, s ast.Span) bool {
	if ed.Start < s.Start || ed.End > s.End {
		return false
	}
	if ed.Start == ed.End && ed.Start == s.End && s.Start < s.End {
		return ed.Owner != (ast.Span{}) && s.Start <= ed.Owner.Start && ed.Owner.End <= s.End
	}
	return true
}

// layoutFold lays out a folded run that replaces span s.
func (e *emitter) layoutFold(c *foldCall, s ast.Span) (text, bool) {
	indent, col := e.outPosition(s.Start)
	unit := "    "
	if strings.Contains(indent, "\t") {
		unit = "\t"
	}
	f := folder{e: e, unit: unit}
	return f.call(c, indent, col, e.restOfLine(s.End))
}

type folder struct {
	e    *emitter
	unit string
}

// call lays out c starting at column col of a line indented by indent,
// trail columns following it on its last line: on that line when it fits,
// one argument per line otherwise.
func (f folder) call(c *foldCall, indent string, col, trail int) (text, bool) {
	if t, ok := f.compact(c, indent); ok && fits(t.String(), col, trail) {
		return t, true
	}
	return f.multi(c, indent)
}

// compact writes c on one line; an argument that spans lines and ends
// with a closing bracket (a closure, an array) keeps its line breaks, its
// lines moved to indent. Any other argument that spans lines — a pipe —
// gets a line of its own (multi).
func (f folder) compact(c *foldCall, indent string) (text, bool) {
	var b text
	b.add(c.callee)
	for i, a := range c.args {
		if i > 0 {
			b.lit(", ")
		}
		b.lit(a.name)
		t, ok := f.arg(a, indent, -1)
		if !ok {
			return nil, false
		}
		ts := t.String()
		if i := strings.LastIndexByte(ts, '\n'); i >= 0 {
			if last := strings.TrimLeft(ts[i+1:], " \t"); last == "" || !strings.ContainsAny(last[:1], "})]") {
				return nil, false
			}
		}
		b.add(t)
	}
	b.lit(")")
	return b, true
}

// multi writes c with one argument per line, one level in from indent. A
// call whose one argument is the inner call keeps it on its line, so that
// nested calls close together: `array_values(array_filter(` + its
// arguments + `))`, as they are written by hand.
func (f folder) multi(c *foldCall, indent string) (text, bool) {
	var b text
	b.add(c.callee)
	if len(c.args) == 1 && c.args[0].inner != nil && c.args[0].name == "" {
		t, ok := f.multi(c.args[0].inner, indent)
		b.add(t)
		b.lit(")")
		return b, ok
	}
	in := indent + f.unit
	b.lit("\n")
	for _, a := range c.args {
		b.lit(in + a.name)
		t, ok := f.arg(a, in, width(in)+width(a.name))
		if !ok {
			return nil, false
		}
		b.add(t)
		b.lit(",\n")
	}
	b.lit(indent + ")")
	return b, true
}

// arg writes an argument on a line indented by indent: an inner call at
// column col (compact when col < 0), text with its lines moved.
func (f folder) arg(a foldArg, indent string, col int) (text, bool) {
	switch {
	case a.inner == nil:
		return f.e.reindent(a.text, a.src, indent)
	case col < 0:
		return f.compact(a.inner, indent)
	default:
		return f.call(a.inner, indent, col, 1)
	}
}

// fits reports whether text starting at column col, trail columns after
// it, stays within foldWidth on its first and last lines.
func fits(text string, col, trail int) bool {
	first, rest, multi := strings.Cut(text, "\n")
	if !multi {
		return col+width(first)+trail <= foldWidth
	}
	last := rest[strings.LastIndexByte(rest, '\n')+1:]
	return col+width(first) <= foldWidth && width(last)+trail <= foldWidth
}

func width(s string) int { return utf8.RuneCountInString(s) }

// reindent moves the continuation lines of an argument's text, rendered
// from the source span src (renderSpan), to start from indent as they
// started from the indentation of src's first line. A multi-line string or
// comment in it cannot move sideways. The indentation it writes is the
// compiler's; the rest of each line stays the source's.
func (e *emitter) reindent(t text, src ast.Span, indent string) (text, bool) {
	from := e.lineIndent(src.Start)
	if !strings.Contains(t.String(), "\n") || from == indent {
		return t, true
	}
	if e.multiLineToken(src.Start, src.End) {
		return nil, false
	}
	lines := t.lines()
	out := lines[0]
	for _, l := range lines[1:] {
		ln := l.String()
		body, nl := strings.CutSuffix(ln, "\n")
		switch {
		case strings.TrimSpace(body) == "":
			if nl {
				out.lit("\n")
			}
		case strings.HasPrefix(body, from):
			out.lit(indent)
			out.add(l.trimPrefix(len(from)))
		default:
			return nil, false
		}
	}
	return out, true
}

// outPosition is where offset at lands in the output: the indentation of
// its output line and its column there, from the line as the edits before
// at on its source line write it.
func (e *emitter) outPosition(at int) (indent string, col int) {
	lineStart := bytes.LastIndexByte(e.src[:at], '\n') + 1
	var idx []int
	for i, ed := range e.edits {
		if ed.Start >= lineStart && ed.End <= at && !(ed.Start == ed.End && ed.Start == at) {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool { return editLess(e.edits[idx[a]], e.edits[idx[b]]) })
	var b strings.Builder
	pos := lineStart
	for _, i := range idx {
		ed := e.edits[i]
		if ed.Start < pos {
			continue
		}
		b.Write(e.src[pos:ed.Start])
		b.WriteString(ed.Text)
		pos = ed.End
	}
	b.Write(e.src[pos:at])
	line := b.String()
	if i := strings.LastIndexByte(line, '\n'); i >= 0 {
		line = line[i+1:] // a line the compiler opened, its indentation its own
	} else {
		line = e.outIndent(at)[len(e.lineIndent(at)):] + line
	}
	indent = line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	return indent, width(line)
}

// restOfLine is the width of the code after offset end on its line.
func (e *emitter) restOfLine(end int) int {
	eol := bytes.IndexByte(e.src[end:], '\n')
	if eol < 0 {
		eol = len(e.src) - end
	}
	return width(strings.TrimRight(string(e.src[end:end+eol]), " \t\r"))
}
