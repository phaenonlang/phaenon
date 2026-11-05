package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Unused private members (design/tooling.md §9.2) follow PHPStan's
// method.unused, property.unused, property.onlyWritten, property.onlyRead
// and classConstant.unused. While the checker types a class it records
// the receiver type of each member access (noteRecv) and the type of each
// dynamic member name (noteName). Once the class is checked, a pass over
// its syntax, and over the traits it uses, sorts the accesses into calls,
// reads and writes. An access counts for a private member of class C when
// its receiver may be a C: a mixed receiver counts, an unrelated class
// does not.

// noteRecv records the receiver type of a member access in a class body.
func (x *fnCtx) noteRecv(n ast.Node, t types.Type) {
	if x.class == nil || t == nil {
		return
	}
	c := x.c
	if c.recvs == nil {
		c.recvs = map[ast.Node]types.Type{}
	}
	if old, ok := c.recvs[n]; ok {
		t = types.MakeUnion(old, t)
	}
	c.recvs[n] = t
}

// noteName records the type of a dynamic member name: `$this->$name()`.
func (x *fnCtx) noteName(n ast.Node, t types.Type) {
	if x.class == nil || t == nil {
		return
	}
	c := x.c
	if c.dynNames == nil {
		c.dynNames = map[ast.Node]types.Type{}
	}
	if old, ok := c.dynNames[n]; ok {
		t = types.MakeUnion(old, t)
	}
	c.dynNames[n] = t
}

// noteByRef records a property passed by reference, `preg_match($re, $s,
// $this->m)`: the callee may write it.
func (x *fnCtx) noteByRef(e ast.Expr) {
	if x.class == nil {
		return
	}
	for {
		switch t := e.(type) {
		case *ast.Paren:
			e = t.Expr
			continue
		case *ast.Index:
			e = t.Array
			continue
		case *ast.PropertyFetch, *ast.StaticPropertyFetch:
			if x.c.byRefArgs == nil {
				x.c.byRefArgs = map[ast.Node]bool{}
			}
			x.c.byRefArgs[t] = true
		}
		return
	}
}

// role is what an access does to a property.
type role uint8

const (
	roleRead role = 1 << iota
	roleWrite
	// roleDim is a write through an offset, `$this->a[] = 1`, which reads
	// the property too when it may be an ArrayAccess object.
	roleDim
)

// propUse is what the class does with one private property.
type propUse struct {
	pr       *program.Property
	read     bool
	written  bool
	trueRead bool // read other than as the same-named argument of new self(…)
}

// useWalk is the pass over one class.
type useWalk struct {
	c          *Checker
	cl         *program.Class
	methods    map[string]*program.Method // by lower-case name
	props      map[string]*propUse
	consts     map[string]*program.Constant
	ctorParams map[string]bool // the constructor's promoted parameters
	roles      map[ast.Node]role

	// The context of the node visited.
	method   string // the enclosing method, closures included
	ctor     bool   // in the constructor's own body, outside closures
	hookProp string // in a hook of this property
	setHook  bool
	ctorArg  string // an argument of new self(…) for this parameter
}

