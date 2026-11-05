package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/program"
)

// TestThrowsMeasure measures what a throws codemod would write on real
// trees, before one is built (design/tooling.md §8.3): for each function
// and method with a body and no declaration of its own, the checked
// exceptions its body throws. Three rules are measured side by side:
//
//   - own: the body's own throw statements (`throw new X`), uncaught;
//   - declared: those, and what the callees it calls declare they throw;
//   - promoted: the declared rule run to a fixpoint, each round's clauses
//     read by the next as declarations, which is what holding every
//     caller to declare what its callees throw would amount to.
//
// Unchecked exceptions are LogicException, Error and their subclasses;
// PHAENON_THROWS_UNCHECKED adds classes, comma-separated. PHAENON_THROWS
// names the trees, each the directory of a phaenon.json, separated as
// PATH is.
func TestThrowsMeasure(t *testing.T) {
	trees := os.Getenv("PHAENON_THROWS")
	if trees == "" {
		t.Skip("PHAENON_THROWS names the trees to measure on")
	}
	unchecked := []string{"LogicException", "Error"}
	if s := os.Getenv("PHAENON_THROWS_UNCHECKED"); s != "" {
		unchecked = append(unchecked, strings.Split(s, ",")...)
	}
	for _, tree := range filepath.SplitList(trees) {
		t.Run(filepath.Base(tree), func(t *testing.T) { measureThrows(t, tree, unchecked) })
	}
}

// throwsDecl is one declaration the measurement types.
type throwsDecl struct {
	path   string
	rel    string
	name   string
	at     int    // where a clause goes: after the return type, or the signature's `)`
	extend int    // the end of an existing clause to extend, or -1
	root   string // the first path segment: src, tests
	line   int    // the line of the name
	own    map[string]bool
	decl   map[string]bool
	prom   map[string]bool
	hasDoc bool // declares @throws or throws already
}

