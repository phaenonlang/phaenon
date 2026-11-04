package migrate

// The pipes codemod (design/tooling.md §8.1, decision D20).
//
// A chain of nested calls — a call, a call in one of its arguments, a call
// in one of that one's — becomes a pipeline of PHP 8.5's |> with PHP 8.6's
// partial application, which the compiler lowers for an 8.5 target
// (design/php-versions.md §5.3):
//
//	$emails = array_values(array_filter(array_map(fn (User $u) => $u->email, $users), fn (?string $e) => $e !== null));
//	// →
//	$emails = $users
//	    |> array_map(fn (User $u) => $u->email, ?)
//	    |> array_filter(?, fn (?string $e) => $e !== null)
//	    |> array_values(...);
//
// A chain is followed through the argument that makes it longest. Its head
// is the innermost call's subject when that call has exactly one argument
// other than literals, constants and closures, otherwise the innermost call
// itself. A stage that takes only the piped value is a first-class
// callable; any other one a partial application with `?`, or `name: ?`,
// where the piped value stood. A pipeline follows the data: the piped value
// goes into a stage's first parameter, or into the data parameter of PHP's
// functions that take it last (dataLast: array_map, implode, explode,
// preg_*, str_replace, in_array …); `sprintf('%d', count($x))` and
// `self::assertSame($expected, f($x))` stay calls.
//
// The rewrite is exact or it is not made. A pipeline evaluates the piped
// value before a stage's other arguments and before a method's receiver, so
// every argument written before the nested call must be a literal, a
// constant, a variable or a closure, and the inner calls must not assign a
// variable read there (evalorder); a method stage's receiver must be $this,
// such a variable, or a property of either that its class declares
// without hooks and the inner calls do not assign — a method they run is
// taken not to reassign it. The piped parameter must not be by reference;
// nothing is spread; the callee must resolve through the program model,
// which says which parameter is by reference.
// A link of the chain that breaks one of these splits it: the part above
// takes the call below the break as its head, the part below stands as an
// argument, and each part of at least the minimum length is rewritten.
// Chains in constant expressions and string interpolation, and chains with
// a scope-reading call (compact(), extract(), func_get_args() …) are left
// whole.
//
// Conservative where design/tooling.md §8.1 says nothing:
//   - a pipeline replaces a whole value — a statement, an assignment's right
//     side, a return, an argument, an array element, an arrow function's
//     body, a condition — never an operand: the outer call is left out;
//   - a nullsafe call, a dynamic callee and a call with explicit type
//     arguments are no stages, nor is a call with another call beside the
//     nested one (which of them is the data is not the codemod's choice);
//   - for an 8.5 target, a stage the lowering cannot write as an arrow
//     function (php-versions.md §5.3: a bound argument by reference, one
//     that writes, one named as the piped parameter) is none either;
//   - a comment between the calls, `__LINE__` anywhere in the chain, a
//     multi-line string that would move sideways, arguments laid out over
//     lines whose stage would pass 120 columns on one, and one stage per
//     line starting inside brackets opened on its line or followed by more
//     code there keep the chain as it is.
//
// Layout: three stages or more, a line past 120 columns, or a piece that
// spans lines put one stage per line, `|>` first, one level in from the
// line the chain starts on; a shorter pipeline stays on one line. A
// multi-line argument moves with its stage, its lines re-indented.

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/evalorder"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Why a chain is left as calls, as the summary counts it.
const (
	ReasonOrder    = "evaluation order"
	ReasonByRef    = "by-reference parameter"
	ReasonSpread   = "spread"
	ReasonConst    = "constant expression"
	ReasonScope    = "compact/extract/func_get_args"
	ReasonCallee   = "unresolved callee"
	ReasonNullsafe = "nullsafe call"
	ReasonTypeArgs = "type arguments"
	ReasonOperand  = "operand of an operator"
	ReasonString   = "string interpolation"
	ReasonComment  = "comment inside"
	ReasonLine     = "__LINE__"
	ReasonText     = "multi-line string"
	ReasonLayout   = "code after it on its line"
	ReasonBrackets = "inside brackets on its line"
	ReasonSeveral  = "several nested calls"
	ReasonLong     = "a stage past 120 columns"
	ReasonLowering = "PHP 8.5 lowering"
	ReasonCheck    = "undone by the check"
	ReasonData     = "not the data parameter"
)

// dataLast names the parameter that takes the data in PHP's functions that
// take it after their other arguments: a pipe stage may feed it, as it may
// feed any function's first parameter (design/tooling.md §8.1).
var dataLast = map[string]string{
	"array_map": "array", "implode": "array", "join": "array", "explode": "string",
	"preg_match": "subject", "preg_match_all": "subject", "preg_split": "subject", "preg_grep": "array",
	"preg_replace": "subject", "preg_replace_callback": "subject", "preg_replace_callback_array": "subject",
	"preg_filter": "subject", "mb_split": "string", "str_replace": "subject", "str_ireplace": "subject",
	"in_array": "haystack", "array_search": "haystack", "array_key_exists": "array", "key_exists": "array",
}

