package migrate

import (
	"bytes"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/token"
)

// CanonicalCasts writes each long spelling of a cast in src, a .phn, as the
// canonical cast it stands for (design/language-overview.md §3): `(int)`
// for `(integer)`, `(bool)` for `(boolean)`, `(float)` for `(double)` and
// `(real)`, `(string)` for `(binary)`. Phaenon has only the canonical
// casts: PHP 8.0 removed `(real)`, PHP 8.5 deprecates the other four, and
// the checker reports a long one (P1001). Only the word changes,
// in any letter case: the parentheses and the blanks inside them stay as
// written, `( Integer )` becoming `( int )`. The lexer reads the long
// spellings as their canonical cast's token, so the rewrite changes no
// token's kind, and text the lexer does not read as a cast, in a string or
// a comment, is left alone.
func CanonicalCasts(src []byte) []byte {
	if !mayHoldLongCast(src) {
		return src
	}
	var out []byte
	pos := 0
	for _, t := range lexer.Tokenize(src, lexer.ModePhaenon) {
		switch t.Kind {
		case token.T_INT_CAST, token.T_BOOL_CAST, token.T_DOUBLE_CAST, token.T_STRING_CAST:
		default:
			continue
		}
		start, end, canonical := lexer.LongCast(t.Text(src))
		if canonical == "" {
			continue
		}
		out = append(out, src[pos:t.Start+start]...)
		out = append(out, canonical...)
		pos = t.Start + end
	}
	if out == nil {
		return src
	}
	return append(out, src[pos:]...)
}

// mayHoldLongCast is a quick scan for the words of the long casts, so that a
// source without any is not tokenized for them.
func mayHoldLongCast(src []byte) bool {
	lower := bytes.ToLower(src)
	for _, w := range []string{"integer", "boolean", "double", "real", "binary"} {
		if bytes.Contains(lower, []byte(w)) {
			return true
		}
	}
	return false
}
