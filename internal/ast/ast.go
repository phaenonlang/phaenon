// Package ast defines the syntax tree of Phaenon (a superset of PHP 8.5).
//
// Nodes are grouped into typed families — Expr, Stmt, TypeExpr, plus the
// declaration-level helpers — so the checker and the emitter get static
// guarantees about what can appear where. Phaenon-only constructs are
// additions to the PHP tree (TypeArgs on calls, IsExpr, TypeAlias, …), never
// a separate tree: PHP-mode and Phaenon-mode share every node.
//
// Every node records its byte span in the source. Doc comments are attached
// to the declarations they precede; other trivia is not part of the tree.
package ast

import "github.com/phaenonlang/phaenon/internal/token"

// Span is a half-open byte range [Start, End) in the source file.
type Span struct{ Start, End int }

// Node is implemented by every syntax node.
type Node interface{ Span() Span }

// Expr is an expression.
type Expr interface {
	Node
	exprNode()
}

// Stmt is a statement (including declarations, which PHP treats as statements).
type Stmt interface {
	Node
	stmtNode()
}

// TypeExpr is a type in a declaration position.
type TypeExpr interface {
	Node
	typeNode()
}

// ClassMember is a member of a class, interface, trait or enum body.
type ClassMember interface {
	Node
	memberNode()
}

// File is one parsed source file.
type File struct {
	S     Span
	Stmts []Stmt
	// Mode-specific: in PHP mode the leading inline HTML / open tag are
	// ordinary statements (InlineHTML) so a template file round-trips.
}

func (f *File) Span() Span { return f.S }

// ------------------------------------------------------------------ names

// NameKind says how a name resolves.
type NameKind uint8

const (
	NameUnqualified    NameKind = iota // Foo
	NameQualified                      // Foo\Bar
	NameFullyQualified                 // \Foo\Bar
	NameRelative                       // namespace\Foo
)

// Name is a (possibly qualified) identifier in a name position: class names,
// function names, constants, `use` targets.
type Name struct {
	S    Span
	Kind NameKind
	// Parts are the segments without separators; for NameRelative the
	// leading "namespace" keyword is not a part.
	Parts []string
}

func (n *Name) Span() Span { return n.S }

// Ident is a bare identifier: member names, labels, named-argument names.
// Keywords are allowed where PHP allows them (semi_reserved).
type Ident struct {
	S    Span
	Name string
}

func (n *Ident) Span() Span { return n.S }
func (*Ident) exprNode()    {} // member names and bare string offsets ("$a[b]")

// ------------------------------------------------------------------ expressions

