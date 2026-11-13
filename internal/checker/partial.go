package checker

import (
	"fmt"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Partial function application (PHP 8.6, design/php-versions.md §5): a call
// with placeholders creates a closure over the callee. The checker types it
// as Closure(…): R from the callee's declaration, reports at compile time
// what PHP throws when the closure is created, and records for the emitter
// how the closure's parameters are named and typed.

// pfaSlot is one parameter of the closure a partial application creates.
type pfaSlot struct {
	param    *program.Param // the callee's parameter it stands for
	name     string
	required bool
	variadic bool     // the callee's variadic parameter, kept variadic by `...`
	arg      *ast.Arg // the placeholder; nil for the parameters `...` adds
}

// pfaShape maps a partial application's arguments onto the callee.
type pfaShape struct {
	slots []pfaSlot
	// rest is set when `...` stands for the remaining parameters.
	rest bool
	// named is set when a named argument or placeholder is written.
	named bool
	// ok is false when the arguments do not fit the callee (the closure is
	// then typed as far as it can be, and not lowered).
	ok bool
}

// partialShape lays a partial application's arguments over the callee's
// parameters as PHP 8.6 does (zend_partial.c): positional placeholders
// first, then named ones in the order written, then — for `...` — every
// parameter left, with its own optionality, the variadic one last. A
// placeholder makes its parameter required. It reports what PHP throws when
// the closure is created: too many or too few arguments, a required
// parameter left out, a named placeholder for no parameter. A name that
// overwrites an argument or a placeholder before it is namedOverwrites',
// as in any call.
func (x *fnCtx) partialShape(sig *signature, args *ast.ArgList, span ast.Span) *pfaShape {
	shape := &pfaShape{ok: true}
	params := sig.params
	var variadic *program.Param
	if n := len(params); n > 0 && params[n-1].Variadic {
		variadic = params[n-1]
		params = params[:n-1]
	}
	required := 0
	for i, p := range params {
		if !p.HasDefault {
			required = i + 1
		}
	}
	covered := map[*program.Param]bool{}
	byName := func(name string) *program.Param {
		for _, p := range params {
			if p.Name == name {
				return p
			}
		}
		return nil
	}
	var positional, named []pfaSlot
	given, variadicSlots := 0, 0
	for _, a := range args.Args {
		switch {
		case a.Placeholder != nil && a.Placeholder.Variadic:
			shape.rest = true
		case a.Unpack:
			shape.ok = false // P1003, reported by the syntax pass
		case a.Name != nil:
			shape.named = true
			p := byName(a.Name.Name)
			if a.Placeholder == nil {
				if p != nil {
					covered[p] = true
				}
				continue // an unknown name is checkCall's P2010
			}
			if p == nil {
				x.report(a.S, P2010, "%s has no parameter $%s a named placeholder can stand for (a variadic one cannot be named)", sig.name, a.Name.Name)
				shape.ok = false
				continue
			}
			if covered[p] {
				shape.ok = false // namedOverwrites' P2010, as in any call
				continue
			}
			covered[p] = true
			named = append(named, pfaSlot{param: p, name: p.Name, required: true, arg: a})
		default:
			if shape.named {
				// A positional argument after a named one: PHP's compile
				// error (P2110, the syntax pass), so no shape.
				shape.ok = false
				return shape
			}
			i := given
			given++
			var p *program.Param
			switch {
			case i < len(params):
				p = params[i]
				covered[p] = true
			case variadic != nil:
				p = variadic
			default:
				continue // too many: reported below
			}
			if a.Placeholder == nil {
				continue
			}
			name := p.Name
			if p == variadic {
				// PHP names a placeholder that binds into the variadic
				// parameter after it, numbered from 0.
				name = fmt.Sprintf("%s%d", p.Name, variadicSlots)
				variadicSlots++
			}
			positional = append(positional, pfaSlot{param: p, name: name, required: true, arg: a})
		}
	}
	switch {
	case given > len(params) && variadic == nil:
		x.report(span, P2102, "partial application of %s expects at most %d argument%s, %d given", sig.name, len(params), plural(len(params)), given)
		shape.ok = false
	case shape.rest:
		// `...` stands for whatever is left, required or not.
	case !shape.named && given < required:
		limit := "at least"
		if len(params) <= required {
			limit = "exactly"
		}
		x.report(span, P2101, "partial application of %s expects %s %d argument%s, %d given", sig.name, limit, required, plural(required), given)
		shape.ok = false
	default:
		for i, p := range params[:required] {
			if !p.HasDefault && !covered[p] {
				x.report(span, P2101, "partial application of %s leaves argument #%d ($%s) out: name it, or add a placeholder or ...", sig.name, i+1, p.Name)
				shape.ok = false
			}
		}
	}
	shape.slots = append(positional, named...)
	if shape.rest {
		for _, p := range params {
			if !covered[p] {
				shape.slots = append(shape.slots, pfaSlot{param: p, name: p.Name, required: !p.HasDefault})
			}
		}
		if variadic != nil {
			shape.slots = append(shape.slots, pfaSlot{param: variadic, name: variadic.Name, variadic: true})
		}
	}
	return shape
}

// partialClosure is the Closure type of a partial application, its
// parameter types and its return as checkCall resolved them.
func partialClosure(shape *pfaShape, paramType func(*program.Param) types.Type, ret types.Type) types.Type {
	c := types.Callable{Closure: true, HasSig: true, Return: ret}
	if c.Return == nil {
		c.Return = types.TMixed
	}
	for _, s := range shape.slots {
		c.Params = append(c.Params, types.CallableParam{Name: s.name, Type: paramType(s.param), Optional: !s.required,
			Variadic: s.variadic, ByRef: s.param.ByRef})
	}
	return c
}

// ------------------------------------------------------------------ lowering for an 8.5 target

// recordPartial stores the emitter's account of a partial application and,
// for an 8.5 target, reports a form with no exact lowering (§5.3). inPipe
// says the application is a pipe's right side that PHP turns into a call;
// callee is the call node; typeOf gives a parameter's type as the call
// resolved it.
func (x *fnCtx) recordPartial(callee ast.Expr, args *ast.ArgList, sig *signature, slots []pfaSlot, rest, named, inPipe bool, typeOf func(*program.Param) types.Type) {
	rec := &program.Partial{}
	for _, s := range slots {
		pp := program.PartialParam{Name: s.name, Optional: !s.required, Variadic: s.variadic, ByRef: s.param.ByRef, Arg: s.arg}
		if s.param.NativeType != nil {
			if n, ok := types.Native(x.selfResolved(s.param.NativeType, sig)); ok {
				pp.Native = n
			}
		}
		if t := typeOf(s.param); t != nil && !types.Equal(t, types.Erase(t)) && !mentionsParam(t) && !mentionsBytesType(t) {
			// A type PHP cannot declare (list<int>, a literal, a generic
			// class) goes into the closure's docblock, for PHPStan.
			pp.Doc = types.PrintQualified(x.selfResolved(t, sig))
		}
		if pp.Optional && !pp.Variadic {
			if d, ok := x.writableDefault(s.param.Default); ok {
				pp.Default = d
			} else {
				pp.Unwritable = true
			}
		}
		rec.Params = append(rec.Params, pp)
	}
	rec.Pipe, rec.Rest, rec.Named = inPipe, rest, named
	_, rec.Void = sig.ret.(types.Void)
	rec.Refused = x.partialRefusal(callee, args, sig, rec, rest, named, inPipe)
	if x.c.file.Partials == nil {
		x.c.file.Partials = map[*ast.ArgList]*program.Partial{}
	}
	x.c.file.Partials[args] = rec
	if rec.Refused != "" {
		x.c.needsPHP(callee.Span(), phpversion.PHP86, "this partial application", rec.Refused+", or target PHP 8.6")
	}
}

// mentionsBytesType reports a type with bytes in it, which PHPStan does
// not know.
func mentionsBytesType(t types.Type) bool {
	found := false
	types.Walk(t, func(m types.Type) {
		if _, ok := m.(types.Bytes); ok {
			found = true
		}
	})
	return found
}

// selfResolved replaces self and static in a parameter type by the class
// they name, so that the type can be written outside it.
func (x *fnCtx) selfResolved(t types.Type, sig *signature) types.Type {
	if sig.decl == nil {
		return t
	}
	return types.MapDeep(t, func(m types.Type) types.Type {
		switch m.(type) {
		case types.Static, types.This:
			return types.NewClass(sig.decl.Name)
		}
		return m
	})
}

// writableDefault prints a parameter's default as PHP code that means the
// same in any file: a literal, or a global constant written fully
// qualified. Anything else — a class constant, `new`, php-src's UNKNOWN —
// is not copied.
func (x *fnCtx) writableDefault(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.IntLit, *ast.FloatLit, *ast.StringLit:
		src := x.sourceOf(e)
		return src, src != ""
	case *ast.Unary:
		if inner, ok := x.writableDefault(e.Expr); ok && (e.Op == token.Kind('-') || e.Op == token.Kind('+')) {
			if _, num := e.Expr.(*ast.IntLit); num {
				return string(rune(e.Op)) + inner, true
			}
			if _, num := e.Expr.(*ast.FloatLit); num {
				return string(rune(e.Op)) + inner, true
			}
		}
	case *ast.Name:
		name, ok := constName(e)
		switch {
		case !ok:
			return "", false
		case name == "null" || name == "true" || name == "false":
			return name, true
		case name == "unknown":
			return "", false
		}
		if c := x.p().Constant(strings.Join(e.Parts, "\\")); c != nil {
			return "\\" + c.Name, true
		}
	case *ast.ArrayLit:
		if len(e.Items) == 0 {
			return "[]", true
		}
		var parts []string
		for _, it := range e.Items {
			if it == nil || it.Unpack || it.ByRef {
				return "", false
			}
			v, ok := x.writableDefault(it.Value)
			if !ok {
				return "", false
			}
			if it.Key != nil {
				k, ok := x.writableDefault(it.Key)
				if !ok {
					return "", false
				}
				v = k + " => " + v
			}
			parts = append(parts, v)
		}
		return "[" + strings.Join(parts, ", ") + "]", true
	}
	return "", false
}

