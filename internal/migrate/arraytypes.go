package migrate

// The list-types codemod (design/tooling.md §8.2), opt-in: a declaration
// written as a bare `array` becomes `list<T>`, `map<K, V>` or `array<V>`
// when every value it can hold is known to be one. The files are checked
// and the values read from what the checker recorded: a private
// property's default and writes, a private or final method's returns, a
// private method's arguments. It runs in rounds, since a method returning
// a property is known once the property is; a type that says mixed waits
// for the precise ones, and a type a round wrote is narrowed by a later
// one when what its values come from has been typed since (array_keys()
// of a bare array is a list<int|string>). A declaration whose values are
// made of its own (a recursion, a property filtered into itself) is typed
// by a self round, which reads its values with its own reads assumed, and
// so is one whose type would say mixed while its values read it. Every
// round's rewrite is checked with the files it may change: types may
// narrow, which is the point, so only findings are compared, and a
// declaration a new finding falls on is left out of the next round. A
// last pass cuts each type to the depth the code uses
// (arraytypes_depth.go).

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// ArrayTypeReport is what a list-types run did.
type ArrayTypeReport struct {
	Written int            // declarations given a list or map type
	Left    map[string]int // candidates left as they were, by reason
	Undone  []Note         // per file (Line 0): why the check undid a round
	// UndoneIn holds the files of Undone, in the same order.
	UndoneIn []string
	// Notes holds, per path, a note for each candidate left, at its line.
	Notes map[string][]Note
}

// Reasons lists the reasons candidates were left, the most frequent first.
func (r *ArrayTypeReport) Reasons() string {
	return (&PipeReport{Left: r.Left}).Reasons()
}

// LeftCount is the number of candidates left as they were.
func (r *ArrayTypeReport) LeftCount() int { return (&PipeReport{Left: r.Left}).LeftCalls() }

const (
	// arrayTypeRounds bounds the rounds of a pass, twice over (those that
	// write precise types and those that let mixed through): a method
	// returning a property is known the round after the property is, and a
	// parameter passed down a chain of methods one round per method. A
	// round ends the pass when it writes nothing, which narrowsAtMost
	// makes sure of: the bound is a backstop.
	arrayTypeRounds = 20
	// narrowsAtMost bounds the times a round narrows one declaration.
	narrowsAtMost = 3
	// arrayTypePasses bounds the passes: rounds until none types more, then
	// a self round, whose types the next pass carries further.
	arrayTypePasses = 3
	// selfProbes bounds the checks a self round makes to reach the types
	// its declarations' values settle on.
	selfProbes = 3
)

// ArrayTypes runs the codemod over files; load builds the program model as
// for Pipes. It returns each file's source, rewritten or as it was.
func ArrayTypes(files []PipeFile, load func(srcs map[string][]byte, checked map[string]bool) (*program.Program, error)) ([][]byte, *ArrayTypeReport, error) {
	r := &arrayRun{load: load, srcs: map[string][]byte{}, watch: map[string]bool{}, checked: map[string]bool{},
		current: map[string]*arrayTyper{}, dirty: map[string]bool{}, calls: map[string]map[string]bool{}, typedAs: map[string]map[string]string{}, narrows: map[string]map[string]int{},
		left: map[string]map[string]string{}, deny: map[string]map[string]string{},
		rep: &ArrayTypeReport{Left: map[string]int{}, Notes: map[string][]Note{}}}
	out := make([][]byte, len(files))
	for i, f := range files {
		out[i] = f.Src
		r.srcs[f.Path] = f.Src
		tree, err := parser.Parse(f.Src, lexer.ModePhaenon)
		if err != nil {
			continue
		}
		r.checked[f.Path] = true
		if declaresBareArray(tree) {
			r.watch[f.Path] = true
			r.dirty[f.Path] = true
		}
	}
	if len(r.watch) == 0 || load == nil {
		return out, r.rep, nil
	}
	for pass := 0; pass < arrayTypePasses; pass++ {
		var typers map[string]*arrayTyper
		converged := false // the last round typed nothing: its typers read the sources as they are
		r.lax = false
		for round := 0; round < 2*arrayTypeRounds; round++ {
			t, proposed, retry, err := r.round(nil)
			if err != nil {
				return nil, nil, err
			}
			typers = t
			switch {
			case !proposed && !r.lax:
				r.lax = true // no precise type is left to write: the others may say mixed
				continue
			case !proposed:
				converged = true
			case r.lax:
				r.lax = false // what they typed may let precise types follow
			}
			if converged {
				break
			}
			if retry && round == 2*arrayTypeRounds-1 {
				round-- // a refused declaration costs a round of its own
			}
		}
		r.lax = true
		if !converged {
			t, err := r.analyze()
			if err != nil {
				return nil, nil, err
			}
			typers = t
		}
		// What no round types may depend on itself: a recursive call, a
		// property filtered into itself, a parameter passed on.
		wrote, err := r.selfRound(typers)
		if err != nil {
			return nil, nil, err
		}
		if converged && !wrote {
			break
		}
	}
	if err := r.shallow(); err != nil {
		return nil, nil, err
	}
	r.notes()
	for i, f := range files {
		out[i] = r.srcs[f.Path]
	}
	return out, r.rep, nil
}

// arrayRun is one run of the codemod: the sources as the rounds left them,
// the files it still types, and what the check refused.
//
// A round changes a few files, and what a file's check finds depends only
// on its source and the declarations it reads. The declarations the
// codemod types are private but for a final method's return, which
// reaches the files that call it. So the first check checks every file
// given, and a later one only the files a round may have changed: the
// ones it rewrote, and the ones that name a method whose return it typed.
// Every other file keeps the reading of its last check.
type arrayRun struct {
	load       func(map[string][]byte, map[string]bool) (*program.Program, error)
	srcs       map[string][]byte
	watch      map[string]bool // the files whose declarations it types
	checked    map[string]bool // the files given
	base       *checked        // the findings before any round, which no round may add to
	baseByFile map[string][]checker.Diagnostic
	left       map[string]map[string]string
	deny       map[string]map[string]string // per file, candidates a check refused, with the finding
	rep        *ArrayTypeReport
	lax        bool // the rounds may write types that say mixed
	// current holds each watched file's reading of its last check, which
	// stays right until the file is dirty: rewritten, or naming a return
	// a round typed.
	current map[string]*arrayTyper
	dirty   map[string]bool
	calls   map[string]map[string]bool // per file, the method names it may call, lower-cased
	// typedAs holds, per file, the declarations the rounds typed and the
	// type each was given, which a later round narrows when its values say
	// less: what they come from may have been typed since.
	typedAs map[string]map[string]string
	narrows map[string]map[string]int // per file, how often a round narrowed each
}

// typed is what one check records for the codemod beside the expressions'
// types.
type typed struct {
	byRef     map[*program.File][]byRefArg
	callables map[ast.Expr]types.Type
	returned  map[*ast.Return]types.Type
}

// check checks the current sources of files, recording what the codemod
// reads of the watched ones; assume, when not nil, is the self round's
// Assume hook. The first check checks every file given: its findings are
// the base.
func (r *arrayRun) check(files map[string]bool, assume func(*program.File, ast.Node) types.Type) (*checked, *typed, error) {
	if r.base == nil && assume == nil {
		files = r.checked
	}
	cur, t, err := r.checkSources(r.srcs, files, assume)
	if err == nil && r.base == nil && assume == nil {
		r.base, r.baseByFile = cur, byFile(cur.diags)
	}
	return cur, t, err
}

// byFile groups findings by their file.
func byFile(diags []checker.Diagnostic) map[string][]checker.Diagnostic {
	out := map[string][]checker.Diagnostic{}
	for _, d := range diags {
		out[d.File] = append(out[d.File], d)
	}
	return out
}

// checkSources checks files of srcs as check does.
func (r *arrayRun) checkSources(srcs map[string][]byte, files map[string]bool, assume func(*program.File, ast.Node) types.Type) (*checked, *typed, error) {
	record := map[string]bool{}
	for path := range files {
		if r.watch[path] {
			record[path] = true
		}
	}
	t := &typed{byRef: map[*program.File][]byRefArg{}, callables: map[ast.Expr]types.Type{}, returned: map[*ast.Return]types.Type{}}
	cur, err := checkSome(srcs, files, record, r.load, func(o *checker.Options) {
		o.ByRef = func(f *program.File, e ast.Expr, out types.Type) { t.byRef[f] = append(t.byRef[f], byRefArg{e, out}) }
		o.CallableArg = func(f *program.File, e ast.Expr, param types.Type) {
			switch e.(type) {
			case *ast.ArrayLit, *ast.MethodCall, *ast.StaticCall:
				t.callables[e] = param
			}
		}
		o.Returned = func(f *program.File, s *ast.Return, rt types.Type) { t.returned[s] = rt }
		o.Assume = assume
	})
	if err != nil {
		return nil, nil, err
	}
	return cur, t, nil
}

// typer is the arrayTyper of one watched file in a check.
func (r *arrayRun) typer(cur *checked, t *typed, path string) *arrayTyper {
	pf := cur.prog.File(path)
	if pf == nil || pf.AST == nil {
		return nil
	}
	return &arrayTyper{prog: cur.prog, file: pf, types: cur.types[path], byRef: t.byRef[pf], callables: t.callables,
		returned: t.returned, left: map[string]string{}, deny: r.deny[path], lax: r.lax, typedAs: r.typedAs[path], narrows: r.narrows[path]}
}

// refresh checks the dirty files and returns each watched file's
// reading, as of its sources now, ready to run.
func (r *arrayRun) refresh() (map[string]*arrayTyper, error) {
	if len(r.dirty) > 0 {
		cur, t, err := r.check(r.dirty, nil)
		if err != nil {
			return nil, err
		}
		for path := range r.dirty {
			delete(r.current, path)
			if !r.watch[path] {
				continue
			}
			if a := r.typer(cur, t, path); a != nil {
				r.current[path] = a
			}
		}
		r.dirty = map[string]bool{}
	}
	typers := map[string]*arrayTyper{}
	for path, a := range r.current {
		if r.watch[path] {
			a.deny, a.lax, a.only, a.typedAs, a.narrows = r.deny[path], r.lax, nil, r.typedAs[path], r.narrows[path]
			typers[path] = a
		}
	}
	return typers, nil
}

// changed marks the readings a kept rewrite of path may change: the
// file's, and those of the watched files that call a method whose return
// it typed. A file with nothing to type needs no reading.
func (r *arrayRun) changed(path string, exported map[string]string) {
	delete(r.calls, path)
	if r.watch[path] {
		r.dirty[path] = true
	}
	var names []string
	for _, n := range exported {
		names = append(names, strings.ToLower(n))
	}
	for p := range r.mentioning(names) {
		if r.watch[p] {
			r.dirty[p] = true
		}
	}
}

// mentioning lists the files given that may call a method of one of the
// lower-cased names: every file that calls one, on any object, and some
// that do not.
func (r *arrayRun) mentioning(names []string) map[string]bool {
	out := map[string]bool{}
	if len(names) == 0 {
		return out
	}
	for path := range r.checked {
		calls, ok := r.calls[path]
		if !ok {
			calls = methodNames(r.srcs[path])
			r.calls[path] = calls
		}
		for _, n := range names {
			if calls[n] {
				out[path] = true
				break
			}
		}
	}
	return out
}

