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
	"github.com/phaenonlang/phaenon/internal/tabs"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Options tune the checker.
type Options struct {
	// Deprecations reports calls to @deprecated declarations (P4006).
	Deprecations bool
	// ReflectedAttributes is phaenon.json's unused.reflectedAttributes: when
	// not nil, a private property escapes P2406–P2408 only through an
	// attribute whose class is one of these or extends or implements one.
	// Nil keeps the default, any attribute but PHP's own; an empty list lets
	// none (design/tooling.md §9.2).
	ReflectedAttributes []string
	// Observe, when set, is called with the type given to each expression
	// typed in a checked file, every time it is typed (a loop body, an
	// argument inferred in two passes): the last call holds the type the
	// checker settled on. phn migrate compares a file's types before and
	// after a rewrite with it.
	Observe func(f *program.File, e ast.Expr, t types.Type)
	// Elementless, when set, is called with each `array` or `iterable` name
	// that a declaration's native type leaves without an element type and
	// P2516 reports: one with no docblock type of its own that takes none
	// from the method it overrides. phn migrate writes `<mixed>` after it
	// (design/collections.md §5).
	Elementless func(f *program.File, t *ast.NamedType)
	// ByRef, when set, is called with each argument passed by reference and
	// the type the callee writes back: nil when it keeps the argument's
	// type (php-src's sort without an @param-out reads and writes it).
	// phn migrate's list-types reads a property's values from them.
	ByRef func(f *program.File, e ast.Expr, out types.Type)
	// CallableArg, when set, is called with each argument and the type of
	// the parameter it is passed to, once the call's templates are known:
	// phn migrate's list-types reads what a method referred to as a
	// callable receives (`array_map([$this, 'm'], $xs)`).
	CallableArg func(f *program.File, e ast.Expr, param types.Type)
	// Returned, when set, is called with each return statement that
	// returns a value and the type it gives its function: the value's, or
	// the unnamed `@var` above it. phn migrate's list-types reads a
	// method's returns from it.
	Returned func(f *program.File, s *ast.Return, t types.Type)
	// Thrown, when set, is called with each function or method declaration
	// of a checked file (*ast.FunctionDecl, *ast.MethodDecl) and the throw
	// points that leave its body (design/tooling.md §9.1), before the
	// body's declaration is held to them. phn migrate's throws codemod
	// reads what a body throws from them.
	Thrown func(f *program.File, decl ast.Node, points []ThrowPoint)
	// Assume, when set, is asked for each expression typed, each
	// assignment's target and each parameter bound in a checked file: a
	// type it returns replaces the checker's — the value read, the type
	// the target holds, the parameter's type in the body.
	// phn migrate's list-types reads the values of a declaration that
	// depend on it — a recursive call, a property filtered into itself —
	// with its own reads assumed empty, then checks the type they give
	// (design/tooling.md §8.2).
	Assume func(f *program.File, n ast.Node) types.Type
	// Cancelled, when set, is asked before each file: once it reports true,
	// Check stops and returns what it found so far. phn lsp cancels the
	// check of a whole project that a newer save makes stale.
	Cancelled func() bool
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
	// Related are the declarations the finding is about ("declared here"):
	// the parameter an argument does not fit, the method an override
	// breaks. phn lsp shows them beside the finding.
	Related []Related
	key     string // the dedupe key, so a rollback can forget the diagnostic
}

