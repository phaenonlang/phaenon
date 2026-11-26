package checker

import (
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// lateStatic reports `static` where PHP compiles a constant expression
// (P2532): a parameter's default, a property's, a constant's value, an
// enum case's value and an attribute's argument, on every declaration that
// carries one. PHP evaluates such an expression for the class that
// declares it, without late static binding, and refuses `static::X`,
// `static::class`, `new static` and a callable made of `static::m` when it
// compiles the file, once for the expression. A static variable's
// initialiser is code that runs (PHP 8.3) and may name static, as may the
// body of a closure written in a constant expression. Outside a class,
// static is P2012: the check of the expression's types reports it, and
// this pass where nothing types the expression, an attribute's argument.
// An operand PHP's compiler folds away is not compiled, and not reported
// (foldStatic). The expressions reported are typed as such
// (refusedStatic), so that a typed default adds no mismatch.
func (c *Checker) lateStatic(f *program.File) {
	var walk func(n ast.Node, class *ast.ClassDecl)
	walk = func(root ast.Node, class *ast.ClassDecl) {
		// PHP compiles the attributes of a class constant and of an enum
		// case after it has declared the constant, the first of a group's.
		declaredFirst := map[*ast.Attribute]int{}
		after := func(groups []*ast.AttrGroup, end int) {
			for _, g := range groups {
				for _, at := range g.Attrs {
					declaredFirst[at] = end
				}
			}
		}
		ast.Inspect(root, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.ClassDecl:
				if n != root {
					walk(n, n)
					return false
				}
			case *ast.FunctionDecl:
				if n != root {
					walk(n, nil) // a function declared in a method has no class
					return false
				}
			case *ast.Param:
				c.lateStaticIn(f, n.Default, class, constPlace{param: true})
			case *ast.Attribute:
				compiled, ok := declaredFirst[n]
				if !ok {
					compiled = n.S.Start
				}
				if n.Args != nil {
					for _, a := range n.Args.Args {
						c.lateStaticIn(f, a.Value, class, constPlace{attribute: true, compiled: compiled})
					}
				}
			case *ast.PropertyItem:
				c.lateStaticIn(f, n.Default, class, constPlace{})
			case *ast.ClassConstDecl:
				if len(n.Consts) > 0 {
					after(n.Attrs, n.Consts[0].S.End)
				}
			case *ast.ConstItem:
				c.lateStaticIn(f, n.Value, class, constPlace{})
			case *ast.EnumCase:
				after(n.Attrs, n.S.End)
				c.lateStaticIn(f, n.Value, class, constPlace{})
			}
			return true
		})
	}
	walk(f.AST, nil)
}

// constPlace is where a constant expression stands: an attribute's
// argument, which nothing else types outside a class, compiled where PHP
// compiles the attribute (lateStatic); a parameter's default; or another
// value, compiled in its own place.
type constPlace struct {
	attribute bool
	compiled  int
	param     bool
}

