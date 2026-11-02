package program

import (
	"fmt"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Visibility of a member.
type Visibility uint8

const (
	Public Visibility = iota
	Protected
	Private
)

func (v Visibility) String() string {
	switch v {
	case Protected:
		return "protected"
	case Private:
		return "private"
	}
	return "public"
}

func visibilityOf(m ast.Modifier) Visibility {
	switch {
	case m&ast.ModPrivate != 0:
		return Private
	case m&ast.ModProtected != 0:
		return Protected
	}
	return Public
}

func setVisibilityOf(m ast.Modifier, get Visibility) Visibility {
	switch {
	case m&ast.ModPrivateSet != 0:
		return Private
	case m&ast.ModProtectedSet != 0:
		return Protected
	case m&ast.ModPublicSet != 0:
		return Public
	}
	return get
}

// TypeParam is a declared type parameter (docblock @template in M2,
// syntax in M3).
type TypeParam struct {
	Name     string
	Bound    types.Type // nil = mixed
	Default  types.Type
	Variance types.Variance
	// Native marks a parameter declared in Phaenon syntax (`class Foo<T>`)
	// rather than in a docblock: the emitter erases it and the checker
	// applies the Phaenon rules (variance positions, static members, …).
	Native bool
	Span   ast.Span
}

// FileAlias is a file-level Phaenon type alias: `type Name<T> = Type;`.
type FileAlias struct {
	Decl   *ast.TypeAlias
	File   *File
	Scope  *Scope
	Params []*TypeParam
	Type   types.Type
	state  uint8 // 0 = raw, 1 = resolving, 2 = done
}

// Class is a class, interface, trait or enum.
type Class struct {
	Name      string // fully qualified, declared case
	Kind      ast.ClassKind
	Abstract  bool
	Final     bool
	Readonly  bool
	Anonymous bool
	// Parent is the fully-qualified parent class ("" if none); Interfaces
	// are the implemented (or, for interfaces, extended) interfaces.
	Parent     string
	Interfaces []string
	// ParentArgs / InterfaceArgs are the type arguments passed up the
	// hierarchy (@extends Base<T>, @implements I<T>), keyed by lower name.
	ParentArgs    []types.Type
	InterfaceArgs map[string][]types.Type
	TypeParams    []*TypeParam
	Aliases       map[string]types.Type // @phpstan-type / @phpstan-import-type
	Traits        []string
	traitUses     []*ast.TraitUse
	Consts        []*Constant
	Props         []*Property
	Methods       []*Method
	Cases         []*EnumCase
	BackingType   types.Type
	Attrs         []*ast.AttrGroup
	Doc           *docblock.Doc
	Deprecated    bool
	Internal      bool
	File          *File
	Scope         *Scope
	Decl          *ast.ClassDecl
	Span          ast.Span

	consts  map[string]*Constant
	props   map[string]*Property
	methods map[string]*Method
	cases   map[string]*EnumCase
	ensured uint8 // 0 = raw, 1 = in progress, 2 = done
}

// Function is a named function.
type Function struct {
	Name       string // fully qualified
	Params     []*Param
	Return     types.Type
	ByRef      bool
	TypeParams []*TypeParam
	Doc        *docblock.Doc
	Deprecated bool
	// DeprecatedNote is what PHP adds to "… is deprecated" from the
	// #[\Deprecated] attribute: " since 8.6, use is_float() instead".
	DeprecatedNote string
	Asserts        []docblock.AssertTag
	File           *File
	Scope          *Scope
	Decl           *ast.FunctionDecl
	// replaced is the stub declaration an overlay function replaces, whose
	// #[\Deprecated] the overlay need not repeat.
	replaced *ast.FunctionDecl
	// Polyfilled marks a stub function that project or vendor code declares
	// too (a polyfill for older PHP versions): the stub's types win, and its
	// absence from an older target is not reported.
	Polyfilled bool
	Span       ast.Span
	ensured    uint8
}

// Method is a method of a class (including those copied in from traits).
type Method struct {
	Name      string
	Class     *Class // the class whose table this entry belongs to
	FromTrait *Class // the trait it was copied from, if any
	Vis       Visibility
	Static    bool
	Abstract  bool
	Final     bool
	ByRef     bool
	Params    []*Param
	Return    types.Type
	// NativeReturn is the declared PHP return type (nil when absent), kept
	// apart from Return so docblock inheritance can tell them apart.
	NativeReturn types.Type
	TypeParams   []*TypeParam
	Doc          *docblock.Doc
	Deprecated   bool
	// DeprecatedNote: see Function.
	DeprecatedNote string
	Asserts        []docblock.AssertTag
	SelfOut        types.Type
	Attrs          []*ast.AttrGroup
	Decl           *ast.MethodDecl
	Span           ast.Span
	// Magic marks a @method tag entry (no body, no Decl).
	Magic bool
}

// Param is one parameter.
type Param struct {
	Name       string
	Type       types.Type // effective (docblock-refined) type
	NativeType types.Type
	HasDefault bool
	Default    ast.Expr
	ByRef      bool
	Variadic   bool
	Promoted   bool
	Modifiers  ast.Modifier
	Attrs      []*ast.AttrGroup
	Out        types.Type // @param-out
	Span       ast.Span
}

// Property is a property (declared, promoted, or from a trait).
type Property struct {
	Name       string
	Class      *Class
	FromTrait  *Class
	Vis        Visibility
	SetVis     Visibility
	Static     bool
	Readonly   bool
	Type       types.Type // effective type; mixed when untyped
	NativeType types.Type // nil when untyped
	HasDefault bool
	Default    ast.Expr
	Hooks      []*ast.PropertyHook
	Virtual    bool // hooked property with no backing storage
	Promoted   bool
	Doc        *docblock.Doc
	Attrs      []*ast.AttrGroup
	Deprecated bool
	Magic      bool // @property tag
	Span       ast.Span
	File       *File
}

// Constant is a global or class constant.
type Constant struct {
	Name  string // for class constants the bare name; for globals the FQN
	Class *Class // nil for a global constant
	Vis   Visibility
	Final bool
	Type  types.Type // declared (native/docblock) type, nil = infer from Value
	Doc   bool       // Type comes from a @var tag, which says more than the value
	Value ast.Expr
	File  *File
	Scope *Scope
	Span  ast.Span
	// Deprecated, DeprecatedNote and Polyfilled (of a global constant):
	// see Function.
	Deprecated     bool
	DeprecatedNote string
	Polyfilled     bool
	// Attrs are the attribute groups of a class constant's declaration.
	Attrs    []*ast.AttrGroup
	evalType types.Type
	evaled   uint8
}

// EnumCase is one case of an enum.
type EnumCase struct {
	Name  string
	Class *Class
	Value ast.Expr
	Span  ast.Span
	Attrs []*ast.AttrGroup
	// Deprecated and DeprecatedNote: see Function.
	Deprecated     bool
	DeprecatedNote string
}

// ------------------------------------------------------------------ collection

// collect walks the top-level statements of a file, records namespace
// scopes and declares classes, functions and constants. Conditional
// declarations (`if (!function_exists(...)) { function ... }`) are
// found by descending into blocks.
func (p *Program) collect(f *File) {
	scope := newScope("")
	start := 0
	var flush func(end int)
	flush = func(end int) {
		if end > start {
			f.Scopes = append(f.Scopes, ScopedRange{Span: ast.Span{Start: start, End: end}, Scope: scope})
		}
	}
	var visit func(stmts []ast.Stmt)
	visit = func(stmts []ast.Stmt) {
		for _, s := range stmts {
			switch s := s.(type) {
			case *ast.Namespace:
				flush(s.S.Start)
				ns := ""
				if s.Name != nil {
					ns = strings.Join(s.Name.Parts, "\\")
				}
				scope = newScope(ns)
				start = s.S.Start
				if s.Braced {
					visit(s.Body)
					flush(s.S.End)
					scope = newScope("")
					start = s.S.End
				}
			case *ast.Use:
				for _, it := range s.Items {
					fqn := strings.Join(it.Name.Parts, "\\")
					if s.Prefix != nil {
						fqn = strings.Join(s.Prefix.Parts, "\\") + "\\" + fqn
					}
					kind := s.Kind
					if it.Kind != ast.UseClass {
						kind = it.Kind
					}
					alias := ""
					if it.Alias != nil {
						alias = it.Alias.Name
					}
					scope.addUse(kind, fqn, alias)
				}
			case *ast.ClassDecl:
				p.declareClass(f, scope, s)
			case *ast.FunctionDecl:
				p.declareFunction(f, scope, s)
			case *ast.TypeAlias:
				if scope.aliases == nil {
					scope.aliases = map[string]*FileAlias{}
				}
				if _, dup := scope.aliases[s.Name.Name]; !dup {
					scope.aliases[s.Name.Name] = &FileAlias{Decl: s, File: f, Scope: scope}
				}
			case *ast.ConstDecl:
				// `/** @var int */ const X = …;` (the stubs declare most
				// constants this way, with an UNKNOWN value).
				var docT types.Type
				doc := parseDoc(s.Doc)
				if doc != nil && len(doc.Vars) > 0 {
					docT = p.newResolver(f, scope, nil, s.S).docType(doc.Vars[0].Type)
				}
				deprecated, note := deprecation(scope, doc, s.Attrs)
				for _, c := range s.Consts {
					k := p.declareConstant(f, scope, scope.prefix()+c.Name.Name, c.Value, c.S)
					if k != nil && k.Type == nil {
						k.Type = docT
						k.Doc = docT != nil
					}
					if k != nil {
						k.Deprecated, k.DeprecatedNote = deprecated, note
					}
				}
			case *ast.ExprStmt:
				// define('NAME', value) and class_alias(New::class, Old::class) at the top level.
				if call, ok := s.Expr.(*ast.Call); ok {
					if n, ok := call.Callee.(*ast.Name); ok && len(n.Parts) == 1 && call.Args != nil && len(call.Args.Args) >= 2 {
						switch strings.ToLower(n.Parts[0]) {
						case "define":
							if lit, ok := call.Args.Args[0].Value.(*ast.StringLit); ok {
								p.declareConstant(f, scope, strings.TrimPrefix(StringValue(lit), "\\"), call.Args.Args[1].Value, s.S)
							}
						case "class_alias":
							r := p.newResolver(f, scope, nil, s.S)
							original, ok1 := classNameArg(call.Args.Args[0].Value, r)
							alias, ok2 := classNameArg(call.Args.Args[1].Value, r)
							if ok1 && ok2 {
								p.classAliases[strings.ToLower(alias)] = original
							}
						}
					}
				}
			case *ast.If:
				visitStmt(s.Then, visit)
				for _, e := range s.ElseIfs {
					visitStmt(e.Body, visit)
				}
				if s.Else != nil {
					visitStmt(s.Else, visit)
				}
			case *ast.Block:
				visit(s.Stmts)
			case *ast.Declare:
				if s.Body != nil {
					visitStmt(s.Body, visit)
				}
			}
		}
	}
	visit(f.AST.Stmts)
	flush(len(f.Src))
}

func visitStmt(s ast.Stmt, visit func([]ast.Stmt)) {
	if b, ok := s.(*ast.Block); ok {
		visit(b.Stmts)
		return
	}
	visit([]ast.Stmt{s})
}

func (p *Program) declareClass(f *File, scope *Scope, d *ast.ClassDecl) *Class {
	c := &Class{
		Kind:     d.Kind,
		Abstract: d.Modifiers&ast.ModAbstract != 0,
		Final:    d.Modifiers&ast.ModFinal != 0,
		Readonly: d.Modifiers&ast.ModReadonly != 0,
		Attrs:    d.Attrs,
		File:     f,
		Scope:    scope,
		Decl:     d,
		Span:     d.S,
	}
	if d.Name == nil {
		p.anon++
		line, _ := f.Position(d.S.Start)
		c.Name = fmt.Sprintf("class@anonymous%s:%d#%d", filepathBase(f.Path), line, p.anon)
		c.Anonymous = true
		c.Final = true
	} else {
		c.Name = scope.prefix() + d.Name.Name
	}
	key := strings.ToLower(c.Name)
	if p.override && !c.Anonymous {
		// The overlay never declares classes: it patches the class of the
		// same name, whether a stub already declared it or vendor code
		// declares it later. Its docblocks and the methods it lists replace
		// theirs; everything else stays.
		p.patches[key] = d
		if existing, exists := p.classes[key]; exists {
			return existing
		}
		return c
	}
	if existing, exists := p.classes[key]; exists && !c.Anonymous {
		return existing
	}
	p.classes[key] = c
	return c
}

func filepathBase(path string) string {
	if i := strings.LastIndexAny(path, "/\\"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// DeclareAnonymous registers an anonymous class found inside a body.
func (p *Program) DeclareAnonymous(f *File, scope *Scope, d *ast.ClassDecl) *Class {
	return p.declareClass(f, scope, d)
}

func (p *Program) declareFunction(f *File, scope *Scope, d *ast.FunctionDecl) *Function {
	name := scope.prefix() + d.Name.Name
	key := strings.ToLower(name)
	existing, exists := p.functions[key]
	if exists && !p.override {
		if existing.File.IsStub() && !f.IsStub() {
			existing.Polyfilled = true // `if (!function_exists('clamp')) { function clamp(…) }`
		}
		return existing
	}
	fn := &Function{Name: name, File: f, Scope: scope, Decl: d, Span: d.S, ByRef: d.ByRef}
	if exists {
		fn.replaced = existing.Decl
	}
	p.functions[key] = fn
	return fn
}

// deprecation reads a declaration's #[\Deprecated] attribute (PHP 8.4) and
// its @deprecated tag. note is what PHP's own message adds after "… is
// deprecated" from the attribute's arguments: " since 8.6, use is_float()
// instead" for #[\Deprecated(message: "use is_float() instead", since:
// "8.6")].
func deprecation(scope *Scope, doc *docblock.Doc, groups ...[]*ast.AttrGroup) (deprecated bool, note string) {
	for _, attrs := range groups {
		for _, g := range attrs {
			for _, a := range g.Attrs {
				if scope == nil || !strings.EqualFold(scope.ResolveClass(a.Name), "Deprecated") {
					continue
				}
				var message, since string
				if a.Args != nil {
					for i, arg := range a.Args.Args {
						lit, ok := arg.Value.(*ast.StringLit)
						if !ok {
							continue
						}
						switch {
						case arg.Name != nil && arg.Name.Name == "message", arg.Name == nil && i == 0:
							message = StringValue(lit)
						case arg.Name != nil && arg.Name.Name == "since", arg.Name == nil && i == 1:
							since = StringValue(lit)
						}
					}
				}
				if since != "" {
					note += " since " + since
				}
				if message != "" {
					note += ", " + message
				}
				return true, note
			}
		}
	}
	return doc != nil && doc.Deprecated, ""
}

// DeclareFunction registers a function declared inside a body (the
// collector only sees top-level and conditionally declared functions).
func (p *Program) DeclareFunction(f *File, scope *Scope, d *ast.FunctionDecl) *Function {
	return p.declareFunction(f, scope, d)
}

// Prefix returns the namespace prefix with a trailing backslash ("" for
// the global namespace).
func (s *Scope) Prefix() string { return s.prefix() }

func (p *Program) declareConstant(f *File, scope *Scope, fqn string, value ast.Expr, span ast.Span) *Constant {
	key := constKey(fqn)
	if existing, exists := p.constants[key]; exists && !p.override {
		if existing.File.IsStub() && !f.IsStub() {
			existing.Polyfilled = true
		}
		// php-src declares `const PHP_URL_USER = UNKNOWN` with a @var: the
		// extension loaded into php knows the value, and the value is what
		// a conditional type (`$component is int<0, max>`) decides on.
		if isUnknownValue(existing.Value) && !isUnknownValue(value) {
			existing.Value = value
			existing.evaled, existing.evalType = 0, nil
		}
		return nil
	}
	k := &Constant{Name: fqn, Value: value, File: f, Scope: scope, Span: span}
	p.constants[key] = k
	return k
}

// classNameArg reads a class name written as `Foo::class` or as a string
// literal, as class_alias() takes them.
func classNameArg(e ast.Expr, r *resolver) (string, bool) {
	switch e := e.(type) {
	case *ast.ClassConstFetch:
		id, ok := e.Name.(*ast.Ident)
		n, isName := e.Class.(*ast.Name)
		if !ok || !isName || !strings.EqualFold(id.Name, "class") {
			return "", false
		}
		return r.resolveClassName(n), true
	case *ast.StringLit:
		return strings.TrimPrefix(StringValue(e), "\\"), true
	}
	return "", false
}

// isUnknownValue reports php-src's UNKNOWN placeholder for a constant value.
func isUnknownValue(e ast.Expr) bool {
	var n *ast.Name
	switch e := e.(type) {
	case *ast.Name: // the parser leaves a bare constant as a name
		n = e
	case *ast.ConstFetch:
		n = e.Name
	default:
		return false
	}
	return len(n.Parts) == 1 && n.Parts[0] == "UNKNOWN"
}

// StringValue returns the value of a constant string literal (quotes
// removed, escapes of single-quoted strings processed; double-quoted
// strings get the common escapes).
func StringValue(lit *ast.StringLit) string {
	raw := lit.Raw
	if len(raw) >= 2 && (raw[0] == '\'' || raw[0] == '"') {
		q := raw[0]
		body := raw[1 : len(raw)-1]
		if q == '\'' {
			return strings.NewReplacer(`\\`, `\`, `\'`, `'`).Replace(body)
		}
		return strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\n`, "\n", `\t`, "\t", `\r`, "\r", `\$`, "$").Replace(body)
	}
	if len(raw) >= 3 && (raw[0] == 'b' || raw[0] == 'B') {
		return StringValue(&ast.StringLit{Raw: raw[1:]})
	}
	// heredoc / nowdoc: strip the first line and the closing label line.
	if strings.HasPrefix(raw, "<<<") {
		nl := strings.IndexByte(raw, '\n')
		if nl < 0 {
			return ""
		}
		body := raw[nl+1:]
		if last := strings.LastIndexByte(body, '\n'); last >= 0 {
			body = body[:last]
		} else {
			body = ""
		}
		return body
	}
	return raw
}