// checkUnusedPrivate reports the private members of cl nothing uses.
func (c *Checker) checkUnusedPrivate(cl *program.Class) {
	if cl.Decl == nil || (cl.Kind != ast.KindClass && cl.Kind != ast.KindEnum) {
		return
	}
	w := &useWalk{c: c, cl: cl, methods: map[string]*program.Method{}, props: map[string]*propUse{},
		consts: map[string]*program.Constant{}, ctorParams: map[string]bool{}, roles: map[ast.Node]role{}}
	for _, m := range cl.Methods {
		if m.Class != cl || m.FromTrait != nil || m.Magic || m.Decl == nil || m.Vis != program.Private {
			continue
		}
		switch strings.ToLower(m.Name) {
		case "__construct", "__clone":
			continue
		}
		w.methods[strings.ToLower(m.Name)] = m
	}
	if cl.Kind == ast.KindClass {
		res := c.p.NewResolver(cl.File, cl.Scope, cl, ast.Span{})
		for _, pr := range cl.Props {
			if pr.Class != cl || pr.FromTrait != nil || pr.Magic || pr.Vis != program.Private {
				continue
			}
			if c.mappedByAttribute(res, pr.Attrs) {
				continue
			}
			u := &propUse{pr: pr}
			readable := !pr.Virtual || hasHook(pr, "get")
			writable := !pr.Virtual || hasHook(pr, "set")
			u.read, u.trueRead = !readable, !readable
			u.written = !writable || pr.Promoted || (pr.HasDefault && pr.Default != nil)
			w.props[pr.Name] = u
		}
	}
	for _, k := range cl.Consts {
		if k.Class != cl || k.Vis != program.Private {
			continue
		}
		w.consts[k.Name] = k
	}
	if len(w.methods) == 0 && len(w.props) == 0 && len(w.consts) == 0 {
		return
	}
	if ctor := cl.FindMethod("__construct"); ctor != nil && ctor.Class == cl && ctor.Decl != nil {
		for _, p := range ctor.Decl.Params {
			if p.Modifiers != 0 || len(p.Hooks) > 0 {
				w.ctorParams[p.Var.Name] = true
			}
		}
	}
	w.class(cl.Decl, true)
	seen := map[string]bool{}
	var traits func(names []string)
	traits = func(names []string) {
		for _, n := range names {
			t := c.p.Class(n)
			if t == nil || t.Decl == nil || seen[strings.ToLower(t.Name)] {
				continue
			}
			seen[strings.ToLower(t.Name)] = true
			w.class(t.Decl, false)
			traits(t.Traits)
		}
	}
	traits(cl.Traits)
	w.report()
}

// mappedByAttribute reports an attribute other than PHP's own: mapping
// frameworks (Doctrine, serializers, object mappers) mark so the private
// properties they read and write through reflection.
func (c *Checker) mappedByAttribute(res *program.Resolver, groups []*ast.AttrGroup) bool {
	for _, g := range groups {
		for _, a := range g.Attrs {
			if ac := c.p.Class(res.ClassName(a.Name)); ac == nil || !isStubFile(ac.File) {
				return true
			}
		}
	}
	return false
}

// propNameSpan is the span of a property's name in its declaration: the
// variable of the property or of the promoted parameter.
func propNameSpan(cl *program.Class, pr *program.Property) ast.Span {
	for _, m := range cl.Decl.Members {
		switch m := m.(type) {
		case *ast.PropertyDecl:
			for _, it := range m.Props {
				if it.Var.Name == pr.Name {
					return it.Var.S
				}
			}
		case *ast.MethodDecl:
			if !pr.Promoted || !strings.EqualFold(m.Name.Name, "__construct") {
				continue
			}
			for _, p := range m.Params {
				if p.Var != nil && p.Var.Name == pr.Name {
					return p.Var.S
				}
			}
		}
	}
	return pr.Span
}

func hasHook(pr *program.Property, name string) bool {
	for _, h := range pr.Hooks {
		if strings.EqualFold(h.Name.Name, name) {
			return true
		}
	}
	return false
}

