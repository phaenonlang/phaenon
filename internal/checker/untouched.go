package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Untouched parameters: what proves an extra check of a value (extra.go).
//
// A parameter is untouched when the read being judged is its only
// occurrence in the body of its function. Then there is no earlier and no
// later place where PHPStan could have learnt anything, of the variable or
// of an expression that holds it: PHPStan remembers what it was told of
// `$i ** -1` as it remembers what it was told of `$i`, and a second
// `$i ** -1` after `assert(is_float($i ** -1))` is a float to it. That is
// a property of the function, computed once from its syntax and
// conservative by construction. It holds when
//
//   - the type comes from this declaration alone (declaredHereAlone);
//   - the text of the body holds `$name` once, and that one occurrence is a
//     plain read (plainReads): no write, no reference, no `use` of a
//     closure, no parameter of a nested function, nothing in a docblock or
//     a comment;
//   - the body has nothing that can write a variable without naming it
//     (writesUnnamed).
//
// The value of the map says whether the parameter is typed by PHP alone
// (typedByPHPAlone), which is what proves that PHPStan knows no value of
// it: a native type names no set of values, and a docblock may list them.
// A finding over two variables, `$i ** $j`, needs each to be untouched.
type untouched map[string]bool

// A plain read is where the one occurrence may stand. Each position was
// settled by giving PHPStan the function with the finding the checker
// makes there and seeing it silent on that line; what was not run is not
// on the list. The variable is
//
//   - an operand of an arithmetic operator (+ - * / % **, unary + and -)
//     or of `.`, or a part of an interpolated string, wherever the result
//     stands that is no condition: an argument, the right-hand side of an
//     assignment, a branch of a ternary, an arm of a match;
//   - what a `return`, an `echo` or a `print` gives, or the body of an
//     arrow function, alone or as an element of an array literal there,
//     and what a `yield` gives, alone;
//   - the whole right-hand side of a plain assignment to a property;
//   - an argument of a call, alone, outside every loop. PHPStan judges the
//     argument before the callee's assertion tags and the type a
//     parameter taken by reference leaves in it, so the one occurrence is
//     judged by its declared type; in a loop the second pass sees what a
//     parameter by reference left, and reports.
//
// A loop, a try with its catch and finally, a generator and a condition
// that does not hold the variable change nothing of this. No plain read
// are a target of any assignment, `++`, a reference, `unset`, `global`,
// `static`, a `foreach` or a `catch`; any place in a condition or in what
// narrows (`if`, `while`, `for`, `switch`, the subject and the conditions
// of a `match`, the condition of `?:`, `??`, `&&`, `||`, a comparison,
// `instanceof`, `isset`, `empty`); the right-hand side of an assignment to
// a variable; an offset or a member of it; a cast; a closure.
type readMode uint8

const (
	noRead       readMode = iota // nothing below is a plain read
	readComputed                 // the variable itself is none, an operand below is
	readOperand                  // the variable is one, an array literal's element is not
	readBare                     // the variable is one, alone or in an array literal
)

// plainReads walks a body and counts, for each candidate, its plain reads;
// a candidate used in any other way is taken out.
type plainReads struct {
	candidates untouched
	reads      map[string]int
	loops      int // the loops around the statement being walked
}

// loop walks the body of a loop.
func (w *plainReads) loop(body ast.Stmt) {
	w.loops++
	w.stmt(body)
	w.loops--
}

func (w *plainReads) deny(n ast.Node) {
	ast.Inspect(n, func(n ast.Node) bool {
		if v, ok := n.(*ast.Variable); ok {
			delete(w.candidates, v.Name)
		}
		return true
	})
}

func (w *plainReads) stmts(list []ast.Stmt) {
	for _, s := range list {
		w.stmt(s)
	}
}

