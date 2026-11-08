package emit

import (
	"bytes"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
)

// lowerTarget writes what PHP 8.6 added in the PHP of an older target
// (design/php-versions.md §4, §5.3). For an 8.6 target there is nothing to
// do: partial applications and #[\Override] on constants are emitted as
// written. For 8.5:
//
//   - a pipe PHP evaluates exactly as the call it makes (the checker's
//     program.File.Folds) is written as that call, a run of them as nested
//     calls, the code the pipeline stands for (fold.go);
//   - any other partial application becomes an arrow function whose
//     parameters are named and typed after the callee's (the checker's
//     records in program.File.Partials), in a pipe one called at once; in
//     a constant expression it becomes the static closure PHP 8.5 accepts
//     there;
//   - #[\Override] on a class constant or an enum case is dropped: 8.5
//     rejects it at compile time, and the check it asks for is the
//     checker's.
//
// The checker has refused (P1002) every form that has no exact lowering, so
// the records seen here all lower. It runs after the other passes: a
// folded pipeline carries the edits made inside it.
func (e *emitter) lowerTarget(f *ast.File) error {
	if e.opts.Program.Target().AtLeast(phpversion.PHP86) {
		return nil
	}
	l := &targetLowering{e: e, bare: map[ast.Expr]bool{}, constExpr: map[ast.Expr]bool{}, pipes: map[ast.Expr]*ast.Binary{}, leftOf: map[ast.Expr]*ast.Binary{}}
	for _, s := range f.Stmts {
		if _, ok := s.(*ast.Namespace); ok {
			l.namespaced = true
		}
	}
	ast.Inspect(f, l.context)
	var err error
	var tops []*ast.Binary
	ast.Inspect(f, func(n ast.Node) bool {
		if err != nil {
			return false
		}
		switch n := n.(type) {
		case *ast.ClassConstDecl:
			l.dropOverride(n.Attrs)
		case *ast.EnumCase:
			l.dropOverride(n.Attrs)
		case *ast.Binary:
			switch {
			case n.Op != token.T_PIPE:
			case l.asCall(n):
				if outer := l.leftOf[n]; outer == nil || !l.asCall(outer) {
					tops = append(tops, n) // the last pipe of a run
				}
			default:
				if args := callArgs(n.Right); args.Partial() {
					err = l.pipe(n, args)
				}
			}
		case *ast.Call, *ast.MethodCall, *ast.StaticCall:
			call := n.(ast.Expr)
			if args := callArgs(call); args.Partial() && l.pipes[call] == nil {
				err = l.partial(call, args)
			}
		}
		return true
	})
	// Inner runs first: a run in a stage's argument moves with the stage.
	for i := len(tops) - 1; i >= 0 && err == nil; i-- {
		err = l.fold(tops[i])
	}
	return err
}

// folds reports a pipe the checker found PHP evaluates as the call it
// makes.
func (l *targetLowering) folds(p *ast.Binary) bool {
	return l.e.opts.File != nil && l.e.opts.File.Folds[p]
}

// asCall reports a pipe the lowering writes as the call it makes: one that
// folds, except a first-class callable after a pipe that stays a pipe,
// which PHP 8.5 runs as written — `A |> (fn($x) => …) |> f(...)` reads
// better so than as `f(A |> (fn($x) => …))`.
func (l *targetLowering) asCall(p *ast.Binary) bool {
	if !l.folds(p) {
		return false
	}
	if callArgs(p.Right).Partial() {
		return true
	}
	left, ok := p.Left.(*ast.Binary)
	return !ok || left.Op != token.T_PIPE || l.asCall(left)
}

type targetLowering struct {
	e *emitter
	// bare marks the expressions that stand where an arrow function needs
	// no parentheses: a whole argument, assigned value, returned value,
	// array element, statement, or already parenthesised.
	bare map[ast.Expr]bool
	// constExpr marks the partial applications inside constant expressions.
	constExpr map[ast.Expr]bool
	// pipes maps a pipe's right side to the pipe, leftOf its left side.
	pipes      map[ast.Expr]*ast.Binary
	leftOf     map[ast.Expr]*ast.Binary
	namespaced bool
}

func callArgs(e ast.Expr) *ast.ArgList {
	switch e := e.(type) {
	case *ast.Call:
		return e.Args
	case *ast.MethodCall:
		return e.Args
	case *ast.StaticCall:
		return e.Args
	}
	return nil
}