// Related is a position a finding refers to, in any file of the program
// (a stub's included).
type Related struct {
	File            string
	Line, Col       int
	EndLine, EndCol int
	Message         string
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
	// guards are the version guards around the code being checked
	// (guards.go).
	guards []libraryGuard
	file   *program.File
	// callSeq numbers call sites so a callee's type parameters get fresh
	// identities per call (T of the callee is not T of the caller).
	callSeq int
	// ignored lists, per file, the lines silenced by @phpstan-ignore /
	// @phaenon-ignore comments.
	ignored map[string]map[int]bool
	// recvs, dynNames and byRefArgs are what the unused-member pass reads
	// (unused.go): the receiver type of each member access in a class body,
	// the type of each dynamic member name, and the properties passed by
	// reference.
	recvs     map[ast.Node]types.Type
	dynNames  map[ast.Node]types.Type
	byRefArgs map[ast.Node]bool
	// refVars are the variables passed by reference, which a try's catch
	// blocks see bound (tryStmt).
	refVars map[*ast.Variable]bool
	// inits and initClasses are what the readonly pass reads (readonly.go):
	// the properties of $this each method body certainly assigns before it
	// returns, and the classes checked, in order.
	inits       map[*ast.MethodDecl]map[string]bool
	initClasses []*program.Class
	// testSeen holds the operand types of each `is` and `as` over the
	// passes that reached it (typetest.go); typeFindings says whether the
	// type it tests against drew a finding of its own (testType).
	testSeen     map[ast.Expr]testSeen
	typeFindings map[ast.TypeExpr]bool
}

// New creates a checker for the program.
func New(p *program.Program, opts Options) *Checker {
	c := &Checker{p: p, opts: opts, seen: map[string]bool{}, ignored: map[string]map[int]bool{}}
	p.ConstType = c.constType
	return c
}

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
		if c.opts.Cancelled != nil && c.opts.Cancelled() {
			break
		}
		c.file = f
		c.checkFile(f)
	}
	c.checkReadonlyInits()
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
	c.reportRelated(span, code, nil, format, args...)
}

// related is a declaration's position for a diagnostic's Related; nil
// without a file.
func related(f *program.File, span ast.Span, format string, args ...any) *Related {
	if f == nil {
		return nil
	}
	line, col := f.Position(span.Start)
	end := span.End
	if end > span.Start {
		end--
	}
	endLine, endCol := f.Position(end)
	return &Related{File: f.Path, Line: line, Col: col, EndLine: endLine, EndCol: endCol + 1, Message: fmt.Sprintf(format, args...)}
}

// reportRelated is report with the declaration the finding is about.
func (c *Checker) reportRelated(span ast.Span, code Code, rel *Related, format string, args ...any) {
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
	d := Diagnostic{File: c.file.Path, Line: line, Col: col, EndLine: endLine, EndCol: endCol + 1,
		Severity: code.Severity, Code: code.ID, Message: msg, key: key}
	if rel != nil {
		d.Related = []Related{*rel}
	}
	c.diags = append(c.diags, d)
}

// mark returns the number of diagnostics so far; rollback(mark) discards
// the ones reported since and forgets them, so a later pass over the same
// code — a loop's next iteration, the real evaluation after a lenient one —
// can report them again.
func (c *Checker) mark() int { return len(c.diags) }

func (c *Checker) rollback(mark int) {
	for _, d := range c.diags[mark:] {
		delete(c.seen, d.key)
	}
	c.diags = c.diags[:mark]
}

// ------------------------------------------------------------------ per file

// checkTabs reports a .phn's tabs outside strings and comments' text, once
// for the file: the remedy, phn fmt, is the file's too.
func (c *Checker) checkTabs(f *program.File) {
	offs := tabs.Find(f.Src)
	if len(offs) == 0 {
		return
	}
	where := "this line"
	if n := tabs.Lines(f.Src, offs); n > 1 {
		where = fmt.Sprintf("%d lines", n)
	}
	c.report(ast.Span{Start: offs[0], End: offs[0] + 1}, P1005,
		"a tab outside a string or a comment's text (%s): Phaenon code is indented with spaces, and phn fmt replaces the tabs", where)
}