// methodNames lists, lower-cased, the names a file may call a method by:
// those of its method and static calls (a first-class callable among
// them), and the strings that may name one in a callable (`[$o, 'm']`,
// `'C::m'`). A call by a name held in a variable types as mixed, which a
// narrower return does not change.
func methodNames(src []byte) map[string]bool {
	out := map[string]bool{}
	f, err := parser.Parse(src, lexer.ModePhaenon)
	if err != nil {
		return out
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.MethodCall:
			if id, ok := n.Name.(*ast.Ident); ok {
				out[strings.ToLower(id.Name)] = true
			}
		case *ast.StaticCall:
			if id, ok := n.Name.(*ast.Ident); ok {
				out[strings.ToLower(id.Name)] = true
			}
		case *ast.StringLit:
			v := strings.ToLower(strings.Trim(n.Raw, `'"`))
			if i := strings.LastIndex(v, "::"); i >= 0 {
				v = v[i+2:]
			}
			out[v] = true
		}
		return true
	})
	return out
}

// analyze reads every watched file as it is, writing nothing.
func (r *arrayRun) analyze() (map[string]*arrayTyper, error) {
	typers, err := r.refresh()
	if err != nil {
		return nil, err
	}
	for path, a := range typers {
		a.run()
		// What it could type now waits for the next pass, if any.
		for _, k := range a.wrote {
			a.left[k] = "the rounds ended before its values were known"
		}
		r.left[path] = a.left
	}
	return typers, nil
}

// round types each watched file from what the check of the current
// sources records, checks the rewrite and keeps what adds no finding. It
// reports whether it proposed a rewrite, and whether a refused declaration
// asks for another round. With a probe, only the probe's declarations are
// typed, from the values of its last check.
func (r *arrayRun) round(probe *selfProbe) (map[string]*arrayTyper, bool, bool, error) {
	typers := map[string]*arrayTyper{}
	rewritten := map[string][]byte{}
	if probe == nil {
		var err error
		if typers, err = r.refresh(); err != nil {
			return nil, false, false, err
		}
	} else {
		for path, a := range probe.typers {
			if r.watch[path] {
				a.deny, a.left, a.edits, a.wrote = r.deny[path], map[string]string{}, nil, nil
				typers[path] = a
			}
		}
	}
	for path, a := range typers {
		src, n := a.run()
		if probe == nil {
			r.left[path] = a.left
		} else {
			if r.left[path] == nil {
				r.left[path] = map[string]string{}
			}
			for k, why := range a.left {
				if a.only[k] {
					r.left[path][k] = why
				}
			}
		}
		if n == 0 {
			continue
		}
		if _, err := parser.Parse(src, lexer.ModePhaenon); err != nil {
			r.rep.Left["the rewrite does not parse"] += n
			r.rep.Undone = append(r.rep.Undone, Note{Msg: fmt.Sprintf("%d array types undone: the rewrite does not parse (%v)", n, err)})
			r.rep.UndoneIn = append(r.rep.UndoneIn, path)
			delete(r.watch, path)
			continue
		}
		rewritten[path] = src
	}
	if len(rewritten) == 0 {
		return typers, false, false, nil
	}
	next := map[string][]byte{}
	for k, v := range r.srcs {
		next[k] = v
	}
	for k, v := range rewritten {
		next[k] = v
	}
	// What the rewrite may change: the rewritten files, and the files that
	// name a method whose return it typed.
	verify := map[string]bool{}
	var names []string
	for path := range rewritten {
		verify[path] = true
		for _, n := range typers[path].exported {
			names = append(names, strings.ToLower(n))
		}
	}
	for path := range r.mentioning(names) {
		verify[path] = true
	}
	// It records the watched files too: when the round keeps every
	// rewrite, this check is the next round's reading of them.
	after, afterTyped, err := r.checkSources(next, verify, nil)
	if err != nil {
		return nil, false, false, err
	}
	// A finding on the line of a declaration this round typed, or of one
	// of its values — a write, a return, an argument — is that
	// declaration's; one on a line that calls a method whose return this
	// round typed, in any file, is that return's. Blamed declarations are
	// left out of the next round, which keeps the others. A finding no
	// declaration of its own file takes gives a rewritten file its arrays
	// back; one in a file not rewritten, which only a return can reach,
	// blames this round's returns, or all it typed when it typed none.
	type blamedKey struct{ path, key string }
	own := map[string]map[int][]string{}
	for path, src := range rewritten {
		own[path] = typers[path].lines(src)
	}
	var exported []blamedKey
	exportedBy := map[string][]blamedKey{} // lower-cased method name → its returns
	for path := range rewritten {
		for k, name := range typers[path].exported {
			bk := blamedKey{path, k}
			exported = append(exported, bk)
			exportedBy[strings.ToLower(name)] = append(exportedBy[strings.ToLower(name)], bk)
		}
	}
	blamed := map[blamedKey]string{} // → the first finding it is blamed for
	undo := map[string]checker.Diagnostic{}
	afterByFile := byFile(after.diags)
	for path := range verify {
		found := newFindings(r.baseByFile[path], afterByFile[path])
		if len(found) == 0 {
			continue
		}
		lines := lineStarts(next[path])
		for _, d := range found {
			var ks []blamedKey
			for _, k := range own[path][d.Line] {
				ks = append(ks, blamedKey{path, k})
			}
			if len(ks) == 0 && d.Line >= 1 && d.Line <= len(lines) {
				end := len(next[path])
				if d.Line < len(lines) {
					end = lines[d.Line]
				}
				text := strings.ToLower(string(next[path][lines[d.Line-1]:end]))
				for name, bks := range exportedBy {
					if strings.Contains(text, name+"(") {
						ks = append(ks, bks...)
					}
				}
			}
			if len(ks) == 0 {
				if _, ok := rewritten[path]; ok {
					if _, seen := undo[path]; !seen {
						undo[path] = d
					}
					continue
				}
				if ks = exported; len(ks) == 0 {
					for p, a := range typers {
						if _, ok := rewritten[p]; ok {
							for _, k := range a.wrote {
								ks = append(ks, blamedKey{p, k})
							}
						}
					}
				}
			}
			for _, k := range ks {
				if _, ok := blamed[k]; !ok {
					rel := ""
					if k.path != path {
						rel = " of " + filepath.Base(path)
					}
					blamed[k] = fmt.Sprintf("the check: line %d%s: %s %s", d.Line, rel, d.Code, d.Message)
				}
			}
		}
	}
	retry := false
	kept := 0
	for path, src := range rewritten {
		a := typers[path]
		if probe != nil {
			delete(probe.typers, path) // read before this rewrite: only a refused file tries again
		}
		refused := false
		for _, k := range a.wrote {
			if why, ok := blamed[blamedKey{path, k}]; ok {
				if r.deny[path] == nil {
					r.deny[path] = map[string]string{}
				}
				r.deny[path][k] = why
				refused = true
			}
		}
		if refused {
			if probe != nil {
				probe.typers[path] = a
			}
			retry = true
			continue
		}
		if d, ok := undo[path]; ok {
			r.rep.Left["the check"] += len(a.wrote)
			r.rep.Undone = append(r.rep.Undone, Note{Msg: fmt.Sprintf("%d array types undone: line %d: %s %s", len(a.wrote), d.Line, d.Code, d.Message)})
			r.rep.UndoneIn = append(r.rep.UndoneIn, path)
			delete(r.watch, path) // its candidates stay as they are
			continue
		}
		r.srcs[path] = src
		if r.typedAs[path] == nil {
			r.typedAs[path] = map[string]string{}
		}
		for _, k := range a.wrote {
			if _, ok := r.typedAs[path][k]; ok {
				if r.narrows[path] == nil {
					r.narrows[path] = map[string]int{}
				}
				r.narrows[path][k]++
			} else {
				r.rep.Written++
			}
			r.typedAs[path][k] = a.spelled[k]
		}
		r.changed(path, a.exported)
		kept++
	}
	if kept == len(rewritten) {
		// The sources are the ones the check read: its reading of the files
		// it checked is current.
		for path := range verify {
			if !r.dirty[path] || !r.watch[path] {
				continue
			}
			if a := r.typer(after, afterTyped, path); a != nil {
				r.current[path] = a
				delete(r.dirty, path)
			}
		}
	}
	return typers, true, retry, nil
}

// selfProbe is a self round's assumption: per file, the declarations
// whose values depend on themselves and the reads of them — a property in
// its own writes, a recursive call, a parameter on entry — which the
// probing checks assume to hold what the last one found, nothing at first.
type selfProbe struct {
	keys    map[string]map[string]bool
	reads   map[string]map[ast.Span]string // file → read → declaration
	assumed map[string]map[string]types.Type
	typers  map[string]*arrayTyper // the last probing check's
}

// selfRound types the declarations whose values depend on themselves, as
// the least types those values settle on: checks that assume their own
// reads empty, then what the last check found, until the values settle,
// and the usual check of the rewrite. It reports whether it kept any.
func (r *arrayRun) selfRound(typers map[string]*arrayTyper) (bool, error) {
	probe := &selfProbe{keys: map[string]map[string]bool{}, reads: map[string]map[ast.Span]string{}, assumed: map[string]map[string]types.Type{}}
	for path, a := range typers {
		keys, reads := a.selfDependent()
		if len(keys) == 0 {
			continue
		}
		probe.keys[path], probe.reads[path], probe.assumed[path] = keys, reads, map[string]types.Type{}
		for k := range keys {
			probe.assumed[path][k] = types.Shape{List: true}
		}
	}
	if len(probe.keys) == 0 {
		return false, nil
	}
	assume := func(f *program.File, n ast.Node) types.Type {
		if f == nil {
			return nil
		}
		if k, ok := probe.reads[f.Path][n.Span()]; ok {
			return probe.assumed[f.Path][k]
		}
		return nil
	}
	for i := 0; i < selfProbes; i++ {
		files := map[string]bool{}
		for path := range probe.keys {
			files[path] = true
		}
		cur, t, err := r.check(files, assume)
		if err != nil {
			return false, err
		}
		probe.typers = map[string]*arrayTyper{}
		settled := true
		for path, keys := range probe.keys {
			a := r.typer(cur, t, path)
			if a == nil {
				continue
			}
			a.only = keys
			a.run()
			probe.typers[path] = a
			for k := range keys {
				v, ok := a.values[k]
				if !ok {
					continue // nothing to assume: its own reads keep the last
				}
				u := assumedUnion(v)
				if was := probe.assumed[path][k]; was == nil || types.Print(was) != types.Print(u) {
					settled = false
				}
				probe.assumed[path][k] = u
			}
		}
		if settled {
			break
		}
	}
	kept := false
	for try := 0; try < selfProbes; try++ {
		before := r.rep.Written
		_, proposed, retry, err := r.round(probe)
		if err != nil {
			return false, err
		}
		if r.rep.Written > before {
			kept = true
		}
		if !proposed || !retry {
			break
		}
	}
	return kept, nil
}

// mentionsMixed reports a spelled type that says mixed somewhere, array<…>
// of keys or elements not known among them.
func mentionsMixed(spelled string) bool {
	return strings.Contains(spelled, "mixed")
}