// piper finds and rewrites the chains of one file.
type piper struct {
	src []byte
	tokenIndex
	prog  *program.Program
	pfile *program.File
	// types are the checker's types for the file's expressions, by span:
	// a method stage's receiver is resolved through them.
	types    map[ast.Span]types.Type
	min      int
	consumed map[ast.Expr]bool
	paths    []*chainPath
	accepted []*pipeline // sorted by position once finalized
}

// chainPath is one maximal chain: the runs it splits into and why the part
// that is not rewritten is not.
type chainPath struct {
	top    ast.Expr
	calls  int
	runs   []*pipeline
	reason string // the first reason a link failed, "" when none did
}

type pipeline struct {
	top    ast.Expr // the outermost call: the span the pipeline replaces
	head   ast.Expr // the innermost call's subject, or the innermost call
	paren  bool     // the head is written in parentheses
	stages []stage  // innermost first
	multi  bool     // one stage per line
	ok     bool
	reason string // why it was refused
}

type stage struct {
	call  ast.Expr
	args  *ast.ArgList
	piped int  // the argument the piped value replaces
	fcc   bool // written f(...): the piped value is the only argument
}

// walkCtx is what the walk knows about the code around a node.
type walkCtx struct {
	class    *program.Class // the class-like whose body this is, nil outside one
	inConst  bool
	inString bool
}

// pipeResult is the rewrite of one file.
type pipeResult struct {
	out       []byte
	pipelines int
	left      map[string]int
	notes     []Note
	segs      []segment  // source ranges copied into out unchanged
	calls     []spanPair // each stage's call in the source → its |> in out
}

type segment struct{ src, end, out int }

type spanPair struct{ orig, out ast.Span }

// rewritePipes rewrites the chains of a file read into the program model
// (pfile, whose Src it rewrites); tys are the checker's types for it.
func rewritePipes(prog *program.Program, pfile *program.File, tys map[ast.Span]types.Type, min int) *pipeResult {
	r := &piper{
		src: pfile.Src, tokenIndex: tokenIndex{lexer.Tokenize(pfile.Src, lexer.ModePhaenon)},
		prog: prog, pfile: pfile, types: tys, min: min, consumed: map[ast.Expr]bool{},
	}
	r.walk(pfile.AST, nil, walkCtx{})
	r.finalize()
	res := &pipeResult{left: map[string]int{}}
	for _, c := range r.paths {
		n := 0
		reason := c.reason
		for _, p := range c.runs {
			if p.ok {
				n++
			} else if reason == "" {
				reason = p.reason
			}
		}
		if n > 0 {
			res.pipelines += n
			continue
		}
		res.left[reason]++
		line, _ := pfile.Position(c.top.Span().Start)
		res.notes = append(res.notes, Note{Line: line, Msg: fmt.Sprintf("chain of %d calls left as calls (%s): %s", c.calls, reason, r.calleeText(c.top))})
	}
	if res.pipelines == 0 {
		res.out = r.src
		return res
	}
	w := &outBuf{lineStart: true}
	r.render(w, 0, len(r.src))
	if w.bad {
		// Re-indentation met a line it was not validated for: keep the
		// file as it is (the check would not see why).
		res.out, res.pipelines = r.src, 0
		res.left[ReasonText] += len(r.accepted)
		return res
	}
	res.out, res.segs, res.calls = w.b, w.segs, w.calls
	sort.Slice(res.segs, func(i, j int) bool { return res.segs[i].src < res.segs[j].src })
	return res
}

// calleeText is the call's callee as written, `f(…)`, for a note.
func (r *piper) calleeText(e ast.Expr) string {
	args := callArgs(e)
	if args == nil {
		return ""
	}
	text := strings.Join(strings.Fields(string(r.src[e.Span().Start:args.S.Start])), " ")
	return text + "(…)"
}

// ------------------------------------------------------------------ finding chains

// children returns the direct children of a node, in source order.
func children(n ast.Node) []ast.Node {
	var out []ast.Node
	ast.Inspect(n, func(c ast.Node) bool {
		if c == n {
			return true
		}
		out = append(out, c)
		return false
	})
	return out
}

func (r *piper) walk(n ast.Node, parent ast.Node, ctx walkCtx) {
	switch n := n.(type) {
	case *ast.ClassDecl:
		ctx.class = nil
		if n.Name != nil && r.prog != nil {
			ctx.class = r.prog.Class(r.pfile.ScopeAt(n.S.Start).Prefix() + n.Name.Name)
		}
	case *ast.InterpolatedString:
		ctx.inString = true
	}
	if e, ok := n.(ast.Expr); ok && chainCall(e) && !r.consumed[e] {
		r.chain(e, parent, ctx)
	}
	for _, c := range children(n) {
		cc := ctx
		if constSlot(n, c) {
			cc.inConst = true
		}
		r.walk(c, n, cc)
	}
}

