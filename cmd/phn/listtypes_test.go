package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/migrate"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// TestListTypesMeasure measures phn migrate --list-types on trees whose
// array types are written, the dogfood trees (design/tooling.md §8.2):
// the types of the declarations the codemod considers are stripped to
// `array` in memory, the codemod runs as phn migrate --list-types runs it
// (then the arrays left get their `<mixed>`), and each declaration's type is
// compared with the one written, in the program model, so that an
// override's bare array counts as the type it takes from its parent. The
// stripped project migrated without the codemod and the one it typed are
// checked whole: the codemod may add no finding. PHAENON_LISTTYPES lists the trees, each the
// directory of a phaenon.json, separated as PATH is; PHAENON_LISTTYPES_MIN
// is the share, in percent, of declarations that must come back as written
// or narrower, 0 by default; PHAENON_LISTTYPES_FINDINGS, when set, lists
// the findings left with the codemod's types. A type wider than written
// fails the test, unless it is only shallower: the depth pass's mixed
// below the levels the code uses, with keys, scalars and objects as
// written.
func TestListTypesMeasure(t *testing.T) {
	trees := os.Getenv("PHAENON_LISTTYPES")
	if trees == "" {
		t.Skip("PHAENON_LISTTYPES names the trees to measure on")
	}
	min := 0
	if s := os.Getenv("PHAENON_LISTTYPES_MIN"); s != "" {
		if _, err := fmt.Sscan(s, &min); err != nil {
			t.Fatalf("PHAENON_LISTTYPES_MIN: %v", err)
		}
	}
	for _, tree := range filepath.SplitList(trees) {
		t.Run(filepath.Base(tree), func(t *testing.T) { measureListTypes(t, tree, min) })
	}
}

// strippedAt is a stripped declaration and its file.
type strippedAt struct {
	path string
	migrate.StrippedType
}

func measureListTypes(t *testing.T, tree string, min int) {
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

	written, stripped := map[string][]byte{}, map[string][]byte{}
	var recs []strippedAt
	var in []migrate.PipeFile
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		written[path] = src
		out, rs, err := migrate.StripListTypes(src)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		stripped[path] = out
		// Every file goes in, as when a project is migrated whole: the
		// codemod checks the callers of what it types.
		in = append(in, migrate.PipeFile{Path: path, Src: out})
		for _, r := range rs {
			recs = append(recs, strippedAt{path, r})
		}
	}
	typedSrcs, rep, err := migrate.ArrayTypes(in, load)
	if err != nil {
		t.Fatal(err)
	}
	// Then what migrate writes for the arrays left: `<mixed>`, which
	// leaves their types as they were.
	for i := range in {
		in[i].Src = typedSrcs[i]
	}
	if typedSrcs, _, err = migrate.Arrays(in, load); err != nil {
		t.Fatal(err)
	}
	typed := map[string][]byte{}
	for i, f := range in {
		typed[f.Path] = typedSrcs[i]
	}
	// Why a declaration was left: its note, or its file's when the check
	// undid a round there.
	notes := map[string][]string{}
	for path, ns := range rep.Notes {
		for _, n := range ns {
			k := fmt.Sprintf("%s:%d", path, n.Line)
			notes[k] = append(notes[k], n.Msg)
		}
	}
	undone := map[string]string{}
	for i, n := range rep.Undone {
		undone[rep.UndoneIn[i]] = n.Msg
	}
	why := func(r strippedAt) string {
		prefix := "left " + r.What() + " as array: "
		for _, msg := range notes[fmt.Sprintf("%s:%d", r.path, r.Line)] {
			if strings.HasPrefix(msg, prefix) {
				return strings.TrimPrefix(msg, prefix)
			}
		}
		if msg, ok := undone[r.path]; ok {
			return msg
		}
		return "no note"
	}

	// What each declaration's type is, as written and as the codemod gave
	// it: the program model's, an override's taken from its parent.
	before, err := load(written, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := load(typed, nil)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	byKind := map[string][]string{}
	for _, r := range recs {
		was, now := declaredType(before, r.StrippedType), declaredType(after, r.StrippedType)
		rel := filepath.ToSlash(strings.TrimPrefix(r.path, tree+string(filepath.Separator)))
		at := fmt.Sprintf("%s:%d %s", rel, r.Line, r.What())
		var kind string
		switch {
		case was == nil || now == nil:
			kind = "not found"
		case types.Print(was) == types.Print(now):
			kind = "as written"
		case types.Print(now) == types.Print(types.TArray):
			kind = "not recovered"
			at += ": " + why(r)
		case types.IsSubtype(now, was, after):
			kind = "narrower"
			at += fmt.Sprintf(": %s, written %s", r.Type, types.Print(now))
		case types.IsSubtype(was, now, after) && mixedBelow(now, was):
			kind = "shallower"
			at += fmt.Sprintf(": %s, written %s", r.Type, types.Print(now))
		case types.IsSubtype(was, now, after):
			kind = "wider"
			at += fmt.Sprintf(": %s, written %s", r.Type, types.Print(now))
		default:
			kind = "other"
			at += fmt.Sprintf(": %s, written %s", r.Type, types.Print(now))
		}
		counts[kind]++
		byKind[kind] = append(byKind[kind], at)
	}
	n := len(recs)
	pct := func(k string) int {
		if n == 0 {
			return 0
		}
		return 100 * counts[k] / n
	}
	t.Logf("%d declarations stripped: %d as written (%d%%), %d narrower (%d%%), %d shallower (%d%%), %d wider (%d%%), %d other, %d not recovered (%d%%), %d not found",
		n, counts["as written"], pct("as written"), counts["narrower"], pct("narrower"), counts["shallower"], pct("shallower"), counts["wider"], pct("wider"),
		counts["other"], counts["not recovered"], pct("not recovered"), counts["not found"])
	for _, k := range []string{"not recovered", "other", "wider", "shallower", "not found"} {
		sort.Strings(byKind[k])
		for _, at := range byKind[k] {
			t.Logf("  %s: %s", k, at)
		}
	}
	if counts["wider"] > 0 {
		t.Errorf("%d declarations come back wider than written: a value the codemod missed", counts["wider"])
	}
	if got := pct("as written") + pct("narrower") + pct("shallower"); got < min {
		t.Errorf("%d%% of the declarations come back as written or narrower, PHAENON_LISTTYPES_MIN is %d%%", got, min)
	}

	// The codemod may add no finding: the stripped project and the one it
	// typed, checked whole.
	all := map[string]bool{}
	for _, path := range files {
		all[path] = true
	}
	messages := map[string]string{}
	findings := func(srcs map[string][]byte) map[string]int {
		p, err := load(srcs, all)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]int{}
		for _, d := range checker.New(p, proj.checkerOptions(checker.Options{Deprecations: true})).Check() {
			k := fmt.Sprintf("%s:%d: %s", d.File, d.Line, d.Code)
			m[k]++
			messages[k] = d.Message
		}
		return m
	}
	// The baseline is the stripped project migrated without the codemod:
	// the residue alone.
	var plain []migrate.PipeFile
	for _, path := range files {
		plain = append(plain, migrate.PipeFile{Path: path, Src: stripped[path]})
	}
	plainSrcs, _, err := migrate.Arrays(plain, load)
	if err != nil {
		t.Fatal(err)
	}
	baseline := map[string][]byte{}
	for i, f := range plain {
		baseline[f.Path] = plainSrcs[i]
	}
	base, now := findings(baseline), findings(typed)
	var added []string
	for k, c := range now {
		if c > base[k] {
			added = append(added, k)
		}
	}
	sort.Strings(added)
	total := func(m map[string]int) int {
		s := 0
		for _, c := range m {
			s += c
		}
		return s
	}
	byCode := map[string]int{}
	for k, c := range now {
		byCode[k[strings.LastIndex(k, " ")+1:]] += c
	}
	var codes []string
	for c, n := range byCode {
		codes = append(codes, fmt.Sprintf("%s %d", c, n))
	}
	sort.Strings(codes)
	t.Logf("phn check: %d findings without the codemod, %d with it (%s)", total(base), total(now), strings.Join(codes, ", "))
	if os.Getenv("PHAENON_LISTTYPES_FINDINGS") != "" {
		var keys []string
		for k := range now {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Logf("  finding: %s %s", strings.TrimPrefix(k, tree+string(filepath.Separator)), messages[k])
		}
	}
	for _, k := range added {
		t.Errorf("a finding only with the codemod's types: %s %s", k, messages[k])
	}
}