// mixedDepth is how deep in a spelled type its deepest mixed sits, in
// the type arguments and tuples around it; -1 when it says none.
func mixedDepth(spelled string) int {
	deepest, depth := -1, 0
	for i := 0; i < len(spelled); i++ {
		switch spelled[i] {
		case '<', '(':
			depth++
		case '>', ')':
			depth--
		case 'm':
			if strings.HasPrefix(spelled[i:], "mixed") && (i == 0 || !isIdentByte(spelled[i-1])) && (i+5 == len(spelled) || !isIdentByte(spelled[i+5])) {
				deepest = max(deepest, depth)
			}
		}
	}
	return deepest
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '\\' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// emptyArray reports an array type that holds nothing: [], or one whose
// key or value type is never.
func emptyArray(t types.Type) bool {
	switch t := t.(type) {
	case types.Shape:
		return len(t.Entries) == 0
	case types.Array:
		_, k := t.Key.(types.Never)
		_, v := t.Value.(types.Never)
		return k || v
	}
	return false
}

// assumedUnion is what a self round assumes a declaration holds: its
// values but the empty arrays, which add nothing an array reads.
func assumedUnion(values []types.Type) types.Type {
	var kept []types.Type
	for _, v := range values {
		for _, m := range types.Members(v) {
			if !emptyArray(m) {
				kept = append(kept, m)
			}
		}
	}
	if len(kept) == 0 {
		return types.Shape{List: true}
	}
	return types.MakeUnion(kept...)
}

// notes reports each candidate left, at its line.
func (r *arrayRun) notes() {
	for path, reasons := range r.left {
		if !r.watch[path] {
			continue
		}
		pf, err := parser.Parse(r.srcs[path], lexer.ModePhaenon)
		if err != nil {
			continue
		}
		lines := lineStarts(r.srcs[path])
		for _, c := range bareArrayDecls(pf) {
			why, ok := reasons[c.key]
			if refused, denied := r.deny[path][c.key]; denied {
				why, ok = refused, true // a last round's refusal, which no round read since
			}
			if !ok {
				continue
			}
			r.rep.Left[why]++
			r.rep.Notes[path] = append(r.rep.Notes[path], Note{Line: lineAt(lines, c.at), Msg: fmt.Sprintf("left %s as array: %s", c.what, why)})
		}
	}
	for path := range r.rep.Notes {
		sort.Slice(r.rep.Notes[path], func(i, j int) bool { return r.rep.Notes[path][i].Line < r.rep.Notes[path][j].Line })
	}
}

// newFindings lists the findings a rewrite added to a file, given the
// file's findings before and after: findings are compared by code and
// line, since a message may name the narrower type.
func newFindings(before, after []checker.Diagnostic) []checker.Diagnostic {
	type key struct {
		code string
		line int
	}
	was := map[key]int{}
	for _, d := range before {
		was[key{d.Code, d.Line}]++
	}
	var out []checker.Diagnostic
	for _, d := range after {
		k := key{d.Code, d.Line}
		if was[k] == 0 {
			out = append(out, d)
			continue
		}
		was[k]--
	}
	return out
}

// candidate is one bare `array` declaration: key names it across rounds,
// at is where it is written, what describes it for a note.
type candidate struct {
	key, what string
	at        int
	span      ast.Span // the type to replace: `array`, or what a round wrote; the `?` of a nullable one stays
	nullable  bool
	method    string // a return's or a parameter's method
	// typ is the type a round wrote, exported marks a return other files
	// may read: typedDecls's.
	typ      ast.TypeExpr
	exported bool
}

// bareArrayDecls lists a file's declarations written as a bare array or
// ?array, the kinds the codemod considers.
func bareArrayDecls(f *ast.File) []candidate {
	var out []candidate
	bare := func(t ast.TypeExpr) (*ast.NamedType, bool, bool) {
		nullable := false
		if n, ok := t.(*ast.NullableType); ok {
			t, nullable = n.Type, true
		}
		nt, ok := t.(*ast.NamedType)
		if !ok || len(nt.TypeArgs) > 0 || len(nt.Name.Parts) != 1 || !strings.EqualFold(nt.Name.Parts[0], "array") {
			return nil, false, false
		}
		return nt, nullable, true
	}
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.ClassDecl)
		if !ok || cl.Name == nil {
			return true
		}
		for _, m := range cl.Members {
			switch m := m.(type) {
			case *ast.PropertyDecl:
				if nt, nullable, ok := bare(m.Type); ok && len(m.Props) == 1 {
					out = append(out, candidate{key: "prop " + cl.Name.Name + "::" + m.Props[0].Var.Name, what: "property $" + m.Props[0].Var.Name, at: nt.S.Start, span: nt.S, nullable: nullable})
				}
			case *ast.MethodDecl:
				if nt, nullable, ok := bare(m.ReturnType); ok {
					out = append(out, candidate{key: "return " + cl.Name.Name + "::" + m.Name.Name, what: m.Name.Name + "()'s return", at: nt.S.Start, span: nt.S, nullable: nullable, method: m.Name.Name})
				}
				for _, p := range m.Params {
					if p.Modifiers != 0 {
						continue // promoted: the constructor's callers write it
					}
					if nt, nullable, ok := bare(p.Type); ok {
						out = append(out, candidate{key: "param " + cl.Name.Name + "::" + m.Name.Name + "$" + p.Var.Name, what: "parameter $" + p.Var.Name + " of " + m.Name.Name + "()", at: nt.S.Start, span: nt.S, nullable: nullable, method: m.Name.Name})
					}
				}
			}
		}
		return true
	})
	return out
}

// bareArrayDeclsAll is bareArrayDecls for a rewritten file, where the
// candidates may already carry the type a round wrote: every property,
// return and parameter, keyed as there.
func bareArrayDeclsAll(f *ast.File) []candidate {
	var out []candidate
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.ClassDecl)
		if !ok || cl.Name == nil {
			return true
		}
		for _, m := range cl.Members {
			switch m := m.(type) {
			case *ast.PropertyDecl:
				if m.Type != nil && len(m.Props) == 1 {
					out = append(out, candidate{key: "prop " + cl.Name.Name + "::" + m.Props[0].Var.Name, at: m.Type.Span().Start})
				}
			case *ast.MethodDecl:
				if m.ReturnType != nil {
					out = append(out, candidate{key: "return " + cl.Name.Name + "::" + m.Name.Name, at: m.ReturnType.Span().Start})
				}
				for _, p := range m.Params {
					if p.Type != nil {
						out = append(out, candidate{key: "param " + cl.Name.Name + "::" + m.Name.Name + "$" + p.Var.Name, at: p.Type.Span().Start})
					}
				}
			}
		}
		return true
	})
	return out
}

// typedDecls lists the declarations of a file that earlier rounds typed,
// keys among those of typedAs, as candidates whose type a round may
// narrow.
func typedDecls(f *ast.File, typedAs map[string]string) []candidate {
	if len(typedAs) == 0 {
		return nil
	}
	var out []candidate
	add := func(key, what, method string, t ast.TypeExpr, exported bool) {
		if _, ok := typedAs[key]; !ok || t == nil {
			return
		}
		nullable := false
		if n, ok := t.(*ast.NullableType); ok {
			t, nullable = n.Type, true
		}
		out = append(out, candidate{key: key, what: what, at: t.Span().Start, span: t.Span(), nullable: nullable, method: method, typ: t, exported: exported})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.ClassDecl)
		if !ok || cl.Name == nil {
			return true
		}
		for _, m := range cl.Members {
			switch m := m.(type) {
			case *ast.PropertyDecl:
				if len(m.Props) == 1 {
					add("prop "+cl.Name.Name+"::"+m.Props[0].Var.Name, "property $"+m.Props[0].Var.Name, "", m.Type, false)
				}
			case *ast.MethodDecl:
				add("return "+cl.Name.Name+"::"+m.Name.Name, m.Name.Name+"()'s return", m.Name.Name, m.ReturnType, m.Modifiers&ast.ModPrivate == 0)
				for _, p := range m.Params {
					if p.Modifiers == 0 {
						add("param "+cl.Name.Name+"::"+m.Name.Name+"$"+p.Var.Name, "parameter $"+p.Var.Name+" of "+m.Name.Name+"()", m.Name.Name, p.Type, false)
					}
				}
			}
		}
		return true
	})
	return out
}

// byRefArg is an argument passed by reference and what the callee writes
// back to it, nil when it keeps the argument's type.
type byRefArg struct {
	e   ast.Expr
	out types.Type
}

// docReturns, docParams and docVars report whether a docblock gives the
// slot a type: its @return, the @param of the named parameter, a @var.
func docReturns(doc string) bool { return doc != "" && docblock.Parse(doc).Return != nil }

func docParams(doc, name string) bool {
	if doc == "" {
		return false
	}
	for _, t := range docblock.Parse(doc).Params {
		if t.Name == name && t.Type != nil {
			return true
		}
	}
	return false
}

func docVars(doc string) bool {
	if doc == "" {
		return false
	}
	for _, v := range docblock.Parse(doc).Vars {
		if v.Type != nil {
			return true
		}
	}
	return false
}

// arrayTyper is one round over one file.
type arrayTyper struct {
	prog  *program.Program
	file  *program.File
	types map[ast.Span]types.Type
	byRef []byRefArg
	// callables holds, for an argument that may be a callable (`[$this,
	// 'm']`, `$this->m(...)`), the type of the parameter it is passed to.
	callables map[ast.Expr]types.Type
	// returned holds the type each return gives its function.
	returned map[*ast.Return]types.Type
	// stored holds, for a callable reference assigned to a typed property
	// (`$this->post = $post ?? self::send(...)`), the property's type.
	stored map[ast.Expr]types.Type
	pcl    *program.Class // the class being read
	// recursive holds, lower-cased, the methods of the class being read
	// that call themselves through others: what narrowing one gives may
	// be the other's type nested once more. Nil until a narrowing asks.
	recursive map[string]bool
	ns        string          // the file's namespace
	only      map[string]bool // a self round's: the candidates it may type
	// lax lets the run write a type that says mixed somewhere, which waits
	// for the rounds that write precise ones: its values may come from a
	// declaration a later round types.
	lax  bool
	deny map[string]string // candidates a check refused, with the finding
	// typedAs holds the declarations earlier rounds typed, with the type
	// written: candidates again, given a narrower type when their values
	// say less than it.
	typedAs map[string]string
	narrows map[string]int // how often a round narrowed each

	// What a run finds, per candidate key.
	left   map[string]string       // why it was left
	values map[string][]types.Type // the values it holds, as far as they are known
	// unspelled marks a candidate whose values are all known, left because
	// no type of them can be written: one a self round may yet type.
	unspelled map[string]bool
	sites     map[string][]int // where its values come from: writes, returns, arguments
	// uses holds where a candidate is read — a property's fetches, a
	// method's calls — and spans a parameter's method: what its type
	// changes, beside its values.
	uses  map[string][]int
	spans map[string]ast.Span
	// selfReads holds the reads of a candidate in its own values (a
	// property in its writes, a recursive call), params a parameter's
	// declaration, and passes the parameters whose values reach it (a
	// call inside a method passing that method's parameters on).
	selfReads map[string][]ast.Span
	// ownWrites marks a property a by-reference call writes back to with
	// a type made of its own: only a self round can read its values.
	ownWrites map[string]bool
	targets   map[string][]ast.Span // the property its writes write: assumed with its reads
	params    map[string]ast.Span
	passes    map[string][]string
	edits     []edit
	wrote     []string          // the candidates this run typed
	spelled   map[string]string // the type it wrote for each
	// exported holds, of them, the returns of methods other files may
	// call, with the method's name.
	exported map[string]string
}

func (a *arrayTyper) site(key string, off int) { a.sites[key] = append(a.sites[key], off) }

func (a *arrayTyper) use(key string, off int) { a.uses[key] = append(a.uses[key], off) }

