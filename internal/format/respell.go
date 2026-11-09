package format

import (
	"fmt"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/migrate"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/program"
)

// Options are what the first layer knows beyond the source.
type Options struct {
	// Tests are the PHP of the file's withdrawn type tests, `is`, `as`
	// and `as?` (roadmap D26), by each test's span in the source: the test
	// each compiled to, which the checked program gives
	// (program.Test.Rewritten). With Program, a test without one has no
	// rewrite.
	Tests   map[ast.Span]string
	Program bool
}

// Left is a withdrawn type test the first layer leaves as written: its
// rewrite needs the checked program, or there is none.
type Left struct {
	Span ast.Span
	Msg  string
}

// respell rewrites the spellings roadmap D26 and D27 withdrew
// (design/tooling.md §5.6), each into what it compiled to:
//   - `fun` goes before a method's name and becomes `function` before a
//     function's and before a method name that needs the keyword
//     (parser.KeywordRequired); `function` goes before every other method
//     name, so that a tree has one spelling of the keyword. The modifiers
//     stay as written: dropping a `public` is phn migrate's.
//   - `is`, `as` and `as?` become the PHP test the emitter wrote for them,
//     which depends on the operand's type (`is string` on bytes is
//     mb_check_encoding()), so opts.Tests give it. Without the checked
//     program only `$x is Foo` with a class name is rewritten, to `$x
//     instanceof Foo`: a name that is no type keyword, no alias the file
//     declares and no type parameter it names. A name that turns out an
//     alias of another file is an unknown class at the next check, never a
//     silent change.
func respell(src []byte, opts Options) ([]byte, []Left, error) {
	f, err := parser.Parse(src, lexer.ModePhaenon)
	if err != nil {
		return nil, nil, fmt.Errorf("parse: %w", err)
	}
	var edits []migrate.HeadEdit
	var left []Left
	var slots *program.Slots
	local := localTypeNames(f)
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FunctionDecl:
			if e, ok := migrate.FunctionHead(n); ok {
				edits = append(edits, e)
			}
		case *ast.MethodDecl:
			if e, ok := migrate.MethodHead(src, n, false); ok {
				edits = append(edits, e)
			}
		case *ast.IsExpr, *ast.AsExpr:
			e := n.(ast.Expr)
			if text, ok := opts.Tests[e.Span()]; ok {
				edits = append(edits, migrate.HeadEdit{Span: e.Span(), Text: text})
				return true
			}
			if is, ok := n.(*ast.IsExpr); ok && !opts.Program {
				if class := className(src, is.Type, local); class != "" {
					if slots == nil {
						slots = program.SlotsOf(f)
					}
					edits = append(edits, instanceOf(is, class, slots)...)
					return true
				}
			}
			left = append(left, Left{Span: e.Span(), Msg: leftMessage(n, opts.Program)})
		}
		return true
	})
	return apply(src, edits), left, nil
}

// instanceOf writes `$x is Foo` as `$x instanceof Foo`, in parentheses
// where the expression around it binds tighter than instanceof.
func instanceOf(is *ast.IsExpr, class string, slots *program.Slots) []migrate.HeadEdit {
	rest := migrate.HeadEdit{Span: ast.Span{Start: is.Expr.Span().End, End: is.S.End}, Text: " instanceof " + class}
	if !slots.Parenthesise(is, program.PrecInstanceof) {
		return []migrate.HeadEdit{rest}
	}
	rest.Text += ")"
	return []migrate.HeadEdit{{Span: ast.Span{Start: is.S.Start, End: is.S.Start}, Text: "("}, rest}
}

func leftMessage(n ast.Node, checked bool) string {
	op := "is"
	if as, ok := n.(*ast.AsExpr); ok {
		op = "as"
		if as.Nullable {
			op = "as?"
		}
	}
	if checked {
		return fmt.Sprintf("`%s` left the language (roadmap D26) and this test has no PHP rewrite: write PHP's own test", op)
	}
	return fmt.Sprintf("`%s` left the language (roadmap D26): its rewrite depends on the operand's type, which phn fmt reads from the checked program of a project", op)
}

// className is the type as written when it is a class name: no type
// keyword, no type arguments, no alias or type parameter of the file's.
func className(src []byte, t ast.TypeExpr, local map[string]bool) string {
	nt, ok := t.(*ast.NamedType)
	if !ok || len(nt.TypeArgs) > 0 || len(nt.Name.Parts) == 0 {
		return ""
	}
	if len(nt.Name.Parts) == 1 && nt.Name.Kind == ast.NameUnqualified {
		if w := nt.Name.Parts[0]; checker.TypeKeyword(w) || local[strings.ToLower(w)] {
			return ""
		}
	}
	return nt.S.Text(src)
}

// localTypeNames are the names, lower-cased, that the file's type aliases
// and type parameters declare.
func localTypeNames(f *ast.File) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.TypeAlias:
			out[strings.ToLower(n.Name.Name)] = true
		case *ast.TypeParam:
			out[strings.ToLower(n.Name.Name)] = true
		}
		return true
	})
	return out
}

// apply makes the edits, in source order. An edit inside one made before
// it is a test in the operand of another, whose rewrite already holds its
// PHP (the checker writes the innermost first); the instanceof rewrites
// made without a program leave the operand alone, so theirs never nest.
func apply(src []byte, edits []migrate.HeadEdit) []byte {
	if len(edits) == 0 {
		return src
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].Span.Start < edits[j].Span.Start })
	var b strings.Builder
	pos := 0
	for _, e := range edits {
		if e.Span.Start < pos {
			continue
		}
		b.Write(src[pos:e.Span.Start])
		b.WriteString(e.Text)
		pos = e.Span.End
	}
	b.Write(src[pos:])
	return []byte(b.String())
}
