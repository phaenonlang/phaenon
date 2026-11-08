package emit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/token"
)

// TestOutputsDependOnPHPOnly holds the promise of design/roadmap.md D24:
// the compiled output depends on nothing but PHP, so no output names a
// class, function or constant under Phaenon\ — in its code, its imports,
// its class-name strings or the docblocks PHPStan reads. It scans the
// emitter goldens as committed (TestGenerics keeps them current) and the
// checker's corpus compiled as TestLineMapProperty compiles it. The one
// exception is a file whose own namespace is under Phaenon\: the stdlib's
// Debug sources, compiled into phaenon/debug, refer to each other.
// TestLineMapTree applies the same scan to a dogfood tree.
func TestOutputsDependOnPHPOnly(t *testing.T) {
	goldens, _ := filepath.Glob(filepath.Join("testdata", "*.php"))
	if len(goldens) == 0 {
		t.Fatal("no compiled goldens")
	}
	for _, path := range goldens {
		out, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		expectPHPOnly(t, path, out)
	}
	corpus, _ := filepath.Glob(filepath.Join("..", "checker", "testdata", "*.phn"))
	compiled := 0
	for _, path := range corpus {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		p := program.New()
		if err := p.LoadStubs(); err != nil {
			t.Fatal(err)
		}
		f, err := p.AddFile(path, src, lexer.ModePhaenon, true)
		if err != nil {
			continue // a corpus file that is a syntax error on purpose
		}
		checker.New(p, checker.Options{}).Check()
		out, _, err := compileRecovered(t, path, src, p, f)
		if err != nil || out == nil {
			continue
		}
		compiled++
		expectPHPOnly(t, path, out)
	}
	if compiled < 50 {
		t.Errorf("only %d corpus files compiled: the promise needs a corpus", compiled)
	}
	stdlib, err := Stdlib()
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range stdlib {
		if _, own := phaenonReferences(out); !own {
			t.Errorf("stdlib %s: compiled into phaenon/debug, yet its namespace is not under Phaenon\\", name)
		}
	}
}

// expectPHPOnly fails the test when out, the output compiled from path,
// names anything under Phaenon\ outside a namespace of its own there.
func expectPHPOnly(t *testing.T, path string, out []byte) {
	t.Helper()
	if refs, own := phaenonReferences(out); !own && len(refs) > 0 {
		t.Errorf("%s: the output depends on more than PHP (design/roadmap.md D24):\n\t%s", path, strings.Join(refs, "\n\t"))
	}
}

// reDocName finds a qualified name in a docblock's text, where PHPStan
// resolves it as PHP resolves a class name in code.
var reDocName = regexp.MustCompile(`\\?[A-Za-z_\x80-\xff][A-Za-z0-9_\x80-\xff]*(?:\\[A-Za-z_\x80-\xff][A-Za-z0-9_\x80-\xff]*)+`)

// reClassString is a string literal that holds a class name under
// Phaenon\, a class-string PHP resolves as fully qualified.
var reClassString = regexp.MustCompile(`^\\?Phaenon\\[A-Za-z_\x80-\xff][A-Za-z0-9_\x80-\xff\\]*$`)

