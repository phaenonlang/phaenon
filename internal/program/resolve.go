package program

import (
	"math"
	"path"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/printer"
	"github.com/phaenonlang/phaenon/internal/types"
)

// resolver converts type syntax (native or docblock) into types.Type in the
// context of a file scope, a class and a set of type parameters.
type resolver struct {
	p          *Program
	f          *File
	scope      *Scope
	class      *Class
	params     map[string]*TypeParam
	aliases    map[string]types.Type
	aliasNodes map[string]*docblock.TypeNode
	resolving  map[string]bool
	span       ast.Span
	// erased counts the positions around the type being read that do not
	// reach PHP as written (a type argument, a closure signature, a tuple's
	// or a shape's members, an alias's right-hand side): the emitter writes
	// them into docblocks, where a word such as `integer` is int.
	erased int
}

func (p *Program) newResolver(f *File, scope *Scope, class *Class, span ast.Span) *resolver {
	r := &resolver{p: p, f: f, scope: scope, class: class, params: map[string]*TypeParam{}, span: span}
	if class != nil {
		class.ensureHeader()
		for _, tp := range class.TypeParams {
			r.params[tp.Name] = tp
		}
		r.aliases = class.Aliases
	}
	return r
}

func (r *resolver) withParams(tps []*TypeParam) *resolver {
	c := *r
	c.params = map[string]*TypeParam{}
	for k, v := range r.params {
		c.params[k] = v
	}
	for _, tp := range tps {
		c.params[tp.Name] = tp
	}
	return &c
}

func (r *resolver) problem(format string, args ...any) {
	r.p.problem(r.f, r.span, format, args...)
}

// WithReported returns a copy that reads each of names as a name already
// reported (types.Unknown): the type parameters of a construct the checker
// refuses, whose uses say nothing more. A type parameter of the same name
// around it is hidden, as the construct's own would hide it.
func (r *Resolver) WithReported(names []string) *Resolver {
	c := *r
	c.aliases = make(map[string]types.Type, len(r.aliases)+len(names))
	for k, v := range r.aliases {
		c.aliases[k] = v
	}
	c.params = make(map[string]*TypeParam, len(r.params))
	for k, v := range r.params {
		c.params[k] = v
	}
	for _, n := range names {
		c.aliases[n] = types.Unknown{Name: n}
		delete(c.params, n)
	}
	return &c
}

// Reported reports a name WithReported made a name already reported.
func (r *Resolver) Reported(name string) bool {
	u, ok := r.aliases[name].(types.Unknown)
	return ok && u.Name == name
}

// Resolver exposes type resolution to the checker for in-body syntax
// (`instanceof Foo`, inline `@var`, closures).
type Resolver = resolver

// NewResolver creates a resolver for a position in a file.
func (p *Program) NewResolver(f *File, scope *Scope, class *Class, span ast.Span) *Resolver {
	return p.newResolver(f, scope, class, span)
}

// WithParams returns a copy that also knows the given type parameters.
func (r *Resolver) WithParams(tps []*TypeParam) *Resolver { return r.withParams(tps) }

// Native resolves a native type expression (nil → nil).
func (r *Resolver) Native(t ast.TypeExpr) types.Type { return r.native(t) }

// NativeErased resolves a type written where it compiles into a docblock: a
// type parameter's bound or default (resolver.erased).
func (r *Resolver) NativeErased(t ast.TypeExpr) types.Type { return r.nativeErased(t) }

// Doc resolves a docblock type node.
func (r *Resolver) Doc(n *docblock.TypeNode) types.Type { return r.docType(n) }

// ClassName resolves a class name in a name position.
func (r *Resolver) ClassName(n *ast.Name) string { return r.resolveClassName(n) }

// Scope returns the resolver's namespace scope.
func (r *Resolver) Scope() *Scope { return r.scope }

// TypeParam returns the type parameter visible under name, or nil.
func (r *Resolver) TypeParam(name string) *TypeParam { return r.params[name] }

// ClassAlias reports an alias the resolver's class declares or imports
// in its docblock (@phpstan-type, @phpstan-import-type) under name, which a
// type position reads before a class of that name.
func (r *Resolver) ClassAlias(name string) bool {
	_, ok := r.alias(name)
	return ok
}

// ParamType is the type a reference to the type parameter name denotes.
func (r *Resolver) ParamType(tp *TypeParam) types.Type {
	return types.Param{Name: tp.Name, Bound: tp.Bound, Owner: r.owner()}
}

// NativeParams builds type parameters from Phaenon syntax in this scope
// (closures declare their own).
func (r *Resolver) NativeParams(tps []*ast.TypeParam) []*TypeParam { return nativeTypeParams(tps, r) }

// NativeArgs resolves a list of type expressions.
func (r *Resolver) NativeArgs(ts []ast.TypeExpr) []types.Type {
	out := make([]types.Type, len(ts))
	for i, t := range ts {
		out[i] = r.nativeErased(t) // type arguments compile into docblocks
	}
	return out
}

// Class returns the resolver's current class (nil outside a class).
func (r *Resolver) Class() *Class { return r.class }

// ------------------------------------------------------------------ native types

func (r *resolver) resolveClassName(n *ast.Name) string {
	name := r.scope.ResolveClass(n)
	return r.special(name)
}

// special maps self/static/parent to concrete class names where possible
// (static stays "static": it is a type, not a name).
func (r *resolver) special(name string) string {
	switch name {
	case "self":
		if r.class != nil {
			return r.class.Name
		}
	case "parent":
		if r.class != nil {
			r.class.ensureHeader()
			if r.class.Parent != "" {
				return r.class.Parent
			}
		}
	}
	return name
}

