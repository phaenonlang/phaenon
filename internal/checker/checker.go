// Package checker is Phaenon's type checker: local, bidirectional,
// flow-sensitive, over the program model. See design/checker.md.
package checker

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/tabs"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Options tune the checker.
type Options struct {
	// Deprecations reports calls to @deprecated declarations (P4006).
	Deprecations bool
	// Unfolded, when set, answers for a checked file what its PHP 8.5
	// lowering answers (emit.UnfoldedBound): the pipe stages kept from
	// their closure (program.Partial.Bound) whose run it does not write as
	// nested calls, which the build refuses. The checker reports each
	// (P1002), so that phn check refuses what phn build does whatever the
	// layout of the run; without it they are left to the build.
	Unfolded func(p *program.Program, f *program.File) []ast.Expr
	// ReflectedAttributes is phaenon.json's unused.reflected-attributes: when
	// not nil, a private property escapes P2406–P2408 only through an
	// attribute whose class is one of these or extends or implements one.
	// Nil keeps the default, any attribute but PHP's own; an empty list lets
	// none (design/tooling.md §9.2).
	ReflectedAttributes []string
	// NoExtraChecks leaves out the extra checks, the findings PHPStan does
	// not make at level max (Extra, kinds.go): phaenon.json's
	// "extra-checks": false, or --no-extra-checks. The zero value keeps
	// them, which is the default (design/roadmap.md D33). What is returned
	// with it set is what is returned without it, less the extra checks.
	NoExtraChecks bool
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
	// Kind says why the finding is reported: a language rule, a parity
	// finding, or an extra check, which Options.NoExtraChecks leaves out
	// (kinds.go).
	Kind    Kind
	Message string
	// Related are the declarations the finding is about ("declared here"):
	// the parameter an argument does not fit, the method an override
	// breaks. phn lsp shows them beside the finding.
	Related []Related
	key     findingKey // the dedupe key, so a rollback can forget the diagnostic
	// silencedBy is the ignore comment that silences the finding: it is
	// kept, and rolled back as any finding is, until Check knows which
	// comments silenced something, and then left out.
	silencedBy *ignoreRule
	// refused marks a finding about code PHP itself refuses: a compile
	// error, or a fatal error when the file is loaded and its classes are
	// declared or linked. No ignore comment silences it (reportRefused).
	refused bool
}

