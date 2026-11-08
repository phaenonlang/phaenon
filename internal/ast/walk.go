package ast

import "reflect"

// Inspect traverses the tree rooted at n in depth-first, source order,
// calling f for every node. When f returns false the node's children are
// skipped. Nil children are never visited.
//
// Every syntactic element that owns a span is visited, including names,
// identifiers, parameters, arguments, attribute groups, type parameters and
// hooks, so a pass can treat the tree uniformly.
func Inspect(n Node, f func(Node) bool) {
	if isNil(n) || !f(n) {
		return
	}
	switch n := n.(type) {
	case *File:
		stmts(n.Stmts, f)

	// ------------------------------------------------------------ names and small nodes
	case *Name, *Ident, *Variable, *IntLit, *FloatLit, *StringLit, *StringPart,
		*MagicConst, *InlineHTML, *OpenTag, *CloseTag, *Nop, *HaltCompiler, *Placeholder:
		// leaves

	// ------------------------------------------------------------ expressions
	case *VariableVariable:
		Inspect(n.Expr, f)
	case *InterpolatedString:
		exprs(n.Parts, f)
	case *StringVarName:
		Inspect(n.Index, f)
	case *EncapsBraced:
		Inspect(n.Expr, f)
	case *ArrayLit:
		for _, it := range n.Items {
			Inspect(it, f)
		}
	case *ArrayItem:
		Inspect(n.Key, f)
		Inspect(n.Value, f)
	case *ConstFetch:
		Inspect(n.Name, f)
	case *ClassConstFetch:
		Inspect(n.Class, f)
		Inspect(n.Name, f)
	case *PropertyFetch:
		Inspect(n.Object, f)
		Inspect(n.Name, f)
	case *StaticPropertyFetch:
		Inspect(n.Class, f)
		Inspect(n.Name, f)
	case *Index:
		Inspect(n.Array, f)
		Inspect(n.Index, f)
	case *Call:
		Inspect(n.Callee, f)
		types(n.TypeArgs, f)
		Inspect(n.Args, f)
	case *MethodCall:
		Inspect(n.Object, f)
		Inspect(n.Name, f)
		types(n.TypeArgs, f)
		Inspect(n.Args, f)
	case *StaticCall:
		Inspect(n.Class, f)
		Inspect(n.Name, f)
		types(n.TypeArgs, f)
		Inspect(n.Args, f)
	case *ArgList:
		for _, a := range n.Args {
			Inspect(a, f)
		}
	case *Arg:
		Inspect(n.Name, f)
		Inspect(n.Value, f)
		Inspect(n.Placeholder, f)
	case *New:
		Inspect(n.Class, f)
		types(n.TypeArgs, f)
		Inspect(n.Args, f)
		Inspect(n.Anon, f)
	case *Clone:
		Inspect(n.Expr, f)
		Inspect(n.Args, f)
	case *Unary:
		Inspect(n.Expr, f)
	case *Cast:
		Inspect(n.Expr, f)
	case *IncDec:
		Inspect(n.Expr, f)
	case *Binary:
		Inspect(n.Left, f)
		Inspect(n.Right, f)
	case *Assign:
		Inspect(n.Target, f)
		Inspect(n.Value, f)
	case *Ternary:
		Inspect(n.Cond, f)
		Inspect(n.Then, f)
		Inspect(n.Else, f)
	case *Instanceof:
		Inspect(n.Expr, f)
		Inspect(n.Class, f)
	case *Isset:
		exprs(n.Vars, f)
	case *Empty:
		Inspect(n.Expr, f)
	case *Exit:
		Inspect(n.Args, f)
	case *Print:
		Inspect(n.Expr, f)
	case *Include:
		Inspect(n.Expr, f)
	case *Eval:
		Inspect(n.Expr, f)
	case *Closure:
		attrs(n.Attrs, f)
		typeParams(n.TypeParams, f)
		params(n.Params, f)
		for _, u := range n.Uses {
			Inspect(u, f)
		}
		Inspect(n.ReturnType, f)
		stmts(n.Body, f)
	case *ClosureUse:
		Inspect(n.Var, f)
	case *ArrowFunc:
		attrs(n.Attrs, f)
		typeParams(n.TypeParams, f)
		params(n.Params, f)
		Inspect(n.ReturnType, f)
		Inspect(n.Body, f)
	case *Withdrawn:
		Inspect(n.Opts, f)
		stmts(n.Body, f)
		Inspect(n.Expr, f)
	case *Match:
		Inspect(n.Subject, f)
		for _, a := range n.Arms {
			Inspect(a, f)
		}
	case *MatchArm:
		exprs(n.Conds, f)
		Inspect(n.Body, f)
	case *Yield:
		Inspect(n.Key, f)
		Inspect(n.Value, f)
	case *YieldFrom:
		Inspect(n.Expr, f)
	case *Throw:
		Inspect(n.Expr, f)
	case *Paren:
		Inspect(n.Expr, f)
	case *IsExpr:
		Inspect(n.Expr, f)
		Inspect(n.Type, f)
	case *AsExpr:
		Inspect(n.Expr, f)
		Inspect(n.Type, f)

	// ------------------------------------------------------------ types
	case *NamedType:
		Inspect(n.Name, f)
		types(n.TypeArgs, f)
	case *NullableType:
		Inspect(n.Type, f)
	case *UnionType:
		types(n.Types, f)
	case *IntersectionType:
		types(n.Types, f)
	case *ClosureType:
		for _, p := range n.Params {
			Inspect(p.Type, f)
		}
		Inspect(n.Return, f)
	case *TupleType:
		types(n.Elems, f)
	case *TypeParam:
		Inspect(n.Name, f)
		Inspect(n.Bound, f)
		Inspect(n.Default, f)

	// ------------------------------------------------------------ declarations
	case *AttrGroup:
		for _, a := range n.Attrs {
			Inspect(a, f)
		}
	case *Attribute:
		Inspect(n.Name, f)
		Inspect(n.Args, f)
	case *Param:
		attrs(n.Attrs, f)
		Inspect(n.Type, f)
		Inspect(n.Var, f)
		Inspect(n.Default, f)
		hooks(n.Hooks, f)
	case *PropertyHook:
		attrs(n.Attrs, f)
		Inspect(n.Name, f)
		params(n.Params, f)
		stmts(n.Body, f)
		Inspect(n.Expr, f)
	case *ClassDecl:
		attrs(n.Attrs, f)
		Inspect(n.Name, f)
		typeParams(n.TypeParams, f)
		namedTypes(n.Extends, f)
		namedTypes(n.Implements, f)
		Inspect(n.BackingType, f)
		for _, m := range n.Members {
			Inspect(m, f)
		}
		Inspect(n.Args, f)
	case *FunctionDecl:
		attrs(n.Attrs, f)
		Inspect(n.Name, f)
		typeParams(n.TypeParams, f)
		params(n.Params, f)
		Inspect(n.ReturnType, f)
		Inspect(n.Throws, f)
		stmts(n.Body, f)
	case *MethodDecl:
		attrs(n.Attrs, f)
		Inspect(n.Name, f)
		typeParams(n.TypeParams, f)
		params(n.Params, f)
		Inspect(n.ReturnType, f)
		Inspect(n.Throws, f)
		stmts(n.Body, f)
	case *Throws:
		namedTypes(n.Types, f)
	case *PropertyDecl:
		attrs(n.Attrs, f)
		Inspect(n.Type, f)
		for _, p := range n.Props {
			Inspect(p, f)
		}
		hooks(n.Hooks, f)
	case *PropertyItem:
		Inspect(n.Var, f)
		Inspect(n.Default, f)
	case *ClassConstDecl:
		attrs(n.Attrs, f)
		Inspect(n.Type, f)
		for _, c := range n.Consts {
			Inspect(c, f)
		}
	case *ConstItem:
		Inspect(n.Name, f)
		Inspect(n.Value, f)
	case *EnumCase:
		attrs(n.Attrs, f)
		Inspect(n.Name, f)
		Inspect(n.Value, f)
	case *TraitUse:
		names(n.Names, f)
		for _, a := range n.Adaptations {
			Inspect(a, f)
		}
	case *TraitAdaptation:
		Inspect(n.Trait, f)
		Inspect(n.Method, f)
		names(n.InsteadOf, f)
		Inspect(n.Alias, f)

	// ------------------------------------------------------------ statements
	case *ExprStmt:
		Inspect(n.Expr, f)
	case *VoidCastStmt:
		Inspect(n.Expr, f)
	case *Echo:
		exprs(n.Exprs, f)
	case *Block:
		stmts(n.Stmts, f)
	case *If:
		Inspect(n.Cond, f)
		Inspect(n.Then, f)
		for _, e := range n.ElseIfs {
			Inspect(e, f)
		}
		Inspect(n.Else, f)
	case *ElseIf:
		Inspect(n.Cond, f)
		Inspect(n.Body, f)
	case *While:
		Inspect(n.Cond, f)
		Inspect(n.Body, f)
	case *DoWhile:
		Inspect(n.Body, f)
		Inspect(n.Cond, f)
	case *For:
		exprs(n.Init, f)
		exprs(n.Cond, f)
		exprs(n.Step, f)
		Inspect(n.Body, f)
	case *Foreach:
		Inspect(n.Expr, f)
		Inspect(n.Key, f)
		Inspect(n.Value, f)
		Inspect(n.Body, f)
	case *Switch:
		Inspect(n.Subject, f)
		for _, c := range n.Cases {
			Inspect(c, f)
		}
	case *Case:
		Inspect(n.Cond, f)
		stmts(n.Body, f)
	case *Break:
		Inspect(n.Levels, f)
	case *Continue:
		Inspect(n.Levels, f)
	case *Return:
		Inspect(n.Expr, f)
	case *Global:
		exprs(n.Vars, f)
	case *StaticVars:
		for _, v := range n.Vars {
			Inspect(v, f)
		}
	case *StaticVar:
		Inspect(n.Var, f)
		Inspect(n.Default, f)
	case *Unset:
		exprs(n.Vars, f)
	case *Try:
		stmts(n.Body, f)
		for _, c := range n.Catches {
			Inspect(c, f)
		}
		stmts(n.Finally, f)
	case *Catch:
		names(n.Types, f)
		Inspect(n.Var, f)
		stmts(n.Body, f)
	case *Goto:
		Inspect(n.Label, f)
	case *Label:
		Inspect(n.Name, f)
	case *Declare:
		for _, d := range n.Directives {
			Inspect(d, f)
		}
		Inspect(n.Body, f)
	case *Namespace:
		Inspect(n.Name, f)
		stmts(n.Body, f)
	case *Use:
		Inspect(n.Prefix, f)
		for _, it := range n.Items {
			Inspect(it, f)
		}
	case *UseItem:
		Inspect(n.Name, f)
		Inspect(n.Alias, f)
	case *ConstDecl:
		attrs(n.Attrs, f)
		for _, c := range n.Consts {
			Inspect(c, f)
		}
	case *TypeAlias:
		attrs(n.Attrs, f)
		Inspect(n.Name, f)
		typeParams(n.TypeParams, f)
		Inspect(n.Type, f)

	default:
		panic("ast.Inspect: unknown node type")
	}
}