func (r *resolver) native(t ast.TypeExpr) types.Type {
	switch t := t.(type) {
	case nil:
		return nil
	case *ast.NamedType:
		args := make([]types.Type, 0, len(t.TypeArgs))
		for _, a := range t.TypeArgs {
			args = append(args, r.nativeErased(a))
		}
		word, otherCase := "", false
		if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified {
			lower := strings.ToLower(t.Name.Parts[0])
			if lower == "class" {
				// `class<T>` is the type of T::class — native syntax only; a
				// docblock writes PHPStan's class-string<T> (generics.md §11.2).
				if len(args) == 1 {
					return types.ClassString{Of: args[0]}
				}
				return types.ClassString{}
			}
			if word, otherCase = OtherCaseTypeName(t.Name.Parts[0]); !otherCase {
				if !r.namesClass(t.Name, lower) {
					if ty, ok := r.keyword(lower, args, nil); ok {
						return ty
					}
				}
			}
			if tp, ok := r.params[t.Name.Parts[0]]; ok {
				return types.Param{Name: tp.Name, Bound: tp.Bound, Owner: r.owner()}
			}
			if a, ok := r.alias(t.Name.Parts[0]); ok {
				return a
			}
		}
		name := r.scope.ResolveClass(t.Name)
		if a, ok := r.typeAlias(name, args); ok {
			return a
		}
		if otherCase && r.f != nil && r.f.Mode == lexer.ModePhaenon && r.p.Class(name) == nil {
			// `Map` where no class of that name resolves: the checker
			// reports it (P2001), saying that Phaenon's type is written
			// `map`, and the type is read as that one, so that nothing
			// else is reported.
			if ty, ok := r.keyword(word, args, nil); ok {
				return ty
			}
		}
		return r.classType(name, args)
	case *ast.NullableType:
		return types.Nullable(r.native(t.Type))
	case *ast.UnionType:
		ms := make([]types.Type, 0, len(t.Types))
		for _, m := range t.Types {
			ms = append(ms, r.native(m))
		}
		return types.MakeUnion(ms...)
	case *ast.IntersectionType:
		ms := make([]types.Type, 0, len(t.Types))
		for _, m := range t.Types {
			ms = append(ms, r.native(m))
		}
		return types.MakeIntersection(ms...)
	case *ast.ClosureType:
		c := types.Callable{Closure: true, HasSig: true, Return: r.nativeErased(t.Return)}
		for _, pt := range t.Params {
			c.Params = append(c.Params, types.CallableParam{Type: r.nativeErased(pt.Type), Optional: pt.Optional, Variadic: pt.Variadic})
		}
		return c
	case *ast.TupleType:
		// (A, B) is the sealed positional shape array{A, B}.
		sh := types.Shape{List: true}
		for i, el := range t.Elems {
			sh.Entries = append(sh.Entries, types.ShapeEntry{IsInt: true, IntKey: int64(i), Type: r.nativeErased(el)})
		}
		return sh
	case *ast.ShapeType:
		// array{id: int, name?: string} is the lattice's shape, the one a
		// docblock's shape is (docType): the two spell one type.
		sh := types.Shape{Unsealed: t.Unsealed}
		for _, e := range t.Entries {
			se := types.ShapeEntry{Type: r.nativeErased(e.Type), Optional: e.Optional}
			if v, ok := types.IntKey(e.Name); e.IsInt {
				se.IsInt, se.IntKey = true, e.Int
			} else if ok {
				se.IsInt, se.IntKey = true, v // a quoted key PHP stores as an int is that int's
			} else {
				se.Key = e.Name
			}
			sh.Entries = append(sh.Entries, se)
		}
		return sh
	}
	return types.TMixed
}

// namesClass reports whether a one-part name written in a Phaenon type
// (lower, its lower case) names a class or a type alias where the resolver
// would otherwise read one of its type words. Where the type reaches PHP
// as written, integer, double, boolean, scalar, numeric, number, noreturn
// and resource are class names, as PHP reads them, and the class is the
// one PHP finds; a type alias of the name is found as a class is
// (design/generics.md, D29), and its declaration the checker judges.
// Without either the checker reports the name (P2001) and the type is read
// as the one meant, so that nothing else is reported. Where the emitter
// writes the type into a docblock (erased), the docblock's words are read
// as PHPStan reads them: the class when a `use` imports one of that name,
// in any letter case, or the namespace declares one (not the global
// namespace), an alias as anywhere else, else the word.
func (r *resolver) namesClass(n *ast.Name, lower string) bool {
	if _, confusable := ConfusableTypeName(lower); !confusable {
		return false
	}
	if r.erased > 0 {
		if _, imported := r.scope.classes[lower]; imported {
			return true
		}
		if r.aliasNamed(r.scope.ResolveClass(n)) != nil {
			return true
		}
		return r.scope.Namespace != "" && r.p.Class(r.scope.Namespace+"\\"+n.Parts[0]) != nil
	}
	resolved := r.scope.ResolveClass(n)
	return r.p.Class(resolved) != nil || r.aliasNamed(resolved) != nil
}

// nativeErased reads a type in a position that does not reach PHP as
// written (resolver.erased).
func (r *resolver) nativeErased(t ast.TypeExpr) types.Type {
	r.erased++
	defer func() { r.erased-- }()
	return r.native(t)
}

