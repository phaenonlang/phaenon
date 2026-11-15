package lsp

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// A `type` alias is a name of its namespace (design/generics.md §11.6), so
// the server treats it as it treats a class (design/tooling.md §6.1): go
// to definition leads from its name to its declaration in whatever file
// holds it, hover prints the type it stands for, and completion offers it
// with its `use`.

// aliasNamed is the alias a name written at off stands for: in a type
// position or a use statement, resolved as a class name is.
func (r *fileCheck) aliasNamed(n *ast.Name, path []ast.Node, i, off int) *program.TypeAlias {
	if len(n.Parts) == 0 {
		return nil
	}
	if _, ok := path[i-1].(*ast.UseItem); ok {
		fqn := strings.Join(n.Parts, `\`)
		if i >= 2 {
			if u, ok := path[i-2].(*ast.Use); ok && u.Prefix != nil {
				fqn = strings.Join(u.Prefix.Parts, `\`) + `\` + fqn
			}
		}
		return r.prog.TypeAlias(fqn)
	}
	scope := r.file.ScopeAt(off)
	return r.prog.NewResolver(r.file, scope, nil, n.S).TypeAlias(n)
}

// aliasDeclared is the alias a `type` declaration of this file declares.
func (r *fileCheck) aliasDeclared(d *ast.TypeAlias) *program.TypeAlias {
	a := r.prog.TypeAlias(r.file.ScopeAt(d.S.Start).Prefix() + d.Name.Name)
	if a == nil || a.Decl != d {
		return nil // a second declaration of the name: the first is the alias
	}
	return a
}

// describeAlias is an alias's declaration with the type it stands for:
// `type App\Orders\Page<T> = (list<T>, int)`. An alias expands when it is
// resolved, so the type is printed from the lattice, not copied from the
// source.
func describeAlias(a *program.TypeAlias) string {
	var b strings.Builder
	b.WriteString("type " + a.Name)
	if len(a.Params) > 0 {
		b.WriteString("<")
		for i, tp := range a.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(tp.Name)
			if tp.Bound != nil {
				b.WriteString(" of " + types.PrintPhaenon(tp.Bound))
			}
			if tp.Default != nil {
				b.WriteString(" = " + types.PrintPhaenon(tp.Default))
			}
		}
		b.WriteString(">")
	}
	b.WriteString(" = " + types.PrintPhaenon(a.Type))
	return b.String()
}

// aliasItems lists the program's aliases whose short name starts with the
// prefix. One the file does not see by that name is completed with its
// import, as a class is (classItems).
func (s *Server) aliasItems(r *fileCheck, scope *program.Scope, prefix string) []CompletionItem {
	var out []CompletionItem
	for _, a := range r.prog.TypeAliases() {
		short := a.Decl.Name.Name
		if !matches(short, prefix) {
			continue
		}
		it := CompletionItem{Label: short, Kind: completionClass, Detail: describeAlias(a), SortText: "2"}
		if !strings.EqualFold(scope.ResolveClassString(short), a.Name) {
			if _, taken := scope.Imports()[strings.ToLower(short)]; taken {
				it.InsertText = `\` + a.Name // another name has it here
			} else if strings.Contains(a.Name, `\`) {
				it.AdditionalTextEdits = []TextEdit{importEdit(r, a.Name)}
			}
		}
		out = append(out, it)
	}
	return out
}
