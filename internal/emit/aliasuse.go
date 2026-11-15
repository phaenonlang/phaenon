package emit

import (
	"bytes"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
)

// An alias is imported with `use`, as a class is, and never reaches PHP:
// nothing in the compiled file names it, and an import of a name that does
// not exist is not PHP a person wrote. So the import is left out of the
// output (design/generics.md §11.6): the statement when aliases are all it
// imports, else those items of its comma list or group.

// dropAliasImports removes the imports of type aliases from the file's use
// statements.
func (e *emitter) dropAliasImports(stmts []ast.Stmt) {
	for _, s := range stmts {
		switch s := s.(type) {
		case *ast.Namespace:
			e.dropAliasImports(s.Body)
		case *ast.Use:
			e.dropAliasImport(s)
		}
	}
}

func (e *emitter) dropAliasImport(u *ast.Use) {
	if u.Kind != ast.UseClass {
		return // `use function`, `use const`
	}
	prefix := ""
	if u.Prefix != nil {
		prefix = strings.Join(u.Prefix.Parts, "\\") + "\\"
	}
	var kept []string
	for _, it := range u.Items {
		if it.Kind == ast.UseClass && e.opts.Program.TypeAlias(prefix+strings.Join(it.Name.Parts, "\\")) != nil {
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
