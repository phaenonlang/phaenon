package lsp

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/format"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/tabs"
	"github.com/phaenonlang/phaenon/internal/types"
)

// ------------------------------------------------------------------ the symbol at a position

// symbol is what a position names: a declaration, a use of one, or an
// expression with its type.
type symbol struct {
	class    *program.Class
	method   *program.Method
	fn       *program.Function
	prop     *program.Property
	constant *program.Constant
	enumCase *program.EnumCase
	variable string     // a variable's name, without the $
	typ      types.Type // an expression's type, when the checker gave one
	span     ast.Span   // the name or expression at the position
	decl     *location  // where a variable is first assigned or declared
}

// location is a span in a program's file.
type location struct {
	file *program.File
	span ast.Span
}

// path returns the nodes whose span holds off, outermost first.
func nodePath(f *ast.File, off int) []ast.Node {
	var path []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		s := n.Span()
		if _, ok := n.(*ast.File); !ok && (off < s.Start || off >= s.End) {
			return false
		}
		path = append(path, n)
		return true
	})
	return path
}

// symbolAt resolves the position: the innermost node that names something,
// or the innermost expression the checker typed.
func (r *fileCheck) symbolAt(off int) *symbol {
	if r.file == nil {
		return nil
	}
	path := nodePath(r.file.AST, off)
	if len(path) <= 1 && off > 0 {
		path = nodePath(r.file.AST, off-1) // the cursor just past a name
	}
	for i := len(path) - 1; i >= 1; i-- {
		if s := r.resolve(path, i, off); s != nil {
			return s
		}
	}
	return nil
}

