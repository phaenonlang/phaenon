package parser

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/token"
)

// statementList parses statements until `until` (EOF, '}' or an
// alternative-syntax end keyword), leaving the terminator in place.
func (p *Parser) statementList(until ...token.Kind) []ast.Stmt {
	var stmts []ast.Stmt
	for {
		k := p.kind()
		if k == token.EOF {
			return stmts
		}
		for _, u := range until {
			if k == u {
				return stmts
			}
		}
		stmts = append(stmts, p.statement())
	}
}

// block parses { statements }.
func (p *Parser) block() []ast.Stmt {
	p.expect(token.Kind('{'))
	stmts := p.statementList(token.Kind('}'))
	p.expect(token.Kind('}'))
	if stmts == nil {
		stmts = []ast.Stmt{}
	}
	return stmts
}

// statement parses one statement, including declarations.
func (p *Parser) statement() ast.Stmt {
	start := p.tok().Start
	doc := p.docComment // for the statement kinds that carry a `@var`
	switch p.kind() {
	case token.T_OPEN_TAG, token.T_OPEN_TAG_WITH_ECHO:
		t := p.next()
		if t.Kind == token.T_OPEN_TAG_WITH_ECHO {
			e := &ast.Echo{Short: true}
			e.Exprs = p.exprList()
			p.semi()
			e.S = p.span(start)
			return e
		}
		if p.mode == lexer.ModePhaenon {
			p.i-- // report at the tag
			p.errorf("Phaenon files do not start with <?php — remove the tag")
		}
		return &ast.OpenTag{S: p.span(start), Raw: t.Text(p.src)}
	case token.T_CLOSE_TAG:
		t := p.next()
		return &ast.CloseTag{S: p.span(start), Raw: t.Text(p.src)}
	case token.T_INLINE_HTML:
		t := p.next()
		return &ast.InlineHTML{S: p.span(start), Text: t.Text(p.src)}
	case token.Kind('{'):
		b := &ast.Block{Stmts: p.block()}
		b.S = p.span(start)
		return b
	case token.Kind(';'):
		p.next()
		return &ast.Nop{S: p.span(start)}
	case token.T_IF:
		s := p.ifStmt()
		if i, ok := s.(*ast.If); ok {
			i.Doc = doc
		}
		return s
	case token.T_WHILE:
		p.next()
		w := &ast.While{Doc: doc, Cond: p.parenExpr()}
		w.Body, w.Alt = p.loopBody(token.T_ENDWHILE)
		w.S = p.span(start)
		return w
	case token.T_DO:
		p.next()
		d := &ast.DoWhile{Doc: doc, Body: p.statement()}
		p.expect(token.T_WHILE)
		d.Cond = p.parenExpr()
		p.semi()
		d.S = p.span(start)
		return d
	case token.T_FOR:
		p.next()
		p.expect(token.Kind('('))
		f := &ast.For{Doc: doc}
		f.Init = p.forExprs(token.Kind(';'))
		p.expect(token.Kind(';'))
		f.Cond = p.forExprs(token.Kind(';'))
		p.expect(token.Kind(';'))
		f.Step = p.forExprs(token.Kind(')'))
		p.expect(token.Kind(')'))
		f.Body, f.Alt = p.loopBody(token.T_ENDFOR)
		f.S = p.span(start)
		return f
	case token.T_FOREACH:
		return p.foreachStmt()
	case token.T_SWITCH:
		s := p.switchStmt()
		if sw, ok := s.(*ast.Switch); ok {
			sw.Doc = doc
		}
		return s
	case token.T_BREAK, token.T_CONTINUE:
		k := p.next().Kind
		var levels ast.Expr
		if p.startsExpr() {
			levels = p.expr()
		}
		p.semi()
		if k == token.T_BREAK {
			return &ast.Break{S: p.span(start), Levels: levels}
		}
		return &ast.Continue{S: p.span(start), Levels: levels}
	case token.T_RETURN:
		p.next()
		r := &ast.Return{Doc: doc}
		if p.startsExpr() {
			r.Expr = p.expr()
		}
		p.semi()
		r.S = p.span(start)
		return r
	case token.T_GLOBAL:
		p.next()
		g := &ast.Global{}
		for {
			if p.at(token.T_VARIABLE) {
				g.Vars = append(g.Vars, p.variable())
			} else {
				g.Vars = append(g.Vars, p.simpleVariable())
			}
			if !p.accept(token.Kind(',')) {
				break
			}
		}
		p.semi()
		g.S = p.span(start)
		return g
	case token.T_STATIC:
		if p.peekKind(1) == token.T_VARIABLE {
			p.next()
			s := &ast.StaticVars{Doc: doc}
			for {
				vstart := p.tok().Start
				v := &ast.StaticVar{Var: p.variable()}
				if p.accept(token.Kind('=')) {
					v.Default = p.expr()
				}
				v.S = p.span(vstart)
				s.Vars = append(s.Vars, v)
				if !p.accept(token.Kind(',')) {
					break
				}
			}
			p.semi()
			s.S = p.span(start)
			return s
		}
	case token.T_ECHO:
		p.next()
		e := &ast.Echo{Doc: doc, Exprs: p.exprList()}
		p.semi()
		e.S = p.span(start)
		return e
	case token.T_UNSET:
		p.next()
		p.expect(token.Kind('('))
		u := &ast.Unset{Doc: doc}
		for !p.at(token.Kind(')')) {
			u.Vars = append(u.Vars, p.expr())
			if !p.accept(token.Kind(',')) {
				break
			}
		}
		p.expect(token.Kind(')'))
		p.semi()
		u.S = p.span(start)
		return u
	case token.T_DECLARE:
		p.next()
		p.expect(token.Kind('('))
		d := &ast.Declare{}
		for {
			d.Directives = append(d.Directives, p.constItem())
			if !p.accept(token.Kind(',')) {
				break
			}
		}
		p.expect(token.Kind(')'))
		if p.accept(token.Kind(':')) {
			d.Alt = true
			body := &ast.Block{Stmts: p.statementList(token.T_ENDDECLARE)}
			p.expect(token.T_ENDDECLARE)
			p.semi()
			body.S = p.span(start)
			d.Body = body
		} else if !p.at(token.Kind(';')) && !p.at(token.T_CLOSE_TAG) {
			d.Body = p.statement()
		} else {
			p.semi()
		}
		d.S = p.span(start)
		return d
	case token.T_TRY:
		s := p.tryStmt()
		if t, ok := s.(*ast.Try); ok {
			t.Doc = doc
		}
		return s
	case token.T_GOTO:
		p.next()
		g := &ast.Goto{Label: p.stringIdent()}
		p.semi()
		g.S = p.span(start)
		return g
	case token.T_VOID_CAST:
		p.next()
		v := &ast.VoidCastStmt{Expr: p.expr()}
		p.semi()
		v.S = p.span(start)
		return v
	case token.T_HALT_COMPILER:
		p.next()
		p.expect(token.Kind('('))
		p.expect(token.Kind(')'))
		h := &ast.HaltCompiler{}
		if p.at(token.T_INLINE_HTML) {
			// The lexer handed us `;` as part of the halted tail only when
			// absent; a normal `;` is a separate token.
		}
		if !p.at(token.T_INLINE_HTML) {
			p.semi()
		}
		if p.at(token.T_INLINE_HTML) {
			h.Rest = p.next().Text(p.src)
		}
		h.S = p.span(start)
		return h
	case token.T_NAMESPACE:
		if p.peekKind(1) != token.Kind('(') && p.peekKind(1) != token.T_PAAMAYIM_NEKUDOTAYIM {
			return p.namespaceStmt()
		}
	case token.T_USE:
		return p.useStmt()
	case token.T_CONST:
		p.next()
		c := &ast.ConstDecl{Doc: doc}
		for {
			c.Consts = append(c.Consts, p.constItem())
			if !p.accept(token.Kind(',')) {
				break
			}
		}
		p.semi()
		c.S = p.span(start)
		return c
	case token.T_FUNCTION:
		if p.isFunctionDecl() {
			return p.functionDecl(nil)
		}
	case token.T_ABSTRACT, token.T_FINAL, token.T_CLASS, token.T_INTERFACE, token.T_TRAIT, token.T_ENUM:
		if p.isClassDeclStart() {
			return p.classDecl(nil)
		}
	case token.T_READONLY:
		if p.peekKind(1) != token.Kind('(') {
			return p.classDecl(nil)
		}
	case token.T_ATTRIBUTE:
		attrs := p.attributes()
		if p.docComment == "" {
			p.docComment = doc // a declaration's docblock sits above its attributes
		}
		switch {
		case p.at(token.T_FUNCTION) && p.isFunctionDecl(), p.funAhead(false):
			return p.functionDecl(attrs)
		case p.at(token.T_CONST):
			p.next()
			c := &ast.ConstDecl{Doc: doc, Attrs: attrs}
			for {
				c.Consts = append(c.Consts, p.constItem())
				if !p.accept(token.Kind(',')) {
					break
				}
			}
			p.semi()
			c.S = p.span(start)
			return c
		case p.isClassDeclStart(), p.at(token.T_READONLY):
			return p.classDecl(attrs)
		case p.mode == lexer.ModePhaenon && p.at(token.T_TYPE) && p.atString1():
			return p.typeAlias(attrs)
		}
		// Attributes on a closure expression statement.
		p.i = p.indexOfOffset(start)
		p.docComment = ""
		e := p.expr()
		p.semi()
		return &ast.ExprStmt{S: p.span(start), Doc: doc, Expr: e}
	case token.T_TYPE:
		if p.mode == lexer.ModePhaenon && p.atString1() {
			return p.typeAlias(nil)
		}
	case token.T_STRING:
		if p.funAhead(false) {
			return p.functionDecl(nil)
		}
		// A block statement needs no semicolon after its brace, like any
		// block; a `concurrently(...)` call goes on to the expression
		// statement below.
		if p.concurrentlyAhead() {
			e := p.withdrawn()
			p.accept(token.Kind(';'))
			return &ast.ExprStmt{S: p.span(start), Doc: doc, Expr: e}
		}
	}
	// A label; Phaenon's contextual keywords are ordinary names here (`out:`).
	if p.atString() && p.peekKind(1) == token.Kind(':') {
		l := &ast.Label{Name: p.stringIdent()}
		p.expect(token.Kind(':'))
		l.S = p.span(start)
		return l
	}
	e := p.expr()
	p.semi()
	return &ast.ExprStmt{S: p.span(start), Doc: doc, Expr: e}
}