// SyntaxError is the P1000 of a file that does not parse (err, from
// parser.Parse or Program.AddFile): at the token the parser stopped at,
// which a caret underlines as any finding's span. An error of another
// kind is the file's, at its start.
func SyntaxError(path string, src []byte, err error) Diagnostic {
	d := Diagnostic{File: path, Line: 1, Col: 1, EndLine: 1, EndCol: 1, Severity: P1000.Severity, Code: P1000.ID, Message: err.Error()}
	pe, ok := err.(*parser.Error)
	if !ok {
		return d
	}
	f := &program.File{Path: path, Src: src}
	d.Line, d.Col = f.Position(pe.Offset)
	d.EndLine, d.EndCol = d.Line, d.Col
	if end := pe.End - 1; end > pe.Offset {
		d.EndLine, d.EndCol = f.Position(end)
	}
	d.EndCol++
	d.Message = pe.Msg
	return d
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
	// seen are the classes checked, in the order of seenLog, which a
	// rollback forgets them by; reported, the findings made, each once
	// (findingKey).
	seen     map[string]bool
	seenLog  []string
	reported map[findingKey]bool
	// guards are the version guards around the code being checked
	// (guards.go).
	guards []libraryGuard
	file   *program.File
	// callSeq numbers call sites so a callee's type parameters get fresh
	// identities per call (T of the callee is not T of the caller).
	callSeq int
	// ignored lists, per file and line, the ignore comments that silence
	// findings there (@phpstan-ignore, @phaenon-ignore): a checked file's,
	// read when the first finding is reported in it or when it is checked,
	// whichever comes first (rules). judged are the files checked to their
	// end, whose comments settleIgnores holds to what they silenced.
	ignored map[string]map[int][]*ignoreRule
	judged  map[string]bool
	// recvs, dynNames and byRefArgs are what the unused-member pass reads
	// (unused.go): the receiver type of each member access in a class body,
	// the type of each dynamic member name, and the properties passed by
	// reference.
	recvs     map[ast.Node]types.Type
	dynNames  map[ast.Node]types.Type
	byRefArgs map[ast.Node]bool
	// refVars are the variables passed by reference, or whose element is,
	// which a try's catch blocks see bound (tryStmt) and a pipe's bound
	// argument may not pass (boundInClosure).
	refVars map[*ast.Variable]bool
	// unknownCallee are the calls through a value whose type gives no
	// parameter list (mixed, a callable or a Closure without a signature,
	// a string, an array), which may take any argument by reference.
	unknownCallee map[*ast.Call]bool
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
	// intArith says of each arithmetic expression over integers how
	// PHPStan types it, where that is proven wider than its type here
	// (noteArithmetic, extra.go); reportUnfit reads it.
	intArith map[*ast.Binary]intArith
	// targetedLabels says, per file, which labels a goto names
	// (labelTargeted).
	targetedLabels map[*program.File]map[*ast.Label]bool
	// attrFlags caches the flags of PHP's attribute classes, -1 for a
	// class that is none (attributeFlags).
	attrFlags map[*program.Class]int
	// caseTypes are the types of backed enum cases' values, each typed
	// once where its enum is declared (caseValue), as a constant's is
	// (program.ConstantType); caseTyping are the cases whose value is being
	// typed, which a value that reads itself meets again.
	caseTypes  map[*program.EnumCase]types.Type
	caseTyping map[*program.EnumCase]bool
	// refs are, per class constant and enum case, the references its value
	// follows whenever PHP evaluates it, and cycles how it reads itself, nil
	// where it does not (selfref.go).
	refs   map[valueDecl][]valueRef
	cycles map[valueDecl]*cycle
	// atOnce maps the argument list of a partial application to the call
	// that calls it where it stands with arguments, `(f(B, ...))(X)`
	// (Partial.Direct).
	atOnce map[*ast.ArgList]*ast.Call
	// refusedStatic holds the static references in constant expressions
	// reported before the file is typed (lateStatic).
	refusedStatic map[ast.Expr]bool
}

// New creates a checker for the program.
func New(p *program.Program, opts Options) *Checker {
	c := &Checker{p: p, opts: opts, seen: map[string]bool{}, reported: map[findingKey]bool{}, ignored: map[string]map[int][]*ignoreRule{}, judged: map[string]bool{}}
	p.ConstType = c.constType
	return c
}

// ignoreRule is one ignore comment and the line it silences. A
// `@phpstan-ignore …` silences every finding of its line a comment may
// silence (silenceable, and none about code PHP refuses), as it always
// has: how PHPStan's identifiers would match Phaenon's codes is not
// settled. A `@phaenon-ignore P2103, P2202` silences the codes it names
// and no other, and like `@phaenon-ignore-line` and
// `@phaenon-ignore-next-line`, which silence every finding, it is itself
// reported (P4015) where it silences nothing, as PHPStan reports its own
// (ignore.unmatchedIdentifier, ignore.unmatchedLine).
type ignoreRule struct {
	line int
	// at is the comment's tag, where a rule that silences nothing is
	// reported; word is the tag as written.
	at   ast.Span
	word string
	// all marks a comment that silences every finding of its line; codes
	// are the codes a @phaenon-ignore names, the words after its tag up to
	// the first that is no code, where its prose begins.
	all   bool
	codes []string
	// native marks a @phaenon-ignore form, held to what it silences.
	native bool
	used   map[string]bool // the codes it silenced
}

// silences reports whether the rule silences a finding of the code.
func (r *ignoreRule) silences(code string) bool {
	return r.all || slices.Contains(r.codes, code)
}

// ignoreRules finds the ignore comments: `// @phpstan-ignore …` (or
// `@phaenon-ignore …`) at the end of a line silences that line, on its own
// line the next one; `-ignore-line` always silences its own line and
// `-ignore-next-line` the next.
func ignoreRules(src []byte, toks []token.Token) map[int][]*ignoreRule {
	out := map[int][]*ignoreRule{}
	for i, t := range toks {
		if t.Kind != token.T_COMMENT && t.Kind != token.T_DOC_COMMENT {
			continue
		}
		text := t.Text(src)
		r := parseIgnore(text)
		if r == nil {
			continue
		}
		r.at = ast.Span{Start: t.Start + r.at.Start, End: t.Start + r.at.End}
		sameLine := strings.HasSuffix(r.word, "-ignore-line")
		if !sameLine && !strings.HasSuffix(r.word, "-ignore-next-line") {
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
			r.line = t.Line
			out[r.line] = append(out[r.line], r)
			continue
		}
		for j := i + 1; j < len(toks); j++ {
			if !toks[j].Kind.IsTrivia() {
				r.line = toks[j].Line
				out[r.line] = append(out[r.line], r)
				break
			}
		}
	}
	return out
}