// sourceOf returns the text of a literal node. Literals of stubs and vendor
// files are read from their own file; the node does not say which, so the
// text is taken from the literal's value instead.
func (x *fnCtx) sourceOf(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.IntLit:
		return e.Raw
	case *ast.FloatLit:
		return e.Raw
	case *ast.StringLit:
		return e.Raw
	}
	return ""
}

// partialRefusal says why a partial application has no exact 8.5 lowering
// (§5.3), or "" when it has one.
func (x *fnCtx) partialRefusal(callee ast.Expr, args *ast.ArgList, sig *signature, rec *program.Partial, rest, named, inPipe bool) string {
	params := map[string]bool{}
	for _, p := range rec.Params {
		params[p.Name] = true
	}
	for _, a := range args.Args {
		if a.Placeholder != nil || a.Value == nil {
			continue
		}
		var p *program.Param
		if a.Name != nil {
			for _, cand := range sig.params {
				if cand.Name == a.Name.Name {
					p = cand
				}
			}
		}
		if p != nil && p.ByRef {
			return "it binds an argument by reference, which a closure written for PHP 8.5 cannot"
		}
		if inPipe {
			if writes(a.Value) {
				return "a bound argument writes to a variable, which the closure written for PHP 8.5 would do to its own copy"
			}
		} else if !stableArg(a.Value) {
			return "a bound argument is computed (a call, a property, an operator), which a closure written for PHP 8.5 would compute at every call: assign it to a variable first"
		}
		if name := clash(a.Value, params); name != "" {
			return fmt.Sprintf("a bound argument reads $%s, which the closure's parameter of that name would hide: rename the variable", name)
		}
	}
	// Positional by-reference bindings.
	given := 0
	for _, a := range args.Args {
		if a.Name != nil || a.Unpack || (a.Placeholder != nil && a.Placeholder.Variadic) {
			continue
		}
		if a.Placeholder == nil && given < len(sig.params) && sig.params[given].ByRef {
			return "it binds an argument by reference, which a closure written for PHP 8.5 cannot"
		}
		if a.Placeholder == nil && given >= len(sig.params) && len(sig.params) > 0 && sig.params[len(sig.params)-1].ByRef {
			return "it binds an argument by reference, which a closure written for PHP 8.5 cannot"
		}
		given++
	}
	if !inPipe {
		switch c := callee.(type) {
		case *ast.MethodCall:
			if !stableReceiver(c.Object) {
				return "its receiver is computed, which a closure written for PHP 8.5 would compute at every call: assign it to a variable first"
			}
			if name := clash(c.Object, params); name != "" {
				return fmt.Sprintf("its receiver $%s has the name of a parameter of the closure, which would hide it: rename the variable", name)
			}
		case *ast.Call:
			if _, isName := c.Callee.(*ast.Name); !isName {
				if v, isVar := c.Callee.(*ast.Variable); !isVar {
					return "the callee is computed, which a closure written for PHP 8.5 would compute at every call: assign it to a variable first"
				} else if params[v.Name] {
					return fmt.Sprintf("the callee $%s has the name of a parameter of the closure, which would hide it: rename the variable", v.Name)
				}
			}
		case *ast.StaticCall:
			if _, isName := c.Class.(*ast.Name); !isName {
				if !stableReceiver(c.Class) {
					return "its class is computed, which a closure written for PHP 8.5 would compute at every call: assign it to a variable first"
				}
			}
		}
	}
	if rest {
		variadic := false
		unwritable := false
		for _, p := range rec.Params {
			if p.Arg == nil {
				variadic = variadic || p.Variadic
				unwritable = unwritable || p.Unwritable
			}
		}
		if named && (variadic || unwritable) {
			return "`...` after a named argument would unpack after it, which PHP forbids"
		}
	}
	return ""
}