func (w *useWalk) report() {
	cl := w.cl
	saved := w.c.file
	w.c.file = cl.File
	defer func() { w.c.file = saved }()
	for _, m := range w.methods {
		kind := "method"
		if m.Static {
			kind = "static method"
		}
		w.c.report(m.Decl.Name.S, P2406, "private %s %s::%s() is never used", kind, cl.Name, m.Name)
	}
	for _, u := range w.props {
		if u.read && !u.trueRead && u.pr.Promoted {
			u.read = false
		}
		kind := "property"
		if u.pr.Static {
			kind = "static property"
		}
		switch {
		case !u.read && !u.written:
			w.c.report(propNameSpan(cl, u.pr), P2406, "private %s %s::$%s is never used", kind, cl.Name, u.pr.Name)
		case !u.read:
			w.c.report(propNameSpan(cl, u.pr), P2407, "private %s %s::$%s is written but never read", kind, cl.Name, u.pr.Name)
		case !u.written:
			w.c.report(propNameSpan(cl, u.pr), P2408, "private %s %s::$%s is read but never written", kind, cl.Name, u.pr.Name)
		}
	}
	for _, k := range w.consts {
		w.c.report(k.Span, P2406, "private constant %s::%s is never used", cl.Name, k.Name)
	}
}

// class walks a class body: cl's own (own), or a trait cl uses.
func (w *useWalk) class(d *ast.ClassDecl, own bool) {
	for _, g := range d.Attrs {
		w.node(g)
	}
	for _, m := range d.Members {
		switch m := m.(type) {
		case *ast.MethodDecl:
			w.method = m.Name.Name
			w.ctor = own && strings.EqualFold(m.Name.Name, "__construct")
			for _, g := range m.Attrs {
				w.node(g)
			}
			for _, p := range m.Params {
				w.param(p)
			}
			for _, s := range m.Body {
				w.node(s)
			}
			if len(m.Body) == 0 && m.Arrow != nil {
				w.node(m.Arrow)
			}
			w.method, w.ctor = "", false
		case *ast.PropertyDecl:
			for _, g := range m.Attrs {
				w.node(g)
			}
			for _, it := range m.Props {
				w.node(it.Default)
			}
			if len(m.Props) == 1 {
				w.hooks(m.Props[0].Var.Name, m.Hooks)
			}
		default:
			w.node(m)
		}
	}
}

// param walks what a parameter evaluates: attributes, the default, hooks.
// Its variable is a declaration, not a read.
func (w *useWalk) param(p *ast.Param) {
	for _, g := range p.Attrs {
		w.node(g)
	}
	w.node(p.Default)
	if len(p.Hooks) > 0 {
		method, ctor := w.method, w.ctor
		w.ctor = false // a hook is not the constructor's body
		w.hooks(p.Var.Name, p.Hooks)
		w.method, w.ctor = method, ctor
	}
}

func (w *useWalk) hooks(prop string, hooks []*ast.PropertyHook) {
	for _, h := range hooks {
		w.hookProp, w.setHook = prop, strings.EqualFold(h.Name.Name, "set")
		w.method = "$" + prop + "::" + h.Name.Name
		for _, g := range h.Attrs {
			w.node(g)
		}
		for _, p := range h.Params {
			w.node(p.Default)
		}
		for _, s := range h.Body {
			w.node(s)
		}
		w.node(h.Expr)
	}
	w.hookProp, w.setHook, w.method = "", false, ""
}

func (w *useWalk) node(n ast.Node) { ast.Inspect(n, w.visit) }

// under walks n's descendants, not n.
func (w *useWalk) under(n ast.Node) {
	top := true
	ast.Inspect(n, func(m ast.Node) bool {
		if top {
			top = false
			return true
		}
		return w.visit(m)
	})
}

