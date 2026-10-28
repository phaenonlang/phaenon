package parser

import (
	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/token"
)

// Binding powers, lowest to highest, from the %precedence table of
// zend_language_parser.y. Prefix operators parse their operand with their
// own power as the minimum.
const (
	bpLowest     = 0
	bpThrow      = 1
	bpArrowFn    = 2
	bpInclude    = 3
	bpLogicalOr  = 4
	bpLogicalXor = 5
	bpLogicalAnd = 6
	bpPrint      = 7
	bpYield      = 8
	bpYieldFrom  = 10
	bpAssign     = 11
	bpTernary    = 12
	bpCoalesce   = 13
	bpBoolOr     = 14
	bpBoolAnd    = 15
	bpBitOr      = 16
	bpBitXor     = 17
	bpBitAnd     = 18
	bpEquality   = 19
	bpComparison = 20
	bpPipe       = 21
	bpConcat     = 22
	bpShift      = 23
	bpAdditive   = 24
	bpMul        = 25
	bpNot        = 26
	bpInstanceof = 27
	bpUnary      = 28 // ~ casts @ unary + -
	bpPow        = 29
	bpClone      = 30
)

type infixOp struct {
	lbp        int
	rightAssoc bool
	nonAssoc   bool
}

func infix(k token.Kind) (infixOp, bool) {
	switch k {
	case token.T_LOGICAL_OR:
		return infixOp{lbp: bpLogicalOr}, true
	case token.T_LOGICAL_XOR:
		return infixOp{lbp: bpLogicalXor}, true
	case token.T_LOGICAL_AND:
		return infixOp{lbp: bpLogicalAnd}, true
	case token.T_COALESCE:
		return infixOp{lbp: bpCoalesce, rightAssoc: true}, true
	case token.T_BOOLEAN_OR:
		return infixOp{lbp: bpBoolOr}, true
	case token.T_BOOLEAN_AND:
		return infixOp{lbp: bpBoolAnd}, true
	case token.Kind('|'):
		return infixOp{lbp: bpBitOr}, true
	case token.Kind('^'):
		return infixOp{lbp: bpBitXor}, true
	case token.T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG, token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG:
		return infixOp{lbp: bpBitAnd}, true
	case token.T_IS_EQUAL, token.T_IS_NOT_EQUAL, token.T_IS_IDENTICAL, token.T_IS_NOT_IDENTICAL, token.T_SPACESHIP:
		return infixOp{lbp: bpEquality, nonAssoc: true}, true
	case token.Kind('<'), token.Kind('>'), token.T_IS_SMALLER_OR_EQUAL, token.T_IS_GREATER_OR_EQUAL:
		return infixOp{lbp: bpComparison, nonAssoc: true}, true
	case token.T_PIPE:
		return infixOp{lbp: bpPipe}, true
	case token.Kind('.'):
		return infixOp{lbp: bpConcat}, true
	case token.T_SL, token.T_SR:
		return infixOp{lbp: bpShift}, true
	case token.Kind('+'), token.Kind('-'):
		return infixOp{lbp: bpAdditive}, true
	case token.Kind('*'), token.Kind('/'), token.Kind('%'):
		return infixOp{lbp: bpMul}, true
	case token.T_POW:
		return infixOp{lbp: bpPow, rightAssoc: true}, true
	}
	return infixOp{}, false
}

func isAssignOp(k token.Kind) bool {
	switch k {
	case token.Kind('='), token.T_PLUS_EQUAL, token.T_MINUS_EQUAL, token.T_MUL_EQUAL, token.T_POW_EQUAL,
		token.T_DIV_EQUAL, token.T_CONCAT_EQUAL, token.T_MOD_EQUAL, token.T_AND_EQUAL, token.T_OR_EQUAL,
		token.T_XOR_EQUAL, token.T_SL_EQUAL, token.T_SR_EQUAL, token.T_COALESCE_EQUAL:
		return true
	}
	return false
}

// isVariableExpr reports the grammar's `variable` non-terminal (an lvalue).
func isVariableExpr(e ast.Expr) bool {
	switch e.(type) {
	case *ast.Variable, *ast.VariableVariable, *ast.Index, *ast.PropertyFetch, *ast.StaticPropertyFetch,
		*ast.Call, *ast.MethodCall, *ast.StaticCall:
		return true
	}
	return false
}

// expr parses a full expression.
func (p *Parser) expr() ast.Expr { return p.exprBP(bpLowest) }

