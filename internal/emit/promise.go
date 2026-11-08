package emit

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/token"
)

// The compiled output depends on nothing but PHP (design/roadmap.md D24):
// it names no class, function or constant under Phaenon\. The tests hold
// every output to it through PhaenonReferences: the emitter's goldens and
// corpus here, a dogfood tree (TestLineMapTree), and a project the
// Composer plugin builds (internal/distribution).

// reDocName finds a qualified name in a docblock's text, where PHPStan
// resolves it as PHP resolves a class name in code.
var reDocName = regexp.MustCompile(`\\?[A-Za-z_\x80-\xff][A-Za-z0-9_\x80-\xff]*(?:\\[A-Za-z_\x80-\xff][A-Za-z0-9_\x80-\xff]*)+`)

// reClassString is a string whose text, around its blanks, is a name under
// Phaenon\ — a class-name string — or a member of one, a callable string
// ('Phaenon\Type::of'). PHP reads either as fully qualified.
var reClassString = regexp.MustCompile(`^\\?(Phaenon\\[A-Za-z_\x80-\xff][A-Za-z0-9_\x80-\xff]*(?:\\[A-Za-z_\x80-\xff][A-Za-z0-9_\x80-\xff]*)*)(?:::[A-Za-z_\x80-\xff][A-Za-z0-9_\x80-\xff]*)?$`)

// PhaenonReferences lists the names under Phaenon\ that a PHP file refers
// to, each as "line N: \Phaenon\…", resolved as PHP resolves them: a fully
// qualified name as written, a qualified one through the file's imports or
// its namespace (`Phaenon\X` in namespace App is App\Phaenon\X), every
// import, a class-name or callable string of any kind (quoted, interpolated,
// heredoc, nowdoc), and the qualified names of docblocks. Comments other
// than docblocks, the line map among them, are not code.
//
// A namespace block whose namespace is Phaenon or under it is not scanned:
// the stdlib's Debug sources, compiled into phaenon/debug, refer to each
// other. exempt reports that the file has such a block; the blocks of other
// namespaces in the same file are scanned all the same.
func PhaenonReferences(src []byte) (refs []string, exempt bool) {
	var toks []token.Token
	for _, tk := range lexer.Tokenize(src, lexer.ModePHP) {
		if k := tk.Kind; k != token.T_WHITESPACE && k != token.T_COMMENT {
			toks = append(toks, tk)
		}
	}
	text := func(i int) string { return toks[i].Text(src) }
	ns := ""                       // the current namespace, "" for the global one
	own := false                   // the current namespace is under Phaenon\: not scanned
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
	// classString reports the string text s, starting on line, when it
	// names something under Phaenon\.
	classString := func(line int, s string) {
		s = strings.ReplaceAll(s, `\\`, `\`)
		trimmed := strings.TrimSpace(s)
		if m := reClassString.FindStringSubmatch(trimmed); m != nil {
			report(line+strings.Count(s[:strings.Index(s, trimmed)], "\n"), m[1])
		}
	}
	for i := 0; i < len(toks); i++ {
		tk := toks[i]
		switch tk.Kind {
		case token.Kind('{'), token.T_CURLY_OPEN, token.T_DOLLAR_OPEN_CURLY_BRACES:
			depth++
			continue
		case token.Kind('}'):
			depth--
			continue
		case token.T_NAMESPACE:
			name := ""
			if i+1 < len(toks) && (toks[i+1].Kind == token.T_STRING || toks[i+1].Kind == token.T_NAME_QUALIFIED) {
				i++
				name = text(i)
			}
			ns, aliases, nsDepth = name, map[string]string{}, depth
			if i+1 < len(toks) && toks[i+1].Kind == token.Kind('{') {
				nsDepth = depth + 1
			}
			own = name == "Phaenon" || under(name)
			exempt = exempt || own
			continue
		}
		if own {
			continue
		}
		switch tk.Kind {
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
			s := strings.TrimLeft(text(i), "bB")
			if len(s) >= 2 {
				classString(tk.Line, s[1:len(s)-1])
			}
		case token.T_ENCAPSED_AND_WHITESPACE:
			// The text of an interpolated string, a heredoc or a nowdoc.
			classString(tk.Line, text(i))
		case token.T_DOC_COMMENT:
			doc := text(i)
			for _, m := range reDocName.FindAllStringIndex(doc, -1) {
				if full := resolve(doc[m[0]:m[1]]); under(full) {
					report(tk.Line+strings.Count(doc[:m[0]], "\n"), full)
				}
			}
		}
	}
	return refs, exempt
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
		kind = toks[0].Text(src)
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
