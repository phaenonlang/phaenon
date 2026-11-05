package migrate

// The list-types codemod (design/tooling.md §8.2), opt-in: a declaration
// written as a bare `array` becomes `list<T>`, `map<K, V>` or `array<V>`
// when every value it can hold is known to be one. The files are checked
// and the values read from what the checker recorded: a private
// property's default and writes, a private or final method's returns, a
// private method's arguments. It runs in rounds, since a method returning
// a property is known once the property is, and checks each round's
// rewrite: a file in which a finding appears gets its arrays back. Types
// may narrow, which is the point, so only findings are compared.

import (
	"fmt"
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

const arrayTypeRounds = 5

// ArrayTypes runs the codemod over files; load builds the program model as
// for Pipes. It returns each file's source, rewritten or as it was.
func ArrayTypes(files []PipeFile, load func(srcs map[string][]byte, checked map[string]bool) (*program.Program, error)) ([][]byte, *ArrayTypeReport, error) {
	rep := &ArrayTypeReport{Left: map[string]int{}, Notes: map[string][]Note{}}
	out := make([][]byte, len(files))
	srcs := map[string][]byte{}
	watch := map[string]bool{}
	for i, f := range files {
		out[i] = f.Src
		srcs[f.Path] = f.Src
		if tree, err := parser.Parse(f.Src, lexer.ModePhaenon); err == nil && declaresBareArray(tree) {
			watch[f.Path] = true
		}
	}
	if len(watch) == 0 || load == nil {
		return out, rep, nil
	}
	var base *checked // the findings before any round, which no round may add to
	left := map[string]map[string]string{}
	deny := map[string]map[string]bool{} // per file, candidates a check refused
	for round := 0; round < arrayTypeRounds; round++ {
		byRef := map[*program.File][]byRefArg{}
		callables := map[ast.Expr]types.Type{}
		cur, err := checkRecorded(srcs, watch, load, func(o *checker.Options) {
			o.ByRef = func(f *program.File, e ast.Expr, out types.Type) { byRef[f] = append(byRef[f], byRefArg{e, out}) }
			o.CallableArg = func(f *program.File, e ast.Expr, param types.Type) {
				switch e.(type) {
				case *ast.ArrayLit, *ast.MethodCall, *ast.StaticCall:
					callables[e] = param
				}
			}
		})
		if err != nil {
			return nil, nil, err
		}
		if base == nil {
			base = cur
		}
		rewritten := map[string][]byte{}
		written := map[string]int{}
		for path := range watch {
			pf := cur.prog.File(path)
			if pf == nil || pf.AST == nil {
				continue
			}
			a := &arrayTyper{prog: cur.prog, file: pf, types: cur.types[path], byRef: byRef[pf], callables: callables, left: map[string]string{}, deny: deny[path]}
			src, n := a.run()
			left[path] = a.left
			if n == 0 {
				continue
			}
			if _, err := parser.Parse(src, lexer.ModePhaenon); err != nil {
				rep.Left["the rewrite does not parse"] += n
				rep.Undone = append(rep.Undone, Note{Msg: fmt.Sprintf("%d array types undone: the rewrite does not parse (%v)", n, err)})
				rep.UndoneIn = append(rep.UndoneIn, path)
				delete(watch, path)
				continue
			}
			rewritten[path] = src
			written[path] = n
		}
		if len(rewritten) == 0 {
			break
		}
		next := map[string][]byte{}
		for k, v := range srcs {
			next[k] = v
		}
		for k, v := range rewritten {
			next[k] = v
		}
		after, err := checkRecorded(next, watch, load)
		if err != nil {
			return nil, nil, err
		}
		retry := false
		for path, src := range rewritten {
			found := newFindings(path, base, after)
			if len(found) == 0 {
				srcs[path] = src
				rep.Written += written[path]
				continue
			}
			// A finding on a declaration's own line is that declaration's:
			// the next round leaves it out and keeps the others. A finding
			// elsewhere gives the file its arrays back.
			tree, err := parser.Parse(src, lexer.ModePhaenon)
			if err != nil {
				delete(watch, path)
				continue
			}
			lines := lineStarts(src)
			blamed := false
			for _, c := range bareArrayDeclsAll(tree) {
				for _, d := range found {
					if lineAt(lines, c.at) == d.Line {
						if deny[path] == nil {
							deny[path] = map[string]bool{}
						}
						deny[path][c.key] = true
						blamed = true
					}
				}
			}
			if blamed {
				retry = true
				continue
			}
			rep.Left["the check"] += written[path]
			rep.Undone = append(rep.Undone, Note{Msg: fmt.Sprintf("%d array types undone: line %d: %s %s", written[path], found[0].Line, found[0].Code, found[0].Message)})
			rep.UndoneIn = append(rep.UndoneIn, path)
			delete(watch, path) // its candidates stay as they are
		}
		if retry && round == arrayTypeRounds-1 {
			round-- // a refused declaration costs a round of its own
		}
	}
	for path, reasons := range left {
		if !watch[path] {
			continue
		}
		pf, err := parser.Parse(srcs[path], lexer.ModePhaenon)
		if err != nil {
			continue
		}
		lines := lineStarts(srcs[path])
		for _, c := range bareArrayDecls(pf) {
			why, ok := reasons[c.key]
			if !ok {
				continue
			}
			rep.Left[why]++
			rep.Notes[path] = append(rep.Notes[path], Note{Line: lineAt(lines, c.at), Msg: fmt.Sprintf("left %s as array: %s", c.what, why)})
		}
	}
	for path := range rep.Notes {
		sort.Slice(rep.Notes[path], func(i, j int) bool { return rep.Notes[path][i].Line < rep.Notes[path][j].Line })
	}
	for i, f := range files {
		out[i] = srcs[f.Path]
	}
	return out, rep, nil
}

// newFindings lists the findings a rewrite added to a file: findings are
// compared by code and line, since a message may name the narrower type.
func newFindings(path string, before, after *checked) []checker.Diagnostic {
	type key struct {
		code string
		line int
	}
	was := map[key]int{}
	for _, d := range before.diags {
		if d.File == path {
			was[key{d.Code, d.Line}]++
		}
	}
	var out []checker.Diagnostic
	for _, d := range after.diags {
		if d.File != path {
			continue
		}
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
	typ       *ast.NamedType // the `array` to replace
	nullable  bool
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
					out = append(out, candidate{key: "prop " + cl.Name.Name + "::" + m.Props[0].Var.Name, what: "property $" + m.Props[0].Var.Name, at: nt.S.Start, typ: nt, nullable: nullable})
				}
			case *ast.MethodDecl:
				if nt, nullable, ok := bare(m.ReturnType); ok {
					out = append(out, candidate{key: "return " + cl.Name.Name + "::" + m.Name.Name, what: m.Name.Name + "()'s return", at: nt.S.Start, typ: nt, nullable: nullable})
				}
				for _, p := range m.Params {
					if p.Modifiers != 0 {
						continue // promoted: the constructor's callers write it
					}
					if nt, nullable, ok := bare(p.Type); ok {
						out = append(out, candidate{key: "param " + cl.Name.Name + "::" + m.Name.Name + "$" + p.Var.Name, what: "parameter $" + p.Var.Name + " of " + m.Name.Name + "()", at: nt.S.Start, typ: nt, nullable: nullable})
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
	// stored holds, for a callable reference assigned to a typed property
	// (`$this->post = $post ?? self::send(...)`), the property's type.
	stored map[ast.Expr]types.Type
	left   map[string]string // candidate key → why it was left
	deny   map[string]bool   // candidates a check refused
	edits  []edit
}

func (a *arrayTyper) run() ([]byte, int) {
	ns, imports := fileNames(a.file.AST)
	cands := bareArrayDecls(a.file.AST)
	byKey := map[string]candidate{}
	for _, c := range cands {
		byKey[c.key] = c
	}
	written := 0
	ast.Inspect(a.file.AST, func(n ast.Node) bool {
		cl, ok := n.(*ast.ClassDecl)
		if !ok || cl.Name == nil {
			return true
		}
		pcl := a.programClass(cl)
		a.stored = storedCallables(cl, pcl)
		write := func(key string, values []types.Type, keys arrayShape) {
			c, ok := byKey[key]
			if !ok {
				return
			}
			if a.deny[key] {
				a.left[key] = "the check"
				return
			}
			spelled, why := spellArray(values, keys, c.nullable, ns, imports)
			if why != "" {
				a.left[key] = why
				return
			}
			a.edits = append(a.edits, edit{c.typ.S, spelled})
			written++
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
				values, shape, why := a.propertyValues(cl, m)
				if why != "" {
					a.left[key] = why
					continue
				}
				write(key, values, shape)
			case *ast.MethodDecl:
				priv := m.Modifiers&ast.ModPrivate != 0
				final := priv || m.Modifiers&ast.ModFinal != 0 || cl.Modifiers&ast.ModFinal != 0
				override := pcl != nil && a.overrides(pcl, m.Name.Name)
				rkey := "return " + cl.Name.Name + "::" + m.Name.Name
				if _, ok := byKey[rkey]; ok {
					switch {
					case !final:
						a.left[rkey] = "the method may be overridden"
					case override:
						a.left[rkey] = "it overrides a method"
					case docReturns(m.Doc):
						a.left[rkey] = "a docblock types it"
					default:
						if values, why := a.returnValues(m); why != "" {
							a.left[rkey] = why
						} else {
							write(rkey, values, arrayShape{})
						}
					}
				}
				for i, p := range m.Params {
					pkey := "param " + cl.Name.Name + "::" + m.Name.Name + "$" + p.Var.Name
					if _, ok := byKey[pkey]; !ok {
						continue
					}
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
							write(pkey, values, arrayShape{})
						}
					}
				}
			}
		}
		return true
	})
	if written == 0 {
		return a.file.Src, 0
	}
	l := &lifter{src: a.file.Src, edits: a.edits}
	return l.apply(), written
}

