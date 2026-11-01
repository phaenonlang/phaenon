package printer

import (
	"fmt"

	"github.com/phaenonlang/phaenon/internal/ast"
)

func (p *Printer) stmts(list []ast.Stmt) {
	for i, s := range list {
		if i > 0 && wantsBlankLineBetween(list[i-1], s) {
			p.blank()
		}
		_, p.nextIsCloseTag = nextStmt(list, i).(*ast.CloseTag)
		p.stmt(s)
	}
	p.nextIsCloseTag = false
}

func nextStmt(list []ast.Stmt, i int) ast.Stmt {
	if i+1 < len(list) {
		return list[i+1]
	}
	return nil
}

// wantsBlankLineBetween separates declarations from their neighbours, and
// the `use` block from what follows, in the PSR-12 spirit.
func wantsBlankLineBetween(prev, cur ast.Stmt) bool {
	isDecl := func(s ast.Stmt) bool {
		switch s.(type) {
		case *ast.ClassDecl, *ast.FunctionDecl, *ast.Namespace, *ast.TypeAlias:
			return true
		}
		return false
	}
	switch prev.(type) {
	case *ast.OpenTag, *ast.InlineHTML, *ast.Label:
		return false
	}
	switch cur.(type) {
	case *ast.CloseTag, *ast.InlineHTML:
		return false
	}
	if isDecl(prev) || isDecl(cur) {
		return true
	}
	if _, wasUse := prev.(*ast.Use); wasUse {
		if _, stillUse := cur.(*ast.Use); !stillUse {
			return true
		}
	}
	if _, wasDeclare := prev.(*ast.Declare); wasDeclare {
		return true
	}
	return false
}

// block prints `{ … }` with the body indented; the caller positions the
// opening brace (typically after a space) and continues after the closing one.
func (p *Printer) block(body []ast.Stmt) {
	p.w("{")
	p.nl()
	p.depth++
	p.stmts(body)
	p.depth--
	p.w("}")
}

// body prints a statement used as the body of a control structure. A Block
// is printed inline as ` { … }`; anything else goes on its own indented line.
// It reports whether the body was a block, so the caller knows whether the
// next keyword (`else`, `while`) continues the line or starts a new one.
func (p *Printer) body(s ast.Stmt) bool {
	if b, ok := s.(*ast.Block); ok {
		p.w(" ")
		p.block(b.Stmts)
		return true
	}
	p.nl()
	p.depth++
	p.stmt(s)
	p.depth--
	return false
}

// altBody prints the statements of an alternative-syntax body (`if (…): … endif;`).
func (p *Printer) altBody(s ast.Stmt) {
	p.nl()
	p.depth++
	if b, ok := s.(*ast.Block); ok {
		p.stmts(b.Stmts)
	} else if s != nil {
		p.stmt(s)
	}
	p.depth--
}

// continueAfterBody positions the cursor for a keyword that follows a body.
func (p *Printer) continueAfterBody(wasBlock bool) {
	if wasBlock {
		p.w(" ")
	} else {
		p.nl()
	}
}

