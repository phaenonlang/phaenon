package printer

import (
	"fmt"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/token"
)

// spell returns the source spelling of an operator or keyword token kind.
func spell(k token.Kind) string {
	if k < 256 {
		return string(rune(k))
	}
	switch k {
	case token.T_PLUS_EQUAL:
		return "+="
	case token.T_MINUS_EQUAL:
		return "-="
	case token.T_MUL_EQUAL:
		return "*="
	case token.T_DIV_EQUAL:
		return "/="
	case token.T_CONCAT_EQUAL:
		return ".="
	case token.T_MOD_EQUAL:
		return "%="
	case token.T_AND_EQUAL:
		return "&="
	case token.T_OR_EQUAL:
		return "|="
	case token.T_XOR_EQUAL:
		return "^="
	case token.T_SL_EQUAL:
		return "<<="
	case token.T_SR_EQUAL:
		return ">>="
	case token.T_POW_EQUAL:
		return "**="
	case token.T_COALESCE_EQUAL:
		return "??="
	case token.T_BOOLEAN_OR:
		return "||"
	case token.T_BOOLEAN_AND:
		return "&&"
	case token.T_LOGICAL_OR:
		return "or"
	case token.T_LOGICAL_AND:
		return "and"
	case token.T_LOGICAL_XOR:
		return "xor"
	case token.T_IS_EQUAL:
		return "=="
	case token.T_IS_NOT_EQUAL:
		return "!="
	case token.T_IS_IDENTICAL:
		return "==="
	case token.T_IS_NOT_IDENTICAL:
		return "!=="
	case token.T_IS_SMALLER_OR_EQUAL:
		return "<="
	case token.T_IS_GREATER_OR_EQUAL:
		return ">="
	case token.T_SPACESHIP:
		return "<=>"
	case token.T_SL:
		return "<<"
	case token.T_SR:
		return ">>"
	case token.T_COALESCE:
		return "??"
	case token.T_POW:
		return "**"
	case token.T_PIPE:
		return "|>"
	case token.T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG, token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG:
		return "&"
	case token.T_INCLUDE:
		return "include"
	case token.T_INCLUDE_ONCE:
		return "include_once"
	case token.T_REQUIRE:
		return "require"
	case token.T_REQUIRE_ONCE:
		return "require_once"
	}
	panic(fmt.Sprintf("printer: no spelling for token %s", k))
}

func (p *Printer) exprs(es []ast.Expr) {
	for i, e := range es {
		if i > 0 {
			p.w(", ")
		}
		p.expr(e)
	}
}