func (a *arrayTyper) programClass(cl *ast.ClassDecl) *program.Class {
	for _, c := range a.prog.Classes() {
		if c.Decl == cl {
			return c
		}
	}
	return nil
}

// overrides reports a method an ancestor declares too, whose type it
// takes (collections.md §2).
func (a *arrayTyper) overrides(cl *program.Class, name string) bool {
	for _, anc := range cl.Ancestors() {
		if m := anc.FindMethod(name); m != nil && m.Vis != program.Private {
			return true
		}
	}
	return false
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
func (a *arrayTyper) propertyValues(cl *ast.ClassDecl, p *ast.PropertyDecl) ([]types.Type, arrayShape, string) {
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
	for _, r := range a.byRef {
		if isSelf(r.e) {
			// What the callee writes back, sort()'s list<V>: a value of
			// its own, unless its elements are unknown, which happens when
			// V comes from this property while it is still untyped; then
			// it says only whether a list stays a list.
			if arr, ok := r.out.(types.Array); ok && types.IsMixed(arr.Value) {
				if !arr.List {
					shape.keyed = true
				}
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
		for _, s := range md.Body {
			ast.Inspect(s, func(n ast.Node) bool {
				if why != "" {
					return false
				}
				switch n := n.(type) {
				case *ast.ClassDecl:
					return false // another class's $this
				case *ast.PropertyFetch:
					if id, ok := n.Name.(*ast.Ident); ok && id.Name == name && !static {
						if v, ok := n.Object.(*ast.Variable); !ok || v.Name != "this" {
							why = "another object's property of that name is used"
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
			})
		}
	}
	if why != "" {
		return nil, shape, why
	}
	return values, shape, ""
}

// returnValues collects what a method returns.
func (a *arrayTyper) returnValues(m *ast.MethodDecl) ([]types.Type, string) {
	var values []types.Type
	why := ""
	for _, s := range m.Body {
		ast.Inspect(s, func(n ast.Node) bool {
			if why != "" {
				return false
			}
			switch n := n.(type) {
			case *ast.Closure, *ast.ArrowFunc, *ast.ClassDecl, *ast.FunctionDecl:
				return false // their returns are theirs
			case *ast.Yield, *ast.YieldFrom:
				why = "it is a generator"
			case *ast.Return:
				if n.Expr == nil {
					why = "a return has no value"
					return false
				}
				t, ok := a.types[n.Expr.Span()]
				if !ok {
					why = "a returned value is not typed"
					return false
				}
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

// argValues collects the arguments a private method's parameter receives
// from its class: at each call, and where the method is passed as a
// callable (`array_map([$this, 'm'], $xs)`, `$this->m(...)`), from the
// type of the callable the parameter it is passed to expects.
func (a *arrayTyper) argValues(cl *ast.ClassDecl, m *ast.MethodDecl, idx int, p *ast.Param) ([]types.Type, string) {
	if p.Variadic || p.ByRef {
		return nil, "a variadic or by-reference parameter"
	}
	name := m.Name.Name
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
		if idx < len(c.Params) {
			values = append(values, c.Params[idx].Type)
			return
		}
		fromDefault()
	}
	arg := func(args *ast.ArgList) {
		calls++
		var found ast.Expr
		for i, ar := range args.Args {
			if ar.Unpack {
				why = "a call spreads its arguments"
				return
			}
			if ar.Name != nil && ar.Name.Name == p.Var.Name || ar.Name == nil && i == idx {
				found = ar.Value
			}
		}
		if found == nil {
			fromDefault()
			return
		}
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
	// The strings that name it inside a callable array are accounted for
	// there; another string reaches it only where PHP takes a callable's
	// name, which callableStrings lists. A key or a git argument that
	// happens to spell it does not.
	named := map[*ast.StringLit]bool{}
	reaching := callableStrings(cl)
	for _, mem := range cl.Members {
		ast.Inspect(mem, func(n ast.Node) bool {
			if why != "" {
				return false
			}
			switch n := n.(type) {
			case *ast.ClassDecl:
				return false
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
				if id, ok := n.Name.(*ast.Ident); ok && strings.EqualFold(id.Name, name) {
					if n.Args != nil && n.Args.FirstClassCallable {
						asCallable(n)
					} else if n.Args != nil {
						arg(n.Args)
					}
				} else if !ok {
					why = "a dynamic method call may reach it"
				}
			case *ast.StaticCall:
				if id, ok := n.Name.(*ast.Ident); ok && strings.EqualFold(id.Name, name) {
					if n.Args != nil && n.Args.FirstClassCallable {
						asCallable(n)
					} else if n.Args != nil {
						arg(n.Args)
					}
				} else if !ok {
					why = "a dynamic method call may reach it"
				}
			case *ast.StringLit:
				v := strings.Trim(n.Raw, `'"`)
				if !named[n] && reaching[n] && (strings.EqualFold(v, name) || strings.HasSuffix(strings.ToLower(v), "::"+strings.ToLower(name))) {
					why = "its name is written as a string"
				}
			}
			return true
		})
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

// callableStrings lists the strings of a class that PHP may take as a
// method's name: an item of a two-item array (a callable array, of this
// class or another), a `'Class::method'` string, and an argument of a
// function that calls or looks up a callable by name.
func callableStrings(cl *ast.ClassDecl) map[*ast.StringLit]bool {
	out := map[*ast.StringLit]bool{}
	byName := map[string]bool{"call_user_func": true, "call_user_func_array": true, "forward_static_call": true,
		"forward_static_call_array": true, "is_callable": true, "method_exists": true, "register_shutdown_function": true,
		"fromcallable": true, "array_map": true, "array_filter": true, "usort": true, "uasort": true, "uksort": true,
		"array_walk": true, "array_reduce": true, "iterator_apply": true}
	ast.Inspect(cl, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ArrayLit:
			if len(n.Items) == 2 {
				for _, it := range n.Items {
					if it != nil && it.Key == nil {
						if lit, ok := it.Value.(*ast.StringLit); ok {
							out[lit] = true
						}
					}
				}
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
func spellArray(values []types.Type, shape arrayShape, nullable bool, ns string, imports map[string]string) (string, string) {
	return spellArrayAt(values, shape, nullable, false, ns, imports)
}

// spellArrayAt is spellArray for a declaration (nested false) or for an
// element type: a declaration's same-typed positions read as a list, an
// element's computed ones as a tuple, as the dogfood writes list<(string,
// string)> but list<Binary> for five values a method returns.
func spellArrayAt(values []types.Type, shape arrayShape, nullable, nested bool, ns string, imports map[string]string) (string, string) {
	sp := &speller{ns: ns, imports: imports}
	list := !shape.keyed
	keys := append([]types.Type{}, shape.keys...)
	elems := append([]types.Type{}, shape.elems...)
	any := len(elems) > 0
	var tuples [][]types.Type // the positions of each value, while every value is a tuple
	tupleLen, tuplesOnly := -1, len(elems) == 0
	literals := false // a position holds a literal: ['a', 'b'] is a list, [$dir, $plugin] a pair
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
				if isLiteralType(e.Type) {
					literals = true
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
			if p != parts[0] {
				differ = true
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
}

func (s *speller) union(ts []types.Type) (string, bool) {
	seen := map[string]bool{}
	var parts []string
	nullable := false
	var arrays []types.Type // spelled together, as one list, map or tuple
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
	if len(arrays) > 0 {
		one, why := spellArrayAt(arrays, arrayShape{}, false, true, s.ns, s.imports)
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
			return "", false // an anonymous class has no name to write
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
		if c, ok := t.Of.(types.Class); ok && len(c.Args) == 0 {
			return "class<" + s.class(c.Name) + ">", true
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
