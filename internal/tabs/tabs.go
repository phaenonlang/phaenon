// Package tabs finds the tabs Phaenon forbids and replaces them with
// spaces (design/functions.md §5.5, D21).
//
// A tab is allowed in a string's content and in a comment's text only.
// Everywhere else it is layout, and forbidden:
//
//   - in the whitespace between tokens, a line's indentation included;
//   - in the leading whitespace of a comment's continuation lines;
//   - in a heredoc's or nowdoc's closing indentation, and in the prefix of
//     its body lines that PHP strips with it;
//   - inside a token that admits whitespace: `(\tint)`, `yield\tfrom`,
//     `<<<\tEOT`.
//
// Expand replaces each forbidden tab with the spaces up to the next tab
// stop, Width columns apart, so the code looks as it did in an editor that
// shows a tab as Width columns. A heredoc's closing indentation and its
// body lines' prefix start their lines, so they become the same spaces and
// the string's value does not change.
package tabs

import (
	"bytes"
	"slices"
	"sort"
	"unicode/utf8"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/token"
)

// Width is the distance between tab stops: an indentation level of the
// canonical layout.
const Width = 4

// Find returns the offsets of the tabs src, a .phn, may not have, in
// order.
func Find(src []byte) []int {
	if bytes.IndexByte(src, '\t') < 0 {
		return nil
	}
	toks := lexer.Tokenize(src, lexer.ModePhaenon)
	var out []int
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch t.Kind {
		case token.T_ENCAPSED_AND_WHITESPACE, token.T_CONSTANT_ENCAPSED_STRING, token.T_INLINE_HTML:
			// A string's content, or text PHP prints: values, not layout.
			// A heredoc's body is handled with its closing marker.
		case token.T_COMMENT, token.T_DOC_COMMENT:
			out = continuationIndents(out, src, t)
		case token.T_START_HEREDOC:
			out = tabsIn(out, src, t.Start, t.End)
			end := closing(toks, i)
			if end < 0 {
				continue
			}
			out = heredocPrefixes(out, src, toks[i:end+1])
		default:
			out = tabsIn(out, src, t.Start, t.End)
		}
	}
	// A closing marker's indentation is reported with its heredoc and as
	// a token of its own.
	sort.Ints(out)
	return slices.Compact(out)
}

// Lines returns the number of distinct lines the offsets lie on.
func Lines(src []byte, offs []int) int {
	n, last := 0, -1
	line, i := 0, 0
	for _, off := range offs { // in order
		for ; i < off; i++ {
			if breaks(src, i) {
				line++
			}
		}
		if line != last {
			n++
			last = line
		}
	}
	return n
}

// Expand returns src with every tab Find reports replaced by spaces. A
// source without such tabs is returned as it is.
func Expand(src []byte) []byte {
	offs := Find(src)
	if len(offs) == 0 {
		return src
	}
	out := make([]byte, 0, len(src)+len(offs)*(Width-1))
	col, next := 0, 0
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n' || c == '\r':
			out = append(out, c)
			col = 0
			i++
		case c == '\t':
			stop := advance(col, c)
			if next < len(offs) && offs[next] == i {
				next++
				out = append(out, bytes.Repeat([]byte{' '}, stop-col)...)
			} else {
				out = append(out, c)
			}
			col = stop
			i++
		default:
			_, size := utf8.DecodeRune(src[i:])
			out = append(out, src[i:i+size]...)
			col++
			i += size
		}
	}
	return out
}

// advance returns the column after c, a space or a tab, written at col: a
// tab reaches the next tab stop. It is the one statement of where a tab
// ends, for Expand, which writes the spaces up to there, and for the reach
// of a heredoc's closing indentation, which must count as Expand writes.
func advance(col int, c byte) int {
	if c == '\t' {
		return col + Width - col%Width
	}
	return col + 1
}

