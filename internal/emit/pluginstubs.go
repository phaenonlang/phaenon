package emit

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/stdlib"
)

// PluginStubs renders the editor-only pseudo-classes the PhpStorm plugin
// bundles, keyed by file name ("ListMethods.php"): every static function
// of Seq, Map, Str and Bytes (stdlib/runtime/*.phn) as an instance method
// without the receiver parameter, with PHPStan docblocks. The plugin adds
// the pseudo-class to every list, map, string or bytes value, so that the
// IDE completes, resolves and types `$xs->map($f)`; the compiler keeps
// writing `Seq::map($xs, $f)`. The stdlib sources are the one source of
// these signatures: `phn stdlib --plugin-stubs DIR` materialises them.
func PluginStubs() (map[string][]byte, error) {
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, ps := range pseudoClasses {
		var src []byte
		var f *program.File
		for _, s := range stdlib.Runtime() {
			if filepath.Base(s.Path) == ps.runtime+".phn" {
				src = s.Src
				f = p.File("<stub>/" + s.Path)
			}
		}
		if f == nil {
			return nil, fmt.Errorf("stdlib/runtime/%s.phn is not embedded", ps.runtime)
		}
		text, err := ps.render(p, f, src)
		if err != nil {
			return nil, err
		}
		out[ps.name+".php"] = []byte(text)
	}
	return out, nil
}

// pseudoClass describes one bundled pseudo-class.
type pseudoClass struct {
	runtime   string   // the stdlib class: "Seq"
	name      string   // the pseudo-class: "ListMethods"
	kind      string   // the receiver's kind as typeKind spells it: "list"
	templates []string // the receiver's type parameters, the pseudo-class's own: T, or K and V
	// summary opens the file comment; classDoc is the class docblock's
	// body; implements is the clause after the class name; members closes
	// the class (the interface methods the plugin's half-array type needs).
	summary    string
	classDoc   []string
	implements string
	members    string
}

var pseudoClasses = []pseudoClass{
	{
		runtime: "Seq", name: "ListMethods", kind: "list", templates: []string{"T"},
		summary: "a Phaenon `list<T>` is a PHP array, and `$xs->map($f)` compiles to\n * `\\Phaenon\\Seq::map($xs, $f)`. The plugin adds this type to every `list<T>` value so that the\n * IDE can complete, resolve and type the method form.",
		classDoc: []string{
			"The `@return` tags name the collection half and the pseudo-class without its type argument:",
			"PhpStorm substitutes `T` in `list<T>` but not in `ListMethods<T>`, and the plugin's type",
			"provider adds the parametrised pseudo-class to every call of these methods itself.",
			"",
			"@template T",
			"@implements \\IteratorAggregate<int, T>",
			"@implements \\ArrayAccess<int, T>",
		},
		implements: " implements \\IteratorAggregate, \\ArrayAccess, \\Countable",
		members: `
    /** @return \Iterator<int, T> */
    public function getIterator(): \Iterator {}

    public function offsetExists(mixed $offset): bool {}

    /** @return T */
    public function offsetGet(mixed $offset): mixed {}

    public function offsetSet(mixed $offset, mixed $value): void {}

    public function offsetUnset(mixed $offset): void {}
`,
	},
	{
		runtime: "Map", name: "MapMethods", kind: "map", templates: []string{"K", "V"},
		summary: "a Phaenon `map<K, V>` is a PHP array, and `$m->keys()` compiles to\n * `\\Phaenon\\Map::keys($m)`. The plugin adds this type to every `map<K, V>` value so that the IDE\n * can complete, resolve and type the method form.",
		classDoc: []string{
			"The `@return` tags name the collection half and the pseudo-class without its type arguments:",
			"PhpStorm substitutes the templates in `array<K, V>` but not in `MapMethods<K, V>`, and the",
			"plugin's type provider adds the parametrised pseudo-class to every call of these methods itself.",
			"",
			"@template K of int|string",
			"@template V",
			"@implements \\IteratorAggregate<K, V>",
			"@implements \\ArrayAccess<K, V>",
		},
		implements: " implements \\IteratorAggregate, \\ArrayAccess, \\Countable",
		members: `
    /** @return \Iterator<K, V> */
    public function getIterator(): \Iterator {}

    public function offsetExists(mixed $offset): bool {}

    /** @return V */
    public function offsetGet(mixed $offset): mixed {}

    public function offsetSet(mixed $offset, mixed $value): void {}

    public function offsetUnset(mixed $offset): void {}
`,
	},
	{
		runtime: "Str", name: "StrMethods", kind: "string",
		summary:    "a Phaenon `string` is a PHP string, and `$s->upper()` compiles to\n * `\\Phaenon\\Str::upper($s)`. The plugin adds this type to every string value (and to `\\Stringable`\n * objects, whose own methods come first) so that the IDE can complete, resolve and type the method form.",
		implements: " implements \\Stringable",
		members: `
    public function __toString(): string {}
`,
	},
	{
		runtime: "Bytes", name: "BytesMethods", kind: "bytes",
		summary: "a Phaenon `bytes` value is a PHP string, and `$raw->hex()` compiles to\n * `\\Phaenon\\Bytes::hex($raw)`. The plugin adds this type to every `bytes` value so that the IDE can\n * complete, resolve and type the method form; the constructors `Bytes::fromHex()` and\n * `Bytes::fromBase64()` have no method form.",
	},
}