// exprBP is the Pratt loop: parse a prefix/primary, then fold infix
// operators whose binding power is at least min.
func (p *Parser) exprBP(min int) ast.Expr {
	start := p.tok().Start
	left := p.prefix()
	lastNonAssoc := -1
	for {
		k := p.kind()

		// Assignment binds to a variable on its left regardless of min:
		// `$a + $b = 3` is `$a + ($b = 3)` in PHP.
		if isAssignOp(k) && (isVariableExpr(left) || isDestructuring(left, k)) {
			p.next()
			a := &ast.Assign{Op: k, Target: left}
			if k == token.Kind('=') && (p.at(token.T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG) || p.at(token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG)) {
				p.next()
				a.ByRef = true
			}
			a.Value = p.exprBP(bpAssign)
			a.S = p.span(start)
			left = a
			continue
		}

		switch k {
		case token.T_INC, token.T_DEC:
			if isVariableExpr(left) {
				p.next()
				left = &ast.IncDec{S: p.span(start), Inc: k == token.T_INC, Expr: left}
				continue
			}
		case token.Kind('?'):
			if bpTernary < min {
				return left
			}
			p.next()
			t := &ast.Ternary{Cond: left}
			if !p.accept(token.Kind(':')) {
				t.Then = p.exprBP(bpLowest)
				p.expect(token.Kind(':'))
			}
			t.Else = p.exprBP(bpTernary + 1)
			t.S = p.span(start)
			left = t
			continue
		case token.T_INSTANCEOF:
			if bpInstanceof < min {
				return left
			}
			p.next()
			left = &ast.Instanceof{Expr: left, Class: p.classNameReference()}
			left.(*ast.Instanceof).S = p.span(start)
			continue
		case token.T_IS, token.T_AS:
			if p.mode != lexer.ModePhaenon || bpInstanceof < min {
				return left
			}
			if k == token.T_IS {
				p.next()
				left = &ast.IsExpr{Expr: left, Type: p.typeExpr()}
				left.(*ast.IsExpr).S = p.span(start)
			} else {
				p.next()
				as := &ast.AsExpr{Expr: left}
				if p.accept(token.Kind('?')) {
					as.Nullable = true
				}
				as.Type = p.typeExpr()
				as.S = p.span(start)
				left = as
			}
			continue
		}

		op, ok := infix(k)
		if !ok || op.lbp < min {
			return left
		}
		if op.nonAssoc && op.lbp == lastNonAssoc {
			p.errorf("syntax error, unexpected %s (non-associative operator)", p.describe())
		}
		p.next()
		rbp := op.lbp + 1
		if op.rightAssoc {
			rbp = op.lbp
		}
		right := p.exprBP(rbp)
		left = &ast.Binary{S: p.span(start), Op: k, Left: left, Right: right}
		if op.nonAssoc {
			lastNonAssoc = op.lbp
		} else {
			lastNonAssoc = -1
		}
	}
}

// isDestructuring reports `[..] = ` and `list(..) = `.
func isDestructuring(e ast.Expr, op token.Kind) bool {
	a, ok := e.(*ast.ArrayLit)
	return ok && op == token.Kind('=') && (a.Kind == ast.ArrayShort || a.Kind == ast.ArrayList)
}