// constSlot reports a child holding a constant expression: a default, a
// constant's or an enum case's value, an attribute's arguments.
func constSlot(parent, child ast.Node) bool {
	switch p := parent.(type) {
	case *ast.Param:
		return p.Default != nil && child == ast.Node(p.Default)
	case *ast.PropertyItem:
		return p.Default != nil && child == ast.Node(p.Default)
	case *ast.ConstItem:
		return p.Value != nil && child == ast.Node(p.Value)
	case *ast.EnumCase:
		return p.Value != nil && child == ast.Node(p.Value)
	case *ast.StaticVar:
		return p.Default != nil && child == ast.Node(p.Default)
	case *ast.Attribute:
		return true
	}
	return false
}

// callArgs is the argument list of a function, method or static call.
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

// chainCall reports a call that can be a link of a chain: one that calls,
// not a first-class callable or a partial application.
func chainCall(e ast.Expr) bool {
	args := callArgs(e)
	return args != nil && !args.Closure()
}

// longest is the chain from e down through the argument that makes it
// longest (the first of equals), outermost call first.
func (r *piper) longest(e ast.Expr) []ast.Expr {
	var best []ast.Expr
	for _, a := range callArgs(e).Args {
		if a.Value != nil && chainCall(a.Value) && !r.consumed[a.Value] {
			if p := r.longest(a.Value); len(p) > len(best) {
				best = p
			}
		}
	}
	return append([]ast.Expr{e}, best...)
}

// pipePosition reports whether a pipeline may stand where e stands without
// parentheses and as the whole of a value.
func pipePosition(parent ast.Node, e ast.Expr) bool {
	switch p := parent.(type) {
	case *ast.ExprStmt, *ast.Return, *ast.Arg, *ast.Echo, *ast.Print, *ast.YieldFrom, *ast.Throw,
		*ast.Paren, *ast.ArrowFunc, *ast.If, *ast.ElseIf, *ast.While, *ast.DoWhile, *ast.Switch:
		return true
	case *ast.Assign:
		return p.Value == e
	case *ast.ArrayItem:
		return p.Value == e
	case *ast.MatchArm:
		return p.Body == e
	case *ast.Match:
		return p.Subject == e
	case *ast.Foreach:
		return p.Expr == e
	case *ast.Yield:
		return p.Value == e
	}
	return false
}

// chain records the chain whose outermost call is top and the runs it
// splits into.
func (r *piper) chain(top ast.Expr, parent ast.Node, ctx walkCtx) {
	path := r.longest(top)
	for _, c := range path {
		r.consumed[c] = true
	}
	if len(path) < r.min {
		return
	}
	c := &chainPath{top: top, calls: len(path)}
	r.paths = append(r.paths, c)
	switch {
	case ctx.inConst:
		c.reason = ReasonConst
		return
	case ctx.inString:
		c.reason = ReasonString
		return
	}
	for _, call := range path {
		if evalorder.ScopeCall(call) {
			c.reason = ReasonScope
			return
		}
	}
	n := len(path)
	// link[k] is why path[k] cannot take path[k+1]'s value from a pipe, ""
	// when it can.
	link := make([]string, n)
	for k := 0; k < n-1; k++ {
		link[k] = r.stageable(path[k], argIndex(path[k], path[k+1]), ctx)
	}
	first := 0
	if !pipePosition(parent, top) {
		first, c.reason = 1, ReasonOperand
	}
	subject := r.subject(path[n-1], ctx)
	for i := first; i < n; {
		j := i
		for j < n-1 && link[j] == "" {
			j++
		}
		if c.reason == "" && j < n-1 {
			c.reason = link[j]
		}
		if j-i+1 >= r.min {
			s := -1
			if j == n-1 {
				s = subject
			}
			c.runs = append(c.runs, r.build(path[i:j+1], s))
		}
		i = j + 1
	}
	if len(c.runs) == 0 && c.reason == "" {
		c.reason = ReasonOperand
	}
}

// argIndex is the index of the argument of call whose value is inner.
func argIndex(call, inner ast.Expr) int {
	for i, a := range callArgs(call).Args {
		if a.Value == inner {
			return i
		}
	}
	return -1
}

// subject is the index of the innermost call's subject: its one argument
// that is not a literal, a constant or a closure, when the call can take it
// from a pipe; -1 when the call is the head itself.
func (r *piper) subject(call ast.Expr, ctx walkCtx) int {
	s := -1
	for i, a := range callArgs(call).Args {
		if a.Value == nil || a.Unpack {
			return -1
		}
		if evalorder.Constant(a.Value) || evalorder.Closure(a.Value) {
			continue
		}
		if s >= 0 {
			return -1
		}
		s = i
	}
	if s < 0 || r.stageable(call, s, ctx) != "" {
		return -1
	}
	return s
}

