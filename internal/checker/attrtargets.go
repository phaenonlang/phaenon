package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
)

// The flags of #[Attribute(...)]: the declarations an attribute class
// may stand on, in the order PHP lists them, and whether it may stand on
// one twice.
const (
	targetClass = 1 << iota
	targetFunction
	targetMethod
	targetProperty
	targetClassConstant
	targetParameter
	targetConstant
	attrRepeatable
	targetAll = attrRepeatable - 1
)

var attributeFlagNames = map[string]int{
	"TARGET_CLASS": targetClass, "TARGET_FUNCTION": targetFunction, "TARGET_METHOD": targetMethod,
	"TARGET_PROPERTY": targetProperty, "TARGET_CLASS_CONSTANT": targetClassConstant,
	"TARGET_PARAMETER": targetParameter, "TARGET_CONSTANT": targetConstant, "TARGET_ALL": targetAll,
	"IS_REPEATABLE": attrRepeatable,
}

var targetNames = []string{"class", "function", "method", "property", "class constant", "parameter", "constant"}

// ownAttributes reports an attribute of PHP's own where PHP's compiler
// refuses it (P2533): on a declaration its flags leave out, `Attribute
// "Deprecated" cannot target parameter`, or a second time on one
// declaration when it is not repeatable, `Attribute "Override" must not
// be repeated`. PHP checks these flags when it compiles the file for the
// attribute classes it declares itself, the ones the stubs declare with
// #[Attribute(...)], whose flags are read from there, php-src's test
// extensions aside; a user's attribute class is checked only when
// ReflectionAttribute::newInstance() runs, and is not judged here.
// #[\DelayedTargetValidation] beside them puts off the check of the
// targets, not that of the repetition, to that call. A promoted parameter
// is a parameter and a property, and either target admits it; a closure
// is a function, a property hook a method and an enum case a class
// constant, as PHP has them.
func (c *Checker) ownAttributes(f *program.File) {
	ast.Inspect(f.AST, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ClassDecl:
			c.attributeTargets(f, n.Attrs, targetClass, "class")
		case *ast.FunctionDecl:
			c.attributeTargets(f, n.Attrs, targetFunction, "function")
		case *ast.Closure:
			c.attributeTargets(f, n.Attrs, targetFunction, "function")
		case *ast.ArrowFunc:
			c.attributeTargets(f, n.Attrs, targetFunction, "function")
		case *ast.MethodDecl:
			c.attributeTargets(f, n.Attrs, targetMethod, "method")
		case *ast.PropertyHook:
			c.attributeTargets(f, n.Attrs, targetMethod, "method")
		case *ast.PropertyDecl:
			c.attributeTargets(f, n.Attrs, targetProperty, "property")
		case *ast.Param:
			target := targetParameter
			if n.Modifiers != 0 {
				target |= targetProperty // promoted
			}
			c.attributeTargets(f, n.Attrs, target, "parameter")
		case *ast.ClassConstDecl:
			c.attributeTargets(f, n.Attrs, targetClassConstant, "class constant")
		case *ast.EnumCase:
			c.attributeTargets(f, n.Attrs, targetClassConstant, "class constant")
		case *ast.ConstDecl:
			c.attributeTargets(f, n.Attrs, targetConstant, "constant")
		}
		return true
	})
}

// attributeTargets judges the attributes of one declaration, whose target
// is what PHP calls it in its message. Each attribute class is reported
// once: the target first, as PHP checks it first.
func (c *Checker) attributeTargets(f *program.File, groups []*ast.AttrGroup, target int, what string) {
	type use struct {
		a     *ast.Attribute
		class *program.Class
		flags int
	}
	var uses []use
	delayed := false
	for _, g := range groups {
		for _, a := range g.Attrs {
			res := c.p.NewResolver(f, f.ScopeAt(a.S.Start), nil, ast.Span{})
			ac := c.p.Class(res.ClassName(a.Name))
			if ac == nil {
				continue
			}
			flags, own := c.attributeFlags(ac)
			if !own {
				continue
			}
			if strings.EqualFold(ac.Name, "DelayedTargetValidation") {
				delayed = true
			}
			uses = append(uses, use{a, ac, flags})
		}
	}
	seen, reported := map[*program.Class]bool{}, map[*program.Class]bool{}
	for _, u := range uses {
		switch {
		case reported[u.class]:
		case !delayed && u.flags&target == 0:
			reported[u.class] = true
			var allowed []string
			for i, name := range targetNames {
				if u.flags&(1<<i) != 0 {
					allowed = append(allowed, name)
				}
			}
			c.report(u.a.S, P2533, "#[\\%s] cannot target a %s (allowed targets: %s): PHP refuses it when it compiles the file", u.class.Name, what, strings.Join(allowed, ", "))
		case seen[u.class] && u.flags&attrRepeatable == 0:
			reported[u.class] = true
			c.report(u.a.S, P2533, "#[\\%s] must not be repeated: PHP refuses a second one on a declaration when it compiles the file", u.class.Name)
		}
		seen[u.class] = true
	}
}

// attributeFlags reads the flags of an attribute class the stubs declare,
// from its #[Attribute(...)]; own is false for any other class, and for
// those of php-src's test extensions, which a php runs without
// (zend_test's ZendTestAttribute, …): to the code they are unknown names.
func (c *Checker) attributeFlags(cl *program.Class) (flags int, own bool) {
	if !cl.File.IsStub() || testExtensionStub(cl.File.Path) {
		return 0, false
	}
	if f, ok := c.attrFlags[cl]; ok {
		return f, f >= 0
	}
	flags = -1
	for _, g := range cl.Attrs {
		for _, a := range g.Attrs {
			if len(a.Name.Parts) != 1 || !strings.EqualFold(a.Name.Parts[0], "Attribute") {
				continue
			}
			flags = targetAll
			if a.Args != nil && len(a.Args.Args) > 0 {
				if v, ok := attributeFlagsOf(a.Args.Args[0].Value); ok {
					flags = v
				}
			}
		}
	}
	if c.attrFlags == nil {
		c.attrFlags = map[*program.Class]int{}
	}
	c.attrFlags[cl] = flags
	return flags, flags >= 0
}

// testExtensionStub reports a stub of php-src's test extensions, which
// the stubs embed beside the rest (stdlib/gen) and no php is built with
// for use.
func testExtensionStub(path string) bool {
	for _, ext := range []string{"/ext/zend_test/", "/ext/dl_test/", "/ext/skeleton/"} {
		if strings.Contains(path, ext) {
			return true
		}
	}
	return false
}

// attributeFlagsOf evaluates Attribute::TARGET_METHOD|Attribute::IS_REPEATABLE,
// the form the stubs write the flags in.
func attributeFlagsOf(e ast.Expr) (int, bool) {
	switch e := e.(type) {
	case *ast.Paren:
		return attributeFlagsOf(e.Expr)
	case *ast.Binary:
		if e.Op != token.Kind('|') {
			return 0, false
		}
		l, lok := attributeFlagsOf(e.Left)
		r, rok := attributeFlagsOf(e.Right)
		return l | r, lok && rok
	case *ast.ClassConstFetch:
		n, isName := e.Class.(*ast.Name)
		id, isIdent := e.Name.(*ast.Ident)
		if isName && isIdent && strings.EqualFold(n.Parts[len(n.Parts)-1], "Attribute") {
			v, ok := attributeFlagNames[id.Name]
			return v, ok
		}
	}
	return 0, false
}