// atString1 reports whether the next token is a plain identifier (for `type Name =`).
func (p *Parser) atString1() bool {
	k := p.peekKind(1)
	return k == token.T_STRING || isPhaenonContextual(k)
}

// indexOfOffset returns the token index whose Start is offset (used to
// rewind after speculative attribute parsing).
func (p *Parser) indexOfOffset(offset int) int {
	for j := p.i; j >= 0; j-- {
		if p.toks[j].Start == offset && !p.toks[j].Kind.IsTrivia() {
			return j
		}
	}
	return p.i
}

// isFunctionDecl distinguishes `function name(` from a closure.
func (p *Parser) isFunctionDecl() bool {
	k := p.peekKind(1)
	if k == token.T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG || k == token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG {
		k = p.peekKind(2)
	}
	return k == token.T_STRING || k == token.T_READONLY || isPhaenonContextual(k)
}

func (p *Parser) isClassDeclStart() bool {
	j := 1
	k := p.kind()
	for isClassModifier(k) {
		k = p.peekKind(j)
		j++
	}
	switch k {
	case token.T_CLASS, token.T_INTERFACE, token.T_TRAIT, token.T_ENUM:
		return true
	}
	return false
}

func (p *Parser) parenExpr() ast.Expr {
	p.expect(token.Kind('('))
	e := p.expr()
	p.expect(token.Kind(')'))
	return e
}