// prefix parses prefix operators and primaries.
func (p *Parser) prefix() ast.Expr {
	start := p.tok().Start
	k := p.kind()
	switch k {
	case token.Kind('!'):
		p.next()
		return &ast.Unary{S: p.span(start), Op: k, Expr: p.exprBP(bpNot)}
	case token.Kind('~'), token.Kind('-'), token.Kind('+'), token.Kind('@'):
		p.next()
		return &ast.Unary{S: p.span(start), Op: k, Expr: p.exprBP(bpUnary)}
	case token.T_INT_CAST, token.T_DOUBLE_CAST, token.T_STRING_CAST, token.T_ARRAY_CAST,
		token.T_OBJECT_CAST, token.T_BOOL_CAST, token.T_UNSET_CAST:
		t := p.next()
		return &ast.Cast{S: p.span(start), Op: k, Raw: t.Text(p.src), Expr: p.exprBP(bpUnary)}
	case token.T_INC, token.T_DEC:
		p.next()
		e := p.exprBP(bpUnary)
		return &ast.IncDec{S: p.span(start), Prefix: true, Inc: k == token.T_INC, Expr: e}
	case token.T_CLONE:
		p.next()
		if p.at(token.Kind('(')) {
			if args, ok := p.cloneArgs(); ok {
				return &ast.Clone{S: p.span(start), Args: args}
			}
		}
		return &ast.Clone{S: p.span(start), Expr: p.exprBP(bpClone)}
	case token.T_PRINT:
		p.next()
		return &ast.Print{S: p.span(start), Expr: p.exprBP(bpPrint)}
	case token.T_YIELD:
		p.next()
		y := &ast.Yield{}
		if p.startsExpr() {
			y.Value = p.exprBP(bpYield)
			if p.accept(token.T_DOUBLE_ARROW) {
				y.Key = y.Value
				y.Value = p.exprBP(bpYield)
			}
		}
		y.S = p.span(start)
		return y
	case token.T_YIELD_FROM:
		p.next()
		return &ast.YieldFrom{S: p.span(start), Expr: p.exprBP(bpYieldFrom)}
	case token.T_THROW:
		p.next()
		return &ast.Throw{S: p.span(start), Expr: p.exprBP(bpThrow)}
	case token.T_INCLUDE, token.T_INCLUDE_ONCE, token.T_REQUIRE, token.T_REQUIRE_ONCE:
		p.next()
		return &ast.Include{S: p.span(start), Kind: k, Expr: p.exprBP(bpInclude)}
	case token.T_NEW:
		return p.postfix(p.newExpr())
	}
	return p.postfix(p.primary())
}

// startsExpr reports whether the current token can begin an expression
// (used after `yield`, `return`, `break`, …): `yield * 2` is `(yield) * 2`.
func (p *Parser) startsExpr() bool {
	k := p.kind()
	switch k {
	case token.T_VARIABLE, token.Kind('$'), token.T_LNUMBER, token.T_DNUMBER, token.T_CONSTANT_ENCAPSED_STRING,
		token.Kind('"'), token.Kind('`'), token.T_START_HEREDOC, token.Kind('['), token.Kind('('),
		token.Kind('!'), token.Kind('~'), token.Kind('-'), token.Kind('+'), token.Kind('@'),
		token.T_INT_CAST, token.T_DOUBLE_CAST, token.T_STRING_CAST, token.T_ARRAY_CAST, token.T_OBJECT_CAST,
		token.T_BOOL_CAST, token.T_UNSET_CAST, token.T_INC, token.T_DEC,
		token.T_NEW, token.T_CLONE, token.T_PRINT, token.T_YIELD, token.T_YIELD_FROM, token.T_THROW,
		token.T_INCLUDE, token.T_INCLUDE_ONCE, token.T_REQUIRE, token.T_REQUIRE_ONCE,
		token.T_ISSET, token.T_EMPTY, token.T_EVAL, token.T_EXIT, token.T_MATCH, token.T_FUNCTION, token.T_FN,
		token.T_ATTRIBUTE, token.T_STATIC, token.T_ARRAY, token.T_LIST, token.T_READONLY,
		token.T_LINE, token.T_FILE, token.T_DIR, token.T_CLASS_C, token.T_TRAIT_C, token.T_METHOD_C,
		token.T_FUNC_C, token.T_PROPERTY_C, token.T_NS_C,
		token.T_STRING, token.T_NAME_QUALIFIED, token.T_NAME_FULLY_QUALIFIED, token.T_NAME_RELATIVE:
		return true
	}
	return isPhaenonContextual(k)
}

// cloneArgs tries the PHP 8.5 `clone($x, [...])` call form. A plain
// `clone($x)` is the prefix form with a parenthesised operand.
func (p *Parser) cloneArgs() (*ast.ArgList, bool) {
	save := p.i
	args := p.argList()
	if len(args.Args) == 1 && args.Args[0].Name == nil && !args.Args[0].Unpack && !args.Args[0].Placeholder &&
		!args.FirstClassCallable && p.toks[p.i-1].Kind != token.Kind(',') && !p.trailingComma(args) {
		p.i = save
		p.docComment = ""
		return nil, false
	}
	return args, true
}

// trailingComma reports whether an argument list ended with ",)".
func (p *Parser) trailingComma(a *ast.ArgList) bool {
	j := p.i - 1 // the ')'
	for j--; j > 0 && p.toks[j].Kind.IsTrivia(); j-- {
	}
	return p.toks[j].Kind == token.Kind(',')
}

