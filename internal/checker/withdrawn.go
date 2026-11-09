package checker

import (
	"fmt"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/program"
)

// withdrawnSpellings reports the spellings roadmap D26 and D27 withdrew on
// 2026-11-09, each as P1001 with its replacement, which phn fmt writes
// (design/tooling.md §5.6): a declaration spelled `fun`, and the type
// tests `is`, `as` and `as?`. It runs after the file's code, whose checks
// derived each test's PHP (typetest.go); a test the checker never reached,
// in code after a return, is reported without it.
func (c *Checker) withdrawnSpellings(f *program.File) {
	if f.Mode != lexer.ModePhaenon {
		return
	}
	var tests []ast.Expr
	ast.Inspect(f.AST, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FunctionDecl:
			if n.Fun {
				c.report(n.Kw, P1001, "`fun` left the language (roadmap D27): a function is declared with `function`, which phn fmt writes")
			}
		case *ast.MethodDecl:
			switch {
			case !n.Fun:
			case parser.KeywordRequired(n.Name.Name):
				c.report(n.Kw, P1001, "`fun` left the language (roadmap D27): a method named %s is declared with `function`, which phn fmt writes", n.Name.Name)
			default:
				c.report(n.Kw, P1001, "`fun` left the language (roadmap D27): a method is declared without a keyword, `%s(…)`, as phn fmt writes it", n.Name.Name)
			}
		case *ast.IsExpr, *ast.AsExpr:
			tests = append(tests, n.(ast.Expr))
		}
		return true
	})
	if len(tests) == 0 {
		return
	}
	// The tests in another's operand come after it in tests: rewritten
	// first, their PHP stands in the operand the outer test's rewrite
	// writes.
	w := &testWriter{c: c, f: f, slots: program.SlotsOf(f.AST)}
	for i := len(tests) - 1; i >= 0; i-- {
		switch n := tests[i].(type) {
		case *ast.IsExpr:
			w.test(n, n.Expr, "is")
		case *ast.AsExpr:
			op := "as"
			if n.Nullable {
				op = "as?"
			}
			w.test(n, n.Expr, op)
		}
	}
}

// testWriter writes the PHP of a file's withdrawn tests, innermost first.
type testWriter struct {
	c     *Checker
	f     *program.File
	slots *program.Slots
	done  []*program.Test
	nodes []ast.Expr
}

// text is the source of span with the rewrites of the tests inside it.
func (w *testWriter) text(span ast.Span) string {
	type edit struct {
		s    ast.Span
		text string
	}
	var edits []edit
	for i, rec := range w.done {
		s := w.nodes[i].Span()
		if rec.Rewritten != "" && span.Start <= s.Start && s.End <= span.End {
			edits = append(edits, edit{s, rec.Rewritten})
		}
	}
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].s.Start != edits[j].s.Start {
			return edits[i].s.Start < edits[j].s.Start
		}
		return edits[i].s.End > edits[j].s.End
	})
	var b strings.Builder
	pos := span.Start
	for _, e := range edits {
		if e.s.Start < pos {
			continue // inside an outer one, whose text holds it
		}
		b.Write(w.f.Src[pos:e.s.Start])
		b.WriteString(e.text)
		pos = e.s.End
	}
	b.Write(w.f.Src[pos:span.End])
	return b.String()
}

// test reports a leftover `is`, `as` or `as?` (P1001) with the PHP test it
// compiled to, recorded on the test for phn fmt, or with why it has none.
func (w *testWriter) test(node, operand ast.Expr, op string) {
	c, f := w.c, w.f
	head := fmt.Sprintf("`%s` left the language (roadmap D26): write PHP's own test", op)
	rec := f.Tests[node]
	if rec == nil {
		c.report(node.Span(), P1001, "%s", head)
		return
	}
	if rec.Cond != "" && rec.Why == "" {
		scope := f.ScopeAt(node.Span().Start)
		qualified := func(function string) bool {
			namespaced, global := scope.ResolveFunction(&ast.Name{Kind: ast.NameUnqualified, Parts: []string{function}})
			return !strings.EqualFold(global, function) || namespaced != global && c.p.Function(namespaced) != nil
		}
		rec.Rewritten, rec.Why = rec.Rewrite(w.text, node, operand, op != "is", op == "as?", w.slots, qualified)
	}
	w.done = append(w.done, rec)
	w.nodes = append(w.nodes, node)
	switch {
	case rec.Rewritten != "" && !strings.Contains(rec.Rewritten, "\n") && len(rec.Rewritten) <= 120:
		c.report(node.Span(), P1001, "%s, `%s`, which phn fmt writes", head, rec.Rewritten)
	case rec.Rewritten != "":
		c.report(node.Span(), P1001, "%s, which phn fmt writes", head)
	case rec.Why != "":
		c.report(node.Span(), P1001, "%s; %s", head, rec.Why)
	default:
		c.report(node.Span(), P1001, "%s", head)
	}
}
