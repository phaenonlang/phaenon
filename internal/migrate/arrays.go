package migrate

// The arrays rule (design/collections.md §5): a declared `array` or
// `iterable` that names no element type, and takes none from the method it
// overrides, is written `array<mixed>` (`iterable<mixed>`), which is what
// the checker already makes of it. P2516 reports the bare form in a .phn;
// the explicit one says what is known and is easy to search for. It runs
// after the codemods, which narrow first what they can prove, and finds its
// sites through the checker, so it writes `<mixed>` exactly where P2516
// would report.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/program"
)

// Arrays writes `<mixed>` after the element-less arrays and iterables of
// files' declarations. load builds the program model, as for Pipes. It
// returns each file's source, rewritten or as it was, and how many types it
// wrote.
func Arrays(files []PipeFile, load func(srcs map[string][]byte, checked map[string]bool) (*program.Program, error)) ([][]byte, int, error) {
	out := make([][]byte, len(files))
	srcs := map[string][]byte{}
	watch := map[string]bool{}
	for i, f := range files {
		out[i] = f.Src
		srcs[f.Path] = f.Src
		if tree, err := parser.Parse(f.Src, lexer.ModePhaenon); err == nil && declaresBareArray(tree) {
			watch[f.Path] = true
		}
	}
	if len(watch) == 0 || load == nil {
		return out, 0, nil
	}
	p, err := load(srcs, watch)
	if err != nil {
		return nil, 0, err
	}
	at := map[string][]int{}
	checker.New(p, checker.Options{Elementless: func(f *program.File, t *ast.NamedType) {
		if f != nil && watch[f.Path] {
			at[f.Path] = append(at[f.Path], t.S.End)
		}
	}}).Check()
	written := 0
	for i, f := range files {
		ends := at[f.Path]
		if len(ends) == 0 {
			continue
		}
		sort.Ints(ends)
		var b []byte
		pos := 0
		for j, end := range ends {
			if j > 0 && end == ends[j-1] {
				continue
			}
			b = append(b, f.Src[pos:end]...)
			b = append(b, "<mixed>"...)
			pos = end
			written++
		}
		b = append(b, f.Src[pos:]...)
		if _, err := parser.Parse(b, lexer.ModePhaenon); err != nil {
			return nil, 0, fmt.Errorf("%s: writing array<mixed> broke the file: %v", f.Path, err)
		}
		out[i] = b
	}
	return out, written, nil
}

// declaresBareArray reports whether a file declares an `array` or
// `iterable` without type arguments anywhere a type is written: the cheap
// test before the checker runs.
func declaresBareArray(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		if nt, ok := n.(*ast.NamedType); ok && len(nt.TypeArgs) == 0 && len(nt.Name.Parts) == 1 {
			found = strings.EqualFold(nt.Name.Parts[0], "array") || strings.EqualFold(nt.Name.Parts[0], "iterable")
		}
		return !found
	})
	return found
}
