// Package checker is Phaenon's type checker: local, bidirectional,
// flow-sensitive, over the program model. See design/checker.md.
package checker

import (
	"fmt"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Options tune the checker.
type Options struct {
	// Deprecations reports calls to @deprecated declarations (P4006).
	Deprecations bool
}

// Diagnostic is one finding.
type Diagnostic struct {
	File     string
	Line     int
	Col      int
	EndLine  int
	EndCol   int
	Severity Severity
	Code     string
	Message  string
}

func (d Diagnostic) String() string {
	return fmt.Sprintf("%s:%d:%d: %s %s: %s", d.File, d.Line, d.Col, d.Severity, d.Code, d.Message)
}

// Checker checks the checked files of a program.
type Checker struct {
	p     *program.Program
	opts  Options
	diags []Diagnostic
	seen  map[string]bool
	file  *program.File
	// callSeq numbers call sites so a callee's type parameters get fresh
	// identities per call (T of the callee is not T of the caller).
	callSeq int
	// ignored lists, per file, the lines silenced by @phpstan-ignore /
	// @phaenon-ignore comments.
	ignored map[string]map[int]bool
	// reified records, per call site whose callee has reified type
	// parameters, the type each one ended up bound to (class parameters
	// first, then the callee's own) — the emitter turns them into the
	// leading runtime arguments.
	reified map[ast.Node][]types.Type
}

// New creates a checker for the program.
func New(p *program.Program, opts Options) *Checker {
	c := &Checker{p: p, opts: opts, seen: map[string]bool{}, ignored: map[string]map[int]bool{}, reified: map[ast.Node][]types.Type{}}
	p.ConstType = c.constType
	return c
}

// ReifiedArgs returns the types bound to the reified type parameters of
// the callee of a call site (a Call, MethodCall, StaticCall or New node),
// or nil when the callee has none.
func (c *Checker) ReifiedArgs(n ast.Node) []types.Type { return c.reified[n] }

// ignoredLines finds the lines silenced by ignore comments: `// @phpstan-ignore …`
// (or `@phaenon-ignore`) at the end of a line silences that line, on its
// own line the next one; `@phpstan-ignore-line` always silences its own
// line and `@phpstan-ignore-next-line` the next.
func ignoredLines(src []byte, mode lexer.Mode) map[int]bool {
	out := map[int]bool{}
	toks := lexer.Tokenize(src, mode)
	for i, t := range toks {
		if t.Kind != token.T_COMMENT && t.Kind != token.T_DOC_COMMENT {
			continue
		}
		text := t.Text(src)
		if !strings.Contains(text, "@phpstan-ignore") && !strings.Contains(text, "@phaenon-ignore") {
			continue
		}
		sameLine := strings.Contains(text, "-ignore-line")
		if !sameLine && !strings.Contains(text, "-ignore-next-line") {
			// Code before the comment on the same line makes it a trailing comment.
			for j := i - 1; j >= 0; j-- {
				if toks[j].Kind.IsTrivia() {
					continue
				}
				sameLine = toks[j].Line == t.Line
				break
			}
		}
		if sameLine {
			out[t.Line] = true
			continue
		}
		for j := i + 1; j < len(toks); j++ {
			if !toks[j].Kind.IsTrivia() {
				out[toks[j].Line] = true
				break
			}
		}
	}
	return out
}

// Check analyses every checked file and returns the diagnostics sorted by
// file and position.
func (c *Checker) Check() []Diagnostic {
	for _, f := range c.p.CheckedFiles() {
		c.file = f
		c.checkFile(f)
	}
	for _, pr := range c.p.Problems {
		if pr.File == nil || !pr.File.Checked {
			continue
		}
		c.file = pr.File
		code := P4002
		if strings.HasPrefix(pr.Msg, "docblock @") {
			code = P4001
		}
		c.report(pr.Span, code, "%s", pr.Msg)
	}
	sort.SliceStable(c.diags, func(i, j int) bool {
		a, b := c.diags[i], c.diags[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Col < b.Col
	})
	return c.diags
}

// report records a diagnostic at a span of the current file.
func (c *Checker) report(span ast.Span, code Code, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	line, col := c.file.Position(span.Start)
	if c.ignored[c.file.Path][line] {
		return
	}
	end := span.End
	if end > span.Start {
		end--
	}
	endLine, endCol := c.file.Position(end)
	key := fmt.Sprintf("%s:%d:%d:%s:%s", c.file.Path, line, col, code.ID, msg)
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.diags = append(c.diags, Diagnostic{File: c.file.Path, Line: line, Col: col, EndLine: endLine, EndCol: endCol + 1,
		Severity: code.Severity, Code: code.ID, Message: msg})
}

// ------------------------------------------------------------------ per file

func (c *Checker) checkFile(f *program.File) {
	phaenon := f.Mode == lexer.ModePhaenon
	c.ignored[f.Path] = ignoredLines(f.Src, f.Mode)
	// Top-level code runs in a context without a class.
	scope := f.ScopeAt(0)
	ctx := c.newCtx(f, scope, nil, true)
	env := newEnv()
	var visit func(stmts []ast.Stmt)
	visit = func(stmts []ast.Stmt) {
		for _, s := range stmts {
			switch s := s.(type) {
			case *ast.Namespace:
				ctx = c.newCtx(f, f.ScopeAt(s.S.Start+1), nil, true)
				if s.Braced {
					visit(s.Body)
				}
			case *ast.Use:
				if phaenon {
					c.checkUse(f, s)
				}
			case *ast.ClassDecl:
				c.checkClassDecl(f, f.ScopeAt(s.S.Start), s)
			case *ast.FunctionDecl:
				c.checkFunctionDecl(f, f.ScopeAt(s.S.Start), s)
			case *ast.OpenTag, *ast.CloseTag, *ast.InlineHTML, *ast.Declare, *ast.ConstDecl, *ast.HaltCompiler:
				if cd, ok := s.(*ast.ConstDecl); ok {
					for _, k := range cd.Consts {
						ctx.expr(k.Value, env, nil)
					}
				}
			default:
				ctx.stmt(s, env)
			}
		}
	}
	visit(f.AST.Stmts)
}

// checkUse reports unused imports (P2013) — only when nothing in the file
// mentions the alias. Cheap text scan: the alias as a word outside the use
// statement itself.
func (c *Checker) checkUse(f *program.File, u *ast.Use) {
	src := string(f.Src)
	for _, it := range u.Items {
		alias := it.Name.Parts[len(it.Name.Parts)-1]
		if it.Alias != nil {
			alias = it.Alias.Name
		}
		before := src[:it.S.Start]
		after := src[it.S.End:]
		if !mentions(before, alias) && !mentions(after, alias) {
			c.report(it.S, P2013, "import %s is not used", strings.Join(it.Name.Parts, "\\"))
		}
	}
}

func mentions(s, word string) bool {
	lower := strings.ToLower(s)
	w := strings.ToLower(word)
	i := 0
	for {
		j := strings.Index(lower[i:], w)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(w)
		okStart := start == 0 || !isWordByte(lower[start-1])
		okEnd := end == len(lower) || !isWordByte(lower[end])
		if okStart && okEnd {
			return true
		}
		i = start + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '\\' || b >= 0x80
}

// ------------------------------------------------------------------ function context

// fnCtx is the context of one function-like body.
type fnCtx struct {
	c          *Checker
	file       *program.File
	class      *program.Class
	static     bool // no $this
	returnType types.Type
	hasReturn  bool // a declared return type exists
	name       string
	generator  bool
	res        *program.Resolver
	byRef      map[string]bool
	arrow      bool
	// method/function being checked (for @phpstan-assert, by-ref params)
	method   *program.Method
	function *program.Function
	// thisType caches the $this type for the class.
	thisType types.Type
	// loopCond is set while a loop condition is evaluated: `while (true)`
	// is idiomatic there and not an always-true finding.
	loopCond bool
	// inScope lists the type parameters declared by the enclosing class
	// and function; any other parameter met in a callee's signature is
	// foreign and gets inferred at the call (as PHPStan does).
	inScope map[string]bool
	// returnTypes collects inferred return types of closures without a declaration.
	returnTypes []types.Type
	// sawReturnValue is set when a `return expr;` was seen.
	sawReturnValue bool
	// condValueType is the type of the last condition cond() evaluated
	// (the short ternary `c ?: d` needs it).
	condValueType types.Type
	// loop bookkeeping: environments at break/continue statements of the
	// innermost loop or switch.
	breakEnvs    []*state
	continueEnvs []*state
	loopDepth    int
	// throwEnvs collects the environment after every statement inside a
	// try body, for the catch blocks.
	throwEnvs []*state
}

func (c *Checker) newCtx(f *program.File, scope *program.Scope, class *program.Class, static bool) *fnCtx {
	x := &fnCtx{c: c, file: f, class: class, static: static, byRef: map[string]bool{}, inScope: map[string]bool{}}
	x.res = c.p.NewResolver(f, scope, class, ast.Span{})
	if class != nil {
		x.thisType = types.This{Of: &types.Class{Name: class.Name, Args: selfArgs(class)}}
		for _, tp := range class.TypeParams {
			x.inScope[tp.Name] = true
		}
	}
	return x
}

func selfArgs(c *program.Class) []types.Type {
	if len(c.TypeParams) == 0 {
		return nil
	}
	out := make([]types.Type, len(c.TypeParams))
	for i, tp := range c.TypeParams {
		out[i] = types.Param{Name: tp.Name, Bound: tp.Bound, Owner: c.Name}
	}
	return out
}

func (x *fnCtx) report(span ast.Span, code Code, format string, args ...any) {
	x.c.report(span, code, format, args...)
}

func (x *fnCtx) p() *program.Program { return x.c.p }

// resolveClassName resolves a class name written in code, reporting unknown
// classes; "static"/"self"/"parent" map to the current class.
func (x *fnCtx) resolveClassName(n *ast.Name, span ast.Span) (string, bool) {
	name := x.res.ClassName(n)
	switch name {
	case "self", "static":
		if x.class == nil {
			x.report(span, P2012, "%s used outside a class", name)
			return "", false
		}
		return x.class.Name, true
	case "parent":
		if x.class == nil {
			x.report(span, P2012, "parent used outside a class")
			return "", false
		}
		x.report(span, P2011, "class %s has no parent", x.class.Name)
		return "", false
	}
	if x.p().Class(name) == nil {
		x.report(span, P2001, "unknown class %s", name)
		return name, false
	}
	return name, true
}

// classRefType returns the type denoted by a class name in `new`,
// `instanceof`, `Foo::` positions: static for `static`, the class otherwise.
func (x *fnCtx) classRefType(n *ast.Name, span ast.Span) (types.Type, *program.Class) {
	if tp := x.typeParamRef(n); tp != nil {
		t, cl, _ := x.paramValue(tp, span, "use as a class")
		return t, cl
	}
	raw := x.res.ClassName(n)
	if raw == "static" && x.class != nil {
		return types.Static{Of: &types.Class{Name: x.class.Name, Args: selfArgs(x.class)}}, x.class
	}
	name, ok := x.resolveClassName(n, span)
	if !ok {
		if name == "" {
			return types.TObject, nil
		}
		return types.Class{Name: name}, nil
	}
	cl := x.p().Class(name)
	if cl == x.class && x.class != nil && raw == "self" {
		return types.Class{Name: cl.Name, Args: selfArgs(cl)}, cl
	}
	return types.Class{Name: cl.Name}, cl
}

// classesOf returns the classes a type's object members denote, loading
// them; unknown names are reported once.
func (x *fnCtx) classesOf(t types.Type, span ast.Span) []*program.Class {
	var out []*program.Class
	for _, name := range types.ClassNames(t) {
		cl := x.p().Class(name)
		if cl == nil {
			x.report(span, P2001, "unknown class %s", name)
			continue
		}
		out = append(out, cl)
	}
	return out
}

// receiverArgs returns the type arguments of t as seen by class decl
// (walking up the hierarchy from the receiver's class).
func (x *fnCtx) memberSubst(recv types.Type, decl *program.Class) map[string]types.Type {
	for _, m := range types.Members(recv) {
		var cls types.Class
		switch m := m.(type) {
		case types.Class:
			cls = m
		case types.Static:
			if m.Of != nil {
				cls = *m.Of
			}
		case types.This:
			if m.Of != nil {
				cls = *m.Of
			}
		case types.Intersection:
			for _, im := range m.Types {
				if c, ok := im.(types.Class); ok {
					cls = c
					break
				}
			}
		default:
			continue
		}
		if cls.Name == "" {
			continue
		}
		from := x.p().Class(cls.Name)
		if from == nil {
			continue
		}
		if args, ok := from.SuperArgs(decl.Name, cls.Args); ok {
			return decl.Subst(args)
		}
	}
	return decl.Subst(nil)
}

// bindReceiver substitutes `static`, `$this` and the declaring class's type
// parameters in a member type for a receiver.
func (x *fnCtx) bindReceiver(t types.Type, recv types.Type, decl *program.Class) types.Type {
	if t == nil {
		return nil
	}
	t = types.Substitute(t, x.memberSubst(recv, decl))
	return replaceStatic(t, recv)
}

// replaceStatic maps static/$this in t to the receiver type.
func replaceStatic(t types.Type, recv types.Type) types.Type {
	switch tt := t.(type) {
	case types.Static:
		return staticOf(recv)
	case types.This:
		return staticOf(recv)
	case types.Union:
		return types.Map(tt, func(m types.Type) types.Type { return replaceStatic(m, recv) })
	case types.Array:
		return types.Array{Key: replaceStatic(tt.Key, recv), Value: replaceStatic(tt.Value, recv), List: tt.List, NonEmpty: tt.NonEmpty}
	case types.Iterable:
		return types.Iterable{Key: replaceStatic(tt.Key, recv), Value: replaceStatic(tt.Value, recv)}
	case types.ClassString:
		if tt.Of == nil {
			return tt
		}
		return types.ClassString{Of: replaceStatic(tt.Of, recv)}
	case types.Class:
		if len(tt.Args) == 0 {
			return tt
		}
		args := make([]types.Type, len(tt.Args))
		for i, a := range tt.Args {
			args[i] = replaceStatic(a, recv)
		}
		return types.Class{Name: tt.Name, Args: args}
	case types.Callable:
		if !tt.HasSig {
			return tt
		}
		ps := make([]types.CallableParam, len(tt.Params))
		for i, p := range tt.Params {
			p.Type = replaceStatic(p.Type, recv)
			ps[i] = p
		}
		return types.Callable{Params: ps, Return: replaceStatic(tt.Return, recv), Closure: tt.Closure, Pure: tt.Pure, HasSig: true}
	}
	return t
}

// staticOf is the type `static` denotes for a receiver: the receiver's own
// static-ness is kept ($this stays $this, static stays static, a class
// becomes that class).
func staticOf(recv types.Type) types.Type {
	return types.Map(types.StripNull(recv), func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Class, types.Static, types.This:
			return m
		case types.EnumCase:
			return types.Class{Name: m.Class}
		case types.ClassString:
			if m.Of != nil {
				return m.Of
			}
			return types.TObject
		case types.StringLit:
			return types.Class{Name: m.V}
		}
		return types.TObject
	})
}

// canAccess checks member visibility from the current class.
func (x *fnCtx) canAccess(vis program.Visibility, decl *program.Class) bool {
	switch vis {
	case program.Public:
		return true
	case program.Protected:
		if x.class == nil {
			return false
		}
		return x.class.IsSubclassOf(decl.Name) || decl.IsSubclassOf(x.class.Name)
	default:
		return x.class != nil && strings.EqualFold(x.class.Name, decl.Name)
	}
}

// ------------------------------------------------------------------ constants

// constType evaluates a constant's value expression (installed as
// program.ConstType).
func (c *Checker) constType(k *program.Constant) types.Type {
	if k.Value == nil || k.File == nil {
		return types.TMixed
	}
	saved := c.file
	c.file = k.File
	defer func() { c.file = saved }()
	// Diagnostics inside vendor constants are not reported: suppress by
	// checking silently.
	silent := !k.File.Checked
	var savedDiags []Diagnostic
	if silent {
		savedDiags = c.diags
	}
	ctx := c.newCtx(k.File, k.Scope, k.Class, true)
	t := ctx.expr(k.Value, newEnv(), k.Type)
	if silent {
		c.diags = savedDiags
	}
	if k.Type != nil {
		return k.Type
	}
	return t
}

// ------------------------------------------------------------------ helpers

func (x *fnCtx) typeName(t types.Type) string { return types.Print(t) }

// declaredReturn returns the return type to check against, nil when none.
func (x *fnCtx) describeCallee() string { return x.name }

// constName returns the lower-cased name of a bare constant expression
// (the parser leaves constants as names).
func constName(e ast.Expr) (string, bool) {
	var n *ast.Name
	switch e := e.(type) {
	case *ast.Name:
		n = e
	case *ast.ConstFetch:
		n = e.Name
	case *ast.Paren:
		return constName(e.Expr)
	default:
		return "", false
	}
	if len(n.Parts) != 1 || n.Kind == ast.NameQualified {
		return "", false
	}
	return strings.ToLower(n.Parts[0]), true
}

func isNullConst(e ast.Expr) bool {
	n, ok := constName(e)
	return ok && n == "null"
}

// inInitialiser reports whether the body being checked may initialise
// readonly properties: a constructor or __clone of the class.
func (x *fnCtx) inInitialiser() bool {
	if x.method == nil {
		return false
	}
	switch strings.ToLower(x.method.Name) {
	case "__construct", "__clone":
		return true
	}
	return false
}

// identName returns the identifier of a member name expression, if static.
func identName(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name, true
	case *ast.StringLit:
		return program.StringValue(e), true
	}
	return "", false
}