func (a *arrayTyper) run() ([]byte, int) {
	a.left, a.values, a.unspelled, a.sites = map[string]string{}, map[string][]types.Type{}, map[string]bool{}, map[string][]int{}
	a.selfReads, a.targets, a.params, a.passes, a.edits, a.wrote = map[string][]ast.Span{}, map[string][]ast.Span{}, map[string]ast.Span{}, map[string][]string{}, nil, nil
	a.spelled = map[string]string{}
	a.exported, a.uses, a.spans, a.ownWrites = map[string]string{}, map[string][]int{}, map[string]ast.Span{}, map[string]bool{}
	ns, imports := fileNames(a.file.AST)
	a.ns = ns
	sp := &speller{ns: ns, imports: imports, prog: a.prog}
	cands := append(bareArrayDecls(a.file.AST), typedDecls(a.file.AST, a.typedAs)...)
	byKey := map[string]candidate{}
	for _, c := range cands {
		byKey[c.key] = c
	}
	ast.Inspect(a.file.AST, func(n ast.Node) bool {
		cl, ok := n.(*ast.ClassDecl)
		if !ok || cl.Name == nil {
			return true
		}
		pcl := a.programClass(cl)
		a.pcl = pcl
		a.stored = storedCallables(cl, pcl)
		a.recursive = nil // found when a narrowing asks
		// write types a candidate from its values; inherited, when not
		// nil, is the type an override takes from the method it overrides,
		// which it keeps unless its values are of another type.
		write := func(key string, values []types.Type, shape arrayShape, inherited types.Type, exported string) {
			c, ok := byKey[key]
			if !ok {
				return
			}
			known := append([]types.Type{}, values...)
			if len(shape.elems) > 0 {
				known = append(known, types.Array{Key: types.MakeUnion(shape.keys...), Value: types.MakeUnion(shape.elems...), List: !shape.keyed})
			}
			a.values[key] = known
			if a.only != nil && !a.only[key] {
				return
			}
			if was, ok := a.typedAs[key]; ok {
				a.narrow(cl, c, key, was, values, shape, inherited, exported, sp)
				return
			}
			if why, ok := a.deny[key]; ok {
				a.left[key] = why
				return
			}
			if a.ownWrites[key] {
				a.left[key] = "a by-reference call writes back to it a type made of its own"
				a.unspelled[key] = true
				return
			}
			sp.uses = a.tupleUses(cl, c)
			spelled, why := spellArray(values, shape, c.nullable, sp)
			if inherited != nil {
				// Written only when it says more than the type it takes:
				// another type, and one of it.
				own, _ := spellArray([]types.Type{inherited}, arrayShape{}, c.nullable, sp)
				st := a.spelledType(spelled)
				if st != nil && c.nullable {
					st = types.MakeUnion(st, types.TNull)
				}
				if why != "" || spelled == own || st == nil || !types.IsSubtype(st, inherited, a.prog) {
					a.left[key] = "it takes the type of the method it overrides"
					return
				}
			}
			if why != "" {
				a.left[key] = why
				a.unspelled[key] = true
				return
			}
			if a.only == nil && mentionsMixed(spelled) && a.readsItself(key) {
				// The mixed may be its own bare array read back, as a
				// recursion spread into array_push() gives it: the self
				// round reads its values with that read assumed.
				a.left[key] = "its values read it, and say mixed"
				a.unspelled[key] = true
				return
			}
			if !a.lax && a.only == nil && mentionsMixed(spelled) {
				a.left[key] = "it says mixed, and waits for the declarations its values come from"
				return
			}
			a.edits = append(a.edits, edit{c.span, spelled})
			a.wrote = append(a.wrote, key)
			a.spelled[key] = spelled
			if exported != "" {
				a.exported[key] = exported
			}
		}
		for _, m := range cl.Members {
			switch m := m.(type) {
			case *ast.PropertyDecl:
				if len(m.Props) != 1 {
					continue
				}
				key := "prop " + cl.Name.Name + "::" + m.Props[0].Var.Name
				if _, ok := byKey[key]; !ok {
					continue
				}
				if why := a.propertyLeft(cl, m); why != "" {
					a.left[key] = why
					continue
				}
				values, shape, why := a.propertyValues(key, cl, m)
				if why != "" {
					a.left[key] = why
					continue
				}
				write(key, values, shape, nil, "")
			case *ast.MethodDecl:
				priv := m.Modifiers&ast.ModPrivate != 0
				final := priv || m.Modifiers&ast.ModFinal != 0 || cl.Modifiers&ast.ModFinal != 0
				rkey := "return " + cl.Name.Name + "::" + m.Name.Name
				if _, ok := byKey[rkey]; ok {
					a.callsOf(rkey, cl, m.Name.Name)
					switch {
					case !final:
						a.left[rkey] = "the method may be overridden"
					case docReturns(m.Doc):
						a.left[rkey] = "a docblock types it"
					default:
						// An override takes its parent's type (collections.md §2):
						// it is written only when it returns one of another, which
						// the check holds to the parent's.
						inherited := a.inheritedReturn(pcl, m.Name.Name)
						if values, why := a.returnValues(rkey, m); why != "" {
							if inherited != nil {
								why = "it takes the type of the method it overrides"
							}
							a.left[rkey] = why
						} else {
							exported := ""
							if !priv {
								exported = m.Name.Name
							}
							write(rkey, values, arrayShape{}, inherited, exported)
						}
					}
				}
				for i, p := range m.Params {
					pkey := "param " + cl.Name.Name + "::" + m.Name.Name + "$" + p.Var.Name
					if _, ok := byKey[pkey]; !ok {
						continue
					}
					a.params[pkey] = p.S
					a.spans[pkey] = m.S
					switch {
					case !priv:
						a.left[pkey] = "the method is not private"
					case usesTrait(cl):
						a.left[pkey] = "a trait may call it"
					case docParams(m.Doc, p.Var.Name):
						a.left[pkey] = "a docblock types it"
					default:
						if values, why := a.argValues(cl, m, i, p); why != "" {
							a.left[pkey] = why
						} else {
							write(pkey, values, arrayShape{}, nil, "")
						}
					}
				}
			}
		}
		return true
	})
	if len(a.wrote) == 0 {
		return a.file.Src, 0
	}
	l := &lifter{src: a.file.Src, edits: a.edits}
	return l.apply(), len(a.wrote)
}

// narrow gives a declaration an earlier round typed as was the type its
// values spell now, when that one is narrower: a value read from a
// declaration not typed then, as array_keys() of a bare array is a
// list<int|string>, is known since. It writes no type that says more
// than was, or none at all: was stays, and no note is due.
func (a *arrayTyper) narrow(cl *ast.ClassDecl, c candidate, key, was string, values []types.Type, shape arrayShape, inherited types.Type, exported string, sp *speller) {
	if _, refused := a.deny[key]; refused || a.ownWrites[key] || a.narrows[key] >= narrowsAtMost {
		return
	}
	// Values read from the declaration itself hold the type written: each
	// narrowing would nest it once more, as [$this->p] does.
	if a.readsItself(key) {
		return
	}
	sp.uses = a.tupleUses(cl, c)
	spelled, why := spellArray(values, shape, c.nullable, sp)
	if why != "" || spelled == was {
		return
	}
	now, before := a.spelledType(spelled), a.spelledType(was)
	if now == nil || before == nil || !types.IsSubtype(now, before, a.prog) || types.IsSubtype(before, now, a.prog) {
		return
	}
	// In a recursion through other methods, an unknown found one level
	// deeper is the recursion unrolled, not news from a value typed since.
	if c.method != "" && mixedDepth(spelled) > mixedDepth(was) {
		if a.recursive == nil {
			a.recursive = a.recursiveMethods(cl)
		}
		if a.recursive[strings.ToLower(c.method)] {
			return
		}
	}
	if inherited != nil {
		if c.nullable {
			now = types.MakeUnion(now, types.TNull)
		}
		if !types.IsSubtype(now, inherited, a.prog) {
			return
		}
	}
	a.edits = append(a.edits, edit{c.span, spelled})
	a.wrote = append(a.wrote, key)
	a.spelled[key] = spelled
	if exported != "" {
		a.exported[key] = exported
	}
}

// tupleUses says how a candidate's reads in its class use it and its
// elements: taken apart or read at a literal index, which a pair is;
// iterated or spread, which a list is. Iteration wins.
func (a *arrayTyper) tupleUses(cl *ast.ClassDecl, c candidate) [2]tupleUse {
	var out [2]tupleUse
	kind, rest, _ := strings.Cut(c.key, " ")
	_, member, _ := strings.Cut(rest, "::")
	region := []ast.Node{cl}
	var read func(e ast.Expr) bool
	switch kind {
	case "prop":
		read = func(e ast.Expr) bool {
			switch e := e.(type) {
			case *ast.PropertyFetch:
				v, ok := e.Object.(*ast.Variable)
				id, named := e.Name.(*ast.Ident)
				return ok && v.Name == "this" && named && id.Name == member
			case *ast.StaticPropertyFetch:
				v, ok := e.Name.(*ast.Variable)
				return ok && v.Name == member && a.ownClass(e.Class)
			}
			return false
		}
	case "return":
		read = func(e ast.Expr) bool {
			switch e := e.(type) {
			case *ast.MethodCall:
				v, ok := e.Object.(*ast.Variable)
				return ok && v.Name == "this" && a.names(e.Name, member)
			case *ast.StaticCall:
				return a.ownClass(e.Class) && a.names(e.Name, member)
			}
			return false
		}
	case "param":
		method, param, _ := strings.Cut(member, "$")
		region = nil
		for _, m := range cl.Members {
			if md, ok := m.(*ast.MethodDecl); ok && strings.EqualFold(md.Name.Name, method) {
				for _, st := range md.Body {
					region = append(region, st)
				}
			}
		}
		read = func(e ast.Expr) bool {
			v, ok := e.(*ast.Variable)
			return ok && v.Name == param
		}
	default:
		return out
	}
	// level is 0 for a read of the declaration, 1 for one of its element.
	level := func(e ast.Expr) int {
		if read(e) {
			return 0
		}
		if ix, ok := e.(*ast.Index); ok && ix.Index != nil && read(ix.Array) {
			return 1
		}
		return -1
	}
	saw := func(l int, u tupleUse) {
		if l >= 0 && l < len(out) && out[l] != usedAsList {
			out[l] = u
		}
	}
	for _, r := range region {
		ast.Inspect(r, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Assign:
				if lit, ok := n.Target.(*ast.ArrayLit); ok && n.Op == token.Kind('=') && destructures(lit) {
					saw(level(n.Value), usedAsTuple)
				}
			case *ast.Foreach:
				l := level(n.Expr)
				if lit, ok := n.Value.(*ast.ArrayLit); ok && destructures(lit) && l >= 0 {
					saw(l+1, usedAsTuple)
				}
				saw(l, usedAsList)
			case *ast.Index:
				if _, ok := n.Index.(*ast.IntLit); ok {
					saw(level(n.Array), usedAsTuple)
				}
			case *ast.ArrayItem:
				if n.Unpack {
					saw(level(n.Value), usedAsList)
				}
			case *ast.Arg:
				if n.Unpack {
					saw(level(n.Value), usedAsList)
				}
			}
			return true
		})
	}
	return out
}

// destructures reports an array literal that takes positions apart:
// `[$a, $b]`, list($a, , $c), no key.
func destructures(lit *ast.ArrayLit) bool {
	if len(lit.Items) == 0 {
		return false
	}
	for _, it := range lit.Items {
		if it != nil && it.Key != nil {
			return false
		}
	}
	return true
}

// readsItself reports a candidate whose values read it: a property in its
// writes, a recursive call among a method's returns, a parameter its
// method passes on to itself.
func (a *arrayTyper) readsItself(key string) bool {
	if len(a.selfReads[key]) > 0 {
		return true
	}
	for _, from := range a.passes[key] {
		if from == key {
			return true
		}
	}
	return false
}

// recursiveMethods lists, lower-cased, the methods of a class that reach
// themselves through calls of its other methods, on $this or the class.
func (a *arrayTyper) recursiveMethods(cl *ast.ClassDecl) map[string]bool {
	calls := map[string]map[string]bool{}
	for _, m := range cl.Members {
		md, ok := m.(*ast.MethodDecl)
		if !ok {
			continue
		}
		from := strings.ToLower(md.Name.Name)
		calls[from] = map[string]bool{}
		for _, s := range md.Body {
			ast.Inspect(s, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.MethodCall:
					if id, ok := n.Name.(*ast.Ident); ok {
						if call, _ := a.reaches(n.Object); call {
							calls[from][strings.ToLower(id.Name)] = true
						}
					}
				case *ast.StaticCall:
					if id, ok := n.Name.(*ast.Ident); ok && a.ownClass(n.Class) {
						calls[from][strings.ToLower(id.Name)] = true
					}
				}
				return true
			})
		}
	}
	out := map[string]bool{}
	for start := range calls {
		// start is recursive when a method it calls, other than itself,
		// leads back to it.
		seen := map[string]bool{}
		var walk func(m string) bool
		walk = func(m string) bool {
			for next := range calls[m] {
				if next == start {
					return true
				}
				if !seen[next] {
					seen[next] = true
					if walk(next) {
						return true
					}
				}
			}
			return false
		}
		for next := range calls[start] {
			if next != start && !seen[next] {
				seen[next] = true
				if walk(next) {
					out[start] = true
					break
				}
			}
		}
	}
	return out
}