// reIgnore is an ignore tag and what follows it on its line: the
// identifiers, comma-separated, up to a parenthesised reason. The tag starts
// the comment's text or follows a space or the comment's own marks: one
// quoted in prose (`@phaenon-ignore P2103`) is no tag.
var reIgnore = regexp.MustCompile(`(?:^|[\s/*#])(@(phpstan|phaenon)-ignore(-next-line|-line)?)\b([^\n(]*)`)

// reCode is a diagnostic code as phn writes it.
var reCode = regexp.MustCompile(`^P[1-4][0-9]{3}$`)

// parseIgnore reads the ignore tag of a comment, nil without one; at is
// the tag's span within the comment.
func parseIgnore(text string) *ignoreRule {
	m := reIgnore.FindStringSubmatchIndex(text)
	if m == nil {
		return nil
	}
	r := &ignoreRule{at: ast.Span{Start: m[2], End: m[3]}, word: text[m[2]:m[3]], used: map[string]bool{}}
	r.native = text[m[4]:m[5]] == "phaenon"
	if !r.native || m[6] >= 0 {
		r.all = true // every finding of the line
		return r
	}
	// The names end at a reason in parentheses (the pattern's stop), at the
	// end of a block comment, or at a second `//`, after which a line
	// comment says something else.
	rest := text[m[8]:m[9]]
	for _, stop := range []string{"*/", "//"} {
		rest, _, _ = strings.Cut(rest, stop)
	}
	// The codes run up to the first word that is none, where the prose
	// after them begins; a code that punctuation ends (`P2103: why`,
	// `P2103.`, `P2103!`) is still the code, and the last one.
	for _, f := range strings.FieldsFunc(rest, func(c rune) bool { return c == ',' || c == ' ' || c == '\t' }) {
		code := strings.TrimRightFunc(f, func(c rune) bool { return !unicode.IsLetter(c) && !unicode.IsDigit(c) })
		if !reCode.MatchString(code) {
			break
		}
		r.codes = append(r.codes, code)
		if code != f {
			break
		}
	}
	return r
}

// without is the advice to a comment that names a code no comment
// silences (P4015, a code of the run or of the syntax): to take the code out where another
// code it names silences something, or may in a run that reports the code
// (leftOut), and otherwise to remove the comment, which silences nothing:
// left with no code, or with codes that silence nothing, it is P4015 again.
func (c *Checker) without(r *ignoreRule, code string) string {
	for _, other := range r.codes {
		if silenceable(other) && (r.used[other] || c.leftOut(other)) {
			return "take " + code + " out of the comment"
		}
	}
	return "remove the comment"
}

// codesFor is the advice to a comment that names no code: the codes still
// reported on its line that a comment silences, which it is for; where there are none, to remove
// it, unless the run leaves some code's findings out (leavesOut) and one
// of those may be there. Advice that a comment followed would make P4015
// again is not given: a code the line does not report, or none, or one
// whose finding there is about code PHP refuses.
func (c *Checker) codesFor(path string, line int, word string) string {
	var codes []string
	for _, d := range c.diags {
		if d.File == path && d.Line == line && silenceable(d.Code) && !d.refused && !slices.Contains(codes, d.Code) {
			codes = append(codes, d.Code)
		}
	}
	switch {
	case len(codes) > 0:
		sort.Strings(codes)
		return fmt.Sprintf("name the codes it is for, here `%s %s`", word, strings.Join(codes, ", "))
	case c.leavesOut():
		return fmt.Sprintf("name the codes it is for, as in `%s P2103`, or remove it", word)
	}
	return fmt.Sprintf("no finding a comment silences is reported on line %d, so remove it", line)
}