func (p *Printer) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.OpenTag:
		p.raw(s.Raw)
		p.nl()
	case *ast.CloseTag:
		p.raw(s.Raw)
	case *ast.InlineHTML:
		p.raw(s.Text)
	case *ast.ExprStmt:
		p.expr(s.Expr)
		p.w(";")
		p.nl()
	case *ast.VoidCastStmt:
		p.w("(void) ")
		p.expr(s.Expr)
		p.w(";")
		p.nl()
	case *ast.Echo:
		if s.Short {
			p.w("<?= ")
			p.exprs(s.Exprs)
			if p.nextIsCloseTag {
				p.w(" ")
				return
			}
			p.w(";")
			p.nl()
			return
		}
		p.w("echo ")
		p.exprs(s.Exprs)
		p.w(";")
		p.nl()
	case *ast.Block:
		p.block(s.Stmts)
		p.nl()
	case *ast.Nop:
		p.w(";")
		p.nl()
	case *ast.If:
		p.ifStmt(s)
	case *ast.While:
		p.w("while (")
		p.expr(s.Cond)
		p.w(")")
		if s.Alt {
			p.w(":")
			p.altBody(s.Body)
			p.w("endwhile;")
			p.nl()
			return
		}
		p.body(s.Body)
		p.nl()
	case *ast.DoWhile:
		p.w("do")
		wasBlock := p.body(s.Body)
		p.continueAfterBody(wasBlock)
		p.w("while (")
		p.expr(s.Cond)
		p.w(");")
		p.nl()
	case *ast.For:
		p.w("for (")
		p.exprs(s.Init)
		p.w("; ")
		p.exprs(s.Cond)
		p.w("; ")
		p.exprs(s.Step)
		p.w(")")
		if s.Alt {
			p.w(":")
			p.altBody(s.Body)
			p.w("endfor;")
			p.nl()
			return
		}
		p.body(s.Body)
		p.nl()
	case *ast.Foreach:
		p.w("foreach (")
		p.expr(s.Expr)
		p.w(" as ")
		if s.Key != nil {
			if s.KeyByRef {
				p.w("&")
			}
			p.expr(s.Key)
			p.w(" => ")
		}
		if s.ByRef {
			p.w("&")
		}
		p.expr(s.Value)
		p.w(")")
		if s.Alt {
			p.w(":")
			p.altBody(s.Body)
			p.w("endforeach;")
			p.nl()
			return
		}
		p.body(s.Body)
		p.nl()
	case *ast.Switch:
		p.w("switch (")
		p.expr(s.Subject)
		p.w(")")
		if s.Alt {
			p.w(":")
		} else {
			p.w(" {")
		}
		p.nl()
		p.depth++
		for _, c := range s.Cases {
			if c.Cond == nil {
				p.w("default:")
			} else {
				p.w("case ")
				p.expr(c.Cond)
				p.w(":")
			}
			p.nl()
			p.depth++
			p.stmts(c.Body)
			p.depth--
		}
		p.depth--
		if s.Alt {
			p.w("endswitch;")
		} else {
			p.w("}")
		}
		p.nl()
	case *ast.Break:
		p.w("break")
		if s.Levels != nil {
			p.w(" ")
			p.expr(s.Levels)
		}
		p.w(";")
		p.nl()
	case *ast.Continue:
		p.w("continue")
		if s.Levels != nil {
			p.w(" ")
			p.expr(s.Levels)
		}
		p.w(";")
		p.nl()
	case *ast.Return:
		p.w("return")
		if s.Expr != nil {
			p.w(" ")
			p.expr(s.Expr)
		}
		p.w(";")
		p.nl()
	case *ast.Global:
		p.w("global ")
		p.exprs(s.Vars)
		p.w(";")
		p.nl()
	case *ast.StaticVars:
		p.w("static ")
		for i, v := range s.Vars {
			if i > 0 {
				p.w(", ")
			}
			p.expr(v.Var)
			if v.Default != nil {
				p.w(" = ")
				p.expr(v.Default)
			}
		}
		p.w(";")
		p.nl()
	case *ast.Unset:
		p.w("unset(")
		p.exprs(s.Vars)
		p.w(");")
		p.nl()
	case *ast.Try:
		p.w("try ")
		p.block(s.Body)
		for _, c := range s.Catches {
			p.w(" catch (")
			for i, t := range c.Types {
				if i > 0 {
					p.w("|")
				}
				p.name(t)
			}
			if c.Var != nil {
				p.w(" ")
				p.expr(c.Var)
			}
			p.w(") ")
			p.block(c.Body)
		}
		if s.HasFinally {
			p.w(" finally ")
			p.block(s.Finally)
		}
		p.nl()
	case *ast.Goto:
		p.w("goto ")
		p.ident(s.Label)
		p.w(";")
		p.nl()
	case *ast.Label:
		p.ident(s.Name)
		p.w(":")
		p.nl()
	case *ast.Declare:
		p.w("declare(")
		for i, d := range s.Directives {
			if i > 0 {
				p.w(", ")
			}
			p.ident(d.Name)
			p.w("=")
			p.expr(d.Value)
		}
		p.w(")")
		switch {
		case s.Alt:
			p.w(":")
			p.altBody(s.Body)
			p.w("enddeclare;")
			p.nl()
		case s.Body != nil:
			p.body(s.Body)
			p.nl()
		default:
			p.w(";")
			p.nl()
		}
	case *ast.HaltCompiler:
		p.w("__halt_compiler();")
		p.raw(s.Rest)
	case *ast.Namespace:
		p.w("namespace")
		if s.Name != nil {
			p.w(" ")
			p.name(s.Name)
		}
		if s.Braced {
			p.w(" ")
			p.block(s.Body)
		} else {
			p.w(";")
		}
		p.nl()
	case *ast.Use:
		p.useStmt(s)
	case *ast.ConstDecl:
		p.attrsLines(s.Attrs)
		p.w("const ")
		p.constItems(s.Consts)
		p.w(";")
		p.nl()
	case *ast.TypeAlias:
		p.doc(s.Doc)
		p.attrsLines(s.Attrs)
		p.w("type ")
		p.ident(s.Name)
		p.typeParams(s.TypeParams)
		p.w(" = ")
		p.typ(s.Type)
		p.w(";")
		p.nl()
	case *ast.FunctionDecl:
		p.doc(s.Doc)
		p.attrsLines(s.Attrs)
		p.w(keyword(s.Fun))
		if s.ByRef {
			p.w("&")
		}
		p.ident(s.Name)
		p.typeParams(s.TypeParams)
		p.params(s.Params)
		p.returnType(s.ReturnType)
		p.nl()
		p.block(s.Body)
		p.nl()
	case *ast.ClassDecl:
		p.classDecl(s)
	default:
		panic(fmt.Sprintf("printer: unknown statement node %T", s))
	}
}