// inheritedReturn is the return type a method takes from the one it
// overrides, nil when it overrides none.
func (a *arrayTyper) inheritedReturn(cl *program.Class, name string) types.Type {
	if cl == nil {
		return nil
	}
	for _, anc := range cl.Ancestors() {
		if m := anc.FindMethod(name); m != nil && m.Vis != program.Private {
			if m.Return != nil {
				return m.Return
			}
			return types.TMixed
		}
	}
	return nil
}

// selfDependent lists the candidates a self round may type — left because
// no type of their values can be written, and reading themselves — and
// the reads to assume, each with the candidate it reads.
func (a *arrayTyper) selfDependent() (map[string]bool, map[ast.Span]string) {
	keys := map[string]bool{}
	reads := map[ast.Span]string{}
	for k := range a.unspelled {
		if len(a.selfReads[k]) > 0 {
			keys[k] = true
			// Its writes' targets too: what a value written is inferred
			// from is the type the property is assumed to hold.
			for _, s := range append(a.selfReads[k], a.targets[k]...) {
				reads[s] = k
			}
		}
	}
	// A parameter reads itself when what a method passes it comes back to
	// it through the parameters it is passed on to: a recursion, direct or
	// through another method.
	var reaches func(from, to string, seen map[string]bool) bool
	reaches = func(from, to string, seen map[string]bool) bool {
		for _, next := range a.passes[from] {
			if next == to {
				return true
			}
			if !seen[next] {
				seen[next] = true
				if reaches(next, to, seen) {
					return true
				}
			}
		}
		return false
	}
	for k := range a.unspelled {
		if span, ok := a.params[k]; ok && reaches(k, k, map[string]bool{}) {
			keys[k] = true
			reads[span] = k
		}
	}
	return keys, reads
}

// lines maps the lines of the candidates this run typed to them: the
// declaration's line in the rewrite, the lines its values come from and
// the lines that read it, a parameter's whole method (the rewrite changes
// no line).
func (a *arrayTyper) lines(rewritten []byte) map[int][]string {
	out := map[int][]string{}
	tree, err := parser.Parse(rewritten, lexer.ModePhaenon)
	if err != nil {
		return out
	}
	at := map[string]int{}
	for _, c := range bareArrayDeclsAll(tree) {
		at[c.key] = c.at
	}
	newLines, oldLines := lineStarts(rewritten), lineStarts(a.file.Src)
	for _, k := range a.wrote {
		lines := map[int]bool{}
		if off, ok := at[k]; ok {
			lines[lineAt(newLines, off)] = true
		}
		for _, off := range append(append([]int{}, a.sites[k]...), a.uses[k]...) {
			lines[lineAt(oldLines, off)] = true
		}
		if sp, ok := a.spans[k]; ok {
			for l := lineAt(oldLines, sp.Start); l <= lineAt(oldLines, sp.End); l++ {
				lines[l] = true
			}
		}
		for l := range lines {
			out[l] = append(out[l], k)
		}
	}
	return out
}

// spelledType is the type a spelling the codemod writes at a class stands
// for, read as the checker reads the declaration; nil when it cannot tell.
func (a *arrayTyper) spelledType(spelled string) types.Type {
	if a.pcl == nil {
		return nil
	}
	tree, err := parser.Parse([]byte("fun f(): "+spelled+" {}"), lexer.ModePhaenon)
	if err != nil {
		return nil
	}
	var rt ast.TypeExpr
	ast.Inspect(tree, func(n ast.Node) bool {
		if fd, ok := n.(*ast.FunctionDecl); ok {
			rt = fd.ReturnType
			return false
		}
		return true
	})
	if rt == nil {
		return nil
	}
	return a.prog.NewResolver(a.file, a.pcl.Scope, a.pcl, ast.Span{}).Native(rt)
}

// programClass is the program's class of a declaration of the file, found
// by its name.
func (a *arrayTyper) programClass(cl *ast.ClassDecl) *program.Class {
	fqn := cl.Name.Name
	if a.ns != "" {
		fqn = a.ns + "\\" + fqn
	}
	if c := a.prog.Class(fqn); c != nil && c.Decl == cl {
		return c
	}
	return nil
}

// propertyLeft says why a property is not considered at all.
func (a *arrayTyper) propertyLeft(cl *ast.ClassDecl, p *ast.PropertyDecl) string {
	switch {
	case p.Modifiers&ast.ModPrivate == 0:
		return "the property is not private"
	case usesTrait(cl):
		return "a trait may write it"
	case docVars(p.Doc):
		return "a docblock types it"
	case len(p.Attrs) > 0:
		return "an attribute may have a framework write it"
	}
	return ""
}

// usesTrait reports a class that uses a trait, whose code may reach the
// class's private members from another file.
func usesTrait(cl *ast.ClassDecl) bool {
	for _, m := range cl.Members {
		if _, ok := m.(*ast.TraitUse); ok {
			return true
		}
	}
	return false
}

// arrayShape is what element writes say of a property: whether a key was
// written (or one unset), and the keys and values written; an append
// writes an int key.
type arrayShape struct {
	keyed       bool
	keys, elems []types.Type
}

// propertyValues collects the values a private property can hold: its
// default, what is assigned to it and to its elements (an element of an
// element too), and what a callee passed it by reference writes back.
func (a *arrayTyper) propertyValues(key string, cl *ast.ClassDecl, p *ast.PropertyDecl) ([]types.Type, arrayShape, string) {
	name := p.Props[0].Var.Name
	static := p.Modifiers&ast.ModStatic != 0
	// isSelf reports the property itself: $this->p, or self::$p,
	// static::$p and the class's own name for a static one.
	isSelf := func(e ast.Expr) bool {
		if static {
			sp, ok := e.(*ast.StaticPropertyFetch)
			if !ok {
				return false
			}
			v, ok := sp.Name.(*ast.Variable)
			if !ok || v.Name != name {
				return false
			}
			cn, ok := sp.Class.(*ast.Name)
			if !ok || len(cn.Parts) != 1 {
				return false
			}
			c := strings.ToLower(cn.Parts[0])
			return c == "self" || c == "static" || strings.EqualFold(cn.Parts[0], cl.Name.Name)
		}
		pf, ok := e.(*ast.PropertyFetch)
		if !ok {
			return false
		}
		v, ok := pf.Object.(*ast.Variable)
		id, ok2 := pf.Name.(*ast.Ident)
		return ok && ok2 && v.Name == "this" && id.Name == name
	}
	var values []types.Type
	var shape arrayShape
	if d := p.Props[0].Default; d != nil {
		t, ok := a.types[d.Span()]
		if !ok {
			return nil, shape, "its default is not typed"
		}
		values = append(values, t)
	}
	// reads records the property's reads in a value written to it, which a
	// self round assumes.
	reads := func(e ast.Expr) {
		ast.Inspect(e, func(n ast.Node) bool {
			if x, ok := n.(ast.Expr); ok && isSelf(x) {
				a.selfReads[key] = append(a.selfReads[key], x.Span())
			}
			return true
		})
	}
	for _, r := range a.byRef {
		if isSelf(r.e) {
			a.site(key, r.e.Span().Start)
			// What the callee writes back, sort()'s list<V>: a value of
			// its own, unless its elements are unknown, which happens when
			// it is made of this property's type while the property is
			// untyped — array_shift()'s `($array is list ? list<V> :
			// array<V>)`. A self round reads it with the type assumed.
			if arr, ok := r.out.(types.Array); ok && types.IsMixed(arr.Value) {
				a.selfReads[key] = append(a.selfReads[key], r.e.Span())
				a.ownWrites[key] = true
			} else if r.out != nil {
				values = append(values, r.out)
			}
			continue
		}
		for e := r.e; ; {
			ix, ok := e.(*ast.Index)
			if !ok {
				break
			}
			if isSelf(ix.Array) {
				return nil, shape, "an element is passed by reference"
			}
			e = ix.Array
		}
	}
	// element records an element written: its key (nil: appended) and value.
	element := func(key ast.Expr, value types.Type) string {
		shape.elems = append(shape.elems, value)
		if key == nil {
			shape.keys = append(shape.keys, types.TInt)
			return ""
		}
		k, ok := a.types[key.Span()]
		if !ok {
			return "a key written to it is not typed"
		}
		shape.keyed = true
		shape.keys = append(shape.keys, k)
		return ""
	}
	why := ""
	for _, m := range cl.Members {
		md, ok := m.(*ast.MethodDecl)
		if !ok {
			continue
		}
		var visit func(n ast.Node) bool
		visit = func(n ast.Node) bool {
			if why != "" {
				return false
			}
			if x, ok := n.(ast.Expr); ok && isSelf(x) {
				a.use(key, x.Span().Start)
			}
			switch n := n.(type) {
			case *ast.ClassDecl:
				// Another class's $this, but for an anonymous class's
				// arguments, which this one's code passes.
				if n.Args != nil {
					ast.Inspect(n.Args, visit)
				}
				return false
			case *ast.PropertyFetch:
				// Another object's property of the name is this one only
				// on an object of this class: one of another class, by its
				// type, has its own.
				if id, ok := n.Name.(*ast.Ident); ok && id.Name == name && !static {
					if v, ok := n.Object.(*ast.Variable); !ok || v.Name != "this" {
						if reaches, maybe := a.reaches(n.Object); reaches || maybe {
							why = "another object's property of that name is used"
						}
					}
				}
			case *ast.Foreach:
				if n.ByRef && isSelf(n.Expr) {
					why = "a foreach takes its elements by reference"
				}
			case *ast.Unset:
				for _, v := range n.Vars {
					if ix, ok := v.(*ast.Index); ok && isSelf(ix.Array) {
						shape.keyed = true
					} else if isSelf(v) {
						why = "it is unset"
					}
				}
			case *ast.Assign:
				if n.ByRef && (isSelf(n.Value) || isSelf(n.Target)) {
					why = "a reference is taken"
					return false
				}
				// A compound assignment (`.=`, `+=`, `??=`) holds what the
				// whole expression gives, as the checker typed it.
				valueOf := func() (types.Type, bool) {
					// `??=` writes its right side, if it writes at all.
					if n.Op == token.Kind('=') || n.Op == token.T_COALESCE_EQUAL {
						t, ok := a.types[n.Value.Span()]
						return t, ok
					}
					t, ok := a.types[n.S]
					return t, ok
				}
				if isSelf(n.Target) {
					t, ok := valueOf()
					if !ok {
						why = "a value written to it is not typed"
						return false
					}
					a.site(key, n.S.Start)
					reads(n.Value)
					a.targets[key] = append(a.targets[key], n.Target.Span())
					values = append(values, t)
					return true
				}
				ix, ok := n.Target.(*ast.Index)
				if !ok {
					return true
				}
				if isSelf(ix.Array) {
					t, ok := valueOf()
					if !ok {
						why = "an element written to it is not typed"
						return false
					}
					a.site(key, n.S.Start)
					reads(n.Value)
					a.targets[key] = append(a.targets[key], ix.Array.Span())
					if why = element(ix.Index, t); why != "" {
						return false
					}
					return true
				}
				// `$this->p[$a][$b] = …`: the element written is an array
				// with that key, deeper ones are not followed.
				if outer, ok := ix.Array.(*ast.Index); ok {
					if isSelf(outer.Array) && n.Op == token.Kind('=') {
						t, ok := a.types[n.Value.Span()]
						if !ok {
							why = "a nested element written to it is not typed"
							return false
						}
						a.site(key, n.S.Start)
						reads(n.Value)
						inner := types.Array{Key: types.TInt, Value: t, List: ix.Index == nil}
						if ix.Index != nil {
							k, ok := a.types[ix.Index.Span()]
							if !ok {
								why = "a nested key written to it is not typed"
								return false
							}
							inner.Key = k
						}
						if why = element(outer.Index, inner); why != "" {
							return false
						}
						return true
					}
					for e := ast.Expr(outer); ; {
						in, ok := e.(*ast.Index)
						if !ok {
							break
						}
						if isSelf(in.Array) {
							why = "an element is written deeper than one level"
							return false
						}
						e = in.Array
					}
				}
			}
			return true
		}
		written := len(a.sites[key])
		for _, s := range md.Body {
			ast.Inspect(s, visit)
		}
		if why != "" || len(a.sites[key]) == written {
			continue
		}
		// A method that writes the property may write back what it read
		// of it through a local (`$files = $this->trees[$t]; …;
		// $this->trees[$sha] = $files`): its reads are its own values'.
		seen := map[ast.Span]bool{}
		for _, sp := range append(append([]ast.Span{}, a.selfReads[key]...), a.targets[key]...) {
			seen[sp] = true
		}
		for _, s := range md.Body {
			ast.Inspect(s, func(n ast.Node) bool {
				if x, ok := n.(ast.Expr); ok && isSelf(x) && !seen[x.Span()] {
					seen[x.Span()] = true
					a.selfReads[key] = append(a.selfReads[key], x.Span())
				}
				return true
			})
		}
	}
	if why != "" {
		return nil, shape, why
	}
	return values, shape, ""
}

