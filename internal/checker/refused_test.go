package checker

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/linemap"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
)

// refusedFor is, by code, how php words the errors a finding of the code
// may be about when it is marked as code PHP refuses (reportRefused): a
// compile error, or a fatal error when the file is loaded and its classes
// are declared or linked. A code that is marked somewhere is here.
var refusedFor = map[string]*regexp.Regexp{
	P2011.ID: regexp.MustCompile(`^Cannot use "parent" when current class scope has no parent`),
	P2012.ID: regexp.MustCompile(`^Cannot use "(self|static|parent)" when no class scope is active`),
	P2110.ID: regexp.MustCompile(`^Cannot use (positional argument|argument unpacking) after named argument`),
	P2201.ID: regexp.MustCompile(`^Cannot use \S+ as default value for property |^Default value for property of type `),
	P2202.ID: regexp.MustCompile(`^Generator return type must be a supertype of Generator`),
	P2204.ID: regexp.MustCompile(`^A void (function|method) must not return a value`),
	P2208.ID: regexp.MustCompile(`^Cannot use \S+ as default value for parameter `),
	P2209.ID: regexp.MustCompile(`^A never-returning (function|method) must not return`),
	P2211.ID: regexp.MustCompile(`^Cannot use \S+ as value for class constant `),
	P2311.ID: regexp.MustCompile(`^Cannot re-assign \$this`),
	P2315.ID: regexp.MustCompile(`^The "yield" expression can only be used inside a function|^Constant expression contains invalid operations|^Generator return type must be a supertype of Generator`),
	P2501.ID: regexp.MustCompile(`abstract methods? and must therefore be declared abstract or implement the remaining methods?`),
	P2502.ID: regexp.MustCompile(`^Declaration of .* must be compatible with |^Access level to |^Cannot make (non )?static |^Cannot redeclare (non-)?(static|readonly) |^Type of \S+ must (be|not be defined)`),
	P2503.ID: regexp.MustCompile(`has #\[\\Override\] attribute, but no matching parent`),
	P2504.ID: regexp.MustCompile(`declares abstract method \S+ and must therefore be declared abstract|^Abstract function \S+ cannot contain body`),
	P2505.ID: regexp.MustCompile(`^Cannot override final method `),
	P2506.ID: regexp.MustCompile(`has not been applied as \S+, because of collision with `),
	P2507.ID: regexp.MustCompile(`of non-backed enum \S+ must not have a value|of backed enum \S+ must have a value|^Enum backing type must be int or string`),
	P2508.ID: regexp.MustCompile(`^Uncaught Error: (Class|Interface) "[^"]+" not found`),
	P2509.ID: regexp.MustCompile(`cannot extend final class `),
	P2510.ID: regexp.MustCompile(`cannot extend (interface|trait) |cannot implement \S+ - it is not an interface|cannot use \S+ - it is not a trait|cannot extend \S+ - it is not an interface`),
	P2511.ID: regexp.MustCompile(`^Cannot redeclare (class|function) |^Cannot redefine class constant `),
	P2512.ID: regexp.MustCompile(`^Type of parameter \S+ of hook \S+ must be compatible with property type`),
	P2513.ID: regexp.MustCompile(`^Static property \S+ cannot be readonly|^Readonly property \S+ must have type|^Readonly class \S+ cannot use trait with a non-readonly property`),
	P2514.ID: regexp.MustCompile(`^Access type for interface method |^Interface function \S+ cannot contain body|^Interfaces may only include hooked properties|^Enum \S+ cannot include (properties|magic method)|^Cannot declare promoted property in an abstract constructor`),
	P2531.ID: regexp.MustCompile(`^Cannot use\(\.\.\.\) variables in constant expression`),
	P2532.ID: regexp.MustCompile(`^"static(::)?" is not allowed in compile-time constants|^static::class cannot be used for compile-time class name resolution|^\(expression\)::class cannot be used in constant expressions`),
	P2533.ID: regexp.MustCompile(`^Attribute "[^"]+" (cannot target|must not be repeated)`),
}

// reMember is a class's member as php names it in a message:
// `App\Shop::$name`, `App\Shop::total()`, `App\Shop::LIMIT`, and an
// enum's magic method, "Enum App\Suit cannot include magic method
// __toString"; reClass, a class it names as one, "Readonly class App\R".
var (
	reMember = regexp.MustCompile(`([\w\\]+::\$?\w+)|([\w\\]+) cannot include (magic method \w+)`)
	reClass  = regexp.MustCompile(`(?i)\bclass ([\w\\]+)`)
)

