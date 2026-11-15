package emit

import (
	"sort"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// A docblock a .phn writes by hand may name a type alias, as a signature
// does: `@return array{total: int, lines: list<Line>}` (design/collections.md
// §6.5). PHPStan knows no alias of a namespace, so the output holds what
// the alias stands for, its class names fully qualified, as the tags the
// emitter generates do. The name is resolved where the docblock stands: a
// type parameter of the declaration it documents and a @phpstan-type of
// the class around it come first, as in the checker.

// docScope is a declaration and the resolver its docblock's types are read
// with.
type docScope struct {
	ast.Span
	res *program.Resolver
}

// docScopes lists the file's named classes, their methods and its
// functions, each with its resolver, in source order.
func (e *emitter) docScopes() []docScope {
	p, f := e.opts.Program, e.opts.File
	var out []docScope
	ast.Inspect(f.AST, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.ClassDecl:
			if d.Name == nil {
				return true
			}
			scope := f.ScopeAt(d.S.Start)
			cl := p.Class(scope.Prefix() + d.Name.Name)
			if cl == nil || cl.Decl != d {
				return true
			}
			cl.Signature()
			res := p.NewResolver(f, cl.Scope, cl, d.S)
			out = append(out, docScope{d.S, res})
			for _, m := range d.Members {
				md, ok := m.(*ast.MethodDecl)
				if !ok {
					continue
				}
				mres := res
				if pm := cl.FindMethod(md.Name.Name); pm != nil && pm.Decl == md {
					mres = res.WithParams(pm.TypeParams)
				}
				out = append(out, docScope{md.S, mres})
			}
		case *ast.FunctionDecl:
			scope := f.ScopeAt(d.S.Start)
			res := p.NewResolver(f, scope, nil, d.S)
			if fn := p.Function(scope.Prefix() + d.Name.Name); fn != nil && fn.Decl == d {
				fn.Signature()
				res = res.WithParams(fn.TypeParams)
			}
			out = append(out, docScope{d.S, res})
		}
		return true
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// docResolver is the resolver of the docblock at offset off: that of the
// declaration the docblock stands before, else of the innermost
// declaration around it, else the file's at that offset.
func (e *emitter) docResolver(off int) *program.Resolver {
	if e.docs == nil {
		e.docs = e.docScopes()
		if e.docs == nil {
			e.docs = []docScope{}
		}
	}
	next := e.codeAfterDoc(off)
	var around *docScope
	for i := range e.docs {
		d := &e.docs[i]
		if d.Start == next {
			return d.res
		}
		if d.Start <= off && off < d.End && (around == nil || d.End-d.Start <= around.End-around.Start) {
			around = d
		}
	}
	if around != nil {
		return around.res
	}
	f := e.opts.File
	return e.opts.Program.NewResolver(f, f.ScopeAt(off), nil, ast.Span{Start: off, End: off})
}

// codeAfterDoc is where the code after the comment at off begins: the
// declaration a docblock documents starts there.
func (e *emitter) codeAfterDoc(off int) int {
	src := e.src
	i := off
	if i+1 < len(src) && src[i] == '/' && src[i+1] == '*' {
		i += 2
		for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
			i++
		}
		i += 2
	}
	for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\r' || src[i] == '\n') {
		i++
	}
	return i
}

// aliasExpander gives translateType the type a mention of an alias stands
// for, in the docblock at offset off: the alias's name, with the type
// arguments written after it. nil when the file is compiled without type
// information.
func (e *emitter) aliasExpander(off int) aliasExpand {
	if e.opts.Program == nil || e.opts.File == nil {
		return nil
	}
	var res *program.Resolver
	return func(mention string, in mentionContext) (string, bool) {
		node, err := docblock.ParseType(mention)
		if err != nil || node.Kind != docblock.KindNamed {
			return "", false
		}
		if res == nil {
			res = e.docResolver(off)
		}
		if res.DocAlias(node.Name) == nil {
			return "", false
		}
		t := res.Doc(node)
		if in.nullable && !in.union && !in.intersection {
			return types.PrintQualifiedNullable(t), true
		}
		if in.nullable {
			t = types.Nullable(t)
		}
		switch {
		case in.intersection:
			return types.PrintQualifiedMember(t, true), true
		case in.union:
			return types.PrintQualifiedMember(t, false), true
		}
		return types.PrintQualified(t), true
	}
}