func tabsIn(out []int, src []byte, start, end int) []int {
	for i := start; i < end; i++ {
		if src[i] == '\t' {
			out = append(out, i)
		}
	}
	return out
}

// continuationIndents reports the tabs in the leading whitespace of a
// comment's lines after its first: a docblock's ` * ` lines follow the
// code's indentation. The first line's indentation is the whitespace token
// before the comment.
func continuationIndents(out []int, src []byte, t token.Token) []int {
	for i := t.Start; i < t.End; i++ {
		if !breaks(src, i) {
			continue
		}
		for j := i + 1; j < t.End && (src[j] == ' ' || src[j] == '\t'); j++ {
			if src[j] == '\t' {
				out = append(out, j)
			}
		}
	}
	return out
}

// closing returns the index of the T_END_HEREDOC that closes the heredoc
// opened at toks[open], skipping heredocs nested in its interpolations, or
// -1 in a file that ends inside it.
func closing(toks []token.Token, open int) int {
	depth := 0
	for i := open; i < len(toks); i++ {
		switch toks[i].Kind {
		case token.T_START_HEREDOC:
			depth++
		case token.T_END_HEREDOC:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// heredocPrefixes reports the tabs of a heredoc's closing indentation and
// of the whitespace each body line starts with, as far as that indentation
// reaches. toks runs from the T_START_HEREDOC to its T_END_HEREDOC; body
// lines start after the opening line and after each line break of the
// body's own text (a nested heredoc's text is that heredoc's), `\r\n`,
// `\n` or a `\r` alone.
//
// The reach is counted in columns, as Expand lays the tabs out, and not in
// the bytes of the closing indentation: Expand changes that number where
// spaces precede a tab, and a prefix counted in bytes would then take in,
// on a second run, a tab the first one left as the string's text. It ends
// at the first byte that is no whitespace too: what PHP strips with the
// closing indentation is whitespace, and a tab after text is text. In a
// heredoc PHP accepts, each body line starts with the closing indentation's
// own bytes, and the two counts name the same tabs.
func heredocPrefixes(out []int, src []byte, toks []token.Token) []int {
	end := toks[len(toks)-1]
	indent := 0
	for end.Start+indent < end.End && (src[end.Start+indent] == ' ' || src[end.Start+indent] == '\t') {
		indent++
	}
	out = tabsIn(out, src, end.Start, end.Start+indent)
	if indent == 0 {
		return out
	}
	width := 0
	for _, c := range src[end.Start : end.Start+indent] {
		width = advance(width, c)
	}
	prefix := func(lineStart, limit int) {
		col := 0
		for j := lineStart; j < limit && col < width && (src[j] == ' ' || src[j] == '\t'); j++ {
			if src[j] == '\t' {
				out = append(out, j)
			}
			col = advance(col, src[j])
		}
	}
	depth := 0
	for k, t := range toks[:len(toks)-1] {
		switch t.Kind {
		case token.T_START_HEREDOC:
			depth++
			if depth == 1 && k+1 < len(toks) {
				// The first body line starts after the opening line. An
				// empty heredoc's next token is the closing marker, whose
				// indentation is reported above.
				if next := toks[k+1]; next.Kind == token.T_ENCAPSED_AND_WHITESPACE {
					prefix(next.Start, next.End)
				}
			}
			continue
		case token.T_END_HEREDOC:
			depth--
			continue
		}
		if depth != 1 || t.Kind != token.T_ENCAPSED_AND_WHITESPACE {
			continue
		}
		for i := t.Start; i < t.End; i++ {
			if breaks(src, i) && i+1 < t.End {
				prefix(i+1, t.End)
			}
		}
	}
	return out
}

// breaks reports whether src[i] ends a line, as PHP's scanner reads line
// breaks: `\n`, a `\r` alone, and `\r\n`, whose `\n` ends it.
func breaks(src []byte, i int) bool {
	return src[i] == '\n' || src[i] == '\r' && (i+1 == len(src) || src[i+1] != '\n')
}