// render writes the pseudo-class for the runtime class declared in f.
func (ps pseudoClass) render(p *program.Program, f *program.File, src []byte) (string, error) {
	var decl *ast.ClassDecl
	for _, s := range f.AST.Stmts {
		ast.Inspect(s, func(n ast.Node) bool {
			if c, ok := n.(*ast.ClassDecl); ok && c.Name != nil && c.Name.Name == ps.runtime {
				decl = c
			}
			return decl == nil
		})
	}
	if decl == nil {
		return "", fmt.Errorf("stdlib/runtime/%s.phn declares no class %s", ps.runtime, ps.runtime)
	}
	cl := p.Class("Phaenon\\" + ps.runtime)
	if cl == nil {
		return "", fmt.Errorf("class Phaenon\\%s is not loaded", ps.runtime)
	}
	cl.Signature()
	l := &lowering{e: &emitter{src: src, opts: Options{Program: p, File: f}}, p: p, f: f, bare: map[ast.Expr]bool{}}
	own := map[string]bool{}
	for _, t := range ps.templates {
		own[t] = true
	}

	var b strings.Builder
	b.WriteString("<?php\n\n/*\n * @generated by phaenon (phn stdlib --plugin-stubs) from stdlib/runtime/" + ps.runtime + ".phn.\n *\n")
	b.WriteString(" * Editor-only declaration, bundled with the Phaenon plugin for PhpStorm. This class does not\n * exist at runtime: " + ps.summary + "\n")
	b.WriteString(" * The methods mirror `\\Phaenon\\" + ps.runtime + "` without the receiver parameter.\n */\n\nnamespace Phaenon\\Internal;\n\n")
	if len(ps.classDoc) > 0 {
		b.WriteString("/**\n")
		for _, line := range ps.classDoc {
			b.WriteString(strings.TrimRight(" * "+line, " ") + "\n")
		}
		b.WriteString(" */\n")
	}
	b.WriteString("final class " + ps.name + ps.implements + "\n{\n    private function __construct()\n    {\n    }\n")

	for _, m := range decl.Members {
		md, ok := m.(*ast.MethodDecl)
		if !ok || md.Modifiers&ast.ModStatic == 0 || len(md.Params) == 0 {
			continue
		}
		recv := md.Params[0]
		if typeKind(recv.Type) != ps.kind {
			continue // a constructor (Bytes::fromHex) or another kind's helper
		}
		sc := l.topScope(decl.S.Start).child()
		sc.class = cl
		for _, tp := range md.TypeParams {
			sc.params[tp.Name.Name] = tp
		}
		var tags []string
		for _, tp := range md.TypeParams {
			if own[tp.Name.Name] {
				continue
			}
			tag := "@template " + tp.Name.Name
			if tp.Bound != nil {
				tag += " of " + l.docType(tp.Bound, sc)
			}
			tags = append(tags, tag)
		}
		var params []string
		rest := md.Params[1:]
		if recv.Variadic {
			rest = md.Params // `concat(list<T> ...$lists)`: the receiver is the first of them
		}
		for _, pd := range rest {
			erased := l.erase(pd.Type, sc)
			doc := l.docType(pd.Type, sc)
			if pd.Type != nil && doc != erased {
				name := "$" + pd.Var.Name
				if pd.Variadic {
					name = "..." + name
				}
				tags = append(tags, "@param "+doc+" "+name)
			}
			param := erased
			if param != "" {
				param += " "
			}
			if pd.ByRef {
				param += "&"
			}
			if pd.Variadic {
				param += "..."
			}
			param += "$" + pd.Var.Name
			if pd.Default != nil {
				sp := pd.Default.Span()
				param += " = " + string(src[sp.Start:sp.End])
			}
			params = append(params, param)
		}
		ret := ""
		if md.ReturnType != nil {
			erased := l.erase(md.ReturnType, sc)
			doc := l.docType(md.ReturnType, sc)
			switch {
			case typeKind(md.ReturnType) == ps.kind && ps.kind != "string":
				// The collection half and the pseudo-class: `list<U>|ListMethods`.
				tags = append(tags, "@return "+doc+"|"+ps.name)
			case doc != erased:
				tags = append(tags, "@return "+doc)
			}
			ret = ": " + erased
		}
		for _, line := range docLines(md.Doc) {
			if strings.HasPrefix(line, "@throws") {
				tags = append(tags, line)
			}
		}
		tags = append(tags, "@see \\Phaenon\\"+ps.runtime+"::"+md.Name.Name+"()")
		summary := docSummary(md.Doc)

		b.WriteString("\n")
		if summary == "" && len(tags) == 1 {
			b.WriteString("    /** " + tags[0] + " */\n")
		} else {
			b.WriteString("    /**\n")
			if summary != "" {
				b.WriteString("     * " + summary + "\n")
			}
			for _, t := range tags {
				b.WriteString("     * " + t + "\n")
			}
			b.WriteString("     */\n")
		}
		b.WriteString("    public function " + md.Name.Name + "(" + strings.Join(params, ", ") + ")" + ret + " {}\n")
	}
	b.WriteString(ps.members)
	b.WriteString("}\n")
	return b.String(), nil
}

