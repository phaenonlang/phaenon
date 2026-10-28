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
	Reified  bool
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
	Asserts    []docblock.AssertTag
	File       *File
	Scope      *Scope
	Decl       *ast.FunctionDecl
	Span       ast.Span
	ensured    uint8
}

// Method is a method of a class (including those copied in from traits).
type Method struct {
	Name       string
	Class      *Class // the class whose table this entry belongs to
	FromTrait  *Class // the trait it was copied from, if any
	Vis        Visibility
	Static     bool
	Abstract   bool
	Final      bool
	ByRef      bool
	Params     []*Param
	Return     types.Type
	TypeParams []*TypeParam
	Doc        *docblock.Doc
	Deprecated bool
	Asserts    []docblock.AssertTag
	SelfOut    types.Type
	Attrs      []*ast.AttrGroup
	Decl       *ast.MethodDecl
	Span       ast.Span
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
	Name     string // for class constants the bare name; for globals the FQN
	Class    *Class // nil for a global constant
	Vis      Visibility
	Final    bool
	Type     types.Type // declared (native/docblock) type, nil = infer from Value
	Value    ast.Expr
	File     *File
	Scope    *Scope
	Span     ast.Span
	evalType types.Type
	evaled   uint8
}

// EnumCase is one case of an enum.
type EnumCase struct {
	Name  string
	Class *Class
	Value ast.Expr
	Span  ast.Span
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
			case *ast.ConstDecl:
				for _, c := range s.Consts {
					p.declareConstant(f, scope, scope.prefix()+c.Name.Name, c.Value, c.S)
				}
			case *ast.ExprStmt:
				// define('NAME', value) at the top level.
				if call, ok := s.Expr.(*ast.Call); ok {
					if n, ok := call.Callee.(*ast.Name); ok && len(n.Parts) == 1 && strings.EqualFold(n.Parts[0], "define") &&
						call.Args != nil && len(call.Args.Args) >= 2 {
						if lit, ok := call.Args.Args[0].Value.(*ast.StringLit); ok {
							p.declareConstant(f, scope, strings.TrimPrefix(StringValue(lit), "\\"), call.Args.Args[1].Value, s.S)
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
	if _, exists := p.classes[key]; exists && !p.override && !c.Anonymous {
		return p.classes[key]
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

func (p *Program) declareFunction(f *File, scope *Scope, d *ast.FunctionDecl) {
	name := scope.prefix() + d.Name.Name
	key := strings.ToLower(name)
	if _, exists := p.functions[key]; exists && !p.override {
		return
	}
	p.functions[key] = &Function{Name: name, File: f, Scope: scope, Decl: d, Span: d.S, ByRef: d.ByRef}
}

func (p *Program) declareConstant(f *File, scope *Scope, fqn string, value ast.Expr, span ast.Span) {
	key := constKey(fqn)
	if _, exists := p.constants[key]; exists && !p.override {
		return
	}
	p.constants[key] = &Constant{Name: fqn, Value: value, File: f, Scope: scope, Span: span}
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