func (w *useWalk) visit(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.ClassDecl:
		// An anonymous class's body is its own class; its constructor
		// arguments are evaluated here.
		w.node(n.Args)
		return false
	case *ast.Closure, *ast.ArrowFunc:
		ctor, hook, set, arg := w.ctor, w.hookProp, w.setHook, w.ctorArg
		w.ctor, w.hookProp, w.setHook, w.ctorArg = false, "", false, ""
		w.under(n)
		w.ctor, w.hookProp, w.setHook, w.ctorArg = ctor, hook, set, arg
		return false
	case *ast.Assign:
		switch {
		case n.ByRef:
			w.target(n.Target, roleWrite)
			w.target(n.Value, roleRead|roleWrite)
		case n.Op == token.Kind('='):
			w.target(n.Target, roleWrite)
		default:
			w.target(n.Target, roleRead|roleWrite)
		}
	case *ast.IncDec:
		w.target(n.Expr, roleRead|roleWrite)
	case *ast.Foreach:
		w.target(n.Key, roleWrite)
		w.target(n.Value, roleWrite)
	case *ast.Variable:
		// The constructor reading a promoted parameter reads the property.
		if r := w.roles[n]; w.ctor && w.ctorParams[n.Name] && (r == 0 || r&roleRead != 0) {
			w.prop(n.Name, n, w.selfType(), roleRead, true)
		}
	case *ast.MethodCall:
		w.method1(n.Name, n, w.recv(n, n.Object))
		return w.callArgs(n)
	case *ast.StaticCall:
		w.method1(n.Name, n, w.recv(n, n.Class))
		return w.callArgs(n)
	case *ast.Call:
		if name, ok := n.Callee.(*ast.Name); ok && n.Args != nil && len(n.Args.Args) > 0 {
			switch strings.ToLower(name.Parts[len(name.Parts)-1]) {
			case "get_object_vars", "array_walk":
				if v, ok := n.Args.Args[0].Value.(*ast.Variable); ok && v.Name == "this" {
					for _, u := range w.props {
						if !u.pr.Static {
							u.read, u.trueRead = true, true
						}
					}
				}
			}
		}
		return w.callArgs(n)
	case *ast.New:
		if !w.ownClass(n.Class) || n.Args == nil {
			return w.callArgs(n)
		}
		var params []*ast.Param
		if ctor := w.cl.FindMethod("__construct"); ctor != nil && ctor.Class == w.cl && ctor.Decl != nil {
			params = ctor.Decl.Params
		}
		saved := w.ctorArg
		for i, a := range n.Args.Args {
			w.ctorArg = ""
			switch {
			case a.Name != nil:
				w.ctorArg = a.Name.Name
			case i < len(params) && !a.Unpack:
				w.ctorArg = params[i].Var.Name
			}
			w.node(a.Value)
		}
		w.ctorArg = saved
		return false
	case *ast.PropertyFetch:
		r := w.roles[n]
		if r == 0 {
			r = roleRead
		}
		if w.c.byRefArgs[n] {
			r |= roleRead | roleWrite
		}
		this := false
		if v, ok := n.Object.(*ast.Variable); ok && v.Name == "this" {
			this = true
		}
		switch name := n.Name.(type) {
		case *ast.Ident:
			w.prop(name.Name, n, w.recv(n, n.Object), r, this)
		case *ast.StringLit:
			w.prop(program.StringValue(name), n, w.recv(n, n.Object), r, this)
		default:
			w.dynamicProp(n)
		}
	case *ast.StaticPropertyFetch:
		r := w.roles[n]
		if r == 0 {
			r = roleRead
		}
		if w.c.byRefArgs[n] {
			r |= roleRead | roleWrite
		}
		if v, ok := n.Name.(*ast.Variable); ok {
			w.prop(v.Name, n, w.recv(n, n.Class), r, false)
		} else {
			w.dynamicProp(n)
		}
	case *ast.ClassConstFetch:
		recv := w.recv(n, n.Class)
		id, ok := n.Name.(*ast.Ident)
		if !ok {
			if w.mayBe(recv) {
				clear(w.consts)
			}
			break
		}
		if w.consts[id.Name] != nil && w.mayBe(recv) {
			delete(w.consts, id.Name)
		}
	case *ast.ArrayLit:
		w.callableArray(n)
	}
	return true
}

// callArgs walks a call's children with no constructor argument in
// effect: the innermost call is this one.
func (w *useWalk) callArgs(n ast.Node) bool {
	saved := w.ctorArg
	w.ctorArg = ""
	w.under(n)
	w.ctorArg = saved
	return false
}