func (p *Printer) expr(e ast.Expr) {
	switch e := e.(type) {
	case *ast.Variable:
		p.w("$")
		p.w(e.Name)
	case *ast.VariableVariable:
		if e.Braced {
			p.w("${")
			p.expr(e.Expr)
			p.w("}")
		} else {
			p.w("$")
			p.expr(e.Expr)
		}
	case *ast.IntLit:
		p.w(e.Raw)
	case *ast.FloatLit:
		p.w(e.Raw)
	case *ast.StringLit:
		p.raw(e.Raw)
	case *ast.InterpolatedString:
		p.w(e.Open)
		p.lineStart = false // heredoc openers end with a newline that must not trigger indentation
		for _, part := range e.Parts {
			// Inside a string `${NAME}` is the T_STRING_VARNAME form; the
			// expression form `${ expr }` needs something between `${` and
			// the label, so the lexer does not take the shortcut.
			if vv, ok := part.(*ast.VariableVariable); ok && vv.Braced {
				p.w("${ ")
				p.expr(vv.Expr)
				p.w("}")
				continue
			}
			p.expr(part)
		}
		p.sb.WriteString(e.Close)
		p.lineStart = false
	case *ast.StringPart:
		p.sb.WriteString(e.Raw)
		p.lineStart = false
	case *ast.StringVarName:
		p.w("${")
		p.w(e.Name)
		if e.Index != nil {
			p.w("[")
			p.expr(e.Index)
			p.w("]")
		}
		p.w("}")
	case *ast.EncapsBraced:
		p.w("{")
		p.expr(e.Expr)
		p.w("}")
	case *ast.ArrayLit:
		p.arrayLit(e)
	case *ast.ConstFetch:
		p.name(e.Name)
	case *ast.MagicConst:
		p.w(e.Raw)
	case *ast.ClassConstFetch:
		p.expr(e.Class)
		p.w("::")
		p.memberName(e.Name)
	case *ast.PropertyFetch:
		p.expr(e.Object)
		p.objectOp(e.NullSafe)
		p.memberName(e.Name)
	case *ast.StaticPropertyFetch:
		p.expr(e.Class)
		p.w("::")
		p.expr(e.Name)
	case *ast.Index:
		p.expr(e.Array)
		p.w("[")
		if e.Index != nil {
			p.expr(e.Index)
		}
		p.w("]")
	case *ast.Call:
		p.expr(e.Callee)
		p.typeArgs(e.TypeArgs)
		p.argList(e.Args)
	case *ast.MethodCall:
		p.expr(e.Object)
		p.objectOp(e.NullSafe)
		p.memberName(e.Name)
		p.typeArgs(e.TypeArgs)
		p.argList(e.Args)
	case *ast.StaticCall:
		p.expr(e.Class)
		p.w("::")
		p.memberName(e.Name)
		p.typeArgs(e.TypeArgs)
		p.argList(e.Args)
	case *ast.New:
		p.w("new ")
		if e.Anon != nil {
			p.anonClass(e.Anon)
			return
		}
		p.expr(e.Class)
		p.typeArgs(e.TypeArgs)
		if e.Args != nil {
			p.argList(e.Args)
		}
	case *ast.Clone:
		p.w("clone")
		if e.Args != nil {
			p.argList(e.Args)
		} else {
			p.w(" ")
			p.expr(e.Expr)
		}
	case *ast.Unary:
		op := spell(e.Op)
		p.w(op)
		if op == "-" || op == "+" {
			// Keep `- -$x` and `- --$x` from fusing into `--`.
			sub := &Printer{depth: p.depth, indentStr: p.indentStr}
			sub.expr(e.Expr)
			inner := sub.sb.String()
			if len(inner) > 0 && (inner[0] == '-' || inner[0] == '+') {
				p.w(" ")
			}
			p.raw(inner)
			return
		}
		p.expr(e.Expr)
	case *ast.Cast:
		p.w(e.Raw)
		p.w(" ")
		p.expr(e.Expr)
	case *ast.IncDec:
		op := "--"
		if e.Inc {
			op = "++"
		}
		if e.Prefix {
			p.w(op)
			p.expr(e.Expr)
		} else {
			p.expr(e.Expr)
			p.w(op)
		}
	case *ast.Binary:
		p.expr(e.Left)
		p.w(" ")
		p.w(spell(e.Op))
		p.w(" ")
		p.expr(e.Right)
	case *ast.Assign:
		p.expr(e.Target)
		p.w(" ")
		p.w(spell(e.Op))
		p.w(" ")
		if e.ByRef {
			p.w("&")
		}
		p.expr(e.Value)
	case *ast.Ternary:
		p.expr(e.Cond)
		if e.Then == nil {
			p.w(" ?: ")
		} else {
			p.w(" ? ")
			p.expr(e.Then)
			p.w(" : ")
		}
		p.expr(e.Else)
	case *ast.Instanceof:
		p.expr(e.Expr)
		p.w(" instanceof ")
		p.expr(e.Class)
	case *ast.Isset:
		p.w("isset(")
		p.exprs(e.Vars)
		p.w(")")
	case *ast.Empty:
		p.w("empty(")
		p.expr(e.Expr)
		p.w(")")
	case *ast.Eval:
		p.w("eval(")
		p.expr(e.Expr)
		p.w(")")
	case *ast.Exit:
		p.w(e.Raw)
		if e.Args != nil {
			p.argList(e.Args)
		}
	case *ast.Print:
		p.w("print ")
		p.expr(e.Expr)
	case *ast.Include:
		p.w(spell(e.Kind))
		p.w(" ")
		p.expr(e.Expr)
	case *ast.Closure:
		p.attrsInline(e.Attrs)
		if e.Static {
			p.w("static ")
		}
		p.w("function ")
		if e.ByRef {
			p.w("&")
		}
		p.typeParams(e.TypeParams)
		p.params(e.Params)
		if len(e.Uses) > 0 {
			p.w(" use (")
			for i, u := range e.Uses {
				if i > 0 {
					p.w(", ")
				}
				if u.ByRef {
					p.w("&")
				}
				p.expr(u.Var)
			}
			p.w(")")
		}
		p.returnType(e.ReturnType)
		p.w(" ")
		p.block(e.Body)
	case *ast.ArrowFunc:
		p.attrsInline(e.Attrs)
		if e.Static {
			p.w("static ")
		}
		p.w("fn")
		if e.ByRef {
			p.w("&")
		}
		p.typeParams(e.TypeParams)
		p.params(e.Params)
		p.returnType(e.ReturnType)
		p.w(" => ")
		p.expr(e.Body)
	case *ast.Match:
		p.w("match (")
		p.expr(e.Subject)
		p.w(") {")
		p.nl()
		p.depth++
		for _, arm := range e.Arms {
			if arm.Conds == nil {
				p.w("default")
			} else {
				p.exprs(arm.Conds)
			}
			p.w(" => ")
			p.expr(arm.Body)
			p.w(",")
			p.nl()
		}
		p.depth--
		p.w("}")
	case *ast.Yield:
		p.w("yield")
		if e.Key != nil {
			p.w(" ")
			p.expr(e.Key)
			p.w(" =>")
		}
		if e.Value != nil {
			p.w(" ")
			p.expr(e.Value)
		}
	case *ast.YieldFrom:
		p.w("yield from ")
		p.expr(e.Expr)
	case *ast.Throw:
		p.w("throw ")
		p.expr(e.Expr)
	case *ast.Paren:
		p.w("(")
		p.expr(e.Expr)
		p.w(")")
	case *ast.IsExpr:
		p.expr(e.Expr)
		p.w(" is ")
		p.typ(e.Type)
	case *ast.AsExpr:
		p.expr(e.Expr)
		if e.Nullable {
			p.w(" as? ")
		} else {
			p.w(" as ")
		}
		p.typ(e.Type)
	case *ast.Name:
		p.name(e)
	case *ast.Ident:
		p.ident(e)
	default:
		panic(fmt.Sprintf("printer: unknown expression node %T", e))
	}
}