// primary parses literals, variables, names, parenthesised expressions,
// closures, match, and the keyword-functions.
func (p *Parser) primary() ast.Expr {
	start := p.tok().Start
	t := p.tok()
	k := t.Kind
	switch {
	case k == token.T_VARIABLE:
		return p.variable()
	case k == token.Kind('$'):
		return p.simpleVariable()
	case k == token.T_LNUMBER:
		p.next()
		return &ast.IntLit{S: p.span(start), Raw: t.Text(p.src)}
	case k == token.T_DNUMBER:
		p.next()
		return &ast.FloatLit{S: p.span(start), Raw: t.Text(p.src)}
	case k == token.T_CONSTANT_ENCAPSED_STRING:
		p.next()
		return &ast.StringLit{S: p.span(start), Raw: t.Text(p.src)}
	case k == token.Kind('"'):
		return p.interpolated(ast.StringDoubleQuoted, token.Kind('"'))
	case k == token.Kind('`'):
		return p.interpolated(ast.StringBacktick, token.Kind('`'))
	case k == token.T_START_HEREDOC:
		return p.heredoc()
	case k == token.Kind('['):
		return p.arrayLiteral(ast.ArrayShort)
	case k == token.T_ARRAY && p.peekKind(1) == token.Kind('('):
		return p.arrayLiteral(ast.ArrayLong)
	case k == token.T_LIST && p.peekKind(1) == token.Kind('('):
		return p.arrayLiteral(ast.ArrayList)
	case k == token.Kind('('):
		p.next()
		e := p.expr()
		p.expect(token.Kind(')'))
		return &ast.Paren{S: p.span(start), Expr: e}
	case k == token.T_ISSET:
		p.next()
		p.expect(token.Kind('('))
		var vars []ast.Expr
		for {
			vars = append(vars, p.expr())
			if !p.accept(token.Kind(',')) || p.at(token.Kind(')')) {
				break
			}
		}
		p.expect(token.Kind(')'))
		return &ast.Isset{S: p.span(start), Vars: vars}
	case k == token.T_EMPTY:
		p.next()
		p.expect(token.Kind('('))
		e := p.expr()
		p.expect(token.Kind(')'))
		return &ast.Empty{S: p.span(start), Expr: e}
	case k == token.T_EVAL:
		p.next()
		p.expect(token.Kind('('))
		e := p.expr()
		p.expect(token.Kind(')'))
		return &ast.Eval{S: p.span(start), Expr: e}
	case k == token.T_EXIT:
		p.next()
		ex := &ast.Exit{Raw: t.Text(p.src)}
		if p.at(token.Kind('(')) {
			ex.Args = p.argList()
		}
		ex.S = p.span(start)
		return ex
	case k == token.T_MATCH && p.peekKind(1) == token.Kind('('):
		return p.matchExpr()
	case k == token.T_FUNCTION || k == token.T_FN || k == token.T_ATTRIBUTE ||
		(k == token.T_STATIC && (p.peekKind(1) == token.T_FUNCTION || p.peekKind(1) == token.T_FN)):
		return p.closure()
	case k == token.T_LINE || k == token.T_FILE || k == token.T_DIR || k == token.T_CLASS_C || k == token.T_TRAIT_C ||
		k == token.T_METHOD_C || k == token.T_FUNC_C || k == token.T_PROPERTY_C || k == token.T_NS_C:
		p.next()
		return &ast.MagicConst{S: p.span(start), Kind: k, Raw: t.Text(p.src)}
	case k == token.T_STATIC:
		// static::foo(), static::$x, static::CONST
		p.next()
		return &ast.Name{S: p.span(start), Parts: []string{"static"}}
	case p.atName():
		return p.name()
	case k == token.T_READONLY && p.peekKind(1) == token.Kind('('):
		// readonly(...) is a function call (the keyword is a valid function name).
		p.next()
		return &ast.Name{S: p.span(start), Parts: []string{t.Text(p.src)}}
	}
	p.errorf("syntax error, unexpected %s", p.describe())
	return nil
}

// simpleVariable parses `$` followed by a variable, braces, or a name
// ($$a, ${expr}).
func (p *Parser) simpleVariable() ast.Expr {
	start := p.tok().Start
	p.expect(token.Kind('$'))
	if p.accept(token.Kind('{')) {
		e := p.expr()
		p.expect(token.Kind('}'))
		return &ast.VariableVariable{S: p.span(start), Expr: e, Braced: true}
	}
	if p.at(token.T_VARIABLE) {
		return &ast.VariableVariable{S: p.span(start), Expr: p.variable()}
	}
	if p.at(token.Kind('$')) {
		return &ast.VariableVariable{S: p.span(start), Expr: p.simpleVariable()}
	}
	p.errorf("syntax error, unexpected %s after '$'", p.describe())
	return nil
}