func (r *resolver) owner() string {
	if r.class != nil {
		return r.class.Name
	}
	return ""
}

// classType builds the type for a resolved class name (self/static/parent
// already mapped where possible).
func (r *resolver) classType(name string, args []types.Type) types.Type {
	switch name {
	case "self":
		if r.class != nil {
			if len(args) == 0 {
				args = r.selfArgs() // self<$this> keeps what it says
			}
			return types.Class{Name: r.class.Name, Args: args}
		}
		return types.TObject
	case "static":
		if r.class != nil {
			if len(args) == 0 {
				args = r.selfArgs()
			}
			return types.Static{Of: &types.Class{Name: r.class.Name, Args: args}}
		}
		return types.TObject
	case "parent":
		if r.class != nil {
			r.class.ensureHeader()
			if r.class.Parent != "" {
				return types.Class{Name: r.class.Parent, Args: r.class.ParentArgs}
			}
		}
		return types.TObject
	}
	if strings.EqualFold(name, "Closure") {
		return types.TClosure
	}
	if len(args) == 1 && ValueOnlyIterable(name) {
		args = []types.Type{types.TMixed, args[0]} // Iterator<V> is Iterator<mixed, V>
	}
	return types.Class{Name: name, Args: args}
}

// ValueOnlyIterable says whether a single type argument on the class names
// its value, not its key: `Iterator<string>` is `Iterator<mixed, string>`,
// `Generator<string>` is `Generator<mixed, string, mixed, mixed>`, as
// `iterable<string>` is and as PHPStan reads the SPL iteration interfaces.
func ValueOnlyIterable(name string) bool {
	switch strings.ToLower(strings.TrimPrefix(name, "\\")) {
	case "traversable", "iterator", "iteratoraggregate", "generator":
		return true
	}
	return false
}

// selfArgs are the class's own type parameters as arguments (self inside a
// generic class is Foo<T>).
func (r *resolver) selfArgs() []types.Type {
	if r.class == nil || len(r.class.TypeParams) == 0 {
		return nil
	}
	out := make([]types.Type, len(r.class.TypeParams))
	for i, tp := range r.class.TypeParams {
		out[i] = types.Param{Name: tp.Name, Bound: tp.Bound, Owner: r.class.Name}
	}
	return out
}

// phaenonTypeNames are the type names Phaenon adds that neither PHP nor
// PHPStan has: `map` and `bytes` in a type position, and in a docblock of a
// .phn also `non-empty-map` and `class<T>`, which the emitter translates
// for PHPStan.
var phaenonTypeNames = map[string]bool{"map": true, "non-empty-map": true, "bytes": true, "class": true}

// OtherCaseTypeName reports a name that spells one of Phaenon's own type
// names in another letter case than lower case, and returns that type name.
// The words Phaenon adds are written in lower case (design/roadmap.md D30),
// and these are names PHP leaves free: `Map` and `Bytes` are class names
// like any other, resolved as such. PHP's type names and PHPStan's
// vocabulary keep their own rule, any case (`Int`, `List<int>` in a
// docblock). `class` in a type position is no name PHP allows, and the
// parser reads it in any case: the checker reports the case (P1006).
func OtherCaseTypeName(name string) (string, bool) {
	lower := strings.ToLower(name)
	return lower, phaenonTypeNames[lower] && name != lower
}

// ConfusableTypeName reports, in any letter case, a name a docblock reads
// as a type and a declaration as a class's name, and returns how PHP
// writes the type in a declaration: integer, double and boolean are int,
// float and bool; scalar, numeric and number are int|float|string|bool,
// int|float|string and int|float; noreturn is never. resource is a type
// PHP cannot declare, and its spelling is "". PHP warns of some of them in
// lower case, and PHPStan reports every one in any case as a class that
// does not exist (class.notFound).
func ConfusableTypeName(name string) (string, bool) {
	switch strings.ToLower(name) {
	case "integer":
		return "int", true
	case "double":
		return "float", true
	case "boolean":
		return "bool", true
	case "scalar":
		return "int|float|string|bool", true
	case "numeric":
		return "int|float|string", true
	case "number":
		return "int|float", true
	case "noreturn":
		return "never", true
	case "resource":
		return "", true
	}
	return "", false
}

// NonClassTypeName reports a name that a type position reads as a type
// which can be no class's, where a `throws` clause wants a class
// (design/functions.md §8): a name PHP reserves, `string` or `void`, or
// Phaenon's `map` and `bytes` in lower case (roadmap D30). `Map`, `Bytes`
// and every other name are class names, `Resource` and `Number` included,
// which PHP accepts as a class's; so are `map` and `bytes` where a class
// of that name resolves, which the caller looks up.
func NonClassTypeName(name string) bool {
	lower := strings.ToLower(name)
	return reservedTypeNames[lower] || (lower == "map" || lower == "bytes") && name == lower
}

// declaredKey is the key an array declared with the key type t holds, as
// PHP stores it and PHPStan reads it: a string PHP reads as an integer is
// that int (types.StoredKey), a bool is 0 or 1, a float an int and null
// the empty string, and a type no key can be, an object or an array, is
// int|string. A map key outside int|string is the checker's one finding
// (P2518); what the declaration holds is an array still, so that nothing
// else follows from it, count() of it or a loop over it.
func declaredKey(t types.Type) types.Type {
	return types.StoredKey(types.Map(t, func(m types.Type) types.Type {
		switch m := m.(type) {
		case types.Bool:
			return types.MakeUnion(types.IntLit(0), types.IntLit(1))
		case types.BoolLit:
			if m.V {
				return types.IntLit(1)
			}
			return types.IntLit(0)
		case types.Float, types.FloatLit:
			return types.TInt
		case types.Null:
			return types.StringLit{V: ""}
		case types.Object, types.Class, types.Static, types.This, types.EnumCase, types.ObjectShape, types.Array, types.Shape:
			return types.TArrayKey
		}
		return m
	}))
}

