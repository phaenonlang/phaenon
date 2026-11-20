package checker

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	phnast "github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
)

// An extra check and a finding of another kind at the same place in the
// same words are one finding, of the other kind, whichever came first and
// whatever was rolled back between them; alone, the extra check is
// returned unless the extra checks are off.
func TestExtraCheckBesideItsTwin(t *testing.T) {
	for _, c := range []struct {
		name   string
		off    bool
		report func(c *Checker, at phnast.Span)
		want   []Kind
	}{
		{"an extra check alone", false, func(c *Checker, at phnast.Span) { c.reportExtra(at, P2103, "x") }, []Kind{Extra}},
		{"an extra check alone, the extra checks off", true, func(c *Checker, at phnast.Span) { c.reportExtra(at, P2103, "x") }, nil},
		{"reported twice", false, func(c *Checker, at phnast.Span) { c.reportExtra(at, P2103, "x"); c.reportExtra(at, P2103, "x") }, []Kind{Extra}},
		{"the extra check first", false, func(c *Checker, at phnast.Span) { c.reportExtra(at, P2103, "x"); c.report(at, P2103, "x") }, []Kind{Parity}},
		{"the extra check second, the extra checks off", true, func(c *Checker, at phnast.Span) { c.report(at, P2103, "x"); c.reportExtra(at, P2103, "x") }, []Kind{Parity}},
		{"other words", false, func(c *Checker, at phnast.Span) { c.reportExtra(at, P2103, "x"); c.report(at, P2103, "y") }, []Kind{Extra, Parity}},
		{"the twin rolled back", false, func(c *Checker, at phnast.Span) {
			c.reportExtra(at, P2103, "x")
			mark := c.mark()
			c.report(at, P2103, "x")
			c.rollback(mark)
		}, []Kind{Extra}},
		{"the extra check rolled back and reported again", false, func(c *Checker, at phnast.Span) {
			mark := c.mark()
			c.reportExtra(at, P2103, "x")
			c.rollback(mark)
			c.reportExtra(at, P2103, "x")
		}, []Kind{Extra}},
		{"a language rule's code at an extra check's place", true, func(c *Checker, at phnast.Span) { c.reportOf(Extra, false, at, P1001, nil, "x") }, []Kind{LanguageRule}},
		// The kind is a field of the key: no message spells it.
		{"words that end as a key once did", false, func(c *Checker, at phnast.Span) {
			c.reportExtra(at, P2301, "on string")
			c.report(at, P2301, "on string|extra")
		}, []Kind{Extra, Parity}},
		{"those words, the extra checks off", true, func(c *Checker, at phnast.Span) {
			c.report(at, P2301, "on string|extra")
			c.reportExtra(at, P2301, "on string")
		}, []Kind{Parity}},
		{"those words the other way", false, func(c *Checker, at phnast.Span) {
			c.report(at, P2301, "on string")
			c.reportExtra(at, P2301, "on string|extra")
		}, []Kind{Parity, Extra}},
	} {
		p := newProgram(t)
		f, err := p.AddFile("kinds.phn", []byte("namespace Kinds;\n"), lexer.ModePhaenon, true)
		if err != nil {
			t.Fatal(err)
		}
		checker := New(p, Options{NoExtraChecks: c.off})
		checker.file = f
		c.report(checker, phnast.Span{Start: 0, End: 9})
		checker.settleExtraChecks()
		var got []Kind
		for _, d := range checker.diags {
			got = append(got, d.Kind)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: the findings are %v, want %v", c.name, got, c.want)
		}
	}
}

// declaredCodes reads codes.go: every `P2103 = Code{"P2103", …}` of it, the
// variable's name to the code's ID.
func declaredCodes(t *testing.T) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "codes.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		lit, ok := spec.Values[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		if name, isCode := lit.Type.(*ast.Ident); !isCode || name.Name != "Code" || len(lit.Elts) == 0 {
			return true
		}
		id, ok := lit.Elts[0].(*ast.BasicLit)
		if !ok {
			t.Fatalf("codes.go: %s is not written Code{\"P…\", …}", spec.Names[0].Name)
		}
		value, _ := strconv.Unquote(id.Value)
		if spec.Names[0].Name != value {
			t.Errorf("codes.go: the variable %s holds the code %s", spec.Names[0].Name, value)
		}
		codes[spec.Names[0].Name] = value
		return true
	})
	if len(codes) < 100 {
		t.Fatalf("codes.go: %d codes found, which is not all of them", len(codes))
	}
	return codes
}

// Every code has its kind (kinds.go): it is a language rule as a whole, or
// its kind is decided where a finding of it is reported, and never both. A
// code added to codes.go fails here until it is put in one of the lists.
func TestEveryCodeHasAKind(t *testing.T) {
	codes := declaredCodes(t)
	known := map[string]bool{}
	for _, id := range codes {
		known[id] = true
		switch {
		case languageRules[id] && placedCodes[id]:
			t.Errorf("%s is in languageRules and in placedCodes: it is one or the other", id)
		case !languageRules[id] && !placedCodes[id]:
			t.Errorf("%s has no kind: add it to languageRules when every finding of it is one, "+
				"else to placedCodes, where report makes it a parity finding and reportExtra an extra check", id)
		}
	}
	for _, list := range []map[string]bool{languageRules, placedCodes} {
		for id := range list {
			if !known[id] {
				t.Errorf("kinds.go names %s, which codes.go does not declare", id)
			}
		}
	}
}

