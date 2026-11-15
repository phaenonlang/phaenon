package lsp

import (
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// probe is the name completion and signature help write where the cursor
// is, so that the text being typed parses and the checker types what comes
// before it: `$this->repo->` becomes `$this->repo->__phnProbe`.
const probe = "__phnProbe"

// maxItems bounds a completion list; a longer one is marked incomplete,
// and the editor asks again as the prefix grows.
const maxItems = 200

// completionContext is what the cursor follows.
type completionContext int

const (
	completeName     completionContext = iota // a class, a function
	completeMember                            // after -> or ?->
	completeStatic                            // after ::
	completeVariable                          // after $
	completePipe                              // after |>
)

func (s *Server) completion(p TextDocumentPositionParams) (any, error) {
	path, ok := s.path(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	s.mu.Lock()
	d := s.docs[path]
	snippets := s.snippets
	var src []byte
	if d != nil {
		src = d.text
	}
	s.mu.Unlock()
	if d == nil {
		return nil, nil
	}
	t := newText(src)
	off := t.offset(p.Position)
	start := off
	for start > 0 && isIdentByte(src[start-1]) {
		start--
	}
	prefix := string(src[start:off])
	ctx := contextOf(src, start)
	replace := t.rangeOf(start, off)

	var probeText string
	switch ctx {
	case completePipe:
		probeText = probe + "(...)"
	default:
		probeText = probe
	}
	// The probe takes the whole word around the cursor, whose rest would
	// otherwise run into it.
	end := off
	for end < len(src) && isIdentByte(src[end]) {
		end++
	}
	typed := ctx != completeVariable && ctx != completeName
	r, inserted := s.probeCheck(path, src, start, end, probeText, typed)
	if r == nil && !typed {
		// The text does not parse elsewhere either: the names and
		// variables of its newest version that did, at the same place.
		if good := s.lastParsed(path); good != nil {
			r, inserted = good, min(start, len(good.text.src))
		}
	}
	list := &CompletionList{Items: []CompletionItem{}}
	add := func(it CompletionItem) {
		if len(list.Items) >= maxItems {
			list.IsIncomplete = true
			return
		}
		if it.TextEdit == nil && it.InsertText == "" {
			it.TextEdit = &TextEdit{Range: replace, NewText: it.Label}
		} else if it.TextEdit == nil {
			it.TextEdit = &TextEdit{Range: replace, NewText: it.InsertText}
			it.InsertText = ""
		}
		list.Items = append(list.Items, it)
	}
	if r == nil || r.file == nil {
		return list, nil
	}
	path2 := nodePath(r.file.AST, inserted)
	switch ctx {
	case completeMember, completeStatic:
		node := probeNode(path2)
		var classes []*program.Class
		static := ctx == completeStatic
		switch n := node.(type) {
		case *ast.MethodCall:
			classes = r.classesOf(n.Object)
		case *ast.PropertyFetch:
			classes = r.classesOf(n.Object)
		case *ast.StaticCall:
			if c := r.classOfExpr(n.Class, path2, inserted); c != nil {
				classes = []*program.Class{c}
			}
		case *ast.ClassConstFetch:
			if c := r.classOfExpr(n.Class, path2, inserted); c != nil {
				classes = []*program.Class{c}
			}
		}
		inside := r.enclosingClass(path2, inserted)
		for _, it := range memberItems(classes, static, inside, snippets) {
			if matches(it.Label, prefix) {
				add(it)
			}
		}
	case completeVariable:
		for _, name := range variablesAt(path2, inserted) {
			if matches(name, prefix) {
				add(CompletionItem{Label: name, Kind: completionVariable, TextEdit: &TextEdit{Range: replace, NewText: name}})
			}
		}
	case completePipe:
		left := pipeLeft(path2)
		if left == nil {
			return list, nil
		}
		lt := r.types[left]
		if lt == nil {
			return list, nil
		}
		for _, it := range pipeItems(r.prog, lt, prefix, snippets) {
			add(it)
		}
	case completeName:
		if prefix == "" {
			return list, nil
		}
		scope := r.file.ScopeAt(inserted)
		for _, fn := range r.prog.Functions() {
			fn = fn.Signature()
			short := fn.Name[strings.LastIndexByte(fn.Name, '\\')+1:]
			ns := fn.Name[:len(fn.Name)-len(short)]
			if !matches(short, prefix) || (ns != "" && ns != scope.Prefix()) || strings.HasPrefix(short, "__phn_") {
				continue
			}
			add(CompletionItem{Label: short, Kind: completionFunction, SortText: "1", Detail: signature("function", fn.Name, fn.TypeParams, fn.Params, fn.Return),
				InsertText: callText(short, len(fn.Params) > 0, snippets), InsertTextFormat: textFormat(snippets)})
		}
		for _, it := range s.classItems(r, scope, prefix, inserted) {
			add(it)
		}
	}
	sort.SliceStable(list.Items, func(i, j int) bool {
		return list.Items[i].SortText+list.Items[i].Label < list.Items[j].SortText+list.Items[j].Label
	})
	return list, nil
}

// contextOf tells what the identifier starting at start follows.
func contextOf(src []byte, start int) completionContext {
	b := src[:start]
	switch {
	case strings.HasSuffix(string(b), "->"):
		return completeMember
	case strings.HasSuffix(string(b), "::"):
		return completeStatic
	case strings.HasSuffix(string(b), "$"):
		return completeVariable
	}
	trimmed := strings.TrimRight(string(b), " \t\r\n")
	if strings.HasSuffix(trimmed, "|>") {
		return completePipe
	}
	return completeName
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

func matches(name, prefix string) bool {
	return strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix))
}

func textFormat(snippets bool) int {
	if snippets {
		return 2
	}
	return 1
}

// callText is what a call's completion inserts.
func callText(name string, params, snippets bool) string {
	switch {
	case !params:
		return name + "()"
	case snippets:
		return name + "($0)"
	default:
		return name + "("
	}
}

// probeCheck checks the text with the probe in place of what the cursor
// has typed (start to off), closed as far as needed for it to parse: the
// statement around the cursor is usually unfinished. inserted is the
// probe's offset in the checked text. With typed false only the parse is
// needed (variables, names), and the newest parse of the document serves.
func (s *Server) probeCheck(path string, src []byte, start, off int, probeText string, typed bool) (r *fileCheck, inserted int) {
	head, tail := string(src[:start]), string(src[off:])
	for _, closing := range []string{"", ";", ")", ");", "))", "));", ")]", "];", "}", ";}"} {
		cand := head + probeText + closing + tail
		if _, err := parser.Parse([]byte(cand), lexer.ModePhaenon); err != nil {
			continue
		}
		if !typed {
			// Variables and names need the parse and the namespace scopes,
			// which adding the file to an empty program computes, and the
			// declarations of the document's latest check.
			pf, err := program.New().AddFile(path, []byte(cand), lexer.ModePhaenon, false)
			if err != nil {
				return nil, 0
			}
			prog := program.New()
			if cur, err := s.current(path); err == nil && cur != nil && cur.prog != nil {
				prog = cur.prog
			}
			return &fileCheck{text: newText([]byte(cand)), file: pf, prog: prog, types: map[ast.Expr]types.Type{}}, start
		}
		r, err := s.checkText(path, []byte(cand), -1)
		if err != nil || r.file == nil {
			return nil, 0
		}
		return r, start
	}
	return nil, 0
}

// probeNode is the member access, call or pipe stage the probe names.
func probeNode(path []ast.Node) ast.Node {
	for i := len(path) - 1; i >= 0; i-- {
		switch n := path[i].(type) {
		case *ast.MethodCall:
			if isProbe(n.Name) {
				return n
			}
		case *ast.PropertyFetch:
			if isProbe(n.Name) {
				return n
			}
		case *ast.StaticCall:
			if isProbe(n.Name) {
				return n
			}
		case *ast.ClassConstFetch:
			if isProbe(n.Name) {
				return n
			}
		}
	}
	return nil
}

func isProbe(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == probe
}

// pipeLeft is the left side of the pipe whose stage is the probe's call.
func pipeLeft(path []ast.Node) ast.Expr {
	for i := len(path) - 1; i >= 0; i-- {
		b, ok := path[i].(*ast.Binary)
		if !ok || b.Op != token.T_PIPE {
			continue
		}
		if c, ok := b.Right.(*ast.Call); ok {
			if n, ok := c.Callee.(*ast.Name); ok && len(n.Parts) == 1 && n.Parts[0] == probe {
				return b.Left
			}
		}
	}
	return nil
}

// memberItems lists the members of the classes the access can reach: the
// instance members after ->, the static ones and the constants after ::;
// private and protected ones only from inside a class that sees them.
func memberItems(classes []*program.Class, static bool, inside *program.Class, snippets bool) []CompletionItem {
	var out []CompletionItem
	seen := map[string]bool{}
	visible := func(owner *program.Class, vis program.Visibility) bool {
		switch vis {
		case program.Public:
			return true
		case program.Private:
			return inside != nil && owner != nil && strings.EqualFold(inside.Name, owner.Name)
		default:
			return inside != nil && owner != nil && (inside.IsSubclassOf(owner.Name) || owner.IsSubclassOf(inside.Name))
		}
	}
	for _, c := range classes {
		c = c.Signature()
		chain := append([]*program.Class{c}, c.Ancestors()...)
		for i, a := range chain {
			chain[i] = a.Signature()
		}
		var methods []*program.Method
		for _, m := range c.MethodTable() {
			methods = append(methods, m)
		}
		sort.Slice(methods, func(i, j int) bool { return methods[i].Name < methods[j].Name })
		for _, m := range methods {
			if m.Static != static || !visible(m.Class, m.Vis) || strings.HasPrefix(m.Name, "__") {
				continue
			}
			key := "m:" + strings.ToLower(m.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, CompletionItem{Label: m.Name, Kind: completionMethod,
				Detail:     signature(methodHead(m), m.Name, m.TypeParams, m.Params, m.Return),
				InsertText: callText(m.Name, len(m.Params) > 0, snippets), InsertTextFormat: textFormat(snippets),
				Documentation: summary(m.Doc)})
		}
		for _, owner := range chain {
			for _, pr := range owner.Props {
				if pr.Static != static || !visible(owner, pr.Vis) {
					continue
				}
				label := pr.Name
				if static {
					label = "$" + pr.Name
				}
				key := "p:" + pr.Name
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, CompletionItem{Label: label, Kind: completionField, Detail: types.PrintPhaenon(pr.Type), Documentation: summary(pr.Doc)})
			}
			if !static {
				continue
			}
			for _, k := range owner.Consts {
				if !visible(owner, k.Vis) || seen["k:"+k.Name] {
					continue
				}
				seen["k:"+k.Name] = true
				out = append(out, CompletionItem{Label: k.Name, Kind: completionConstant})
			}
			for _, k := range owner.Cases {
				if seen["k:"+k.Name] {
					continue
				}
				seen["k:"+k.Name] = true
				out = append(out, CompletionItem{Label: k.Name, Kind: completionEnumCase})
			}
		}
		if static && !seen["k:class"] {
			seen["k:class"] = true
			out = append(out, CompletionItem{Label: "class", Kind: completionKeyword, SortText: "~"})
		}
	}
	return out
}