// rules returns the ignore comments of a checked file by the line each
// silences. They are read when the first finding is reported in the file
// or when the file is checked, whichever comes first: a constant's value
// is checked from the file that reads it and a trait's body from the class
// that uses it, either of which may be checked before the file the finding
// is in, and a comment there silences the finding in whichever order the
// files come. A file that is not checked has none; nothing is reported in
// it.
func (c *Checker) rules(f *program.File) map[int][]*ignoreRule {
	if f == nil || !f.Checked {
		return nil
	}
	rules, ok := c.ignored[f.Path]
	if !ok {
		rules = ignoreRules(f.Src, lexer.Tokenize(f.Src, f.Mode))
		c.ignored[f.Path] = rules
	}
	return rules
}

// silencing returns the ignore comment that silences a finding of the code
// on a line of the file, nil when none does.
func (c *Checker) silencing(f *program.File, line int, code string) *ignoreRule {
	for _, r := range c.rules(f)[line] {
		if r.silences(code) {
			return r
		}
	}
	return nil
}

// leftOut reports a code whose findings this run does not report: the
// deprecations, of the language (P1101) and of the library (P4006),
// without Options.Deprecations, as in every phn build and under phn check
// --no-deprecations. An ignore comment is not held to such a code: whether
// it silences anything is known only to a run that reports it, and a
// comment that is right must not warn in every build.
func (c *Checker) leftOut(code string) bool {
	return !c.opts.Deprecations && (code == P1101.ID || code == P4006.ID)
}

// leavesOut reports a run that leaves some code's findings out (leftOut):
// a comment that silences every finding of its line is not held to what
// it silenced there, since the finding it is for may be one of those.
func (c *Checker) leavesOut() bool {
	return !c.opts.Deprecations
}

// settleIgnores leaves the silenced findings out, and reports the
// @phaenon-ignore comments that silenced nothing they name (P4015), in the
// files checked to their end. A comment is held to the codes this run
// reports and no other (leftOut). P4015 itself is silenced by no comment,
// or a stale suppression could be hidden by naming it, and a comment that
// names it is told so. So is one that names the code of a finding about
// code PHP refuses on its line, or about code that does something else
// than it reads (otherThanItReads), where it silences no other finding of
// that code.
func (c *Checker) settleIgnores() {
	type lineCode struct {
		file string
		line int
		code string
	}
	// standing are the findings no comment silences that a comment may
	// name on their line: about code PHP refuses, or code that does
	// something else than it reads.
	standing := map[lineCode]bool{}
	kept := c.diags[:0]
	for _, d := range c.diags {
		if d.refused || otherThanItReads[d.Code] {
			standing[lineCode{d.File, d.Line, d.Code}] = true
		}
		if d.silencedBy != nil {
			// Every comment that covers the line and names the code silenced
			// it, not the first alone: one above the line and one after it,
			// or a -next-line form and a trailing one.
			for _, r := range c.ignored[d.File][d.Line] {
				if r.silences(d.Code) {
					r.used[d.Code] = true
				}
			}
			continue
		}
		kept = append(kept, d)
	}
	c.diags = kept
	for _, f := range c.p.CheckedFiles() {
		if !c.judged[f.Path] {
			continue
		}
		rules := c.ignored[f.Path]
		lines := make([]int, 0, len(rules))
		for line := range rules {
			lines = append(lines, line)
		}
		sort.Ints(lines)
		c.file = f
		for _, line := range lines {
			for _, r := range rules[line] {
				if !r.native {
					continue
				}
				switch {
				case r.all:
					if len(r.used) == 0 && !c.leavesOut() {
						c.report(r.at, P4015, "%s: no finding a comment silences is reported on line %d", r.word, line)
					}
				case r.codes == nil:
					c.report(r.at, P4015, "%s names no code, and silences nothing: %s", r.word, c.codesFor(f.Path, line, r.word))
				default:
					// One finding for each code that silences nothing, as PHPStan
					// reports each identifier (ignore.unmatchedIdentifier).
					for _, code := range r.codes {
						switch {
						case code == P4015.ID:
							c.report(r.at, P4015, "%s: no comment silences P4015, or one that silences nothing could hide itself: %s", r.word, c.without(r, code))
						case ofTheRun[code]:
							c.report(r.at, P4015, "%s: %s is no finding of a line, and no comment silences it: %s", r.word, code, c.without(r, code))
						case ofTheSyntax[code]:
							c.report(r.at, P4015, "%s: %s says the code is not Phaenon as written, and no comment silences it: %s", r.word, code, c.without(r, code))
						case !r.used[code] && standing[lineCode{f.Path, line, code}] && otherThanItReads[code]:
							c.report(r.at, P4015, "%s: %s on line %d is about code that compiles to PHP doing something else than it reads, and no comment silences it: %s", r.word, code, line, c.without(r, code))
						case !r.used[code] && standing[lineCode{f.Path, line, code}]:
							c.report(r.at, P4015, "%s: %s on line %d is about code PHP refuses to compile or load, and no comment silences it: %s", r.word, code, line, c.without(r, code))
						case !r.used[code] && !c.leftOut(code):
							c.report(r.at, P4015, "%s: no %s is reported on line %d", r.word, code, line)
						}
					}
				}
			}
		}
	}
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
		c.settleUnmoved(f)
		c.judged[f.Path] = true
	}
	c.checkReadonlyInits()
	c.settleUnrecorded()
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
	// The comments first, the extra checks after them: a comment that
	// silences an extra check has silenced a finding of this program, and
	// is held to it with the extra checks off as with them on.
	c.settleIgnores()
	c.settleExtraChecks()
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