func measureThrows(t *testing.T, tree string, unchecked []string) {
	t.Chdir(tree)
	savedExcludes, savedSources, savedShare := excludes, projectSources, share
	t.Cleanup(func() { excludes, projectSources, share = savedExcludes, savedSources, savedShare })
	proj := loadProject()
	if proj == nil {
		t.Fatalf("%s: no %s", tree, ProjectFile)
	}
	roots := proj.roots()
	excludes = append(excludes[:0:0], proj.Exclude...)
	projectSources = proj.sources()
	skip := func(path string) bool { return excluded(path) || proj.isOutput(path) }
	files := collect(roots, skip, ".phn")
	if len(files) == 0 {
		t.Fatalf("%s: no .phn under %s", tree, strings.Join(roots, ", "))
	}
	load := migrateLoad(files[0], projectTarget(proj, roots).Version)
	srcs := map[string][]byte{}
	all := map[string]bool{}
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		srcs[path] = src
		all[path] = true
	}
	opts := proj.checkerOptions(checker.Options{Deprecations: true})

	// check runs the checker over srcs, calling seen with each body's
	// points, and returns the findings.
	check := func(srcs map[string][]byte, seen func(p *program.Program, f *program.File, d ast.Node, points []checker.ThrowPoint)) ([]checker.Diagnostic, *program.Program) {
		p, err := load(srcs, all)
		if err != nil {
			t.Fatal(err)
		}
		o := opts
		o.Thrown = func(f *program.File, d ast.Node, points []checker.ThrowPoint) { seen(p, f, d, points) }
		return checker.New(p, o).Check(), p
	}
	// PHAENON_THROWS_OWNED, when set, counts only the exception classes
	// under these namespace prefixes (comma-separated): a project's own.
	var owned []string
	if s := os.Getenv("PHAENON_THROWS_OWNED"); s != "" {
		owned = strings.Split(s, ",")
	}
	isUnchecked := func(p *program.Program, name string) bool {
		if len(owned) > 0 {
			ok := false
			for _, o := range owned {
				if strings.HasPrefix(name, o) {
					ok = true
				}
			}
			if !ok {
				return true
			}
		}
		for _, u := range unchecked {
			if strings.EqualFold(name, u) || p.IsSubclassOf(name, u) {
				return true
			}
		}
		return false
	}
	// checkedOf lists the checked classes of points: own throws only, or
	// callee declarations too.
	checkedOf := func(p *program.Program, points []checker.ThrowPoint, calls bool) map[string]bool {
		out := map[string]bool{}
		for _, tp := range points {
			if !tp.Explicit || tp.Call && !calls {
				continue
			}
			for _, c := range tp.Classes {
				if c == "Throwable" || isUnchecked(p, c) {
					continue
				}
				out[c] = true
			}
		}
		return out
	}

	// The first check: every declaration, its own and declared sets.
	decls := map[ast.Node]*throwsDecl{}
	var order []*throwsDecl
	base, _ := check(srcs, func(p *program.Program, f *program.File, d ast.Node, points []checker.ThrowPoint) {
		td := &throwsDecl{path: f.Path, rel: filepath.ToSlash(strings.TrimPrefix(f.Path, tree+string(filepath.Separator)))}
		td.root = strings.SplitN(td.rel, "/", 2)[0]
		var name *ast.Ident
		var ret ast.TypeExpr
		var throws *ast.Throws
		var doc string
		var body bool
		switch d := d.(type) {
		case *ast.FunctionDecl:
			name, ret, throws, doc, body = d.Name, d.ReturnType, d.Throws, d.Doc, true
		case *ast.MethodDecl:
			name, ret, throws, doc, body = d.Name, d.ReturnType, d.Throws, d.Doc, d.HasBody
		default:
			return
		}
		if !body || name == nil {
			return
		}
		td.name = name.Name
		td.line = lineOf(srcs[f.Path], name.S.Start)
		td.hasDoc = throws != nil || docThrows(doc)
		td.extend = -1
		if throws != nil {
			td.extend = throws.S.End
		}
		if ret != nil {
			td.at = ret.Span().End
		} else {
			td.at = signatureEnd(srcs[f.Path], name.S.End)
		}
		td.own, td.decl = checkedOf(p, points, false), checkedOf(p, points, true)
		decls[d] = td
		order = append(order, td)
	})
	baseCount := len(base)

	// Promotion to a fixpoint: each round writes the clauses the last one
	// found and checks again, reading what the callees now declare.
	// A clause adds no line, so a declaration keeps its name's line in
	// every rewrite: the key.
	prom := map[string]map[string]bool{} // path:line → classes
	key := func(td *throwsDecl) string { return td.path + ":" + fmt.Sprint(td.line) }
	for _, td := range order {
		prom[key(td)] = copySet(td.decl)
	}
	written := func(clauses map[string]map[string]bool) map[string][]byte {
		out := map[string][]byte{}
		edits := map[string][]clauseEdit{}
		for _, td := range order {
			cs := clauses[key(td)]
			if len(cs) == 0 {
				continue
			}
			edits[td.path] = append(edits[td.path], clauseEdit{td, cs})
		}
		for path, src := range srcs {
			es := edits[path]
			if len(es) == 0 {
				out[path] = src
				continue
			}
			sort.Slice(es, func(i, j int) bool { return es[i].d.at > es[j].d.at })
			s := string(src)
			for _, e := range es {
				var names []string
				for c := range e.classes {
					names = append(names, "\\"+c)
				}
				sort.Strings(names)
				if e.d.extend >= 0 {
					s = s[:e.d.extend] + "|" + strings.Join(names, "|") + s[e.d.extend:]
				} else {
					s = s[:e.d.at] + " throws " + strings.Join(names, "|") + s[e.d.at:]
				}
			}
			out[path] = []byte(s)
		}
		return out
	}
	rounds := 0
	for ; rounds < 10; rounds++ {
		grew := false
		w := written(prom)
		_, _ = check(w, func(p *program.Program, f *program.File, d ast.Node, points []checker.ThrowPoint) {
			var name *ast.Ident
			switch d := d.(type) {
			case *ast.FunctionDecl:
				name = d.Name
			case *ast.MethodDecl:
				name = d.Name
			}
			if name == nil {
				return
			}
			k := f.Path + ":" + fmt.Sprint(lineOf(w[f.Path], name.S.Start))
			cs, ok := prom[k]
			if !ok {
				return
			}
			for c := range checkedOf(p, points, true) {
				if !cs[c] {
					cs[c] = true
					grew = true
				}
			}
		})
		if !grew {
			break
		}
	}
	for _, td := range order {
		td.prom = prom[key(td)]
	}

	// Findings with the own rule's clauses written, and the promoted ones.
	ownClauses := map[string]map[string]bool{}
	for _, td := range order {
		if !td.hasDoc {
			ownClauses[key(td)] = td.own
		}
	}
	promClauses := map[string]map[string]bool{}
	for _, td := range order {
		if !td.hasDoc {
			promClauses[key(td)] = td.prom
		}
	}
	ownFindings, _ := check(written(ownClauses), func(*program.Program, *program.File, ast.Node, []checker.ThrowPoint) {})
	promFindings, _ := check(written(promClauses), func(*program.Program, *program.File, ast.Node, []checker.ThrowPoint) {})

	// Report.
	type tally struct {
		decls, clauses int
		classes        map[string]int
		byRoot         map[string]int
	}
	count := func(pick func(td *throwsDecl) map[string]bool) tally {
		ta := tally{classes: map[string]int{}, byRoot: map[string]int{}}
		for _, td := range order {
			if td.hasDoc {
				continue
			}
			ta.decls++
			cs := pick(td)
			if len(cs) == 0 {
				continue
			}
			ta.clauses++
			ta.byRoot[td.root]++
			for c := range cs {
				ta.classes[c]++
			}
		}
		return ta
	}
	top := func(m map[string]int, n int) string {
		type kv struct {
			k string
			v int
		}
		var kvs []kv
		for k, v := range m {
			kvs = append(kvs, kv{k, v})
		}
		sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v || kvs[i].v == kvs[j].v && kvs[i].k < kvs[j].k })
		var parts []string
		for i, e := range kvs {
			if i == n {
				break
			}
			parts = append(parts, fmt.Sprintf("%s %d", e.k, e.v))
		}
		return strings.Join(parts, ", ")
	}
	declared := 0
	for _, td := range order {
		if td.hasDoc {
			declared++
		}
	}
	t.Logf("%d bodies, %d declare throws already; unchecked: %s", len(order), declared, strings.Join(unchecked, ", "))
	for _, r := range []struct {
		name string
		pick func(td *throwsDecl) map[string]bool
	}{{"own", func(td *throwsDecl) map[string]bool { return td.own }}, {"declared", func(td *throwsDecl) map[string]bool { return td.decl }}, {"promoted", func(td *throwsDecl) map[string]bool { return td.prom }}} {
		ta := count(r.pick)
		t.Logf("  %-8s %d of %d bodies get a clause (%d%%): by root %s; classes: %s", r.name, ta.clauses, ta.decls, 100*ta.clauses/max(ta.decls, 1), top(ta.byRoot, 5), top(ta.classes, 12))
	}
	t.Logf("  promotion settled after %d rounds", rounds)
	t.Logf("  phn check: %d findings as is, %d with the own clauses, %d with the promoted ones", baseCount, len(ownFindings), len(promFindings))
	if os.Getenv("PHAENON_THROWS_FINDINGS") != "" {
		for _, d := range newDiags(base, ownFindings) {
			t.Logf("    own: %s:%d %s %s", strings.TrimPrefix(d.File, tree+string(filepath.Separator)), d.Line, d.Code, d.Message)
		}
		for _, d := range newDiags(base, promFindings) {
			t.Logf("    promoted: %s:%d %s %s", strings.TrimPrefix(d.File, tree+string(filepath.Separator)), d.Line, d.Code, d.Message)
		}
	}
	// PHAENON_THROWS_LIST lists every clause the own rule writes.
	if os.Getenv("PHAENON_THROWS_LIST") != "" {
		for _, td := range order {
			if td.hasDoc || len(td.own) == 0 {
				continue
			}
			var cs []string
			for c := range td.own {
				cs = append(cs, c)
			}
			sort.Strings(cs)
			t.Logf("    %s:%d %s(): throws %s", td.rel, td.line, td.name, strings.Join(cs, "|"))
		}
	}
	// Of the bodies whose authors declared throws: how many throw a
	// checked exception themselves, and how many declare what only their
	// callees throw.
	ownThrows, viaCallees, neither := 0, 0, 0
	for _, td := range order {
		switch {
		case !td.hasDoc:
		case len(td.own) > 0:
			ownThrows++
		case len(td.decl) > 0:
			viaCallees++
		default:
			neither++
		}
	}
	t.Logf("  declared by the authors: %d throw a checked exception themselves, %d only through callees, %d neither (unchecked, or callees without declarations)", ownThrows, viaCallees, neither)
}

func lineOf(src []byte, off int) int {
	return 1 + strings.Count(string(src[:min(off, len(src))]), "\n")
}

type clauseEdit struct {
	d       *throwsDecl
	classes map[string]bool
}

func copySet(m map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// docThrows reports a docblock with a @throws tag.
func docThrows(doc string) bool {
	return doc != "" && len(docblock.Parse(doc).Throws) > 0
}

// signatureEnd is the offset after the `)` closing the parameter list
// that follows a declaration's name.
func signatureEnd(src []byte, from int) int {
	depth := 0
	for i := from; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return from
}

func newDiags(before, after []checker.Diagnostic) []checker.Diagnostic {
	was := map[string]int{}
	for _, d := range before {
		was[fmt.Sprintf("%s:%d:%s", d.File, d.Line, d.Code)]++
	}
	var out []checker.Diagnostic
	for _, d := range after {
		k := fmt.Sprintf("%s:%d:%s", d.File, d.Line, d.Code)
		if was[k] > 0 {
			was[k]--
			continue
		}
		out = append(out, d)
	}
	return out
}