// context fills bare, constExpr and pipes.
func (l *targetLowering) context(n ast.Node) bool {
	markConst := func(root ast.Expr) {
		ast.Inspect(root, func(m ast.Node) bool {
			switch m := m.(type) {
			case *ast.Closure, *ast.ArrowFunc, *ast.ClassDecl:
				return false
			case *ast.Call, *ast.StaticCall:
				l.constExpr[m.(ast.Expr)] = true
			}
			return true
		})
	}
	switch n := n.(type) {
	case *ast.Arg:
		l.bare[n.Value] = true
	case *ast.Assign:
		l.bare[n.Value] = true
	case *ast.Return:
		l.bare[n.Expr] = true
	case *ast.ArrayItem:
		l.bare[n.Value] = true
	case *ast.ExprStmt:
		l.bare[n.Expr] = true
	case *ast.Paren:
		l.bare[n.Expr] = true
	case *ast.Echo:
		for _, x := range n.Exprs {
			l.bare[x] = true
		}
	case *ast.Binary:
		if n.Op == token.T_PIPE {
			l.pipes[n.Right] = n
			l.leftOf[n.Left] = n
		}
	case *ast.Param:
		if n.Default != nil {
			l.bare[n.Default] = true
			markConst(n.Default)
		}
	case *ast.Attribute:
		if n.Args != nil {
			for _, a := range n.Args.Args {
				if a.Value != nil {
					markConst(a.Value)
				}
			}
		}
	case *ast.PropertyItem:
		if n.Default != nil {
			l.bare[n.Default] = true
			markConst(n.Default)
		}
	case *ast.ConstItem:
		l.bare[n.Value] = true
		markConst(n.Value)
	case *ast.EnumCase:
		if n.Value != nil {
			markConst(n.Value)
		}
	case *ast.StaticVar:
		if n.Default != nil {
			l.bare[n.Default] = true
			markConst(n.Default)
		}
	}
	return true
}

func (l *targetLowering) record(args *ast.ArgList, at ast.Node) (*program.Partial, error) {
	if l.e.opts.File != nil {
		if rec := l.e.opts.File.Partials[args]; rec != nil {
			return rec, nil
		}
	}
	return nil, l.e.errorf(at, "this partial application needs PHP 8.6, or the checker's types to be lowered for the target 8.5")
}

// params writes a closure's parameter list from the record.
func params(rec *program.Partial, fallback bool) string {
	var parts []string
	rest := false
	for _, p := range rec.Params {
		if fallback && p.Arg == nil {
			if !rest {
				parts = append(parts, "mixed ...$args")
				rest = true
			}
			continue
		}
		var sb strings.Builder
		if p.Native != "" {
			sb.WriteString(p.Native)
			sb.WriteByte(' ')
		}
		if p.ByRef {
			sb.WriteByte('&')
		}
		if p.Variadic {
			sb.WriteString("...")
		}
		sb.WriteString("$" + p.Name)
		if p.Optional && !p.Variadic {
			sb.WriteString(" = " + p.Default)
		}
		parts = append(parts, sb.String())
	}
	return strings.Join(parts, ", ")
}

// fallback reports whether the parameters `...` stands for have a default
// no file can spell, so they are passed on as `mixed ...$args`.
func fallback(rec *program.Partial) bool {
	for _, p := range rec.Params {
		if p.Arg == nil && p.Unwritable {
			return true
		}
	}
	return false
}

// fillPlaceholders replaces each placeholder by the closure parameter it
// stands for, and `...` by the parameters it leaves.
func (l *targetLowering) fillPlaceholders(args *ast.ArgList, rec *program.Partial, fb bool) {
	for _, a := range args.Args {
		if a.Placeholder == nil {
			continue
		}
		if !a.Placeholder.Variadic {
			for _, p := range rec.Params {
				if p.Arg == a {
					l.e.replace(a.Placeholder.S, "$"+p.Name)
				}
			}
			continue
		}
		var list []string
		if fb {
			list = []string{"...$args"}
		} else {
			for _, p := range rec.Params {
				switch {
				case p.Arg != nil:
				case p.Variadic:
					list = append(list, "...$"+p.Name)
				case rec.Named:
					list = append(list, p.Name+": $"+p.Name)
				default:
					list = append(list, "$"+p.Name)
				}
			}
		}
		if len(list) > 0 {
			l.e.replace(a.Placeholder.S, strings.Join(list, ", "))
			continue
		}
		// Nothing left: drop `...` with the comma before it.
		comma := bytes.LastIndexByte(l.e.src[:a.Placeholder.S.Start], ',')
		span := ast.Span{Start: comma, End: a.Placeholder.S.End}
		l.e.replace(span, strings.Repeat("\n", bytes.Count(l.e.src[span.Start:span.End], []byte("\n"))))
	}
}