// postfix applies [..], ->, ?->, ::, (..) chains to a primary.
func (p *Parser) postfix(e ast.Expr) ast.Expr {
	start := e.Span().Start
	for {
		switch p.kind() {
		case token.Kind('['):
			p.next()
			idx := &ast.Index{Array: e}
			if !p.at(token.Kind(']')) {
				idx.Index = p.expr()
			}
			p.expect(token.Kind(']'))
			idx.S = p.span(start)
			e = idx
		case token.Kind('{'):
			// $a{0} was removed in PHP 8.
			return e
		case token.T_OBJECT_OPERATOR, token.T_NULLSAFE_OBJECT_OPERATOR:
			nullSafe := p.kind() == token.T_NULLSAFE_OBJECT_OPERATOR
			p.next()
			name := p.propertyName()
			if p.at(token.Kind('(')) || (p.mode == lexer.ModePhaenon && p.at(token.Kind('<')) && p.looksLikeTypeArgs()) {
				mc := &ast.MethodCall{Object: e, Name: name, NullSafe: nullSafe}
				mc.TypeArgs = p.optionalCallTypeArgs()
				mc.Args = p.argList()
				mc.S = p.span(start)
				e = mc
			} else {
				e = &ast.PropertyFetch{S: p.span(start), Object: e, Name: name, NullSafe: nullSafe}
			}
		case token.T_PAAMAYIM_NEKUDOTAYIM:
			p.next()
			e = p.staticMember(start, e)
		case token.Kind('('):
			e = &ast.Call{Callee: e, Args: p.argList()}
			e.(*ast.Call).S = p.span(start)
		case token.Kind('<'):
			if p.mode == lexer.ModePhaenon && p.looksLikeTypeArgs() {
				if n, ok := e.(*ast.Name); ok {
					c := &ast.Call{Callee: n, TypeArgs: p.typeArgs()}
					c.Args = p.argList()
					c.S = p.span(start)
					e = c
					continue
				}
			}
			return e
		default:
			return e
		}
	}
}

// propertyName parses property_name after -> : T_STRING, {expr}, $var, $$var.
func (p *Parser) propertyName() ast.Expr {
	start := p.tok().Start
	switch {
	case p.at(token.Kind('{')):
		p.next()
		e := p.expr()
		p.expect(token.Kind('}'))
		return &ast.Paren{S: p.span(start), Expr: e}
	case p.at(token.T_VARIABLE):
		return p.variable()
	case p.at(token.Kind('$')):
		return p.simpleVariable()
	}
	// After -> the lexer already turned keywords into T_STRING.
	return p.identifier()
}

// staticMember parses what follows `Class::`.
func (p *Parser) staticMember(start int, class ast.Expr) ast.Expr {
	switch {
	case p.at(token.T_VARIABLE), p.at(token.Kind('$')):
		var name ast.Expr
		if p.at(token.T_VARIABLE) {
			name = p.variable()
		} else {
			name = p.simpleVariable()
		}
		if p.at(token.Kind('(')) {
			sc := &ast.StaticCall{Class: class, Name: name, Args: p.argList()}
			sc.S = p.span(start)
			return sc
		}
		return &ast.StaticPropertyFetch{S: p.span(start), Class: class, Name: name}
	case p.at(token.Kind('{')):
		nstart := p.tok().Start
		p.next()
		e := p.expr()
		p.expect(token.Kind('}'))
		name := &ast.Paren{S: p.span(nstart), Expr: e}
		if p.at(token.Kind('(')) {
			sc := &ast.StaticCall{Class: class, Name: name, Args: p.argList()}
			sc.S = p.span(start)
			return sc
		}
		return &ast.ClassConstFetch{S: p.span(start), Class: class, Name: name}
	}
	name := p.identifier()
	if p.at(token.Kind('(')) || (p.mode == lexer.ModePhaenon && p.at(token.Kind('<')) && p.looksLikeTypeArgs()) {
		sc := &ast.StaticCall{Class: class, Name: name}
		sc.TypeArgs = p.optionalCallTypeArgs()
		sc.Args = p.argList()
		sc.S = p.span(start)
		return sc
	}
	return &ast.ClassConstFetch{S: p.span(start), Class: class, Name: name}
}

// optionalCallTypeArgs parses `<T>` before an argument list in Phaenon mode.
func (p *Parser) optionalCallTypeArgs() []ast.TypeExpr {
	if p.mode == lexer.ModePhaenon && p.at(token.Kind('<')) {
		return p.typeArgs()
	}
	return nil
}