// stableArg reports a bound argument an arrow function may evaluate at each
// call with the result PHP 8.6 computes once: a literal, a constant, a
// variable (an arrow function captures it by value when it is created).
func stableArg(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Paren:
		return stableArg(e.Expr)
	case *ast.Variable:
		return true
	case *ast.IntLit, *ast.FloatLit, *ast.StringLit, *ast.Name, *ast.ConstFetch, *ast.MagicConst:
		return true
	case *ast.ClassConstFetch:
		_, named := e.Class.(*ast.Name)
		_, ident := e.Name.(*ast.Ident)
		return named && ident
	case *ast.Unary:
		return (e.Op == token.Kind('-') || e.Op == token.Kind('+')) && stableArg(e.Expr)
	case *ast.ArrayLit:
		for _, it := range e.Items {
			if it == nil {
				continue
			}
			if it.ByRef || (it.Key != nil && !stableArg(it.Key)) || !stableArg(it.Value) {
				return false
			}
		}
		return true
	}
	return false
}

// stableReceiver is a receiver an arrow function may evaluate at each call:
// a variable or $this.
func stableReceiver(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Variable:
		return true
	case *ast.Paren:
		return stableReceiver(e.Expr)
	}
	return false
}

// writes reports an expression that assigns: written inside a closure, the
// assignment would change the closure's copy of the variable. What an
// anonymous class's methods assign is theirs; its constructor arguments
// are the expression's (ast.InspectScope).
func writes(e ast.Expr) bool {
	found := false
	ast.InspectScope(e, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.Assign, *ast.IncDec, *ast.Yield, *ast.YieldFrom:
			found = true
		case *ast.Closure, *ast.ArrowFunc:
			return false
		}
		return !found
	})
	return found
}

