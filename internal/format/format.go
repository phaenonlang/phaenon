// Package format is the first layer of phn fmt: Phaenon's own layout,
// with no options (design/tooling.md §5.1).
//
// It writes what no PHP tool sees, because the compiler rewrites it or
// leaves it out of the output:
//
//   - the canonical layout of design/functions.md §5, migrate's without
//     the expression bodies: a function's brace on its declaration's line,
//     a class-like's on a line of its own, `{}` for an empty body, a
//     one-line docblock on one line;
//   - spaces for the tabs Phaenon forbids (design/functions.md §5.5),
//     before anything else;
//   - no blank line before the file's first line, which the compiler's
//     header would absorb;
//   - the spacing of Phaenon's own syntax: type parameters and type
//     arguments (`class Box<T of Item>`, `map<string, list<int>>`), tuple
//     and closure types (`(int, string)`, `Closure(int, string...): bool`),
//     `throws A|B`, `=>` of an expression body, and type aliases.
//
// Before all of it, the first layer rewrites the spellings roadmap D26 and
// D27 withdrew (design/tooling.md §5.6, respell.go): `fun`, `function` on
// a method whose name needs no keyword, and the type tests `is`, `as` and
// `as?`, those the checked program gives the PHP of (Options.Tests) and,
// without one, `$x is Foo` with a class name. These are the layer's only
// changes of tokens.
//
// Everything else is the project's PHP style, which the second layer, the
// project's fixer run through phn fix, writes. The spacing changes only
// whitespace within a line: no line is joined or broken, and a construct
// with a comment inside is left as it is. Source checks that the tokens
// come out of the layout as they went in.
package format

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/migrate"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/tabs"
	"github.com/phaenonlang/phaenon/internal/token"
)

// Source lays a .phn out as Phaenon's layout wants it, with the rewrites
// that need no checked program. Formatting the result again changes
// nothing.
func Source(src []byte) ([]byte, error) {
	out, _, err := SourceWith(src, Options{})
	return out, err
}

// SourceWith is Source with what opts know beyond the source. left are the
// withdrawn type tests it leaves as written, which stay P1001.
func SourceWith(src []byte, opts Options) (out []byte, left []Left, err error) {
	// The withdrawn spellings go first, where the checker's offsets hold;
	// then the tabs: every later step lays out spaces.
	out, left, err = respell(src, opts)
	if err != nil {
		return nil, nil, err
	}
	out = trimLeadingBlankLines(tabs.Expand(out))
	if out, err = migrate.LayoutBlocks(out); err != nil {
		return nil, nil, err
	}
	out, err = space(out)
	return out, left, err
}

// trimLeadingBlankLines drops the blank lines a file opens with.
func trimLeadingBlankLines(src []byte) []byte {
	for {
		nl := bytes.IndexByte(src, '\n')
		if nl < 0 || len(bytes.TrimSpace(src[:nl])) > 0 {
			return src
		}
		src = src[nl+1:]
	}
}

// spacer collects the whitespace wanted between consecutive tokens.
type spacer struct {
	src  []byte
	code []token.Token  // the tokens other than whitespace, without EOF
	want map[int]string // i → the whitespace between code[i] and code[i+1]
}

func space(src []byte) ([]byte, error) {
	f, err := parser.Parse(src, lexer.ModePhaenon)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	s := &spacer{src: src, code: codeTokens(src), want: map[int]string{}}
	ast.Inspect(f, func(n ast.Node) bool {
		s.node(n)
		return true
	})
	out := s.apply()
	if !sameTokens(src, out) {
		return nil, fmt.Errorf("format: the spacing changed a token (a bug in phn fmt)")
	}
	return out, nil
}

func codeTokens(src []byte) []token.Token {
	var out []token.Token
	for _, t := range lexer.Tokenize(src, lexer.ModePhaenon) {
		if t.Kind != token.T_WHITESPACE && t.Kind != token.EOF {
			out = append(out, t)
		}
	}
	return out
}

// sameTokens reports whether a and b differ in whitespace only. A `>>`
// counts as the two `>` it closes in `list<list<int>>`, where the parser
// splits it.
func sameTokens(a, b []byte) bool {
	ta, tb := tokenTexts(a), tokenTexts(b)
	if len(ta) != len(tb) {
		return false
	}
	for i := range ta {
		if ta[i] != tb[i] {
			return false
		}
	}
	return true
}

func tokenTexts(src []byte) []string {
	var out []string
	for _, t := range codeTokens(src) {
		text := string(src[t.Start:t.End])
		if t.Kind == token.T_SR {
			out = append(out, ">", ">")
			continue
		}
		out = append(out, text)
	}
	return out
}

