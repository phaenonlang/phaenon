package program

import (
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/types"
)

// A type alias is a name of its namespace (design/generics.md §11.6):
// `type Row = …;` in namespace App\Orders declares App\Orders\Row, as
// `class Orders` declares App\Orders\Orders. The aliases share the classes'
// rule for names: a table by qualified name, compared without regard to
// case, in which a name is declared once, by an alias or by a class-like.
// A name in a type position of a .phn resolves as a class name does,
// through the file's imports and else in its namespace, and what it finds
// is a class or an alias.
//
// An alias never reaches PHP, so nothing autoloads it: it is found by its
// declaration. The files of the program declare theirs as they are added
// (collect); PHP code declares none and names none, so a type written in a
// .php file is never looked up here.

// aliasTable is a program's type aliases.
type aliasTable struct {
	// byName holds the first declaration of each qualified name,
	// lower-cased. A later one is a duplicate, which the checker reports.
	byName map[string]*TypeAlias
	// resolving are the aliases whose right-hand sides are being resolved,
	// the outermost first: an alias met again while it is here mentions
	// itself.
	resolving []*TypeAlias
}

// declareAlias records a `type` declaration under its qualified name. The
// first declaration of a name wins, as for a class.
func (p *Program) declareAlias(f *File, scope *Scope, d *ast.TypeAlias) {
	name := scope.prefix() + d.Name.Name
	key := strings.ToLower(name)
	if p.aliases.byName == nil {
		p.aliases.byName = map[string]*TypeAlias{}
	}
	if _, declared := p.aliases.byName[key]; declared {
		return
	}
	p.aliases.byName[key] = &TypeAlias{Name: name, Decl: d, File: f, Scope: scope}
}

// TypeAlias returns the type alias declared under a fully-qualified name
// (any case, with or without a leading backslash), resolved; nil when the
// name is not an alias.
func (p *Program) TypeAlias(name string) *TypeAlias {
	a := p.alias(strings.ToLower(strings.TrimPrefix(name, "\\")))
	if a != nil {
		p.resolveAlias(a)
	}
	return a
}

// alias is the declaration of a lower-cased qualified name.
func (p *Program) alias(lower string) *TypeAlias { return p.aliases.byName[lower] }

// TypeAliases returns every declared alias, sorted by name (for tools).
func (p *Program) TypeAliases() []*TypeAlias {
	out := make([]*TypeAlias, 0, len(p.aliases.byName))
	for _, a := range p.aliases.byName {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// resolveAlias resolves an alias's type parameters and right-hand side, in
// the scope of its own declaration, once. It reports false for an alias
// that is being resolved: the mention that led here is the alias's own,
// directly or through the aliases resolved since, and each of them is
// marked Cyclic.
func (p *Program) resolveAlias(a *TypeAlias) bool {
	switch a.state {
	case 1:
		stack := p.aliases.resolving
		for i := len(stack) - 1; i >= 0; i-- {
			stack[i].Cyclic = true
			if stack[i] == a {
				break
			}
		}
		return false
	case 0:
		a.state = 1
		p.aliases.resolving = append(p.aliases.resolving, a)
		r := p.newResolver(a.File, a.Scope, nil, a.Decl.S)
		a.Params = nativeTypeParams(a.Decl.TypeParams, r)
		ImplyMapKeyBounds(a.Params, a.Decl)
		a.Type = r.withParams(a.Params).native(a.Decl.Type)
		p.aliases.resolving = p.aliases.resolving[:len(p.aliases.resolving)-1]
		a.state = 2
	}
	return true
}

// namesAliases reports whether the types the resolver reads may name an
// alias: those written in Phaenon. PHP has no aliases, so a name in a .php
// file is a class or nothing, as PHP itself would find.
func (r *resolver) namesAliases() bool {
	return r.f != nil && r.f.Mode == lexer.ModePhaenon
}

// aliasNamed is the alias a qualified name stands for where the resolver
// reads types, unresolved. A name the program has a class under is the
// class: the two collide, which the checker reports at the alias, and the
// uses of the name are judged once, as uses of the class.
func (r *resolver) aliasNamed(name string) *TypeAlias {
	if !r.namesAliases() || isSpecialClassName(name) {
		return nil
	}
	lower := strings.ToLower(strings.TrimPrefix(name, "\\"))
	if _, class := r.p.classes[lower]; class {
		return nil
	}
	return r.p.alias(lower)
}

// typeAlias resolves the alias a qualified name stands for, instantiating
// a generic one with args (a missing argument falls back to the
// parameter's default, then its bound). A mention of an alias inside its
// own right-hand side reads as mixed.
func (r *resolver) typeAlias(name string, args []types.Type) (types.Type, bool) {
	a := r.aliasNamed(name)
	if a == nil {
		return nil, false
	}
	if !r.p.resolveAlias(a) {
		return types.TMixed, true
	}
	if len(a.Params) == 0 {
		return a.Type, true
	}
	subst := make(map[string]types.Type, len(a.Params))
	for i, tp := range a.Params {
		switch {
		case i < len(args) && args[i] != nil:
			subst[tp.Name] = args[i]
		case tp.Default != nil:
			subst[tp.Name] = tp.Default
		case tp.Bound != nil:
			subst[tp.Name] = tp.Bound
		default:
			subst[tp.Name] = types.TMixed
		}
	}
	return types.Substitute(a.Type, subst), true
}

// TypeAlias returns the alias a name written in a type position stands
// for, resolved: the name goes through the file's imports and else its
// namespace, as a class name does. nil when the name is something that
// comes first (a type keyword, a type parameter in scope, the class's own
// @phpstan-type), or is no alias.
func (r *Resolver) TypeAlias(n *ast.Name) *TypeAlias {
	if n == nil || len(n.Parts) == 0 || !r.namesAliases() {
		return nil
	}
	if len(n.Parts) == 1 && n.Kind == ast.NameUnqualified {
		if _, param := r.params[n.Parts[0]]; param {
			return nil
		}
		if _, keyword := r.keyword(strings.ToLower(n.Parts[0]), nil, nil); keyword {
			return nil
		}
		if _, own := r.alias(n.Parts[0]); own {
			return nil
		}
	}
	a := r.aliasNamed(r.scope.ResolveClass(n))
	if a != nil {
		r.p.resolveAlias(a)
	}
	return a
}

// DocAlias is TypeAlias for a name as a docblock writes it (`Row`,
// `Orders\Row`, `\App\Orders\Row`): a docblock of a .phn may name an alias,
// and resolves it as a signature does (design/collections.md §6.5).
func (r *Resolver) DocAlias(name string) *TypeAlias {
	if name == "" || strings.HasPrefix(name, "$") || !r.namesAliases() {
		return nil
	}
	if !strings.Contains(name, "\\") {
		if _, param := r.params[name]; param {
			return nil
		}
		if _, keyword := r.keyword(strings.ToLower(name), nil, nil); keyword {
			return nil
		}
		if _, own := r.alias(name); own {
			return nil
		}
	}
	a := r.aliasNamed(r.special(r.scope.ResolveClassString(name)))
	if a != nil {
		r.p.resolveAlias(a)
	}
	return a
}
