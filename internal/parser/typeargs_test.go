package parser

import (
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/token"
)

// shape writes an expression as its operators, fully parenthesised, a call
// with the number of type arguments each level of its first one has:
// `f<list<int>>($x) >> 1` is `(f<1<1>>() >> 1)`. Operands are their source
// text.
func shape(src []byte, e ast.Expr) string {
	typeArgs := func(args []ast.TypeExpr) string {
		s := ""
		for len(args) > 0 {
			s += "<" + string(rune('0'+len(args)))
			n, ok := args[0].(*ast.NamedType)
			if !ok {
				break
			}
			args = n.TypeArgs
		}
		return s + strings.Repeat(">", strings.Count(s, "<"))
	}
	switch e := e.(type) {
	case *ast.Binary:
		op := string(src[e.Left.Span().End:e.Right.Span().Start])
		return "(" + shape(src, e.Left) + " " + strings.TrimSpace(op) + " " + shape(src, e.Right) + ")"
	case *ast.Assign:
		return shape(src, e.Target) + " = " + shape(src, e.Value)
	case *ast.ArrayLit:
		var items []string
		for _, it := range e.Items {
			items = append(items, shape(src, it.Value))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case *ast.Call:
		var args []string
		for _, a := range e.Args.Args {
			args = append(args, shape(src, a.Value))
		}
		return shape(src, e.Callee) + typeArgs(e.TypeArgs) + "(" + strings.Join(args, ", ") + ")"
	case *ast.MethodCall:
		return shape(src, e.Object) + "->" + shape(src, e.Name) + typeArgs(e.TypeArgs) + "()"
	case *ast.StaticCall:
		return shape(src, e.Class) + "::" + shape(src, e.Name) + typeArgs(e.TypeArgs) + "()"
	}
	return string(src[e.Span().Start:e.Span().End])
}

// TestTypeArgumentGuess: `<` after a name may open a call's type arguments,
// which the parser finds out by reading them and looking for the `(`. A
// guess that fails leaves the tokens as they were, the `>>` or `>=` it
// split to close nested arguments included, so that what PHP reads as a
// comparison and a shift is read as PHP reads it; a guess that holds is
// read again as the call, splits and all.
func TestTypeArgumentGuess(t *testing.T) {
	for in, want := range map[string]string{
		// The guess fails: these are PHP's operators.
		`A < B >> 1`:           "(A < (B >> 1))",
		`Foo < Bar >> 2`:       "(Foo < (Bar >> 2))",
		`\A\B < C\D >> 2`:      `(\A\B < (C\D >> 2))`,
		`$x = A < B >> 1`:      "$x = (A < (B >> 1))",
		`[A < B >> 1, 2]`:      "[(A < (B >> 1)), 2]",
		`[A < B, C >> 1]`:      "[(A < B), (C >> 1)]",
		`[A < B, C >= 1]`:      "[(A < B), (C >= 1)]",
		`g(A < B >> 1)`:        "g((A < (B >> 1)))",
		`g(A < B, C >> 1)`:     "g((A < B), (C >> 1))",
		`g(A < B, C >= 1)`:     "g((A < B), (C >= 1))",
		`$o->p < B >> 1`:       "($o->p < (B >> 1))",
		`X::Y < B >> 1`:        "(X::Y < (B >> 1))",
		`A < B >> 1 || C > 1`:  "((A < (B >> 1)) || (C > 1))",
		`[A < B<C>>, D >> 1]`:  "",
		`A < B >> 1 && $x > 1`: "((A < (B >> 1)) && ($x > 1))",
		// The guess holds: nested type arguments end in `>>`, `>>>` or `>=`.
		`f<list<int>>($x)`:                   "f<1<1>>($x)",
		`f<list<int>>($x) >> 1`:              "(f<1<1>>($x) >> 1)",
		`f<A<B<C>>>($x)`:                     "f<1<1<1>>>($x)",
		`f<A<B>, C<D>>($x, $y)`:              "f<2<1>>($x, $y)",
		`$o->m<list<int>>($x)`:               "$o->m<1<1>>()",
		`X::m<list<int>>($x)`:                "X::m<1<1>>()",
		`f<A<B>>(g<C<D>>(1), E < F >> 1)`:    "f<1<1>>(g<1<1>>(1), (E < (F >> 1)))",
		`[A < B >> 1, f<list<int>>($x)]`:     "[(A < (B >> 1)), f<1<1>>($x)]",
		`[f<list<int>>($x), A < B >> 1]`:     "[f<1<1>>($x), (A < (B >> 1))]",
		`f<list<int>>($x) < f<list<int>>(1)`: "(f<1<1>>($x) < f<1<1>>(1))",
	} {
		e, err := ParseExpr([]byte(in), lexer.ModePhaenon)
		if want == "" {
			// `A < B<C>>` closes no call and PHP has no such comparison.
			if err == nil {
				t.Errorf("%s: parsed, want an error", in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got := shape([]byte(in), e); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

// TestTypeArgumentGuessInStatements: the same in whole files, where a
// token split by a failed guess used to shift every token after it.
func TestTypeArgumentGuessInStatements(t *testing.T) {
	for _, src := range []string{
		"const A = 1; const B = 4;\nif (A < B >> 1) {}\n$y = [A < B >> 1, 2];\ng(A < B >> 1);\necho 1;\n",
		"$x = Foo < Bar >> 2;\necho $x;\necho 1;\n",
		"while (A < B >> 1) { $x = [A < B, C >= 1]; }\nreturn A < B >> 1;\n",
		"class C { const X = A < B >> 1; public int $p = A < B >> 1; m(): bool => self::X < B >> 1; }\n",
		// A rewind without a guess reads the tokens a split left: clone's
		// argument list, attributes before a closure.
		"$c = clone(f<list<int>>($x));\n$d = clone(f<list<int>>($x), ['a' => A < B >> 1]);\n",
		"#[At(f<list<int>>(1), A < B >> 1)] fn() => A < B >> 1;\n",
		// Declarations split too, with no guess.
		"class Box<T of A<B>= D, U = list<list<int>>> { public list<list<int>> $p = []; }\n",
		"function f<T of A<B>>(list<list<T>> $x): list<list<T>> => $x;\n$y = f<list<int>>([]) >> 1;\n",
	} {
		f, err := Parse([]byte(src), lexer.ModePhaenon)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		// Every shift the source has is a shift in the tree.
		shifts := 0
		ast.Inspect(f, func(n ast.Node) bool {
			if b, ok := n.(*ast.Binary); ok && b.Op == token.T_SR {
				shifts++
			}
			return true
		})
		if want := strings.Count(src, " >> "); shifts != want {
			t.Errorf("%s: %d shifts in the tree, want %d", src, shifts, want)
		}
	}
}

// TestTypeArgumentGuessMatchesPHP: what PHP refuses stays refused, for
// PHP's reason and at PHP's token: comparisons do not chain, and the second
// one is the `>=` of the source, not the `>` a failed guess split off it.
func TestTypeArgumentGuessMatchesPHP(t *testing.T) {
	for in, tok := range map[string]string{
		`A < B >= 1`:      `">="`,
		`A < B > 1`:       `">"`,
		`$x = A < B >= 1`: `">="`,
		`Foo < Bar >= $y`: `">="`,
		`A < B >> 1 > 2`:  `">"`,
	} {
		for _, mode := range []lexer.Mode{lexer.ModePhaenon, lexer.ModePHP} {
			src := in + ";"
			if mode == lexer.ModePHP {
				src = "<?php " + src
			}
			_, err := Parse([]byte(src), mode)
			if err == nil {
				t.Errorf("%s (mode %v): parsed, want PHP's syntax error", in, mode)
				continue
			}
			if msg := err.Error(); !strings.Contains(msg, "non-associative") || !strings.Contains(msg, "unexpected token "+tok) {
				t.Errorf("%s (mode %v): %v, want the non-associative %s", in, mode, err, tok)
			}
		}
	}
}