// keyword resolves a native or docblock type keyword.
func (r *resolver) keyword(lower string, args []types.Type, node *docblock.TypeNode) (types.Type, bool) {
	arg := func(i int, def types.Type) types.Type {
		if i < len(args) && args[i] != nil {
			return args[i]
		}
		return def
	}
	switch lower {
	case "int", "integer":
		if len(args) == 2 && node != nil {
			return intRange(node), true
		}
		return types.TInt, true
	case "float", "double":
		return types.TFloat, true
	case "string":
		return types.TString, true
	case "bytes":
		return types.TBytes, true
	case "bool", "boolean":
		return types.TBool, true
	case "true":
		return types.TTrue, true
	case "false":
		return types.TFalse, true
	case "null":
		return types.TNull, true
	case "mixed":
		return types.TMixed, true
	case "void":
		return types.TVoid, true
	case "never", "never-return", "never-returns", "no-return", "noreturn":
		return types.TNever, true
	case "object":
		return types.TObject, true
	case "resource", "closed-resource", "open-resource":
		return types.TResource, true
	case "scalar":
		return types.TScalar, true
	case "numeric":
		return types.TNumeric, true
	case "number":
		return types.MakeUnion(types.TInt, types.TFloat), true
	case "array-key":
		return types.TArrayKey, true
	case "callable":
		return types.TCallable, true
	case "pure-callable":
		return types.Callable{Pure: true}, true
	case "closure", "pure-closure":
		return types.TClosure, true
	case "iterable":
		switch len(args) {
		case 1:
			return types.Iterable{Key: types.TMixed, Value: arg(0, types.TMixed)}, true
		case 2:
			return types.Iterable{Key: arg(0, types.TMixed), Value: arg(1, types.TMixed)}, true
		}
		return types.TIterable, true
	case "array", "non-empty-array", "callable-array":
		a := types.Array{Key: types.TArrayKey, Value: types.TMixed, NonEmpty: lower == "non-empty-array"}
		switch len(args) {
		case 1:
			a.Value = arg(0, types.TMixed)
		case 2:
			// array<numeric-string, V> holds int|numeric-string keys, as
			// PHPStan reads it: PHP stores a decimal string as an int.
			a.Key, a.Value = declaredKey(arg(0, types.TArrayKey)), arg(1, types.TMixed)
		}
		return a, true
	case "list", "non-empty-list":
		return types.Array{Key: types.TInt, Value: arg(0, types.TMixed), List: true, NonEmpty: lower == "non-empty-list"}, true
	case "map", "non-empty-map":
		// Phaenon's name for array<K, V>: a keyed array.
		return types.Array{Key: declaredKey(arg(0, types.TArrayKey)), Value: arg(1, types.TMixed), NonEmpty: lower == "non-empty-map"}, true
	case "non-empty-string":
		return types.String{NonEmpty: true}, true
	case "numeric-string":
		return types.String{Numeric: true}, true
	case "decimal-int-string":
		return types.TDecimalIntString, true
	case "literal-string", "non-empty-literal-string":
		return types.String{Literal: true, NonEmpty: lower == "non-empty-literal-string"}, true
	case "callable-string":
		return types.String{Callable: true}, true
	case "lowercase-string", "non-empty-lowercase-string":
		return types.String{Lowercase: true, NonEmpty: lower == "non-empty-lowercase-string"}, true
	case "truthy-string", "non-falsy-string":
		return types.String{NonEmpty: true}, true
	case "class":
		// Phaenon's class<T>, which a .phn docblock may write as the
		// signature does (design/collections.md §4); the emitter spells it
		// class-string<T> for PHPStan.
		if len(args) == 1 {
			return types.ClassString{Of: args[0]}, true
		}
	case "class-string", "interface-string", "enum-string", "trait-string":
		if len(args) == 1 {
			return types.ClassString{Of: args[0]}, true
		}
		return types.ClassString{}, true
	case "positive-int":
		return types.Int{Lo: 1, Hi: math.MaxInt64}, true
	case "negative-int":
		return types.Int{Lo: math.MinInt64, Hi: -1}, true
	case "non-negative-int":
		return types.Int{Lo: 0, Hi: math.MaxInt64}, true
	case "non-positive-int":
		return types.Int{Lo: math.MinInt64, Hi: 0}, true
	case "int-mask", "int-mask-of":
		return types.TInt, true
	case "key-of":
		return types.TArrayKey, true
	case "value-of":
		return types.TMixed, true
	case "static":
		return r.classType("static", args), true
	case "self":
		return r.classType("self", args), true
	case "parent":
		return r.classType("parent", nil), true
	case "$this":
		if r.class != nil {
			return types.This{Of: &types.Class{Name: r.class.Name, Args: r.selfArgs()}}, true
		}
		return types.TObject, true
	}
	return nil, false
}

