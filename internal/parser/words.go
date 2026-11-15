package parser

import (
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/token"
)

// The words Phaenon adds to PHP are written in lower case (design/roadmap.md
// D30). Where only the word can stand, the parser reads it in any letter
// case, as it always has, so that one written in another case is the one
// mistake it is and not a cascade of others: `THROWS X` stays the clause,
// `TYPE Row = int;` the declaration. A name of the same spelling is none of
// these, since the parser read it as a name by the rules that decide the
// position: a class `Throws` as a return type, a method `Type()`, a type
// parameter `In`. Words finds them in a parsed file, for the checker to
// report (P1006) and phn fmt to write in lower case.
//
// `map` and `bytes` are not among them: PHP leaves those names free, and in
// another case they are class names, which the checker resolves.

// Word is one of Phaenon's words written in another spelling than its own:
// S spans it as written, the parentheses of the cast `(BYTES)` included,
// Text is what S holds and Want the word's spelling, in lower case, the
// cast's blanks kept.
type Word struct {
	S    ast.Span
	Text string
	Want string
}

// Words returns, in source order, the words Phaenon adds to PHP that f,
// parsed from src in Phaenon mode, writes in another spelling than lower
// case: `throws` and `nothrow` after a signature, `type` before an alias,
// `of`, `in` and `out` in a type parameter list, `class` and `list` in a
// type position (PHP reserves both, so neither can be a class's name), and
// the cast `(bytes)`. The parser matches the clause words under Unicode's
// case folding, so a spelling that only folds to the word (`throwſ`) is
// found too. A withdrawn type test, `$x IS List`, is left out with its
// type: it is reported whole (P1001) and rewritten whole.
func Words(f *ast.File, src []byte) []Word {
	var out []Word
	add := func(s ast.Span, want string) {
		if s.Start < 0 || s.End <= s.Start || s.End > len(src) {
			return
		}
		if text := string(src[s.Start:s.End]); text != want {
			out = append(out, Word{S: s, Text: text, Want: want})
		}
	}
	// at spans the word that starts at off, as the lexer read it.
	at := func(off int, word string) ast.Span {
		end := off
		for end < len(src) && isWordByte(src[end]) {
			end++
		}
		if end == off {
			end = off + len(word)
		}
		return ast.Span{Start: off, End: end}
	}
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.IsExpr:
			ast.Inspect(n.Expr, visit)
			return false
		case *ast.AsExpr:
			ast.Inspect(n.Expr, visit)
			return false
		case *ast.Throws:
			if n.Nothrow {
				add(n.Kw, "nothrow")
			} else {
				add(n.Kw, "throws")
			}
		case *ast.TypeAlias:
			add(at(n.S.Start, "type"), "type")
		case *ast.TypeParam:
			switch n.Variance {
			case ast.Covariant:
				add(at(n.S.Start, "out"), "out")
			case ast.Contravariant:
				add(at(n.S.Start, "in"), "in")
			}
			if n.Bound != nil {
				add(n.Of, "of")
			}
		case *ast.NamedType:
			if n.Name.Kind == ast.NameUnqualified && len(n.Name.Parts) == 1 {
				if w := strings.ToLower(n.Name.Parts[0]); w == "class" || w == "list" {
					add(n.Name.S, w)
				}
			}
		case *ast.Cast:
			if n.Op == token.T_BYTES_CAST {
				// `( BYTES )`: the blanks stay as written.
				inner := strings.TrimSuffix(strings.TrimPrefix(n.Raw, "("), ")")
				lead := inner[:len(inner)-len(strings.TrimLeft(inner, " \t"))]
				trail := inner[len(strings.TrimRight(inner, " \t")):]
				add(ast.Span{Start: n.S.Start, End: n.S.Start + len(n.Raw)}, "("+lead+"bytes"+trail+")")
			}
		}
		return true
	}
	ast.Inspect(f, visit)
	sort.SliceStable(out, func(i, j int) bool { return out[i].S.Start < out[j].S.Start })
	return out
}

// isWordByte reports a byte of a word as the lexer reads one: an ASCII
// letter, a digit, `_` or a byte of a multi-byte character.
func isWordByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b >= 0x80
}
