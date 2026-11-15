package program

import (
	"bytes"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/phaenonlang/phaenon/internal/lexer"
)

// The aliases of sources outside the program (design/generics.md §11.6):
// an installed Phaenon package, read from its .phn, and the rest of the
// project in a run over given paths. There a class is loaded when it is
// named, through the loaders' maps, and an alias has no file to be found
// by. So the first name the maps give no class for, under a PSR-4 prefix
// whose directories hold Phaenon sources, makes the program read the
// `type` declarations of those directories, once. The reading is paid only
// by a name that is not a class, which is an alias or a mistake; a class
// is not parsed to find that out, so the vendor code stays lazily loaded.

// SourceLoader is a Loader that knows which of its directories hold
// Phaenon sources.
type SourceLoader interface {
	Loader
	// SourceDirs lists the source directories of the PSR-4 prefixes the
	// lower-cased name falls under; none when they hold PHP.
	SourceDirs(lower string) []string
}

// SourceDirs implements SourceLoader: the source roots (Sources) behind the
// directories of every PSR-4 prefix of the name. A directory Composer's
// maps name without a source root holds a PHP package, which declares no
// alias.
func (l *ComposerLoader) SourceDirs(lower string) []string {
	// The prefixes that have a source root are few, the project's own and
	// those of its Phaenon packages, and the question is asked for every
	// name a .phn writes: they are found once.
	l.sourcesOnce.Do(func() {
		for _, prefix := range l.prefixes {
			var dirs []string
			for _, dir := range l.psr4[prefix] {
				if src, ok := l.source(dir); ok {
					dirs = append(dirs, src)
				}
			}
			if len(dirs) > 0 {
				l.sourcePrefixes = append(l.sourcePrefixes, sourcePrefix{strings.ToLower(prefix), dirs})
			}
		}
	})
	var out []string
	for _, sp := range l.sourcePrefixes {
		if strings.HasPrefix(lower, sp.lower) {
			out = append(out, sp.dirs...)
		}
	}
	return out
}

// sourcePrefix is a PSR-4 prefix, lower-cased, whose directories have
// source roots.
type sourcePrefix struct {
	lower string
	dirs  []string
}

// SourceDirs implements SourceLoader: the root itself.
func (l DirLoader) SourceDirs(lower string) []string {
	if !strings.HasPrefix(lower, strings.ToLower(l.Prefix)) {
		return nil
	}
	return []string{l.Dir}
}

// aliasSources is what the program has read of the sources outside it.
type aliasSources struct {
	// asked are the lower-cased names the loaders were asked for.
	asked map[string]bool
	// read are the directories whose `type` declarations were read.
	read map[string]bool
	// abs holds the absolute path of each of the first seen files of the
	// program: a file the program already has is not read again under
	// another spelling of its path.
	abs  map[string]bool
	seen int
}

// findAlias looks for the alias a lower-cased qualified name may stand for
// in the sources outside the program, and reports whether any were read.
// It asks once per name, and reads nothing for a name that is a class.
//
// A name under a prefix with sources is first loaded as a class is, from
// the file the maps give it: that file may declare the class, or be where
// the alias of that name was put, and only its declarations say which. A
// name that is neither then has the `type` declarations of the prefix's
// directories read. So what a name stands for does not depend on which
// names were met before it, nor on how its alias's file is called.
func (p *Program) findAlias(lower string) bool {
	s := &p.aliases.sources
	if len(p.loaders) == 0 || s.asked[lower] {
		return false
	}
	if s.asked == nil {
		s.asked, s.read = map[string]bool{}, map[string]bool{}
	}
	s.asked[lower] = true
	if _, declared := p.classes[lower]; declared {
		return false
	}
	if _, aliased := p.classAliases[lower]; aliased {
		return false
	}
	type source struct {
		l    Loader
		dirs []string
	}
	var sources []source
	for _, l := range p.loaders {
		if sl, ok := l.(SourceLoader); ok {
			if dirs := sl.SourceDirs(lower); len(dirs) > 0 {
				sources = append(sources, source{l, dirs})
			}
		}
	}
	if len(sources) == 0 {
		return false
	}
	if p.Class(lower) != nil {
		return false
	}
	if p.aliases.byName[lower] != nil {
		return true // declared in the file the maps give the name
	}
	read := false
	for _, src := range sources {
		for _, dir := range src.dirs {
			if !s.read[dir] {
				s.read[dir] = true
				p.readTypeDeclarations(src.l, dir)
				read = true
			}
		}
	}
	return read
}

// declaresType reports whether a source may declare a type alias, without
// parsing it. It errs on one side only: a file it reports is parsed, so a
// match in a comment or a string costs a parse and no more, and it reports
// every source the parser reads a declaration in. A declaration is the
// tokens the parser takes for one, `type`, a name, then `=` or the `<` of
// its type parameters, the keyword in any letter case as the lexer reads
// it, with the blanks and the comments the lexer passes over between them.
func declaresType(src []byte) bool {
	for at := 0; at+4 <= len(src); at++ {
		if src[at]|0x20 != 't' || src[at+1]|0x20 != 'y' || src[at+2]|0x20 != 'p' || src[at+3]|0x20 != 'e' {
			continue
		}
		if at > 0 && nameByte(src[at-1]) {
			continue // the end of a longer word, or a variable
		}
		j := skipTrivia(src, at+4)
		if j == at+4 || j >= len(src) || !nameByte(src[j]) || src[j] == '$' || src[j] >= '0' && src[j] <= '9' {
			continue
		}
		for j < len(src) && nameByte(src[j]) && src[j] != '$' {
			j++
		}
		if j = skipTrivia(src, j); j < len(src) && (src[j] == '=' || src[j] == '<') {
			return true
		}
	}
	return false
}

func nameByte(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

// skipTrivia is the offset after the blanks and comments at i: what the
// lexer leaves out between two tokens. A comment that is not closed runs
// to the end.
func skipTrivia(src []byte, i int) int {
	for i < len(src) {
		switch c := src[i]; {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := bytes.Index(src[i+2:], []byte("*/"))
			if end < 0 {
				return len(src)
			}
			i += 2 + end + 2
		case c == '/' && i+1 < len(src) && src[i+1] == '/', c == '#' && !(i+1 < len(src) && src[i+1] == '['):
			end := bytes.IndexByte(src[i:], '\n')
			if end < 0 {
				return len(src)
			}
			i += end + 1
		default:
			return i
		}
	}
	return i
}

// readTypeDeclarations adds the .phn files under dir that declare a type
// alias and that the program does not hold yet, as vendor code is added:
// unchecked, their classes declared with their aliases.
func (p *Program) readTypeDeclarations(l Loader, dir string) {
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if name := d.Name(); path != dir && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".phn" || p.holds(path) {
			return nil
		}
		src, err := l.Read(path)
		if err != nil || !declaresType(src) {
			return nil
		}
		p.AddFile(path, src, lexer.ModePhaenon, false) // a file that does not parse declares nothing
		return nil
	})
}

// holds reports whether the program has the file at path, under this
// spelling of the path or another.
func (p *Program) holds(path string) bool {
	if _, ok := p.files[path]; ok {
		return true
	}
	s := &p.aliases.sources
	if s.abs == nil {
		s.abs = map[string]bool{}
	}
	for ; s.seen < len(p.order); s.seen++ {
		if f := p.order[s.seen]; !f.IsStub() {
			if abs, err := filepath.Abs(f.Path); err == nil {
				s.abs[abs] = true
			}
		}
	}
	abs, err := filepath.Abs(path)
	return err == nil && s.abs[abs]
}