type (
	// Variable is $name.
	Variable struct {
		S    Span
		Name string // without the $
	}
	// VariableVariable is $$expr or ${expr}.
	VariableVariable struct {
		S      Span
		Expr   Expr
		Braced bool
	}
	// IntLit is an integer literal in its source spelling (0x1F, 1_000, 017).
	IntLit struct {
		S   Span
		Raw string
	}
	// FloatLit is a float literal in its source spelling.
	FloatLit struct {
		S   Span
		Raw string
	}
	// StringLit is a constant string: 'x', "x" without interpolation, or a
	// nowdoc / heredoc without interpolation. Raw is the whole literal
	// including quotes or heredoc delimiters.
	StringLit struct {
		S   Span
		Raw string
	}
	// InterpolatedString is "…$x…", a heredoc with interpolation, or `…`.
	InterpolatedString struct {
		S     Span
		Kind  StringKind
		Open  string // the opening delimiter as written: `"`, `b"`, "<<<EOT\n", "`"
		Close string // the closing delimiter: `"`, "  EOT", "`"
		Parts []Expr // StringPart or an expression
	}
	// StringPart is a literal segment inside an InterpolatedString, raw.
	StringPart struct {
		S   Span
		Raw string
	}
	// StringVarName is the "${name}" / "${name[expr]}" interpolation form.
	StringVarName struct {
		S     Span
		Name  string
		Index Expr // nil without [expr]
	}
	// EncapsBraced is the "{$expr}" interpolation form; Expr is the variable expression inside.
	EncapsBraced struct {
		S    Span
		Expr Expr
	}
	// ArrayLit is [..] or array(..) — also the destructuring target `[$a, $b] = …`
	// and `list($a, $b) = …`.
	ArrayLit struct {
		S     Span
		Kind  ArrayKind
		Items []*ArrayItem // nil entries mark skipped positions in list(, $b)
	}
	// ArrayItem is one element of an array literal.
	ArrayItem struct {
		S      Span
		Key    Expr // nil without =>
		Value  Expr
		ByRef  bool // => &$v
		Unpack bool // ...$v
	}
	// ConstFetch is a bare name used as an expression: FOO, null, Foo\BAR.
	ConstFetch struct {
		S    Span
		Name *Name
	}
	// MagicConst is __LINE__, __CLASS__, …
	MagicConst struct {
		S    Span
		Kind token.Kind
		Raw  string
	}
	// ClassConstFetch is Foo::BAR, Foo::class, $x::BAR, Foo::{expr}.
	ClassConstFetch struct {
		S     Span
		Class Expr // *Name (incl. static), or an expression
		Name  Expr // *Ident, or an expression for Foo::{expr}
	}
	// PropertyFetch is $a->b, $a?->b, $a->{expr}, $a->$b.
	PropertyFetch struct {
		S        Span
		Object   Expr
		Name     Expr // *Ident, *Variable, *VariableVariable or braced expression
		NullSafe bool
	}
	// StaticPropertyFetch is Foo::$bar, $x::$bar, Foo::$$bar.
	StaticPropertyFetch struct {
		S     Span
		Class Expr
		Name  Expr // *Variable or *VariableVariable
	}
	// Index is $a[i] or $a[] (nil Index, append position).
	Index struct {
		S     Span
		Array Expr
		Index Expr
	}
	// Call is f(..), $f(..), (expr)(..), "str"(..).
	Call struct {
		S        Span
		Callee   Expr // *Name for plain function calls
		TypeArgs []TypeExpr
		Args     *ArgList
	}
	// MethodCall is $a->m(..) / $a?->m(..).
	MethodCall struct {
		S        Span
		Object   Expr
		Name     Expr // *Ident, *Variable, or braced expression
		NullSafe bool
		TypeArgs []TypeExpr
		Args     *ArgList
	}
	// StaticCall is Foo::m(..), $x::m(..), Foo::$m(..).
	StaticCall struct {
		S        Span
		Class    Expr
		Name     Expr // *Ident, *Variable, *VariableVariable, or braced expression
		TypeArgs []TypeExpr
		Args     *ArgList
	}
	// ArgList is a parenthesised argument list. FirstClassCallable is f(...).
	ArgList struct {
		S                  Span
		Args               []*Arg
		FirstClassCallable bool
	}
	// Arg is one argument: positional, named, unpacked, or a PFA placeholder `?`.
	Arg struct {
		S           Span
		Name        *Ident // named argument
		Value       Expr   // nil for a placeholder
		Unpack      bool   // ...$x
		Placeholder bool   // ? (partial function application, PHP 8.5)
	}
	// New is new Foo(..), new $x, new (expr)(..), new class(..) {..}.
	New struct {
		S        Span
		Class    Expr       // *Name, variable chain, parenthesised expr; nil when Anon != nil
		Anon     *ClassDecl // anonymous class
		TypeArgs []TypeExpr
		Args     *ArgList // nil when written without parentheses
	}
	// Clone is clone $x or clone($x, [...]) (PHP 8.5 clone-with).
	Clone struct {
		S    Span
		Expr Expr     // operand for the prefix form
		Args *ArgList // for the call form; Expr is nil then
	}
	// Unary is !x, ~x, -x, +x, @x.
	Unary struct {
		S    Span
		Op   token.Kind
		Expr Expr
	}
	// Cast is (int)x etc. Op is the cast token kind; Raw its spelling.
	Cast struct {
		S    Span
		Op   token.Kind
		Raw  string
		Expr Expr
	}
	// IncDec is ++x, x++, --x, x--.
	IncDec struct {
		S      Span
		Prefix bool
		Inc    bool
		Expr   Expr
	}
	// Binary is every infix operator except assignment and ternary.
	// Coalesce (??), instanceof-free comparisons, pipe (|>) are Binary too.
	Binary struct {
		S     Span
		Op    token.Kind
		Left  Expr
		Right Expr
	}
	// Assign is target = value, target = &value, or target op= value.
	Assign struct {
		S      Span
		Op     token.Kind // '=' or a compound token like T_PLUS_EQUAL
		Target Expr
		Value  Expr
		ByRef  bool
	}
	// Ternary is c ? a : b and c ?: b (Then == nil).
	Ternary struct {
		S    Span
		Cond Expr
		Then Expr
		Else Expr
	}
	// Instanceof is x instanceof Foo / x instanceof $cls / x instanceof (expr).
	Instanceof struct {
		S     Span
		Expr  Expr
		Class Expr
	}
	// Isset is isset(a, b).
	Isset struct {
		S    Span
		Vars []Expr
	}
	// Empty is empty(x).
	Empty struct {
		S    Span
		Expr Expr
	}
	// Exit is exit / die with optional argument list.
	Exit struct {
		S    Span
		Raw  string // "exit" or "die" as written
		Args *ArgList
	}
	// Print is print x.
	Print struct {
		S    Span
		Expr Expr
	}
	// Include is include/include_once/require/require_once x.
	Include struct {
		S    Span
		Kind token.Kind
		Expr Expr
	}
	// Eval is eval(x).
	Eval struct {
		S    Span
		Expr Expr
	}
	// Closure is function(..) use(..): T {..}.
	Closure struct {
		S          Span
		Attrs      []*AttrGroup
		Static     bool
		ByRef      bool
		TypeParams []*TypeParam
		Params     []*Param
		Uses       []*ClosureUse
		ReturnType TypeExpr
		Body       []Stmt
	}
	// ClosureUse is one entry of use(..).
	ClosureUse struct {
		S     Span
		ByRef bool
		Var   *Variable
	}
	// ArrowFunc is fn(..) => expr.
	ArrowFunc struct {
		S          Span
		Attrs      []*AttrGroup
		Static     bool
		ByRef      bool
		TypeParams []*TypeParam
		Params     []*Param
		ReturnType TypeExpr
		Body       Expr
	}
	// Match is match(x) { .. }.
	Match struct {
		S       Span
		Subject Expr
		Arms    []*MatchArm
	}
	// MatchArm is conds => body; Conds == nil is default.
	MatchArm struct {
		S     Span
		Conds []Expr
		Body  Expr
	}
	// Yield is yield, yield v, yield k => v.
	Yield struct {
		S     Span
		Key   Expr
		Value Expr
	}
	// YieldFrom is yield from x.
	YieldFrom struct {
		S    Span
		Expr Expr
	}
	// Throw is throw x (an expression since PHP 8).
	Throw struct {
		S    Span
		Expr Expr
	}
	// Paren is (x). Kept so that printing preserves the author's grouping.
	Paren struct {
		S    Span
		Expr Expr
	}
	// IsExpr is Phaenon's `x is T` / `x is !T`.
	IsExpr struct {
		S    Span
		Expr Expr
		Type TypeExpr
	}
	// AsExpr is Phaenon's `x as T` (throws) / `x as? T` (null on failure).
	AsExpr struct {
		S        Span
		Expr     Expr
		Type     TypeExpr
		Nullable bool
	}
)