// returnValues collects what a method returns, and its recursive calls,
// which a self round assumes.
func (a *arrayTyper) returnValues(key string, m *ast.MethodDecl) ([]types.Type, string) {
	var values []types.Type
	why := ""
	for _, s := range m.Body {
		ast.Inspect(s, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.MethodCall:
				if v, ok := n.Object.(*ast.Variable); ok && v.Name == "this" && a.names(n.Name, m.Name.Name) && !n.Args.Closure() {
					a.selfReads[key] = append(a.selfReads[key], n.S)
				}
			case *ast.StaticCall:
				if a.ownClass(n.Class) && a.names(n.Name, m.Name.Name) && !n.Args.Closure() {
					a.selfReads[key] = append(a.selfReads[key], n.S)
				}
			}
			return true
		})
		ast.Inspect(s, func(n ast.Node) bool {
			if why != "" {
				return false
			}
			switch n := n.(type) {
			case *ast.Closure, *ast.ArrowFunc, *ast.ClassDecl, *ast.FunctionDecl, *ast.Spawn, *ast.Concurrently:
				return false // their returns are theirs
			case *ast.Yield, *ast.YieldFrom:
				why = "it is a generator"
			case *ast.Return:
				if n.Expr == nil {
					why = "a return has no value"
					return false
				}
				// What the return gives the method: an unnamed @var above it
				// types the value.
				t, ok := a.returned[n]
				if !ok {
					t, ok = a.types[n.Expr.Span()]
				}
				if !ok {
					why = "a returned value is not typed"
					return false
				}
				a.site(key, n.S.Start)
				values = append(values, t)
			}
			return true
		})
	}
	if why != "" {
		return nil, why
	}
	if len(values) == 0 {
		return nil, "it returns nothing"
	}
	return values, ""
}

// callsOf records the calls of a class's method in the class as uses of
// key.
func (a *arrayTyper) callsOf(key string, cl *ast.ClassDecl, name string) {
	ast.Inspect(cl, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.MethodCall:
			if a.names(n.Name, name) {
				a.use(key, n.S.Start)
			}
		case *ast.StaticCall:
			if a.names(n.Name, name) {
				a.use(key, n.S.Start)
			}
		}
		return true
	})
}

// names reports a method name written as the identifier name.
func (a *arrayTyper) names(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && strings.EqualFold(id.Name, name)
}

// ownClass reports self, static or the class's own name before `::`.
func (a *arrayTyper) ownClass(e ast.Expr) bool {
	cn, ok := e.(*ast.Name)
	if !ok || a.pcl == nil {
		return false
	}
	last := cn.Parts[len(cn.Parts)-1]
	short := a.pcl.Name[strings.LastIndex(a.pcl.Name, "\\")+1:]
	return len(cn.Parts) == 1 && (strings.EqualFold(last, "self") || strings.EqualFold(last, "static")) || strings.EqualFold(last, short)
}

// reaches says whether a call on obj calls this class's own method of
// the name: on $this, or on an object whose type is the class or one of
// its subclasses. maybe reports an object whose type does not tell: a
// parent type, a type not known.
func (a *arrayTyper) reaches(obj ast.Expr) (call, maybe bool) {
	if v, ok := obj.(*ast.Variable); ok && v.Name == "this" {
		return true, false
	}
	t, ok := a.types[obj.Span()]
	if !ok || a.pcl == nil {
		return false, true
	}
	call = false
	for _, m := range types.Members(types.StripNull(t)) {
		c, ok := m.(types.Class)
		if !ok {
			return false, true
		}
		switch cc := a.prog.Class(c.Name); {
		case cc == nil:
			return false, true
		case cc.IsSubclassOf(a.pcl.Name):
			call = true
		case a.pcl.IsSubclassOf(cc.Name):
			return false, true // an object of a parent type may be of this class
		}
	}
	return call, false
}

// argValues collects the arguments a private method's parameter receives
// from its class: at each call, piped in (`$x |> $this->m(...)`), and
// where the method is passed as a callable (`array_map([$this, 'm'],
// $xs)`), from the type of the callable the parameter it is passed to
// expects.
func (a *arrayTyper) argValues(cl *ast.ClassDecl, m *ast.MethodDecl, idx int, p *ast.Param) ([]types.Type, string) {
	if p.Variadic || p.ByRef {
		return nil, "a variadic or by-reference parameter"
	}
	name := m.Name.Name
	key := "param " + cl.Name.Name + "::" + name + "$" + p.Var.Name
	var values []types.Type
	calls := 0
	why := ""
	fromDefault := func() bool {
		if p.Default == nil {
			why = "a call leaves it out"
			return false
		}
		if t, ok := a.types[p.Default.Span()]; ok {
			values = append(values, t)
		} else if !isEmptyArray(p.Default) {
			why = "its default is not typed"
			return false
		}
		return true
	}
	// asCallable reads what the method receives when e, a reference to it,
	// is passed as a callable.
	asCallable := func(e ast.Expr) {
		expected, ok := a.callables[e]
		if !ok {
			expected = a.stored[e]
		}
		c, ok := types.StripNull(expected).(types.Callable)
		if !ok || !c.HasSig {
			why = "it is used as a callable whose arguments are not known"
			return
		}
		calls++
		a.site(key, e.Span().Start)
		if idx < len(c.Params) {
			values = append(values, c.Params[idx].Type)
			return
		}
		fromDefault()
	}
	// in is the member being read: an argument passed inside one of the
	// class's methods may carry that method's parameters on.
	var in *ast.MethodDecl
	passes := func(e ast.Expr) {
		if in == nil {
			return
		}
		mentions := false
		ast.Inspect(e, func(n ast.Node) bool {
			if v, ok := n.(*ast.Variable); ok && v.Name != "this" {
				mentions = true
			}
			return !mentions
		})
		if mentions {
			for _, q := range in.Params {
				a.passes[key] = append(a.passes[key], "param "+cl.Name.Name+"::"+in.Name.Name+"$"+q.Var.Name)
			}
		}
	}
	// arg reads what a call passes the parameter. piped, when not nil, is
	// what a pipe gives the call: a first-class callable's first
	// parameter, a partial application's first placeholder.
	arg := func(args *ast.ArgList, piped ast.Expr) {
		calls++
		var found ast.Expr
		if args.FirstClassCallable {
			if idx == 0 {
				found = piped
			}
		} else {
			taken := false // the piped value has its placeholder
			for i, ar := range args.Args {
				if ar.Unpack {
					why = "a call spreads its arguments"
					return
				}
				mine := ar.Name != nil && ar.Name.Name == p.Var.Name || ar.Name == nil && i == idx
				if ar.Placeholder != nil {
					// `...` stands for this parameter and the ones after it.
					covers := mine || ar.Name == nil && ar.Placeholder.Variadic && i < idx
					switch {
					case piped == nil:
						why = "it is applied partially"
						return
					case !taken:
						taken = true
						if mine {
							found = piped
						}
					case covers:
						why = "it is applied partially"
						return
					}
					continue
				}
				if mine {
					found = ar.Value
				}
			}
		}
		if found == nil {
			fromDefault()
			return
		}
		a.site(key, found.Span().Start)
		passes(found)
		t, ok := a.types[found.Span()]
		if !ok {
			if isEmptyArray(found) {
				return
			}
			why = "an argument is not typed"
			return
		}
		values = append(values, t)
	}
	// call reads a call to a method of the name, args nil for a dynamic
	// name; obj is its object, nil for a static call.
	piped := map[ast.Expr]ast.Expr{} // a pipe's stage → what it is given
	call := func(e ast.Expr, obj ast.Expr, class ast.Expr, nameExpr ast.Expr, args *ast.ArgList) {
		id, static := nameExpr.(*ast.Ident)
		if static && !strings.EqualFold(id.Name, name) {
			return // another method
		}
		// Whose method it calls: this class's, another's, or one that
		// cannot be told.
		reaches, maybe := false, false
		switch {
		case obj != nil:
			reaches, maybe = a.reaches(obj)
		default:
			if _, ok := class.(*ast.Name); ok {
				reaches = a.ownClass(class) // not parent::, not another class's
			} else {
				maybe = true // $class::m()
			}
		}
		switch {
		case maybe:
			why = "a call on an object of another type may reach it"
			return
		case !reaches:
			return
		case !static:
			why = "a dynamic method call may reach it"
			return
		case args == nil:
			return
		}
		if left, ok := piped[e]; ok {
			arg(args, left)
			return
		}
		if args.FirstClassCallable {
			asCallable(e)
			return
		}
		arg(args, nil)
	}
	// The strings that name it inside a callable array are accounted for
	// there; another string reaches it only where PHP takes a callable's
	// name, which callableStrings lists. A key or a git argument that
	// happens to spell it does not.
	named := map[*ast.StringLit]bool{}
	fqn := cl.Name.Name
	if a.pcl != nil {
		fqn = a.pcl.Name
	}
	reaching := callableStrings(cl, fqn)
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		if why != "" {
			return false
		}
		switch n := n.(type) {
		case *ast.ClassDecl:
			if n.Args != nil {
				ast.Inspect(n.Args, visit) // an anonymous class's arguments are this class's code
			}
			return false
		case *ast.Binary:
			if n.Op == token.T_PIPE {
				stage := n.Right
				for {
					pe, ok := stage.(*ast.Paren)
					if !ok {
						break
					}
					stage = pe.Expr
				}
				piped[stage] = n.Left
			}
		case *ast.ArrayLit:
			if lit, ok := callableArray(n, name); ok {
				named[lit] = true
				asCallable(n)
			} else if len(n.Items) == 2 && n.Items[0] != nil && n.Items[1] != nil && n.Items[1].Key == nil && selfRef(n.Items[0].Value) {
				if _, lit := n.Items[1].Value.(*ast.StringLit); !lit {
					why = "a callable array names a method by a value"
				}
			}
		case *ast.MethodCall:
			call(n, n.Object, nil, n.Name, n.Args)
		case *ast.StaticCall:
			call(n, nil, n.Class, n.Name, n.Args)
		case *ast.StringLit:
			v := strings.Trim(n.Raw, `'"`)
			if !named[n] && reaching[n] && (strings.EqualFold(v, name) || strings.HasSuffix(strings.ToLower(v), "::"+strings.ToLower(name))) {
				why = "its name is written as a string"
			}
		}
		return true
	}
	for _, mem := range cl.Members {
		in, _ = mem.(*ast.MethodDecl)
		ast.Inspect(mem, visit)
	}
	if why != "" {
		return nil, why
	}
	if calls == 0 {
		return nil, "nothing calls it"
	}
	return values, ""
}