func (w *plainReads) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case nil:
	case *ast.Block:
		w.stmts(s.Stmts)
	case *ast.ExprStmt:
		w.statement(s.Expr)
	case *ast.Echo:
		for _, e := range s.Exprs {
			w.expr(e, readBare)
		}
	case *ast.Return:
		if s.Expr != nil {
			w.expr(s.Expr, readBare)
		}
	case *ast.If:
		w.deny(s.Cond)
		w.stmt(s.Then)
		for _, ei := range s.ElseIfs {
			w.deny(ei.Cond)
			w.stmt(ei.Body)
		}
		if s.Else != nil {
			w.stmt(s.Else)
		}
	case *ast.While:
		w.deny(s.Cond)
		w.loop(s.Body)
	case *ast.DoWhile:
		w.loop(s.Body)
		w.deny(s.Cond)
	case *ast.For:
		for _, list := range [][]ast.Expr{s.Init, s.Cond, s.Step} {
			for _, e := range list {
				w.deny(e)
			}
		}
		w.loop(s.Body)
	case *ast.Foreach:
		w.deny(s.Expr)
		if s.Key != nil {
			w.deny(s.Key)
		}
		if s.Value != nil {
			w.deny(s.Value)
		}
		w.loop(s.Body)
	case *ast.Switch:
		w.deny(s.Subject)
		for _, c := range s.Cases {
			if c.Cond != nil {
				w.deny(c.Cond)
			}
			w.stmts(c.Body)
		}
	case *ast.Try:
		w.stmts(s.Body)
		for _, c := range s.Catches {
			if c.Var != nil {
				w.deny(c.Var)
			}
			w.stmts(c.Body)
		}
		w.stmts(s.Finally)
	default:
		w.deny(s)
	}
}

// statement is an expression that stands as a statement: an assignment is
// read here and nowhere else.
func (w *plainReads) statement(e ast.Expr) {
	switch e := e.(type) {
	case *ast.Assign:
		switch {
		case e.ByRef:
			w.deny(e)
		case e.Op == token.Kind('='):
			w.deny(e.Target)
			switch unparen(e.Target).(type) {
			case *ast.PropertyFetch, *ast.StaticPropertyFetch:
				w.expr(e.Value, readBare)
			default:
				w.expr(e.Value, readComputed)
			}
		case arithmeticAssign(e.Op):
			w.deny(e.Target)
			w.expr(e.Value, readOperand)
		default:
			w.deny(e)
		}
	case *ast.Print:
		w.expr(e.Expr, readBare)
	default:
		w.expr(e, readComputed)
	}
}

func (w *plainReads) expr(e ast.Expr, mode readMode) {
	if mode == noRead {
		w.deny(e)
		return
	}
	// below is the mode of what stands in an array literal here.
	below := readComputed
	if mode == readBare {
		below = readBare
	}
	switch e := e.(type) {
	case nil:
	case *ast.Variable:
		if _, ok := w.candidates[e.Name]; !ok {
			return
		}
		if mode == readComputed {
			delete(w.candidates, e.Name)
			return
		}
		w.reads[e.Name]++
	case *ast.Paren:
		w.expr(e.Expr, mode)
	case *ast.IntLit, *ast.FloatLit, *ast.StringLit, *ast.Name, *ast.ConstFetch, *ast.MagicConst:
	case *ast.ArrayLit:
		for _, item := range e.Items {
			switch {
			case item == nil:
			case item.ByRef || item.Unpack:
				w.deny(item)
			default:
				if item.Key != nil {
					w.expr(item.Key, below)
				}
				w.expr(item.Value, below)
			}
		}
	case *ast.Binary:
		if !arithmeticOrConcat(e.Op) {
			w.deny(e)
			return
		}
		w.expr(e.Left, readOperand)
		w.expr(e.Right, readOperand)
	case *ast.Unary:
		if e.Op != token.Kind('-') && e.Op != token.Kind('+') {
			w.deny(e)
			return
		}
		w.expr(e.Expr, readOperand)
	case *ast.InterpolatedString:
		if e.Kind == ast.StringBacktick {
			w.deny(e)
			return
		}
		for _, part := range e.Parts {
			switch part := part.(type) {
			case *ast.StringPart:
			case *ast.Variable:
				w.expr(part, readOperand)
			case *ast.EncapsBraced:
				w.expr(part.Expr, readOperand)
			default:
				w.deny(part)
			}
		}
	case *ast.Yield:
		if e.Key != nil {
			w.expr(e.Key, readOperand)
		}
		if e.Value != nil {
			w.expr(e.Value, readOperand)
		}
	case *ast.Call:
		w.deny(e.Callee)
		w.args(e.Args)
	case *ast.MethodCall:
		w.deny(e.Object)
		w.deny(e.Name)
		w.args(e.Args)
	case *ast.StaticCall:
		w.deny(e.Class)
		w.deny(e.Name)
		w.args(e.Args)
	case *ast.New:
		if e.Anon != nil {
			w.deny(e)
			return
		}
		w.deny(e.Class)
		w.args(e.Args)
	case *ast.Ternary:
		// `c ?: b` gives its condition: only the long form has branches
		// that are no condition.
		if e.Then == nil {
			w.deny(e)
			return
		}
		w.deny(e.Cond)
		w.expr(e.Then, mode)
		w.expr(e.Else, mode)
	case *ast.Match:
		w.deny(e.Subject)
		for _, arm := range e.Arms {
			for _, c := range arm.Conds {
				w.deny(c)
			}
			w.expr(arm.Body, mode)
		}
	case *ast.ArrowFunc:
		// Its parameters are variables of its own: one of a candidate's
		// name takes the candidate out, as any other use does.
		for _, p := range e.Params {
			w.deny(p)
		}
		w.expr(e.Body, readBare)
	default:
		w.deny(e)
	}
}