// StringKind distinguishes interpolated string forms.
type StringKind uint8

const (
	StringDoubleQuoted StringKind = iota
	StringHeredoc
	StringBacktick
)

// ArrayKind distinguishes array literal spellings.
type ArrayKind uint8

const (
	ArrayShort ArrayKind = iota // [..]
	ArrayLong                   // array(..)
	ArrayList                   // list(..) — destructuring only
)

func (*Variable) exprNode()            {}
func (*VariableVariable) exprNode()    {}
func (*IntLit) exprNode()              {}
func (*FloatLit) exprNode()            {}
func (*StringLit) exprNode()           {}
func (*InterpolatedString) exprNode()  {}
func (*StringPart) exprNode()          {}
func (*StringVarName) exprNode()       {}
func (*EncapsBraced) exprNode()        {}
func (*ArrayLit) exprNode()            {}
func (*ConstFetch) exprNode()          {}
func (*MagicConst) exprNode()          {}
func (*ClassConstFetch) exprNode()     {}
func (*PropertyFetch) exprNode()       {}
func (*StaticPropertyFetch) exprNode() {}
func (*Index) exprNode()               {}
func (*Call) exprNode()                {}
func (*MethodCall) exprNode()          {}
func (*StaticCall) exprNode()          {}
func (*New) exprNode()                 {}
func (*Clone) exprNode()               {}
func (*Unary) exprNode()               {}
func (*Cast) exprNode()                {}
func (*IncDec) exprNode()              {}
func (*Binary) exprNode()              {}
func (*Assign) exprNode()              {}
func (*Ternary) exprNode()             {}
func (*Instanceof) exprNode()          {}
func (*Isset) exprNode()               {}
func (*Empty) exprNode()               {}
func (*Exit) exprNode()                {}
func (*Print) exprNode()               {}
func (*Include) exprNode()             {}
func (*Eval) exprNode()                {}
func (*Closure) exprNode()             {}
func (*ArrowFunc) exprNode()           {}
func (*Match) exprNode()               {}
func (*Yield) exprNode()               {}
func (*YieldFrom) exprNode()           {}
func (*Throw) exprNode()               {}
func (*Paren) exprNode()               {}
func (*IsExpr) exprNode()              {}
func (*AsExpr) exprNode()              {}
func (*Name) exprNode()                {} // as a class reference in New/StaticCall/Instanceof