// target marks what an assignment does to its target.
func (w *useWalk) target(e ast.Expr, r role) {
	switch t := e.(type) {
	case *ast.Paren:
		w.target(t.Expr, r)
	case *ast.Variable, *ast.PropertyFetch, *ast.StaticPropertyFetch:
		w.roles[t] = r
	case *ast.Index:
		root := t.Array
		for {
			if ix, ok := root.(*ast.Index); ok {
				root = ix.Array
				continue
			}
			break
		}
		switch root.(type) {
		case *ast.PropertyFetch, *ast.StaticPropertyFetch:
			w.roles[root] = r | roleDim
		}
	case *ast.ArrayLit:
		for _, it := range t.Items {
			if it != nil {
				w.target(it.Value, r)
			}
		}
	}
}

// recv is the receiver type of a member access: the one recorded while
// the class was typed, else what the syntax says, else nil (anything).
func (w *useWalk) recv(n ast.Node, obj ast.Expr) types.Type {
	if t, ok := w.c.recvs[n]; ok {
		return t
	}
	switch o := obj.(type) {
	case *ast.Variable:
		if o.Name == "this" {
			return w.selfType()
		}
	case *ast.Name:
		switch specialClassName(o) {
		case "self", "static":
			return w.selfType()
		}
	}
	return nil
}

func (w *useWalk) selfType() types.Type { return types.Class{Name: w.cl.Name} }