// variablesAt lists the variables of the function around the position:
// its parameters, $this in a method, and the variables written before the
// position.
func variablesAt(path []ast.Node, off int) []string {
	var params []*ast.Param
	var body ast.Node
	method := false
	for i := len(path) - 1; i >= 0 && body == nil; i-- {
		switch n := path[i].(type) {
		case *ast.MethodDecl:
			params, body, method = n.Params, n, n.Modifiers&ast.ModStatic == 0
		case *ast.FunctionDecl:
			params, body = n.Params, n
		case *ast.Closure:
			params, body = n.Params, n
			method = !n.Static
		case *ast.ArrowFunc:
			params, body = n.Params, n
		}
	}
	seen := map[string]bool{probe: true}
	var out []string
	addName := func(name string) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if method {
		addName("this")
	}
	for _, p := range params {
		if p.Var != nil {
			addName(p.Var.Name)
		}
	}
	if body == nil && len(path) > 0 {
		body = path[0]
	}
	if body != nil {
		ast.Inspect(body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.ClassDecl, *ast.FunctionDecl:
				return n == body
			case *ast.Closure, *ast.ArrowFunc:
				if n != body {
					return false
				}
			case *ast.Variable:
				if n.S.End <= off {
					addName(n.Name)
				}
			}
			return true
		})
	}
	sort.Strings(out)
	return out
}