func intRange(n *docblock.TypeNode) types.Type {
	bound := func(a *docblock.TypeNode, def int64) int64 {
		switch a.Kind {
		case docblock.KindIntLit:
			return a.Int
		case docblock.KindNamed:
			switch strings.ToLower(a.Name) {
			case "min":
				return math.MinInt64
			case "max":
				return math.MaxInt64
			}
		}
		return def
	}
	return types.Int{Lo: bound(n.Args[0], math.MinInt64), Hi: bound(n.Args[1], math.MaxInt64)}
}

// ------------------------------------------------------------------ docblock types

func (r *resolver) docType(n *docblock.TypeNode) types.Type {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case docblock.KindNamed:
		lower := strings.ToLower(n.Name)
		if lower == "int" && len(n.Args) == 2 {
			return intRange(n)
		}
		args := make([]types.Type, 0, len(n.Args))
		for _, a := range n.Args {
			args = append(args, r.docType(a))
		}
		if !strings.Contains(n.Name, "\\") {
			if tp, ok := r.params[n.Name]; ok {
				return types.Param{Name: tp.Name, Bound: tp.Bound, Owner: r.owner()}
			}
			// `Map` and `Bytes` are class names, as PHPStan reads them.
			if _, otherCase := OtherCaseTypeName(n.Name); !otherCase {
				if ty, ok := r.keyword(lower, args, n); ok {
					return ty
				}
			}
			if a, ok := r.alias(n.Name); ok {
				return a
			}
		}
		if strings.EqualFold(strings.TrimPrefix(n.Name, "\\"), "Closure") {
			return types.TClosure
		}
		name := r.special(r.scope.ResolveClassString(n.Name))
		if a, ok := r.typeAlias(name, args); ok {
			return a // a .phn docblock names an alias as a signature does
		}
		return r.classType(name, args)
	case docblock.KindNullable:
		return types.Nullable(r.docType(n.Args[0]))
	case docblock.KindUnion:
		ms := make([]types.Type, 0, len(n.Args))
		for _, a := range n.Args {
			ms = append(ms, r.docType(a))
		}
		return types.MakeUnion(ms...)
	case docblock.KindIntersection:
		ms := make([]types.Type, 0, len(n.Args))
		for _, a := range n.Args {
			ms = append(ms, r.docType(a))
		}
		return types.MakeIntersection(ms...)
	case docblock.KindStringLit:
		return types.StringLit{V: n.Str}
	case docblock.KindIntLit:
		return types.IntLit(n.Int)
	case docblock.KindFloatLit:
		return types.FloatLit{V: n.Float}
	case docblock.KindBoolLit:
		return types.BoolLit{V: n.Bool}
	case docblock.KindNull:
		return types.TNull
	case docblock.KindShape:
		lower := strings.ToLower(n.Name)
		if lower == "object" {
			var entries []types.ShapeEntry
			for _, e := range n.Entries {
				if !e.HasKey || e.IsInt {
					return types.TObject // positional entries make no object
				}
				entries = append(entries, types.ShapeEntry{Key: e.Key, Type: r.docType(e.Type), Optional: e.Optional})
			}
			if len(entries) == 0 {
				return types.TObject
			}
			return types.ObjectShape{Entries: entries}
		}
		s := types.Shape{Unsealed: n.Unsealed, List: lower == "list" || lower == "non-empty-list"}
		next := int64(0)
		for _, e := range n.Entries {
			se := types.ShapeEntry{Type: r.docType(e.Type), Optional: e.Optional}
			switch {
			case !e.HasKey:
				se.IsInt, se.IntKey = true, next
				next++
			case e.IsInt:
				se.IsInt, se.IntKey = true, e.IntKey
				if e.IntKey >= next {
					next = e.IntKey + 1
				}
			default:
				// Only what PHP itself stores as an int: '7', not '07',
				// '-0' or '+1', which PHPStan keeps as strings too.
				if v, ok := types.IntKey(e.Key); ok {
					se.IsInt, se.IntKey = true, v
				} else {
					se.Key = e.Key
				}
			}
			s.Entries = append(s.Entries, se)
		}
		return s
	case docblock.KindCallable:
		lower := strings.ToLower(strings.TrimPrefix(n.Name, "\\"))
		c := types.Callable{HasSig: true, Closure: lower == "closure" || lower == "pure-closure", Pure: strings.HasPrefix(lower, "pure-")}
		for _, p := range n.Params {
			cp := types.CallableParam{Name: p.Name, Optional: p.Optional, Variadic: p.Variadic, ByRef: p.ByRef}
			if p.Type != nil {
				cp.Type = r.docType(p.Type)
			} else {
				cp.Type = types.TMixed
			}
			c.Params = append(c.Params, cp)
		}
		if n.Return != nil {
			c.Return = r.docType(n.Return)
		} else {
			c.Return = types.TMixed
		}
		return c
	case docblock.KindConst:
		// Foo::BAR → the constant's type; a wildcard — Foo::*,
		// Foo::PREFIX_*, Foo::*_SUFFIX, Foo::*PART* — the union of the
		// constants it names, as PHPStan reads it. A constant's name holds
		// nothing path.Match treats specially but the `*`.
		className := r.special(r.scope.ResolveClassString(n.Name))
		if strings.Contains(n.Str, "*") {
			if c := r.p.Class(className); c != nil {
				c.ensure()
				var ms []types.Type
				for _, k := range c.Consts {
					if ok, _ := path.Match(n.Str, k.Name); ok {
						ms = append(ms, r.p.ConstantType(k))
					}
				}
				if len(ms) > 0 {
					return types.MakeUnion(ms...)
				}
			}
			return types.TMixed
		}
		if c := r.p.Class(className); c != nil {
			if k := c.FindConst(n.Str); k != nil {
				return r.p.ConstantType(k)
			}
			if cs := c.FindCase(n.Str); cs != nil {
				return types.EnumCase{Class: c.Name, Case: cs.Name}
			}
		}
		return types.TMixed
	case docblock.KindConditional:
		// Evaluated at the call site, once the arguments are known.
		c := types.Conditional{Target: r.docType(n.Target), Then: r.docType(n.Then), Else: r.docType(n.Else), Negated: n.Negated}
		if n.Subject.Kind == docblock.KindNamed && strings.HasPrefix(n.Subject.Name, "$") {
			c.ParamName = n.Subject.Name[1:]
		} else {
			c.Subject = r.docType(n.Subject)
		}
		return c
	}
	return types.TMixed
}