func (n *Variable) Span() Span            { return n.S }
func (n *VariableVariable) Span() Span    { return n.S }
func (n *IntLit) Span() Span              { return n.S }
func (n *FloatLit) Span() Span            { return n.S }
func (n *StringLit) Span() Span           { return n.S }
func (n *InterpolatedString) Span() Span  { return n.S }
func (n *StringPart) Span() Span          { return n.S }
func (n *StringVarName) Span() Span       { return n.S }
func (n *EncapsBraced) Span() Span        { return n.S }
func (n *ArrayLit) Span() Span            { return n.S }
func (n *ArrayItem) Span() Span           { return n.S }
func (n *ConstFetch) Span() Span          { return n.S }
func (n *MagicConst) Span() Span          { return n.S }
func (n *ClassConstFetch) Span() Span     { return n.S }
func (n *PropertyFetch) Span() Span       { return n.S }
func (n *StaticPropertyFetch) Span() Span { return n.S }
func (n *Index) Span() Span               { return n.S }
func (n *Call) Span() Span                { return n.S }
func (n *MethodCall) Span() Span          { return n.S }
func (n *StaticCall) Span() Span          { return n.S }
func (n *ArgList) Span() Span             { return n.S }
func (n *Arg) Span() Span                 { return n.S }
func (n *New) Span() Span                 { return n.S }
func (n *Clone) Span() Span               { return n.S }
func (n *Unary) Span() Span               { return n.S }
func (n *Cast) Span() Span                { return n.S }
func (n *IncDec) Span() Span              { return n.S }
func (n *Binary) Span() Span              { return n.S }
func (n *Assign) Span() Span              { return n.S }
func (n *Ternary) Span() Span             { return n.S }
func (n *Instanceof) Span() Span          { return n.S }
func (n *Isset) Span() Span               { return n.S }
func (n *Empty) Span() Span               { return n.S }
func (n *Exit) Span() Span                { return n.S }
func (n *Print) Span() Span               { return n.S }
func (n *Include) Span() Span             { return n.S }
func (n *Eval) Span() Span                { return n.S }
func (n *Closure) Span() Span             { return n.S }
func (n *ClosureUse) Span() Span          { return n.S }
func (n *ArrowFunc) Span() Span           { return n.S }
func (n *Match) Span() Span               { return n.S }
func (n *MatchArm) Span() Span            { return n.S }
func (n *Yield) Span() Span               { return n.S }
func (n *YieldFrom) Span() Span           { return n.S }
func (n *Throw) Span() Span               { return n.S }
func (n *Paren) Span() Span               { return n.S }
func (n *IsExpr) Span() Span              { return n.S }
func (n *AsExpr) Span() Span              { return n.S }