// extraCheckSites are the places that may report an extra check: the
// function the call stands in, what it calls and the code. Each has the
// proof it rests on that PHPStan makes no such finding (extra.go), and the rule
// PHPStan judges the place by, found by giving the place a value both
// refuse and reading the identifier, and then the values the extra check
// is about, which PHPStan passed there. A call that is not listed fails
// TestExtraCheckSites: it is added here with its proof and its run, or it
// reports through report. The assignment of an offset is not here: PHPStan
// holds it to offsetAssign.valueType, which refuses a general array that
// argument.type passes for the same list shape.
var extraCheckSites = map[string]struct{ proof, phpstan string }{
	"decls.go checkOverrideOf reportExtraRelated P2502": {
		"declarations: the native types written fit as PHP compares them; no value is involved",
		"method.childParameterType and method.childReturnType, which level max does not apply to the documented types of static methods"},
	"expr.go arithResult reportExtra P2301": {
		"operands: the string is an untouched parameter typed by PHP alone, the other operand a number written out or another untouched parameter",
		"binaryOp.invalid, reported for a string whose value PHPStan knows, an array, a class and a mixed, and for no string or ?string"},
	// reportUnfit: the value's expression is an untouched parameter, which
	// is its only occurrence in the function, or a literal (knownAlike), or
	// arithmetic over an untouched parameter (arithmeticFits).
	"call.go checkCallArgs reportUnfit P2103": {
		"value: the argument's expression (the variable itself outside every loop: PHPStan judges an argument before the callee's assertion tags and before what a parameter by reference leaves)",
		"argument.type, for a function, a method, a static method, a constructor and a named argument"},
	"stmt.go returnStmt reportUnfit P2202": {
		"value: the returned expression, none under an inline @var",
		"return.type"},
	"stmt.go yield reportUnfit P2202": {
		"value: the key's and the value's expression",
		"generator.keyType and generator.valueType"},
	"stmt.go arrowFunc reportUnfit P2202": {
		"value: the body's expression",
		"return.type of the arrow function where it declares a return type; else argument.type or return.type of the place the closure is given, " +
			"which holds the closure's return type as it holds a value"},
	"stmt.go body reportUnfitRefused P2208": {
		"value: the default, which a constant expression makes a literal",
		"parameter.defaultValue"},
	"decls.go checkClass reportUnfitRefused P2211": {
		"value: the constant's expression, where it is a literal",
		"classConstant.value"},
	"decls.go checkProperty reportUnfitRefused P2201": {
		"value: the default, where it is a literal",
		"property.defaultValue"},
	"expr.go assignFrom reportUnfit P2201": {
		"value: the right-hand side of a plain assignment to a static property",
		"assign.propertyType"},
	"expr.go assignDeclared reportUnfit P2201": {
		"value: the right-hand side of a plain assignment to a property",
		"assign.propertyType"},
}

// Every place that may report an extra check is listed with its proof, and
// names a code whose kind its place decides: under a language rule's code
// it would report a language rule, whatever it says. The calls are those of
// reportExtra, reportExtraRelated, reportUnfit and reportUnfitRefused, and
// of reportOf with the kind written out.
func TestExtraCheckSites(t *testing.T) {
	codeArgument := map[string]int{"reportExtra": 1, "reportExtraRelated": 1, "reportUnfit": 4, "reportUnfitRefused": 5, "reportOf": 3}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := map[string]bool{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				at, reports := codeArgument[sel.Sel.Name]
				if !reports || at >= len(call.Args) {
					return true
				}
				if sel.Sel.Name == "reportOf" {
					if kind, ok := call.Args[0].(*ast.Ident); !ok || kind.Name != "Extra" {
						return true // the kind is the caller's: report, reportRelated
					}
				}
				code, ok := call.Args[at].(*ast.Ident)
				if !ok {
					t.Errorf("%s: %s names its code in an expression the test cannot read", fset.Position(call.Pos()), sel.Sel.Name)
					return true
				}
				if code.Name == "code" {
					return true // the entry points pass their own parameter on
				}
				if languageRules[code.Name] {
					t.Errorf("%s: %s reports %s, a language rule by its code, as an extra check", fset.Position(call.Pos()), sel.Sel.Name, code.Name)
				}
				site := path + " " + fn.Name.Name + " " + sel.Sel.Name + " " + code.Name
				found[site] = true
				if at := extraCheckSites[site]; at.proof == "" || at.phpstan == "" {
					t.Errorf("%s: %s may report an extra check and is not in extraCheckSites: add it with the proof it rests on (extra.go) and the rule PHPStan judges the place by, or report through report", fset.Position(call.Pos()), site)
				}
				return true
			})
		}
	}
	for site := range extraCheckSites {
		if !found[site] {
			t.Errorf("extraCheckSites lists %q, and no such call is found", site)
		}
	}
}