// settleExtraChecks is the last word on the extra checks, once every
// finding is in: an extra check that a finding of another kind repeats, at
// the same place in the same words, is that finding, and with
// Options.NoExtraChecks the extra checks are left out.
//
// They are left out here and not where they are reported, so that a check
// with the switch off is the check with it on, less its extra checks, and
// nothing else: a pass that is rolled back, a finding that is reported
// once, a decision taken from whether a type drew findings
// (typeFindings) all see the same findings in both modes. Whatever else
// reads the findings before they are returned comes before this call: an
// ignore comment that silences an extra check has silenced something, with
// the switch off too.
func (c *Checker) settleExtraChecks() {
	extra := 0
	for _, d := range c.diags {
		if d.Kind == Extra {
			extra++
		}
	}
	if extra == 0 {
		return
	}
	kept := c.diags[:0]
	for _, d := range c.diags {
		twin := d.key
		twin.extra = false
		if d.Kind == Extra && (c.opts.NoExtraChecks || c.reported[twin]) {
			continue
		}
		kept = append(kept, d)
	}
	c.diags = kept
}

// report records a finding at a span of the current file: a language rule
// where its code is one (languageRules), a parity finding otherwise. A
// finding PHPStan does not make is an extra check where the place that
// reports it can prove that (extra.go).
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
	c.reportOf(Parity, false, span, code, rel, format, args...)
}

// reportRefused is reportRelated for a finding that may be about code PHP
// itself refuses, which refused says: a compile error that `php -l`
// reports, or a fatal error when the file is loaded and its classes are
// declared or linked. Such a finding is silenced by no ignore comment, as
// the compiled file would not run at all: a comment accepts a finding
// about what code does, and code PHP refuses does nothing. The place that
// reports it decides, finding by finding, since a code may mean such code
// in one finding and not in another: an incompatible native signature is
// fatal when the class is declared, a docblock that PHP never reads is
// not.
func (c *Checker) reportRefused(refused bool, span ast.Span, code Code, rel *Related, format string, args ...any) {
	c.reportOf(Parity, refused, span, code, rel, format, args...)
}

// reportRefused is the checker's, for a finding without a declaration.
func (x *fnCtx) reportRefused(refused bool, span ast.Span, code Code, format string, args ...any) {
	x.c.reportRefused(refused, span, code, nil, format, args...)
}

// relateAlso adds rel to the related positions of the finding reported
// since mark, where one was: a finding that was already reported, in the
// same words at the same place, keeps what it has.
func (c *Checker) relateAlso(mark checkMark, rel *Related) {
	if rel != nil && len(c.diags) > mark.diags {
		d := &c.diags[len(c.diags)-1]
		d.Related = append(d.Related, *rel)
	}
}

// findingKey is what makes two findings one: the place, the code and the
// words. An extra check is kept apart from a finding of another kind at the
// same place in the same words, which is reported, rolled back and
// reported again on its own (settleExtraChecks then leaves the extra one
// out); the kind is a field of the key and no text in it, which a message
// could spell.
type findingKey struct {
	file      string
	line, col int
	code, msg string
	extra     bool
}