// typeKind names the receiver kind a Phaenon type belongs to: "list",
// "map", "bytes", "string" (also for `string|\Stringable`), else "".
func typeKind(t ast.TypeExpr) string {
	switch t := t.(type) {
	case *ast.NamedType:
		switch strings.ToLower(t.Name.Parts[len(t.Name.Parts)-1]) {
		case "list", "map", "bytes", "string":
			return strings.ToLower(t.Name.Parts[len(t.Name.Parts)-1])
		}
	case *ast.NullableType:
		return typeKind(t.Type)
	case *ast.UnionType:
		kinds := map[string]bool{}
		for _, m := range t.Types {
			kinds[typeKind(m)] = true
		}
		if kinds["string"] && len(kinds) <= 2 {
			return "string" // string|\Stringable
		}
	}
	return ""
}

// docLines returns the lines of a docblock without its frame.
func docLines(doc string) []string {
	var out []string
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "/**")
		line = strings.TrimSuffix(line, "*/")
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// docSummary is the prose of a docblock (the lines before its tags), on one line.
func docSummary(doc string) string {
	var prose []string
	for _, line := range docLines(doc) {
		if strings.HasPrefix(line, "@") {
			break
		}
		prose = append(prose, line)
	}
	return strings.Join(prose, " ")
}

// sortedKeys lists a map's keys in order (for stable output and tests).
func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
