package emit

import (
	"bytes"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
)

// ScopeClass is the runtime class behind concurrently blocks and spawn.
const ScopeClass = "Phaenon\\Concurrent\\Scope"

// lowerConcurrency compiles the syntax of design/concurrency.md §6: a
// `concurrently { … }` block becomes `Scope::run(function () use (…) { … })`
// and `spawn expr` becomes `Scope::spawn(fn () => expr)`. The `use` clause
// is computed the way a person writes it: a variable of the enclosing scope
// the body reads is captured by value, one the body assigns and the
// enclosing scope mentions as well by reference. Every edit keeps its line
// count; nothing inside a body moves.
func (e *emitter) lowerConcurrency(f *ast.File) {
	if !hasConcurrency(f) {
		return
	}
	stmtForm := map[*ast.Concurrently]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if s, ok := n.(*ast.ExprStmt); ok {
			if c, ok := s.Expr.(*ast.Concurrently); ok {
				stmtForm[c] = true
			}
		}
		return true
	})
	l := &concurrencyLowering{e: e, stmtForm: stmtForm}
	l.scope(f, nil)
}

func hasConcurrency(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.Concurrently, *ast.Spawn:
			found = true
		}
		return !found
	})
	return found
}

type concurrencyLowering struct {
	e        *emitter
	stmtForm map[*ast.Concurrently]bool
	res      *program.Resolver
}

// scopeClass is the name Scope is called by in this file (D11).
func (l *concurrencyLowering) scopeClass(off int) string {
	if l.e.opts.Program == nil || l.e.opts.File == nil {
		return "\\" + ScopeClass
	}
	if l.res == nil {
		l.res = l.e.opts.Program.NewResolver(l.e.opts.File, l.e.opts.File.ScopeAt(off), nil, ast.Span{})
	}
	return l.e.importShort(ScopeClass, l.res)
}

// scope lowers the blocks directly inside one variable scope — a function,
// method, closure, arrow function, block body or the file — and recurses
// into the scopes nested in it. inherited names the variables the scope has
// from outside itself: an arrow function's enclosing variables, a block's
// captures.
func (l *concurrencyLowering) scope(owner ast.Node, inherited map[string]bool) {
	var blocks []ast.Node
	var nested []ast.Node
	eachDirect(owner, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.Concurrently, *ast.Spawn:
			blocks = append(blocks, n)
			return false
		case *ast.FunctionDecl, *ast.MethodDecl, *ast.Closure, *ast.ArrowFunc:
			nested = append(nested, n)
			return false
		case *ast.ClassDecl:
			// Methods are found through the class; a class has no variables.
			return true
		}
		return true
	})
	for _, n := range nested {
		if _, arrow := n.(*ast.ArrowFunc); arrow {
			// An arrow function sees its enclosing variables.
			l.scope(n, union(inherited, mentions([]ast.Node{owner}, ast.Span{})))
			continue
		}
		l.scope(n, nil)
	}
	for _, b := range blocks {
		// The options of a block are evaluated outside the closure; only the
		// body's variables are captured.
		var body ast.Span
		var roots []ast.Node
		switch b := b.(type) {
		case *ast.Concurrently:
			body = ast.Span{Start: b.Open.Start, End: b.Close.End}
			roots = stmtNodes(b.Body)
		case *ast.Spawn:
			body = ast.Span{Start: b.Kw.End, End: b.S.End}
			if b.Expr != nil {
				roots = []ast.Node{b.Expr}
			} else {
				roots = stmtNodes(b.Body)
			}
		}
		outside := union(inherited, mentions([]ast.Node{owner}, body))
		inside := mentions(roots, ast.Span{})
		captures := make(map[string]bool, len(inside))
		var names []string
		for name := range inside {
			if outside[name] {
				captures[name] = true
				names = append(names, name)
			}
		}
		sort.Slice(names, func(i, j int) bool { return inside[names[i]] < inside[names[j]] })
		switch b := b.(type) {
		case *ast.Concurrently:
			byRef := map[string]bool{}
			for _, v := range assignedIn(b.Body) {
				byRef[v.Name] = true
			}
			l.block(b, names, byRef)
		case *ast.Spawn:
			l.spawn(b, names)
		}
		l.scope(b, captures)
	}
}

// block rewrites `concurrently (opts) {` … `}` as
// `Scope::run(function () use (…) {` … `}, opts)`, a `;` added when the
// block is a statement.
func (l *concurrencyLowering) block(b *ast.Concurrently, names []string, byRef map[string]bool) {
	head := ast.Span{Start: b.Kw.Start, End: b.Open.End}
	text := l.scopeClass(b.Kw.Start) + "::run(function () " + useClause(names, byRef) + "{"
	l.e.replace(head, text+strings.Repeat("\n", bytes.Count(l.e.src[head.Start:head.End], []byte("\n"))))
	tail := "}"
	if b.Opts != nil {
		if opts := optionsText(l.e.src, b.Opts); opts != "" {
			tail += ", " + opts
		}
	}
	tail += ")"
	if l.stmtForm[b] && !followedBySemicolon(l.e.src, b.Close.End) {
		tail += ";"
	}
	l.e.replace(b.Close, tail)
}

