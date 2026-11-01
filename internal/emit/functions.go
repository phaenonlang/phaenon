package emit

import (
	"bytes"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
)

// lowerFunctions compiles the syntax of design/functions.md, none of which
// needs type information: a `fun` head becomes the PSR-12 `function` head
// with `public` written out, a class constant or a property or promoted
// parameter with an asymmetric visibility and no main one gets its implied
// `public`, and a
// closure in a constant expression becomes the `static function` PHP 8.5
// accepts there. It runs after the generics lowering, so that a docblock that
// pass inserts at a declaration's start precedes the rewritten head.
func (e *emitter) lowerFunctions(f *ast.File) {
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FunctionDecl:
			if n.Fun {
				e.replace(n.Head, "function")
			}
		case *ast.MethodDecl:
			if n.Fun {
				e.methodHead(n)
			}
		case *ast.PropertyDecl:
			e.impliedPublic(n.Modifiers, n.Mods)
		case *ast.ClassConstDecl:
			if n.Modifiers&(ast.ModPublic|ast.ModProtected|ast.ModPrivate) == 0 {
				e.insert(n.Kw.Start, "public ")
			}
		case *ast.Param:
			e.impliedPublic(n.Modifiers, n.Mods)
			if n.Default != nil {
				e.constClosures(n.Default)
			}
		case *ast.Attribute:
			if n.Args != nil {
				for _, a := range n.Args.Args {
					if a.Value != nil {
						e.constClosures(a.Value)
					}
				}
			}
		case *ast.PropertyItem:
			if n.Default != nil {
				e.constClosures(n.Default)
			}
		case *ast.ConstItem:
			e.constClosures(n.Value)
		case *ast.EnumCase:
			if n.Value != nil {
				e.constClosures(n.Value)
			}
		case *ast.StaticVar:
			if n.Default != nil {
				e.constClosures(n.Default)
			}
		}
		return true
	})
}

// methodHead rewrites `[modifiers] fun` as `[modifiers] function` in PSR-12
// order with `public` written out. A head broken over lines or holding a
// comment keeps its modifiers as written and has only the keyword replaced,
// so the line count holds and nothing the programmer wrote is lost.
func (e *emitter) methodHead(n *ast.MethodDecl) {
	head := e.src[n.Head.Start:n.Head.End]
	if bytes.ContainsAny(head, "\n\r/#") {
		kw := ast.Span{Start: n.Head.End - len("fun"), End: n.Head.End}
		if n.Modifiers&(ast.ModPublic|ast.ModProtected|ast.ModPrivate) == 0 {
			e.replace(kw, "public function")
		} else {
			e.replace(kw, "function")
		}
		return
	}
	e.replace(n.Head, methodModifiers(n.Modifiers)+"function")
}

// methodModifiers renders a method's modifiers in PSR-12 order, `public`
// written out, each followed by a space.
func methodModifiers(m ast.Modifier) string {
	var parts []string
	if m&ast.ModAbstract != 0 {
		parts = append(parts, "abstract")
	}
	if m&ast.ModFinal != 0 {
		parts = append(parts, "final")
	}
	switch {
	case m&ast.ModProtected != 0:
		parts = append(parts, "protected")
	case m&ast.ModPrivate != 0:
		parts = append(parts, "private")
	default:
		parts = append(parts, "public")
	}
	if m&ast.ModStatic != 0 {
		parts = append(parts, "static")
	}
	return strings.Join(parts, " ") + " "
}

// impliedPublic writes the `public` a `private(set)` property or promoted
// parameter leaves implied (PHP 8.4 allows the omission; PSR-12 tooling does
// not): `private(set) int $x` → `public private(set) int $x`.
func (e *emitter) impliedPublic(m ast.Modifier, mods ast.Span) {
	if m&(ast.ModPublicSet|ast.ModProtectedSet|ast.ModPrivateSet) == 0 || m&(ast.ModPublic|ast.ModProtected|ast.ModPrivate) != 0 {
		return
	}
	e.insert(mods.Start, "public ")
}

// constClosures rewrites the closures of a constant expression — an
// attribute argument, a constant, a default — as the `static function`
// closures PHP accepts there: `fn (…) => expr` becomes
// `static function (…) { return expr; }` and a `function` closure gets
// `static`. The closures' own bodies are ordinary code and are left alone.
func (e *emitter) constClosures(root ast.Expr) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ArrowFunc:
			kw := "static function"
			if n.Static {
				kw = "function"
			}
			e.replace(n.Kw, kw)
			e.replace(n.Arrow, "{ return")
			e.insert(n.Body.Span().End, "; }")
			return false
		case *ast.Closure:
			if !n.Static {
				e.insert(n.Kw.Start, "static ")
			}
			return false
		case *ast.ClassDecl:
			return false
		}
		return true
	})
}