// pipeItems lists the functions a pipe can call with its left side: those
// whose first parameter takes the left side's type, inserted as a partial
// application `f(?, …)`, or `f(...)` when the left side is the only
// argument they need. A first parameter of mixed takes anything and sorts
// after the others.
func pipeItems(p *program.Program, left types.Type, prefix string, snippets bool) []CompletionItem {
	var out []CompletionItem
	for _, fn := range p.Functions() {
		fn = fn.Signature()
		if len(fn.Params) == 0 || strings.HasPrefix(fn.Name, "__phn_") {
			continue
		}
		short := fn.Name[strings.LastIndexByte(fn.Name, '\\')+1:]
		if !matches(short, prefix) {
			continue
		}
		first := fn.Params[0]
		if first.ByRef || first.Variadic || first.Type == nil {
			continue
		}
		if !types.IsSubtype(left, first.Type, p) {
			continue
		}
		rest := 0
		for _, q := range fn.Params[1:] {
			if !q.HasDefault && !q.Variadic {
				rest++
			}
		}
		insert := short + "(...)"
		switch {
		case rest > 0 && snippets:
			insert = short + "(?, $0)"
		case rest > 0:
			insert = short + "(?, "
		}
		sortKey := "0"
		if types.IsMixed(first.Type) {
			sortKey = "1"
		}
		out = append(out, CompletionItem{Label: short, Kind: completionFunction, SortText: sortKey,
			Detail:     signature("function", fn.Name, fn.TypeParams, fn.Params, fn.Return),
			InsertText: insert, InsertTextFormat: textFormat(snippets)})
	}
	return out
}