// storedCallables finds the callable references a class assigns to its
// typed properties, as the value or an operand of `??` and `?:`, with the
// property's declared type: what the method so referred to receives is
// what that type's callable is called with.
func storedCallables(cl *ast.ClassDecl, pcl *program.Class) map[ast.Expr]types.Type {
	out := map[ast.Expr]types.Type{}
	if pcl == nil {
		return out
	}
	var leaves func(e ast.Expr, t types.Type)
	leaves = func(e ast.Expr, t types.Type) {
		switch v := e.(type) {
		case *ast.Paren:
			leaves(v.Expr, t)
		case *ast.Binary:
			if v.Op == token.T_COALESCE {
				leaves(v.Left, t)
				leaves(v.Right, t)
			}
		case *ast.Ternary:
			if v.Then != nil {
				leaves(v.Then, t)
			} else {
				leaves(v.Cond, t)
			}
			leaves(v.Else, t)
		case *ast.ArrayLit, *ast.MethodCall, *ast.StaticCall:
			out[e] = t
		}
	}
	ast.Inspect(cl, func(n ast.Node) bool {
		as, ok := n.(*ast.Assign)
		if !ok || as.Op != token.Kind('=') {
			return true
		}
		var name string
		switch t := as.Target.(type) {
		case *ast.PropertyFetch:
			v, ok1 := t.Object.(*ast.Variable)
			id, ok2 := t.Name.(*ast.Ident)
			if !ok1 || !ok2 || v.Name != "this" {
				return true
			}
			name = id.Name
		case *ast.StaticPropertyFetch:
			v, ok := t.Name.(*ast.Variable)
			if !ok {
				return true
			}
			name = v.Name
		default:
			return true
		}
		if pr := pcl.FindProperty(name); pr != nil && pr.Type != nil {
			leaves(as.Value, pr.Type)
		}
		return true
	})
	return out
}

// callableStrings lists the strings of a class that PHP may take as one of
// its methods' names: the second item of a two-item array whose first may
// be the class (a callable array: not `['git', 'status']`), a
// `'Class::method'` string, and an argument of a function that calls or
// looks up a callable by name. fqn is the class's name.
func callableStrings(cl *ast.ClassDecl, fqn string) map[*ast.StringLit]bool {
	out := map[*ast.StringLit]bool{}
	byName := map[string]bool{"call_user_func": true, "call_user_func_array": true, "forward_static_call": true,
		"forward_static_call_array": true, "is_callable": true, "method_exists": true, "register_shutdown_function": true,
		"fromcallable": true, "array_map": true, "array_filter": true, "usort": true, "uasort": true, "uksort": true,
		"array_walk": true, "array_reduce": true, "iterator_apply": true}
	ast.Inspect(cl, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ArrayLit:
			if len(n.Items) == 2 && n.Items[0] != nil && n.Items[1] != nil && n.Items[0].Key == nil && n.Items[1].Key == nil {
				lit, ok := n.Items[1].Value.(*ast.StringLit)
				if !ok {
					break
				}
				// A class named as a string must be this one.
				if first, ok := n.Items[0].Value.(*ast.StringLit); ok {
					v := strings.TrimPrefix(strings.Trim(first.Raw, `'"`), "\\")
					short := fqn[strings.LastIndex(fqn, "\\")+1:]
					if !strings.EqualFold(v, fqn) && !strings.EqualFold(v, short) {
						break
					}
				}
				out[lit] = true
			}
		case *ast.StringLit:
			if strings.Contains(n.Raw, "::") {
				out[n] = true
			}
		case *ast.Call:
			if fn, ok := n.Callee.(*ast.Name); ok && byName[strings.ToLower(fn.Parts[len(fn.Parts)-1])] && n.Args != nil {
				for _, a := range n.Args.Args {
					if lit, ok := a.Value.(*ast.StringLit); ok {
						out[lit] = true
					}
				}
			}
		case *ast.StaticCall:
			if id, ok := n.Name.(*ast.Ident); ok && strings.EqualFold(id.Name, "fromCallable") && n.Args != nil {
				for _, a := range n.Args.Args {
					if lit, ok := a.Value.(*ast.StringLit); ok {
						out[lit] = true
					}
				}
			}
		}
		return true
	})
	return out
}

// selfRef reports `$this`, `self::class` or `static::class`.
func selfRef(e ast.Expr) bool {
	switch r := e.(type) {
	case *ast.Variable:
		return r.Name == "this"
	case *ast.ClassConstFetch:
		cn, ok := r.Class.(*ast.Name)
		id, ok2 := r.Name.(*ast.Ident)
		return ok && ok2 && len(cn.Parts) == 1 && strings.EqualFold(id.Name, "class") &&
			(strings.EqualFold(cn.Parts[0], "self") || strings.EqualFold(cn.Parts[0], "static"))
	}
	return false
}

// callableArray reports `[$this, 'name']`, `[self::class, 'name']` or
// `[static::class, 'name']`, and the string that names the method.
func callableArray(al *ast.ArrayLit, name string) (*ast.StringLit, bool) {
	if len(al.Items) != 2 || al.Items[0] == nil || al.Items[1] == nil || al.Items[0].Key != nil || al.Items[1].Key != nil {
		return nil, false
	}
	lit, ok := al.Items[1].Value.(*ast.StringLit)
	if !ok || !strings.EqualFold(strings.Trim(lit.Raw, `'"`), name) {
		return nil, false
	}
	switch r := al.Items[0].Value.(type) {
	case *ast.Variable:
		return lit, r.Name == "this"
	case *ast.ClassConstFetch:
		cn, ok := r.Class.(*ast.Name)
		id, ok2 := r.Name.(*ast.Ident)
		return lit, ok && ok2 && len(cn.Parts) == 1 && strings.EqualFold(id.Name, "class") &&
			(strings.EqualFold(cn.Parts[0], "self") || strings.EqualFold(cn.Parts[0], "static"))
	}
	return nil, false
}

func isEmptyArray(e ast.Expr) bool {
	al, ok := e.(*ast.ArrayLit)
	return ok && len(al.Items) == 0
}

// fileNames is a file's namespace and its class imports (lower-cased
// FQN → alias), for spelling class names.
func fileNames(f *ast.File) (string, map[string]string) {
	ns := ""
	imports := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Namespace:
			if n.Name != nil {
				ns = strings.Join(n.Name.Parts, "\\")
			}
		case *ast.Use:
			if n.Kind != ast.UseClass {
				return false
			}
			prefix := ""
			if n.Prefix != nil {
				prefix = strings.Join(n.Prefix.Parts, "\\") + "\\"
			}
			for _, it := range n.Items {
				if it.Kind != ast.UseClass {
					continue
				}
				fqn := prefix + strings.Join(it.Name.Parts, "\\")
				alias := it.Name.Parts[len(it.Name.Parts)-1]
				if it.Alias != nil {
					alias = it.Alias.Name
				}
				imports[strings.ToLower(fqn)] = alias
			}
			return false
		case *ast.ClassDecl, *ast.FunctionDecl:
			return false
		}
		return true
	})
	return ns, imports
}

// spellArray writes the type of the values in Phaenon's syntax, or says
// why it cannot: a tuple (A, B) when every value is one of the same length
// whose positions differ in type or are computed rather than literal,
// list<T> when every value is a list and no key is written, map<K, V>
// otherwise, array<V> when the keys are any int|string. An element type
// of more than three members is written mixed: a document's
// map<string, mixed> reads better than every type it holds.
func spellArray(values []types.Type, shape arrayShape, nullable bool, sp *speller) (string, string) {
	return spellArrayAt(values, shape, nullable, false, sp)
}

// spellArrayAt is spellArray for a declaration (nested false) or for an
// element type: a declaration's same-typed positions read as a list, an
// element's computed ones as a tuple, as the dogfood writes list<(string,
// string)> but list<Binary> for five values a method returns.
func spellArrayAt(values []types.Type, shape arrayShape, nullable, nested bool, sp *speller) (string, string) {
	list := !shape.keyed
	keys := append([]types.Type{}, shape.keys...)
	elems := append([]types.Type{}, shape.elems...)
	any := len(elems) > 0
	var tuples [][]types.Type // the positions of each value, while every value is a tuple
	tupleLen, tuplesOnly := -1, len(elems) == 0
	literals := false // a position holds a literal or an array: ['a', 'b'] is a list, [$dir, $plugin] a pair
	records := true   // every position holds an array: rules in a row, a list of whatever types
	var collect func(t types.Type) string
	collect = func(t types.Type) string {
		switch t := t.(type) {
		case types.Union:
			for _, m := range t.Types {
				if why := collect(m); why != "" {
					return why
				}
			}
		case types.Null:
			if !nullable {
				return "it may hold null"
			}
		case types.Never:
		case types.Array:
			if emptyArray(t) {
				tuplesOnly = false // an array of nothing, as array_filter([]) gives
				return ""
			}
			any, tuplesOnly = true, false
			if t.List {
				keys = append(keys, types.TInt) // what a list's keys are, should it be a map
			} else {
				list = false
				keys = append(keys, t.Key)
			}
			elems = append(elems, t.Value)
		case types.Shape:
			if t.Unsealed {
				return "an open shape"
			}
			if len(t.Entries) == 0 {
				tuplesOnly = false // [] fits a list or a map, not a tuple
				return ""
			}
			positional := true
			var pos []types.Type
			for i, e := range t.Entries {
				any = true
				if !(e.IsInt && e.IntKey == int64(i) && !e.Optional) {
					positional = false
				}
				if !(t.List || e.IsInt && e.IntKey == int64(i) && !e.Optional) {
					list = false
				}
				if e.IsInt {
					keys = append(keys, types.TInt)
				} else {
					keys = append(keys, types.TString)
				}
				elems = append(elems, e.Type)
				pos = append(pos, e.Type)
				if isLiteralType(e.Type) || isArrayType(e.Type) {
					literals = true // records in a row are a list, not a pair
				}
				if !isArrayType(e.Type) {
					records = false
				}
			}
			if !positional || len(pos) < 2 || tupleLen >= 0 && tupleLen != len(pos) {
				tuplesOnly = false
			} else {
				tupleLen = len(pos)
				tuples = append(tuples, pos)
			}
		default:
			return "it may hold " + types.Print(t)
		}
		return ""
	}
	for _, v := range values {
		if why := collect(v); why != "" {
			return "", why
		}
	}
	if !any {
		return "", "it only ever holds []"
	}
	if tuplesOnly && len(tuples) > 0 {
		var parts []string
		for i := 0; i < tupleLen; i++ {
			var at []types.Type
			for _, tup := range tuples {
				at = append(at, tup[i])
			}
			one, ok := sp.union(at)
			if !ok {
				return "", "an element type has no spelling"
			}
			parts = append(parts, one)
		}
		// Positions of one type read as a list when they hold literals, as
		// ['a', 'b'] does, or at a declaration's top; computed ones in an
		// element type, [$dir, $plugin], make a pair.
		differ := nested && !literals
		for _, p := range parts[1:] {
			if p != parts[0] && !records {
				differ = true // (int, string): a pair
			}
		}
		// How the code reads the values decides, where it tells: taken
		// apart (`[$commit, $body] = …`) they are pairs, iterated a list.
		if sp != nil && sp.depth < len(sp.uses) {
			switch sp.uses[sp.depth] {
			case usedAsTuple:
				differ = true
			case usedAsList:
				differ = false
			}
		}
		if differ {
			return "(" + strings.Join(parts, ", ") + ")", ""
		}
	}
	elem, ok := sp.union(elems)
	if !ok {
		return "", "an element type has no spelling"
	}
	if members(elem) > 3 {
		elem = "mixed"
	}
	// For ?array the `?` stays where it is written; null is a value here.
	if list {
		return "list<" + elem + ">", ""
	}
	key, ok := sp.union(keys)
	if !ok || key == "mixed" || key == "int|string" || key == "string|int" {
		if elem == "mixed" {
			return "", "nothing is known of its keys or elements"
		}
		return "array<" + elem + ">", ""
	}
	return "map<" + key + ", " + elem + ">", ""
}

