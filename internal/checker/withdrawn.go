package checker

import (
	"fmt"
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
	var slots *program.Slots
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
		case *ast.IsExpr:
			c.withdrawnTest(f, n, n.Expr, "is", &slots)
		case *ast.AsExpr:
			op := "as"
			if n.Nullable {
				op = "as?"
			}
			c.withdrawnTest(f, n, n.Expr, op, &slots)
		}
		return true
	})
}

// withdrawnTest reports a leftover `is`, `as` or `as?` (P1001) with the
// PHP test it compiled to, recorded on the test for phn fmt, or with why
// it has none.
func (c *Checker) withdrawnTest(f *program.File, node, operand ast.Expr, op string, slots **program.Slots) {
	head := fmt.Sprintf("`%s` left the language (roadmap D26): write PHP's own test", op)
	rec := f.Tests[node]
	if rec == nil {
		c.report(node.Span(), P1001, "%s", head)
		return
	}
	if rec.Cond != "" && rec.Why == "" {
		if *slots == nil {
			*slots = program.SlotsOf(f.AST)
		}
		scope := f.ScopeAt(node.Span().Start)
		qualified := func(function string) bool {
			namespaced, global := scope.ResolveFunction(&ast.Name{Kind: ast.NameUnqualified, Parts: []string{function}})
			return !strings.EqualFold(global, function) || namespaced != global && c.p.Function(namespaced) != nil
		}
		rec.Rewritten, rec.Why = rec.Rewrite(f.Src, node, operand, op != "is", op == "as?", *slots, qualified)
	}
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