// phaenonReferences lists the names under Phaenon\ that a PHP file refers
// to, each as "line N: \Phaenon\…", resolved as PHP resolves them: a fully
// qualified name as written, a qualified one through the file's imports or
// its namespace (`Phaenon\X` in namespace App is App\Phaenon\X), every
// import, a class-name string, and the qualified names of docblocks.
// Comments other than docblocks, the line map among them, are not code.
// own reports a file whose namespace is Phaenon or under it.
func phaenonReferences(src []byte) (refs []string, own bool) {
	var toks []token.Token
	for _, tk := range lexer.Tokenize(src, lexer.ModePHP) {
		if k := tk.Kind; k != token.T_WHITESPACE && k != token.T_COMMENT {
			toks = append(toks, tk)
		}
	}
	text := func(i int) string { return toks[i].Text(src) }
	ns := ""                       // the current namespace, "" for the global one
	aliases := map[string]string{} // the class imports: lower-case alias → full name
	depth, nsDepth := 0, 0         // brace depth; the depth of the namespace's statements
	under := func(name string) bool { return strings.HasPrefix(name, `Phaenon\`) }
	report := func(line int, name string) {
		refs = append(refs, fmt.Sprintf("line %d: \\%s", line, name))
	}
	// resolve is the full name of a qualified class name written in code
	// or a docblock.
	resolve := func(name string) string {
		if full, ok := strings.CutPrefix(name, `\`); ok {
			return full
		}
		first, rest, _ := strings.Cut(name, `\`)
		if full, ok := aliases[strings.ToLower(first)]; ok {
			return full + `\` + rest
		}
		if ns == "" {
			return name
		}
		return ns + `\` + name
	}
	for i := 0; i < len(toks); i++ {
		tk := toks[i]
		switch tk.Kind {
		case token.Kind('{'), token.T_CURLY_OPEN, token.T_DOLLAR_OPEN_CURLY_BRACES:
			depth++
		case token.Kind('}'):
			depth--
		case token.T_NAMESPACE:
			name := ""
			if i+1 < len(toks) && (toks[i+1].Kind == token.T_STRING || toks[i+1].Kind == token.T_NAME_QUALIFIED) {
				i++
				name = text(i)
			}
			if name == "Phaenon" || under(name) {
				return nil, true
			}
			ns, aliases, nsDepth = name, map[string]string{}, depth
			if i+1 < len(toks) && toks[i+1].Kind == token.Kind('{') {
				nsDepth = depth + 1
			}
		case token.T_USE:
			// An import, not a closure's use (after its parameters) or a
			// trait's (in a class body).
			if depth != nsDepth || i > 0 && toks[i-1].Kind == token.Kind(')') {
				continue
			}
			end := i + 1
			for end < len(toks) && toks[end].Kind != token.Kind(';') {
				end++
			}
			for _, imp := range imports(toks[i+1:end], src) {
				if under(imp.full) {
					report(tk.Line, imp.full)
				}
				if imp.class {
					aliases[strings.ToLower(imp.alias)] = imp.full
				}
			}
			i = end
		case token.T_NAME_FULLY_QUALIFIED, token.T_NAME_QUALIFIED:
			if full := resolve(text(i)); under(full) {
				report(tk.Line, full)
			}
		case token.T_NAME_RELATIVE:
			full := strings.TrimPrefix(text(i)[len("namespace"):], `\`)
			if ns != "" {
				full = ns + `\` + full
			}
			if under(full) {
				report(tk.Line, full)
			}
		case token.T_CONSTANT_ENCAPSED_STRING:
			s := text(i)
			if len(s) >= 2 {
				s = strings.ReplaceAll(s[1:len(s)-1], `\\`, `\`)
				if reClassString.MatchString(s) {
					report(tk.Line, strings.TrimPrefix(s, `\`))
				}
			}
		case token.T_DOC_COMMENT:
			doc := text(i)
			for _, m := range reDocName.FindAllStringIndex(doc, -1) {
				if full := resolve(doc[m[0]:m[1]]); under(full) {
					report(tk.Line+strings.Count(doc[:m[0]], "\n"), full)
				}
			}
		}
	}
	return refs, false
}

// imported is one name a `use` statement imports.
type imported struct {
	full  string // the full name, without a leading backslash
	alias string // the name it is known by in the file
	class bool   // a class import, not `use function` or `use const`
}

// imports reads the clauses of a `use` statement, the tokens between `use`
// and `;`: `A\B`, `A\B as C`, lists of them, `function` and `const`
// imports, and groups (`A\{B, C\D as E}`).
func imports(toks []token.Token, src []byte) []imported {
	var out []imported
	kind := ""
	if len(toks) > 0 && (toks[0].Kind == token.T_FUNCTION || toks[0].Kind == token.T_CONST) {
		kind = string(toks[0].Text(src))
		toks = toks[1:]
	}
	prefix := ""
	itemKind := kind
	name, alias := "", ""
	flush := func() {
		if name == "" {
			return
		}
		full := strings.TrimPrefix(prefix+name, `\`)
		if alias == "" {
			alias = full[strings.LastIndex(full, `\`)+1:]
		}
		out = append(out, imported{full: full, alias: alias, class: itemKind == ""})
		name, alias, itemKind = "", "", kind
	}
	for i := 0; i < len(toks); i++ {
		tk := toks[i]
		switch {
		case tk.Kind == token.T_NS_SEPARATOR && i+1 < len(toks) && toks[i+1].Kind == token.Kind('{'):
			prefix, name = name+`\`, ""
			i++
		case tk.Kind == token.Kind('}'):
			flush()
			prefix = ""
		case tk.Kind == token.Kind(','):
			flush()
		case tk.Kind == token.T_AS && i+1 < len(toks):
			i++
			alias = toks[i].Text(src)
		case name == "" && (tk.Kind == token.T_FUNCTION || tk.Kind == token.T_CONST):
			itemKind = tk.Text(src)
		default:
			name += tk.Text(src)
		}
	}
	flush()
	return out
}

// TestPhaenonReferences pins down what the promise's scan counts as a
// reference, and what it leaves alone.
func TestPhaenonReferences(t *testing.T) {
	for _, c := range []struct {
		name, src string
		want      []string
		own       bool
	}{
		{"an import", "<?php\n\nnamespace App;\n\nuse Phaenon\\Debug\\LineMap;\n", []string{`line 5: \Phaenon\Debug\LineMap`}, false},
		{"a group import", "<?php\nnamespace App;\nuse Phaenon\\{Type, Debug\\LineMap as M}, Other\\X;\n", []string{`line 3: \Phaenon\Type`, `line 3: \Phaenon\Debug\LineMap`}, false},
		{"function and const imports", "<?php\nnamespace App;\nuse function Phaenon\\f;\nuse const \\Phaenon\\C;\n", []string{`line 3: \Phaenon\f`, `line 4: \Phaenon\C`}, false},
		{"a fully qualified call", "<?php\nnamespace App;\n$t = \\Phaenon\\Type::of('int');\n", []string{`line 3: \Phaenon\Type`}, false},
		{"qualified in the global namespace", "<?php\n$t = Phaenon\\Type::of('int');\n", []string{`line 2: \Phaenon\Type`}, false},
		{"qualified in a namespace", "<?php\nnamespace App;\n$t = Phaenon\\Type::of('int');\n", nil, false},
		{"qualified through an import", "<?php\nnamespace App;\nuse Phaenon as P;\n$t = P\\Type::of('int');\n", []string{`line 4: \Phaenon\Type`}, false},
		{"relative in the global namespace", "<?php\n$t = namespace\\Phaenon\\Type::class;\n", []string{`line 2: \Phaenon\Type`}, false},
		{"a class-name string", "<?php\nnamespace App;\n$c = 'Phaenon\\\\Bytes';\n", []string{`line 3: \Phaenon\Bytes`}, false},
		{"a docblock", "<?php\nnamespace App;\n/**\n * @param \\Phaenon\\Type<int> $t\n */\nfunction f($t) {}\n", []string{`line 4: \Phaenon\Type`}, false},
		{"comments and the map", "<?php\n// \\Phaenon\\Type\n/* Phaenon\\Bytes */\n// phaenon-map v1 a.phn 1-6=1 7:1\n", nil, false},
		{"a trait's use", "<?php\nnamespace App;\nfinal class A\n{\n    use Phaenon\\T;\n}\n", nil, false},
		{"a closure's use", "<?php\nnamespace App;\n$f = function () use ($x) { return \\strlen($x); };\n", nil, false},
		{"text that only mentions it", "<?php\nnamespace App;\n$s = 'see Phaenon\\\\Debug for more';\n", nil, false},
		{"a braced namespace", "<?php\nnamespace App {\n    use Phaenon\\X;\n}\nnamespace {\n    $y = Phaenon\\Y::class;\n}\n", []string{`line 3: \Phaenon\X`, `line 6: \Phaenon\Y`}, false},
		{"its own namespace", "<?php\nnamespace Phaenon\\Debug;\n\nuse Phaenon\\Debug\\LineMap;\n", nil, true},
	} {
		refs, own := phaenonReferences([]byte(c.src))
		if fmt.Sprint(refs) != fmt.Sprint(c.want) || own != c.own {
			t.Errorf("%s: got %q, own %v; want %q, own %v", c.name, refs, own, c.want, c.own)
		}
	}
}