// clash returns a variable the expression reads that has the name of one
// of the closure's parameters. An anonymous class's properties and method
// bodies read none: only its constructor arguments are the expression's.
func clash(e ast.Expr, params map[string]bool) string {
	name := ""
	ast.InspectScope(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Variable:
			if params[n.Name] && name == "" {
				name = n.Name
			}
		case *ast.Closure, *ast.ArrowFunc:
			return false
		}
		return name == ""
	})
	return name
}

// ------------------------------------------------------------------ syntax PHP checks at compile time

// partialSyntax reports what PHP 8.6's compiler rejects in argument lists
// with placeholders (P1003): a variadic placeholder that is not last or not
// alone, placeholders beside unpacking, a closure created from `new`, from
// a `?->` call or as an attribute's arguments. The parser accepts these as
// zendparse does. It also reports writes through a constant's object for a
// target before 8.6 (RFC const_object_property_write).
func (c *Checker) partialSyntax(f *program.File) {
	ast.Inspect(f.AST, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ArgList:
			c.argListSyntax(n)
		case *ast.New:
			if n.Args.Closure() {
				c.report(n.Args.S, P1003, "cannot create a closure from a new expression")
			}
		case *ast.MethodCall:
			if n.NullSafe && n.Args.Closure() {
				c.report(n.Args.S, P1003, "cannot combine ?-> with creating a closure")
			}
		case *ast.Attribute:
			if n.Args.Closure() {
				c.report(n.Args.S, P1003, "cannot create a closure as an attribute's arguments")
			}
		case *ast.Assign:
			c.constObjectWrite(n.Target)
		case *ast.IncDec:
			c.constObjectWrite(n.Expr)
		case *ast.Unset:
			for _, e := range n.Vars {
				c.constObjectWrite(e)
			}
		}
		return true
	})
}

