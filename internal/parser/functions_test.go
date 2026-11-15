package parser

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// TestNothrow covers `nothrow` where a `throws` clause goes
// (design/functions.md §4.5): a Throws node marked Nothrow, without types,
// its spans the word's, in any case.
func TestNothrow(t *testing.T) {
	src := `
function find(int $id): ?User nothrow { return null; }
function bare() NoThrow {}
function arrow(): string nothrow => 'x';
interface I {
    supports(Command $c): bool nothrow;
    static of(): static
        nothrow;
}
abstract class C {
    abstract protected name(): string NOTHROW;
    public function len(): int nothrow => 0;
    keep(): void throws X {}
    none(): void {}
}
`
	f, err := Parse([]byte(src), lexer.ModePhaenon)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	record := func(name string, th *ast.Throws) {
		if th == nil || !th.Nothrow {
			return
		}
		got[name] = true
		if len(th.Types) != 0 {
			t.Errorf("%s: nothrow with %d types", name, len(th.Types))
		}
		if !strings.EqualFold(src[th.Kw.Start:th.Kw.End], "nothrow") || th.S != th.Kw {
			t.Errorf("%s: spans hold %q and %q", name, src[th.Kw.Start:th.Kw.End], src[th.S.Start:th.S.End])
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
	for _, name := range []string{"find", "bare", "arrow", "supports", "of", "name", "len"} {
		if !got[name] {
			t.Errorf("%s: no nothrow", name)
		}
	}
	for _, name := range []string{"keep", "none"} {
		if got[name] {
			t.Errorf("%s: nothrow it does not declare", name)
		}
	}
	refused := map[string]string{
		"function f() nothrow throws X {}":       "together",
		"function f(): int throws X nothrow {}":  "together",
		"class C { f(): int nothrow throws X; }": "together",
		"function f() nothrow nothrow {}":        "",
		"function f() nothrow X {}":              "",
		"$f = function () nothrow {};":           "",
		"$f = fn () nothrow => 1;":               "",
	}
	// The other word before a parameter list names the next member, and
	// the member before it lacks its `;`, which the message says as it
	// does after any member.
	refused["interface I {\n    a(): int throws A\n    nothrow(): int;\n}"] = `unexpected token "nothrow"`
	refused["interface I {\n    a(): int nothrow\n    throws(): int;\n}"] = `unexpected token "throws"`
	refused["interface I {\n    a(): int throws A\n    nothrow<T>(T $x): T;\n}"] = `unexpected token "nothrow"`
	refused["abstract class C {\n    abstract a(): int nothrow\n    throws() {}\n}"] = `unexpected token "throws"`
	for src, msg := range refused {
		_, err := Parse([]byte(src), lexer.ModePhaenon)
		if err == nil {
			t.Errorf("%q parsed; want a syntax error", src)
			continue
		}
		if !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: %v; want a message containing %q", src, err, msg)
		}
	}
	if _, err := Parse([]byte("<?php function f() nothrow {}"), lexer.ModePHP); err == nil {
		t.Errorf("nothrow parsed in PHP mode")
	}
}

// TestNothrowStaysAName: PHP code that names something `nothrow` parses
// as before, in Phaenon mode as in PHP mode; php -l agrees
// (TestNothrowNamesUnderPHP).
func TestNothrowStaysAName(t *testing.T) {
	for _, mode := range []lexer.Mode{lexer.ModePHP, lexer.ModePhaenon} {
		src := nothrowNames
		if mode == lexer.ModePhaenon {
			src = strings.TrimPrefix(src, "<?php\n")
		}
		if _, err := Parse([]byte(src), mode); err != nil {
			t.Errorf("mode %v: %v", mode, err)
		}
	}
	// A method named nothrow, declared without a keyword, with the clause;
	// and methods named nothrow and throws after a member that ends in a
	// clause.
	for _, src := range []string{
		"class C { nothrow(): nothrow nothrow {} static nothrow() nothrow => 1; }",
		"interface I {\n    a(): int throws A;\n    nothrow(): int;\n    b(): int nothrow;\n    throws(): int throws A;\n    c(): int nothrow;\n    nothrow<T>(T $x): T nothrow;\n}",
		"class C {\n    a(): int throws A {}\n    nothrow(): int nothrow => 1;\n    b(): int nothrow {}\n    throws(): int throws A => 1;\n}",
	} {
		if _, err := Parse([]byte(src), lexer.ModePhaenon); err != nil {
			t.Errorf("%q: %v", src, err)
		}
	}
}

// nothrowNames is PHP that names a namespace, an import, a constant, a
// function, a class, its members, an enum case and a label `nothrow`.
const nothrowNames = `<?php
namespace nothrow;
use Foo\nothrow as nothrow2;
const nothrow = 1;
function nothrow(nothrow $nothrow): nothrow { return nothrow; }
class nothrow extends Base implements nothrow\I {
    const nothrow = nothrow;
    public nothrow $nothrow;
    public function nothrow(): nothrow { return $this->nothrow instanceof nothrow ? nothrow::nothrow : nothrow(nothrow); }
    public static function make(): static { return new static(); }
}
interface I { public function nothrow(): void; }
trait T { public function nothrow() {} }
class U { use T { nothrow as protected; T::nothrow as nothrow2; } }
enum E: string implements nothrow\I { case nothrow = 'nothrow'; }
$x = match (nothrow) { nothrow => nothrow and nothrow };
foreach (nothrow as $nothrow) {}
goto nothrow;
nothrow:
$f = fn (nothrow $t): nothrow => $t;
$g = function (nothrow $t): nothrow { return $t; };
`

// TestNothrowNamesUnderPHP holds the parser to php -l, the oracle, on
// `nothrow` in PHP's positions: what php accepts parses in both modes, and
// what it refuses is refused in PHP mode; in Phaenon mode only the clause
// after a named function's or method's signature is new.
func TestNothrowNamesUnderPHP(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php in PATH")
	}
	cases := []struct {
		src     string
		phaenon bool // the clause: Phaenon accepts what php refuses
	}{
		{src: strings.TrimPrefix(nothrowNames, "<?php\n")},
		{src: "function nothrow() {}\nnothrow();\n"},
		{src: "class C { public function nothrow(): int { return 1; } }\n(new C)->nothrow();\n"},
		{src: "class C { const nothrow = 1; public $nothrow; }\necho C::nothrow;\n"},
		{src: "const NOTHROW = 1;\necho NOTHROW;\n"},
		{src: "goto nothrow;\nnothrow:\necho 1;\n"},
		{src: "echo nothrow;\n"},
		{src: "function f(): nothrow { return new nothrow(); }\n"},
		{src: "function f() nothrow {}\n", phaenon: true},
		{src: "function f(): int nothrow { return 1; }\n", phaenon: true},
		{src: "interface I { public function f(): int nothrow; }\n", phaenon: true},
		{src: "class C { public function f() nothrow {} }\n", phaenon: true},
		{src: "$f = function () nothrow {};\n"},
		{src: "$f = fn () nothrow => 1;\n"},
		{src: "function f() nothrow throws X {}\n"},
		{src: "echo 1 nothrow;\n"},
	}
	dir := t.TempDir()
	for i, c := range cases {
		path := filepath.Join(dir, fmt.Sprintf("case%d.php", i))
		if err := os.WriteFile(path, []byte("<?php\n"+c.src), 0o644); err != nil {
			t.Fatal(err)
		}
		phpOK := exec.Command(php, "-l", path).Run() == nil
		_, perr := Parse([]byte("<?php\n"+c.src), lexer.ModePHP)
		_, nerr := Parse([]byte(c.src), lexer.ModePhaenon)
		phaenonOK := phpOK || c.phaenon
		if c.phaenon && phpOK {
			t.Errorf("%q: php accepts what the case says only Phaenon does", c.src)
		}
		if (perr == nil) != phpOK {
			t.Errorf("%q: php -l says %v, the parser in PHP mode %v", c.src, phpOK, perr)
		}
		if (nerr == nil) != phaenonOK {
			t.Errorf("%q: want accepted %v in Phaenon mode, the parser says %v", c.src, phaenonOK, nerr)
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
