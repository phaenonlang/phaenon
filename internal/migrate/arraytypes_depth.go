package migrate

// The list-types codemod's last pass (design/tooling.md §8.2): a type is
// spelled only as deep as the code uses it. The rounds spell what a
// declaration's values are, all the way down, so a document a test hands
// to json_encode() comes out nine levels deep. The pass cuts each type at
// the shallowest level that adds no finding to the files the type
// reaches; the uses are the checker's: indexes, loops, destructurings,
// and the typed parameters, returns and properties a value is handed to.
// At the cut an array keeps its kind, its keys and what it holds besides
// arrays (a scalar or an object is a word), and holds mixed where it held
// arrays; a level of arrays beside other things, or of two kinds of
// array, is mixed whole. What the declaration holds keeps its kind when
// it is one kind of array: list<map<string, mixed>>, not list<mixed>.
// Each declaration's shallower spellings are tried in turn, the
// shallowest first, and the first the check takes is kept.

import (
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// depthPasses bounds the passes over the declarations: a cut lets those
// whose values go to it be cut in the next one.
const depthPasses = 8

// deepDecl is a declaration a round typed two or more array levels deep.
type deepDecl struct {
	path string
	span ast.Span
	// cuts holds its spellings, the shallowest first and the type as the
	// rounds wrote it last; at is the one chosen.
	cuts []string
	at   int
	// files are the files a change of its type reaches: its own, and for
	// a return other files may read, those that may call it.
	files map[string]bool
	// named is how a finding names it: `::m()` for a method's return or
	// parameter, `$p` for a property.
	named string
	// from is the shallowest spelling worth trying: one the code's own
	// uses refused is refused again, as is every shallower one. blocked
	// holds the declarations whose types refused the others, which a cut
	// of one of them may let through, and retry asks for a next pass.
	from    int
	try     int
	blocked map[*deepDecl]bool
	retry   bool
}

// shallow cuts the types the rounds wrote to the depth the code uses.
func (r *arrayRun) shallow() error {
	decls, byPath := r.deepDecls()
	if len(decls) == 0 {
		return nil
	}
	// The findings with the types as the rounds wrote them, which no cut
	// may add to.
	all := map[string]bool{}
	for _, d := range decls {
		for f := range d.files {
			all[f] = true
		}
	}
	ref, err := checkSome(r.srcs, all, nil, r.load)
	if err != nil {
		return err
	}
	refByFile := byFile(ref.diags)
	source := func(path string, trying map[*deepDecl]bool) []byte {
		var edits []edit
		for _, d := range byPath[path] {
			at := d.at
			if trying[d] {
				at = d.try
			}
			if at != len(d.cuts)-1 {
				edits = append(edits, edit{d.span, d.cuts[at]})
			}
		}
		if len(edits) == 0 {
			return r.srcs[path]
		}
		return (&lifter{src: r.srcs[path], edits: edits}).apply()
	}

	for pass := 0; pass < depthPasses; pass++ {
		// Each declaration tries the spellings shallower than its own, the
		// shallowest first. One check tries several, of declarations whose
		// types reach no file in common: what it finds in a file is the
		// one declaration's whose type reaches it.
		var pending []*deepDecl
		for _, d := range decls {
			if (pass == 0 || d.retry) && d.from < d.at {
				d.try = d.from
				pending = append(pending, d)
			}
			d.retry = false
		}
		if len(pending) == 0 {
			break
		}
		var cut []*deepDecl
		for len(pending) > 0 {
			reached := map[string]bool{}
			trying := map[*deepDecl]bool{}
			var rest []*deepDecl
			for _, d := range pending {
				if overlaps(d.files, reached) {
					rest = append(rest, d)
					continue
				}
				for f := range d.files {
					reached[f] = true
				}
				trying[d] = true
			}
			srcs := map[string][]byte{}
			for path, src := range r.srcs {
				srcs[path] = src
			}
			for path := range reached {
				if len(byPath[path]) > 0 {
					srcs[path] = source(path, trying)
				}
			}
			after, err := checkSome(srcs, reached, nil, r.load)
			if err != nil {
				return err
			}
			afterByFile := byFile(after.diags)
			for _, d := range pending {
				if !trying[d] {
					continue
				}
				var found []checker.Diagnostic
				for f := range d.files {
					found = append(found, newFindings(refByFile[f], afterByFile[f])...)
				}
				if len(found) == 0 {
					d.at = d.try
					cut = append(cut, d)
					continue
				}
				// A finding that names another declaration cut too deep
				// for this one's values may go once that one is cut; any
				// other is this one's own uses.
				blockers := map[*deepDecl]bool{}
				own := false
				for _, f := range found {
					named := false
					for _, e := range decls {
						if e != d && e.at > 0 && strings.Contains(f.Message, e.named) {
							blockers[e], named = true, true
						}
					}
					own = own || !named
				}
				if own {
					d.from = d.try + 1
				} else {
					if d.blocked == nil {
						d.blocked = map[*deepDecl]bool{}
					}
					for e := range blockers {
						d.blocked[e] = true
					}
				}
				if d.try++; d.try < d.at {
					rest = append(rest, d)
				}
			}
			pending = rest
		}
		for _, d := range decls {
			for _, c := range cut {
				if d.blocked[c] {
					d.retry = true
				}
			}
		}
	}
	for path := range byPath {
		r.srcs[path] = source(path, nil)
	}
	return nil
}

// deepDecls lists the declarations the rounds typed two or more array
// levels deep, by file too: the files in order, each from its end.
func (r *arrayRun) deepDecls() ([]*deepDecl, map[string][]*deepDecl) {
	var paths []string
	for path := range r.typedAs {
		if r.watch[path] {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	var decls []*deepDecl
	byPath := map[string][]*deepDecl{}
	for _, path := range paths {
		src := r.srcs[path]
		tree, err := parser.Parse(src, lexer.ModePhaenon)
		if err != nil {
			continue
		}
		cs := typedDecls(tree, r.typedAs[path])
		// The last first: a method is usually written below its callers,
		// and a declaration whose values go to another is cut once that
		// one is.
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].span.Start > cs[j].span.Start })
		for _, c := range cs {
			cuts := depthCuts(src, c.typ)
			if len(cuts) < 2 {
				continue
			}
			d := &deepDecl{path: path, span: c.span, cuts: cuts, at: len(cuts) - 1, files: map[string]bool{path: true}, named: "::" + c.method + "()"}
			if c.method == "" {
				d.named = "$" + c.key[strings.LastIndex(c.key, "::")+2:]
			}
			if c.exported {
				for p := range r.mentioning([]string{strings.ToLower(c.method)}) {
					d.files[p] = true
				}
			}
			decls = append(decls, d)
			byPath[path] = append(byPath[path], d)
		}
	}
	return decls, byPath
}

