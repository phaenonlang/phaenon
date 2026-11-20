package checker

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
)

// A value that reads itself (P2016). PHP evaluates a class constant's
// value, and a backed enum case's when it makes the case, the first time
// the value is needed, and marks each constant it fetches on the way as
// being evaluated; a fetch of a marked constant throws "Cannot declare
// self-referencing constant self::X" at that fetch, naming the constant as
// written there. Which declaration of a cycle php names depends on which
// is needed first, so each declaration on the cycle is reported, at its
// reference into it, as each alias of a cycle is (P2015). One that only
// leads into a cycle is not: the fetch php throws at is one of the cycle's.
// The reference is followed only where php follows it whatever the values:
// not into the right side of `??`, `&&` and `||`, the branches of a
// ternary or a closure's body.

// valueDecl is a declaration whose value is read by name: a class constant,
// or an enum case, whose object PHP makes with its value.
type valueDecl struct {
	k  *program.Constant
	cs *program.EnumCase
}

func (d valueDecl) value() ast.Expr {
	if d.k != nil {
		return d.k.Value
	}
	return d.cs.Value
}

// class is the class the value is evaluated for, which self names.
func (d valueDecl) class() *program.Class {
	if d.k != nil {
		return d.k.Class
	}
	return d.cs.Class
}

// where are the file and the namespace the value is written in.
func (d valueDecl) where() (*program.File, *program.Scope) {
	if d.k != nil {
		return d.k.File, d.k.Scope
	}
	if cl := d.cs.Class; cl != nil {
		return cl.File, cl.Scope
	}
	return nil, nil
}

func (d valueDecl) name() string {
	if d.k != nil {
		return d.class().Name + "::" + d.k.Name
	}
	return d.class().Name + "::" + d.cs.Name
}

// valueRef is a fetch in a value of another declaration's: where it is,
// the constant as PHP names it there, and what it fetches.
type valueRef struct {
	at      ast.Span
	written string
	to      valueDecl
}

// valueRefs are the fetches php follows whenever it evaluates d's value,
// in the order it evaluates them.
func (c *Checker) valueRefs(d valueDecl) []valueRef {
	if refs, ok := c.refs[d]; ok {
		return refs
	}
	if c.refs == nil {
		c.refs = map[valueDecl][]valueRef{}
	}
	c.refs[d] = nil
	f, scope := d.where()
	cl := d.class()
	if f == nil || cl == nil || d.value() == nil {
		return nil
	}
	res := c.p.NewResolver(f, scope, cl, ast.Span{})
	var refs []valueRef
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Closure, *ast.ArrowFunc:
			return false // the body runs when the closure is called
		case *ast.Binary:
			switch n.Op {
			case token.T_COALESCE, token.T_BOOLEAN_AND, token.T_BOOLEAN_OR, token.T_LOGICAL_AND, token.T_LOGICAL_OR:
				ast.Inspect(n.Left, visit)
				return false // the right side is evaluated where the left asks for it
			}
		case *ast.Ternary:
			ast.Inspect(n.Cond, visit)
			return false
		case *ast.ClassConstFetch:
			if r, ok := c.valueRef(n, res, cl); ok {
				refs = append(refs, r)
			}
		}
		return true
	}
	ast.Inspect(d.value(), visit)
	c.refs[d] = refs
	return refs
}

// valueRef is the declaration `Name::X` fetches in a value evaluated for
// cl. static is no reference: PHP refuses it in a constant expression.
func (c *Checker) valueRef(e *ast.ClassConstFetch, res *program.Resolver, cl *program.Class) (valueRef, bool) {
	n, ok := e.Class.(*ast.Name)
	id, named := e.Name.(*ast.Ident)
	if !ok || !named || strings.EqualFold(id.Name, "class") {
		return valueRef{}, false
	}
	var target *program.Class
	written := n.Parts[0]
	switch specialClassName(n) {
	case "self":
		target = cl
	case "parent":
		target = cl.ParentClass()
	case "static":
		return valueRef{}, false
	default:
		name := res.ClassName(n)
		target = c.p.Class(name)
		written = strings.TrimPrefix(name, `\`)
	}
	if target == nil {
		return valueRef{}, false
	}
	r := valueRef{at: e.S, written: written + "::" + id.Name}
	if cs := target.FindCase(id.Name); cs != nil {
		r.to = valueDecl{cs: cs}
	} else if k := target.FindConst(id.Name); k != nil {
		r.to = valueDecl{k: k}
	} else {
		return valueRef{}, false
	}
	return r, true
}

// cycle is how a value reads itself: the first fetch of it that leads
// back to it, and the declarations on the way, the empty list for a value
// that fetches itself.
type cycle struct {
	at      valueRef
	through []string
}

// cycleOf is d's cycle, nil for a value that does not read itself.
func (c *Checker) cycleOf(d valueDecl) *cycle {
	if cy, ok := c.cycles[d]; ok {
		return cy
	}
	var found *cycle
	seen := map[valueDecl]bool{}
	var through []string // the declarations between d and the fetch of d
	var reaches func(from valueDecl) bool
	reaches = func(from valueDecl) bool {
		if from == d {
			return true
		}
		if seen[from] {
			return false
		}
		seen[from] = true
		through = append(through, from.name())
		for _, r := range c.valueRefs(from) {
			if reaches(r.to) {
				return true
			}
		}
		through = through[:len(through)-1]
		return false
	}
	for _, r := range c.valueRefs(d) {
		if reaches(r.to) {
			found = &cycle{at: r, through: through}
			break
		}
	}
	if c.cycles == nil {
		c.cycles = map[valueDecl]*cycle{}
	}
	c.cycles[d] = found
	return found
}

// selfReference reports d's value where it reads d itself, at the first
// fetch that leads back to d, once: it is the fetch php throws at when d's
// value is the one first needed. A constant cl does not declare (a trait's,
// copied into it) is reported where it is written, by the trait's own
// check.
func (c *Checker) selfReference(d valueDecl, cl *program.Class) bool {
	if d.value() == nil || d.k != nil && !declaredIn(cl, d.k) {
		return false
	}
	cy := c.cycleOf(d)
	if cy == nil {
		return false
	}
	what := "case"
	if d.k != nil {
		what = "constant"
	}
	via := ""
	if len(cy.through) > 0 {
		via = " through " + strings.Join(cy.through, ", ")
	}
	c.report(cy.at.at, P2016, "%s %s refers to itself%s: PHP throws \"Cannot declare self-referencing constant %s\" when the value is first needed", what, d.name(), via, cy.at.written)
	return true
}

// declaredIn reports whether cl's own declaration holds the constant's, and
// not a trait's that cl uses.
func declaredIn(cl *program.Class, k *program.Constant) bool {
	if cl.Decl == nil || k.File != cl.File {
		return false
	}
	return k.Span.Start >= cl.Decl.S.Start && k.Span.End <= cl.Decl.S.End
}
