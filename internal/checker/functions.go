package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/program"
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
// lacks the class. `nothrow` names nothing. With the declaration's
// docblock (doc) and its name, the clause or its absence is then held
// against the @throws tags (throwsContradiction).
func (x *fnCtx) throwsClause(t *ast.Throws, doc *docblock.Doc, name ast.Span) {
	x.throwsContradiction(t, doc, name)
	if t == nil || t.Nothrow {
		return
	}
	for _, nt := range t.Types {
		if tp := x.typeParamRef(nt.Name); tp != nil {
			if tp.Bound == nil || !types.IsSubtype(tp.Bound, types.Class{Name: "Throwable"}, x.p()) {
				x.report(nt.S, P2319, "type parameter %s is not bounded by Throwable, so throws cannot name it", tp.Name)
			}
			continue
		}
		if x.throwsKeyword(nt) {
			// A type keyword names no class: no Throwable, and nothing for
			// PHP to look up. The declaration leaves it out (program's
			// throwsMember), so nothing else follows from it. `throws void`
			// alone meant `nothrow`; beside a class it says nothing.
			word := nt.Name.Parts[0]
			if strings.EqualFold(word, "void") && len(t.Types) == 1 {
				x.report(nt.S, P2319, "%s is a type and not a class, so throws cannot name it; `nothrow` says that nothing escapes", word)
			} else {
				x.report(nt.S, P2319, "%s is a type and not a class, so throws cannot name it; throws names Throwable classes", word)
			}
			continue
		}
		saved := x.nameOnly
		x.nameOnly, x.inDocblock = true, true
		name, ok := x.resolveClassName(nt.Name, nt.S)
		x.nameOnly, x.inDocblock = saved, false
		if !ok {
			continue
		}
		if !x.p().IsSubclassOf(name, "Throwable") {
			x.report(nt.S, P2319, "%s is not a Throwable, so throws cannot name it", name)
		}
	}
}

// throwsKeyword reports a member of a `throws` clause that names a type
// and no class: a type keyword (program.NonClassTypeName) for which no
// class of that name resolves.
func (x *fnCtx) throwsKeyword(nt *ast.NamedType) bool {
	if len(nt.Name.Parts) != 1 || nt.Name.Kind != ast.NameUnqualified || !program.NonClassTypeName(nt.Name.Parts[0]) {
		return false
	}
	return x.p().Class(x.res.ClassName(nt.Name)) == nil
}

// throwsContradiction reports a declaration that says that nothing
// escapes, with `nothrow` or a `@throws void` tag, and also that something
// does, with a `throws` clause or a @throws tag other than `@throws void`
// (design/functions.md §4.5): P2320, once for the declaration however many
// tags take part. `nothrow` beside `throws` is the same contradiction in
// one place, a syntax error; PHPStan joins a declaration's tags into one
// type, in which `void` is no Throwable (throws.notThrowable). The
// finding stands at the clause's word when there is a clause, at the name
// when the docblock alone says both.
func (x *fnCtx) throwsContradiction(t *ast.Throws, doc *docblock.Doc, name ast.Span) {
	var void, other []string
	if doc != nil {
		for _, tag := range doc.Throws {
			if tag.IsVoid() {
				void = append(void, "@throws "+tag.String())
			} else {
				other = append(other, "@throws "+tag.String())
			}
		}
	}
	switch {
	case t != nil && t.Nothrow:
		if len(other) > 0 {
			x.report(t.Kw, P2320, "nothrow says that nothing escapes, and the docblock's %s that something does", strings.Join(other, " and "))
		}
	case t != nil:
		// A type keyword in the clause names nothing that escapes: it is
		// P2319, and the declaration leaves it out.
		var names []string
		for _, nt := range t.Types {
			if x.throwsKeyword(nt) {
				continue
			}
			names = append(names, string(x.file.Src[nt.S.Start:nt.S.End]))
		}
		if len(void) > 0 && len(names) > 0 {
			x.report(t.Kw, P2320, "the docblock's %s says that nothing escapes, and throws %s that something does", void[0], strings.Join(names, "|"))
		}
	case len(void) > 0 && len(other) > 0:
		x.report(name, P2320, "%s says that nothing escapes, and %s that something does", void[0], strings.Join(other, " and "))
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