// spawn rewrites `spawn expr` as `Scope::spawn(fn () => expr)` and
// `spawn {` … `}` as `Scope::spawn(function () use (…) {` … `})`.
func (l *concurrencyLowering) spawn(s *ast.Spawn, names []string) {
	if s.Expr != nil {
		l.e.replace(s.Kw, l.scopeClass(s.Kw.Start)+"::spawn(fn () =>")
		l.e.insert(s.Expr.Span().End, ")")
		return
	}
	head := ast.Span{Start: s.Kw.Start, End: s.Open.End}
	text := l.scopeClass(s.Kw.Start) + "::spawn(function () " + useClause(names, nil) + "{"
	l.e.replace(head, text+strings.Repeat("\n", bytes.Count(l.e.src[head.Start:head.End], []byte("\n"))))
	l.e.replace(s.Close, "})")
}

// useClause renders `use ($a, &$b) ` for the captured names, "" for none.
func useClause(names []string, byRef map[string]bool) string {
	if len(names) == 0 {
		return ""
	}
	parts := make([]string, len(names))
	for i, n := range names {
		if byRef[n] {
			parts[i] = "&$" + n
		} else {
			parts[i] = "$" + n
		}
	}
	return "use (" + strings.Join(parts, ", ") + ") "
}

// optionsText is the source of a block's options without their parentheses,
// whitespace runs collapsed so that the text fits on the closing line.
func optionsText(src []byte, opts *ast.ArgList) string {
	text := string(src[opts.S.Start:opts.S.End])
	text = strings.TrimSuffix(strings.TrimPrefix(text, "("), ")")
	return strings.Join(strings.Fields(text), " ")
}

// followedBySemicolon reports whether the next non-blank byte is `;`.
func followedBySemicolon(src []byte, off int) bool {
	for off < len(src) {
		switch src[off] {
		case ' ', '\t', '\r', '\n':
			off++
			continue
		case ';':
			return true
		}
		return false
	}
	return false
}

// eachDirect calls f for every node under owner (owner excluded) in source
// order, not descending into a node f returns false for.
func eachDirect(owner ast.Node, f func(ast.Node) bool) {
	ast.Inspect(owner, func(n ast.Node) bool {
		if n == owner {
			return true
		}
		return f(n)
	})
}

// mentions maps the variables the roots mention, outside the span skip, to
// their first offset. A nested function or closure is another scope and is
// not entered — a closure's use list is, since it reads the variables here.
// Arrow functions, blocks and spawns capture implicitly and are entered.
// `$this` and the superglobals are never captured.
func mentions(roots []ast.Node, skip ast.Span) map[string]int {
	out := map[string]int{}
	for _, root := range roots {
		ast.Inspect(root, func(n ast.Node) bool {
			if n != root && skip.End > skip.Start {
				if s := n.Span(); s.Start >= skip.Start && s.End <= skip.End {
					return false
				}
			}
			switch n := n.(type) {
			case *ast.Variable:
				if n.Name != "this" && !superglobal(n.Name) {
					if _, seen := out[n.Name]; !seen {
						out[n.Name] = n.S.Start
					}
				}
			case *ast.Closure:
				if n != root {
					for _, u := range n.Uses {
						if _, seen := out[u.Var.Name]; !seen {
							out[u.Var.Name] = u.Var.S.Start
						}
					}
					return false
				}
			case *ast.FunctionDecl, *ast.MethodDecl, *ast.ClassDecl:
				if n != root {
					return false
				}
			}
			return true
		})
	}
	return out
}

func stmtNodes(stmts []ast.Stmt) []ast.Node {
	out := make([]ast.Node, len(stmts))
	for i, s := range stmts {
		out[i] = s
	}
	return out
}

func superglobal(name string) bool {
	switch name {
	case "GLOBALS", "_SERVER", "_GET", "_POST", "_FILES", "_COOKIE", "_SESSION", "_REQUEST", "_ENV":
		return true
	}
	return false
}

// assignedIn lists the variables a block body writes: assignment targets
// (destructuring and element writes included), `++`/`--`, foreach keys and
// values, catch variables. Nested functions and classes are other scopes.
func assignedIn(body []ast.Stmt) []*ast.Variable {
	var out []*ast.Variable
	var target func(e ast.Expr)
	target = func(e ast.Expr) {
		switch e := e.(type) {
		case *ast.Variable:
			out = append(out, e)
		case *ast.Index:
			target(e.Array)
		case *ast.ArrayLit:
			for _, it := range e.Items {
				if it != nil {
					target(it.Value)
				}
			}
		}
	}
	for _, s := range body {
		ast.Inspect(s, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Assign:
				target(n.Target)
			case *ast.IncDec:
				target(n.Expr)
			case *ast.Foreach:
				target(n.Key)
				target(n.Value)
			case *ast.Catch:
				if n.Var != nil {
					out = append(out, n.Var)
				}
			case *ast.Closure, *ast.ArrowFunc, *ast.ClassDecl, *ast.FunctionDecl, *ast.Spawn:
				return false
			}
			return true
		})
	}
	return out
}

func union(a map[string]bool, b map[string]int) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}
