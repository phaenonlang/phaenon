package migrate

// The list-types codemod's measure (design/tooling.md §8.2): on a tree
// whose array types are written, StripListTypes gives the declarations the
// codemod considers their bare `array` back, and the codemod is measured by
// how much of what was written it writes again.

import (
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// StrippedType is a declaration StripListTypes gave its bare array back.
type StrippedType struct {
	Class  string // fully qualified
	Kind   string // "property", "return" or "parameter"
	Member string // the property, or the method
	Param  string // the parameter's name, for a parameter
	Line   int
	Type   string // the type as written
}

// What names the declaration as the codemod's notes do.
func (s StrippedType) What() string {
	switch s.Kind {
	case "property":
		return "property $" + s.Member
	case "return":
		return s.Member + "()'s return"
	}
	return "parameter $" + s.Param + " of " + s.Member + "()"
}

// StripListTypes writes `array` for the native list<…>, map<…> and
// array<…> types (nullable or not) of the declarations the codemod
// considers: a private property, a final method's return, a private
// method's parameter, none of them typed by a docblock, in a class that
// uses no trait.
func StripListTypes(src []byte) ([]byte, []StrippedType, error) {
	f, err := parser.Parse(src, lexer.ModePhaenon)
	if err != nil {
		return nil, nil, err
	}
	ns, _ := fileNames(f)
	lines := lineStarts(src)
	var spans []ast.Span
	var out []StrippedType
	strip := func(t ast.TypeExpr, rec StrippedType) {
		if n, ok := t.(*ast.NullableType); ok {
			t = n.Type
		}
		nt, ok := t.(*ast.NamedType)
		if !ok || len(nt.TypeArgs) == 0 || len(nt.Name.Parts) != 1 {
			return
		}
		switch strings.ToLower(nt.Name.Parts[0]) {
		case "list", "map", "array":
		default:
			return
		}
		spans = append(spans, nt.S)
		rec.Line = lineAt(lines, nt.S.Start)
		rec.Type = string(src[nt.S.Start:nt.S.End])
		out = append(out, rec)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.ClassDecl)
		if !ok || cl.Name == nil || usesTrait(cl) {
			return true
		}
		class := cl.Name.Name
		if ns != "" {
			class = ns + "\\" + class
		}
		for _, m := range cl.Members {
			switch m := m.(type) {
			case *ast.PropertyDecl:
				if m.Modifiers&ast.ModPrivate != 0 && len(m.Attrs) == 0 && len(m.Props) == 1 && !docVars(m.Doc) {
					strip(m.Type, StrippedType{Class: class, Kind: "property", Member: m.Props[0].Var.Name})
				}
			case *ast.MethodDecl:
				priv := m.Modifiers&ast.ModPrivate != 0
				final := priv || m.Modifiers&ast.ModFinal != 0 || cl.Modifiers&ast.ModFinal != 0
				if final && !docReturns(m.Doc) {
					strip(m.ReturnType, StrippedType{Class: class, Kind: "return", Member: m.Name.Name})
				}
				if !priv {
					continue
				}
				for _, p := range m.Params {
					if p.Modifiers == 0 && !p.Variadic && !p.ByRef && !docParams(m.Doc, p.Var.Name) {
						strip(p.Type, StrippedType{Class: class, Kind: "parameter", Member: m.Name.Name, Param: p.Var.Name})
					}
				}
			}
		}
		return true
	})
	if len(spans) == 0 {
		return src, nil, nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	var b strings.Builder
	pos := 0
	for _, s := range spans {
		b.Write(src[pos:s.Start])
		b.WriteString("array")
		pos = s.End
	}
	b.Write(src[pos:])
	return []byte(b.String()), out, nil
}
