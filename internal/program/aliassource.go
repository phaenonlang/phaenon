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
// It asks once per name, and reads nothing for a name that is a class: one
// the program has, or one a loader's maps have a file for.
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
	read := false
	for _, l := range p.loaders {
		sl, ok := l.(SourceLoader)
		if !ok {
			continue
		}
		dirs := sl.SourceDirs(lower)
		if len(dirs) == 0 {
			continue
		}
		if !p.missing[lower] {
			if _, found := l.FindClass(lower); found {
				return read // a class, loaded when something needs it
			}
		}
		for _, dir := range dirs {
			if !s.read[dir] {
				s.read[dir] = true
				p.readTypeDeclarations(l, dir)
				read = true
			}
		}
	}
	return read
}

// declaresType reports what may be a `type Name = …;` or `type Name<T> =
// …;` declaration in a source, without parsing it: the word `type`, a
// name, then `=` or `<`. A file it reports is parsed, so a match in a
// comment or a string costs a parse and no more; a declaration it would
// miss has a comment between those tokens.
func declaresType(src []byte) bool {
	word := []byte("type")
	for at := 0; ; {
		i := bytes.Index(src[at:], word)
		if i < 0 {
			return false
		}
		start := at + i
		at = start + len(word)
		if start > 0 && nameByte(src[start-1]) {
			continue
		}
		j := skipBlank(src, at)
		if j == at || j >= len(src) || !nameByte(src[j]) || src[j] >= '0' && src[j] <= '9' {
			continue
		}
		for j < len(src) && nameByte(src[j]) {
			j++
		}
		if j = skipBlank(src, j); j < len(src) && (src[j] == '=' || src[j] == '<') {
			return true
		}
	}
}

func nameByte(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

func skipBlank(src []byte, i int) int {
	for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\r' || src[i] == '\n') {
		i++
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
