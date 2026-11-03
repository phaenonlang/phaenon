package migrate

// The pipes codemod over a set of files, checked (design/tooling.md §8):
// the files are type-checked as the lift left them, the chains rewritten
// with what the checker knows, the rewritten files checked again; a file
// whose findings change, or in which the checker gives any expression a
// different type — a rewritten chain's pipe the type of the call it
// replaces, every other expression the type it had — gets its calls back.

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// PipeFile is one file of a pipes run: where it is written and its
// Phaenon source as the lift left it.
type PipeFile struct {
	Path string
	Src  []byte
}

// PipeOptions configure the pipes codemod.
type PipeOptions struct {
	// MinCalls is the length from which a chain is rewritten: 3, or 2
	// with --pipes=2. Below 2 the codemod is off (--keep-calls).
	MinCalls int
	// Load builds the program model of a run: srcs read as Phaenon at
	// their paths, the ones in checked as checked files, beside the
	// standard library, the project's other files and its vendor code.
	Load func(srcs map[string][]byte, checked map[string]bool) (*program.Program, error)
}

// PipeReport is what a pipes run did.
type PipeReport struct {
	Pipelines int            // chains written as pipelines
	Left      map[string]int // chains left as calls, by reason
	Undone    []Note         // per file (Line 0): why the check undid its pipelines
	UndoneIn  []string       // the files of Undone, in the same order
	// Notes holds, per path, a note for each chain left as calls, at its
	// line in the source given.
	Notes map[string][]Note
}

// LeftCalls is the number of chains left as calls.
func (r *PipeReport) LeftCalls() int {
	n := 0
	for _, c := range r.Left {
		n += c
	}
	return n
}