// stageable says why call cannot take the value of its argument piped from
// a pipe, "" when it can.
func (r *piper) stageable(call ast.Expr, piped int, ctx walkCtx) string {
	args := callArgs(call)
	switch c := call.(type) {
	case *ast.Call:
		if _, ok := c.Callee.(*ast.Name); !ok {
			return ReasonCallee
		}
		if len(c.TypeArgs) > 0 {
			return ReasonTypeArgs
		}
	case *ast.MethodCall:
		if c.NullSafe {
			return ReasonNullsafe
		}
		if _, ok := c.Name.(*ast.Ident); !ok {
			return ReasonCallee
		}
		if len(c.TypeArgs) > 0 {
			return ReasonTypeArgs
		}
	case *ast.StaticCall:
		_, named := c.Class.(*ast.Name)
		_, ident := c.Name.(*ast.Ident)
		if !named || !ident {
			return ReasonCallee
		}
		if len(c.TypeArgs) > 0 {
			return ReasonTypeArgs
		}
	}
	for _, a := range args.Args {
		if a.Unpack {
			return ReasonSpread
		}
	}
	inner := args.Args[piped].Value
	written := evalorder.Writes(inner, r.byValue(ctx))
	if mc, ok := call.(*ast.MethodCall); ok && !r.stableReceiver(mc.Object, written, inner, ctx) {
		return ReasonOrder
	}
	for _, a := range args.Args[:piped] {
		if !evalorder.Movable(a.Value, written) {
			return ReasonOrder
		}
	}
	// With another call among the arguments, which one is the data is
	// not the codemod's to choose.
	for _, a := range args.Args[piped+1:] {
		if a.Value != nil && chainCall(a.Value) {
			return ReasonSeveral
		}
	}
	params, ok := r.params(call, ctx)
	if !ok {
		return ReasonCallee
	}
	p := bind(params, args.Args, piped)
	if p == nil {
		return ReasonCallee
	}
	if p.ByRef {
		return ReasonByRef
	}
	if p != params[0] && !r.takesDataLast(call, p) {
		return ReasonData
	}
	if !r.prog.Target().AtLeast(phpversion.PHP86) && !(len(args.Args) == 1 && args.Args[0].Name == nil) && !lowerable(params, args.Args, piped, p) {
		return ReasonLowering
	}
	return ""
}

// lowerable reports whether a partial application in a pipe can be written
// for PHP 8.5 as an arrow function called at once (design/php-versions.md
// §5.3), as the checker decides it: no bound argument is by reference,
// writes to a variable, or reads one named as the piped parameter, which
// the arrow function's parameter would hide. An anonymous class's members
// are none of these (ast.InspectScope).
func lowerable(params []*program.Param, args []*ast.Arg, piped int, p *program.Param) bool {
	for i, a := range args {
		if i == piped {
			continue
		}
		if b := bind(params, args, i); b != nil && b.ByRef {
			return false
		}
		clash := false
		ast.InspectScope(a.Value, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Assign, *ast.IncDec, *ast.Yield, *ast.YieldFrom:
				clash = true
			case *ast.Variable:
				clash = clash || n.Name == p.Name
			case *ast.Closure, *ast.ArrowFunc:
				return false
			}
			return !clash
		})
		if clash {
			return false
		}
	}
	return true
}

// params resolves a call's callee through the program model.
func (r *piper) params(call ast.Expr, ctx walkCtx) ([]*program.Param, bool) {
	if r.prog == nil || r.pfile == nil {
		return nil, false
	}
	scope := r.pfile.ScopeAt(call.Span().Start)
	switch c := call.(type) {
	case *ast.Call:
		n, ok := c.Callee.(*ast.Name)
		if !ok {
			return nil, false
		}
		ns, global := scope.ResolveFunction(n)
		fn := r.prog.Function(ns)
		if fn == nil {
			fn = r.prog.Function(global)
		}
		if fn == nil {
			return nil, false
		}
		return fn.Signature().Params, true
	case *ast.MethodCall:
		id, ok := c.Name.(*ast.Ident)
		if !ok {
			return nil, false
		}
		return methodParams(r.receiverClass(c.Object, ctx), id.Name)
	case *ast.StaticCall:
		n, ok := c.Class.(*ast.Name)
		id, ok2 := c.Name.(*ast.Ident)
		if !ok || !ok2 {
			return nil, false
		}
		var cl *program.Class
		switch specialClassName(n) {
		case "self", "static":
			cl = ctx.class
		case "parent":
			if ctx.class != nil {
				cl = ctx.class.ParentClass()
			}
		default:
			cl = r.prog.Class(scope.ResolveClass(n))
		}
		return methodParams(cl, id.Name)
	}
	return nil, false
}