// lateStaticIn reports the static of one constant expression, once.
// Closures in it are left out: their bodies are code, and their own
// parameters' defaults are constant expressions of their own, reached as
// parameters.
func (c *Checker) lateStaticIn(f *program.File, e ast.Expr, class *ast.ClassDecl, place constPlace) {
	if e == nil || class == nil && !place.attribute {
		return
	}
	at := foldScope{class: class, compiled: e.Span().Start, scope: f.ScopeAt(e.Span().Start), p: c.p, noConstants: place.param}
	if place.attribute {
		at.compiled = place.compiled
	}
	reported := false
	refuse := func(n ast.Expr, span ast.Span, written, instead string) {
		if c.refusedStatic == nil {
			c.refusedStatic = map[ast.Expr]bool{}
		}
		c.refusedStatic[n] = true
		if reported {
			return // php refuses the expression once
		}
		reported = true
		switch {
		case class == nil:
			c.report(span, P2012, "static used outside a class")
		case instead == "":
			c.reportRefused(true, span, P2532, nil, "%s in a constant expression: PHP evaluates the expression for the class that declares it and refuses static there at compile time", written)
		default:
			c.reportRefused(true, span, P2532, nil, "%s in a constant expression: PHP evaluates the expression for the class that declares it and refuses static there at compile time; write %s", written, instead)
		}
	}
	fold := func(e ast.Expr) foldResult { return foldStatic(e, at, 0) }
	// scan says whether PHP compiles a static in root, and refuses it
	// where refusing is set.
	var scan func(root ast.Node, refusing bool) bool
	scan = func(root ast.Node, refusing bool) (found bool) {
		static := func(n ast.Expr, span ast.Span, written, instead string) {
			found = true
			if refusing {
				refuse(n, span, written, instead)
			}
		}
		ast.Inspect(root, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Closure, *ast.ArrowFunc, *ast.ClassDecl:
				return false
			case *ast.Binary:
				var decides func(foldResult) bool // whether the right side is compiled
				switch n.Op {
				case token.T_COALESCE:
					decides = func(l foldResult) bool { return l.null }
				case token.T_BOOLEAN_AND, token.T_LOGICAL_AND:
					decides = func(l foldResult) bool { return l.truthy }
				case token.T_BOOLEAN_OR, token.T_LOGICAL_OR:
					decides = func(l foldResult) bool { return !l.truthy }
				default:
					return true
				}
				found = scan(n.Left, refusing) || found
				if l := fold(n.Left); l.kind == notFolded || l.kind == folded && decides(l) {
					found = scan(n.Right, refusing) || found
				}
				return false
			case *ast.Ternary:
				found = scan(n.Cond, refusing) || found
				switch cond := fold(n.Cond); cond.kind {
				case notFolded:
					found = scan(n.Then, refusing) || found
					found = scan(n.Else, refusing) || found
				case folded:
					if cond.truthy {
						found = scan(n.Then, refusing) || found // nil for `?:`: the condition is the value
					} else {
						found = scan(n.Else, refusing) || found
					}
				case maybeFolded:
					// PHP keeps one branch or both: where each holds static,
					// it compiles static whatever it folds the condition to.
					if n.Then != nil && scan(n.Then, false) && scan(n.Else, false) {
						scan(n.Then, refusing)
						scan(n.Else, refusing)
						found = true
					}
				}
				return false
			case *ast.ClassConstFetch:
				if s := staticName(n.Class); s != nil {
					name := "X"
					if id, ok := n.Name.(*ast.Ident); ok {
						name = id.Name
					}
					static(n, s.S, "static::"+name, "self::"+name)
					return true
				}
				if inner, ok := n.Class.(*ast.ClassConstFetch); ok {
					if s := staticName(inner.Class); s != nil {
						// `static::M::class`: php refuses `self::M::class` as well,
						// an expression's class in a constant expression.
						written := "static::"
						if id, ok := inner.Name.(*ast.Ident); ok {
							written += id.Name
						}
						if id, ok := n.Name.(*ast.Ident); ok {
							written += "::" + id.Name
						}
						static(inner, s.S, written, "")
						return false
					}
				}
			case *ast.New:
				if s := staticName(n.Class); s != nil {
					static(n, s.S, "new static", "new self")
				}
			case *ast.StaticCall:
				if s := staticName(n.Class); s != nil && n.Args.Closure() {
					name := "m"
					if id, ok := n.Name.(*ast.Ident); ok {
						name = id.Name
					}
					static(n, s.S, "static::"+name+"(…)", "self::"+name+"(…)")
				}
			}
			return true
		})
		return found
	}
	scan(e, true)
}

// staticName is the name of a class reference written `static`, nil for
// any other.
func staticName(e ast.Expr) *ast.Name {
	if n, ok := e.(*ast.Name); ok && specialClassName(n) == "static" {
		return n
	}
	return nil
}

// foldKind says what PHP's compiler makes of an operand of a constant
// expression before it compiles the rest: a value it folds the operand to
// and which is known here, a value it may fold the operand to that is not
// known here (an internal constant, another class's constant, an
// arithmetic), or none, the operand being compiled as it is.
type foldKind uint8

const (
	notFolded foldKind = iota
	maybeFolded
	folded
)

// foldResult is an operand as PHP's compiler folds it; null and truthy
// describe a folded value.
type foldResult struct {
	kind   foldKind
	null   bool
	truthy bool
}

// foldScope is what PHP's compiler has at hand where it compiles a
// constant expression, as far as folding it goes: the class, the place
// it compiles the expression at (the class's constants declared before
// it are the ones it has compiled), the names in scope, and whether it is
// a parameter's default, where it substitutes no constant at all
// (ZEND_COMPILE_NO_CONSTANT_SUBSTITUTION, kept for ReflectionParameter).
type foldScope struct {
	class       *ast.ClassDecl
	compiled    int
	scope       *program.Scope
	p           *program.Program
	noConstants bool
}

