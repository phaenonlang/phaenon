package checker

import (
	"fmt"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
	"github.com/phaenonlang/phaenon/stdlib"
)

// The library across versions (design/php-versions.md §6): the embedded
// stubs are the newest supported release's, and stdlib's version table
// says which release added a symbol. A use of one the target lacks is
// P4011, "clamp() needs PHP 8.6 (target 8.5)", where an older checker
// would have said "unknown function". Only the stubs' declarations are
// judged: a project or vendor declaration of the same name (a polyfill)
// makes the symbol available on every target.

// needsLibrary reports a library symbol PHP v added when the target is
// older and no version guard around the use proves it (guards.go; key is
// the symbol as a guard names it). what names it as the message does
// ("clamp()", "class Time\Duration").
func (c *Checker) needsLibrary(span ast.Span, key string, v phpversion.Version, newer bool, what string) {
	if !newer {
		return
	}
	t := c.target()
	if t.AtLeast(v) || c.guarded(key, v) {
		return
	}
	c.report(span, P4011, "%s needs PHP %s (target %s)", what, v, t)
}

// libraryFunction judges a call or reference to a function.
func (c *Checker) libraryFunction(span ast.Span, fn *program.Function) {
	if fn == nil || !fn.File.IsStub() || fn.Polyfilled {
		return
	}
	v, ok := stdlib.FunctionSince(fn.Name)
	c.needsLibrary(span, "function "+strings.ToLower(fn.Name), v, ok, fn.Name+"()")
}

// libraryClass judges a use of a class name: new, static access,
// instanceof, catch, extends, implements, attributes, declared types.
func (c *Checker) libraryClass(span ast.Span, cl *program.Class) {
	if cl == nil || !cl.File.IsStub() {
		return
	}
	v, ok := stdlib.ClassSince(cl.Name)
	if ok && !c.target().AtLeast(v) && c.p.ClassPolyfilled(cl.Name) {
		return
	}
	c.needsLibrary(span, "class "+strings.ToLower(cl.Name), v, ok, kindWord(cl)+" "+cl.Name)
}

func kindWord(cl *program.Class) string {
	switch cl.Kind {
	case ast.KindInterface:
		return "interface"
	case ast.KindTrait:
		return "trait"
	case ast.KindEnum:
		return "enum"
	}
	return "class"
}

// libraryMethod judges a call to a method of a stub class.
func (c *Checker) libraryMethod(span ast.Span, m *program.Method) {
	if m == nil || m.Class == nil || !m.Class.File.IsStub() {
		return
	}
	v, ok := stdlib.MethodSince(m.Class.Name, m.Name)
	c.needsLibrary(span, "method "+strings.ToLower(m.Name), v, ok, fmt.Sprintf("method %s::%s()", m.Class.Name, m.Name))
}

// libraryConstant judges a global constant.
func (c *Checker) libraryConstant(span ast.Span, k *program.Constant) {
	if k == nil || k.Class != nil || !k.File.IsStub() || k.Polyfilled {
		return
	}
	v, ok := stdlib.ConstantSince(k.Name)
	c.needsLibrary(span, "const "+k.Name, v, ok, "constant "+k.Name)
}

// libraryClassConstant judges a class constant or an enum case of a stub
// class.
func (c *Checker) libraryClassConstant(span ast.Span, cl *program.Class, name string) {
	if cl == nil || !cl.File.IsStub() {
		return
	}
	v, ok := stdlib.ClassConstantSince(cl.Name, name)
	c.needsLibrary(span, "const "+cl.Name+"::"+name, v, ok, fmt.Sprintf("constant %s::%s", cl.Name, name))
}

// libraryParam judges an argument passed to a parameter a release added to
// an existing function or method (callee as a signature names it,
// "openssl_sign()" or "Class::method()").
func (c *Checker) libraryParam(span ast.Span, callee, param string) {
	callee = strings.TrimSuffix(callee, "()")
	v, ok := stdlib.ParamSince(callee, param)
	c.needsLibrary(span, "", v, ok, fmt.Sprintf("parameter $%s of %s()", param, callee))
}

// libraryTypes judges the class names of a type written in a declaration
// (a parameter, a return type, a property, a class constant): PHP resolves
// them when a value is checked against the type, so on an older PHP every
// call fails, and a class that does not exist at all is P2001, as PHPStan
// reports it (class.notFound). A type argument, a closure signature's
// members and a tuple's elements are named too, though erased.
func (x *fnCtx) libraryTypes(t ast.TypeExpr) { x.declaredTypes(t, false) }

// declaredTypes walks a declared type; erased says the position does not
// reach PHP as written but through the emitter (a type argument, a closure
// signature, a tuple), where a bare `Closure` is Phaenon's name for
// \Closure (design/checker.md §9).
func (x *fnCtx) declaredTypes(t ast.TypeExpr, erased bool) {
	switch t := t.(type) {
	case *ast.NamedType:
		for _, a := range t.TypeArgs {
			x.declaredTypes(a, true)
		}
		if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified {
			lower := strings.ToLower(t.Name.Parts[0])
			if _, builtin := keywordArity[lower]; builtin {
				if lower == "closure" && !erased {
					x.bareClosure(t.Name)
				}
				return
			}
			if x.res.TypeParam(t.Name.Parts[0]) != nil || x.res.FileAlias(t.Name.Parts[0]) != nil {
				return
			}
		}
		// What the name denotes: a class, or an alias a class docblock
		// declares or imports (@phpstan-type, @phpstan-import-type), self,
		// static.
		c, ok := x.res.Native(&ast.NamedType{S: t.S, Name: t.Name}).(types.Class)
		if !ok {
			return
		}
		cl := x.p().Class(c.Name)
		if cl == nil {
			x.report(t.Name.S, P2001, "unknown class %s", c.Name)
			return
		}
		x.c.libraryClass(t.Name.S, cl)
	case *ast.NullableType:
		x.declaredTypes(t.Type, erased)
	case *ast.UnionType:
		for _, m := range t.Types {
			x.declaredTypes(m, erased)
		}
	case *ast.IntersectionType:
		for _, m := range t.Types {
			x.declaredTypes(m, erased)
		}
	case *ast.ClosureType:
		for _, m := range t.Params {
			x.declaredTypes(m.Type, true)
		}
		x.declaredTypes(t.Return, true)
	case *ast.TupleType:
		for _, m := range t.Elems {
			x.declaredTypes(m, true)
		}
	}
}

// bareClosure judges `Closure` written where the type reaches PHP as
// written: in a namespace that does not import it, PHP resolves the name
// to a class of that namespace, so the type is a class that does not exist
// (`App\Closure`) and every value fails it. The checker reads a bare
// `Closure` as \Closure anywhere, which is what the emitter writes where
// it erases a type; here the code itself is wrong.
func (x *fnCtx) bareClosure(n *ast.Name) {
	name := x.res.ClassName(n)
	if strings.EqualFold(name, "Closure") || x.p().Class(name) != nil {
		return
	}
	x.report(n.S, P2001, "unknown class %s: write \\Closure, or import it with `use Closure;`", name)
}