// reportOf records a finding of the kind its place gives it; a code that
// is a language rule as a whole makes it one whatever the place says.
// refused marks a finding about code PHP refuses (reportRefused), which no
// comment silences; a finding reported again with the mark takes it.
func (c *Checker) reportOf(kind Kind, refused bool, span ast.Span, code Code, rel *Related, format string, args ...any) {
	if languageRules[code.ID] {
		kind = LanguageRule
	}
	msg := fmt.Sprintf(format, args...)
	line, col := c.file.Position(span.Start)
	end := span.End
	if end > span.Start {
		end--
	}
	endLine, endCol := c.file.Position(end)
	key := findingKey{file: c.file.Path, line: line, col: col, code: code.ID, msg: msg, extra: kind == Extra}
	if c.reported[key] {
		if refused {
			for i := range c.diags {
				if c.diags[i].key == key {
					c.diags[i].refused, c.diags[i].silencedBy = true, nil
				}
			}
		}
		return
	}
	c.reported[key] = true
	d := Diagnostic{File: c.file.Path, Line: line, Col: col, EndLine: endLine, EndCol: endCol + 1,
		Severity: code.Severity, Code: code.ID, Kind: kind, Message: msg, key: key, refused: refused}
	if silenceable(code.ID) && !refused {
		d.silencedBy = c.silencing(c.file, line, code.ID)
	}
	if rel != nil {
		d.Related = []Related{*rel}
	}
	c.diags = append(c.diags, d)
}

// checkMark is a point of the check rollback returns to: the number of
// diagnostics and of classes checked so far.
type checkMark struct{ diags, seen int }

// mark returns the point the check has reached; rollback(mark) discards
// the diagnostics reported since and forgets them, so a later pass over
// the same code — a loop's next iteration, the real evaluation after a
// lenient one — can report them again. It forgets the classes checked
// since as well: an anonymous class in that code is the same class in
// the later pass (Program.DeclareAnonymous), whose body is to be checked
// again for its findings to be kept.
func (c *Checker) mark() checkMark { return checkMark{len(c.diags), len(c.seenLog)} }

func (c *Checker) rollback(m checkMark) {
	for _, d := range c.diags[m.diags:] {
		delete(c.reported, d.key)
	}
	c.diags = c.diags[:m.diags]
	for _, key := range c.seenLog[m.seen:] {
		delete(c.seen, key)
	}
	c.seenLog = c.seenLog[:m.seen]
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
	toks := lexer.Tokenize(f.Src, f.Mode)
	if _, read := c.ignored[f.Path]; !read {
		c.ignored[f.Path] = ignoreRules(f.Src, toks)
	}
	if phaenon {
		c.checkTabs(f)
		c.checkWords(f)
	}
	c.lateStatic(f)
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
	c.ownAttributes(f)
	c.withdrawnSpellings(f)
	c.genericClosures(f)
	c.partialSyntax(f)
	c.checkUses(f, uses, toks)
}