// foldStatic is what PHP's compiler folds e to (zend_eval_const_expr):
// literals, true, false and null, the operators of folded operands, and,
// outside a parameter's default, a constant of the class being compiled
// whose value folds and which the class declares before the place PHP
// compiles e at: PHP looks the constant up among those it has compiled
// so far (zend_try_ct_eval_class_const). An unqualified constant other
// than true, false and null is looked up in the namespace, where PHP's
// own constants are not, and folds nothing there, unless the program
// declares the constant in the namespace. Where the value is not known
// here, a constant of another class or a fully qualified or global one,
// the side it may drop is left alone, the lenient side where php does
// not fold after all (a user's global constant).
func foldStatic(e ast.Expr, at foldScope, depth int) foldResult {
	known := func(truthy bool) foldResult { return foldResult{kind: folded, truthy: truthy} }
	maybe := foldResult{kind: maybeFolded}
	if depth > 16 {
		return maybe
	}
	both := func(rs ...foldResult) foldResult {
		for _, r := range rs {
			if r.kind == notFolded {
				return foldResult{}
			}
		}
		return maybe
	}
	switch e := e.(type) {
	case *ast.Paren:
		return foldStatic(e.Expr, at, depth+1)
	case *ast.IntLit:
		if v, isInt := types.IsIntLit(intLitType(e.Raw)); isInt {
			return known(v != 0)
		}
		return known(true) // an overflow is a non-zero float
	case *ast.FloatLit:
		f, err := strconv.ParseFloat(strings.ReplaceAll(e.Raw, "_", ""), 64)
		if err != nil {
			return maybe
		}
		return known(f != 0)
	case *ast.StringLit:
		v := program.StringValue(e)
		return known(v != "" && v != "0")
	case *ast.ConstFetch:
		return foldStatic(e.Name, at, depth+1)
	case *ast.Name:
		n := e
		if len(n.Parts) == 1 {
			switch strings.ToLower(n.Parts[0]) {
			case "true":
				return known(true)
			case "false":
				return known(false)
			case "null":
				return foldResult{kind: folded, null: true}
			}
		}
		if at.noConstants {
			return foldResult{}
		}
		if namespaced, global := at.scope.ResolveConst(n); namespaced != global && at.p.Constant(namespaced) == nil {
			return foldResult{} // looked up in the namespace, where it is not
		}
		return maybe // an internal constant folds, a user's does not
	case *ast.MagicConst:
		return maybe
	case *ast.Unary:
		inner := foldStatic(e.Expr, at, depth+1)
		if inner.kind != folded {
			return inner
		}
		switch e.Op {
		case token.Kind('!'):
			return known(!inner.truthy)
		case token.Kind('-'), token.Kind('+'):
			return known(inner.truthy)
		}
		return maybe
	case *ast.Cast:
		return both(foldStatic(e.Expr, at, depth+1))
	case *ast.ArrayLit:
		for _, it := range e.Items {
			if it == nil || it.ByRef || it.Unpack {
				return foldResult{}
			}
			if it.Key != nil && foldStatic(it.Key, at, depth+1).kind == notFolded || foldStatic(it.Value, at, depth+1).kind == notFolded {
				return foldResult{}
			}
		}
		return known(len(e.Items) > 0)
	case *ast.ClassConstFetch:
		n, isName := e.Class.(*ast.Name)
		id, isIdent := e.Name.(*ast.Ident)
		if !isName || !isIdent || specialClassName(n) == "static" {
			return foldResult{}
		}
		if strings.EqualFold(id.Name, "class") {
			return maybe
		}
		if at.noConstants {
			return foldResult{}
		}
		class := at.class
		own := class != nil && (specialClassName(n) == "self" ||
			class.Name != nil && n.Kind == ast.NameUnqualified && len(n.Parts) == 1 && strings.EqualFold(n.Parts[0], class.Name.Name))
		if !own {
			return maybe // another class's constant folds where php knows the class
		}
		for _, m := range class.Members {
			switch m := m.(type) {
			case *ast.ClassConstDecl:
				for _, k := range m.Consts {
					if k.Name.Name == id.Name {
						if k.S.End > at.compiled {
							return foldResult{} // not declared yet: php compiles the fetch
						}
						inner := at
						inner.compiled = k.Value.Span().Start
						return foldStatic(k.Value, inner, depth+1)
					}
				}
			case *ast.EnumCase:
				if m.Name.Name == id.Name {
					return foldResult{} // an object: php does not fold it
				}
			}
		}
		return foldResult{}
	case *ast.Binary:
		l := foldStatic(e.Left, at, depth+1)
		switch e.Op {
		case token.T_COALESCE:
			if l.kind == folded {
				if !l.null {
					return l
				}
				return foldStatic(e.Right, at, depth+1)
			}
		case token.T_BOOLEAN_AND, token.T_LOGICAL_AND, token.T_BOOLEAN_OR, token.T_LOGICAL_OR:
			or := e.Op == token.T_BOOLEAN_OR || e.Op == token.T_LOGICAL_OR
			if l.kind == folded && l.truthy == or {
				return known(or)
			}
			if r := foldStatic(e.Right, at, depth+1); l.kind == folded && r.kind == folded {
				return known(r.truthy)
			}
		case token.T_PIPE:
			return foldResult{}
		}
		return both(l, foldStatic(e.Right, at, depth+1))
	case *ast.Ternary:
		cond := foldStatic(e.Cond, at, depth+1)
		if cond.kind == folded {
			if cond.truthy {
				if e.Then == nil {
					return cond
				}
				return foldStatic(e.Then, at, depth+1)
			}
			return foldStatic(e.Else, at, depth+1)
		}
		rs := []foldResult{cond, foldStatic(e.Else, at, depth+1)}
		if e.Then != nil {
			rs = append(rs, foldStatic(e.Then, at, depth+1))
		}
		return both(rs...)
	case *ast.Index:
		if e.Index == nil {
			return foldResult{}
		}
		return both(foldStatic(e.Array, at, depth+1), foldStatic(e.Index, at, depth+1))
	}
	return foldResult{}
}

// lateStaticRefused says whether e is a static reference lateStatic
// reported, which is typed as an expression already reported.
func (x *fnCtx) lateStaticRefused(e ast.Expr) bool {
	return x.c.refusedStatic[e]
}
