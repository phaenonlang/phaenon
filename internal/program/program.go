// Package program is the program model: every class, function and constant
// the checker can see, where it came from, and how names resolve to it.
//
// Declarations enter the model from three kinds of source: the checked
// project files (parsed in Phaenon mode), vendor PHP found through
// Composer's autoload maps, and the embedded php-src stubs plus Phaenon's
// overlay. Vendor and stub declarations are loaded lazily, by name, and a
// declaration's signature (its resolved types) is built on first use.
package program

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/types"
	"github.com/phaenonlang/phaenon/stdlib"
)

// Program holds every loaded declaration.
type Program struct {
	classes   map[string]*Class
	functions map[string]*Function
	constants map[string]*Constant
	// classAliases maps a name class_alias() gives a class to the class, as
	// PHP's autoloader would have run it (`class_alias(New::class,
	// Old::class)` keeps a moved class reachable under its old name).
	classAliases map[string]string
	files        map[string]*File
	order        []*File
	loaders      []Loader
	// missing remembers names no loader could find, so the lookup is not
	// repeated for every use.
	missing map[string]bool
	// override is set while the overlay loads: declarations replace
	// earlier ones instead of being ignored.
	override bool
	anon     int
	// patches are overlay declarations of classes the stubs already
	// declare, applied on top of them (see declareClass).
	patches map[string]*ast.ClassDecl
	// Problems are interop (P4) findings made while reading declarations:
	// unparsable docblocks, docblock types narrower than the signature…
	Problems []Problem
	// ConstType evaluates a constant's value expression; the checker
	// installs it (the program package cannot type expressions itself).
	ConstType func(c *Constant) types.Type
	stubs     bool
	target    phpversion.Version
	// extensionsPHP is the version of the php LoadExtensions reflected.
	extensionsPHP string
	// Parses, when set, keeps the trees of the files this program parses
	// for the next program of the same process (ParseCache).
	Parses *ParseCache
}

// Problem is a finding attached to a declaration site.
type Problem struct {
	File *File
	Span ast.Span
	Msg  string
}

// Loader finds the file that declares a class.
type Loader interface {
	// FindClass maps a lower-cased fully-qualified class name to a file
	// path, or returns false.
	FindClass(lower string) (string, bool)
	// Preload lists files to parse eagerly: Composer's `files` autoload
	// entries, which hold functions and constants no map can locate.
	Preload() []string
	// Read returns the contents of a path the loader returned.
	Read(path string) ([]byte, error)
}

// New creates an empty program.
func New() *Program {
	return &Program{
		classes:      map[string]*Class{},
		classAliases: map[string]string{},
		functions:    map[string]*Function{},
		constants:    map[string]*Constant{},
		files:        map[string]*File{},
		missing:      map[string]bool{},
		patches:      map[string]*ast.ClassDecl{},
	}
}

// Target is the PHP version the program compiles for: the oldest PHP the
// emitted code must run on (design/php-versions.md §2). The checker and the
// emitter ask it whether a construct or a library symbol needs lowering or a
// diagnostic; it is phpversion.Floor until SetTarget says otherwise.
func (p *Program) Target() phpversion.Version {
	if p == nil || p.target.IsZero() {
		return phpversion.Floor
	}
	return p.target
}

// SetTarget sets the version Target reports. phn resolves it from
// phaenon.json and composer.json; tests set it directly.
func (p *Program) SetTarget(v phpversion.Version) { p.target = v }

// AddLoader appends a loader; loaders are consulted in order.
func (p *Program) AddLoader(l Loader) {
	p.loaders = append(p.loaders, l)
	for _, path := range l.Preload() {
		src, err := l.Read(path)
		if err != nil {
			continue
		}
		p.AddFile(path, src, lexer.ModePHP, false) // parse errors in vendor files are ignored
	}
}

