package parser

import (
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
)

// TestExpressionBodies covers `=> expr;` in place of a named function's
// block (design/functions.md §3): the declaration's Body holds the
// statement the arrow stands for, a return unless the return type is void
// or never.
func TestExpressionBodies(t *testing.T) {
	src := `
function id(int $x): int => $x;
function plain(): string => 'a';
function log(string $m): void => print $m;
function fail(): NEVER => throw new LogicException();
function untyped() => 1;
class C {
    name(): string => $this->name;
    static of(string $s): self
        => new self($s);
    private go(): Void => $this->run();
    abstract handle(): int;
}
`
	f, err := Parse([]byte(src), lexer.ModePhaenon)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"id": "return", "plain": "return", "log": "expr", "fail": "expr", "untyped": "return",
		"name": "return", "of": "return", "go": "expr",
	}
	got := map[string]string{}
	check := func(name string, arrow *ast.ExprBody, body []ast.Stmt) {
		if arrow == nil {
			return
		}
		if len(body) != 1 {
			t.Errorf("%s: body has %d statements, want 1", name, len(body))
			return
		}
		switch s := body[0].(type) {
		case *ast.Return:
			got[name] = "return"
			if s.Expr != arrow.Expr {
				t.Errorf("%s: the return does not carry the arrow's expression", name)
			}
		case *ast.ExprStmt:
			got[name] = "expr"
			if s.Expr != arrow.Expr {
				t.Errorf("%s: the statement does not carry the arrow's expression", name)
			}
		}
		if text := src[arrow.Arrow.Start:arrow.Arrow.End]; text != "=>" {
			t.Errorf("%s: arrow span holds %q", name, text)
		}
		if text := src[arrow.Semi.Start:arrow.Semi.End]; text != ";" {
			t.Errorf("%s: semicolon span holds %q", name, text)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FunctionDecl:
			check(n.Name.Name, n.Arrow, n.Body)
		case *ast.MethodDecl:
			check(n.Name.Name, n.Arrow, n.Body)
			if n.Name.Name == "handle" && (n.HasBody || n.Arrow != nil) {
				t.Errorf("handle: abstract method parsed with a body")
			}
		}
		return true
	})
	for name, kind := range want {
		if got[name] != kind {
			t.Errorf("%s: body is %q, want %q", name, got[name], kind)
		}
	}
}

// TestThrows covers the contextual `throws A|B` after a signature
// (design/functions.md §4).
func TestThrows(t *testing.T) {
	src := `
function load(int $id): T throws NotFound => $this->get($id);
function none() throws \RuntimeException|Io\Failure {}
interface I {
    handle(): int throws Refusal;
    more(): void
        throws A
        | B;
}
`
	f, err := Parse([]byte(src), lexer.ModePhaenon)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	record := func(name string, th *ast.Throws) {
		if th == nil {
			return
		}
		var parts []string
		for _, ty := range th.Types {
			parts = append(parts, src[ty.S.Start:ty.S.End])
		}
		got[name] = strings.Join(parts, "|")
		if src[th.Kw.Start:th.Kw.End] != "throws" {
			t.Errorf("%s: keyword span holds %q", name, src[th.Kw.Start:th.Kw.End])
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FunctionDecl:
			record(n.Name.Name, n.Throws)
		case *ast.MethodDecl:
			record(n.Name.Name, n.Throws)
		}
		return true
	})
	want := map[string]string{"load": "NotFound", "none": `\RuntimeException|Io\Failure`, "handle": "Refusal", "more": "A|B"}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s: throws %q, want %q", name, got[name], w)
		}
	}
}

// TestThrowsAndArrowStayPHP: PHP code that names something `throws`
// parses as before, in Phaenon mode as in PHP mode, and the new syntax is
// Phaenon's only — a closure has neither.
func TestThrowsAndArrowStayPHP(t *testing.T) {
	php := `<?php
namespace throws;
use Foo\throws as throws2;
const throws = 1;
function throws(throws $throws): throws { return throws; }
class throws extends Base implements throws\I {
    const throws = throws;
    public throws $throws;
    public function throws(): throws { return $this->throws instanceof throws ? throws::throws : throws(throws); }
}
interface I { public function throws(): void; }
trait T { public function throws() {} }
class U { use T { throws as protected; T::throws as throws2; } }
enum E: string implements throws\I { case throws = 'throws'; }
$x = match (throws) { throws => throws and throws };
foreach (throws as $throws) {}
goto throws;
throws:
$f = fn (throws $t): throws => $t;
`
	for _, mode := range []lexer.Mode{lexer.ModePHP, lexer.ModePhaenon} {
		src := php
		if mode == lexer.ModePhaenon {
			src = strings.TrimPrefix(php, "<?php\n")
		}
		if _, err := Parse([]byte(src), mode); err != nil {
			t.Errorf("mode %v: %v", mode, err)
		}
	}
	refused := map[string]lexer.Mode{
		"<?php function f() throws X {}":            lexer.ModePHP,
		"<?php function f(): int => 1;":             lexer.ModePHP,
		"<?php class C { function f(): int => 1; }": lexer.ModePHP,
		"$f = function () throws X {};":             lexer.ModePhaenon,
		"$f = function (): int => 1;":               lexer.ModePhaenon,
		"function f(): int => 1":                    lexer.ModePhaenon, // the semicolon ends it
		"function f(): int throws => 1;":            lexer.ModePhaenon, // a name is required
		"function f(): int throws A| => 1;":         lexer.ModePhaenon,
		"class C { f(): int => { return 1; } }":     lexer.ModePhaenon,
		"class C { f(): int => 1 { return 1; } }":   lexer.ModePhaenon,
	}
	for src, mode := range refused {
		if _, err := Parse([]byte(src), mode); err == nil {
			t.Errorf("%q parsed; want a syntax error", src)
		}
	}
}