func (c *Checker) checkFile(f *program.File) {
	phaenon := f.Mode == lexer.ModePhaenon
	c.ignored[f.Path] = ignoredLines(f.Src, f.Mode)
	if phaenon {
		c.checkTabs(f)
	}
	// Top-level code runs in a context without a class.
	scope := f.ScopeAt(0)
	ctx := c.newCtx(f, scope, nil, true)
	env := newEnv()
	var uses []*ast.Use
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
				c.useAliases(s)
				if phaenon {
					uses = append(uses, s)
				}
			case *ast.ClassDecl:
				c.checkClassDecl(f, f.ScopeAt(s.S.Start), s)
			case *ast.FunctionDecl:
				c.checkFunctionDecl(f, f.ScopeAt(s.S.Start), s)
			case *ast.OpenTag, *ast.CloseTag, *ast.InlineHTML, *ast.Declare, *ast.ConstDecl, *ast.HaltCompiler:
				if cd, ok := s.(*ast.ConstDecl); ok {
					for _, k := range cd.Consts {
						c.constantName(k.Name.S, k.Name.Name)
						ctx.inConst = true
						ctx.expr(k.Value, env, nil)
						ctx.inConst = false
					}
				}
			default:
				ctx.stmt(s, env)
			}
		}
	}
	visit(f.AST.Stmts)
	c.partialSyntax(f)
	for _, u := range uses {
		c.checkUse(f, u)
	}
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
		// A backslash after the word continues a name the alias begins
		// (`ORM\Column` after `use Doctrine\ORM\Mapping as ORM`); one
		// before it makes the word another name's segment.
		okEnd := end == len(lower) || !isWordByte(lower[end]) || lower[end] == '\\'
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
	// certainOperands says the last binary comparison's operands have the
	// PHP types they were inferred with (no docblock refinement), so an
	// outcome the ranges decide is reportable.
	certainOperands bool
	c               *Checker
	file            *program.File
	class           *program.Class
	// bindThis, when set, is the object the next closure literal is
	// called with (`(fn () => $this->x)->call($o)`): child gives that
	// closure $this and its class's scope, then clears it.
	bindThis     *thisBinding
	static       bool // no $this
	inConst      bool // checking a constant expression: an attribute argument, a constant, a default
	constClosure bool // a closure inside a constant expression: static, capturing nothing (P2531)
	nameOnly     bool // resolving a class name PHP does not load: X::class, instanceof X, catch (X)
	inFinally    int  // depth of finally blocks around the statement being checked
	// throwFrames collect the throw points of the try blocks and the body
	// being checked, innermost last (design/tooling.md §9.1, throws.go);
	// resolvedCalls are the calls whose callee gave their point, and
	// throwOperand is the expression a throw is evaluating.
	throwFrames   [][]throwPoint
	resolvedCalls map[ast.Span]bool
	throwOperand  ast.Expr
	returnType    types.Type
	hasReturn     bool // a declared return type exists
	name          string
	generator     bool
	res           *program.Resolver
	byRef         map[string]bool
	arrow         bool
	// method/function being checked (for @phpstan-assert, by-ref params)
	method   *program.Method
	function *program.Function
	// thisType caches the $this type for the class.
	thisType types.Type
	// loopCond is set while a loop condition is evaluated: `while (true)`
	// is idiomatic there and not an always-true finding.
	loopCond bool
	// testedNames are the names an `is` or `as` being checked tests its
	// operand against, where a bare `list` is list<mixed> (testType).
	testedNames map[*ast.NamedType]bool
	// inScope lists the type parameters declared by the enclosing class
	// and function; any other parameter met in a callee's signature is
	// foreign and gets inferred at the call (as PHPStan does).
	inScope map[string]bool
	// returnTypes collects inferred return types of closures without a declaration.
	returnTypes []types.Type
	// yieldKeys and yieldValues collect what a generator closure yields:
	// its Generator<K, V, mixed, R> is inferred from them, as its return
	// type is from its returns.
	yieldKeys, yieldValues []types.Type
	// returnEnvs collects the environment at every return while
	// trackReturns is set: a closure with by-reference uses leaves them as
	// any of its exits does.
	returnEnvs   []*state
	trackReturns bool
	// trackInits records the properties of $this a method body assigns
	// (state.inits), for the readonly properties a constructor must
	// initialise (readonly.go).
	trackInits bool
	// sawReturnValue is set when a `return expr;` was seen.
	sawReturnValue bool
	// shortCircuit marks `$a?->b` expressions whose null, if any, comes from
	// $a alone: b itself is not nullable, so a member reached through them
	// is reached on a non-null object or not at all (PHP skips the chain).
	shortCircuit map[ast.Expr]bool
	// condValueType is the type of the last condition cond() evaluated
	// (the short ternary `c ?: d` needs it).
	condValueType types.Type
	// loop bookkeeping: environments at break/continue statements of the
	// innermost loop or switch.
	breakEnvs    []*state
	continueEnvs []*state
	loopDepth    int
	// deepJumps are `break N` / `continue N` (N > 1) still leaving the
	// constructs between them and their target: each loop or switch they
	// leave takes a level off, and the construct around the last one takes
	// them as its own break or continue.
	deepJumps []deepJump
	// throwEnvs collects the environment after every statement inside a
	// try body, for the catch blocks; trySeeds holds each try's state at
	// its body's first throw point (tryStmt).
	throwEnvs []*state
	trySeeds  map[*ast.Try]*state
	// argKnown holds a pipe's left side, typed before the stage it feeds
	// is checked as a call that takes it as an argument; pipeOperand is
	// that expression while its stage is checked, and pipeParam the
	// parameter it was bound to (design/php-versions.md §5.4).
	argKnown    map[ast.Expr]types.Type
	pipeOperand ast.Expr
	pipeParam   *program.Param
	pipeSig     *signature
	// pipeParamType is pipeParam's type as the call resolved it.
	pipeParamType types.Type
	// pipeLeft counts the pipes whose left side is being checked; while
	// it is above zero, the arguments bound to a parameter that is not by
	// reference are noted in byValue, for the fold of the pipe (pipeFolds).
	pipeLeft int
	byValue  map[*ast.Arg]bool
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