func (p *Parser) exprList() []ast.Expr {
	var list []ast.Expr
	for {
		list = append(list, p.expr())
		if !p.accept(token.Kind(',')) {
			return list
		}
	}
}

// forExprs parses a comma list of expressions (possibly empty) up to `until`.
func (p *Parser) forExprs(until token.Kind) []ast.Expr {
	var list []ast.Expr
	for !p.at(until) {
		if p.at(token.T_VOID_CAST) {
			start := p.tok().Start
			p.next()
			x := p.expr()
			list = append(list, &ast.Cast{S: p.span(start), Op: token.T_VOID_CAST, Raw: "(void)", Expr: x})
		} else {
			list = append(list, p.expr())
		}
		if !p.accept(token.Kind(',')) {
			break
		}
	}
	return list
}

// loopBody parses a statement or the alternative `: … endX;` form.
func (p *Parser) loopBody(endKw token.Kind) (ast.Stmt, bool) {
	start := p.tok().Start
	if p.accept(token.Kind(':')) {
		b := &ast.Block{Stmts: p.statementList(endKw)}
		p.expect(endKw)
		p.semi()
		b.S = p.span(start)
		return b, true
	}
	return p.statement(), false
}

func (p *Parser) ifStmt() ast.Stmt {
	start := p.tok().Start
	p.expect(token.T_IF)
	s := &ast.If{Cond: p.parenExpr()}
	if p.accept(token.Kind(':')) {
		s.Alt = true
		s.Then = p.altBlock(start, token.T_ELSEIF, token.T_ELSE, token.T_ENDIF)
		for p.at(token.T_ELSEIF) {
			estart := p.tok().Start
			p.next()
			ei := &ast.ElseIf{Cond: p.parenExpr()}
			p.expect(token.Kind(':'))
			ei.Body = p.altBlock(estart, token.T_ELSEIF, token.T_ELSE, token.T_ENDIF)
			ei.S = p.span(estart)
			s.ElseIfs = append(s.ElseIfs, ei)
		}
		if p.at(token.T_ELSE) {
			estart := p.tok().Start
			p.next()
			p.expect(token.Kind(':'))
			s.Else = p.altBlock(estart, token.T_ENDIF)
		}
		p.expect(token.T_ENDIF)
		p.semi()
		s.S = p.span(start)
		return s
	}
	s.Then = p.statement()
	for p.at(token.T_ELSEIF) || (p.at(token.T_ELSE) && p.peekKind(1) == token.T_IF && false) {
		estart := p.tok().Start
		p.next()
		ei := &ast.ElseIf{Cond: p.parenExpr()}
		ei.Body = p.statement()
		ei.S = p.span(estart)
		s.ElseIfs = append(s.ElseIfs, ei)
	}
	if p.accept(token.T_ELSE) {
		s.Else = p.statement()
	}
	s.S = p.span(start)
	return s
}