// alias resolves a @phpstan-type alias visible in the current class.
func (r *resolver) alias(name string) (types.Type, bool) {
	if r.aliases != nil {
		if t, ok := r.aliases[name]; ok {
			return t, true
		}
	}
	if r.aliasNodes != nil {
		if n, ok := r.aliasNodes[name]; ok {
			if r.resolving[name] {
				return types.TMixed, true
			}
			r.resolving[name] = true
			t := r.docType(n)
			delete(r.resolving, name)
			if r.aliases == nil {
				r.aliases = map[string]types.Type{}
			}
			r.aliases[name] = t
			return t, true
		}
	}
	return nil, false
}

// ------------------------------------------------------------------ merging

// effective combines a native type and a docblock type: the docblock wins
// when it refines the native type, otherwise the native type wins and a
// problem is recorded.
func (r *resolver) effective(native, doc types.Type, what string) types.Type {
	if doc == nil {
		return native
	}
	if native == nil {
		return doc
	}
	if types.IsMixed(doc) {
		return native
	}
	if types.IsSubtype(doc, native, r.p) {
		return doc
	}
	// Tolerate the common harmless disagreements: docblock `static`/`$this`
	// against native `self`/`static`, callable docblocks on Closure/callable.
	if types.IsSubtype(types.Erase(doc), types.Erase(native), r.p) {
		return doc
	}
	r.problem("%s: docblock type %s is not a subtype of the declared type %s", what, types.Print(doc), types.Print(native))
	return native
}

// ------------------------------------------------------------------ building signatures

// nativeTypeParams builds type parameters from Phaenon syntax
// (`<T of Foo = Bar, out U>`).
func nativeTypeParams(tps []*ast.TypeParam, r *resolver) []*TypeParam {
	if len(tps) == 0 {
		return nil
	}
	out := make([]*TypeParam, len(tps))
	for i, tp := range tps {
		out[i] = &TypeParam{Name: tp.Name.Name, Variance: types.Variance(tp.Variance), Native: true, Span: tp.S}
	}
	rr := r.withParams(out)
	for i, tp := range tps {
		// A bound and a default compile into @template, a docblock.
		if tp.Bound != nil {
			out[i].Bound = rr.nativeErased(tp.Bound)
			out[i].BoundText = printer.Type(tp.Bound)
		}
		if tp.Default != nil {
			out[i].Default = rr.nativeErased(tp.Default)
		}
	}
	return out
}

// ImplyMapKeyBounds gives an unbounded type parameter that a declaration
// uses as a map key the bound map's keys have: `map<K, V>` in a generic
// declaration means `K of int|string` (design/collections.md §2). Runs
// before the declaration's types are resolved, since a parameter's bound
// travels with every type that mentions it.
//
// The K of a `map<K, V>` is the one in scope where it is written. Under a
// class, a method, a function or a closure that declares a K of its own
// shadows the class's, and a class declared inside sees none of the outer
// type parameters: neither says anything about the class's K. What those
// declare is bounded when their own signature is resolved.
func ImplyMapKeyBounds(tps []*TypeParam, roots ...ast.Node) {
	if len(tps) == 0 {
		return
	}
	byName := make(map[string]*TypeParam, len(tps))
	for _, tp := range tps {
		if tp.Bound == nil {
			byName[tp.Name] = tp
		}
	}
	if len(byName) == 0 {
		return
	}
	for _, root := range roots {
		implyMapKeyBounds(byName, root)
	}
}

// implyMapKeyBounds bounds the parameters of byName used as a map key under
// root, which is the declaration that owns them or a part of it.
func implyMapKeyBounds(byName map[string]*TypeParam, root ast.Node) {
	ast.Inspect(root, func(n ast.Node) bool {
		if n != root {
			if c, ok := n.(*ast.ClassDecl); ok {
				// Another class: only the arguments of an anonymous one are
				// written in this scope.
				if c.Args != nil {
					implyMapKeyBounds(byName, c.Args)
				}
				return false
			}
			if own := declaredTypeParams(n); len(own) > 0 {
				visible := make(map[string]*TypeParam, len(byName))
				for name, tp := range byName {
					if !own[name] {
						visible[name] = tp
					}
				}
				if len(visible) < len(byName) {
					if len(visible) > 0 {
						implyMapKeyBounds(visible, n)
					}
					return false
				}
			}
		}
		t, ok := n.(*ast.NamedType)
		if !ok || len(t.TypeArgs) != 2 || len(t.Name.Parts) != 1 || !strings.EqualFold(t.Name.Parts[0], "map") {
			return true
		}
		if k, ok := t.TypeArgs[0].(*ast.NamedType); ok && len(k.Name.Parts) == 1 && k.Name.Kind == ast.NameUnqualified {
			if tp := byName[k.Name.Parts[0]]; tp != nil {
				tp.Bound = types.TArrayKey
			}
		}
		return true
	})
}