func (w *useWalk) ownClass(e ast.Expr) bool {
	n, ok := e.(*ast.Name)
	if !ok {
		return false
	}
	switch specialClassName(n) {
	case "self", "static":
		return true
	case "parent":
		return false
	}
	short := w.cl.Name[strings.LastIndex(w.cl.Name, `\`)+1:]
	return strings.EqualFold(n.Parts[len(n.Parts)-1], short)
}

// method1 counts a call of a method by name.
func (w *useWalk) method1(nameExpr ast.Expr, n ast.Node, recv types.Type) {
	name, ok := identName(nameExpr)
	if !ok {
		names, any := w.dynamicNames(n)
		if any {
			clear(w.methods)
			return
		}
		for _, s := range names {
			delete(w.methods, strings.ToLower(s))
		}
		return
	}
	key := strings.ToLower(name)
	if w.methods[key] == nil {
		return
	}
	if w.method == name && w.definitelyOwn(recv) {
		return // a recursive call
	}
	if w.mayBe(recv) {
		delete(w.methods, key)
	}
}

// callableArray counts [$this, 'name'] and [self::class, 'name'].
func (w *useWalk) callableArray(n *ast.ArrayLit) {
	if len(n.Items) != 2 || n.Items[0] == nil || n.Items[1] == nil || n.Items[0].Key != nil || n.Items[1].Key != nil {
		return
	}
	lit, ok := n.Items[1].Value.(*ast.StringLit)
	if !ok {
		return
	}
	name := program.StringValue(lit)
	key := strings.ToLower(name)
	if w.methods[key] == nil || w.method == name {
		return
	}
	t, ok := w.c.recvs[n]
	if !ok {
		switch o := n.Items[0].Value.(type) {
		case *ast.Variable:
			if o.Name == "this" {
				t = w.selfType()
			}
		case *ast.ClassConstFetch:
			if id, ok := o.Name.(*ast.Ident); ok && strings.EqualFold(id.Name, "class") {
				if nm, ok := o.Class.(*ast.Name); ok && (specialClassName(nm) == "self" || specialClassName(nm) == "static" || w.ownClass(nm)) {
					t = w.selfType()
				}
			}
		}
	}
	if t != nil && w.definitelyOwn(t) {
		delete(w.methods, key)
	}
}

// dynamicNames are the names a dynamic member name may hold; any when it
// is not a union of literals.
func (w *useWalk) dynamicNames(n ast.Node) (names []string, any bool) {
	t, ok := w.c.dynNames[n]
	if !ok {
		return nil, true
	}
	for _, m := range types.Members(t) {
		lit, ok := m.(types.StringLit)
		if !ok {
			return nil, true
		}
		names = append(names, lit.V)
	}
	return names, false
}

func (w *useWalk) dynamicProp(n ast.Node) {
	names, any := w.dynamicNames(n)
	if any {
		clear(w.props)
		return
	}
	for _, s := range names {
		delete(w.props, s)
	}
}

// prop counts an access of a property by name.
func (w *useWalk) prop(name string, n ast.Node, recv types.Type, r role, onThis bool) {
	u := w.props[name]
	if u == nil || !w.mayBe(recv) {
		return
	}
	// A hook's own property is its backing store: the set hook's accesses
	// and the get hook's reads do not count.
	inHook := onThis && w.hookProp == name
	if r&roleDim != 0 && r&roleRead == 0 && mayBeObject(u.pr.Type) {
		r |= roleRead
	}
	if r&roleRead != 0 && !inHook {
		u.read = true
		if !(w.ctorArg == name && u.pr.Promoted && w.definitelyOwn(recv)) {
			u.trueRead = true
		}
	}
	if r&roleWrite != 0 && !(inHook && w.setHook) {
		u.written = true
	}
}

// mayBe reports whether a value of type t may be an instance of the
// class (or, as a static receiver, name it). nil is any type.
func (w *useWalk) mayBe(t types.Type) bool {
	if t == nil {
		return true
	}
	for _, m := range types.Members(t) {
		if w.memberMayBe(m) {
			return true
		}
	}
	return false
}

func (w *useWalk) memberMayBe(t types.Type) bool {
	switch t := t.(type) {
	case types.Mixed, types.Object, types.Unknown, types.String:
		return true
	case types.Class:
		return w.classMayBe(t.Name)
	case types.This:
		return t.Of == nil || w.classMayBe(t.Of.Name)
	case types.Static:
		return t.Of == nil || w.classMayBe(t.Of.Name)
	case types.ClassString:
		return t.Of == nil || w.mayBe(t.Of)
	case types.StringLit:
		return w.classMayBe(t.V)
	case types.Intersection:
		for _, m := range t.Types {
			if !w.memberMayBe(m) {
				return false
			}
		}
		return true
	case types.Param:
		return t.Bound == nil || w.mayBe(t.Bound)
	case types.Conditional, types.ObjectShape:
		return true
	}
	return false
}

func (w *useWalk) classMayBe(name string) bool {
	p := w.c.p
	if p.IsSubclassOf(name, w.cl.Name) || p.IsSubclassOf(w.cl.Name, name) {
		return true
	}
	k := p.Class(name)
	if k == nil || k.Kind == ast.KindTrait {
		return true
	}
	final := finalByKeyword(w.cl) || w.cl.Kind == ast.KindEnum
	return k.Kind == ast.KindInterface && !final
}

// definitelyOwn reports a receiver that is always the class or a subclass.
func (w *useWalk) definitelyOwn(t types.Type) bool {
	if t == nil {
		return false
	}
	for _, m := range types.Members(t) {
		var name string
		switch m := m.(type) {
		case types.Class:
			name = m.Name
		case types.This:
			if m.Of != nil {
				name = m.Of.Name
			}
		case types.Static:
			if m.Of != nil {
				name = m.Of.Name
			}
		case types.ClassString:
			if c, ok := m.Of.(types.Class); ok {
				name = c.Name
			}
		case types.StringLit:
			name = m.V // self::class
		}
		if name == "" {
			return false
		}
		if k := w.c.p.Class(name); k != nil && k.Kind == ast.KindTrait {
			continue
		}
		if !w.c.p.IsSubclassOf(name, w.cl.Name) {
			return false
		}
	}
	return true
}
