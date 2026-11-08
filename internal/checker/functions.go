package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/types"
)

// The rules of design/functions.md §3–4 that are not types of their own:
// where an expression body may stand, and what a `throws` clause may name.
// An expression body is typed by the statement it stands for (the parser
// puts a Return, or an ExprStmt under void and never, in the
// declaration's Body), so everything a block's return is held to holds
// for the arrow too.

// throwsClause checks a declaration's `throws A|B`: each member names a
// Throwable class or interface, or a type parameter bounded by one. The
// clause is documentation the checker verifies, as a @throws tag is, and
// not a checked-exception rule: callers are not held to it. It compiles
// to a docblock, so like a `catch` it fails nothing on a target that
// lacks the class.
func (x *fnCtx) throwsClause(t *ast.Throws) {
	if t == nil {
		return
	}
	for _, nt := range t.Types {
		if tp := x.typeParamRef(nt.Name); tp != nil {
			if tp.Bound == nil || !types.IsSubtype(tp.Bound, types.Class{Name: "Throwable"}, x.p()) {
				x.report(nt.S, P2319, "type parameter %s is not bounded by Throwable, so throws cannot name it", tp.Name)
			}
			continue
		}
		saved := x.nameOnly
		x.nameOnly = true
		name, ok := x.resolveClassName(nt.Name, nt.S)
		x.nameOnly = saved
		if !ok {
			continue
		}
		if !x.p().IsSubclassOf(name, "Throwable") {
			x.report(nt.S, P2319, "%s is not a Throwable, so throws cannot name it", name)
		}
	}
}

// exprBody judges where an expression body stands (design/functions.md
// §3.1) and returns the statements to check in place of the body. A
// constructor or destructor may not have one: what PHP does with a value
// they return is its own rule (8.6 deprecates it), so the expression is
// checked as a statement and no second diagnostic follows. Nor may the
// expression yield: the function would be a generator, which a block
// says plainly. An abstract or interface method with an arrow has a body,
// reported where any body is.
func (x *fnCtx) exprBody(name string, b *ast.ExprBody, body []ast.Stmt) []ast.Stmt {
	if b == nil {
		return body
	}
	switch strings.ToLower(name) {
	case "__construct", "__destruct":
		x.report(b.Arrow, P1004, "%s() cannot have an expression body; write a block", name)
		return []ast.Stmt{&ast.ExprStmt{S: b.S, Expr: b.Expr}}
	}
	if y := firstYield(b.Expr); y != nil {
		x.report(y.Span(), P1004, "an expression body cannot yield: it would make %s() a generator; write a block", name)
	}
	return body
}

// firstYield is the first yield in an expression that makes its function
// a generator (one inside a closure is the closure's).
func firstYield(e ast.Expr) ast.Node {
	var found ast.Node
	ast.Inspect(e, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		switch n.(type) {
		case *ast.Yield, *ast.YieldFrom:
			found = n
		case *ast.Closure, *ast.ArrowFunc, *ast.ClassDecl:
			return false
		}
		return found == nil
	})
	return found
}