func (p *Printer) objectOp(nullSafe bool) {
	if nullSafe {
		p.w("?->")
	} else {
		p.w("->")
	}
}

// memberName prints what follows -> or ::. A Paren node there means the
// braced form `{expr}`.
func (p *Printer) memberName(n ast.Expr) {
	if par, ok := n.(*ast.Paren); ok {
		p.w("{")
		p.expr(par.Expr)
		p.w("}")
		return
	}
	p.expr(n)
}

func (p *Printer) argList(a *ast.ArgList) {
	p.w("(")
	if a.FirstClassCallable {
		p.w("...)")
		return
	}
	for i, arg := range a.Args {
		if i > 0 {
			p.w(", ")
		}
		if arg.Name != nil {
			p.ident(arg.Name)
			p.w(": ")
		}
		switch {
		case arg.Placeholder != nil && arg.Placeholder.Variadic:
			p.w("...")
		case arg.Placeholder != nil:
			p.w("?")
		case arg.Unpack:
			p.w("...")
			p.expr(arg.Value)
		default:
			p.expr(arg.Value)
		}
	}
	p.w(")")
}

func (p *Printer) arrayLit(a *ast.ArrayLit) {
	open, close := "[", "]"
	switch a.Kind {
	case ast.ArrayLong:
		open, close = "array(", ")"
	case ast.ArrayList:
		open, close = "list(", ")"
	}
	p.w(open)
	for i, it := range a.Items {
		if i > 0 {
			p.w(", ")
		}
		if it == nil {
			continue
		}
		if it.Unpack {
			p.w("...")
		}
		if it.Key != nil {
			p.expr(it.Key)
			p.w(" => ")
		}
		if it.ByRef {
			p.w("&")
		}
		p.expr(it.Value)
	}
	p.w(close)
}
