package emit

import (
	"bytes"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
)

// An import the compiled file would not use is left out of it. An import
// of a name nothing in the output mentions is not PHP a person wrote, and
// a fixer that removes unused imports would take it out of the output and,
// through phn fix, out of the source, where it is used.
//
// Two kinds of import end up so (design/generics.md §11.6):
//
//   - the import of a type alias. An alias never reaches PHP: nothing in
//     the compiled file names it;
//   - an import that only erased names use: a namespace through which
//     aliases alone are named (`use App\Orders;` for `Orders\Row`), a class
//     named only among the type arguments of an alias (`Page<Customer>`,
//     whose expansion writes the class by its qualified name), in a `type`
//     declaration, which leaves no trace, or among the type arguments of a
//     call, which are erased. A docblock's mention is erased too where
//     the emitter expands the alias it names.
//
// An import the output still uses anywhere stays: in a signature, a `new`,
// an attribute, a docblock the emitter leaves or writes with the name, a
// `{@see}`. So does one nothing uses at all, which is the checker's to
// report (P2013), not the emitter's to remove.

// dropErasedImports removes those imports from the file's use statements:
// the statement when nothing is left of it, else the items.
func (e *emitter) dropErasedImports(f *ast.File) {
	var uses []*ast.Use
	var statements []ast.Span
	var collect func(stmts []ast.Stmt)
	collect = func(stmts []ast.Stmt) {
		for _, s := range stmts {
			switch s := s.(type) {
			case *ast.Namespace:
				collect(s.Body)
			case *ast.Use:
				uses = append(uses, s)
				statements = append(statements, s.S)
			}
		}
	}
	collect(f.Stmts)
	if len(uses) == 0 {
		return
	}
	p := e.opts.Program
	drop := map[*ast.UseItem]bool{}
	// The class imports that are no alias's, by the name each gives.
	named := map[string][]*ast.UseItem{}
	for _, u := range uses {
		if u.Kind != ast.UseClass {
			continue // `use function`, `use const`
		}
		for _, it := range u.Items {
			if it.Kind != ast.UseClass {
				continue
			}
			if p.TypeAlias(importTarget(u, it)) != nil {
				drop[it] = true
				continue
			}
			name := strings.ToLower(importName(it))
			named[name] = append(named[name], it)
		}
	}
	if len(named) > 0 && p.HasTypeAliases() || len(named) > 0 && hasCallTypeArgs(f) {
		for name := range e.erasedOnly(f, statements, named) {
			for _, it := range named[name] {
				drop[it] = true
			}
		}
	}
	if len(drop) == 0 {
		return
	}
	for _, u := range uses {
		e.dropImports(u, drop)
	}
}

// importTarget is the qualified name an import item names.
func importTarget(u *ast.Use, it *ast.UseItem) string {
	name := strings.Join(it.Name.Parts, "\\")
	if u.Prefix != nil {
		name = strings.Join(u.Prefix.Parts, "\\") + "\\" + name
	}
	return name
}

// importName is the name an import item gives its target in the file.
func importName(it *ast.UseItem) string {
	if it.Alias != nil {
		return it.Alias.Name
	}
	return it.Name.Parts[len(it.Name.Parts)-1]
}

// hasCallTypeArgs reports whether a call of the file passes type
// arguments, which are erased with the names in them.
func hasCallTypeArgs(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		found = found || len(callTypeArgs(n)) > 0
		return !found
	})
	return found
}

func callTypeArgs(n ast.Node) []ast.TypeExpr {
	switch n := n.(type) {
	case *ast.Call:
		return n.TypeArgs
	case *ast.MethodCall:
		return n.TypeArgs
	case *ast.StaticCall:
		return n.TypeArgs
	case *ast.New:
		return n.TypeArgs
	}
	return nil
}