// classItems lists the classes whose short name starts with the prefix:
// those the program declares and those Composer's maps can load. A class
// the file does not see by that name is completed with its import.
func (s *Server) classItems(r *fileCheck, scope *program.Scope, prefix string, off int) []CompletionItem {
	var out []CompletionItem
	seen := map[string]bool{}
	names := map[string]ast.ClassKind{}
	for _, c := range r.prog.Classes() {
		if !c.Anonymous {
			names[c.Name] = c.Kind
		}
	}
	for _, n := range s.host.ClassNames() {
		if _, ok := names[n]; !ok {
			names[n] = ast.KindClass
		}
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, fqn := range sorted {
		short := fqn[strings.LastIndexByte(fqn, '\\')+1:]
		if !matches(short, prefix) || seen[strings.ToLower(fqn)] || strings.HasPrefix(short, "__") {
			continue
		}
		seen[strings.ToLower(fqn)] = true
		kind := completionClass
		switch names[fqn] {
		case ast.KindInterface:
			kind = completionInterface
		case ast.KindEnum:
			kind = completionEnum
		}
		it := CompletionItem{Label: short, Kind: kind, Detail: fqn, SortText: "2"}
		if !strings.EqualFold(scope.ResolveClassString(short), fqn) {
			if _, taken := scope.Imports()[strings.ToLower(short)]; taken {
				it.InsertText = `\` + fqn // another class has the name here
			} else if strings.Contains(fqn, `\`) {
				it.AdditionalTextEdits = []TextEdit{importEdit(r, fqn)}
			}
		}
		out = append(out, it)
	}
	return out
}

// importEdit adds `use FQN;` after the file's last import, else after its
// namespace statement, else at its top.
func importEdit(r *fileCheck, fqn string) TextEdit {
	t := r.text
	at, text := 0, "use "+fqn+";\n"
	var last ast.Stmt
	var visit func(stmts []ast.Stmt)
	visit = func(stmts []ast.Stmt) {
		for _, st := range stmts {
			switch n := st.(type) {
			case *ast.Use:
				last = n
			case *ast.Namespace:
				if last == nil || last.Span().End < n.S.Start {
					if !n.Braced {
						last = n
					}
				}
				visit(n.Body)
			}
		}
	}
	visit(r.file.AST.Stmts)
	if last != nil {
		end := last.Span().End
		if ns, ok := last.(*ast.Namespace); ok && ns.Name != nil {
			end = ns.Name.S.End + 1 // after `namespace A;`
			text = "\n" + text
		}
		for end < len(t.src) && t.src[end] != '\n' {
			end++
		}
		at = end + 1
		if at > len(t.src) {
			at = len(t.src)
		}
	}
	pos := t.position(at)
	return TextEdit{Range: Range{Start: pos, End: pos}, NewText: text}
}

// ------------------------------------------------------------------ signature help

func (s *Server) signatureHelp(p TextDocumentPositionParams) (any, error) {
	path, ok := s.path(p.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	s.mu.Lock()
	d := s.docs[path]
	var src []byte
	if d != nil {
		src = d.text
	}
	s.mu.Unlock()
	if d == nil {
		return nil, nil
	}
	off := newText(src).offset(p.Position)
	var r *fileCheck
	for _, ins := range []string{"", probe, probe + ")", probe + ");", ")", ");", probe + "));", "));"} {
		cand := []byte(string(src[:off]) + ins + string(src[off:]))
		if _, err := parser.Parse(cand, lexer.ModePhaenon); err != nil {
			continue
		}
		var err error
		if r, err = s.checkText(path, cand, -1); err != nil {
			return nil, err
		}
		break
	}
	if r == nil || r.file == nil {
		return nil, nil
	}
	path2 := nodePath(r.file.AST, off)
	for i := len(path2) - 1; i >= 0; i-- {
		var args *ast.ArgList
		var head, name string
		var tps []*program.TypeParam
		var params []*program.Param
		var ret types.Type
		var doc string
		switch n := path2[i].(type) {
		case *ast.Call:
			args = n.Args
			if nm, ok := n.Callee.(*ast.Name); ok {
				if fn := r.function(nm, off); fn != nil {
					fn = fn.Signature()
					head, name, tps, params, ret, doc = "function", fn.Name, fn.TypeParams, fn.Params, fn.Return, summary(fn.Doc)
				}
			}
		case *ast.MethodCall:
			args = n.Args
			if id, ok := n.Name.(*ast.Ident); ok {
				for _, c := range r.classesOf(n.Object) {
					if m := c.FindMethod(id.Name); m != nil {
						head, name, tps, params, ret, doc = methodHead(m), m.Name, m.TypeParams, m.Params, m.Return, summary(m.Doc)
						break
					}
				}
			}
		case *ast.StaticCall:
			args = n.Args
			if id, ok := n.Name.(*ast.Ident); ok {
				if c := r.classOfExpr(n.Class, path2, off); c != nil {
					if m := c.FindMethod(id.Name); m != nil {
						head, name, tps, params, ret, doc = methodHead(m), c.Name+"::"+m.Name, m.TypeParams, m.Params, m.Return, summary(m.Doc)
					}
				}
			}
		case *ast.New:
			args = n.Args
			if nm, ok := n.Class.(*ast.Name); ok {
				if c := r.className(nm, path2, off); c != nil {
					if m := c.FindMethod("__construct"); m != nil {
						head, name, params, doc = "new", c.Name, m.Params, summary(c.Doc)
					}
				}
			}
		default:
			continue
		}
		if args == nil || off <= args.S.Start || off >= args.S.End || name == "" {
			if args != nil && off > args.S.Start && off < args.S.End {
				return nil, nil // the innermost call, whose callee is unknown
			}
			continue
		}
		label, offs := signatureLabel(head, name, tps, params, ret)
		if head == "new" {
			label, offs = signatureLabel("new", name, nil, params, nil)
		}
		info := SignatureInformation{Label: label, Documentation: doc, Parameters: []ParameterInformation{}}
		for _, o := range offs {
			info.Parameters = append(info.Parameters, ParameterInformation{Label: [2]int{utf16Count(label[:o[0]]), utf16Count(label[:o[1]])}})
		}
		active := activeParameter(r.text.src, args, off)
		if n := len(params); n > 0 && active >= n && params[n-1].Variadic {
			active = n - 1
		}
		return SignatureHelp{Signatures: []SignatureInformation{info}, ActiveParameter: active}, nil
	}
	return nil, nil
}

// activeParameter counts the argument list's own commas before the cursor
// at off, in src, the text args was parsed from. They are the commas after
// the arguments the parser read, so that what an argument holds counts for
// nothing: a comma between explicit type arguments, which the parser takes
// for such before a `(` (`f<int, string>($x)`), is the argument's own, and
// `$a < $b, $c > $d` stays two arguments.
func activeParameter(src []byte, args *ast.ArgList, off int) int {
	n := 0
	for _, a := range args.Args {
		if a.S.End >= off || off > len(src) {
			break
		}
		for _, t := range lexer.Tokenize(src[a.S.End:off], lexer.ModePhaenon) {
			if t.Kind == token.T_WHITESPACE || t.Kind == token.T_COMMENT || t.Kind == token.T_DOC_COMMENT {
				continue
			}
			if t.Kind == ',' {
				n++
			}
			break
		}
	}
	return n
}