// LoadStubs declares the embedded php-src stubs and the Phaenon overlay.
func (p *Program) LoadStubs() error {
	if p.stubs {
		return nil
	}
	p.stubs = true
	for _, s := range stdlib.Stubs() {
		if _, err := p.AddFile("<stub>/"+s.Path, patchStub(s.Src), lexer.ModePHP, false); err != nil {
			return fmt.Errorf("%s: %w", s.Path, err)
		}
	}
	// The values behind the stubs' UNKNOWN constants (declareConstant fills
	// them in; the @var types stay).
	if _, err := p.AddFile("<stub>/constants.php", stdlib.Constants(), lexer.ModePHP, false); err != nil {
		return fmt.Errorf("constants.php: %w", err)
	}
	for _, s := range append(stdlib.Runtime(), stdlib.Concurrent()...) {
		mode := lexer.ModePHP
		if strings.HasSuffix(s.Path, ".phn") {
			mode = lexer.ModePhaenon // the stdlib is written in Phaenon
		}
		if _, err := p.AddFile("<stub>/"+s.Path, s.Src, mode, false); err != nil {
			return fmt.Errorf("%s: %w", s.Path, err)
		}
	}
	p.override = true
	defer func() { p.override = false }()
	for _, s := range stdlib.Overlay() {
		if _, err := p.AddFile("<stub>/"+s.Path, s.Src, lexer.ModePHP, false); err != nil {
			return fmt.Errorf("%s: %w", s.Path, err)
		}
	}
	return nil
}

// LoadSuggested declares the embedded Monolog and Sentry declarations
// (stdlib.Suggested) that the runtime package's integrations are written
// against, after LoadStubs. Only the programs that check or compile the
// stdlib itself call it: in a project's program they would shadow the real
// libraries its vendor directory holds.
func (p *Program) LoadSuggested() error {
	if err := p.LoadStubs(); err != nil {
		return err
	}
	for _, s := range stdlib.Suggested() {
		if _, err := p.AddFile("<stub>/"+s.Path, s.Src, lexer.ModePHP, false); err != nil {
			return fmt.Errorf("%s: %w", s.Path, err)
		}
	}
	return nil
}

// reStubKeywordFn matches the stub declarations of functions whose names are
// keywords (`function clone(…)`, `exit`, `die`): valid for php-src's
// generator, not for PHP. The checker types those forms as syntax.
var reStubKeywordFn = regexp.MustCompile(`\bfunction (clone|exit|die)\(`)

func patchStub(src []byte) []byte {
	return reStubKeywordFn.ReplaceAll(src, []byte("function __phn_$1("))
}

// AddFile parses src and collects its declarations. Checked files are the
// ones the checker will analyse. A file added twice is returned as is.
func (p *Program) AddFile(path string, src []byte, mode lexer.Mode, checked bool) (*File, error) {
	if f, ok := p.files[path]; ok {
		return f, nil
	}
	tree, err := p.Parses.parse(path, src, mode)
	if err != nil {
		return nil, err
	}
	f := &File{Path: path, Src: src, AST: tree, Mode: mode, Checked: checked, program: p}
	p.files[path] = f
	p.order = append(p.order, f)
	p.collect(f)
	return f, nil
}

// Files returns the checked files in the order they were added.
func (p *Program) CheckedFiles() []*File {
	var out []*File
	for _, f := range p.order {
		if f.Checked {
			out = append(out, f)
		}
	}
	return out
}

// File returns a loaded file by path.
func (p *Program) File(path string) *File { return p.files[path] }

// IsStub reports a file of the embedded standard library: the php-src
// stubs, the overlay, the runtime packages' declarations.
func (f *File) IsStub() bool { return f != nil && strings.HasPrefix(f.Path, "<stub>/") }