func (p *Parser) altBlock(start int, until ...token.Kind) *ast.Block {
	b := &ast.Block{Stmts: p.statementList(until...)}
	b.S = p.span(start)
	return b
}

func (p *Parser) foreachStmt() ast.Stmt {
	start := p.tok().Start
	doc := p.docComment
	p.expect(token.T_FOREACH)
	p.expect(token.Kind('('))
	f := &ast.Foreach{Doc: doc, Expr: p.exprNoAs()}
	p.expect(token.T_AS)
	byRef, v := p.foreachVariable()
	if p.accept(token.T_DOUBLE_ARROW) {
		f.Key, f.KeyByRef = v, byRef
		byRef, v = p.foreachVariable()
	}
	f.ByRef, f.Value = byRef, v
	p.expect(token.Kind(')'))
	f.Body, f.Alt = p.loopBody(token.T_ENDFOREACH)
	f.S = p.span(start)
	return f
}

func (p *Parser) foreachVariable() (bool, ast.Expr) {
	byRef := p.acceptAmpersand()
	if byRef {
		return true, p.postfix(p.primary())
	}
	switch {
	case p.at(token.Kind('[')):
		return false, p.arrayLiteral(ast.ArrayShort)
	case p.at(token.T_LIST) && p.peekKind(1) == token.Kind('('):
		return false, p.arrayLiteral(ast.ArrayList)
	}
	return false, p.postfix(p.primary())
}

func (p *Parser) switchStmt() ast.Stmt {
	start := p.tok().Start
	p.expect(token.T_SWITCH)
	s := &ast.Switch{Subject: p.parenExpr()}
	closer := token.Kind('}')
	if p.accept(token.Kind(':')) {
		s.Alt = true
		closer = token.T_ENDSWITCH
	} else {
		p.expect(token.Kind('{'))
	}
	p.accept(token.Kind(';'))
	for !p.at(closer) {
		cstart := p.tok().Start
		c := &ast.Case{}
		if p.accept(token.T_DEFAULT) {
		} else {
			p.expect(token.T_CASE)
			c.Cond = p.expr()
		}
		if !p.accept(token.Kind(':')) {
			p.expect(token.Kind(';'))
		}
		c.Body = p.statementList(token.T_CASE, token.T_DEFAULT, closer)
		c.S = p.span(cstart)
		s.Cases = append(s.Cases, c)
	}
	p.expect(closer)
	if s.Alt {
		p.semi()
	}
	s.S = p.span(start)
	return s
}

func (p *Parser) tryStmt() ast.Stmt {
	start := p.tok().Start
	p.expect(token.T_TRY)
	t := &ast.Try{Body: p.block()}
	for p.at(token.T_CATCH) {
		cstart := p.tok().Start
		p.next()
		p.expect(token.Kind('('))
		c := &ast.Catch{}
		for {
			c.Types = append(c.Types, p.className())
			if !p.accept(token.Kind('|')) {
				break
			}
		}
		if p.at(token.T_VARIABLE) {
			c.Var = p.variable()
		}
		p.expect(token.Kind(')'))
		c.Body = p.block()
		c.S = p.span(cstart)
		t.Catches = append(t.Catches, c)
	}
	if p.accept(token.T_FINALLY) {
		t.HasFinally = true
		t.Finally = p.block()
	}
	t.S = p.span(start)
	return t
}

func (p *Parser) namespaceStmt() ast.Stmt {
	start := p.tok().Start
	p.expect(token.T_NAMESPACE)
	n := &ast.Namespace{}
	if !p.at(token.Kind('{')) {
		if p.at(token.T_NAME_QUALIFIED) || p.at(token.T_STRING) || isPhaenonContextual(p.kind()) {
			n.Name = p.name()
		} else {
			id := p.identifier()
			n.Name = &ast.Name{S: id.S, Parts: []string{id.Name}}
		}
	}
	if p.at(token.Kind('{')) {
		n.Braced = true
		n.Body = p.block()
	} else {
		p.semi()
	}
	n.S = p.span(start)
	return n
}

