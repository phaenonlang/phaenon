package checker

import (
	"fmt"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
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
// (a parameter, a return type, a property): PHP resolves them when a value
// is checked against the type, so on an older PHP every call fails.
func (x *fnCtx) libraryTypes(t ast.TypeExpr) {
	switch t := t.(type) {
	case *ast.NamedType:
		if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified {
			if _, builtin := keywordArity[strings.ToLower(t.Name.Parts[0])]; builtin {
				return
			}
			if x.res.TypeParam(t.Name.Parts[0]) != nil || x.res.FileAlias(t.Name.Parts[0]) != nil {
				return
			}
		}
		if cl := x.p().Class(x.res.ClassName(t.Name)); cl != nil {
			x.c.libraryClass(t.Name.S, cl)
		}
		for _, a := range t.TypeArgs {
			x.libraryTypes(a)
		}
	case *ast.NullableType:
		x.libraryTypes(t.Type)
	case *ast.UnionType:
		for _, m := range t.Types {
			x.libraryTypes(m)
		}
	case *ast.IntersectionType:
		for _, m := range t.Types {
			x.libraryTypes(m)
		}
	}
}