func methodParams(cl *program.Class, name string) ([]*program.Param, bool) {
	if cl == nil {
		return nil, false
	}
	m := cl.FindMethod(name)
	if m == nil || m.Magic {
		return nil, false
	}
	return m.Params, true
}

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

// stableReceiver reports a method stage's receiver that is the same object
// when it is read after the piped value as before it: $this, a variable the
// inner calls do not write, or a property of either that its class declares
// without hooks, which the inner calls do not assign. A method the inner
// calls run may still assign the property: the codemod takes a declared
// property as not changed by computing a value for it (design/tooling.md
// §8.1), and the 8.5 lowering keeps such a stage a pipe's.
func (r *piper) stableReceiver(obj ast.Expr, written evalorder.Set, inner ast.Expr, ctx walkCtx) bool {
	switch o := obj.(type) {
	case *ast.Variable:
		return !written.Has(o.Name)
	case *ast.PropertyFetch:
		id, ok := o.Name.(*ast.Ident)
		if !ok || o.NullSafe || !r.stableReceiver(o.Object, written, inner, ctx) {
			return false
		}
		if _, isVar := o.Object.(*ast.Variable); !isVar {
			return false
		}
		cl := r.receiverClass(o.Object, ctx)
		if cl == nil {
			return false
		}
		pr := cl.FindProperty(id.Name)
		if pr == nil || pr.Magic || pr.Static || len(pr.Hooks) > 0 {
			return false
		}
		return !assignsProperty(inner, id.Name)
	}
	return false
}

// assignsProperty reports an assignment or an increment of a property
// named name in e.
func assignsProperty(e ast.Expr, name string) bool {
	found := false
	ast.InspectScope(e, func(n ast.Node) bool {
		var target ast.Expr
		switch n := n.(type) {
		case *ast.Assign:
			target = n.Target
		case *ast.IncDec:
			target = n.Expr
		}
		for target != nil && !found {
			switch t := target.(type) {
			case *ast.PropertyFetch:
				id, ok := t.Name.(*ast.Ident)
				found = !ok || id.Name == name
				target = t.Object
			case *ast.Index:
				target = t.Array
			default:
				target = nil
			}
		}
		return !found
	})
	return found
}

// takesDataLast reports a function of dataLast called with the piped value
// in its data parameter.
func (r *piper) takesDataLast(call ast.Expr, p *program.Param) bool {
	c, ok := call.(*ast.Call)
	if !ok || r.pfile == nil {
		return false
	}
	n, ok := c.Callee.(*ast.Name)
	if !ok {
		return false
	}
	ns, global := r.pfile.ScopeAt(call.Span().Start).ResolveFunction(n)
	if r.prog.Function(ns) != nil && !strings.EqualFold(ns, global) {
		return false // a function of the namespace, not PHP's
	}
	data, ok := dataLast[strings.ToLower(strings.TrimPrefix(global, "\\"))]
	return ok && p.Name == data
}

// receiverClass is the class of a method call's receiver: the enclosing
// class for $this, the class the checker gave a variable otherwise, the
// declared class of a property of either.
func (r *piper) receiverClass(obj ast.Expr, ctx walkCtx) *program.Class {
	if pf, ok := obj.(*ast.PropertyFetch); ok {
		id, ok := pf.Name.(*ast.Ident)
		cl := r.receiverClass(pf.Object, ctx)
		if !ok || cl == nil {
			return nil
		}
		if pr := cl.FindProperty(id.Name); pr != nil {
			if t, ok := pr.Type.(types.Class); ok {
				return r.prog.Class(t.Name)
			}
		}
		return nil
	}
	v, ok := obj.(*ast.Variable)
	if !ok {
		return nil
	}
	if v.Name == "this" {
		return ctx.class
	}
	switch t := r.types[v.S].(type) {
	case types.Class:
		return r.prog.Class(t.Name)
	case types.This:
		if t.Of != nil {
			return r.prog.Class(t.Of.Name)
		}
	case types.Static:
		if t.Of != nil {
			return r.prog.Class(t.Of.Name)
		}
	}
	return nil
}

// bind is the parameter an argument is passed to, nil when none takes it.
func bind(params []*program.Param, args []*ast.Arg, i int) *program.Param {
	a := args[i]
	if a.Name != nil {
		for _, p := range params {
			if p.Name == a.Name.Name && !p.Variadic {
				return p
			}
		}
		return nil
	}
	if i < len(params) {
		return params[i]
	}
	if n := len(params); n > 0 && params[n-1].Variadic {
		return params[n-1]
	}
	return nil
}