// ClassPolyfilled reports whether a loader (Composer's maps: a polyfill
// package, the project) knows a file for a class the stubs declare, so the
// class exists on PHP versions older than the one that added it.
func (p *Program) ClassPolyfilled(name string) bool {
	lower := strings.ToLower(strings.TrimPrefix(name, "\\"))
	for _, l := range p.loaders {
		if _, ok := l.FindClass(lower); ok {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ lookup

// Class returns the class with the given fully-qualified name (any case,
// with or without a leading backslash), loading it if a loader knows it.
func (p *Program) Class(name string) *Class {
	key := strings.ToLower(strings.TrimPrefix(name, "\\"))
	if target, ok := p.classAliases[key]; ok && !strings.EqualFold(target, key) {
		return p.Class(target) // the alias wins over a declaration kept for IDEs (`if (false) { class Old … }`)
	}
	if c, ok := p.classes[key]; ok {
		return c
	}
	p.load(key)
	if target, ok := p.classAliases[key]; ok && !strings.EqualFold(target, key) {
		return p.Class(target) // the loaded file aliased the name
	}
	return p.classes[key]
}

// Function returns a function by fully-qualified name.
func (p *Program) Function(name string) *Function {
	return p.functions[strings.ToLower(strings.TrimPrefix(name, "\\"))]
}

// Constant returns a global constant by fully-qualified name. The namespace
// part is case-insensitive; the constant name is not.
func (p *Program) Constant(name string) *Constant {
	return p.constants[constKey(strings.TrimPrefix(name, "\\"))]
}

func constKey(fqn string) string {
	i := strings.LastIndex(fqn, "\\")
	if i < 0 {
		return fqn
	}
	return strings.ToLower(fqn[:i]) + "\\" + fqn[i+1:]
}

// load asks the loaders for a class.
func (p *Program) load(lower string) {
	if p.missing[lower] {
		return
	}
	for _, l := range p.loaders {
		path, ok := l.FindClass(lower)
		if !ok {
			continue
		}
		if _, loaded := p.files[path]; loaded {
			continue // the file was parsed but does not declare the class
		}
		src, err := l.Read(path)
		if err != nil {
			continue
		}
		mode := lexer.ModePHP
		if strings.HasSuffix(path, ".phn") {
			mode = lexer.ModePhaenon
		}
		if _, err := p.AddFile(path, src, mode, false); err != nil {
			continue
		}
		if _, ok := p.classes[lower]; ok {
			return
		}
		if target, ok := p.classAliases[lower]; ok && !strings.EqualFold(target, lower) {
			return // the file declared the name as an alias
		}
	}
	p.missing[lower] = true
}

// Classes returns every loaded class, sorted by name (for tests and tools).
func (p *Program) Classes() []*Class {
	out := make([]*Class, 0, len(p.classes))
	for _, c := range p.classes {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Functions returns every loaded function, sorted by name.
func (p *Program) Functions() []*Function {
	out := make([]*Function, 0, len(p.functions))
	for _, f := range p.functions {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (p *Program) problem(f *File, s ast.Span, format string, args ...any) {
	p.Problems = append(p.Problems, Problem{File: f, Span: s, Msg: fmt.Sprintf(format, args...)})
}

// ------------------------------------------------------------------ types.Hierarchy

// IsSubclassOf implements types.Hierarchy.
func (p *Program) IsSubclassOf(sub, super string) bool {
	sub, super = p.Canonical(sub), p.Canonical(super)
	if strings.EqualFold(sub, super) {
		return true
	}
	c := p.Class(sub)
	if c == nil {
		return false
	}
	return c.IsSubclassOf(super)
}

// Canonical is the class a name stands for: the class itself, or the one
// class_alias() points the name at.
func (p *Program) Canonical(name string) string {
	key := strings.ToLower(strings.TrimPrefix(name, "\\"))
	for i := 0; i < 8; i++ {
		target, ok := p.classAliases[key]
		if !ok || strings.EqualFold(target, key) {
			break
		}
		name, key = target, strings.ToLower(target)
	}
	return name
}

// IsAlias reports a name class_alias() gives another class.
func (p *Program) IsAlias(name string) bool {
	_, ok := p.classAliases[strings.ToLower(strings.TrimPrefix(name, "\\"))]
	return ok
}

// IsFinal implements types.Hierarchy.
func (p *Program) IsFinal(name string) bool {
	c := p.Class(name)
	return c != nil && (c.Final || c.Kind == ast.KindEnum || c.Anonymous)
}

// Variances implements types.Hierarchy.
func (p *Program) Variances(name string) []types.Variance {
	c := p.Class(name)
	if c == nil {
		return nil
	}
	c.ensure()
	if len(c.TypeParams) == 0 {
		return nil
	}
	out := make([]types.Variance, len(c.TypeParams))
	for i, tp := range c.TypeParams {
		out[i] = tp.Variance
	}
	return out
}

// Exists implements types.Hierarchy.
func (p *Program) Exists(name string) bool { return p.Class(name) != nil }

// IteratorArgs implements types.Hierarchy: the Traversable<K, V> arguments
// a class inherits (`interface Stream extends \Traversable<int, Frame>`,
// `class Bag implements IteratorAggregate<string, T>`).
func (p *Program) IteratorArgs(c types.Class) (types.Type, types.Type, bool) {
	cl := p.Class(c.Name)
	if cl == nil {
		return nil, nil, false
	}
	args, ok := cl.SuperArgs("Traversable", c.Args)
	if !ok || len(args) != 2 {
		return nil, nil, false
	}
	return args[0], args[1], true
}

// IsInvokable implements types.Hierarchy.
func (p *Program) IsInvokable(name string) bool {
	c := p.Class(name)
	if c == nil {
		return false
	}
	c.Signature()
	return c.FindMethod("__invoke") != nil
}

// TraitUsers returns the loaded classes that use the trait, sorted by name.
func (p *Program) TraitUsers(trait string) []*Class {
	var out []*Class
	for _, c := range p.classes {
		c.ensureHeader()
		for _, t := range c.Traits {
			if strings.EqualFold(t, trait) {
				out = append(out, c)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// PropertyType implements types.Hierarchy.
func (p *Program) PropertyType(class, name string) (types.Type, bool) {
	cl := p.Class(class)
	if cl == nil {
		return nil, false
	}
	cl.Signature()
	pr := cl.FindProperty(name)
	if pr == nil || pr.Static {
		return nil, false
	}
	return pr.Type, true
}

// IsCallableName implements types.Hierarchy: 'strval' names a function,
// 'Foo::bar' a static method.
func (p *Program) IsCallableName(name string) bool {
	name = strings.TrimPrefix(name, "\\")
	if cls, method, ok := strings.Cut(name, "::"); ok {
		c := p.Class(cls)
		if c == nil {
			return false
		}
		c.Signature()
		m := c.FindMethod(method)
		return m != nil && m.Static
	}
	return p.Function(name) != nil
}

// ------------------------------------------------------------------ files and scopes

// Partial is the checker's account of a partial application (PHP 8.6,
// design/php-versions.md §5), which the emitter needs to lower it for an
// 8.5 target: the parameters of the closure it creates, in PHP's order,
// each named and typed after the callee's parameter it stands for.
type Partial struct {
	Params []PartialParam
	// Refused says why the form has no exact 8.5 lowering; the checker
	// reports it (P1002) when the target is 8.5. Empty when it lowers.
	Refused string
	// Pipe marks the right side of a pipe that PHP turns into a call
	// (`A |> f(B, ?, C)` is f(B, A, C)); Params then holds the one
	// parameter the left side goes to. Whether the pipe may be written as
	// that call is File.Folds.
	Pipe bool
	// Rest marks a `...` placeholder; Named a named argument before it.
	Rest, Named bool
	// Void says the callee returns void: a closure body calls it without
	// returning its result.
	Void bool
}

// PartialParam is one parameter of the closure a partial application
// creates.
type PartialParam struct {
	// Name is the callee's parameter name; a placeholder that binds into a
	// variadic parameter is named after it with its offset ($args0, …), as
	// PHP names it.
	Name string
	// Native is the callee parameter's declared type as PHP code, class
	// names fully qualified; "" when it declares none.
	Native string
	// Doc is its type for a docblock when the type says more than PHP can
	// declare (list<int>, a literal, a generic class); "" otherwise.
	Doc string
	// Default is an optional parameter's default as PHP code that means
	// the same in any file; "" when required or when it cannot be written
	// so (Unwritable).
	Default    string
	Unwritable bool
	Optional   bool
	Variadic   bool
	ByRef      bool
	// Arg is the placeholder that creates the parameter (`?` or `name: ?`);
	// nil for the parameters `...` stands for.
	Arg *ast.Arg
}

// Test is the checker's lowering of an `is`, `as` or `as?` to the test PHP
// makes itself (design/generics.md §11.4): `\is_int($x)`, `$x instanceof
// Foo`, `\is_array($x) && \array_is_list($x)`, without the checks the
// operand's PHP type already settles. The emitter writes it as recorded and
// derives no test of its own, so what runs is what the checker narrowed on.
type Test struct {
	// Cond is the condition as PHP code with TestOperand wherever the
	// operand is written (`\is_int(␀[0])` tests a tuple's first element);
	// "" when the type has no test, which the checker reported.
	Cond string
	// Prec is how tightly Cond binds: written where an operand of a looser
	// operator is expected, it needs no parentheses.
	Prec Prec
	// Expected is the tested type in Phaenon's spelling, for the message
	// of the TypeError an `as` throws.
	Expected string
}

// TestOperand stands for the operand in a Test's Cond.
const TestOperand = "\x00"

// PHPString is s as a single-quoted PHP string literal.
func PHPString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// Prec orders the PHP operators a lowered test is written with, the
// loosest first.
type Prec uint8

const (
	PrecTernary    Prec = iota // cond ? a : b, the form of `as`
	PrecOr                     // ||
	PrecAnd                    // &&
	PrecEquality               // ===
	PrecInstanceof             // instanceof
	PrecAtom                   // a call, a constant
)

// File is one parsed source file and its namespace scopes.
type File struct {
	Path    string
	Src     []byte
	AST     *ast.File
	Mode    lexer.Mode
	Checked bool
	// Partials records each checked partial application by its argument
	// list (see Partial).
	Partials map[*ast.ArgList]*Partial
	// Folds marks each checked pipe PHP evaluates exactly as the call it
	// makes (`A |> f(B, ?, C)` as f(B, A, C)): the stage's callee and the
	// arguments before the placeholder compute the same before A as after
	// it, and A goes by value. The 8.5 lowering writes these pipes as the
	// calls (design/php-versions.md §5.3). A pipe the checker did not reach
	// is absent.
	Folds map[*ast.Binary]bool
	// Tests holds the lowering of each checked `is`, `as` and `as?`, by
	// its node (an *ast.IsExpr or *ast.AsExpr).
	Tests map[ast.Expr]*Test
	// Scopes maps statement ranges to the namespace scope in force there.
	Scopes  []ScopedRange
	lines   []int
	program *Program
}

// ScopedRange says which Scope applies to a byte range of the file.
type ScopedRange struct {
	Span  ast.Span
	Scope *Scope
}

// ScopeAt returns the namespace scope in force at a byte offset.
func (f *File) ScopeAt(off int) *Scope {
	for i := len(f.Scopes) - 1; i >= 0; i-- {
		r := f.Scopes[i]
		if r.Span.Start <= off && off < r.Span.End {
			return r.Scope
		}
	}
	if len(f.Scopes) > 0 {
		return f.Scopes[0].Scope
	}
	return &Scope{}
}

// Position converts a byte offset to a 1-based line and column.
func (f *File) Position(off int) (line, col int) {
	if f.lines == nil {
		f.lines = []int{0}
		for i, b := range f.Src {
			if b == '\n' {
				f.lines = append(f.lines, i+1)
			}
		}
	}
	i := sort.Search(len(f.lines), func(i int) bool { return f.lines[i] > off }) - 1
	if i < 0 {
		i = 0
	}
	return i + 1, off - f.lines[i] + 1
}

// Line returns the text of a 1-based line without its line break.
func (f *File) Line(n int) string {
	f.Position(0)
	if n < 1 || n > len(f.lines) {
		return ""
	}
	start := f.lines[n-1]
	end := len(f.Src)
	if n < len(f.lines) {
		end = f.lines[n] - 1
	}
	return strings.TrimRight(string(f.Src[start:end]), "\r\n")
}

// Scope is a namespace plus its imports.
type Scope struct {
	Namespace string            // "" for the global namespace
	classes   map[string]string // lower alias → FQN
	functions map[string]string // lower alias → FQN
	consts    map[string]string // alias → FQN (case-sensitive)
	aliases   map[string]*FileAlias
}

func newScope(ns string) *Scope {
	return &Scope{Namespace: ns, classes: map[string]string{}, functions: map[string]string{}, consts: map[string]string{}}
}

// Alias returns the file-level type alias declared under name in this
// scope (case-sensitive, like PHPStan's @phpstan-type), or nil.
func (s *Scope) Alias(name string) *FileAlias {
	if s == nil || s.aliases == nil {
		return nil
	}
	return s.aliases[name]
}

// Aliases lists the scope's type aliases in declaration order.
func (s *Scope) Aliases() []*FileAlias {
	out := make([]*FileAlias, 0, len(s.aliases))
	for _, a := range s.aliases {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Decl.S.Start < out[j].Decl.S.Start })
	return out
}

// Special class names that never resolve through the namespace.
func isSpecialClassName(lower string) bool {
	switch lower {
	case "self", "static", "parent":
		return true
	}
	return false
}

// reservedTypeNames are the native type keywords (never class names).
var reservedTypeNames = map[string]bool{
	"int": true, "float": true, "string": true, "bool": true, "array": true, "iterable": true,
	"callable": true, "object": true, "mixed": true, "void": true, "null": true, "never": true,
	"false": true, "true": true,
}

// ResolveClass resolves a class name in a name position (new, extends,
// instanceof, type) to a fully-qualified name. self/static/parent are
// returned lower-cased as they are.
func (s *Scope) ResolveClass(n *ast.Name) string {
	return s.ResolveClassParts(n.Kind, n.Parts)
}

// ResolveClassParts is ResolveClass on the parts of a name.
func (s *Scope) ResolveClassParts(kind ast.NameKind, parts []string) string {
	switch kind {
	case ast.NameFullyQualified:
		return strings.Join(parts, "\\")
	case ast.NameRelative:
		return s.prefix() + strings.Join(parts, "\\")
	}
	first := strings.ToLower(parts[0])
	if len(parts) == 1 && isSpecialClassName(first) {
		return first
	}
	if fqn, ok := s.classes[first]; ok {
		if len(parts) == 1 {
			return fqn
		}
		return fqn + "\\" + strings.Join(parts[1:], "\\")
	}
	return s.prefix() + strings.Join(parts, "\\")
}

// ResolveClassString resolves a class name written as a string (docblocks,
// `'Foo\Bar'` literals never go through here — they are already FQN).
func (s *Scope) ResolveClassString(name string) string {
	if strings.HasPrefix(name, "\\") {
		return name[1:]
	}
	if strings.HasPrefix(strings.ToLower(name), "namespace\\") {
		return s.prefix() + name[len("namespace\\"):]
	}
	parts := strings.Split(name, "\\")
	kind := ast.NameUnqualified
	if len(parts) > 1 {
		kind = ast.NameQualified
	}
	return s.ResolveClassParts(kind, parts)
}

// ResolveFunction returns the candidate names for an unqualified call:
// the namespaced name and the global fallback (equal for qualified names).
func (s *Scope) ResolveFunction(n *ast.Name) (namespaced, global string) {
	switch n.Kind {
	case ast.NameFullyQualified:
		fqn := strings.Join(n.Parts, "\\")
		return fqn, fqn
	case ast.NameRelative:
		fqn := s.prefix() + strings.Join(n.Parts, "\\")
		return fqn, fqn
	}
	if len(n.Parts) == 1 {
		if fqn, ok := s.functions[strings.ToLower(n.Parts[0])]; ok {
			return fqn, fqn
		}
		return s.prefix() + n.Parts[0], n.Parts[0]
	}
	// Qualified: the first part may be a class alias.
	if fqn, ok := s.classes[strings.ToLower(n.Parts[0])]; ok {
		full := fqn + "\\" + strings.Join(n.Parts[1:], "\\")
		return full, full
	}
	full := s.prefix() + strings.Join(n.Parts, "\\")
	return full, full
}

// ResolveConst is ResolveFunction for constants (case-sensitive last part).
func (s *Scope) ResolveConst(n *ast.Name) (namespaced, global string) {
	switch n.Kind {
	case ast.NameFullyQualified:
		fqn := strings.Join(n.Parts, "\\")
		return fqn, fqn
	case ast.NameRelative:
		fqn := s.prefix() + strings.Join(n.Parts, "\\")
		return fqn, fqn
	}
	if len(n.Parts) == 1 {
		if fqn, ok := s.consts[n.Parts[0]]; ok {
			return fqn, fqn
		}
		return s.prefix() + n.Parts[0], n.Parts[0]
	}
	if fqn, ok := s.classes[strings.ToLower(n.Parts[0])]; ok {
		full := fqn + "\\" + strings.Join(n.Parts[1:], "\\")
		return full, full
	}
	full := s.prefix() + strings.Join(n.Parts, "\\")
	return full, full
}

func (s *Scope) prefix() string {
	if s.Namespace == "" {
		return ""
	}
	return s.Namespace + "\\"
}

// Imports returns the class imports (alias → FQN) for unused-import checks.
func (s *Scope) Imports() map[string]string { return s.classes }

func (s *Scope) addUse(kind ast.UseKind, fqn string, alias string) {
	if alias == "" {
		alias = fqn[strings.LastIndex(fqn, "\\")+1:]
	}
	switch kind {
	case ast.UseClass:
		s.classes[strings.ToLower(alias)] = fqn
	case ast.UseFunction:
		s.functions[strings.ToLower(alias)] = fqn
	case ast.UseConst:
		s.consts[alias] = fqn
	}
}
