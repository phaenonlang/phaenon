package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
)

// Readonly properties a constructor must initialise (P2515), as PHPStan's
// MissingReadOnlyPropertyAssignRule reads them: a class's readonly
// property without a default that is not promoted must be assigned on
// every way out of its constructor, or of a method of the class the
// constructor calls on $this, self or static, which PHPStan counts too
// (the assignment there is P2206 on its own). An exit by throw needs
// nothing. A class with no constructor of its own initialises nothing;
// properties a trait brings are the using class's. The flow is the
// checker's own: each method body records, in state.inits, the
// properties of $this it certainly assigns, and the pass runs once every
// file is checked, when the bodies of the traits a class uses have been
// read too.

// initialise records that the body assigned $this->name on the way here.
func (e *state) initialise(name string) {
	if e.inits == nil {
		e.inits = map[string]bool{}
	}
	e.inits[name] = true
}

// recordInits keeps what a method body certainly assigns before it
// returns: the properties every live exit has. A body that never returns
// (it always throws) initialises nothing, as for PHPStan.
func (c *Checker) recordInits(d *ast.MethodDecl, exits []*state) {
	var out map[string]bool
	for _, e := range exits {
		if e == nil || e.dead {
			continue
		}
		if out == nil {
			out = map[string]bool{}
			for k := range e.inits {
				out[k] = true
			}
			continue
		}
		for k := range out {
			if !e.inits[k] {
				delete(out, k)
			}
		}
	}
	if out == nil {
		out = map[string]bool{}
	}
	if c.inits == nil {
		c.inits = map[*ast.MethodDecl]map[string]bool{}
	}
	c.inits[d] = out
}

// mustInitialise reports whether the constructor of cl must assign pr:
// a readonly instance property declared in cl or a trait it uses, typed,
// without a default (PHP 8.6 initialises it with its default; on 8.5 the
// default is P1002's mistake), not promoted by cl's own constructor and
// not virtual.
func mustInitialise(cl *program.Class, pr *program.Property) bool {
	return pr.Class == cl && pr.Readonly && !pr.Static && !pr.Magic && pr.NativeType != nil &&
		!pr.HasDefault && !pr.Virtual && !(pr.Promoted && pr.FromTrait == nil)
}

// checkReadonlyInits reports each readonly property a checked class's
// constructor leaves uninitialised, on the property's declaration: in the
// trait that declares it when a trait brought it, where PHPStan reports it
// too (and an ignore comment on it is read).
func (c *Checker) checkReadonlyInits() {
	saved := c.file
	defer func() { c.file = saved }()
	for _, cl := range c.initClasses {
		if cl.Kind != ast.KindClass || cl.File == nil || !cl.File.Checked {
			continue
		}
		var props []*program.Property
		for _, pr := range cl.Props {
			if mustInitialise(cl, pr) {
				props = append(props, pr)
			}
		}
		if len(props) == 0 {
			continue
		}
		assigned, known := c.constructorInits(cl)
		if !known {
			continue
		}
		for _, pr := range props {
			if assigned[pr.Name] || pr.File == nil || !pr.File.Checked {
				continue
			}
			name := cl.Name
			if cl.Decl != nil && cl.Decl.Name == nil {
				name = "class@anonymous" // its internal name carries the file and an ordinal
			}
			c.file = pr.File
			c.report(pr.Span, P2515, "class %s has an uninitialised readonly property $%s: assign it in the constructor", name, pr.Name)
		}
	}
}

// constructorInits is what cl's constructor and the methods of cl it calls
// on $this, self or static (and they in turn) assign: a property any of
// them assigns on every way out counts, as PHPStan counts it. A
// constructor cl inherits initialises none of cl's properties. known is
// false when a body it needs was not read (a trait of a file not checked):
// nothing is then reported.
func (c *Checker) constructorInits(cl *program.Class) (assigned map[string]bool, known bool) {
	assigned = map[string]bool{}
	ctor := cl.FindMethod("__construct")
	if ctor == nil || ctor.Class != cl {
		return assigned, true
	}
	seen := map[string]bool{}
	queue := []*program.Method{ctor}
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		if seen[strings.ToLower(m.Name)] || m.Decl == nil {
			continue
		}
		seen[strings.ToLower(m.Name)] = true
		if m.Abstract || !m.Decl.HasBody {
			continue
		}
		inits, ok := c.inits[m.Decl]
		if !ok {
			return assigned, false
		}
		for k := range inits {
			assigned[k] = true
		}
		for _, name := range thisCalls(m.Decl) {
			if callee := cl.FindMethod(name); callee != nil && callee.Class == cl {
				queue = append(queue, callee)
			}
		}
	}
	return assigned, true
}

// thisCalls are the names of the methods a method body calls on $this,
// self or static, outside the closures it declares.
func thisCalls(d *ast.MethodDecl) []string {
	var out []string
	visit := func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Closure, *ast.ArrowFunc, *ast.ClassDecl:
			return false
		case *ast.MethodCall:
			if v, ok := n.Object.(*ast.Variable); ok && v.Name == "this" {
				if name, ok := identName(n.Name); ok {
					out = append(out, name)
				}
			}
		case *ast.StaticCall:
			if cn, ok := n.Class.(*ast.Name); ok {
				switch specialClassName(cn) {
				case "self", "static":
					if name, ok := identName(n.Name); ok {
						out = append(out, name)
					}
				}
			}
		}
		return true
	}
	for _, s := range d.Body { // an expression body's statement too
		ast.Inspect(s, visit)
	}
	return out
}

// prematureRead reports a read of a readonly property of $this in the
// constructor before every way here has assigned it: PHP throws "must not
// be accessed before initialization" (PHPStan's property.uninitializedReadonly
// as well). Under isset() and ?? PHP answers false instead, but the answer
// is known, and PHPStan reports those reads too.
func (x *fnCtx) prematureRead(e *ast.PropertyFetch, env *state) {
	if !x.trackInits || x.class == nil || x.method == nil || !strings.EqualFold(x.method.Name, "__construct") {
		return
	}
	v, ok := e.Object.(*ast.Variable)
	if !ok || v.Name != "this" {
		return
	}
	name, ok := identName(e.Name)
	if !ok || env.inits[name] {
		return
	}
	pr := x.class.FindProperty(name)
	if pr == nil || !mustInitialise(x.class, pr) {
		return
	}
	x.report(e.Name.Span(), P2515, "readonly property %s::$%s is read before the constructor initialises it", x.class.Name, name)
}