func (w *plainReads) args(list *ast.ArgList) {
	if list == nil {
		return
	}
	for _, a := range list.Args {
		switch {
		case a.Value == nil:
		case a.Unpack:
			w.deny(a.Value)
		case w.loops > 0:
			w.expr(a.Value, readComputed)
		default:
			// The variable alone, outside every loop; in an array literal
			// it is not.
			w.expr(a.Value, readOperand)
		}
	}
}

func arithmeticOrConcat(op token.Kind) bool {
	switch op {
	case token.Kind('+'), token.Kind('-'), token.Kind('*'), token.Kind('/'), token.Kind('%'), token.T_POW, token.Kind('.'):
		return true
	}
	return false
}

func arithmeticAssign(op token.Kind) bool {
	switch op {
	case token.T_PLUS_EQUAL, token.T_MINUS_EQUAL, token.T_MUL_EQUAL, token.T_DIV_EQUAL, token.T_MOD_EQUAL, token.T_POW_EQUAL, token.T_CONCAT_EQUAL:
		return true
	}
	return false
}

// writesUnnamed reports whether a body holds something that can write a
// variable, or read them all, without naming it: a variable variable,
// extract(), compact(), get_defined_vars(), parse_str(), eval, include and
// require, goto. PHP refuses to call the functions among them by a name
// held in a variable, so their names as written are all of them; a
// function of the same name in a namespace counts too.
func writesUnnamed(stmts []ast.Stmt) bool {
	found := false
	for _, s := range stmts {
		ast.Inspect(s, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.VariableVariable, *ast.StringVarName, *ast.Eval, *ast.Include, *ast.Goto, *ast.Withdrawn:
				found = true
			case *ast.Call:
				if name, ok := n.Callee.(*ast.Name); ok && len(name.Parts) > 0 {
					switch strings.ToLower(name.Parts[len(name.Parts)-1]) {
					case "extract", "compact", "get_defined_vars", "parse_str":
						found = true
					}
				}
			}
			return !found
		})
	}
	return found
}

// untouchedParams finds the untouched parameters of the function or method
// being checked.
func (x *fnCtx) untouchedParams(params []*program.Param, decls []*ast.Param, stmts []ast.Stmt) untouched {
	candidates := untouched{}
	bodyStart := 0
	for i, p := range params {
		if i < len(decls) && decls[i].S.End > bodyStart {
			bodyStart = decls[i].S.End
		}
		if !x.declaredHereAlone(p) {
			continue
		}
		// A type assumed for the parameter is not the declaration's.
		if assume := x.c.opts.Assume; assume != nil && i < len(decls) && assume(x.file, decls[i]) != nil {
			continue
		}
		candidates[p.Name] = x.typedByPHPAlone(p)
	}
	bodyEnd, ok := x.declEnd()
	if len(candidates) == 0 || !ok || len(decls) != len(params) || writesUnnamed(stmts) {
		return nil
	}
	w := &plainReads{candidates: candidates, reads: map[string]int{}}
	w.stmts(stmts)
	// The body's text holds `$name` once, and that is the plain read the
	// walk counted: what the walk does not see as a variable (a docblock
	// above a statement, a comment, a string in single quotes, a closure's
	// parameter) names it all the same.
	if bodyStart > bodyEnd || bodyEnd > len(x.file.Src) {
		return nil
	}
	text := string(x.file.Src[bodyStart:bodyEnd])
	for name := range candidates {
		if w.reads[name] != 1 || variableMentions(text, name) != 1 {
			delete(candidates, name)
		}
	}
	return candidates
}

