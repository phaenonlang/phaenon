package checker

import (
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
)

// Version guards. Code that runs on several PHP versions tests for what it
// uses: `if (\function_exists('clamp'))`, `if (\PHP_VERSION_ID >= 80600)`,
// or the early exit `if (PHP_VERSION_ID < 80600) { return …; }`. Inside a
// guard a library symbol newer than the target is not P4011: the code that
// uses it only runs where it exists, as PHPStan reads function_exists().

// libraryGuard is what a condition proves while its branch runs: symbols
// that exist, keyed as the library checks key them ("function clamp",
// "class time\duration", "method isreadable", "const ARRAY_FILTER_USE_VALUE"),
// and a PHP version the code runs on at least.
type libraryGuard struct {
	symbols map[string]bool
	atLeast phpversion.Version
}

func (g libraryGuard) empty() bool { return len(g.symbols) == 0 && g.atLeast.IsZero() }

// guarded reports whether an active guard proves a symbol PHP v added.
func (c *Checker) guarded(key string, v phpversion.Version) bool {
	for _, g := range c.guards {
		if g.symbols[key] || (!g.atLeast.IsZero() && g.atLeast.AtLeast(v)) {
			return true
		}
	}
	return false
}

func (c *Checker) pushGuard(g libraryGuard) func() {
	if g.empty() {
		return func() {}
	}
	c.guards = append(c.guards, g)
	n := len(c.guards)
	return func() { c.guards = c.guards[:n-1] }
}

// guardOf reads what a condition proves when it holds (positive) and when
// it does not (negative). Only the forms PHP code writes for the purpose
// are read: the *_exists() and defined() calls with a literal name, the
// PHP_VERSION_ID comparisons with an integer, and their conjunctions,
// disjunctions and negations.
func (x *fnCtx) guardOf(e ast.Expr) (positive, negative libraryGuard) {
	switch e := e.(type) {
	case *ast.Paren:
		return x.guardOf(e.Expr)
	case *ast.Unary:
		if e.Op == token.Kind('!') {
			p, n := x.guardOf(e.Expr)
			return n, p
		}
	case *ast.Binary:
		switch e.Op {
		case token.T_BOOLEAN_AND, token.T_LOGICAL_AND:
			lp, _ := x.guardOf(e.Left)
			rp, _ := x.guardOf(e.Right)
			return mergeGuards(lp, rp), libraryGuard{}
		case token.T_BOOLEAN_OR, token.T_LOGICAL_OR:
			_, ln := x.guardOf(e.Left)
			_, rn := x.guardOf(e.Right)
			return libraryGuard{}, mergeGuards(ln, rn)
		}
		return versionComparison(e)
	case *ast.Call:
		return x.existsCall(e), libraryGuard{}
	}
	return libraryGuard{}, libraryGuard{}
}

func mergeGuards(a, b libraryGuard) libraryGuard {
	out := libraryGuard{symbols: map[string]bool{}, atLeast: a.atLeast}
	if out.atLeast.Less(b.atLeast) {
		out.atLeast = b.atLeast
	}
	for k := range a.symbols {
		out.symbols[k] = true
	}
	for k := range b.symbols {
		out.symbols[k] = true
	}
	return out
}

// existsCall reads function_exists('f'), class_exists(C::class) and kin,
// method_exists($x, 'm') and defined('C').
func (x *fnCtx) existsCall(e *ast.Call) libraryGuard {
	n, ok := e.Callee.(*ast.Name)
	if !ok || e.Args == nil || len(e.Args.Args) == 0 {
		return libraryGuard{}
	}
	name := func(i int) (string, bool) {
		if i >= len(e.Args.Args) || e.Args.Args[i].Value == nil {
			return "", false
		}
		switch v := e.Args.Args[i].Value.(type) {
		case *ast.StringLit:
			return strings.TrimPrefix(program.StringValue(v), "\\"), true
		case *ast.ClassConstFetch:
			if id, ok := v.Name.(*ast.Ident); ok && strings.EqualFold(id.Name, "class") {
				if cn, ok := v.Class.(*ast.Name); ok {
					return x.res.ClassName(cn), true
				}
			}
		}
		return "", false
	}
	key := ""
	switch strings.ToLower(n.Parts[len(n.Parts)-1]) {
	case "function_exists":
		if s, ok := name(0); ok {
			key = "function " + strings.ToLower(s)
		}
	case "class_exists", "interface_exists", "enum_exists", "trait_exists":
		if s, ok := name(0); ok {
			key = "class " + strings.ToLower(s)
		}
	case "method_exists":
		if s, ok := name(1); ok {
			key = "method " + strings.ToLower(s)
		}
	case "defined":
		if s, ok := name(0); ok {
			key = "const " + s
		}
	}
	if key == "" {
		return libraryGuard{}
	}
	return libraryGuard{symbols: map[string]bool{key: true}}
}

// versionComparison reads PHP_VERSION_ID >= 80600, either way round and
// with any ordering operator.
func versionComparison(e *ast.Binary) (positive, negative libraryGuard) {
	op := e.Op
	okL := isVersionID(e.Left)
	n, okR := intValue(e.Right)
	if !okL || !okR {
		n2, okL2 := intValue(e.Left)
		if !isVersionID(e.Right) || !okL2 {
			return libraryGuard{}, libraryGuard{}
		}
		n, op = n2, flipOrdering(op)
	}
	// The smallest release line at or above a version id: 80600 is 8.6,
	// 80599 would round up to 8.6 as well.
	atLeast := func(v int) phpversion.Version {
		major, minor := v/10000, (v/100)%100
		if v%100 != 0 {
			minor++
		}
		return phpversion.Version{Major: major, Minor: minor}
	}
	switch op {
	case token.T_IS_GREATER_OR_EQUAL:
		return libraryGuard{atLeast: atLeast(n)}, libraryGuard{}
	case token.Kind('>'):
		return libraryGuard{atLeast: atLeast(n + 1)}, libraryGuard{}
	case token.Kind('<'):
		return libraryGuard{}, libraryGuard{atLeast: atLeast(n)}
	case token.T_IS_SMALLER_OR_EQUAL:
		return libraryGuard{}, libraryGuard{atLeast: atLeast(n + 1)}
	}
	return libraryGuard{}, libraryGuard{}
}

func isVersionID(e ast.Expr) bool {
	var n *ast.Name
	switch e := e.(type) {
	case *ast.ConstFetch:
		n = e.Name
	case *ast.Name:
		n = e
	default:
		return false
	}
	return n.Parts[len(n.Parts)-1] == "PHP_VERSION_ID"
}

// alwaysExits reports a branch whose last statement leaves the enclosing
// code: return, throw, exit, continue or break.
func alwaysExits(s ast.Stmt) bool {
	for {
		b, ok := s.(*ast.Block)
		if !ok || len(b.Stmts) == 0 {
			break
		}
		s = b.Stmts[len(b.Stmts)-1]
	}
	switch s := s.(type) {
	case *ast.Return, *ast.Break, *ast.Continue:
		return true
	case *ast.ExprStmt:
		switch s.Expr.(type) {
		case *ast.Throw, *ast.Exit:
			return true
		}
	}
	return false
}

func intValue(e ast.Expr) (int, bool) {
	lit, ok := e.(*ast.IntLit)
	if !ok {
		return 0, false
	}
	v, err := strconv.Atoi(strings.ReplaceAll(lit.Raw, "_", ""))
	return v, err == nil
}