// erasedOnly returns, among the lower-cased names the file's imports give,
// those that erased names alone use: used somewhere, and nowhere the
// output keeps. The uses are the ones the checker counts
// (program.ImportUses).
func (e *emitter) erasedOnly(f *ast.File, statements []ast.Span, named map[string][]*ast.UseItem) map[string]bool {
	// The spans whose text the output does not have: a `type` declaration
	// with its docblock, the mention of an alias in a type position with
	// its type arguments, the type arguments of a call.
	var erased []ast.Span
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.TypeAlias:
			erased = append(erased, e.aliasDeclaration(n))
			return false
		case *ast.NamedType:
			if e.docResolver(n.S.Start).TypeAlias(n.Name) != nil {
				erased = append(erased, n.S)
				return false
			}
		default:
			if args := callTypeArgs(n); len(args) > 0 {
				erased = append(erased, ast.Span{Start: args[0].Span().Start, End: args[len(args)-1].Span().End})
			}
		}
		return true
	})
	within := func(s ast.Span) bool {
		for _, span := range erased {
			if span.Start <= s.Start && s.End <= span.End {
				return true
			}
		}
		return false
	}
	idents := map[int]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			idents[id.S.Start] = true
		}
		return true
	})
	wanted := make(map[string]bool, len(named))
	for name := range named {
		wanted[name] = true
	}
	gone := map[string]bool{}
	program.ImportUses(e.src, e.tokens(), statements, idents, wanted, func(name string, at ast.Span, comment bool) bool {
		kept := !within(at)
		if kept && comment && bytes.HasPrefix(e.src[at.Start:at.End], []byte("/**")) {
			// A docblock keeps the name unless the emitter expands every
			// alias that mentions it.
			written := translateDocWith(string(e.src[at.Start:at.End]), e.aliasExpander(at.Start))
			kept = program.Mentions(strings.ToLower(written), name)
		}
		if kept {
			delete(gone, name)
			return false // the output uses the import: nothing more to ask
		}
		gone[name] = true
		return true
	})
	return gone
}

// aliasDeclaration is the span a `type` declaration is removed with: the
// statement, and the docblock that documents it.
func (e *emitter) aliasDeclaration(a *ast.TypeAlias) ast.Span {
	s := a.S
	if a.Doc == "" {
		return s
	}
	if at := bytes.LastIndex(e.src[:s.Start], []byte(a.Doc)); at >= 0 && len(bytes.TrimSpace(e.src[at+len(a.Doc):s.Start])) == 0 {
		s.Start = at
	}
	return s
}

// dropImports removes the items of a use statement that drop holds.
func (e *emitter) dropImports(u *ast.Use, drop map[*ast.UseItem]bool) {
	var kept []string
	for _, it := range u.Items {
		if drop[it] {
			if e.droppedImports == nil {
				e.droppedImports = map[string]bool{}
			}
			e.droppedImports[strings.ToLower(importName(it))+"\x00"+strings.ToLower(importTarget(u, it))] = true
			continue
		}
		kept = append(kept, string(e.src[it.S.Start:it.S.End]))
	}
	switch {
	case len(kept) == len(u.Items):
		return
	case len(kept) == 0:
		if e.aliasUses == nil {
			e.aliasUses = map[*ast.Use]bool{}
		}
		e.aliasUses[u] = true
		e.drop = append(e.drop, u.S)
		return
	}
	// What is left is written anew, on one line: the statement's own
	// layout was for the list it had.
	text := "use " + strings.Join(kept, ", ") + ";"
	if u.Prefix != nil {
		text = "use " + string(e.src[u.Prefix.S.Start:u.Prefix.S.End]) + "\\{" + strings.Join(kept, ", ") + "};"
	}
	if bytes.IndexByte(e.src[u.S.Start:u.S.End], '\n') >= 0 {
		e.replaceFree(u.S, text)
		return
	}
	e.replace(u.S, text)
}

// importDropped reports whether the file's import of target under name
// was left out of the output.
func (e *emitter) importDropped(name, target string) bool {
	return e.droppedImports[strings.ToLower(name)+"\x00"+strings.ToLower(target)]
}