// declEnd is where the declaration of the function or method being checked
// ends in its file.
func (x *fnCtx) declEnd() (int, bool) {
	switch {
	case x.method != nil && x.method.Decl != nil:
		return x.method.Decl.S.End, true
	case x.method == nil && x.function != nil && x.function.Decl != nil:
		return x.function.Decl.S.End, true
	}
	return 0, false
}

// declaredHereAlone reports whether a parameter of the function or method
// being checked has its type from that declaration and from nowhere else,
// for PHPStan as for the checker: its own native type and its own docblock.
// PHPStan inherits a docblock by more routes than the checker follows (a
// trait's abstract method, an interface where the body comes from a trait,
// a parent constructor), so the rule does not chase them: a method that
// any class, interface or trait of its hierarchy also declares is not
// declared here alone, a constructor included, nor is a trait's own
// method, which each class that uses the trait gives its own parents. A
// promoted, variadic or by-reference parameter is left out as well, and
// one the docblock names more than once: of `@param` and `@phpstan-param`
// on one parameter each tool takes one, and the rule does not rest on
// their taking the same. The parameters of a closure or an arrow function
// are never asked: PHPStan types them from where the closure is passed
// (`array_map(fn (int $i) => …, [1, 2, 3])`).
func (x *fnCtx) declaredHereAlone(p *program.Param) bool {
	if p.Inherited || p.Promoted || p.Variadic || p.ByRef {
		return false
	}
	doc, ok := x.ownDoc()
	if !ok || variableMentions(doc, p.Name) > 1 {
		return false
	}
	return x.method == nil || x.c.declaredNowhereElse(x.method)
}

// ownDoc is the docblock, as written, of the function or method being
// checked; a declaration without a node has none to read.
func (x *fnCtx) ownDoc() (string, bool) {
	switch {
	case x.method != nil && x.method.Decl != nil:
		return x.method.Decl.Doc, true
	case x.method == nil && x.function != nil && x.function.Decl != nil:
		return x.function.Decl.Doc, true
	}
	return "", false
}

// declaredNowhereElse reports whether no class, interface or trait of a
// method's hierarchy declares a method of its name beside its own class:
// not an ancestor, not a trait the class or an ancestor uses. A hierarchy
// that cannot be read whole (a parent, an interface or a trait that is not
// known) says no.
func (c *Checker) declaredNowhereElse(m *program.Method) bool {
	cl := m.Class
	if cl == nil || cl.File == nil || cl.Kind == ast.KindTrait || m.FromTrait != nil || m.Magic {
		return false
	}
	p := c.p
	for _, k := range append([]*program.Class{cl}, cl.Ancestors()...) {
		if k != cl && k.FindMethod(m.Name) != nil {
			return false
		}
		if k.Parent != "" && p.Class(k.Parent) == nil {
			return false
		}
		for _, name := range k.Interfaces {
			if p.Class(name) == nil {
				return false
			}
		}
		for _, name := range k.Traits {
			if t := p.Class(name); t == nil || t.FindMethod(m.Name) != nil {
				return false
			}
		}
	}
	return true
}

// typedByPHPAlone reports whether a parameter declared here alone
// (declaredHereAlone) has the type its native declaration writes and no
// other: the declaration's docblock does not so much as name it, under
// whatever tag. A native type names no set of values, so what the function
// is entered with is whatever a caller passes; a docblock may list values
// (`1|3|5`), which the lattice merges into a range or widens to int.
func (x *fnCtx) typedByPHPAlone(p *program.Param) bool {
	if p.NativeType == nil || !types.Equal(p.Type, types.Erase(p.NativeType)) {
		return false
	}
	doc, ok := x.ownDoc()
	return ok && variableMentions(doc, p.Name) == 0
}

// variableMentions counts the places a text holds `$name` as a whole word.
func variableMentions(text, name string) int {
	n := 0
	for rest := text; ; {
		i := strings.Index(rest, "$"+name)
		if i < 0 {
			return n
		}
		rest = rest[i+1+len(name):]
		if rest != "" {
			if c := rest[0]; c == '_' || c >= 0x80 || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' {
				continue
			}
		}
		n++
	}
}