// ------------------------------------------------------------------ types

type (
	// NamedType is int, string, Foo, \Foo\Bar, static, self, array, callable,
	// and Phaenon generics Foo<T>, list<T>, map<K, V>.
	NamedType struct {
		S        Span
		Name     *Name
		TypeArgs []TypeExpr
	}
	// NullableType is ?T.
	NullableType struct {
		S    Span
		Type TypeExpr
	}
	// UnionType is A|B|C; members may be IntersectionType (DNF).
	UnionType struct {
		S     Span
		Types []TypeExpr
	}
	// IntersectionType is A&B&C. Parenthesised reports (A&B) inside a union.
	IntersectionType struct {
		S             Span
		Types         []TypeExpr
		Parenthesised bool
	}
	// ClosureType is Phaenon's Closure(A, B): R.
	ClosureType struct {
		S      Span
		Params []TypeExpr
		Return TypeExpr
	}
	// TupleType is Phaenon's (A, B): a positional shape (design/collections.md §2.4).
	TupleType struct {
		S     Span
		Elems []TypeExpr
	}
)

func (*NamedType) typeNode()        {}
func (*NullableType) typeNode()     {}
func (*UnionType) typeNode()        {}
func (*IntersectionType) typeNode() {}
func (*ClosureType) typeNode()      {}
func (*TupleType) typeNode()        {}

func (n *NamedType) Span() Span        { return n.S }
func (n *NullableType) Span() Span     { return n.S }
func (n *UnionType) Span() Span        { return n.S }
func (n *IntersectionType) Span() Span { return n.S }
func (n *ClosureType) Span() Span      { return n.S }
func (n *TupleType) Span() Span        { return n.S }

// Variance of a Phaenon type parameter.
type Variance uint8

const (
	Invariant     Variance = iota
	Covariant              // out T
	Contravariant          // in T
)

// TypeParam is one Phaenon generic parameter: [in|out] T [of Bound] [= Default].
type TypeParam struct {
	S        Span
	Name     *Ident
	Bound    TypeExpr
	Default  TypeExpr
	Variance Variance
}

func (n *TypeParam) Span() Span { return n.S }

// ------------------------------------------------------------------ declarations

// Modifier flags on members, parameters, classes and hooks.
type Modifier uint16

const (
	ModPublic Modifier = 1 << iota
	ModProtected
	ModPrivate
	ModPublicSet
	ModProtectedSet
	ModPrivateSet
	ModStatic
	ModAbstract
	ModFinal
	ModReadonly
	ModVar // the legacy `var` keyword
)

// AttrGroup is one #[...] group.
type AttrGroup struct {
	S     Span
	Attrs []*Attribute
}

func (n *AttrGroup) Span() Span { return n.S }

// Attribute is Name(args) inside #[...].
type Attribute struct {
	S    Span
	Name *Name
	Args *ArgList // nil when written without parentheses
}

func (n *Attribute) Span() Span { return n.S }

// Param is a function/method/closure/hook parameter, possibly a promoted property.
type Param struct {
	S         Span
	Doc       string // a preceding doc comment: `/** @var T */` on a promoted property
	Attrs     []*AttrGroup
	Modifiers Modifier // constructor promotion
	Type      TypeExpr
	ByRef     bool
	Variadic  bool
	Var       *Variable
	Default   Expr
	Hooks     []*PropertyHook // promoted property with hooks
}

func (n *Param) Span() Span { return n.S }

// PropertyHook is get/set inside a hooked property.
type PropertyHook struct {
	S         Span
	Attrs     []*AttrGroup
	Modifiers Modifier
	ByRef     bool
	Name      *Ident   // get or set
	Params    []*Param // nil when written without parentheses
	Body      []Stmt   // block body; nil with Expr or when abstract
	Expr      Expr     // => expr body
	Abstract  bool     // written as `get;`
}