// isArrayType reports an array type, or a union of them.
func isArrayType(t types.Type) bool {
	for _, m := range types.Members(t) {
		switch m.(type) {
		case types.Array, types.Shape:
		default:
			return false
		}
	}
	return true
}

// isLiteralType reports a type that is one value: a literal, an enum case.
func isLiteralType(t types.Type) bool {
	switch t := t.(type) {
	case types.StringLit, types.FloatLit, types.BoolLit, types.EnumCase, types.Null:
		return true
	case types.Int:
		return t.Lo == t.Hi
	case types.Union:
		for _, m := range t.Types {
			if !isLiteralType(m) {
				return false
			}
		}
		return true
	}
	return false
}

// members counts the members of a spelled union at its top level.
func members(spelled string) int {
	n, depth := 1, 0
	for _, c := range spelled {
		switch c {
		case '<', '(', '{':
			depth++
		case '>', ')', '}':
			depth--
		case '|':
			if depth == 0 {
				n++
			}
		}
	}
	if strings.HasPrefix(spelled, "?") {
		n++
	}
	return n
}

// speller spells lattice types in Phaenon's syntax, widened to what can be
// written: literals to their types, shapes to the list or map they are,
// refinements to their base types.
type speller struct {
	ns      string
	imports map[string]string
	prog    *program.Program // for the class an anonymous one extends; may be nil
	// uses says how the declaration being spelled is read, at its top and
	// in its elements, depth the level being spelled.
	uses  [2]tupleUse
	depth int
}

// tupleUse is what the reads of a fixed-length array say it is.
type tupleUse int

const (
	usedUnknown tupleUse = iota
	usedAsTuple          // only taken apart, `[$a, $b] = $x`, or read at a literal index
	usedAsList           // iterated, or spread
)

func (s *speller) union(ts []types.Type) (string, bool) {
	seen := map[string]bool{}
	var parts []string
	nullable := false
	classes := s.classNames(ts) // literals that all name classes: a class<…>
	var arrays []types.Type     // spelled together, as one list, map or tuple
	var add func(t types.Type) bool
	add = func(t types.Type) bool {
		if u, ok := t.(types.Union); ok {
			for _, m := range u.Types {
				if !add(m) {
					return false
				}
			}
			return true
		}
		switch t.(type) {
		case types.Array, types.Shape:
			arrays = append(arrays, t)
			return true
		}
		if _, ok := t.(types.Never); ok {
			return true
		}
		if _, ok := t.(types.Null); ok {
			nullable = true
			return true
		}
		one, ok := s.one(t)
		if !ok {
			return false
		}
		if _, lit := t.(types.StringLit); lit && classes != "" {
			one = classes
		}
		if one == "mixed" {
			parts, seen = []string{"mixed"}, map[string]bool{"mixed": true}
			return true
		}
		if !seen[one] && !seen["mixed"] {
			seen[one] = true
			parts = append(parts, one)
		}
		return true
	}
	for _, t := range ts {
		if !add(t) {
			return "", false
		}
	}
	// Lists and maps among the elements are spelled apart, as a decoded
	// JSON answer is map<string, mixed>|list<map<string, mixed>>: one
	// array<…> of both would know neither its keys nor its elements.
	for _, group := range listsAndMaps(arrays) {
		s.depth++
		one, why := spellArrayAt(group, arrayShape{}, false, true, s)
		s.depth--
		switch {
		case why == "":
		case why == "it only ever holds []" || why == "nothing is known of its keys or elements":
			one = "array<mixed>"
		default:
			return "", false
		}
		if !seen[one] {
			seen[one] = true
			parts = append(parts, one)
		}
	}
	if seen["mixed"] {
		return "mixed", true
	}
	if len(parts) == 0 {
		if nullable {
			return "null", true
		}
		return "", false
	}
	// bool covers true and false.
	if seen["true"] && seen["false"] || seen["bool"] {
		var kept []string
		for _, p := range parts {
			if p != "true" && p != "false" {
				kept = append(kept, p)
			}
		}
		if !seen["bool"] {
			kept = append(kept, "bool")
		}
		parts = kept
	}
	out := strings.Join(parts, "|")
	if nullable {
		if len(parts) == 1 && !strings.ContainsAny(out, "<(") {
			return "?" + out, true
		}
		return out + "|null", true
	}
	return out, true
}

// listsAndMaps groups array types to be spelled together: the lists
// (tuples among them) apart from the maps of string keys when there are
// both, in the order they come; all of them as one group when any is
// keyed otherwise. An empty array goes with the first group.
func listsAndMaps(arrays []types.Type) [][]types.Type {
	if len(arrays) == 0 {
		return nil
	}
	var lists, maps, empty []types.Type
	listFirst := false
	for _, t := range arrays {
		isList, isMap := false, false
		switch t := t.(type) {
		case types.Array:
			switch {
			case emptyArray(t):
			case t.List:
				isList = true
			case types.IsSubtype(t.Key, types.TString, nil):
				isMap = true
			default:
				return [][]types.Type{arrays}
			}
		case types.Shape:
			switch {
			case len(t.Entries) == 0:
			case t.List || shapeIsPositional(t):
				isList = true
			case shapeHasStringKeys(t):
				isMap = true
			default:
				return [][]types.Type{arrays}
			}
		}
		switch {
		case isList:
			if len(lists) == 0 && len(maps) == 0 {
				listFirst = true
			}
			lists = append(lists, t)
		case isMap:
			maps = append(maps, t)
		default:
			empty = append(empty, t)
		}
	}
	if len(lists) == 0 || len(maps) == 0 {
		return [][]types.Type{arrays}
	}
	if listFirst {
		return [][]types.Type{append(lists, empty...), maps}
	}
	return [][]types.Type{append(maps, empty...), lists}
}

// shapeIsPositional reports a shape whose keys are 0, 1, … in order.
func shapeIsPositional(t types.Shape) bool {
	for i, e := range t.Entries {
		if !e.IsInt || e.IntKey != int64(i) || e.Optional {
			return false
		}
	}
	return true
}

// shapeHasStringKeys reports a shape whose every key is a string.
func shapeHasStringKeys(t types.Shape) bool {
	for _, e := range t.Entries {
		if e.IsInt {
			return false
		}
	}
	return true
}

// classNames spells the string literals among ts as the class-string
// they are when every one names a class, as Foo::class does: class<Foo>
// for one class, class for several; "" when one names none, or another
// string is among them.
func (s *speller) classNames(ts []types.Type) string {
	if s.prog == nil {
		return ""
	}
	var names []string
	other := false
	var walk func(t types.Type)
	walk = func(t types.Type) {
		switch t := t.(type) {
		case types.Union:
			for _, m := range t.Types {
				walk(m)
			}
		case types.StringLit:
			// Spelled as declared, as Foo::class gives it: 'error' is a
			// word, not the class Error.
			if cl := s.prog.Class(t.V); cl == nil || cl.Name != strings.TrimPrefix(t.V, "\\") {
				other = true
			}
			names = append(names, t.V)
		case types.String:
			other = true
		}
	}
	for _, t := range ts {
		walk(t)
	}
	if other || len(names) == 0 {
		return ""
	}
	for _, n := range names[1:] {
		if !strings.EqualFold(n, names[0]) {
			return "class"
		}
	}
	return "class<" + s.class(s.prog.Class(names[0]).Name) + ">"
}

func (s *speller) one(t types.Type) (string, bool) {
	switch t := t.(type) {
	case types.Mixed:
		return "mixed", true
	case types.Int:
		return "int", true
	case types.Float, types.FloatLit:
		return "float", true
	case types.String, types.StringLit:
		return "string", true
	case types.Bool:
		return "bool", true
	case types.BoolLit:
		if t.V {
			return "true", true
		}
		return "false", true
	case types.Object:
		return "object", true
	case types.Class:
		if strings.Contains(t.Name, "@anonymous") {
			// An anonymous class has no name to write: the class it extends,
			// or the one interface it implements, stands for it.
			if s.prog == nil {
				return "", false
			}
			cl := s.prog.Class(t.Name)
			switch {
			case cl == nil:
				return "", false
			case cl.Parent != "":
				return s.one(types.Class{Name: cl.Parent})
			case len(cl.Interfaces) == 1:
				return s.one(types.Class{Name: cl.Interfaces[0]})
			}
			return "", false
		}
		name := s.class(t.Name)
		var args []string
		for _, a := range t.Args {
			if _, never := a.(types.Never); never {
				return name, true // nothing known of its arguments: the class alone
			}
			one, ok := s.union([]types.Type{a})
			if !ok {
				return name, true
			}
			args = append(args, one)
		}
		if len(args) > 0 {
			return name + "<" + strings.Join(args, ", ") + ">", true
		}
		return name, true
	case types.EnumCase:
		return s.class(t.Class), true
	case types.Resource:
		return "resource", true
	case types.Param:
		// A template by its name: one the declaration's scope does not
		// have fails the check, which leaves the declaration alone.
		if strings.Contains(t.Name, "\x00") {
			return "", false
		}
		return t.Name, true
	case types.Callable:
		if !t.HasSig {
			if t.Closure {
				return "\\Closure", true
			}
			return "callable", true
		}
		if !t.Closure {
			return "", false // a callable(…) signature has no native spelling
		}
		var params []string
		for _, p := range t.Params {
			one, ok := s.union([]types.Type{p.Type})
			if !ok || p.ByRef {
				return "", false
			}
			switch {
			case p.Variadic:
				one += "..."
			case p.Optional:
				one += "="
			}
			params = append(params, one)
		}
		ret := "mixed"
		if t.Return != nil {
			if _, void := t.Return.(types.Void); void {
				ret = "void"
			} else if r, ok := s.union([]types.Type{t.Return}); ok {
				ret = r
			} else {
				return "", false
			}
		}
		return "Closure(" + strings.Join(params, ", ") + "): " + ret, true
	case types.ClassString:
		switch of := t.Of.(type) {
		case nil, types.Object:
			return "class", true // $object::class names some class
		case types.Class:
			if !strings.Contains(of.Name, "@anonymous") {
				return "class<" + s.class(of.Name) + ">", true
			}
		case types.Param:
			if !strings.Contains(of.Name, "\x00") {
				return "class<" + of.Name + ">", true
			}
		}
		return "string", true
	}
	return "", false
}

// class spells a class name as the file names it: by its import, short in
// the file's namespace, else fully qualified.
func (s *speller) class(fqn string) string {
	if alias, ok := s.imports[strings.ToLower(fqn)]; ok {
		return alias
	}
	if s.ns != "" && strings.HasPrefix(strings.ToLower(fqn), strings.ToLower(s.ns)+"\\") && !strings.Contains(fqn[len(s.ns)+1:], "\\") {
		return fqn[len(s.ns)+1:]
	}
	if s.ns == "" && !strings.Contains(fqn, "\\") {
		return fqn
	}
	return "\\" + fqn
}

func lineStarts(src []byte) []int {
	starts := []int{0}
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func lineAt(starts []int, off int) int {
	return sort.Search(len(starts), func(i int) bool { return starts[i] > off })
}