// InspectScope is Inspect confined to one variable scope, for the passes
// that ask which variables of a scope some code reads or writes. A class
// declared under n, named or anonymous, has none of them: its attributes,
// members, property defaults and method bodies are not visited, except an
// anonymous class's constructor arguments, which are evaluated where the
// class is instantiated — `new class ($a) { public int $b = 1; }` reads $a
// and has no $b. f is not called for the class itself. Closures and arrow
// functions are left to f: whether their bodies belong to the scope is the
// pass's rule.
func InspectScope(n Node, f func(Node) bool) {
	var visit func(Node) bool
	visit = func(m Node) bool {
		if c, ok := m.(*ClassDecl); ok {
			Inspect(c.Args, visit)
			return false
		}
		return f(m)
	}
	Inspect(n, visit)
}

// isNil reports whether n is nil or an interface holding a typed nil pointer
// (an optional child such as Arg.Name or New.Anon that was not written).
func isNil(n Node) bool {
	if n == nil {
		return true
	}
	v := reflect.ValueOf(n)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

func stmts(ss []Stmt, f func(Node) bool) {
	for _, s := range ss {
		Inspect(s, f)
	}
}

func exprs(es []Expr, f func(Node) bool) {
	for _, e := range es {
		Inspect(e, f)
	}
}

func types(ts []TypeExpr, f func(Node) bool) {
	for _, t := range ts {
		Inspect(t, f)
	}
}

func names(ns []*Name, f func(Node) bool) {
	for _, n := range ns {
		Inspect(n, f)
	}
}

func namedTypes(ts []*NamedType, f func(Node) bool) {
	for _, t := range ts {
		Inspect(t, f)
	}
}

func attrs(gs []*AttrGroup, f func(Node) bool) {
	for _, g := range gs {
		Inspect(g, f)
	}
}

func params(ps []*Param, f func(Node) bool) {
	for _, p := range ps {
		Inspect(p, f)
	}
}

func typeParams(ps []*TypeParam, f func(Node) bool) {
	for _, p := range ps {
		Inspect(p, f)
	}
}

func hooks(hs []*PropertyHook, f func(Node) bool) {
	for _, h := range hs {
		Inspect(h, f)
	}
}