func (r *fileCheck) resolve(path []ast.Node, i, off int) *symbol {
	n, parent := path[i], path[i-1]
	p := r.prog
	switch n := n.(type) {
	case *ast.Ident:
		switch par := parent.(type) {
		case *ast.MethodCall:
			if par.Name == n {
				for _, c := range r.classesOf(par.Object) {
					if m := c.FindMethod(n.Name); m != nil {
						return &symbol{method: m, span: n.S}
					}
				}
				return &symbol{span: n.S}
			}
		case *ast.PropertyFetch:
			if par.Name == n {
				for _, c := range r.classesOf(par.Object) {
					if pr := c.FindProperty(n.Name); pr != nil {
						return &symbol{prop: pr, typ: r.types[par], span: n.S}
					}
				}
				return &symbol{typ: r.types[par], span: n.S}
			}
		case *ast.StaticCall:
			if par.Name == n {
				if c := r.classOfExpr(par.Class, path, off); c != nil {
					if m := c.FindMethod(n.Name); m != nil {
						return &symbol{method: m, span: n.S}
					}
				}
			}
		case *ast.ClassConstFetch:
			if par.Name == n {
				if c := r.classOfExpr(par.Class, path, off); c != nil {
					if k := c.FindCase(n.Name); k != nil {
						return &symbol{enumCase: k, class: c, span: n.S}
					}
					if k := c.FindConst(n.Name); k != nil {
						return &symbol{constant: k, typ: r.types[par], span: n.S}
					}
				}
			}
		case *ast.ClassDecl:
			if par.Name == n {
				if c := r.declared(par); c != nil {
					return &symbol{class: c, span: n.S}
				}
			}
		case *ast.MethodDecl:
			if par.Name == n {
				if c := r.enclosingClass(path, off); c != nil {
					if m := c.FindMethod(n.Name); m != nil {
						return &symbol{method: m, span: n.S}
					}
				}
			}
		case *ast.FunctionDecl:
			if par.Name == n {
				scope := r.file.ScopeAt(off)
				if fn := p.Function(scope.Prefix() + n.Name); fn != nil {
					return &symbol{fn: fn, span: n.S}
				}
			}
		case *ast.ConstItem, *ast.EnumCase:
			if c := r.enclosingClass(path, off); c != nil {
				if k := c.FindCase(n.Name); k != nil {
					return &symbol{enumCase: k, class: c, span: n.S}
				}
				if k := c.FindConst(n.Name); k != nil {
					return &symbol{constant: k, span: n.S}
				}
			}
		}
	case *ast.Name:
		switch par := parent.(type) {
		case *ast.Call:
			if par.Callee == n {
				if fn := r.function(n, off); fn != nil {
					return &symbol{fn: fn, span: n.S}
				}
				return nil
			}
		case *ast.ConstFetch:
			ns, global := r.file.ScopeAt(off).ResolveConst(n)
			for _, name := range []string{ns, global} {
				if k := p.Constant(name); k != nil {
					return &symbol{constant: k, typ: r.types[par], span: n.S}
				}
			}
			return &symbol{typ: r.types[par], span: n.S}
		case *ast.UseItem:
			if c := p.Class(strings.Join(n.Parts, `\`)); c != nil {
				return &symbol{class: c, span: n.S}
			}
			return nil
		}
		if c := r.className(n, path, off); c != nil {
			return &symbol{class: c, span: n.S}
		}
	case *ast.Variable:
		s := &symbol{variable: n.Name, typ: r.types[n], span: n.S}
		if a, ok := parent.(*ast.Assign); ok && a.Target == n && s.typ == nil {
			// An assignment's target is not typed as an expression: it
			// holds what the assignment gives it.
			s.typ = r.types[a]
			if s.typ == nil {
				s.typ = r.types[a.Value]
			}
		}
		if n.Name != "this" {
			s.decl = r.firstAssignment(path, n.Name)
		}
		return s
	case *ast.Param:
		if n.Var != nil {
			return &symbol{variable: n.Var.Name, span: n.Var.S, decl: &location{r.file, n.Var.S}}
		}
	case ast.Expr:
		if t, ok := r.types[n]; ok {
			return &symbol{typ: t, span: n.Span()}
		}
	}
	return nil
}

// classesOf returns the classes of an expression's type: its class, the
// classes of a union's members, static's and $this's class.
func (r *fileCheck) classesOf(e ast.Expr) []*program.Class {
	var out []*program.Class
	var walk func(t types.Type)
	walk = func(t types.Type) {
		switch t := t.(type) {
		case types.Class:
			if c := r.prog.Class(t.Name); c != nil {
				out = append(out, c)
			}
		case types.Static:
			if t.Of != nil {
				walk(*t.Of)
			}
		case types.This:
			if t.Of != nil {
				walk(*t.Of)
			}
		case types.Union:
			for _, m := range t.Types {
				walk(m)
			}
		case types.Intersection:
			for _, m := range t.Types {
				walk(m)
			}
		case types.Param:
			if t.Bound != nil {
				walk(t.Bound)
			}
		}
	}
	walk(r.types[e])
	return out
}

// classOfExpr is the class a static access names: `Foo::`, `self::`,
// `static::`, `parent::`, or an expression's class.
func (r *fileCheck) classOfExpr(e ast.Expr, path []ast.Node, off int) *program.Class {
	if n, ok := e.(*ast.Name); ok {
		return r.className(n, path, off)
	}
	if cs := r.classesOf(e); len(cs) > 0 {
		return cs[0]
	}
	if cs, ok := r.types[e].(types.ClassString); ok && cs.Of != nil {
		if c, ok := cs.Of.(types.Class); ok {
			return r.prog.Class(c.Name)
		}
	}
	return nil
}

// className resolves a class name where it is written.
func (r *fileCheck) className(n *ast.Name, path []ast.Node, off int) *program.Class {
	if len(n.Parts) == 1 {
		switch strings.ToLower(n.Parts[0]) {
		case "self", "static":
			return r.enclosingClass(path, off)
		case "parent":
			if c := r.enclosingClass(path, off); c != nil {
				return c.ParentClass()
			}
			return nil
		}
	}
	return r.prog.Class(r.file.ScopeAt(off).ResolveClass(n))
}

func (r *fileCheck) function(n *ast.Name, off int) *program.Function {
	ns, global := r.file.ScopeAt(off).ResolveFunction(n)
	if fn := r.prog.Function(ns); fn != nil {
		return fn
	}
	return r.prog.Function(global)
}

// declared is the class a declaration in this file declared.
func (r *fileCheck) declared(d *ast.ClassDecl) *program.Class {
	for _, c := range r.prog.Classes() {
		if c.Decl == d {
			return c
		}
	}
	return nil
}

// enclosingClass is the class declared around the position.
func (r *fileCheck) enclosingClass(path []ast.Node, off int) *program.Class {
	for i := len(path) - 1; i >= 0; i-- {
		if d, ok := path[i].(*ast.ClassDecl); ok {
			return r.declared(d)
		}
	}
	return nil
}

// firstAssignment is where a variable first appears in the function around
// it: a parameter, or the first time the body names it.
func (r *fileCheck) firstAssignment(path []ast.Node, name string) *location {
	var params []*ast.Param
	var body ast.Node
	for i := len(path) - 1; i >= 0; i-- {
		switch n := path[i].(type) {
		case *ast.MethodDecl:
			params, body = n.Params, n
		case *ast.FunctionDecl:
			params, body = n.Params, n
		case *ast.Closure:
			params, body = n.Params, n
		case *ast.ArrowFunc:
			params, body = n.Params, n
		default:
			continue
		}
		break
	}
	for _, p := range params {
		if p.Var != nil && p.Var.Name == name {
			return &location{r.file, p.Var.S}
		}
	}
	if body == nil {
		body = r.file.AST
	}
	var first *ast.Variable
	ast.Inspect(body, func(n ast.Node) bool {
		if first != nil {
			return false
		}
		if v, ok := n.(*ast.Variable); ok && v.Name == name {
			first = v
		}
		return true
	})
	if first == nil {
		return nil
	}
	return &location{r.file, first.S}
}

// ------------------------------------------------------------------ hover

// parsed is the check of a document's newest version, or, when that one
// does not parse, of the newest that did: an unfinished line elsewhere
// must not take hover and go to definition away. Positions are taken as
// they are; an edit before them shifts them until the text parses again.
func (s *Server) parsed(path string) (*fileCheck, error) {
	r, err := s.current(path)
	if err != nil || r == nil || r.file != nil {
		return r, err
	}
	if good := s.lastParsed(path); good != nil {
		return good, nil
	}
	return r, nil
}

// lastParsed is the newest check of a document whose text parsed.
func (s *Server) lastParsed(path string) *fileCheck {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.docs[path]; d != nil {
		return d.good
	}
	return nil
}

func (s *Server) hover(p TextDocumentPositionParams) (any, error) {
	path, ok := s.path(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	r, err := s.parsed(path)
	if err != nil || r == nil {
		return nil, err
	}
	sym := r.symbolAt(r.text.offset(p.Position))
	if sym == nil {
		return nil, nil
	}
	code, doc := describe(sym)
	if code == "" {
		return nil, nil
	}
	value := "```phaenon\n" + code + "\n```"
	if doc != "" {
		value += "\n\n" + doc
	}
	rng := r.text.rangeOf(sym.span.Start, sym.span.End)
	return Hover{Contents: MarkupContent{Kind: "markdown", Value: value}, Range: &rng}, nil
}

// describe is a symbol's declaration in Phaenon's spelling, and its
// docblock's summary.
func describe(sym *symbol) (code, doc string) {
	switch {
	case sym.method != nil:
		m := sym.method
		code = signature(methodHead(m), m.Class.Name+"::"+m.Name, m.TypeParams, m.Params, m.Return)
		doc = summary(m.Doc)
	case sym.fn != nil:
		fn := sym.fn.Signature()
		code = signature("function", fn.Name, fn.TypeParams, fn.Params, fn.Return)
		doc = summary(fn.Doc)
	case sym.prop != nil:
		pr := sym.prop
		t := pr.Type
		if sym.typ != nil {
			t = sym.typ
		}
		code = pr.Class.Name + "::$" + pr.Name + ": " + types.PrintPhaenon(t)
		doc = summary(pr.Doc)
	case sym.enumCase != nil:
		code = "case " + sym.enumCase.Class.Name + "::" + sym.enumCase.Name
	case sym.constant != nil:
		k := sym.constant
		name := k.Name
		if k.Class != nil {
			name = k.Class.Name + "::" + k.Name
		}
		code = "const " + name
		if t := sym.typ; t != nil {
			code += ": " + types.PrintPhaenon(t)
		} else if k.Type != nil {
			code += ": " + types.PrintPhaenon(k.Type)
		}
	case sym.class != nil:
		c := sym.class.Signature()
		code = classHead(c)
		doc = summary(c.Doc)
	case sym.variable != "":
		code = "$" + sym.variable
		if sym.typ != nil {
			code += ": " + types.PrintPhaenon(sym.typ)
		}
	case sym.typ != nil:
		code = types.PrintPhaenon(sym.typ)
	}
	return code, doc
}

// summary is a docblock's summary line, or "".
func summary(d *docblock.Doc) string {
	if d == nil {
		return ""
	}
	return d.Summary
}

// methodHead is a method's modifiers as its declaration writes them, with
// no keyword (roadmap D27): "" for a public instance method.
func methodHead(m *program.Method) string {
	var words []string
	switch m.Vis {
	case program.Protected:
		words = append(words, "protected")
	case program.Private:
		words = append(words, "private")
	}
	if m.Static {
		words = append(words, "static")
	}
	return strings.Join(words, " ")
}

// signature writes a function's head, `function name<T>(int $a, ?T $b =
// …): R`, or a method's, `static name<T>(…): R`.
func signature(head, name string, tps []*program.TypeParam, params []*program.Param, ret types.Type) string {
	label, _ := signatureLabel(head, name, tps, params, ret)
	return label
}

// signatureLabel is signature with each parameter's byte offsets in it.
func signatureLabel(head, name string, tps []*program.TypeParam, params []*program.Param, ret types.Type) (string, [][2]int) {
	var b strings.Builder
	if head != "" {
		b.WriteString(head + " ")
	}
	b.WriteString(name)
	if len(tps) > 0 {
		b.WriteString("<")
		for i, tp := range tps {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(tp.Name)
			if tp.Bound != nil {
				b.WriteString(" of " + types.PrintPhaenon(tp.Bound))
			}
		}
		b.WriteString(">")
	}
	b.WriteString("(")
	var offs [][2]int
	for i, p := range params {
		if i > 0 {
			b.WriteString(", ")
		}
		start := b.Len()
		if p.Type != nil && !types.IsMixed(p.Type) || p.NativeType != nil {
			b.WriteString(types.PrintPhaenon(p.Type) + " ")
		}
		if p.ByRef {
			b.WriteString("&")
		}
		if p.Variadic {
			b.WriteString("...")
		}
		b.WriteString("$" + p.Name)
		if p.HasDefault {
			b.WriteString(" = …")
		}
		offs = append(offs, [2]int{start, b.Len()})
	}
	b.WriteString(")")
	if ret != nil {
		b.WriteString(": " + types.PrintPhaenon(ret))
	}
	return b.String(), offs
}

func classHead(c *program.Class) string {
	var b strings.Builder
	if c.Final {
		b.WriteString("final ")
	}
	if c.Abstract && c.Kind == ast.KindClass {
		b.WriteString("abstract ")
	}
	if c.Readonly {
		b.WriteString("readonly ")
	}
	switch c.Kind {
	case ast.KindInterface:
		b.WriteString("interface ")
	case ast.KindTrait:
		b.WriteString("trait ")
	case ast.KindEnum:
		b.WriteString("enum ")
	default:
		b.WriteString("class ")
	}
	b.WriteString(c.Name)
	if len(c.TypeParams) > 0 {
		b.WriteString("<")
		for i, tp := range c.TypeParams {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(tp.Name)
			if tp.Bound != nil {
				b.WriteString(" of " + types.PrintPhaenon(tp.Bound))
			}
		}
		b.WriteString(">")
	}
	if c.Parent != "" {
		b.WriteString(" extends " + c.Parent)
	}
	if len(c.Interfaces) > 0 && c.Kind != ast.KindInterface {
		b.WriteString(" implements " + strings.Join(c.Interfaces, ", "))
	}
	return b.String()
}

// ------------------------------------------------------------------ go to definition

func (s *Server) definition(p TextDocumentPositionParams) (any, error) {
	path, ok := s.path(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	r, err := s.parsed(path)
	if err != nil || r == nil {
		return nil, err
	}
	sym := r.symbolAt(r.text.offset(p.Position))
	if sym == nil {
		return nil, nil
	}
	loc := declaration(sym)
	if loc == nil || loc.file == nil {
		return nil, nil
	}
	return s.location(loc)
}

// declaration is where a symbol is declared.
func declaration(sym *symbol) *location {
	switch {
	case sym.method != nil:
		m := sym.method
		f := m.Class.File
		if m.FromTrait != nil {
			f = m.FromTrait.File
		}
		if m.Decl != nil && m.Decl.Name != nil {
			return &location{f, m.Decl.Name.S}
		}
		return &location{f, m.Span}
	case sym.fn != nil:
		if sym.fn.Decl != nil && sym.fn.Decl.Name != nil {
			return &location{sym.fn.File, sym.fn.Decl.Name.S}
		}
	case sym.prop != nil:
		f := sym.prop.File
		if f == nil && sym.prop.Class != nil {
			f = sym.prop.Class.File
		}
		return &location{f, sym.prop.Span}
	case sym.enumCase != nil:
		return &location{sym.enumCase.Class.File, sym.enumCase.Span}
	case sym.constant != nil:
		f := sym.constant.File
		if f == nil && sym.constant.Class != nil {
			f = sym.constant.Class.File
		}
		return &location{f, sym.constant.Span}
	case sym.class != nil:
		if sym.class.Decl != nil && sym.class.Decl.Name != nil {
			return &location{sym.class.File, sym.class.Decl.Name.S}
		}
		return &location{sym.class.File, sym.class.Span}
	case sym.decl != nil:
		return sym.decl
	}
	return nil
}

// location converts a span in a program's file to LSP's. The embedded
// stubs have no file of their own: they are written under the user's
// cache directory the first time a definition leads into one.
func (s *Server) location(loc *location) (*Location, error) {
	f := loc.file
	t := newText(f.Src)
	uri := ""
	switch {
	case strings.HasPrefix(f.Path, "<"):
		path, err := stubFile(f)
		if err != nil {
			return nil, err
		}
		uri = uriOf(path)
	default:
		uri = s.uri(f.Path)
	}
	return &Location{URI: uri, Range: t.rangeOf(loc.span.Start, loc.span.End)}, nil
}

// stubFile writes an embedded file (`<stub>/…`, `<ext>/…`) where an editor
// can open it, and returns its path.
func stubFile(f *program.File) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	name := strings.NewReplacer("<", "", ">", "").Replace(f.Path)
	path := filepath.Join(dir, "phaenon", "lsp", filepath.FromSlash(name))
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, f.Src) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	// Read-only, as a reminder that editing it changes nothing; a newer phn
	// replaces it.
	os.Remove(path)
	return path, os.WriteFile(path, f.Src, 0o444)
}

// ------------------------------------------------------------------ document symbols

func (s *Server) documentSymbols(uri string) (any, error) {
	path, ok := s.path(uri)
	if !ok {
		return nil, nil
	}
	r, err := s.current(path)
	if err != nil || r == nil {
		return nil, err
	}
	good := r
	if r.file == nil {
		s.mu.Lock()
		if d := s.docs[path]; d != nil && d.good != nil {
			good = d.good
		}
		s.mu.Unlock()
	}
	if good.file == nil {
		return []DocumentSymbol{}, nil
	}
	return symbolsOf(good.text, good.file.AST.Stmts), nil
}

func symbolsOf(t *text, stmts []ast.Stmt) []DocumentSymbol {
	out := []DocumentSymbol{}
	rng := func(s ast.Span) Range { return t.rangeOf(s.Start, s.End) }
	for _, st := range stmts {
		switch n := st.(type) {
		case *ast.Namespace:
			children := symbolsOf(t, n.Body)
			if n.Name == nil {
				out = append(out, children...)
				continue
			}
			if !n.Braced {
				// `namespace A;` holds the statements after it, which the
				// tree keeps as its siblings.
				out = append(out, DocumentSymbol{Name: strings.Join(n.Name.Parts, `\`), Kind: symbolNamespace, Range: rng(n.S), SelectionRange: rng(n.Name.S)})
				continue
			}
			out = append(out, DocumentSymbol{Name: strings.Join(n.Name.Parts, `\`), Kind: symbolNamespace, Range: rng(n.S), SelectionRange: rng(n.Name.S), Children: children})
		case *ast.ClassDecl:
			if n.Name == nil {
				continue
			}
			kind := symbolClass
			switch n.Kind {
			case ast.KindInterface:
				kind = symbolInterface
			case ast.KindEnum:
				kind = symbolEnum
			case ast.KindTrait:
				kind = symbolStruct
			}
			out = append(out, DocumentSymbol{Name: n.Name.Name, Kind: kind, Range: rng(n.S), SelectionRange: rng(n.Name.S), Children: memberSymbols(t, n.Members)})
		case *ast.FunctionDecl:
			out = append(out, DocumentSymbol{Name: n.Name.Name, Kind: symbolFunction, Range: rng(n.S), SelectionRange: rng(n.Name.S)})
		}
	}
	return out
}

func memberSymbols(t *text, members []ast.ClassMember) []DocumentSymbol {
	out := []DocumentSymbol{}
	rng := func(s ast.Span) Range { return t.rangeOf(s.Start, s.End) }
	for _, m := range members {
		switch n := m.(type) {
		case *ast.MethodDecl:
			kind := symbolMethod
			if strings.EqualFold(n.Name.Name, "__construct") {
				kind = symbolConstructor
			}
			out = append(out, DocumentSymbol{Name: n.Name.Name, Kind: kind, Range: rng(n.S), SelectionRange: rng(n.Name.S)})
			for _, p := range n.Params {
				if p.Modifiers != 0 && p.Var != nil { // a promoted property
					out = append(out, DocumentSymbol{Name: "$" + p.Var.Name, Kind: symbolProperty, Range: rng(p.S), SelectionRange: rng(p.Var.S)})
				}
			}
		case *ast.PropertyDecl:
			for _, it := range n.Props {
				out = append(out, DocumentSymbol{Name: "$" + it.Var.Name, Kind: symbolProperty, Range: rng(n.S), SelectionRange: rng(it.Var.S)})
			}
		case *ast.ClassConstDecl:
			for _, it := range n.Consts {
				out = append(out, DocumentSymbol{Name: it.Name.Name, Kind: symbolConstant, Range: rng(n.S), SelectionRange: rng(it.Name.S)})
			}
		case *ast.EnumCase:
			out = append(out, DocumentSymbol{Name: n.Name.Name, Kind: symbolEnumMember, Range: rng(n.S), SelectionRange: rng(n.Name.S)})
		}
	}
	return out
}

// ------------------------------------------------------------------ formatting and quick-fixes

// formatting is phn fmt's first layer (design/tooling.md §5.3): the
// project's fixer costs a PHP process, and runs on an explicit command.
// When the check of the text in the buffer is done, its program gives the
// rewrites of the withdrawn type tests that depend on types (§5.6).
func (s *Server) formatting(uri string) (any, error) {
	path, ok := s.path(uri)
	if !ok {
		return nil, nil
	}
	s.mu.Lock()
	d := s.docs[path]
	var src []byte
	var checked *fileCheck
	if d != nil {
		src = d.text
		if d.result != nil && d.result.version == d.version && d.result.file != nil {
			checked = d.result
		}
	}
	s.mu.Unlock()
	if d == nil {
		return nil, nil
	}
	var opts format.Options
	if checked != nil {
		s.query.Lock()
		opts = format.Options{Tests: map[ast.Span]string{}, Program: true}
		for n, rec := range checked.file.Tests {
			if rec.Rewritten != "" {
				opts.Tests[n.Span()] = rec.Rewritten
			}
		}
		s.query.Unlock()
	}
	out, _, err := format.SourceWith(src, opts)
	if err != nil || bytes.Equal(out, src) {
		return []TextEdit{}, nil // a text that does not parse is left as it is
	}
	return []TextEdit{{Range: newText(src).whole(), NewText: string(out)}}, nil
}

// codeActions offers the fixes of the diagnostics in the range: spaces for
// the tabs of P1005.
func (s *Server) codeActions(p CodeActionParams) (any, error) {
	out := []CodeAction{}
	path, ok := s.path(p.TextDocument.URI)
	if !ok {
		return out, nil
	}
	s.mu.Lock()
	d := s.docs[path]
	var src []byte
	if d != nil {
		src = d.text
	}
	s.mu.Unlock()
	for _, diag := range p.Context.Diagnostics {
		if diag.Code != "P1005" || src == nil {
			continue
		}
		fixed := tabs.Expand(src)
		if bytes.Equal(fixed, src) {
			continue
		}
		out = append(out, CodeAction{
			Title: "Replace the tabs with spaces", Kind: "quickfix", IsPreferred: true,
			Diagnostics: []Diagnostic{diag},
			Edit: &WorkspaceEdit{Changes: map[string][]TextEdit{
				p.TextDocument.URI: {{Range: newText(src).whole(), NewText: string(fixed)}},
			}},
		})
	}
	return out, nil
}