func (n *PropertyHook) Span() Span { return n.S }

// ClassKind distinguishes class-like declarations.
type ClassKind uint8

const (
	KindClass ClassKind = iota
	KindInterface
	KindTrait
	KindEnum
)

// ClassDecl is a class, interface, trait, enum, or anonymous class.
type ClassDecl struct {
	S           Span
	Doc         string
	Attrs       []*AttrGroup
	Kind        ClassKind
	Modifiers   Modifier
	Name        *Ident // nil for an anonymous class
	TypeParams  []*TypeParam
	Extends     []*NamedType // one for classes, many for interfaces; TypeArgs in Phaenon
	Implements  []*NamedType
	BackingType TypeExpr // enum Foo: string
	Members     []ClassMember
	// Anonymous classes carry their constructor arguments.
	Args *ArgList
}

func (n *ClassDecl) Span() Span { return n.S }
func (*ClassDecl) stmtNode()    {}

// FunctionDecl is a named function.
type FunctionDecl struct {
	S          Span
	Doc        string
	Attrs      []*AttrGroup
	ByRef      bool
	Name       *Ident
	TypeParams []*TypeParam
	Params     []*Param
	ReturnType TypeExpr
	Body       []Stmt
}

func (n *FunctionDecl) Span() Span { return n.S }
func (*FunctionDecl) stmtNode()    {}

type (
	// MethodDecl is a method; Body == nil means abstract / interface method.
	MethodDecl struct {
		S          Span
		Doc        string
		Attrs      []*AttrGroup
		Modifiers  Modifier
		ByRef      bool
		Name       *Ident
		TypeParams []*TypeParam
		Params     []*Param
		ReturnType TypeExpr
		Body       []Stmt
		HasBody    bool
	}
	// PropertyDecl is `modifiers type $a = 1, $b;` or a hooked property.
	PropertyDecl struct {
		S         Span
		Doc       string
		Attrs     []*AttrGroup
		Modifiers Modifier
		Type      TypeExpr
		Props     []*PropertyItem
		Hooks     []*PropertyHook // only with a single Props entry
	}
	// PropertyItem is one $name = default.
	PropertyItem struct {
		S       Span
		Var     *Variable
		Default Expr
	}
	// ClassConstDecl is `modifiers const [type] A = 1, B = 2;`.
	ClassConstDecl struct {
		S         Span
		Doc       string
		Attrs     []*AttrGroup
		Modifiers Modifier
		Type      TypeExpr
		Consts    []*ConstItem
	}
	// ConstItem is one NAME = value.
	ConstItem struct {
		S     Span
		Name  *Ident
		Value Expr
	}
	// EnumCase is `case Foo = 'x';`.
	EnumCase struct {
		S     Span
		Doc   string
		Attrs []*AttrGroup
		Name  *Ident
		Value Expr
	}
	// TraitUse is `use A, B { … }`.
	TraitUse struct {
		S           Span
		Names       []*Name
		Adaptations []*TraitAdaptation
		Braced      bool
	}
	// TraitAdaptation is `A::m insteadof B;` or `[A::]m as [modifier] [alias];`.
	TraitAdaptation struct {
		S         Span
		Trait     *Name // nil when the method is unqualified
		Method    *Ident
		InsteadOf []*Name // non-nil for insteadof
		Alias     *Ident
		Modifiers Modifier
	}
)

func (*MethodDecl) memberNode()     {}
func (*PropertyDecl) memberNode()   {}
func (*ClassConstDecl) memberNode() {}
func (*EnumCase) memberNode()       {}
func (*TraitUse) memberNode()       {}

func (n *MethodDecl) Span() Span      { return n.S }
func (n *PropertyDecl) Span() Span    { return n.S }
func (n *PropertyItem) Span() Span    { return n.S }
func (n *ClassConstDecl) Span() Span  { return n.S }
func (n *ConstItem) Span() Span       { return n.S }
func (n *EnumCase) Span() Span        { return n.S }
func (n *TraitUse) Span() Span        { return n.S }
func (n *TraitAdaptation) Span() Span { return n.S }