// declaredTypeParams names the type parameters a method, a function or a
// closure declares: those of its `<…>`, or else, as typeParamsOf reads
// them, its docblock's templates.
func declaredTypeParams(n ast.Node) map[string]bool {
	var native []*ast.TypeParam
	doc := ""
	switch n := n.(type) {
	case *ast.MethodDecl:
		native, doc = n.TypeParams, n.Doc
	case *ast.FunctionDecl:
		native, doc = n.TypeParams, n.Doc
	case *ast.Closure:
		native = n.TypeParams
	case *ast.ArrowFunc:
		native = n.TypeParams
	default:
		return nil
	}
	var own map[string]bool
	if len(native) > 0 {
		own = make(map[string]bool, len(native))
		for _, tp := range native {
			own[tp.Name.Name] = true
		}
		return own
	}
	if !strings.Contains(doc, "template") {
		return nil
	}
	if d := parseDoc(doc); d != nil && len(d.Templates) > 0 {
		own = make(map[string]bool, len(d.Templates))
		for _, t := range d.Templates {
			own[t.Name] = true
		}
	}
	return own
}

// signatureNodes lists the type expressions of a parameter list and a
// return type as walkable nodes.
func signatureNodes(params []*ast.Param, ret ast.TypeExpr) []ast.Node {
	var out []ast.Node
	for _, p := range params {
		if p.Type != nil {
			out = append(out, p.Type)
		}
	}
	if ret != nil {
		out = append(out, ret)
	}
	return out
}

// typeParamsOf prefers Phaenon syntax over docblock templates.
func typeParamsOf(native []*ast.TypeParam, doc *docblock.Doc, r *resolver) []*TypeParam {
	if len(native) > 0 {
		return nativeTypeParams(native, r)
	}
	return templatesOf(doc, r)
}

func templatesOf(doc *docblock.Doc, r *resolver) []*TypeParam {
	if doc == nil || len(doc.Templates) == 0 {
		return nil
	}
	tps := make([]*TypeParam, 0, len(doc.Templates))
	for _, t := range doc.Templates {
		tp := &TypeParam{Name: t.Name, Variance: types.Variance(t.Variance)}
		tps = append(tps, tp)
	}
	// Bounds may reference other parameters; resolve with all names known.
	rr := r.withParams(tps)
	for i, t := range doc.Templates {
		if t.Bound != nil {
			tps[i].Bound = rr.docType(t.Bound)
			tps[i].BoundText = t.Bound.String()
		}
		if t.Default != nil {
			tps[i].Default = rr.docType(t.Default)
		}
	}
	return tps
}

func parseDoc(s string) *docblock.Doc {
	if s == "" {
		return nil
	}
	return docblock.Parse(s)
}

func (r *resolver) reportDocErrors(doc *docblock.Doc) {
	if doc == nil {
		return
	}
	for _, e := range doc.Errors {
		r.problem("docblock %s: %s", e.Tag, e.Msg)
	}
}

// buildParams converts AST parameters with their docblock types.
func (r *resolver) buildParams(ps []*ast.Param, doc *docblock.Doc) []*Param {
	out := make([]*Param, 0, len(ps))
	for _, ap := range ps {
		p := &Param{
			Name:       ap.Var.Name,
			NativeType: r.native(ap.Type),
			HasDefault: ap.Default != nil,
			Default:    ap.Default,
			ByRef:      ap.ByRef,
			Variadic:   ap.Variadic,
			Promoted:   ap.Modifiers != 0,
			Modifiers:  ap.Modifiers,
			Attrs:      ap.Attrs,
			Span:       ap.S,
		}
		for _, g := range ap.Attrs {
			for _, a := range g.Attrs {
				p.Sensitive = p.Sensitive || strings.EqualFold(strings.TrimPrefix(r.resolveClassName(a.Name), "\\"), "SensitiveParameter")
			}
		}
		var docT types.Type
		if doc != nil {
			for _, pt := range doc.Params {
				if pt.Name == p.Name {
					docT = r.docType(pt.Type)
					if pt.Variadic && docT != nil {
						// `@param T ...$xs` describes one element.
					}
				}
			}
			for _, pt := range doc.ParamOut {
				if pt.Name == p.Name {
					p.Out = r.docType(pt.Type)
				}
			}
		}
		if docT == nil && ap.Doc != "" {
			// `/** @var T */` on a promoted property types the parameter too.
			for _, vt := range docblock.Parse(ap.Doc).Vars {
				if vt.Name == "" || vt.Name == p.Name {
					docT = r.docType(vt.Type)
				}
			}
		}
		native := p.NativeType
		if p.Variadic && native != nil {
			// The native variadic type describes one element too.
		}
		p.Type = r.effective(native, docT, "parameter $"+p.Name)
		if p.Type == nil {
			p.Type = types.TMixed
		}
		// `= null` makes the type implicitly nullable (PHP's legacy rule), and
		// a docblock saying `string` on a `?string $x = null` does not take
		// the null away (PHPStan adds it back from the default).
		if ap.Default != nil && !types.IsNullable(p.Type) && isNullLiteral(ap.Default) {
			p.Type = types.Nullable(p.Type)
		}
		out = append(out, p)
	}
	return out
}

