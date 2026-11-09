package migrate

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// HeadEdit replaces Span of a source with Text.
type HeadEdit struct {
	Span ast.Span
	Text string
}

// MethodHead respells a method's head as roadmap D27 has it
// (design/functions.md §1, tooling.md §5.6): the keyword, PHP's `function`
// or the withdrawn `fun`, goes from before a name that needs none, and
// `fun` becomes `function` before one that does (parser.KeywordRequired).
// The modifiers stay as written; dropPublic, phn migrate's unless
// --keep-public, also drops a `public`, which a method without visibility
// has anyway. A head broken over lines or holding a comment keeps its
// modifiers and loses only the keyword. ok is false when the head is
// spelled so already: a method without a keyword, or one whose name keeps
// `function`.
func MethodHead(src []byte, m *ast.MethodDecl, dropPublic bool) (edit HeadEdit, ok bool) {
	if m.Bare() {
		return HeadEdit{}, false
	}
	if parser.KeywordRequired(m.Name.Name) {
		if !m.Fun {
			return HeadEdit{}, false
		}
		return HeadEdit{Span: m.Kw, Text: "function"}, true
	}
	// The keyword goes with the blanks after it, up to the `&` or the name,
	// which a keyword ending its line brings up onto it.
	end := m.Kw.End
	for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	if end < len(src) && (src[end] == '\n' || src[end] == '\r') {
		for end < len(src) && (src[end] == ' ' || src[end] == '\t' || src[end] == '\n' || src[end] == '\r') {
			end++
		}
	}
	mods := string(src[m.Head.Start:m.Kw.Start])
	if strings.ContainsAny(mods, "\n\r/#") {
		return HeadEdit{Span: ast.Span{Start: m.Kw.Start, End: end}, Text: ""}, true
	}
	var words []string
	for _, w := range strings.Fields(mods) {
		if dropPublic && strings.EqualFold(w, "public") {
			continue
		}
		words = append(words, w)
	}
	text := strings.Join(words, " ")
	if text != "" {
		text += " "
	}
	return HeadEdit{Span: ast.Span{Start: m.Head.Start, End: end}, Text: text}, true
}

// FunctionHead respells the withdrawn `fun` before a function as PHP's
// `function`, which every named declaration that is not at class-member
// level keeps (roadmap D27). ok is false when the head says `function`.
func FunctionHead(fn *ast.FunctionDecl) (edit HeadEdit, ok bool) {
	if !fn.Fun {
		return HeadEdit{}, false
	}
	return HeadEdit{Span: fn.Kw, Text: "function"}, true
}