func (p *Parser) useKind() ast.UseKind {
	switch {
	case p.accept(token.T_FUNCTION):
		return ast.UseFunction
	case p.accept(token.T_CONST):
		return ast.UseConst
	}
	return ast.UseClass
}

func (p *Parser) useStmt() ast.Stmt {
	start := p.tok().Start
	p.expect(token.T_USE)
	u := &ast.Use{Kind: p.useKind()}
	// Group use: prefix\{ … }
	if (p.at(token.T_NAME_QUALIFIED) || p.at(token.T_NAME_FULLY_QUALIFIED) || p.at(token.T_STRING)) &&
		p.peekKind(1) == token.T_NS_SEPARATOR && p.peekKind(2) == token.Kind('{') {
		u.Prefix = p.name()
		p.expect(token.T_NS_SEPARATOR)
		p.expect(token.Kind('{'))
		for !p.at(token.Kind('}')) {
			istart := p.tok().Start
			it := &ast.UseItem{Kind: p.useKind()}
			it.Name = p.name()
			if p.accept(token.T_AS) {
				it.Alias = p.stringIdent()
			}
			it.S = p.span(istart)
			u.Items = append(u.Items, it)
			if !p.accept(token.Kind(',')) {
				break
			}
		}
		p.expect(token.Kind('}'))
	} else {
		for {
			istart := p.tok().Start
			it := &ast.UseItem{Kind: u.Kind, Name: p.name()}
			if p.accept(token.T_AS) {
				it.Alias = p.stringIdent()
			}
			it.S = p.span(istart)
			u.Items = append(u.Items, it)
			if !p.accept(token.Kind(',')) {
				break
			}
		}
	}
	p.semi()
	u.S = p.span(start)
	return u
}

// constItem parses NAME = expr (top-level const, class const, declare directive).
func (p *Parser) constItem() *ast.ConstItem {
	start := p.tok().Start
	c := &ast.ConstItem{Name: p.identifier()}
	p.expect(token.Kind('='))
	c.Value = p.expr()
	c.S = p.span(start)
	return c
}

// typeAlias parses Phaenon's `type Name<T> = Type;`.
func (p *Parser) typeAlias(attrs []*ast.AttrGroup) ast.Stmt {
	start := p.tok().Start
	doc := p.takeDoc()
	p.expect(token.T_TYPE)
	t := &ast.TypeAlias{Doc: doc, Attrs: attrs, Name: p.stringIdent()}
	t.TypeParams = p.typeParams()
	p.expect(token.Kind('='))
	t.Type = p.typeExpr()
	p.semi()
	t.S = p.span(start)
	return t
}

// ------------------------------------------------------------------ attributes, params

// attributes parses zero or more #[...] groups.
func (p *Parser) attributes() []*ast.AttrGroup {
	var groups []*ast.AttrGroup
	for p.at(token.T_ATTRIBUTE) {
		start := p.tok().Start
		p.next()
		g := &ast.AttrGroup{}
		for !p.at(token.Kind(']')) {
			astart := p.tok().Start
			a := &ast.Attribute{Name: p.className()}
			if p.at(token.Kind('(')) {
				a.Args = p.argList()
			}
			a.S = p.span(astart)
			g.Attrs = append(g.Attrs, a)
			if !p.accept(token.Kind(',')) {
				break
			}
		}
		p.expect(token.Kind(']'))
		g.S = p.span(start)
		groups = append(groups, g)
	}
	return groups
}

// modifiers parses a run of member/promotion modifiers.
func (p *Parser) modifiers() ast.Modifier {
	var m ast.Modifier
	for {
		switch p.kind() {
		case token.T_PUBLIC:
			m |= ast.ModPublic
		case token.T_PROTECTED:
			m |= ast.ModProtected
		case token.T_PRIVATE:
			m |= ast.ModPrivate
		case token.T_PUBLIC_SET:
			m |= ast.ModPublicSet
		case token.T_PROTECTED_SET:
			m |= ast.ModProtectedSet
		case token.T_PRIVATE_SET:
			m |= ast.ModPrivateSet
		case token.T_STATIC:
			m |= ast.ModStatic
		case token.T_ABSTRACT:
			m |= ast.ModAbstract
		case token.T_FINAL:
			m |= ast.ModFinal
		case token.T_READONLY:
			m |= ast.ModReadonly
		case token.T_VAR:
			m |= ast.ModVar
		default:
			return m
		}
		p.next()
	}
}

// params parses ( parameter_list ).
func (p *Parser) params() []*ast.Param {
	p.expect(token.Kind('('))
	params := []*ast.Param{}
	for !p.at(token.Kind(')')) {
		params = append(params, p.param())
		if !p.accept(token.Kind(',')) {
			break
		}
	}
	p.expect(token.Kind(')'))
	return params
}