// looksLikeTypeArgs speculatively checks that `<…>` at the current position
// is a type-argument list followed by '(' — the C#/TypeScript rule.
func (p *Parser) looksLikeTypeArgs() bool {
	save := p.i
	saveToks := p.toks
	saveDoc := p.docComment
	ok := func() (ok bool) {
		defer func() {
			if r := recover(); r != nil {
				if _, isBail := r.(bailout); isBail {
					ok = false
					return
				}
				panic(r)
			}
		}()
		p.typeArgs()
		return p.at(token.Kind('('))
	}()
	p.i = save
	p.toks = saveToks
	p.docComment = saveDoc
	return ok
}

// argList parses `( args )`, including f(...) and PFA placeholders.
func (p *Parser) argList() *ast.ArgList {
	start := p.tok().Start
	p.expect(token.Kind('('))
	al := &ast.ArgList{}
	if p.at(token.T_ELLIPSIS) && p.peekKind(1) == token.Kind(')') {
		p.next()
		p.next()
		al.FirstClassCallable = true
		al.S = p.span(start)
		return al
	}
	for !p.at(token.Kind(')')) {
		astart := p.tok().Start
		a := &ast.Arg{}
		switch {
		case p.at(token.T_ELLIPSIS):
			p.next()
			a.Unpack = true
			a.Value = p.expr()
		case p.at(token.Kind('?')) && (p.peekKind(1) == token.Kind(',') || p.peekKind(1) == token.Kind(')')):
			p.next()
			a.Placeholder = true
		case (p.at(token.T_STRING) || isSemiReserved(p.kind())) && p.peekKind(1) == token.Kind(':') && p.peekKind(2) != token.Kind(':'):
			a.Name = p.identifier()
			p.expect(token.Kind(':'))
			if p.at(token.Kind('?')) && (p.peekKind(1) == token.Kind(',') || p.peekKind(1) == token.Kind(')')) {
				p.next()
				a.Placeholder = true
			} else {
				a.Value = p.expr()
			}
		default:
			a.Value = p.expr()
		}
		a.S = p.span(astart)
		al.Args = append(al.Args, a)
		if !p.accept(token.Kind(',')) {
			break
		}
	}
	p.expect(token.Kind(')'))
	al.S = p.span(start)
	return al
}

// newExpr parses new Foo(..), new $x, new (expr), new class {..}.
func (p *Parser) newExpr() ast.Expr {
	start := p.tok().Start
	p.expect(token.T_NEW)
	n := &ast.New{}
	// Anonymous class: new [#[..]] [modifiers] class (..) extends .. {..}
	if p.at(token.T_CLASS) || p.at(token.T_ATTRIBUTE) || isClassModifier(p.kind()) {
		n.Anon = p.anonymousClass()
		n.S = p.span(start)
		return n
	}
	n.Class = p.classNameReference()
	n.TypeArgs = p.optionalCallTypeArgs()
	if p.at(token.Kind('(')) {
		n.Args = p.argList()
	}
	n.S = p.span(start)
	if n.Args == nil {
		// new_non_dereferenceable: no postfix chain may follow.
		return n
	}
	return n
}

func isClassModifier(k token.Kind) bool {
	return k == token.T_ABSTRACT || k == token.T_FINAL || k == token.T_READONLY
}

// classNameReference parses class_name_reference: a name, `static`, a
// parenthesised expression, or a new_variable chain (no calls).
func (p *Parser) classNameReference() ast.Expr {
	start := p.tok().Start
	if p.at(token.T_STATIC) || p.atName() {
		return p.className()
	}
	if p.at(token.Kind('(')) {
		p.next()
		e := p.expr()
		p.expect(token.Kind(')'))
		return &ast.Paren{S: p.span(start), Expr: e}
	}
	var e ast.Expr
	switch {
	case p.at(token.T_VARIABLE):
		e = p.variable()
	case p.at(token.Kind('$')):
		e = p.simpleVariable()
	default:
		p.errorf("syntax error, unexpected %s, expecting class name", p.describe())
	}
	// new_variable: [..], ->prop, ?->prop, ::$var
	for {
		switch p.kind() {
		case token.Kind('['):
			p.next()
			idx := &ast.Index{Array: e}
			if !p.at(token.Kind(']')) {
				idx.Index = p.expr()
			}
			p.expect(token.Kind(']'))
			idx.S = p.span(start)
			e = idx
		case token.T_OBJECT_OPERATOR, token.T_NULLSAFE_OBJECT_OPERATOR:
			nullSafe := p.kind() == token.T_NULLSAFE_OBJECT_OPERATOR
			p.next()
			e = &ast.PropertyFetch{S: p.span(start), Object: e, Name: p.propertyName(), NullSafe: nullSafe}
		case token.T_PAAMAYIM_NEKUDOTAYIM:
			if p.peekKind(1) != token.T_VARIABLE && p.peekKind(1) != token.Kind('$') {
				return e
			}
			p.next()
			var name ast.Expr
			if p.at(token.T_VARIABLE) {
				name = p.variable()
			} else {
				name = p.simpleVariable()
			}
			e = &ast.StaticPropertyFetch{S: p.span(start), Class: e, Name: name}
		default:
			return e
		}
	}
}