// Reasons lists the reasons with their counts, the most frequent first.
func (r *PipeReport) Reasons() string {
	type kv struct {
		k string
		v int
	}
	var all []kv
	for k, v := range r.Left {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	s := ""
	for i, e := range all {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%d %s", e.v, e.k)
	}
	return s
}

// Pipes rewrites the chains of files as pipelines and checks the result.
// It returns each file's source, rewritten or as it was.
func Pipes(files []PipeFile, opts PipeOptions) ([][]byte, *PipeReport, error) {
	out := make([][]byte, len(files))
	rep := &PipeReport{Left: map[string]int{}, Notes: map[string][]Note{}}
	for i, f := range files {
		out[i] = f.Src
	}
	if opts.MinCalls < 2 || opts.Load == nil {
		return out, rep, nil
	}
	srcs := map[string][]byte{}
	watch := map[string]bool{}
	for _, f := range files {
		srcs[f.Path] = f.Src
		if tree, err := parser.Parse(f.Src, lexer.ModePhaenon); err == nil && hasChain(tree, opts.MinCalls) {
			watch[f.Path] = true
		}
	}
	if len(watch) == 0 {
		return out, rep, nil
	}
	before, err := checkRecorded(srcs, watch, opts.Load)
	if err != nil {
		return nil, nil, err
	}
	results := map[string]*pipeResult{}
	rewritten := map[string][]byte{}
	for _, f := range files {
		if !watch[f.Path] {
			continue
		}
		pf := before.prog.File(f.Path)
		if pf == nil || pf.AST == nil {
			continue
		}
		res := rewritePipes(before.prog, pf, before.types[f.Path], opts.MinCalls)
		for k, v := range res.left {
			rep.Left[k] += v
		}
		if len(res.notes) > 0 {
			rep.Notes[f.Path] = res.notes
		}
		if res.pipelines == 0 {
			continue
		}
		if _, err := parser.Parse(res.out, lexer.ModePhaenon); err != nil {
			rep.undo(f.Path, res.pipelines, fmt.Sprintf("the pipelines do not parse (%v)", err))
			continue
		}
		results[f.Path] = res
		rewritten[f.Path] = res.out
	}
	if len(results) == 0 {
		return out, rep, nil
	}
	afterSrcs := map[string][]byte{}
	afterWatch := map[string]bool{}
	for path, src := range srcs {
		afterSrcs[path] = src
		if r, ok := rewritten[path]; ok {
			afterSrcs[path] = r
			afterWatch[path] = true
		}
	}
	after, err := checkRecorded(afterSrcs, afterWatch, opts.Load)
	if err != nil {
		return nil, nil, err
	}
	for i, f := range files {
		res, ok := results[f.Path]
		if !ok {
			continue
		}
		if why := compareChecks(f.Path, before, after, res); why != "" {
			rep.undo(f.Path, res.pipelines, why)
			continue
		}
		rep.Pipelines += res.pipelines
		out[i] = res.out
	}
	return out, rep, nil
}

func (r *PipeReport) undo(path string, pipelines int, why string) {
	r.Left[ReasonCheck] += pipelines
	r.Undone = append(r.Undone, Note{Msg: fmt.Sprintf("%d pipelines undone: %s", pipelines, why)})
	r.UndoneIn = append(r.UndoneIn, path)
}

// checked is one checker run over a program: its findings and, for the
// watched files, the type it settled on for each expression.
type checked struct {
	prog  *program.Program
	diags []checker.Diagnostic
	types map[string]map[ast.Span]types.Type
}

func checkRecorded(srcs map[string][]byte, watch map[string]bool, load func(map[string][]byte, map[string]bool) (*program.Program, error)) (*checked, error) {
	p, err := load(srcs, watch)
	if err != nil {
		return nil, err
	}
	c := &checked{prog: p, types: map[string]map[ast.Span]types.Type{}}
	// The checker also types expressions of other files while it checks
	// one — a callee's default, a constant's value — in that file's scope:
	// only a file's own nodes, typed while it is checked, are recorded.
	owned := map[ast.Expr]string{}
	for path := range watch {
		if f := p.File(path); f != nil && f.AST != nil {
			ast.Inspect(f.AST, func(n ast.Node) bool {
				if e, ok := n.(ast.Expr); ok {
					owned[e] = path
				}
				return true
			})
		}
	}
	observe := func(f *program.File, e ast.Expr, t types.Type) {
		path, ok := owned[e]
		if !ok || f == nil || f.Path != path {
			return
		}
		m := c.types[path]
		if m == nil {
			m = map[ast.Span]types.Type{}
			c.types[path] = m
		}
		m[e.Span()] = t
	}
	c.diags = checker.New(p, checker.Options{Deprecations: true, Observe: observe}).Check()
	return c, nil
}

// compareChecks says why a file's rewrite changed what the checker makes of
// it, "" when it did not.
func compareChecks(path string, before, after *checked, res *pipeResult) string {
	findings := func(c *checked) map[string]int {
		m := map[string]int{}
		for _, d := range c.diags {
			if d.File == path {
				m[fmt.Sprintf("%s %s: %s", d.Severity, d.Code, d.Message)]++
			}
		}
		return m
	}
	was, is := findings(before), findings(after)
	for k, n := range is {
		if was[k] < n {
			return "a finding appears: " + k
		}
	}
	for k, n := range was {
		if is[k] < n {
			return "a finding goes: " + k
		}
	}
	explicit := map[ast.Span]ast.Span{}
	for _, c := range res.calls {
		explicit[c.orig] = c.out
	}
	srcBefore := before.prog.File(path).Src
	newTypes := after.types[path]
	var spans []ast.Span
	for s := range before.types[path] {
		spans = append(spans, s)
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].Start != spans[j].Start {
			return spans[i].Start < spans[j].Start
		}
		return spans[i].End < spans[j].End
	})
	for _, s := range spans {
		t0 := before.types[path][s]
		ns, ok := explicit[s]
		if !ok {
			if ns, ok = res.mapSpan(s); !ok {
				continue
			}
		}
		t1, ok := newTypes[ns]
		if !ok {
			continue
		}
		if a, b := printType(t0), printType(t1); a != b {
			return fmt.Sprintf("`%s` is %s as calls and %s as a pipeline", clip(string(srcBefore[s.Start:s.End])), a, b)
		}
	}
	return ""
}

// reAnonymousLine is the line in an anonymous class's name, which a
// pipeline that moves the class to another line changes.
var reAnonymousLine = regexp.MustCompile(`(class@anonymous[^:\s]*):\d+(#\d+)`)

// printType prints a type for the comparison: anonymous classes by file
// and number, not line.
func printType(t types.Type) string {
	return reAnonymousLine.ReplaceAllString(types.Print(t), "$1$2")
}

// mapSpan finds where a span of the source went in the rewrite: both its
// ends inside text copied unchanged.
func (res *pipeResult) mapSpan(s ast.Span) (ast.Span, bool) {
	start, ok := res.mapOffset(s.Start)
	if !ok || s.End <= s.Start {
		return ast.Span{}, false
	}
	last, ok := res.mapOffset(s.End - 1)
	if !ok {
		return ast.Span{}, false
	}
	return ast.Span{Start: start, End: last + 1}, true
}

func (res *pipeResult) mapOffset(off int) (int, bool) {
	segs := res.segs
	i := sort.Search(len(segs), func(i int) bool { return segs[i].end > off })
	if i == len(segs) || segs[i].src > off {
		return 0, false
	}
	return segs[i].out + off - segs[i].src, true
}

// clip shortens an expression for a message.
func clip(s string) string {
	r := []rune(s)
	for i, c := range r {
		if c == '\n' {
			r = append(r[:i:i], []rune(" …")...)
			break
		}
	}
	if len(r) > 60 {
		r = append(r[:57:57], []rune("…")...)
	}
	return string(r)
}