func (p *Parser) param() *ast.Param {
	start := p.tok().Start
	doc := p.takeDoc()
	pr := &ast.Param{Attrs: p.attributes()}
	if doc == "" {
		doc = p.takeDoc()
	}
	pr.Doc = doc
	ms := p.tok().Start
	pr.Modifiers = p.modifiers()
	if pr.Modifiers != 0 {
		pr.Mods = ast.Span{Start: ms, End: p.end()}
	}
	if !p.at(token.T_VARIABLE) && !p.at(token.T_ELLIPSIS) &&
		!p.at(token.T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG) && !p.at(token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG) {
		pr.Type = p.typeExpr()
	}
	pr.ByRef = p.acceptAmpersand()
	pr.Variadic = p.accept(token.T_ELLIPSIS)
	pr.Var = p.variable()
	if p.accept(token.Kind('=')) {
		pr.Default = p.expr()
	}
	if p.at(token.Kind('{')) {
		pr.Hooks = p.propertyHooks()
	}
	pr.S = p.span(start)
	return pr
}

// propertyHooks parses { get …; set …; }.
func (p *Parser) propertyHooks() []*ast.PropertyHook {
	p.expect(token.Kind('{'))
	hooks := []*ast.PropertyHook{}
	for !p.at(token.Kind('}')) {
		start := p.tok().Start
		h := &ast.PropertyHook{Attrs: p.attributes()}
		h.Modifiers = p.modifiers()
		h.ByRef = p.acceptAmpersand()
		h.Name = p.stringIdent()
		if p.at(token.Kind('(')) {
			h.Params = p.params()
		}
		switch {
		case p.accept(token.Kind(';')):
			h.Abstract = true
		case p.accept(token.T_DOUBLE_ARROW):
			h.Expr = p.expr()
			p.expect(token.Kind(';'))
		default:
			h.Body = p.block()
		}
		h.S = p.span(start)
		hooks = append(hooks, h)
	}
	p.expect(token.Kind('}'))
	return hooks
}

// ------------------------------------------------------------------ declarations

func (p *Parser) functionDecl(attrs []*ast.AttrGroup) ast.Stmt {
	start := p.tok().Start
	if len(attrs) > 0 {
		start = attrs[0].S.Start
	}
	doc := p.takeDoc()
	kw := p.tok()
	fun := p.withdrawnWord("fun")
	if fun {
		p.next()
	} else {
		p.expect(token.T_FUNCTION)
	}
	f := &ast.FunctionDecl{Doc: doc, Attrs: attrs, Fun: fun, Kw: ast.Span{Start: kw.Start, End: kw.End}}
	f.ByRef = p.acceptAmpersand()
	if p.at(token.T_READONLY) {
		t := p.next()
		f.Name = &ast.Ident{S: ast.Span{Start: t.Start, End: t.End}, Name: t.Text(p.src)}
	} else {
		f.Name = p.stringIdent()
	}
	f.TypeParams = p.typeParams()
	f.Params = p.params()
	if p.accept(token.Kind(':')) {
		f.ReturnType = p.typeExpr()
	}
	f.Throws = p.throwsClause()
	if f.Arrow = p.exprBody(); f.Arrow != nil {
		f.Body = []ast.Stmt{bodyStmt(f.Arrow, f.ReturnType)}
	} else {
		f.Body = p.block()
	}
	f.S = p.span(start)
	return f
}

// throwsClause parses Phaenon's `throws A|B` after a signature
// (design/functions.md §4). `throws` is contextual: it is a keyword only
// here, after the parameter list or the return type, where PHP's grammar
// admits nothing but `{` or `;`, so the lexer leaves it a T_STRING and
// every name `throws` PHP code declares keeps parsing.
func (p *Parser) throwsClause() *ast.Throws {
	if p.mode != lexer.ModePhaenon || !p.at(token.T_STRING) || !strings.EqualFold(p.text(), "throws") {
		return nil
	}
	kw := p.next()
	t := &ast.Throws{Kw: ast.Span{Start: kw.Start, End: kw.End}}
	for {
		start := p.tok().Start
		t.Types = append(t.Types, &ast.NamedType{Name: p.name(), S: p.span(start)})
		if !p.accept(token.Kind('|')) {
			break
		}
	}
	t.S = p.span(kw.Start)
	return t
}