// checkUses reports the imports nothing in the file uses (P2013). A use is
// a token that names a class, a function or a constant, or a mention in a
// docblock, whose types PHPStan and the checker read; program.ImportUses
// has the rule.
func (c *Checker) checkUses(f *program.File, uses []*ast.Use, toks []token.Token) {
	if len(uses) == 0 {
		return
	}
	alias := func(it *ast.UseItem) string {
		if it.Alias != nil {
			return strings.ToLower(it.Alias.Name)
		}
		return strings.ToLower(it.Name.Parts[len(it.Name.Parts)-1])
	}
	used := map[string]bool{}
	statements := make([]ast.Span, 0, len(uses))
	for _, u := range uses {
		statements = append(statements, u.S)
		for _, it := range u.Items {
			used[alias(it)] = false
		}
	}
	// The identifiers of the tree are the names that refer to no class,
	// function or constant: a declaration's own name, a member's, a named
	// argument's, a label's.
	idents := map[int]bool{}
	ast.Inspect(f.AST, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			idents[id.S.Start] = true
		}
		return true
	})
	wanted := make(map[string]bool, len(used))
	for name := range used {
		wanted[name] = true
	}
	program.ImportUses(f.Src, toks, statements, idents, wanted, func(name string, _ ast.Span, _ bool) bool {
		used[name] = true
		return false // one use is all an import needs
	})
	for _, u := range uses {
		for _, it := range u.Items {
			if !used[alias(it)] {
				c.report(it.S, P2013, "import %s is not used", strings.Join(it.Name.Parts, "\\"))
			}
		}
	}
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
	bindThis *thisBinding
	// untouched are the parameters of the function or method being
	// checked that its body names exactly once, in a read
	// (untouched.go); arith holds the two sides of the arithmetic
	// being typed. Both serve the proof an extra check needs (extra.go).
	untouched    untouched
	arith        arithOperands
	static       bool // no $this
	inConst      bool // checking a constant expression: an attribute argument, a constant, a default
	constClosure bool // a closure inside a constant expression: static, capturing nothing (P2531)
	nameOnly     bool // resolving a class name PHP does not load: X::class, instanceof X, catch (X)
	inDocblock   bool // resolving a name the compiled file writes in a docblock alone: a throws clause's
	// hookRefusesYield marks a property hook's body whose return type does
	// not admit a Generator, void for set and the property's native type
	// for get: PHP refuses a yield there when it compiles the file (P2315).
	hookRefusesYield bool
	inFinally        int // depth of finally blocks around the statement being checked
	// matchCaught counts the try blocks around the statement being checked
	// whose catches take the UnhandledMatchError of a match without a
	// matching arm (catchesUnhandledMatch): there such a match is no
	// P2304. A closure's context starts at none, as the error leaves it.
	matchCaught int
	// partialRecv is the receiver's type of each method call a partial
	// application makes, whose classes say which methods may run.
	partialRecv map[*ast.MethodCall]types.Type
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
	// returnExpr is the declared return type as written, where the
	// signature has one (generatorReturnType).
	returnExpr ast.TypeExpr
	res        *program.Resolver
	byRef      map[string]bool
	arrow      bool
	// method/function being checked (for @phpstan-assert, by-ref params)
	method   *program.Method
	function *program.Function
	// enclosing is the context a closure is written in, nil for a
	// function's or a method's own: the type parameters it names are
	// declared there (boundText).
	enclosing *fnCtx
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
	// skippedChain marks the members of a chain PHP skips whole: a ?-> on
	// a value that is null, `$n?->a()` and what follows it, `->b`, all
	// null and reported once (skipChain).
	skippedChain map[ast.Expr]bool
	// condValueType is the type of the last condition cond() evaluated
	// (the short ternary `c ?: d` needs it).
	condValueType types.Type
	// loop bookkeeping: environments at break/continue statements of the
	// innermost loop or switch.
	breakEnvs    []*state
	continueEnvs []*state
	loopDepth    int
	// loopBack and loopBreaks are the back edge and the breaks of the last
	// iteration of the loop fixpoint ran last: what leaves a loop whose
	// body runs at least once (leftAfterOnce).
	loopBack   *state
	loopBreaks []*state
	// refTaken are the variables a reference may have been taken to where
	// the checker does not follow it: an `&$v` in an array literal, an
	// argument of a callee the checker cannot see. Neither they nor byRef
	// decide that a loop is entered (entersOnce).
	refTaken map[string]bool
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
	// writeOffsets are the offsets a reference is taken to, `$v = &$a['k']`,
	// which PHP creates when they are missing, and those whose key is taken
	// from the array itself, `$a[array_key_first($a)]`: no P2321 for them,
	// as PHPStan has none. `.=` and `++` read the offset first, and are
	// P2321.
	writeOffsets map[*ast.Index]bool
	// stmtExpr is the expression of the statement being checked: an
	// assignment that is not it is a part of an expression, a
	// condition's (rememberKeyOf).
	stmtExpr ast.Expr
	// truthyAssign is an assignment a condition tests for truthiness
	// itself, `if ($k = array_key_first($r))`, while it is checked.
	truthyAssign *ast.Assign
	// quietOffsets is above zero while the object of a member that
	// isset(), empty() or ?? tests is typed, `$r['o']` of `isset($r['o']->p)`:
	// PHP reads the whole chain as quietly as its last link, so an offset in
	// it that may be missing is no P2321, as PHPStan has it.
	quietOffsets int
	// constKeys caches the key of an offset a class constant names
	// (constOffsetKey), "" where it has none.
	constKeys map[*ast.ClassConstFetch]string
	// argKnown holds a pipe's left side, typed before the stage it feeds
	// is checked as a call that takes it as an argument; pipeOperand is
	// that expression while its stage is checked, and pipeParam the
	// parameter it was bound to (design/php-versions.md §5.4).
	argKnown map[ast.Expr]types.Type
	// wished is, for a partial application that is an argument of a call
	// being checked, the callable type the call's own expected type makes
	// of its parameter (partialChoice.join).
	wished      map[ast.Expr]types.Type
	pipeOperand ast.Expr
	pipeParam   *program.Param
	pipeSig     *signature
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