func (p *Printer) ifStmt(s *ast.If) {
	p.w("if (")
	p.expr(s.Cond)
	p.w(")")
	if s.Alt {
		p.w(":")
		p.altBody(s.Then)
		for _, ei := range s.ElseIfs {
			p.w("elseif (")
			p.expr(ei.Cond)
			p.w("):")
			p.altBody(ei.Body)
		}
		if s.Else != nil {
			p.w("else:")
			p.altBody(s.Else)
		}
		p.w("endif;")
		p.nl()
		return
	}
	wasBlock := p.body(s.Then)
	for _, ei := range s.ElseIfs {
		p.continueAfterBody(wasBlock)
		p.w("elseif (")
		p.expr(ei.Cond)
		p.w(")")
		wasBlock = p.body(ei.Body)
	}
	if s.Else != nil {
		p.continueAfterBody(wasBlock)
		p.w("else")
		if _, ok := s.Else.(*ast.If); ok {
			// `else if` keeps nesting flat.
			p.w(" ")
			p.stmt(s.Else)
			return
		}
		p.body(s.Else)
	}
	p.nl()
}

func (p *Printer) useStmt(s *ast.Use) {
	p.w("use ")
	p.useKind(s.Kind)
	if s.Prefix != nil {
		p.name(s.Prefix)
		p.w("\\{")
		for i, it := range s.Items {
			if i > 0 {
				p.w(", ")
			}
			if it.Kind != ast.UseClass {
				p.useKind(it.Kind)
			}
			p.useItem(it)
		}
		p.w("};")
		p.nl()
		return
	}
	for i, it := range s.Items {
		if i > 0 {
			p.w(", ")
		}
		p.useItem(it)
	}
	p.w(";")
	p.nl()
}

func (p *Printer) useKind(k ast.UseKind) {
	switch k {
	case ast.UseFunction:
		p.w("function ")
	case ast.UseConst:
		p.w("const ")
	}
}

func (p *Printer) useItem(it *ast.UseItem) {
	p.name(it.Name)
	if it.Alias != nil {
		p.w(" as ")
		p.ident(it.Alias)
	}
}

func (p *Printer) constItems(items []*ast.ConstItem) {
	for i, c := range items {
		if i > 0 {
			p.w(", ")
		}
		p.ident(c.Name)
		p.w(" = ")
		p.expr(c.Value)
	}
}

// ------------------------------------------------------------------ classes

func (p *Printer) classDecl(c *ast.ClassDecl) {
	p.doc(c.Doc)
	p.attrsLines(c.Attrs)
	p.modifiers(c.Modifiers)
	p.classKeyword(c.Kind)
	p.w(" ")
	p.ident(c.Name)
	p.typeParams(c.TypeParams)
	p.classHeader(c)
	p.nl()
	p.classBody(c.Members)
	p.nl()
}