// arrayLiteral parses [..], array(..) or list(..).
func (p *Parser) arrayLiteral(kind ast.ArrayKind) *ast.ArrayLit {
	start := p.tok().Start
	closer := token.Kind(']')
	if kind == ast.ArrayShort {
		p.expect(token.Kind('['))
	} else {
		p.next() // array / list
		p.expect(token.Kind('('))
		closer = token.Kind(')')
	}
	a := &ast.ArrayLit{Kind: kind}
	for !p.at(closer) {
		if p.at(token.Kind(',')) {
			p.next()
			a.Items = append(a.Items, nil) // skipped position in list(, $b)
			continue
		}
		a.Items = append(a.Items, p.arrayItem())
		if !p.accept(token.Kind(',')) {
			break
		}
	}
	p.expect(closer)
	a.S = p.span(start)
	return a
}

func (p *Parser) arrayItem() *ast.ArrayItem {
	start := p.tok().Start
	it := &ast.ArrayItem{}
	switch {
	case p.at(token.T_ELLIPSIS):
		p.next()
		it.Unpack = true
		it.Value = p.expr()
	case p.at(token.T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG), p.at(token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG):
		p.next()
		it.ByRef = true
		it.Value = p.exprBP(bpLowest)
	default:
		v := p.expr()
		if p.accept(token.T_DOUBLE_ARROW) {
			it.Key = v
			if p.at(token.T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG) || p.at(token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG) {
				p.next()
				it.ByRef = true
			}
			it.Value = p.expr()
		} else {
			it.Value = v
		}
	}
	it.S = p.span(start)
	return it
}

// interpolated parses "…" or `…` after seeing the opening quote token.
func (p *Parser) interpolated(kind ast.StringKind, closer token.Kind) ast.Expr {
	start := p.tok().Start
	open := p.next()
	s := &ast.InterpolatedString{Kind: kind, Open: open.Text(p.src)}
	for !p.at(closer) {
		s.Parts = append(s.Parts, p.encapsPart())
	}
	s.Close = p.next().Text(p.src)
	s.S = p.span(start)
	return s
}

// heredoc parses <<<X … X (heredoc or nowdoc).
func (p *Parser) heredoc() ast.Expr {
	start := p.tok().Start
	open := p.next()
	s := &ast.InterpolatedString{Kind: ast.StringHeredoc, Open: open.Text(p.src)}
	for !p.at(token.T_END_HEREDOC) {
		s.Parts = append(s.Parts, p.encapsPart())
	}
	s.Close = p.expect(token.T_END_HEREDOC).Text(p.src)
	s.S = p.span(start)
	return s
}

