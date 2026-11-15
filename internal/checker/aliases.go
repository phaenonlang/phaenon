package checker

import (
	"fmt"
	"strconv"
	"strings"

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
	// The file gives the name to something else: a type position reads the
	// import, as PHP reads a class name, and the alias would never be
	// meant by its own file.
	if it := x.importNamed(a.Name.Name); it != nil && !strings.EqualFold(it.target, name) {
		x.report(a.Name.S, P2511, "type alias %s is declared under a name its file imports: `use %s%s;` on line %d gives %s to another type", decl.Name, it.target, it.as, it.line, a.Name.Name)
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
	// The classes the right-hand side names are judged here, once: the
	// expansion carries them into every docblock that uses the alias,
	// where PHPStan would meet a class that does not exist.
	x.declaredTypes(a.Type, true)
	x.res = saved
}

// fileImport is a class import of a file: what it names, the ` as Name` it
// was written with, and its line.
type fileImport struct {
	target, as string
	line       int
}

// importNamed is the class import that gives name, in any letter case, in
// the namespace the context checks; nil when the file imports none.
func (x *fnCtx) importNamed(name string) *fileImport {
	scope := x.res.Scope()
	if _, imported := scope.Imports()[strings.ToLower(name)]; !imported {
		return nil
	}
	var found *fileImport
	ast.Inspect(x.file.AST, func(n ast.Node) bool {
		u, ok := n.(*ast.Use)
		if !ok {
			return found == nil
		}
		if u.Kind != ast.UseClass || x.file.ScopeAt(u.S.Start) != scope {
			return false
		}
		for _, it := range u.Items {
			if it.Kind != ast.UseClass {
				continue
			}
			given, as := it.Name.Parts[len(it.Name.Parts)-1], ""
			if it.Alias != nil {
				given, as = it.Alias.Name, " as "+it.Alias.Name
			}
			if !strings.EqualFold(given, name) {
				continue
			}
			target := strings.Join(it.Name.Parts, "\\")
			if u.Prefix != nil {
				target = strings.Join(u.Prefix.Parts, "\\") + "\\" + target
			}
			line, _ := x.file.Position(it.S.Start)
			found = &fileImport{target: target, as: as, line: line}
		}
		return false
	})
	return found
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

// checkShape validates a shape written in a `type` declaration
// (design/collections.md §6.1): the types of its entries, and that a key
// is written once. A quoted key that is a decimal integer is that
// integer's key, so `'1'` after `1` is the same key again.
func (x *fnCtx) checkShape(t *ast.ShapeType) {
	type key struct {
		name  string
		n     int64
		isInt bool
	}
	seen := map[key]bool{}
	for _, e := range t.Entries {
		k := key{name: e.Name}
		if v, err := strconv.ParseInt(e.Name, 10, 64); e.IsInt {
			k = key{n: e.Int, isInt: true}
		} else if err == nil {
			k = key{n: v, isInt: true}
		}
		if seen[k] {
			x.report(e.Key, P2511, "shape key %s is written twice", e.Raw)
		}
		seen[k] = true
		x.checkTypeExpr(e.Type)
	}
}