// partial lowers a partial application outside a pipe:
// `f(B, ?)` → `fn(T $p) => f(B, $p)`.
func (l *targetLowering) partial(call ast.Expr, args *ast.ArgList) error {
	rec, err := l.record(args, call)
	if err != nil {
		return err
	}
	if rec.Refused != "" {
		return l.e.errorf(call, "this partial application needs PHP 8.6 (target 8.5): %s", rec.Refused)
	}
	fb := fallback(rec)
	l.e.wrapClosure(call.Span(), closureParts(rec, fb, l.constExpr[call], captured(call, args)), !l.bare[call])
	l.fillPlaceholders(args, rec, fb)
	return nil
}

// pipe lowers `A |> f(B, ?, C)` that does not fold to
// `A |> (fn(T $p) => f(B, $p, C))`, whose closure runs at once, after A,
// as PHP 8.6 evaluates B and C.
func (l *targetLowering) pipe(p *ast.Binary, args *ast.ArgList) error {
	rec, err := l.record(args, p.Right)
	if err != nil {
		return err
	}
	if !rec.Pipe {
		// Several placeholders: PHP 8.6 creates the closure and calls it,
		// which the standalone lowering writes the same way.
		return l.partial(p.Right, args)
	}
	if rec.Refused != "" {
		return l.e.errorf(p.Right, "this partial application needs PHP 8.6 (target 8.5): %s", rec.Refused)
	}
	var placeholder *ast.Arg
	for _, a := range args.Args {
		if a.Placeholder != nil {
			placeholder = a
		}
	}
	l.e.wrapClosure(p.Right.Span(), closureParts(rec, false, false, captured(p.Right, args)), true)
	l.e.replace(placeholder.Placeholder.S, "$"+rec.Params[0].Name)
	return nil
}

// closure is the closure a lowered partial application wraps its call in.
type closure struct {
	head  string   // `fn(T $p) => `, or `function (T $p) use ($v) {`
	block bool     // head opens a block, whose statement is the call
	vars  []string // the block's inline @var docblocks, before the statement
	ret   bool     // the statement returns the call's value
}

// closureParts is the closure of a lowered partial application: `fn(T $p)
// => ` before the call, as a rule. A parameter whose type says more than
// PHP declares (list<int>) needs an inline @var, which PHPStan reads inside
// a function body and nowhere on a closure, so the closure is then
// `function (T $p) use ($v) {` with the @var and `return` the call in its
// block, capturing by value what the arrow function would. In a constant
// expression it is the static closure PHP 8.5 accepts there.
func closureParts(rec *program.Partial, fb, constExpr bool, uses []string) closure {
	var vars []string
	for _, p := range rec.Params {
		if p.Doc != "" && !p.Variadic && !(fb && p.Arg == nil) {
			vars = append(vars, "/** @var "+p.Doc+" $"+p.Name+" */")
		}
	}
	if len(vars) == 0 && !constExpr {
		return closure{head: "fn(" + params(rec, fb) + ") => "}
	}
	kw := "function"
	if constExpr {
		kw = "static function"
		uses = nil
	}
	head := kw + " (" + params(rec, fb) + ")"
	if len(uses) > 0 {
		head += " use ($" + strings.Join(uses, ", $") + ")"
	}
	return closure{head: head + " {", block: true, vars: vars, ret: !rec.Void}
}

