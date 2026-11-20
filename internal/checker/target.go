package checker

import (
	"fmt"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
)

// target is the PHP version the program compiles for
// (design/php-versions.md §2).
func (c *Checker) target() phpversion.Version { return c.p.Target() }

// needsPHP reports a construct PHP v added that the target lacks and the
// emitter does not lower, with one P1002: "<what> needs PHP 8.6 (target
// 8.5)" plus an optional hint. It reports nothing and returns false when
// the target has v.
func (c *Checker) needsPHP(span ast.Span, v phpversion.Version, what, hint string) bool {
	t := c.target()
	if t.AtLeast(v) {
		return false
	}
	msg := fmt.Sprintf("%s needs PHP %s (target %s)", what, v, t)
	if hint != "" {
		msg += ": " + hint
	}
	c.report(span, P1002, "%s", msg)
	return true
}

// settleUnrecorded refuses for PHP 8.5, once every file is checked, the
// partial applications whose callee the checker could not type, and so
// made no record of (program.Partial) for the lowering to write the
// closure from: a value whose type gives no parameter list (`callable`,
// a Closure without a signature, a string, mixed), a class known only
// when the call runs (`$name::m(...)` with $name a string). The build
// refuses each, and each is given a record that says why, so that the
// two say the same. One the checker reported an error in, an unknown
// function or method, a placeholder PHP refuses, is left to that error.
func (c *Checker) settleUnrecorded() {
	if c.target().AtLeast(phpversion.PHP86) {
		return
	}
	for _, f := range c.p.CheckedFiles() {
		if f.AST == nil {
			continue
		}
		var erred [][2]int
		for _, d := range c.diags {
			if d.File == f.Path && d.Severity == Error {
				erred = append(erred, [2]int{d.Line, d.Col})
			}
		}
		ast.Inspect(f.AST, func(n ast.Node) bool {
			var args *ast.ArgList
			why := ""
			switch n := n.(type) {
			case *ast.Call:
				args = n.Args
				why = c.untypedCallee(f, n)
			case *ast.MethodCall:
				args = n.Args
				why = "the method's parameters are not known, which the closure written for PHP 8.5 is written from: give the receiver a type that declares the method"
				if _, ident := n.Name.(*ast.Ident); !ident {
					why = "the method is named only when the call runs, and with it the parameters the closure written for PHP 8.5 is written from: name it, as `$obj->m(...)`"
				}
			case *ast.StaticCall:
				args = n.Args
				switch _, ident := n.Name.(*ast.Ident); {
				case !ident:
					why = "the method is named only when the call runs, and with it the parameters the closure written for PHP 8.5 is written from: name it, as `Base::m(...)`"
				default:
					why = "the class is known only when the call runs, and with it the parameters the closure written for PHP 8.5 is written from: name the class, or give the value a type that names it, as `class-string<Base>`"
					if _, named := n.Class.(*ast.Name); named {
						why = "the method's parameters are not known, which the closure written for PHP 8.5 is written from: declare the method"
					}
				}
			}
			if !args.Partial() || f.Partials[args] != nil {
				return true
			}
			span := n.Span()
			// An error reported in the call, an unknown callee, a
			// placeholder PHP refuses, is its one finding.
			fromLine, fromCol := f.Position(span.Start)
			toLine, toCol := f.Position(span.End)
			for _, at := range erred {
				after := at[0] > fromLine || at[0] == fromLine && at[1] >= fromCol
				before := at[0] < toLine || at[0] == toLine && at[1] < toCol
				if after && before {
					return true
				}
			}
			if f.Partials == nil {
				f.Partials = map[*ast.ArgList]*program.Partial{}
			}
			f.Partials[args] = &program.Partial{Refused: why}
			c.file = f
			c.needsPHP(span, phpversion.PHP86, "this partial application", why+", or target PHP 8.6")
			return true
		})
	}
}

// untypedCallee says why a partial application of a callee that is not a
// function's name has no record, with advice that, followed, gives one:
// a string or an array names a function or a method, which is written
// itself; a value's type is given its parameters.
func (c *Checker) untypedCallee(f *program.File, call *ast.Call) string {
	const from = "which the closure written for PHP 8.5 is written from"
	args := ""
	if call.Args != nil {
		args = string(f.Src[call.Args.S.Start:call.Args.S.End])
	}
	switch callee := unparen(call.Callee).(type) {
	case *ast.Name:
		return "the function's parameters are not known, " + from + ": declare the function"
	case *ast.StringLit:
		name := program.StringValue(callee)
		if cls, m, ok := strings.Cut(name, "::"); ok {
			return fmt.Sprintf("a string names the method, whose parameters are not known from it, %s: call the method by its name, `%s::%s%s`", from, cls, m, args)
		}
		return fmt.Sprintf("a string names the function, whose parameters are not known from it, %s: call the function by its name, `%s%s`", from, name, args)
	case *ast.ArrayLit:
		if len(callee.Items) == 2 && callee.Items[0] != nil && callee.Items[1] != nil {
			if m, ok := callee.Items[1].Value.(*ast.StringLit); ok {
				recv := callee.Items[0].Value
				text := string(f.Src[recv.Span().Start:recv.Span().End])
				if s, ok := recv.(*ast.StringLit); ok {
					return fmt.Sprintf("an array names the method, whose parameters are not known from it, %s: call the method itself, `%s::%s%s`", from, program.StringValue(s), program.StringValue(m), args)
				}
				return fmt.Sprintf("an array names the method, whose parameters are not known from it, %s: call the method itself, `%s->%s%s`", from, text, program.StringValue(m), args)
			}
		}
		return "an array names the callee, whose parameters are not known from it, " + from + ": call the function or the method itself"
	}
	return "the callee's type gives no parameter list, " + from + ": give it one, as `Closure(string): string` in a docblock"
}