// exprBody parses Phaenon's expression body `=> expr;` in place of a
// named function's block (design/functions.md §3). PHP's grammar has no
// `=>` after a named function's signature, so plain PHP never reaches it.
func (p *Parser) exprBody() *ast.ExprBody {
	if p.mode != lexer.ModePhaenon || !p.at(token.T_DOUBLE_ARROW) {
		return nil
	}
	arrow := p.next()
	b := &ast.ExprBody{Arrow: ast.Span{Start: arrow.Start, End: arrow.End}, Expr: p.expr()}
	semi := p.expect(token.Kind(';'))
	b.Semi = ast.Span{Start: semi.Start, End: semi.End}
	b.S = ast.Span{Start: arrow.Start, End: semi.End}
	return b
}

// bodyStmt is the statement an expression body stands for: the
// expression returned, or only evaluated when the return type is void or
// never.
func bodyStmt(b *ast.ExprBody, ret ast.TypeExpr) ast.Stmt {
	if nt, ok := ret.(*ast.NamedType); ok && len(nt.TypeArgs) == 0 && len(nt.Name.Parts) == 1 && nt.Name.Kind == ast.NameUnqualified {
		if w := strings.ToLower(nt.Name.Parts[0]); w == "void" || w == "never" {
			return &ast.ExprStmt{S: b.S, Expr: b.Expr}
		}
	}
	return &ast.Return{S: b.S, Expr: b.Expr}
}

// classDecl parses class / interface / trait / enum declarations.
func (p *Parser) classDecl(attrs []*ast.AttrGroup) ast.Stmt {
	start := p.tok().Start
	if len(attrs) > 0 {
		start = attrs[0].S.Start
	}
	doc := p.takeDoc()
	c := &ast.ClassDecl{Doc: doc, Attrs: attrs}
	c.Modifiers = p.modifiers()
	switch p.kind() {
	case token.T_CLASS:
		c.Kind = ast.KindClass
	case token.T_INTERFACE:
		c.Kind = ast.KindInterface
	case token.T_TRAIT:
		c.Kind = ast.KindTrait
	case token.T_ENUM:
		c.Kind = ast.KindEnum
	default:
		p.errorf("unexpected %s, expecting class declaration", p.describe())
	}
	p.next()
	c.Name = p.stringIdent()
	c.TypeParams = p.typeParams()
	p.classHeader(c)
	c.Members = p.classBody()
	c.S = p.span(start)
	return c
}

// anonymousClass parses [#[..]] [modifiers] class (args) extends … { … } after `new`.
func (p *Parser) anonymousClass() *ast.ClassDecl {
	start := p.tok().Start
	c := &ast.ClassDecl{Attrs: p.attributes(), Kind: ast.KindClass}
	c.Modifiers = p.modifiers()
	p.expect(token.T_CLASS)
	if p.at(token.Kind('(')) {
		c.Args = p.argList()
	}
	p.classHeader(c)
	c.Members = p.classBody()
	c.S = p.span(start)
	return c
}

func (p *Parser) classHeader(c *ast.ClassDecl) {
	if c.Kind == ast.KindEnum && p.accept(token.Kind(':')) {
		c.BackingType = p.typeExpr()
	}
	if p.accept(token.T_EXTENDS) {
		for {
			c.Extends = append(c.Extends, p.superType())
			if c.Kind != ast.KindInterface || !p.accept(token.Kind(',')) {
				break
			}
		}
	}
	if p.accept(token.T_IMPLEMENTS) {
		for {
			c.Implements = append(c.Implements, p.superType())
			if !p.accept(token.Kind(',')) {
				break
			}
		}
	}
}

// superType parses a class name in an extends / implements list, with type
// arguments in Phaenon mode (`extends Base<User>`).
func (p *Parser) superType() *ast.NamedType {
	start := p.tok().Start
	t := &ast.NamedType{Name: p.className()}
	if p.mode == lexer.ModePhaenon && p.at(token.Kind('<')) {
		t.TypeArgs = p.typeArgs()
	}
	t.S = p.span(start)
	return t
}

func (p *Parser) classBody() []ast.ClassMember {
	p.expect(token.Kind('{'))
	members := []ast.ClassMember{}
	for !p.at(token.Kind('}')) {
		members = append(members, p.classMember())
	}
	p.expect(token.Kind('}'))
	return members
}