// anonClass prints `[#[…]] [modifiers] class(args) extends … { … }` after `new `.
func (p *Printer) anonClass(c *ast.ClassDecl) {
	p.attrsInline(c.Attrs)
	p.modifiers(c.Modifiers)
	p.w("class")
	if c.Args != nil {
		p.argList(c.Args)
	}
	p.classHeader(c)
	p.w(" ")
	p.classBody(c.Members)
}

func (p *Printer) classKeyword(k ast.ClassKind) {
	switch k {
	case ast.KindClass:
		p.w("class")
	case ast.KindInterface:
		p.w("interface")
	case ast.KindTrait:
		p.w("trait")
	case ast.KindEnum:
		p.w("enum")
	}
}

func (p *Printer) classHeader(c *ast.ClassDecl) {
	if c.BackingType != nil {
		p.w(": ")
		p.typ(c.BackingType)
	}
	if len(c.Extends) > 0 {
		p.w(" extends ")
		p.namedTypes(c.Extends)
	}
	if len(c.Implements) > 0 {
		p.w(" implements ")
		p.namedTypes(c.Implements)
	}
}

func (p *Printer) classBody(members []ast.ClassMember) {
	p.w("{")
	p.nl()
	p.depth++
	for i, m := range members {
		if i > 0 && (isMethodLike(m) || isMethodLike(members[i-1])) {
			p.blank()
		}
		p.member(m)
	}
	p.depth--
	p.w("}")
}

func isMethodLike(m ast.ClassMember) bool {
	switch m := m.(type) {
	case *ast.MethodDecl:
		return true
	case *ast.PropertyDecl:
		return m.Hooks != nil
	}
	return false
}

func (p *Printer) member(m ast.ClassMember) {
	switch m := m.(type) {
	case *ast.ClassConstDecl:
		p.doc(m.Doc)
		p.attrsLines(m.Attrs)
		p.modifiers(m.Modifiers)
		p.w("const ")
		if m.Type != nil {
			p.typ(m.Type)
			p.w(" ")
		}
		p.constItems(m.Consts)
		p.w(";")
		p.nl()
	case *ast.PropertyDecl:
		p.doc(m.Doc)
		p.attrsLines(m.Attrs)
		p.modifiers(m.Modifiers)
		if m.Type != nil {
			p.typ(m.Type)
			p.w(" ")
		}
		for i, it := range m.Props {
			if i > 0 {
				p.w(", ")
			}
			p.expr(it.Var)
			if it.Default != nil {
				p.w(" = ")
				p.expr(it.Default)
			}
		}
		if m.Hooks != nil {
			p.w(" ")
			p.hooks(m.Hooks)
		} else {
			p.w(";")
		}
		p.nl()
	case *ast.MethodDecl:
		p.doc(m.Doc)
		p.attrsLines(m.Attrs)
		p.modifiers(m.Modifiers)
		p.w(keyword(m.Fun))
		if m.ByRef {
			p.w("&")
		}
		p.ident(m.Name)
		p.typeParams(m.TypeParams)
		p.params(m.Params)
		p.returnType(m.ReturnType)
		if m.HasBody {
			p.nl()
			p.block(m.Body)
		} else {
			p.w(";")
		}
		p.nl()
	case *ast.EnumCase:
		p.doc(m.Doc)
		p.attrsLines(m.Attrs)
		p.w("case ")
		p.ident(m.Name)
		if m.Value != nil {
			p.w(" = ")
			p.expr(m.Value)
		}
		p.w(";")
		p.nl()
	case *ast.TraitUse:
		p.w("use ")
		p.names(m.Names)
		if !m.Braced {
			p.w(";")
			p.nl()
			return
		}
		p.w(" {")
		p.nl()
		p.depth++
		for _, a := range m.Adaptations {
			if a.Trait != nil {
				p.name(a.Trait)
				p.w("::")
			}
			p.ident(a.Method)
			if a.InsteadOf != nil {
				p.w(" insteadof ")
				p.names(a.InsteadOf)
			} else {
				p.w(" as ")
				p.modifiers(a.Modifiers)
				if a.Alias != nil {
					p.ident(a.Alias)
				}
			}
			p.w(";")
			p.nl()
		}
		p.depth--
		p.w("}")
		p.nl()
	default:
		panic(fmt.Sprintf("printer: unknown member node %T", m))
	}
}

// keyword is the spelling of a named declaration: `fun` in Phaenon, else `function`.
func keyword(fun bool) string {
	if fun {
		return "fun "
	}
	return "function "
}