func (c *Checker) argListSyntax(a *ast.ArgList) {
	if !a.Partial() {
		return
	}
	variadicAt, unpack, named := -1, false, false
	for i, arg := range a.Args {
		switch {
		case arg.Unpack:
			unpack = true
		case arg.Placeholder != nil && arg.Placeholder.Variadic:
			if variadicAt >= 0 {
				c.report(arg.S, P1003, "the variadic placeholder may only appear once")
				return
			}
			variadicAt = i
			continue
		case arg.Name != nil:
			named = true
		case named:
			c.report(arg.S, P2110, "positional argument after a named argument")
		}
		if variadicAt >= 0 {
			c.report(a.Args[variadicAt].S, P1003, "the variadic placeholder must be the last argument")
			return
		}
	}
	if unpack {
		c.report(a.S, P1003, "cannot combine partial application and unpacking")
	}
}

// constObjectWrite reports, for a target before 8.6, a write whose object is
// a constant: `C->x = 1`, `C[0]->x++`, `K::O->x .= …`.
func (c *Checker) constObjectWrite(target ast.Expr) {
	pf, ok := target.(*ast.PropertyFetch)
	if !ok {
		// `C->list[] = 1` writes through the property too.
		for {
			ix, isIndex := target.(*ast.Index)
			if !isIndex {
				break
			}
			target = ix.Array
		}
		if pf, ok = target.(*ast.PropertyFetch); !ok {
			return
		}
	}
	obj := pf.Object
	for {
		if ix, isIndex := obj.(*ast.Index); isIndex {
			obj = ix.Array
			continue
		}
		if inner, isProp := obj.(*ast.PropertyFetch); isProp {
			obj = inner.Object
			continue
		}
		break
	}
	switch obj.(type) {
	case *ast.Name, *ast.ConstFetch, *ast.ClassConstFetch:
		c.needsPHP(target.Span(), phpversion.PHP86, "writing a property through a constant", "")
	}
}