// wrapClosure writes c around the call at span, in parentheses when paren
// says so. An arrow function goes in place; a block is laid out as PER
// Coding Style §7 has a closure: the brace on the closure's line, the
// docblocks and the statement one level in on lines of their own, the
// closing brace on the next line, at the indentation of the closure's.
// A call that spans lines moves in with the statement.
func (e *emitter) wrapClosure(span ast.Span, c closure, paren bool) {
	open, end := "", ""
	if paren {
		open, end = "(", ")"
	}
	if !c.block {
		e.insert(span.Start, open+c.head)
		if end != "" {
			e.insertEnd(span, end)
		}
		return
	}
	indent := e.outIndent(span.Start)
	level := "    "
	if strings.Contains(indent, "\t") {
		level = "\t"
	}
	var b strings.Builder
	b.WriteString(open + c.head + "\n")
	for _, v := range c.vars {
		b.WriteString(indent + level + v + "\n")
	}
	b.WriteString(indent + level)
	if c.ret {
		b.WriteString("return ")
	}
	e.insertFree(span.Start, b.String())
	e.insertEndFree(span, ";\n"+indent+"}"+end)
	if e.lineOf(span.Start) != e.lineOf(span.End) && !e.multiLineToken(span.Start, span.End) {
		// From the call's first character on: the closure's own lines are
		// laid out already (sourceRelative).
		e.indents = append(e.indents, indentReq{Span: ast.Span{Start: span.Start + 1, End: span.End}, Text: level})
	}
}

// captured lists the variables a lowered closure must capture: those the
// bound arguments, the receiver and a variable callee read, in order,
// without $this (bound by itself) and the superglobals (which PHP forbids
// in use()). An anonymous class in a bound argument contributes its
// constructor arguments only: its properties and method bodies are not
// variables here (ast.InspectScope).
func captured(call ast.Expr, args *ast.ArgList) []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if name == "this" || ast.IsSuperglobal(name) || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	var walk func(n ast.Node, shadowed map[string]bool)
	walk = func(root ast.Node, shadowed map[string]bool) {
		ast.InspectScope(root, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Variable:
				if !shadowed[n.Name] {
					add(n.Name)
				}
			case *ast.Closure:
				for _, u := range n.Uses {
					if !shadowed[u.Var.Name] {
						add(u.Var.Name)
					}
				}
				return false
			case *ast.ArrowFunc:
				inner := map[string]bool{}
				for k := range shadowed {
					inner[k] = true
				}
				for _, p := range n.Params {
					inner[p.Var.Name] = true
				}
				walk(n.Body, inner)
				return false
			}
			return true
		})
	}
	switch c := call.(type) {
	case *ast.MethodCall:
		walk(c.Object, nil)
	case *ast.StaticCall:
		if _, named := c.Class.(*ast.Name); !named {
			walk(c.Class, nil)
		}
	case *ast.Call:
		if _, named := c.Callee.(*ast.Name); !named {
			walk(c.Callee, nil)
		}
	}
	for _, a := range args.Args {
		if a.Value != nil {
			walk(a.Value, nil)
		}
	}
	return out
}

// editedWithin reports whether an edit already touches the span.
func (e *emitter) editedWithin(s ast.Span) bool {
	for _, ed := range e.edits {
		if ed.Start < s.End && ed.End > s.Start || ed.Start == ed.End && ed.Start > s.Start && ed.Start < s.End {
			return true
		}
	}
	return false
}

// dropOverride removes #[\Override] from a class constant or an enum case.
func (l *targetLowering) dropOverride(groups []*ast.AttrGroup) {
	for _, g := range groups {
		for i, a := range g.Attrs {
			if !l.isOverride(a) {
				continue
			}
			if len(g.Attrs) == 1 {
				l.removeGroup(g.S)
				continue
			}
			// One of several: drop it with its comma.
			span := a.S
			if i+1 < len(g.Attrs) {
				span.End = g.Attrs[i+1].S.Start
			} else {
				span.Start = g.Attrs[i-1].S.End
			}
			l.e.replace(span, strings.Repeat("\n", bytes.Count(l.e.src[span.Start:span.End], []byte("\n"))))
		}
	}
}

// removeGroup deletes an attribute group (dropLines).
func (l *targetLowering) removeGroup(s ast.Span) {
	l.e.drop = append(l.e.drop, s)
}

func (l *targetLowering) isOverride(a *ast.Attribute) bool {
	n := a.Name
	if len(n.Parts) != 1 || !strings.EqualFold(n.Parts[0], "Override") {
		if f := l.e.opts.File; f != nil {
			return strings.EqualFold(f.ScopeAt(a.S.Start).ResolveClass(n), "Override")
		}
		return false
	}
	switch {
	case n.Kind == ast.NameFullyQualified:
		return true
	case l.e.opts.File != nil:
		return strings.EqualFold(l.e.opts.File.ScopeAt(a.S.Start).ResolveClass(n), "Override")
	default:
		return n.Kind == ast.NameUnqualified && !l.namespaced
	}
}