// scopeKnown says whether PHP knows, when it compiles the code being
// checked, the class that self, static and parent name there, and so
// refuses the file where there is none (P2012) or it has no parent
// (P2011): in the
// body of a named function and of a method outside a trait, where the
// class says `extends` nothing. A closure is bound to its scope when it is
// created, top-level code runs in the scope that includes the file, a
// trait's method in each class that uses it, and a constant expression is
// evaluated when its value is first needed. A throws clause compiles to
// a docblock, which PHP never reads.
func (x *fnCtx) scopeKnown() bool {
	if x.inConst || x.inDocblock {
		return false
	}
	if x.class == nil {
		return x.function != nil
	}
	return x.method != nil && x.class.Kind != ast.KindTrait && x.class.Parent == ""
}

// resolveClassName resolves a class name written in code, reporting unknown
// classes; "static"/"self"/"parent" map to the current class.
func (x *fnCtx) resolveClassName(n *ast.Name, span ast.Span) (string, bool) {
	if x.reportedName(n) {
		return "", false // a refused closure's type parameter (P1007)
	}
	name := x.res.ClassName(n)
	switch name {
	case "self", "static":
		if x.class == nil {
			x.reportRefused(x.scopeKnown(), span, P2012, "%s used outside a class", name)
			return "", false
		}
		return x.class.Name, true
	case "parent":
		if x.class == nil {
			x.reportRefused(x.scopeKnown(), span, P2012, "parent used outside a class")
			return "", false
		}
		x.reportRefused(x.scopeKnown(), span, P2011, "class %s has no parent", x.class.Name)
		return "", false
	}
	cl := x.p().Class(name)
	if cl == nil {
		x.report(span, P2001, "%s", x.c.unknownClass("class", name))
		return name, false
	}
	if !x.nameOnly {
		x.c.libraryClass(span, cl)
	}
	return name, true
}

// reportedName reports a name written as a class's that is the type
// parameter of a closure the checker refuses (P1007): it was reported
// with the closure, and nothing more follows from it, in a type position
// or a class name's (`new T()`, `T::class`, `instanceof T`, a catch).
func (x *fnCtx) reportedName(n *ast.Name) bool {
	return n != nil && len(n.Parts) == 1 && n.Kind == ast.NameUnqualified && x.res.Reported(n.Parts[0])
}

// classRefType returns the type denoted by a class name in `new`,
// `instanceof`, `Foo::` positions: static for `static`, the class otherwise.
func (x *fnCtx) classRefType(n *ast.Name, span ast.Span) (types.Type, *program.Class) {
	if x.reportedName(n) {
		return types.TError, nil // a refused closure's type parameter (P1007)
	}
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
			x.report(span, P2001, "%s", x.c.unknownClass("class", name))
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

// replaceStatic maps static/$this in t to the receiver type, wherever t
// holds one: a tuple's member, a shape's entry, an object shape's
// property, an intersection's member and a conditional's target as well as
// a type argument (types.MapDeep reaches every part of a type). A type
// that holds none is returned as it is.
func replaceStatic(t types.Type, recv types.Type) types.Type {
	if !mentionsStatic(t) {
		return t
	}
	return types.MapDeep(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Static, types.This:
			return staticOf(recv)
		case types.Intersection:
			// static&Countable on a receiver A|B: the members changed, so
			// the intersection is normalised again.
			return types.MakeIntersection(m.Types...)
		}
		return m
	})
}

// mentionsStatic reports a static or a $this anywhere in t.
func mentionsStatic(t types.Type) bool {
	found := false
	types.Walk(t, func(m types.Type) {
		switch m.(type) {
		case types.Static, types.This:
			found = true
		}
	})
	return found
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
	// checking silently. Nor are those of a value that reads itself, which
	// is reported once as such (P2016), in its class's check.
	silent := !k.File.Checked || k.Class != nil && c.cycleOf(valueDecl{k: k}) != nil
	var savedDiags checkMark
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
