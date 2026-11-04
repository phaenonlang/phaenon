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
	for _, off := range offs {
		line := bytes.Count(src[:off], []byte{'\n'})
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
		case c == '\n':
			out = append(out, c)
			col = 0
			i++
		case c == '\t':
			n := Width - col%Width
			if next < len(offs) && offs[next] == i {
				next++
				out = append(out, bytes.Repeat([]byte{' '}, n)...)
			} else {
				out = append(out, c)
			}
			col += n
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
		if src[i] != '\n' {
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
// of the same number of bytes at the start of each body line. toks runs
// from the T_START_HEREDOC to its T_END_HEREDOC; body lines start after the
// opening line and after each line break of the body's own text (a nested
// heredoc's text is that heredoc's).
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
	prefix := func(lineStart, limit int) {
		for j := lineStart; j < lineStart+indent && j < limit && src[j] != '\n'; j++ {
			if src[j] == '\t' {
				out = append(out, j)
			}
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
			if src[i] == '\n' && i+1 < t.End {
				prefix(i+1, t.End)
			}
		}
	}
	return out
}