// namesMember reports a finding whose message names a member php's
// message names, as the checker writes it: the member of the same class,
// or a trait's property that php names on a class using the trait, the
// finding naming that class and the property ("readonly class App\R
// cannot declare static property $count" beside "Readonly class App\R
// cannot use trait with a non-readonly property App\Counts::$count").
func namesMember(finding, php string) bool {
	for _, m := range reMember.FindAllStringSubmatch(php, -1) {
		switch {
		case m[2] != "":
			if mentions(finding, m[2]) && mentions(finding, m[3]) {
				return true
			}
		case mentions(finding, m[1]):
			return true
		case strings.Contains(m[1], "::$"):
			property := m[1][strings.Index(m[1], "::$")+2:]
			for _, c := range reClass.FindAllStringSubmatch(php, -1) {
				if mentions(finding, c[1]) && mentions(finding, property) {
					return true
				}
			}
		}
	}
	return false
}

// mentions reports a text that names a name whole: `App\Box::get` is not
// in `App\Box::getAll()`, nor `Box` in `App\StrBox`.
func mentions(text, name string) bool {
	return regexp.MustCompile(`(?:^|[^\w\\$])` + regexp.QuoteMeta(name) + `(?:$|[^\w])`).MatchString(text)
}

// TestRefusedByPHP holds the mark of a finding about code PHP refuses,
// which no ignore comment silences, to php, over the golden corpus
// compiled as phn build compiles it. php is given each compiled file
// (phpRefusals), which is loaded again without each declaration, or
// class member, php stops at, so that php names the errors of each in
// turn; it names some on another line than the finding, a misplaced
// attribute on its declaration, a trait's collision on the class. So a
// finding is held to its place (placeOf): its class member, or the class's
// own lines, or a top-level declaration that is no class.
//
//   - A marked finding stands where php refuses the code for the reason
//     its code names (refusedFor), or names the member php refuses for
//     it elsewhere: a trait's, in the class that uses it. A finding marked
//     beside another of its code that php refuses, in another member of
//     the class, is not shown by that one.
//   - Where php refuses a declaration for a reason a code names, a finding
//     of that code reported in the declaration is marked, one at least:
//     php names its first error alone.
//
// It runs with the parity run (PHAENON_PHPSTAN), with php as that run
// takes it (PHAENON_PHP, or php on PATH). A golden compiled for a newer
// PHP than the floor needs that php (PHAENON_PHP86), and one that does
// not compile can show nothing.
func TestRefusedByPHP(t *testing.T) {
	if os.Getenv("PHAENON_PHPSTAN") == "" {
		t.Skip("PHAENON_PHPSTAN not set: the mark is held to php with the parity run")
	}
	floor := os.Getenv("PHAENON_PHP")
	if floor == "" {
		found, err := exec.LookPath("php")
		if err != nil {
			t.Skip("no php: PHAENON_PHP is not set, and none is on PATH")
		}
		floor = found
	}
	files, _ := filepath.Glob(filepath.Join("testdata", "*.phn"))
	vendorSrc, err := os.ReadFile(filepath.Join("testdata", "vendor", "Acme", "Repo.php"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			holdRefusedToPHP(t, path, src, vendorSrc, floor)
		})
	}
}