// isNullLiteral reports the constant null, which the parser leaves as a
// bare name.
func isNullLiteral(e ast.Expr) bool {
	var n *ast.Name
	switch e := e.(type) {
	case *ast.Name:
		n = e
	case *ast.ConstFetch:
		n = e.Name
	default:
		return false
	}
	return len(n.Parts) == 1 && strings.EqualFold(n.Parts[0], "null")
}

func (r *resolver) buildReturn(native ast.TypeExpr, doc *docblock.Doc) types.Type {
	n := r.native(native)
	var d types.Type
	if doc != nil && doc.Return != nil {
		d = r.docType(doc.Return)
	}
	return r.effective(n, d, "return")
}

// buildThrows reads what a declaration says it lets escape
// (design/functions.md §4, tooling.md §9.1): the `throws` clause, else the
// docblock's @throws tags joined into one union. nil when neither is
// written; `@throws void`, "throws nothing", is types.TVoid, and so is
// `nothrow`, its clause spelling (§4.5). A clause's member is read as a
// `catch` reads its class (throwsMember); one that names no class is
// left out, and a clause left with none declares nothing.
func (r *resolver) buildThrows(clause *ast.Throws, doc *docblock.Doc) types.Type {
	var ms []types.Type
	switch {
	case clause != nil && clause.Nothrow:
		return types.TVoid
	case clause != nil:
		for _, t := range clause.Types {
			if m := r.throwsMember(t); m != nil {
				ms = append(ms, m)
			}
		}
		if len(ms) == 0 {
			return nil
		}
	case doc != nil && len(doc.Throws) > 0:
		for _, t := range doc.Throws {
			ms = append(ms, r.docType(t))
		}
	default:
		return nil
	}
	return types.MakeUnion(ms...)
}

// throwsMember reads a member of a `throws` clause as PHP reads the class
// of a `catch` (design/functions.md §8): a type parameter, or a class name,
// `self` and `parent` included, resolved through the file's imports and
// never as a type alias, which is erased and names no class. What names no
// class is nil: a type keyword, `string` or `void`, which the checker
// reports (P2319), and `self` or `parent` where there is none (P2012,
// P2011), so that no finding follows from the declaration they break.
// `throws void` is not `nothrow`. An alias or an unknown name is the class
// it would be, which does not exist (P2001).
func (r *resolver) throwsMember(t *ast.NamedType) types.Type {
	name := r.scope.ResolveClass(t.Name)
	if len(t.Name.Parts) == 1 && t.Name.Kind == ast.NameUnqualified {
		if tp, ok := r.params[t.Name.Parts[0]]; ok {
			return types.Param{Name: tp.Name, Bound: tp.Bound, Owner: r.owner()}
		}
		if NonClassTypeName(t.Name.Parts[0]) && r.p.Class(name) == nil {
			return nil
		}
	}
	switch c := r.classType(name, nil).(type) {
	case types.Class, types.Static:
		return c
	}
	return nil
}

// ensure builds a function's signature.
func (fn *Function) ensure() {
	if fn.ensured != 0 {
		return
	}
	fn.ensured = 1
	p := fn.File.program
	r := p.newResolver(fn.File, fn.Scope, nil, fn.Span)
	fn.Doc = parseDoc(fn.Decl.Doc)
	if fn.File.Checked {
		r.reportDocErrors(fn.Doc)
	}
	fn.TypeParams = typeParamsOf(fn.Decl.TypeParams, fn.Doc, r)
	ImplyMapKeyBounds(fn.TypeParams, signatureNodes(fn.Decl.Params, fn.Decl.ReturnType)...)
	r = r.withParams(fn.TypeParams)
	fn.Params = r.buildParams(fn.Decl.Params, fn.Doc)
	fn.NativeReturn = r.native(fn.Decl.ReturnType)
	fn.Return = r.buildReturn(fn.Decl.ReturnType, fn.Doc)
	fn.Throws = r.buildThrows(fn.Decl.Throws, fn.Doc)
	if fn.Doc != nil {
		fn.Asserts = fn.Doc.Asserts
	}
	var stubAttrs []*ast.AttrGroup
	if fn.replaced != nil {
		stubAttrs = fn.replaced.Attrs
	}
	fn.Deprecated, fn.DeprecatedNote = deprecation(fn.Scope, fn.Doc, fn.Decl.Attrs, stubAttrs)
	fn.ensured = 2
}

// Signature returns the function with its types built.
func (fn *Function) Signature() *Function {
	fn.ensure()
	return fn
}

// ConstantType returns the type of a constant: its declared type, or the
// type of its value as computed by the checker's evaluator.
func (p *Program) ConstantType(c *Constant) types.Type {
	if c.Type != nil {
		// `const int VERSION = 1` or `const array M = [...]` says less than
		// the value: the value's type is returned when it fits the
		// declaration (a default `= self::VERSION` is then the literal 1).
		if c.evaled == 2 && c.evalType != nil && types.IsSubtype(c.evalType, c.Type, p) {
			return c.evalType
		}
		if c.evaled != 0 || p.ConstType == nil {
			return c.Type
		}
		c.evaled = 1
		c.evalType = p.ConstType(c)
		c.evaled = 2
		if c.evalType != nil && !types.IsMixed(c.evalType) && types.IsSubtype(c.evalType, c.Type, p) {
			return c.evalType
		}
		return c.Type
	}
	if c.evaled == 2 {
		return c.evalType
	}
	if c.evaled == 1 || p.ConstType == nil {
		return types.TMixed
	}
	c.evaled = 1
	c.evalType = p.ConstType(c)
	c.evaled = 2
	return c.evalType
}