// specialClassName is "self", "static" or "parent" when the class name is
// written so (the resolver maps self and parent to class names, which the
// callers here must not confuse with a class named outright), else "".
func specialClassName(n *ast.Name) string {
	if len(n.Parts) != 1 || n.Kind != ast.NameUnqualified {
		return ""
	}
	switch l := strings.ToLower(n.Parts[0]); l {
	case "self", "static", "parent":
		return l
	}
	return ""
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
	cl := x.p().Class(name)
	if cl == nil {
		x.report(span, P2001, "unknown class %s", name)
		return name, false
	}
	if !x.nameOnly {
		x.c.libraryClass(span, cl)
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
		case types.Class, types.Static, types.This, types.Param:
			return m // static of a class-string<T> receiver is T
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
		if x.class.Kind == ast.KindTrait {
			if strings.EqualFold(decl.Name, x.class.Name) || decl.Kind == ast.KindTrait {
				return true // the trait's own members, or another trait's
			}
			// A trait's body runs in its users: `static::getContainer()` in a
			// trait of KernelTestCase subclasses is fine when every user is one.
			users := x.p().TraitUsers(x.class.Name)
			for _, u := range users {
				if !u.IsSubclassOf(decl.Name) && !decl.IsSubclassOf(u.Name) {
					return false
				}
			}
			return true
		}
		return x.class.IsSubclassOf(decl.Name) || decl.IsSubclassOf(x.class.Name)
	default:
		if x.class == nil {
			return false
		}
		if strings.EqualFold(x.class.Name, decl.Name) {
			return true
		}
		if x.class.Kind == ast.KindTrait {
			// A trait's body runs in its users' scope, their private
			// members included.
			if decl.Kind == ast.KindTrait {
				return true
			}
			for _, u := range x.p().TraitUsers(x.class.Name) {
				if strings.EqualFold(u.Name, decl.Name) {
					return true
				}
			}
		}
		return false
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
	savedDiags := 0
	if silent {
		savedDiags = c.mark()
	}
	ctx := c.newCtx(k.File, k.Scope, k.Class, true)
	ctx.inConst = true
	t := ctx.expr(k.Value, newEnv(), k.Type)
	if silent {
		c.rollback(savedDiags)
	}
	if k.Type != nil {
		// `const array M = [...]`: the value says more than the declaration.
		if t != nil && !types.IsMixed(t) && types.IsAcceptable(t, k.Type, ctx.p()) {
			return t
		}
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