// holdRefusedToPHP checks a golden file and holds the findings it marks
// as code PHP refuses to php, given the compiled file beside the vendor
// fixture (TestRefusedByPHP).
func holdRefusedToPHP(t *testing.T, path string, src, vendorSrc []byte, floor string) {
	t.Helper()
	name := filepath.Base(path)
	p := newProgram(t)
	target, opts := goldenSetup(t, src)
	p.SetTarget(target)
	f, err := p.AddFile(path, src, lexer.ModePhaenon, true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := New(p, opts).Check()
	judged := false
	for _, d := range diags {
		if d.refused && refusedFor[d.Code] == nil {
			t.Errorf("line %d: %s is marked as code PHP refuses, and refusedFor has no words of php's for the code", d.Line, d.Code)
		}
		judged = judged || refusedFor[d.Code] != nil
	}
	if !judged {
		return
	}
	compiled, err := emit.File(src, emit.Options{SourceName: name, Program: p, File: f})
	if err != nil {
		t.Skipf("does not compile, and php cannot be asked: %s", strings.SplitN(err.Error(), "\n", 2)[0])
	}
	php := floor
	if target != phpversion.Floor {
		variable := "PHAENON_PHP" + strings.ReplaceAll(target.String(), ".", "")
		if php = os.Getenv(variable); php == "" {
			t.Skipf("target %s: %s names no php %s", target, variable, target)
		}
	}
	m, err := linemap.Find(compiled)
	if err != nil || m == nil {
		t.Fatalf("compiled without a line map (%v)", err)
	}
	dir := t.TempDir()
	phpName := strings.TrimSuffix(name, ".phn") + ".php"
	for file, content := range map[string][]byte{"Repo.php": vendorSrc, phpName: compiled} {
		if err := os.WriteFile(filepath.Join(dir, file), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	refused := phpRefusals(t, php, dir, phpName, compiled, func(n int) int {
		s, _ := m.Forward(n)
		return s
	}, nil)
	placeOf, declaration := placesOf(f)
	for _, d := range diags {
		if !d.refused || refusedFor[d.Code] == nil {
			continue
		}
		shown := false
		for line, messages := range refused {
			for _, message := range strings.Split(messages, "\n") {
				if refusedFor[d.Code].MatchString(message) {
					shown = shown || placeOf(line) == placeOf(d.Line) || namesMember(d.Message, message)
				}
			}
		}
		if !shown {
			t.Errorf("line %d: %s is marked as code PHP refuses, and php does not refuse it there for that: %s", d.Line, d.Code, d.Message)
		}
	}
	lines := make([]int, 0, len(refused))
	for line := range refused {
		lines = append(lines, line)
	}
	sort.Ints(lines)
	for _, line := range lines {
		first, last := declaration(line)
		for code, words := range refusedFor {
			if !slices.ContainsFunc(strings.Split(refused[line], "\n"), words.MatchString) {
				continue
			}
			var at []int
			marked := false
			for _, d := range diags {
				if d.Code == code && first <= d.Line && d.Line <= last {
					marked = marked || d.refused
					at = append(at, d.Line)
				}
			}
			if !marked && len(at) > 0 {
				t.Errorf("line %d: php refuses it (%s), and no %s of the declaration (lines %v) is marked as code PHP refuses",
					line, refused[line], code, slices.Compact(at))
			}
		}
	}
}

// place is where a line of a checked file stands for php's errors: the
// top-level declaration that holds it, and in a class the member, -1 for
// the class's own lines (its attributes, its header, a trait's use), on
// which php names what is wrong with the class as a whole.
type place struct{ decl, member int }

// placesOf gives, for a line of a checked file, its place, and the lines
// of the top-level declaration that holds it, its attributes included, as
// phpRefusals gives php the file again and again; a line no declaration
// holds is its own.
func placesOf(f *program.File) (placeOf func(line int) place, declaration func(line int) (first, last int)) {
	type span struct{ first, last int }
	lines := func(sp ast.Span) span {
		first, _ := f.Position(sp.Start)
		end := sp.End
		if end > sp.Start {
			end--
		}
		last, _ := f.Position(end)
		return span{first, last}
	}
	var decls []span
	var members [][]span
	var add func(stmts []ast.Stmt)
	add = func(stmts []ast.Stmt) {
		for _, s := range stmts {
			if ns, ok := s.(*ast.Namespace); ok && len(ns.Body) > 0 {
				add(ns.Body)
				continue
			}
			var own []span
			if cd, ok := s.(*ast.ClassDecl); ok {
				for _, m := range cd.Members {
					if _, use := m.(*ast.TraitUse); !use {
						own = append(own, lines(m.Span()))
					}
				}
			}
			decls = append(decls, lines(s.Span()))
			members = append(members, own)
		}
	}
	add(f.AST.Stmts)
	placeOf = func(line int) place {
		for i, d := range decls {
			if d.first <= line && line <= d.last {
				for j, m := range members[i] {
					if m.first <= line && line <= m.last {
						return place{i, j}
					}
				}
				return place{i, -1}
			}
		}
		return place{-1 - line, -1}
	}
	declaration = func(line int) (int, int) {
		for _, d := range decls {
			if d.first <= line && line <= d.last {
				return d.first, d.last
			}
		}
		return line, line
	}
	return placeOf, declaration
}
