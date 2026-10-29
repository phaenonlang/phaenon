// Package stdlib embeds the PHP standard library signatures: the
// `*.stub.php` files of php-src (see php-src/LICENSE) and Phaenon's overlay
// that refines them with the types PHP cannot express natively.
package stdlib

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

//go:embed all:php-src
var phpSrc embed.FS

//go:embed all:overlay
var overlay embed.FS

//go:embed all:runtime
var runtime embed.FS

// Stub is one embedded signature file.
type Stub struct {
	Path string // e.g. "php-src/ext/standard/basic_functions.stub.php"
	Src  []byte
}

// Stubs returns every php-src stub, in a stable order.
func Stubs() []Stub {
	return walk(phpSrc, "php-src", ".stub.php")
}

// Overlay returns Phaenon's overlay files, which are read after the stubs
// and replace the signatures they redeclare.
func Overlay() []Stub {
	return walk(overlay, "overlay", ".php")
}

// EmbeddedExtensions names the extensions whose stubs are embedded, as
// get_loaded_extensions() spells them lower-cased ("core", "standard",
// "spl", "pdo_odbc"); the rest of a php's extensions are read from its
// reflection (program.LoadExtensions).
func EmbeddedExtensions() map[string]bool {
	out := map[string]bool{"core": true}
	entries, err := fs.ReadDir(phpSrc, "php-src/ext")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			out[e.Name()] = true
		}
	}
	return out
}

// Runtime returns the declarations of the phaenon/runtime package
// (`\Phaenon\Type`), which compiled code may depend on.
func Runtime() []Stub {
	return walk(runtime, "runtime", ".php")
}

func walk(f embed.FS, root, suffix string) []Stub {
	var out []Stub
	fs.WalkDir(f, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, suffix) {
			return nil
		}
		src, err := f.ReadFile(path)
		if err != nil {
			return nil
		}
		out = append(out, Stub{Path: path, Src: src})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