func (s *spacer) node(n ast.Node) {
	switch n := n.(type) {
	case *ast.NamedType:
		if len(n.TypeArgs) > 0 {
			name := s.last(n.Name.S.End)
			s.set(name, "")
			s.tight(ast.Span{Start: s.start(name + 1), End: n.S.End})
		}
	case *ast.ClosureType:
		s.tight(n.S)
	case *ast.TupleType:
		s.tight(n.S)
	case *ast.Call:
		s.typeArgs(n.TypeArgs)
	case *ast.MethodCall:
		s.typeArgs(n.TypeArgs)
	case *ast.StaticCall:
		s.typeArgs(n.TypeArgs)
	case *ast.New:
		s.typeArgs(n.TypeArgs)
	case *ast.ClassDecl:
		s.typeParams(n.TypeParams)
	case *ast.FunctionDecl:
		s.typeParams(n.TypeParams)
		s.exprBody(n.Arrow)
	case *ast.MethodDecl:
		s.typeParams(n.TypeParams)
		s.exprBody(n.Arrow)
	case *ast.Closure:
		s.typeParams(n.TypeParams)
	case *ast.ArrowFunc:
		s.typeParams(n.TypeParams)
	case *ast.TypeAlias:
		s.typeParams(n.TypeParams)
		if kw := s.at(n.S.Start); kw >= 0 {
			s.set(kw, " ")
		}
		if eq := s.first(n.Type.Span().Start) - 1; eq >= 0 && s.text(eq) == "=" {
			s.set(eq-1, " ")
			s.set(eq, " ")
		}
		s.tight(n.Type.Span())
	case *ast.Throws:
		kw := s.at(n.Kw.Start)
		if kw >= 0 && s.tight(n.S) {
			s.set(kw-1, " ")
			s.set(kw, " ")
		}
	}
}

// typeParams spaces a type parameter list: `<in T of Bound = Default, U>`.
func (s *spacer) typeParams(tps []*ast.TypeParam) {
	if len(tps) == 0 {
		return
	}
	lt := s.first(tps[0].S.Start) - 1
	gt := s.last(tps[len(tps)-1].S.End) + 1
	if lt < 1 || gt >= len(s.code) || s.text(lt) != "<" || s.text(gt) != ">" {
		return
	}
	if !s.tight(ast.Span{Start: s.code[lt].Start, End: s.code[gt].End}) {
		return
	}
	s.set(lt-1, "")
	for i := lt; i < gt; i++ {
		if s.code[i].Kind == token.T_OF || s.text(i) == "=" {
			s.set(i-1, " ")
			s.set(i, " ")
		}
	}
	for _, tp := range tps {
		if tp.Variance != 0 {
			if v := s.at(tp.S.Start); v >= 0 {
				s.set(v, " ")
			}
		}
	}
}

// typeArgs spaces the type arguments of a call: `make<int, string>(`.
func (s *spacer) typeArgs(args []ast.TypeExpr) {
	if len(args) == 0 {
		return
	}
	lt := s.first(args[0].Span().Start) - 1
	gt := s.last(args[len(args)-1].Span().End) + 1
	if lt < 1 || gt >= len(s.code) || s.text(lt) != "<" || s.text(gt) != ">" {
		return
	}
	if s.tight(ast.Span{Start: s.code[lt].Start, End: s.code[gt].End}) {
		s.set(lt-1, "")
	}
}

// exprBody spaces the arrow of an expression body: `name(): T => expr;`.
func (s *spacer) exprBody(b *ast.ExprBody) {
	if b == nil {
		return
	}
	if arrow := s.at(b.Arrow.Start); arrow >= 0 {
		s.set(arrow-1, " ")
		s.set(arrow, " ")
	}
}

// tight wants the tokens of span next to each other, but for one space
// after a comma or a colon. A span holding a comment is left as it is, and
// tight says so.
func (s *spacer) tight(span ast.Span) bool {
	a, b := s.first(span.Start), s.last(span.End)
	if a < 0 || b < a {
		return false
	}
	for i := a; i <= b; i++ {
		if k := s.code[i].Kind; k == token.T_COMMENT || k == token.T_DOC_COMMENT {
			return false
		}
	}
	for i := a; i < b; i++ {
		if t := s.text(i); t == "," || t == ":" {
			s.want[i] = " "
		} else {
			s.want[i] = ""
		}
	}
	return true
}

func (s *spacer) set(i int, ws string) {
	if i >= 0 && i+1 < len(s.code) {
		s.want[i] = ws
	}
}

func (s *spacer) text(i int) string {
	if i < 0 || i >= len(s.code) {
		return ""
	}
	return string(s.src[s.code[i].Start:s.code[i].End])
}

func (s *spacer) start(i int) int {
	if i < len(s.code) {
		return s.code[i].Start
	}
	return len(s.src)
}

// first is the index of the first token starting at or after off.
func (s *spacer) first(off int) int {
	i := sort.Search(len(s.code), func(i int) bool { return s.code[i].Start >= off })
	if i == len(s.code) {
		return -1
	}
	return i
}

// last is the index of the last token ending at or before off.
func (s *spacer) last(off int) int {
	return sort.Search(len(s.code), func(i int) bool { return s.code[i].End > off }) - 1
}

// at is the index of the token starting at off, -1 without one.
func (s *spacer) at(off int) int {
	if i := s.first(off); i >= 0 && s.code[i].Start == off {
		return i
	}
	return -1
}

// apply writes the wanted whitespace into the gaps that lie on one line.
func (s *spacer) apply() []byte {
	var b bytes.Buffer
	pos := 0
	for i := 0; i+1 < len(s.code); i++ {
		ws, ok := s.want[i]
		if !ok {
			continue
		}
		from, to := s.code[i].End, s.code[i+1].Start
		gap := s.src[from:to]
		if bytes.ContainsAny(gap, "\n\r") || string(gap) == ws {
			continue
		}
		b.Write(s.src[pos:from])
		b.WriteString(ws)
		pos = to
	}
	b.Write(s.src[pos:])
	return b.Bytes()
}