// declaredType is a stripped declaration's type in a program model.
func declaredType(p *program.Program, r migrate.StrippedType) types.Type {
	cl := p.Class(r.Class)
	if cl == nil {
		return nil
	}
	switch r.Kind {
	case "property":
		if pr := cl.FindProperty(r.Member); pr != nil {
			return pr.Type
		}
	case "return":
		if m := cl.FindMethod(r.Member); m != nil {
			return m.Return
		}
	case "parameter":
		if m := cl.FindMethod(r.Member); m != nil {
			for _, prm := range m.Params {
				if prm.Name == r.Param {
					return prm.Type
				}
			}
		}
	}
	return nil
}

// mixedBelow reports now as was with, below some level, mixed where was
// holds arrays: the depth pass's cut, not a value the codemod missed.
// Keys, scalars and objects must be as written.
func mixedBelow(now, was types.Type) bool {
	if types.Print(now) == types.Print(was) {
		return true
	}
	if types.IsMixed(now) {
		for _, m := range types.Members(was) {
			switch m.(type) {
			case types.Array, types.Shape:
				return true
			}
		}
		return false
	}
	nows, wass := types.Members(now), types.Members(was)
	if len(nows) > 1 || len(wass) > 1 {
		if len(nows) != len(wass) {
			return false
		}
		used := make([]bool, len(wass))
		for _, n := range nows {
			found := false
			for i, w := range wass {
				if !used[i] && mixedBelow(n, w) {
					used[i], found = true, true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	switch n := now.(type) {
	case types.Array:
		w, ok := was.(types.Array)
		return ok && n.List == w.List && types.Print(n.Key) == types.Print(w.Key) && mixedBelow(n.Value, w.Value)
	case types.Shape:
		w, ok := was.(types.Shape)
		if !ok || n.List != w.List || n.Unsealed != w.Unsealed || len(n.Entries) != len(w.Entries) {
			return false
		}
		for i, e := range n.Entries {
			f := w.Entries[i]
			if e.Key != f.Key || e.IntKey != f.IntKey || e.IsInt != f.IsInt || e.Optional != f.Optional || !mixedBelow(e.Type, f.Type) {
				return false
			}
		}
		return true
	}
	return false
}