// byValue resolves the calls under an inner call for evalorder.Writes: an
// argument goes by value when the program model resolves its callee and
// binds it to a parameter that is not by reference. A constructor's
// arguments are not resolved.
func (r *piper) byValue(ctx walkCtx) evalorder.ByValue {
	return func(call ast.Expr, i int) bool {
		if _, isNew := call.(*ast.New); isNew {
			return false
		}
		params, ok := r.params(call, ctx)
		if !ok {
			return false
		}
		p := bind(params, callArgs(call).Args, i)
		return p != nil && !p.ByRef
	}
}

// atomic reports a head that needs no parentheses before `|>`.
func atomic(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.Variable, *ast.PropertyFetch, *ast.StaticPropertyFetch, *ast.Index, *ast.Call, *ast.MethodCall,
		*ast.StaticCall, *ast.IntLit, *ast.FloatLit, *ast.StringLit, *ast.InterpolatedString, *ast.ConstFetch,
		*ast.ClassConstFetch, *ast.MagicConst, *ast.ArrayLit, *ast.Paren, *ast.Name:
		return true
	case *ast.New:
		return e.Args != nil
	}
	return false
}

// build makes the pipeline of a run of calls, outermost first; subject is
// the innermost call's subject, -1 when that call is the head.
func (r *piper) build(run []ast.Expr, subject int) *pipeline {
	p := &pipeline{top: run[0]}
	inner := run[len(run)-1]
	if subject >= 0 {
		p.head = callArgs(inner).Args[subject].Value
		p.stages = append(p.stages, newStage(inner, subject))
	} else {
		p.head = inner
	}
	for k := len(run) - 2; k >= 0; k-- {
		p.stages = append(p.stages, newStage(run[k], argIndex(run[k], run[k+1])))
	}
	p.paren = !atomic(p.head)
	return p
}

func newStage(call ast.Expr, piped int) stage {
	args := callArgs(call)
	return stage{call: call, args: args, piped: piped, fcc: len(args.Args) == 1 && args.Args[0].Name == nil}
}

// ------------------------------------------------------------------ layout

// pieces are the spans a pipeline copies from the source: its head and
// the bound arguments of its stages.
func (p *pipeline) pieces() []ast.Span {
	out := []ast.Span{p.head.Span()}
	for _, s := range p.stages {
		for i, a := range s.args.Args {
			if i != s.piped {
				out = append(out, a.Span())
			}
		}
	}
	return out
}

// finalize decides, innermost first, which pipelines are written and how
// they are laid out.
func (r *piper) finalize() {
	var all []*pipeline
	for _, c := range r.paths {
		all = append(all, c.runs...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i].top.Span(), all[j].top.Span()
		return a.End-a.Start < b.End-b.Start
	})
	for _, p := range all {
		p.multi = r.multiLine(p)
		if why := r.refuse(p); why != "" {
			p.reason = why
			continue
		}
		p.ok = true
		r.accepted = append(r.accepted, p)
	}
	sort.Slice(r.accepted, func(i, j int) bool { return r.accepted[i].top.Span().Start < r.accepted[j].top.Span().Start })
}

// within lists the accepted pipelines inside a span.
func (r *piper) within(s ast.Span) []*pipeline {
	var out []*pipeline
	for _, p := range r.accepted {
		if t := p.top.Span(); s.Start <= t.Start && t.End <= s.End {
			out = append(out, p)
		}
	}
	return out
}

// multiLine reports a pipeline written one stage per line.
func (r *piper) multiLine(p *pipeline) bool {
	if len(p.stages) >= 3 {
		return true
	}
	for _, s := range p.pieces() {
		if bytes.ContainsAny(r.src[s.Start:s.End], "\n\r") {
			return true
		}
		for _, q := range r.within(s) {
			if q.multi {
				return true
			}
		}
	}
	top := p.top.Span()
	var b strings.Builder
	b.Write(r.src[lineStart(r.src, top.Start):top.Start])
	if p.paren {
		b.WriteString("(")
	}
	head := p.head.Span()
	b.Write(r.src[head.Start:head.End])
	if p.paren {
		b.WriteString(")")
	}
	for _, s := range p.stages {
		b.WriteString(" |> ")
		b.WriteString(r.stageText(s))
	}
	end := bytes.IndexAny(r.src[top.End:], "\r\n")
	if end < 0 {
		end = len(r.src) - top.End
	}
	b.Write(r.src[top.End : top.End+end])
	return width(b.String()) > maxColumns
}

// stageText is a stage as written from the source's bound arguments.
func (r *piper) stageText(s stage) string {
	callee := string(r.src[s.call.Span().Start:s.args.S.Start])
	if s.fcc {
		return callee + "(...)"
	}
	var parts []string
	for i, a := range s.args.Args {
		switch {
		case i != s.piped:
			s := a.Span()
			parts = append(parts, string(r.src[s.Start:s.End]))
		case a.Name != nil:
			parts = append(parts, a.Name.Name+": ?")
		default:
			parts = append(parts, "?")
		}
	}
	return callee + "(" + strings.Join(parts, ", ") + ")"
}