// encapsPart parses one element of encaps_list.
func (p *Parser) encapsPart() ast.Expr {
	start := p.tok().Start
	switch p.kind() {
	case token.T_ENCAPSED_AND_WHITESPACE:
		t := p.next()
		return &ast.StringPart{S: p.span(start), Raw: t.Text(p.src)}
	case token.T_VARIABLE:
		v := p.variable()
		switch p.kind() {
		case token.Kind('['):
			p.next()
			var idx ast.Expr
			istart := p.tok().Start
			switch p.kind() {
			case token.T_STRING:
				idx = p.stringIdent()
			case token.T_NUM_STRING:
				t := p.next()
				idx = &ast.IntLit{S: p.span(istart), Raw: t.Text(p.src)}
			case token.Kind('-'):
				p.next()
				t := p.expect(token.T_NUM_STRING)
				idx = &ast.IntLit{S: p.span(istart), Raw: "-" + t.Text(p.src)}
			case token.T_VARIABLE:
				idx = p.variable()
			default:
				p.errorf("syntax error, unexpected %s in string offset", p.describe())
			}
			p.expect(token.Kind(']'))
			return &ast.Index{S: p.span(start), Array: v, Index: idx}
		case token.T_OBJECT_OPERATOR, token.T_NULLSAFE_OBJECT_OPERATOR:
			nullSafe := p.kind() == token.T_NULLSAFE_OBJECT_OPERATOR
			p.next()
			name := p.stringIdent()
			return &ast.PropertyFetch{S: p.span(start), Object: v, Name: name, NullSafe: nullSafe}
		}
		return v
	case token.T_DOLLAR_OPEN_CURLY_BRACES:
		p.next()
		if p.at(token.T_STRING_VARNAME) {
			t := p.next()
			sv := &ast.StringVarName{Name: t.Text(p.src)}
			if p.accept(token.Kind('[')) {
				sv.Index = p.expr()
				p.expect(token.Kind(']'))
			}
			p.expect(token.Kind('}'))
			sv.S = p.span(start)
			return sv
		}
		e := p.expr()
		p.expect(token.Kind('}'))
		return &ast.VariableVariable{S: p.span(start), Expr: e, Braced: true}
	case token.T_CURLY_OPEN:
		p.next()
		e := p.expr()
		p.expect(token.Kind('}'))
		return &ast.EncapsBraced{S: p.span(start), Expr: e}
	}
	p.errorf("syntax error, unexpected %s in string", p.describe())
	return nil
}

// matchExpr parses match (x) { arms }.
func (p *Parser) matchExpr() ast.Expr {
	start := p.tok().Start
	p.expect(token.T_MATCH)
	p.expect(token.Kind('('))
	m := &ast.Match{Subject: p.expr()}
	p.expect(token.Kind(')'))
	p.expect(token.Kind('{'))
	for !p.at(token.Kind('}')) {
		astart := p.tok().Start
		arm := &ast.MatchArm{}
		if p.at(token.T_DEFAULT) && (p.peekKind(1) == token.T_DOUBLE_ARROW || p.peekKind(1) == token.Kind(',')) {
			p.next()
			p.accept(token.Kind(','))
		} else {
			for {
				arm.Conds = append(arm.Conds, p.expr())
				if !p.accept(token.Kind(',')) || p.at(token.T_DOUBLE_ARROW) {
					break
				}
			}
		}
		p.expect(token.T_DOUBLE_ARROW)
		arm.Body = p.expr()
		arm.S = p.span(astart)
		m.Arms = append(m.Arms, arm)
		if !p.accept(token.Kind(',')) {
			break
		}
	}
	p.expect(token.Kind('}'))
	m.S = p.span(start)
	return m
}

// closure parses [#[..]] [static] function(..) use(..): T {..} and fn(..) => expr.
func (p *Parser) closure() ast.Expr {
	start := p.tok().Start
	attrs := p.attributes()
	static := p.accept(token.T_STATIC)
	if p.at(token.T_FN) {
		p.next()
		f := &ast.ArrowFunc{Attrs: attrs, Static: static}
		f.ByRef = p.acceptAmpersand()
		f.TypeParams = p.typeParams()
		f.Params = p.params()
		if p.accept(token.Kind(':')) {
			f.ReturnType = p.typeExpr()
		}
		p.expect(token.T_DOUBLE_ARROW)
		f.Body = p.exprBP(bpArrowFn)
		f.S = p.span(start)
		return f
	}
	p.expect(token.T_FUNCTION)
	c := &ast.Closure{Attrs: attrs, Static: static}
	c.ByRef = p.acceptAmpersand()
	c.TypeParams = p.typeParams()
	c.Params = p.params()
	if p.accept(token.T_USE) {
		p.expect(token.Kind('('))
		for !p.at(token.Kind(')')) {
			ustart := p.tok().Start
			u := &ast.ClosureUse{}
			u.ByRef = p.acceptAmpersand()
			u.Var = p.variable()
			u.S = p.span(ustart)
			c.Uses = append(c.Uses, u)
			if !p.accept(token.Kind(',')) {
				break
			}
		}
		p.expect(token.Kind(')'))
	}
	if p.accept(token.Kind(':')) {
		c.ReturnType = p.typeExpr()
	}
	c.Body = p.block()
	c.S = p.span(start)
	return c
}

func (p *Parser) acceptAmpersand() bool {
	if p.at(token.T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG) || p.at(token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG) {
		p.next()
		return true
	}
	return false
}
