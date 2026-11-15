package migrate

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Lifting the aliases PHP code already has (design/collections.md §6.6,
// generics.md §11.6).
//
// `@phpstan-type Row array{…}` (`@psalm-type` and the `=` form too) becomes
// `type Row = array{…};` before the class that carried the tag, and
// `@phpstan-import-type Row from Orders` becomes `use Other\Namespace\Row;`,
// or nothing when Orders is of the same namespace. Its right-hand side is
// the one place a shape with named keys has a Phaenon spelling.
//
// PHPStan's aliases are scoped to a class; Phaenon's belong to a
// namespace. So a name that is taken there stays a tag, read as before,
// with a note that names the other declaration: a class-like of the
// namespace, a `type` it already declares, another class's alias of that
// name. Which of two declarations is renamed is a decision for a person.
// An alias is lifted with the aliases it names, or not at all, and an
// import only when the alias it imports was.
//
// The decision is made on the program model read before the migration, so
// that the file that declares an alias and a file that imports it decide
// the same, each on its own lift. Without a model nothing is lifted: the
// namespace's other declarations are not known.

// aliasDecision is what the lift does with one alias of a class: text is
// the right-hand side in Phaenon's spelling when it is lifted, why the
// reason when it stays a tag.
type aliasDecision struct {
	text string
	why  string
}

// migrating reports whether a file of the model is rewritten by this run.
func (l *lifter) migrating(f *program.File) bool {
	return f != nil && (f == l.pfile || l.files[f])
}

// namespaceOf is the namespace of a qualified name, "" for a global one.
func namespaceOf(name string) string {
	if i := strings.LastIndexByte(name, '\\'); i >= 0 {
		return name[:i]
	}
	return ""
}

func qualify(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "\\" + name
}

// ownAlias is the @phpstan-type tag of a class under a name, as PHPStan
// matches it: by case.
func ownAlias(c *program.Class, name string) *docblock.TypeAliasTag {
	if c.Doc == nil {
		return nil
	}
	for i := range c.Doc.TypeAliases {
		if c.Doc.TypeAliases[i].Name == name {
			return &c.Doc.TypeAliases[i]
		}
	}
	return nil
}

// importedAlias is the @phpstan-import-type tag that gives a class a name.
func importedAlias(c *program.Class, name string) *docblock.ImportTypeTag {
	if c.Doc == nil {
		return nil
	}
	for i := range c.Doc.ImportTypes {
		it := &c.Doc.ImportTypes[i]
		if it.Alias == name || it.Alias == "" && it.Name == name {
			return it
		}
	}
	return nil
}

// decideAlias decides the lift of the alias a class declares under name.
// visiting holds the aliases whose decision led here: one that names
// itself has no declaration.
func (l *lifter) decideAlias(c *program.Class, name string, visiting map[string]bool) aliasDecision {
	c.Signature()
	tag := ownAlias(c, name)
	if tag == nil {
		return aliasDecision{why: fmt.Sprintf("%s declares no such alias", c.Name)}
	}
	if !l.migrating(c.File) {
		return aliasDecision{why: fmt.Sprintf("%s is not among the files being migrated", c.Name)}
	}
	key := strings.ToLower(c.Name) + "::" + name
	if visiting[key] {
		return aliasDecision{why: "it names itself"}
	}
	if why := l.nameTaken(c, name); why != "" {
		return aliasDecision{why: why}
	}
	if importer := l.importerOutside(c, name); importer != nil {
		return aliasDecision{why: fmt.Sprintf("%s imports it and is not among the files being migrated", importer.Name)}
	}
	visiting[key] = true
	defer delete(visiting, key)
	for _, n := range typeNames(tag.Type) {
		if strings.Contains(n, "\\") {
			continue
		}
		for _, tp := range c.TypeParams {
			if tp.Name == n {
				return aliasDecision{why: fmt.Sprintf("it mentions %s, a type parameter of %s", n, c.Name)}
			}
		}
		switch {
		case n == name:
			return aliasDecision{why: "it names itself"}
		case ownAlias(c, n) != nil:
			if d := l.decideAlias(c, n, visiting); d.why != "" {
				return aliasDecision{why: fmt.Sprintf("it names %s, which stays a tag", n)}
			}
		case importedAlias(c, n) != nil:
			if _, why := l.decideImport(c, importedAlias(c, n), visiting); why != "" {
				return aliasDecision{why: fmt.Sprintf("it names %s, which stays a tag", n)}
			}
		}
	}
	text, ok := renderType(tag.Type, true)
	if !ok {
		return aliasDecision{why: "no Phaenon spelling for it"}
	}
	return aliasDecision{text: text}
}

// nameTaken says why the name of a class's alias cannot be a `type` of the
// class's namespace, "" when it is free.
func (l *lifter) nameTaken(c *program.Class, name string) string {
	ns := namespaceOf(c.Name)
	full := qualify(ns, name)
	if other := l.prog.Class(full); other != nil {
		return fmt.Sprintf("the %s %s bears the name", kindOf(other), other.Name)
	}
	if a := l.prog.TypeAlias(full); a != nil {
		return fmt.Sprintf("its namespace already declares type %s", a.Name)
	}
	if _, imported := c.Scope.Imports()[strings.ToLower(name)]; imported {
		return fmt.Sprintf("its file imports another %s", name)
	}
	if c.Doc != nil {
		for _, t := range c.Doc.TypeAliases {
			if t.Name != name && strings.EqualFold(t.Name, name) {
				return fmt.Sprintf("the class declares %s too, and the names of a namespace are compared without regard to case", t.Name)
			}
		}
	}
	for _, other := range l.prog.Classes() {
		if other == c || other.File.IsStub() || !strings.EqualFold(namespaceOf(other.Name), ns) {
			continue
		}
		other.Signature()
		if other.Doc == nil {
			continue
		}
		for _, t := range other.Doc.TypeAliases {
			if strings.EqualFold(t.Name, name) {
				return fmt.Sprintf("%s declares a %s too, and a namespace declares a name once", other.Name, t.Name)
			}
		}
	}
	return ""
}

// importerOutside is a class the run does not rewrite whose docblock
// imports the alias a class declares under name, nil without one. Its
// @phpstan-import-type would name an alias the class no longer declares,
// so the alias stays a tag, as an import stays one whose alias did.
func (l *lifter) importerOutside(c *program.Class, name string) *program.Class {
	for _, other := range l.prog.Classes() {
		if other == c || other.File.IsStub() || l.migrating(other.File) {
			continue
		}
		other.Signature()
		if other.Doc == nil {
			continue
		}
		for _, it := range other.Doc.ImportTypes {
			if it.Name == name && l.prog.Class(other.Scope.ResolveClassString(it.From.Name)) == c {
				return other
			}
		}
	}
	return nil
}

func kindOf(c *program.Class) string {
	switch c.Kind {
	case ast.KindInterface:
		return "interface"
	case ast.KindTrait:
		return "trait"
	case ast.KindEnum:
		return "enum"
	}
	return "class"
}

// decideImport decides the lift of a @phpstan-import-type tag of class c:
// use is the import that replaces it, "" when the name needs none (an
// alias of c's own namespace, under its own name); why is the reason it
// stays a tag.
func (l *lifter) decideImport(c *program.Class, it *docblock.ImportTypeTag, visiting map[string]bool) (use, why string) {
	from := l.prog.Class(c.Scope.ResolveClassString(it.From.Name))
	if from == nil {
		return "", fmt.Sprintf("the class %s is not known", it.From.Name)
	}
	from.Signature()
	target := qualify(namespaceOf(from.Name), it.Name)
	if ownAlias(from, it.Name) != nil {
		if d := l.decideAlias(from, it.Name, visiting); d.why != "" {
			return "", fmt.Sprintf("the alias it imports stays a tag (%s)", d.why)
		}
	} else if a := l.prog.TypeAlias(target); a != nil {
		target = a.Name // lifted by an earlier migration
	} else {
		return "", fmt.Sprintf("%s declares no alias %s", from.Name, it.Name)
	}
	local := it.Name
	if it.Alias != "" {
		local = it.Alias
	}
	ns := namespaceOf(c.Name)
	if local == it.Name && strings.EqualFold(namespaceOf(target), ns) {
		return "", "" // a name of the class's own namespace
	}
	// The import gives the whole file the name the tag gave the class.
	if other, imported := c.Scope.Imports()[strings.ToLower(local)]; imported && !strings.EqualFold(other, target) {
		return "", fmt.Sprintf("its file imports another %s", local)
	}
	if !strings.EqualFold(qualify(ns, local), target) {
		if other := l.prog.Class(qualify(ns, local)); other != nil {
			return "", fmt.Sprintf("the %s %s bears the name %s", kindOf(other), other.Name, local)
		}
		if a := l.prog.TypeAlias(qualify(ns, local)); a != nil {
			return "", fmt.Sprintf("its namespace already declares type %s", a.Name)
		}
		if ownAlias(c, local) != nil {
			return "", fmt.Sprintf("the class declares a %s of its own", local)
		}
	}
	use = "use " + target
	if local != it.Name {
		use += " as " + local
	}
	return use + ";", ""
}

// liftAliases lifts the @phpstan-type and @phpstan-import-type tags of a
// class docblock: the declarations go before the docblock, the imports
// with the file's own, and sc learns the names a signature may now be
// lifted with.
func (l *lifter) liftAliases(c *ast.ClassDecl, d *doc, sc scope) {
	var decls []string
	for _, g := range d.groups {
		if g.name != "@type" && g.name != "@import-type" {
			continue
		}
		off := d.start + lineOffset(d, g.first)
		if l.class == nil {
			l.note(off, "kept %s: whether its name is free in the namespace is not known without the program's other files", g.tag)
			continue
		}
		switch g.name {
		case "@type":
			if len(g.parsed.TypeAliases) != 1 {
				l.note(off, "kept %s: unreadable", g.tag)
				continue
			}
			tag := g.parsed.TypeAliases[0]
			if rest := aliasProse(g.text, tag.Name); rest != "" {
				l.note(off, "kept %s %s: it has a description", g.tag, tag.Name)
				continue
			}
			dec := l.decideAlias(l.class, tag.Name, map[string]bool{})
			if dec.why != "" {
				l.note(off, "kept %s %s: %s", g.tag, tag.Name, dec.why)
				continue
			}
			decls = append(decls, "type "+tag.Name+" = "+dec.text+";")
			sc[tag.Name] = l.docAtoms(tag.Type, sc)
			d.remove(g)
		case "@import-type":
			if len(g.parsed.ImportTypes) != 1 {
				l.note(off, "kept %s: unreadable", g.tag)
				continue
			}
			it := g.parsed.ImportTypes[0]
			use, why := l.decideImport(l.class, &it, map[string]bool{})
			if why != "" {
				l.note(off, "kept %s %s: %s", g.tag, it.Name, why)
				continue
			}
			if use != "" {
				l.addUse(use, d.start)
			}
			local := it.Name
			if it.Alias != "" {
				local = it.Alias
			}
			if from := l.prog.Class(l.class.Scope.ResolveClassString(it.From.Name)); from != nil {
				if tag := ownAlias(from, it.Name); tag != nil {
					sc[local] = l.docAtoms(tag.Type, scope{})
				} else if a := l.prog.TypeAlias(qualify(namespaceOf(from.Name), it.Name)); a != nil {
					sc[local] = typeAtoms(a.Type)
				}
			}
			d.remove(g)
		}
	}
	if len(decls) == 0 {
		return
	}
	indent := l.src[bytesLineStart(l.src, d.start):d.start]
	l.insert(d.start, strings.Join(decls, "\n"+string(indent))+"\n\n"+string(indent))
}

// aliasProse is what a @phpstan-type tag says after its type.
func aliasProse(text, name string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), name))
	rest = strings.TrimSpace(strings.TrimPrefix(rest, "="))
	_, after, err := docblock.ParseTypePrefix(rest)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(after)
}