func overlaps(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

// depthCuts lists the spellings of a written type as deep as a use may
// reach, the shallowest first: one array level, two, …, and the type as
// written last.
func depthCuts(src []byte, t ast.TypeExpr) []string {
	s := t.Span()
	whole := string(src[s.Start:s.End])
	var out []string
	for k := 1; k < arrayDepth(t); k++ {
		cut := whole
		if edits := depthCut(t, 1, k); len(edits) > 0 {
			cut = string((&lifter{src: src[:s.End], edits: edits}).apply()[s.Start:])
		}
		if cut != whole && (len(out) == 0 || out[len(out)-1] != cut) {
			out = append(out, cut)
		}
	}
	return append(out, whole)
}

// depthCut is the edits that spell t, whose arrays are level d, only k
// levels deep: what a level k array holds is mixed where it holds an
// array, but for one kind of array, which keeps its kind and keys and
// holds mixed where it held an array.
func depthCut(t ast.TypeExpr, d, k int) []edit {
	var out []edit
	for _, m := range typeMembers(t) {
		if !isArrayTypeExpr(m) {
			continue
		}
		for _, e := range heldTypes(m) {
			switch {
			case arrayDepth(e) == 0:
			case d < k:
				out = append(out, depthCut(e, d+1, k)...)
			case isArrayTypeExpr(e):
				for _, inner := range heldTypes(e) {
					if arrayDepth(inner) > 0 {
						out = append(out, edit{inner.Span(), "mixed"})
					}
				}
			default:
				out = append(out, edit{e.Span(), "mixed"})
			}
		}
	}
	return out
}

// typeMembers lists the members of a union, through a nullable type.
func typeMembers(t ast.TypeExpr) []ast.TypeExpr {
	switch t := t.(type) {
	case *ast.NullableType:
		return typeMembers(t.Type)
	case *ast.UnionType:
		var out []ast.TypeExpr
		for _, m := range t.Types {
			out = append(out, typeMembers(m)...)
		}
		return out
	}
	return []ast.TypeExpr{t}
}

// isArrayTypeExpr reports a written array type: list<…>, map<…>,
// array<…> and their non-empty forms, or a tuple.
func isArrayTypeExpr(t ast.TypeExpr) bool {
	switch t := t.(type) {
	case *ast.TupleType:
		return true
	case *ast.NamedType:
		if len(t.TypeArgs) == 0 || len(t.Name.Parts) != 1 {
			return false
		}
		switch strings.ToLower(t.Name.Parts[0]) {
		case "list", "map", "array", "non-empty-list", "non-empty-array":
			return true
		}
	}
	return false
}

// heldTypes lists what an array type holds: a tuple's elements, the
// values of the others (keys are never arrays).
func heldTypes(t ast.TypeExpr) []ast.TypeExpr {
	switch t := t.(type) {
	case *ast.TupleType:
		return t.Elems
	case *ast.NamedType:
		return t.TypeArgs[len(t.TypeArgs)-1:]
	}
	return nil
}

// arrayDepth is how many array levels a written type nests: 0 for one
// that is no array, 1 for list<int>.
func arrayDepth(t ast.TypeExpr) int {
	deepest := 0
	for _, m := range typeMembers(t) {
		if !isArrayTypeExpr(m) {
			continue
		}
		d := 1
		for _, e := range heldTypes(m) {
			d = max(d, 1+arrayDepth(e))
		}
		deepest = max(deepest, d)
	}
	return deepest
}