// ------------------------------------------------------------------ statements

type (
	// ExprStmt is expr; Doc carries a preceding doc comment (`/** @var T $x */`).
	ExprStmt struct {
		S    Span
		Doc  string
		Expr Expr
	}
	// VoidCastStmt is PHP 8.5's `(void) expr;`.
	VoidCastStmt struct {
		S    Span
		Expr Expr
	}
	// Echo is echo a, b; and the short tag form <?= a ?>.
	Echo struct {
		S     Span
		Doc   string
		Exprs []Expr
		Short bool
	}
	// InlineHTML is text outside PHP tags (PHP mode only). Text is the raw
	// HTML; the surrounding ?> / <?php tokens are implied.
	InlineHTML struct {
		S    Span
		Text string
	}
	// OpenTag marks a <?php / <?= at the start of a file or after inline HTML.
	// It is kept so a PHP-mode file prints back exactly; Phaenon files never contain it.
	OpenTag struct {
		S   Span
		Raw string
	}
	// CloseTag is ?> (followed by InlineHTML or EOF).
	CloseTag struct {
		S   Span
		Raw string
	}
	// Block is { stmts }.
	Block struct {
		S     Span
		Stmts []Stmt
	}
	// Nop is a lone semicolon.
	Nop struct{ S Span }
	// If is if/elseif/else, braces or alternative syntax.
	If struct {
		S       Span
		Doc     string
		Cond    Expr
		Then    Stmt
		ElseIfs []*ElseIf
		Else    Stmt
		Alt     bool // if (...): ... endif;
	}
	ElseIf struct {
		S    Span
		Cond Expr
		Body Stmt
	}
	While struct {
		S    Span
		Doc  string
		Cond Expr
		Body Stmt
		Alt  bool
	}
	DoWhile struct {
		S    Span
		Doc  string
		Body Stmt
		Cond Expr
	}
	For struct {
		S    Span
		Doc  string
		Init []Expr
		Cond []Expr
		Step []Expr
		Body Stmt
		Alt  bool
	}
	Foreach struct {
		S        Span
		Doc      string // a preceding `/** @var T $v */`
		Expr     Expr
		Key      Expr
		KeyByRef bool // `&$k => $v` — the parser accepts it; PHP's compiler rejects it
		Value    Expr // Variable, destructuring ArrayLit, …
		ByRef    bool
		Body     Stmt
		Alt      bool
	}
	Switch struct {
		S       Span
		Doc     string
		Subject Expr
		Cases   []*Case
		Alt     bool
	}
	// Case is case x: stmts / default: stmts (Cond == nil).
	Case struct {
		S    Span
		Cond Expr
		Body []Stmt
	}
	Break struct {
		S      Span
		Levels Expr
	}
	Continue struct {
		S      Span
		Levels Expr
	}
	Return struct {
		S    Span
		Doc  string // a preceding `/** @var T $x */`
		Expr Expr
	}
	Global struct {
		S    Span
		Vars []Expr
	}
	StaticVars struct {
		S    Span
		Doc  string
		Vars []*StaticVar
	}
	StaticVar struct {
		S       Span
		Var     *Variable
		Default Expr
	}
	Unset struct {
		S    Span
		Doc  string
		Vars []Expr
	}
	Try struct {
		S          Span
		Doc        string
		Body       []Stmt
		Catches    []*Catch
		Finally    []Stmt
		HasFinally bool
	}
	Catch struct {
		S     Span
		Types []*Name
		Var   *Variable // nil for catch (Foo)
		Body  []Stmt
	}
	Goto struct {
		S     Span
		Label *Ident
	}
	Label struct {
		S    Span
		Name *Ident
	}
	Declare struct {
		S          Span
		Directives []*ConstItem
		Body       Stmt // nil for declare(...);
		Alt        bool
	}
	HaltCompiler struct {
		S    Span
		Rest string // everything after __halt_compiler();
	}
	// Namespace is namespace X; (Body == nil, Braced false) or namespace X { … }.
	Namespace struct {
		S      Span
		Name   *Name // nil for the global namespace block
		Body   []Stmt
		Braced bool
	}
	// Use is use A, B as C; / use function f; / use A\{B, C as D};
	Use struct {
		S      Span
		Kind   UseKind
		Prefix *Name // group use prefix, nil otherwise
		Items  []*UseItem
	}
	UseItem struct {
		S     Span
		Kind  UseKind // per-item kind inside a mixed group use
		Name  *Name
		Alias *Ident
	}
	// ConstDecl is a top-level `const A = 1, B = 2;`.
	ConstDecl struct {
		S      Span
		Doc    string
		Attrs  []*AttrGroup
		Consts []*ConstItem
	}
	// TypeAlias is Phaenon's `type Name<T> = Type;`.
	TypeAlias struct {
		S          Span
		Doc        string
		Attrs      []*AttrGroup
		Name       *Ident
		TypeParams []*TypeParam
		Type       TypeExpr
	}
)