func bytesLineStart(src []byte, off int) int {
	for off > 0 && src[off-1] != '\n' {
		off--
	}
	return off
}

// addUse writes an import among the file's own: after the last use
// statement of the namespace, else after the namespace statement, else
// before the declaration at before. One the file already has is not
// written again.
func (l *lifter) addUse(use string, before int) {
	if l.added[use] {
		return
	}
	if l.added == nil {
		l.added = map[string]bool{}
	}
	l.added[use] = true
	switch {
	case l.lastUse > 0:
		l.insert(l.lastUse, "\n"+use)
	case l.nsEnd > 0:
		l.insert(l.nsEnd, "\n\n"+use)
	default:
		l.insert(before, use+"\n\n")
	}
}

// typeNames lists the names a docblock type mentions, the entries of its
// shapes included.
func typeNames(t *docblock.TypeNode) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(n *docblock.TypeNode)
	walk = func(n *docblock.TypeNode) {
		if n == nil {
			return
		}
		if n.Kind == docblock.KindNamed && !seen[n.Name] {
			seen[n.Name] = true
			out = append(out, n.Name)
		}
		for _, a := range n.Args {
			walk(a)
		}
		for _, e := range n.Entries {
			walk(e.Type)
		}
		for _, p := range n.Params {
			walk(p.Type)
		}
		for _, m := range []*docblock.TypeNode{n.Return, n.Subject, n.Target, n.Then, n.Else} {
			walk(m)
		}
	}
	walk(t)
	return out
}

