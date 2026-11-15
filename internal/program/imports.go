package program

import (
	"bytes"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/token"
)

// ImportUses finds the uses of a file's imports among its tokens, toks
// being those of src. wanted holds the lower-cased names the imports give
// (their aliases) that are looked for; use is called with each use of one
// — the name, where the use stands, and whether it is a comment's mention
// — and says whether the name is still wanted. The use statements
// themselves, statements in source order, name nothing through an alias,
// and neither do the tokens that start at idents, the tree's identifiers.
// The checker asks it which imports nothing uses, and the emitter which
// uses the compiled file keeps.
//
// A name is a use where it stands alone or begins a qualified name
// (`ORM\Column`). What merely spells the alias is not: a variable, a member
// after `->`, `?->` or `::`, the name a declaration gives, a named
// argument, the text of a string or a heredoc, a `//` or `#` comment, a
// segment after a backslash, a namespace's own name. A docblock's mention
// is a use, since its types are read, and so is a block comment's, where
// an editor reads `/* @var T $x */`; a `$name` there is a variable. A
// comment is one use of each name it mentions, at the comment's span.
//
// The rule errs on the side of a use, so that an import found unused is
// one that can be deleted: the kinds of import are not told apart (a call
// of user() uses `use App\User;`), any word of a docblock counts, and a
// file of several namespaces is searched as one.
func ImportUses(src []byte, toks []token.Token, statements []ast.Span, idents map[int]bool, wanted map[string]bool, use func(name string, at ast.Span, comment bool) bool) {
	var buf []byte
	mark := func(name []byte, at ast.Span) {
		buf = append(buf[:0], name...)
		for i, b := range buf {
			if b >= 'A' && b <= 'Z' {
				buf[i] = b + 'a' - 'A'
			}
		}
		if wanted[string(buf)] {
			wanted[string(buf)] = use(string(buf), at, false)
		}
	}
	prev := token.EOF // the token before, whitespace and comments apart
	for _, t := range toks {
		switch t.Kind {
		case token.T_WHITESPACE:
			continue
		case token.T_COMMENT, token.T_DOC_COMMENT:
			if text := src[t.Start:t.End]; bytes.HasPrefix(text, []byte("/*")) {
				lower := strings.ToLower(string(text))
				for name, still := range wanted {
					if still && Mentions(lower, name) {
						wanted[name] = use(name, ast.Span{Start: t.Start, End: t.End}, true)
					}
				}
			}
			continue
		}
		before := prev
		prev = t.Kind
		for len(statements) > 0 && statements[0].End <= t.Start {
			statements = statements[1:]
		}
		if len(statements) > 0 && statements[0].Start <= t.Start {
			continue // inside a use statement
		}
		if idents[t.Start] {
			continue
		}
		switch t.Kind {
		case token.T_VARIABLE, token.T_STRING_VARNAME, token.T_NUM_STRING,
			token.T_CONSTANT_ENCAPSED_STRING, token.T_ENCAPSED_AND_WHITESPACE,
			token.T_START_HEREDOC, token.T_END_HEREDOC, token.T_INLINE_HTML,
			token.T_NAME_FULLY_QUALIFIED, token.T_NAME_RELATIVE:
			continue
		}
		switch before {
		case token.T_OBJECT_OPERATOR, token.T_NULLSAFE_OBJECT_OPERATOR, token.T_PAAMAYIM_NEKUDOTAYIM, token.T_NAMESPACE:
			continue // a member's name, a namespace's
		}
		// Any other token is a name, a keyword or punctuation. Only a
		// name spells an alias, and Phaenon's contextual keywords
		// (`type`, `of`, `in`, `out`), which are names where the grammar
		// allows one: the token's text is looked up, whatever its kind.
		name := src[t.Start:t.End]
		if i := bytes.IndexByte(name, '\\'); i >= 0 {
			name = name[:i]
		}
		mark(name, ast.Span{Start: t.Start, End: t.End})
	}
}

// Mentions reports the lower-cased word in the lower-cased text of a
// comment, as a name of its own or the first segment of a qualified one.
func Mentions(lower, w string) bool {
	i := 0
	for {
		j := strings.Index(lower[i:], w)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(w)
		// A backslash before the word makes it another name's segment and
		// a dollar a variable; a backslash after it continues a name the
		// alias begins (`ORM\Column` after `use Doctrine\ORM\Mapping as ORM`).
		okStart := start == 0 || !isWordByte(lower[start-1]) && lower[start-1] != '$'
		okEnd := end == len(lower) || !isWordByte(lower[end]) || lower[end] == '\\'
		if okStart && okEnd {
			return true
		}
		i = start + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '\\' || b >= 0x80
}