func (p *Parser) classMember() ast.ClassMember {
	start := p.tok().Start
	// `use(`, `case(` and their generic forms begin methods named so
	// (roadmap D27).
	if p.at(token.T_USE) && !p.bareMethodAhead() {
		return p.traitUse()
	}
	doc := p.takeDoc()
	attrs := p.attributes()
	if doc == "" {
		doc = p.takeDoc()
	}
	if p.at(token.T_CASE) && !p.bareMethodAhead() {
		p.next()
		e := &ast.EnumCase{Doc: doc, Attrs: attrs, Name: p.identifier()}
		if p.accept(token.Kind('=')) {
			e.Value = p.expr()
		}
		p.expect(token.Kind(';'))
		e.S = p.span(start)
		return e
	}
	head := p.tok().Start
	mods := p.modifiers()
	modsEnd := p.end()
	switch {
	case p.at(token.T_CONST):
		kw := p.next()
		c := &ast.ClassConstDecl{Doc: doc, Attrs: attrs, Modifiers: mods, Kw: ast.Span{Start: kw.Start, End: kw.End}}
		if mods != 0 {
			c.Mods = ast.Span{Start: head, End: modsEnd}
		}
		// Typed constant: `const int FOO = 1;` — a type is present when the
		// token after the next one is not '='.
		if p.peekKind(1) != token.Kind('=') {
			c.Type = p.typeExpr()
		}
		for {
			c.Consts = append(c.Consts, p.constItem())
			if !p.accept(token.Kind(',')) {
				break
			}
		}
		p.expect(token.Kind(';'))
		c.S = p.span(start)
		return c
	case p.at(token.T_FUNCTION), p.funAhead(true), p.bareMethodAhead():
		m := &ast.MethodDecl{Doc: doc, Attrs: attrs, Modifiers: mods}
		if fun := p.funAhead(true); fun || p.at(token.T_FUNCTION) {
			// PHP's `function`, or the withdrawn `fun`.
			kw := p.next()
			m.Fun = fun
			m.Kw = ast.Span{Start: kw.Start, End: kw.End}
			m.Head = ast.Span{Start: head, End: kw.End}
		} else {
			// No keyword (roadmap D27): the head is the modifiers.
			at := p.tok().Start
			m.Kw = ast.Span{Start: at, End: at}
			m.Head = ast.Span{Start: at, End: at}
			if mods != 0 {
				m.Head = ast.Span{Start: head, End: modsEnd}
			}
		}
		m.ByRef = p.acceptAmpersand()
		m.Name = p.identifier()
		m.TypeParams = p.typeParams()
		m.Params = p.params()
		if p.accept(token.Kind(':')) {
			m.ReturnType = p.typeExpr()
		}
		m.Throws = p.throwsClause()
		switch {
		case p.accept(token.Kind(';')):
			m.HasBody = false
		default:
			m.HasBody = true
			if m.Arrow = p.exprBody(); m.Arrow != nil {
				m.Body = []ast.Stmt{bodyStmt(m.Arrow, m.ReturnType)}
			} else {
				m.Body = p.block()
			}
		}
		m.S = p.span(start)
		return m
	}
	// Property.
	pd := &ast.PropertyDecl{Doc: doc, Attrs: attrs, Modifiers: mods}
	if mods != 0 {
		pd.Mods = ast.Span{Start: head, End: modsEnd}
	}
	if !p.at(token.T_VARIABLE) {
		pd.Type = p.typeExpr()
	}
	for {
		istart := p.tok().Start
		it := &ast.PropertyItem{Var: p.variable()}
		if p.accept(token.Kind('=')) {
			it.Default = p.expr()
		}
		it.S = p.span(istart)
		pd.Props = append(pd.Props, it)
		if !p.accept(token.Kind(',')) {
			break
		}
	}
	if p.at(token.Kind('{')) {
		pd.Hooks = p.propertyHooks()
	} else {
		p.expect(token.Kind(';'))
	}
	pd.S = p.span(start)
	return pd
}

func (p *Parser) traitUse() ast.ClassMember {
	start := p.tok().Start
	p.expect(token.T_USE)
	t := &ast.TraitUse{}
	for {
		t.Names = append(t.Names, p.className())
		if !p.accept(token.Kind(',')) {
			break
		}
	}
	if p.accept(token.Kind(';')) {
		t.S = p.span(start)
		return t
	}
	t.Braced = true
	p.expect(token.Kind('{'))
	for !p.at(token.Kind('}')) {
		astart := p.tok().Start
		a := &ast.TraitAdaptation{}
		if p.peekKind(1) == token.T_PAAMAYIM_NEKUDOTAYIM {
			a.Trait = p.className()
			p.next()
		}
		a.Method = p.identifier()
		if p.accept(token.T_INSTEADOF) {
			for {
				a.InsteadOf = append(a.InsteadOf, p.className())
				if !p.accept(token.Kind(',')) {
					break
				}
			}
		} else {
			p.expect(token.T_AS)
			if isModifierKeyword(p.kind()) && p.kind() != token.T_STATIC {
				a.Modifiers = p.modifiers()
			}
			if !p.at(token.Kind(';')) {
				a.Alias = p.identifier()
			}
		}
		p.expect(token.Kind(';'))
		a.S = p.span(astart)
		t.Adaptations = append(t.Adaptations, a)
	}
	p.expect(token.Kind('}'))
	t.S = p.span(start)
	return t
}