// renderShape spells a docblock's array{…} with named keys as the shape
// of a `type` declaration (design/collections.md §6.1): every entry
// keyed, `?` for an optional key, `...` last for an unsealed one. The
// keys are the docblock's, a key that is no name quoted.
func renderShape(n *docblock.TypeNode) (string, bool) {
	if !strings.EqualFold(n.Name, "array") {
		return "", false // list{…}, object{…}, non-empty-array{…}
	}
	var parts []string
	for _, e := range n.Entries {
		if !e.HasKey {
			return "", false
		}
		key, ok := shapeKey(e)
		if !ok {
			return "", false
		}
		t, ok := renderType(e.Type, true)
		if !ok {
			return "", false
		}
		if e.Optional {
			key += "?"
		}
		parts = append(parts, key+": "+t)
	}
	if n.Unsealed {
		parts = append(parts, "...")
	}
	return "array{" + strings.Join(parts, ", ") + "}", true
}

// shapeKey spells a docblock shape's key for the syntax: an integer, a
// name, or the text in quotes, which the syntax takes as written.
func shapeKey(e docblock.ShapeEntry) (string, bool) {
	if e.IsInt {
		return strconv.FormatInt(e.IntKey, 10), true
	}
	name := e.Key != ""
	for i := 0; i < len(e.Key); i++ {
		c := e.Key[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80 || c >= '0' && c <= '9' && i > 0) {
			name = false
		}
	}
	switch {
	case name:
		return e.Key, true
	case !strings.ContainsAny(e.Key, `'\`):
		return "'" + e.Key + "'", true
	case !strings.ContainsAny(e.Key, `"\$`):
		return `"` + e.Key + `"`, true
	}
	return "", false // it would need an escape sequence, which a key has none of
}

// typeAtoms is the erasure of a resolved type: the native PHP types the
// emitter writes for it.
func typeAtoms(t types.Type) atoms {
	out := atoms{}
	native, ok := types.Native(t)
	if !ok {
		native = "mixed"
	}
	if rest, nullable := strings.CutPrefix(native, "?"); nullable {
		native = rest
		out["null"] = true
	}
	for _, part := range strings.Split(native, "|") {
		out[strings.ToLower(strings.TrimPrefix(part, "\\"))] = true
	}
	return normalise(out)
}