// refuse says why a pipeline cannot be written as laid out, "" when it can:
// a comment it would drop, a `__LINE__` it would move; laid out one stage
// per line, code that would follow its last stage on the line or brackets
// around its first; a multi-line piece it cannot re-indent; arguments laid
// out over lines that one line would not hold.
func (r *piper) refuse(p *pipeline) string {
	top := p.top.Span()
	pieces := p.pieces()
	inPiece := func(off int) bool {
		for _, s := range pieces {
			if s.Start <= off && off < s.End {
				return true
			}
		}
		return false
	}
	for i := r.tokenAt(top.Start); i < len(r.toks) && r.toks[i].Start < top.End; i++ {
		t := r.toks[i]
		switch t.Kind {
		case token.T_COMMENT, token.T_DOC_COMMENT:
			if !inPiece(t.Start) {
				return ReasonComment
			}
		case token.T_LINE:
			return ReasonLine
		}
	}
	if p.multi && !r.endsLine(top.End) {
		return ReasonLayout
	}
	if p.multi && r.insideBrackets(top.Start) {
		return ReasonBrackets
	}
	ind := indentOf(r.src, top.Start)
	stageInd := ind
	if p.multi {
		stageInd = ind + levelOf(ind)
	}
	for k, s := range pieces {
		to := stageInd
		if k == 0 {
			to = ind // the head stays on the line the chain starts on
		}
		if !r.reindentable(s, indentOf(r.src, s.Start), to) {
			return ReasonText
		}
	}
	// A call whose arguments the source lays out over lines keeps them so
	// when its stage would pass 120 columns on one line.
	for _, s := range p.stages {
		if s.fcc || !bytes.ContainsAny(r.src[s.args.S.Start:s.args.S.End], "\n\r") {
			continue
		}
		first, _, _ := strings.Cut(r.stageText(s), "\n")
		if width(stageInd+"|> "+first) > maxColumns {
			return ReasonLong
		}
	}
	return ""
}

// endsLine reports whether only closing brackets, commas and semicolons
// follow off on its line, before a comment: a pipeline laid out one stage
// per line ends there, not in the middle of other code.
func (r *piper) endsLine(off int) bool {
	for i := r.tokenAt(off); i < len(r.toks); i++ {
		t := r.toks[i]
		switch t.Kind {
		case token.T_WHITESPACE:
			if bytes.ContainsAny(r.src[t.Start:t.End], "\n\r") {
				return true
			}
		case token.T_COMMENT, token.T_DOC_COMMENT:
			return true
		case token.Kind(')'), token.Kind(']'), token.Kind('}'), token.Kind(','), token.Kind(';'):
		default:
			return false
		}
	}
	return true
}

// insideBrackets reports whether a bracket opened on off's line before off
// is still open there: a pipeline laid out one stage per line starts the
// value of its line — a statement, an assignment, an argument or an
// element on a line of its own — not the middle of a call or an array
// written on one line.
func (r *piper) insideBrackets(off int) bool {
	depth := 0
	for i := r.tokenAt(lineStart(r.src, off)); i < len(r.toks) && r.toks[i].Start < off; i++ {
		switch r.toks[i].Kind {
		case token.Kind('('), token.Kind('['), token.Kind('{'), token.T_CURLY_OPEN, token.T_DOLLAR_OPEN_CURLY_BRACES:
			depth++
		case token.Kind(')'), token.Kind(']'), token.Kind('}'):
			if depth > 0 {
				depth--
			}
		}
	}
	return depth > 0
}

// levelOf is one indentation level: four spaces, since Phaenon's
// whitespace is spaces (design/functions.md §5.5) and Prepare has replaced
// a PHP file's tabs.
func levelOf(string) string { return "    " }

