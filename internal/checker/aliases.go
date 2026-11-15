package checker

import (
	"fmt"

	"github.com/phaenonlang/phaenon/internal/ast"
)

// This file holds the rules of a `type` alias as a name of its namespace
// (design/generics.md §11.6): the name is declared once, by an alias or by
// a class-like; an alias does not mention itself; and where PHP would look
// for a class, an alias is none.

// checkTypeAlias validates a `type` declaration.
func (x *fnCtx) checkTypeAlias(a *ast.TypeAlias) {
	name := x.res.Scope().Prefix() + a.Name.Name
	decl := x.p().TypeAlias(name)
	if decl == nil {
		return
	}
	if decl.Decl != a {
		// A duplicate declaration: the first one won.
		line, _ := decl.File.Position(decl.Decl.Name.S.Start)
		x.report(a.Name.S, P2511, "type alias %s is already declared (%s:%d)", decl.Name, decl.File.Path, line)
		return
	}
	// One table of names is what makes `use` unambiguous: a namespace's
	// name is an alias or a class-like, never both.
	if cl := x.p().Class(name); cl != nil {
		x.report(a.Name.S, P2511, "type alias %s has the name of the %s %s: a namespace declares a name once", decl.Name, kindWord(cl), cl.Name)
	}
	if decl.Cyclic {
		x.report(a.Name.S, P2015, "type alias %s mentions itself: an alias is expanded wherever it is used, so it has no expansion; a recursive structure is a class", decl.Name)
	}
	saved := x.res
	x.res = x.res.WithParams(decl.Params)
	x.checkTypeParams(a.TypeParams)
	x.checkTypeExpr(a.Type)
	x.res = saved
}

// unknownClass is the message of a name PHP would look for a class under
// and find none (P2001); what is "class", "interface", "trait" or
// "attribute class". A type alias is such a name: it is erased, and stands
// where a type is written and in `use`, nowhere else.
func (c *Checker) unknownClass(what, name string) string {
	if a := c.p.TypeAlias(name); a != nil {
		return fmt.Sprintf("unknown %s %s: %s is a type alias, which is erased and names no class", what, name, a.Decl.Name.Name)
	}
	return fmt.Sprintf("unknown %s %s", what, name)
}