// UseKind is the kind of a use import.
type UseKind uint8

const (
	UseClass UseKind = iota
	UseFunction
	UseConst
)

func (*ExprStmt) stmtNode()     {}
func (*VoidCastStmt) stmtNode() {}
func (*Echo) stmtNode()         {}
func (*InlineHTML) stmtNode()   {}
func (*OpenTag) stmtNode()      {}
func (*CloseTag) stmtNode()     {}
func (*Block) stmtNode()        {}
func (*Nop) stmtNode()          {}
func (*If) stmtNode()           {}
func (*While) stmtNode()        {}
func (*DoWhile) stmtNode()      {}
func (*For) stmtNode()          {}
func (*Foreach) stmtNode()      {}
func (*Switch) stmtNode()       {}
func (*Break) stmtNode()        {}
func (*Continue) stmtNode()     {}
func (*Return) stmtNode()       {}
func (*Global) stmtNode()       {}
func (*StaticVars) stmtNode()   {}
func (*Unset) stmtNode()        {}
func (*Try) stmtNode()          {}
func (*Goto) stmtNode()         {}
func (*Label) stmtNode()        {}
func (*Declare) stmtNode()      {}
func (*HaltCompiler) stmtNode() {}
func (*Namespace) stmtNode()    {}
func (*Use) stmtNode()          {}
func (*ConstDecl) stmtNode()    {}
func (*TypeAlias) stmtNode()    {}

func (n *ExprStmt) Span() Span     { return n.S }
func (n *VoidCastStmt) Span() Span { return n.S }
func (n *Echo) Span() Span         { return n.S }
func (n *InlineHTML) Span() Span   { return n.S }
func (n *OpenTag) Span() Span      { return n.S }
func (n *CloseTag) Span() Span     { return n.S }
func (n *Block) Span() Span        { return n.S }
func (n *Nop) Span() Span          { return n.S }
func (n *If) Span() Span           { return n.S }
func (n *ElseIf) Span() Span       { return n.S }
func (n *While) Span() Span        { return n.S }
func (n *DoWhile) Span() Span      { return n.S }
func (n *For) Span() Span          { return n.S }
func (n *Foreach) Span() Span      { return n.S }
func (n *Switch) Span() Span       { return n.S }
func (n *Case) Span() Span         { return n.S }
func (n *Break) Span() Span        { return n.S }
func (n *Continue) Span() Span     { return n.S }
func (n *Return) Span() Span       { return n.S }
func (n *Global) Span() Span       { return n.S }
func (n *StaticVars) Span() Span   { return n.S }
func (n *StaticVar) Span() Span    { return n.S }
func (n *Unset) Span() Span        { return n.S }
func (n *Try) Span() Span          { return n.S }
func (n *Catch) Span() Span        { return n.S }
func (n *Goto) Span() Span         { return n.S }
func (n *Label) Span() Span        { return n.S }
func (n *Declare) Span() Span      { return n.S }
func (n *HaltCompiler) Span() Span { return n.S }
func (n *Namespace) Span() Span    { return n.S }
func (n *Use) Span() Span          { return n.S }
func (n *UseItem) Span() Span      { return n.S }
func (n *ConstDecl) Span() Span    { return n.S }
func (n *TypeAlias) Span() Span    { return n.S }