// reindentable reports whether the lines after the first of a piece can
// move from indentation from to indentation to: each starts with from or
// is blank, and no token but whitespace and comments spans lines.
func (r *piper) reindentable(s ast.Span, from, to string) bool {
	text := r.src[s.Start:s.End]
	if from == to || !bytes.ContainsAny(text, "\n\r") {
		return true
	}
	for i := r.tokenAt(s.Start); i < len(r.toks) && r.toks[i].Start < s.End; i++ {
		t := r.toks[i]
		switch t.Kind {
		case token.T_WHITESPACE, token.T_COMMENT, token.T_DOC_COMMENT:
			continue
		}
		if bytes.ContainsAny(r.src[t.Start:t.End], "\n\r") {
			return false
		}
	}
	for _, line := range strings.Split(string(text), "\n")[1:] {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, from) {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------------ writing

// outBuf collects the rewritten file and where its pieces came from.
type outBuf struct {
	b    []byte
	segs []segment
	// calls maps each stage's call to the pipe expression ending with it.
	calls []spanPair
	// reindent holds, innermost last, the indentation rewrites in force:
	// a line starting inside a piece that moved sideways starts with to
	// instead of from.
	reindent  [][2]string
	lineStart bool
	pending   string // indentation seen at a line start, not yet written
	bad       bool   // a line did not start with the indentation to rewrite
}

func (w *outBuf) gen(s string) { w.write([]byte(s), -1) }

func (w *outBuf) copy(src []byte, from, to int) {
	if from < to {
		w.write(src[from:to], from)
	}
}

// write appends text, which is src[srcOff:] when srcOff ≥ 0, applying the
// indentation rewrites at each line start.
func (w *outBuf) write(text []byte, srcOff int) {
	i := 0
	for i < len(text) {
		if w.lineStart && len(w.reindent) > 0 {
			j := i
			for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
				j++
			}
			if j == len(text) {
				w.pending += string(text[i:j])
				return
			}
			indent := w.pending + string(text[i:j])
			w.pending = ""
			if text[j] != '\n' && text[j] != '\r' {
				w.b = append(w.b, w.rewrite(indent)...)
			}
			w.lineStart = false
			i = j
			continue
		}
		end := len(text)
		if k := bytes.IndexByte(text[i:], '\n'); k >= 0 {
			end = i + k + 1
		}
		if srcOff >= 0 {
			w.segs = append(w.segs, segment{src: srcOff + i, end: srcOff + end, out: len(w.b)})
		}
		w.b = append(w.b, text[i:end]...)
		w.lineStart = text[end-1] == '\n'
		i = end
	}
}

// flush writes indentation held at a line start, before the rewrites in
// force change.
func (w *outBuf) flush() {
	if w.pending != "" {
		w.b = append(w.b, w.rewrite(w.pending)...)
		w.pending = ""
		w.lineStart = false
	}
}

func (w *outBuf) rewrite(indent string) string {
	for k := len(w.reindent) - 1; k >= 0; k-- {
		from, to := w.reindent[k][0], w.reindent[k][1]
		if !strings.HasPrefix(indent, from) {
			w.bad = true
			continue
		}
		indent = to + indent[len(from):]
	}
	return indent
}

// render writes src[from:to) with the pipelines inside it.
func (r *piper) render(w *outBuf, from, to int) {
	pos := from
	for _, p := range r.accepted {
		s := p.top.Span()
		if s.Start < pos || s.End > to {
			continue
		}
		w.copy(r.src, pos, s.Start)
		r.writePipeline(w, p)
		pos = s.End
	}
	w.copy(r.src, pos, to)
}

// piece writes a span copied into the pipeline, its later lines moved
// from indentation from to indentation to.
func (r *piper) piece(w *outBuf, s ast.Span, from, to string) {
	w.flush()
	w.reindent = append(w.reindent, [2]string{from, to})
	r.render(w, s.Start, s.End)
	w.flush()
	w.reindent = w.reindent[:len(w.reindent)-1]
}

func (r *piper) writePipeline(w *outBuf, p *pipeline) {
	w.flush()
	ind := indentOf(r.src, p.top.Span().Start)
	stageInd := ind
	if p.multi {
		stageInd = ind + levelOf(ind)
	}
	start := len(w.b)
	if p.paren {
		w.gen("(")
	}
	head := p.head.Span()
	r.piece(w, head, indentOf(r.src, head.Start), ind)
	if p.paren {
		w.gen(")")
	}
	for _, s := range p.stages {
		if p.multi {
			w.gen("\n" + stageInd + "|> ")
		} else {
			w.gen(" |> ")
		}
		w.copy(r.src, s.call.Span().Start, s.args.S.Start)
		if s.fcc {
			w.gen("(...)")
		} else {
			w.gen("(")
			for i, a := range s.args.Args {
				if i > 0 {
					w.gen(", ")
				}
				if i != s.piped {
					s := a.Span()
					r.piece(w, s, indentOf(r.src, s.Start), stageInd)
					continue
				}
				if a.Name != nil {
					w.copy(r.src, a.Name.S.Start, a.Name.S.End)
					w.gen(": ")
				}
				w.gen("?")
			}
			w.gen(")")
		}
		w.calls = append(w.calls, spanPair{orig: s.call.Span(), out: ast.Span{Start: start, End: len(w.b)}})
	}
}

// ------------------------------------------------------------------ the syntactic scan

// hasChain reports whether src holds a chain of at least min calls,
// before anything is resolved: the files worth checking.
func hasChain(f *ast.File, min int) bool {
	r := &piper{min: min, consumed: map[ast.Expr]bool{}}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok && chainCall(e) && len(r.longest(e)) >= min {
			found = true
		}
		return !found
	})
	return found
}
