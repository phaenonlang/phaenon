package program

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/token"
)

// ComposerLoader finds classes through Composer's generated autoload maps.
type ComposerLoader struct {
	// Dir is the vendor/composer directory the maps were read from.
	Dir string
	// Sources maps an output directory to the source root it is built
	// from, both absolute, as the project file declares them: Composer's
	// maps name the outputs, where the PHP is, and the checker reads the
	// .phn sources behind them instead.
	Sources  map[string]string
	psr4     map[string][]string // prefix (with trailing \) → directories
	psr0     map[string][]string
	classmap map[string]string // lower FQN → path
	files    []string
	prefixes []string // psr4 prefixes, longest first

	names     []string // the classmap's names, declared case
	namesOnce sync.Once
	allNames  []string
}

// ErrNoComposer is returned when no vendor/composer directory is found.
var ErrNoComposer = errors.New("no vendor/composer directory found")

// FindComposer walks up from dir to the nearest vendor/composer directory
// and loads its maps.
func FindComposer(dir string) (*ComposerLoader, error) {
	cand, err := FindComposerDir(dir)
	if err != nil {
		return nil, err
	}
	return LoadComposer(cand)
}

// FindComposerDir walks up from dir to the nearest vendor/composer
// directory, without reading its maps.
func FindComposerDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(abs); err == nil && !st.IsDir() {
		abs = filepath.Dir(abs)
	}
	for {
		cand := filepath.Join(abs, "vendor", "composer")
		if st, err := os.Stat(filepath.Join(cand, "autoload_psr4.php")); err == nil && !st.IsDir() {
			return cand, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", ErrNoComposer
		}
		abs = parent
	}
}

// LoadComposer reads the maps in a vendor/composer directory.
func LoadComposer(dir string) (*ComposerLoader, error) {
	l := &ComposerLoader{Dir: dir, psr4: map[string][]string{}, psr0: map[string][]string{}, classmap: map[string]string{}}
	vendorDir := filepath.Dir(dir)
	baseDir := filepath.Dir(vendorDir)
	vars := map[string]string{"vendorDir": vendorDir, "baseDir": baseDir}
	if m, err := readMap(filepath.Join(dir, "autoload_psr4.php"), vars); err == nil {
		for k, v := range m {
			l.psr4[k] = v
		}
	} else {
		return nil, err
	}
	if m, err := readMap(filepath.Join(dir, "autoload_namespaces.php"), vars); err == nil {
		for k, v := range m {
			l.psr0[k] = v
		}
	}
	if m, err := readMap(filepath.Join(dir, "autoload_classmap.php"), vars); err == nil {
		for k, v := range m {
			if len(v) > 0 {
				l.classmap[strings.ToLower(k)] = v[0]
				l.names = append(l.names, k)
			}
		}
	}
	if m, err := readMap(filepath.Join(dir, "autoload_files.php"), vars); err == nil {
		var keys []string
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			l.files = append(l.files, m[k]...)
		}
	}
	for k := range l.psr4 {
		l.prefixes = append(l.prefixes, k)
	}
	sort.Slice(l.prefixes, func(i, j int) bool { return len(l.prefixes[i]) > len(l.prefixes[j]) })
	return l, nil
}

// FindClass implements Loader.
func (l *ComposerLoader) FindClass(lower string) (string, bool) {
	if p, ok := l.classmap[lower]; ok {
		if src, ok := l.source(filepath.Dir(p)); ok {
			base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
			if q, ok := findCaseInsensitive(src, []string{base}); ok {
				return q, true
			}
		}
		return p, true
	}
	// PSR-4: the prefix comparison is case-sensitive in Composer, but
	// class names are case-insensitive in PHP; compare lower-cased and
	// build the path from the declared-case prefix.
	for _, prefix := range l.prefixes {
		lp := strings.ToLower(prefix)
		if !strings.HasPrefix(lower, lp) {
			continue
		}
		rel := lower[len(lp):]
		for _, dir := range l.psr4[prefix] {
			if src, ok := l.source(dir); ok {
				if p, ok := findCaseInsensitive(src, strings.Split(rel, "\\")); ok {
					return p, true
				}
			}
			// The file system is usually case-sensitive on Linux, so try
			// to find the file with the real case by scanning the directory.
			if p, ok := findCaseInsensitive(dir, strings.Split(rel, "\\")); ok {
				return p, true
			}
		}
	}
	for prefix, dirs := range l.psr0 {
		lp := strings.ToLower(prefix)
		if !strings.HasPrefix(lower, lp) {
			continue
		}
		for _, dir := range dirs {
			if p, ok := findCaseInsensitive(dir, strings.Split(lower, "\\")); ok {
				return p, true
			}
		}
	}
	return "", false
}

// source maps a directory under an output directory to the same place
// under its source root.
func (l *ComposerLoader) source(dir string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	for out, src := range l.Sources {
		if abs == out {
			return src, true
		}
		if strings.HasPrefix(abs, out+string(filepath.Separator)) {
			return filepath.Join(src, abs[len(out)+1:]), true
		}
	}
	return "", false
}

// findCaseInsensitive walks dir through the segments, matching each
// case-insensitively; the last segment is the class's file, Foo.phn when
// the project writes Phaenon (the source, where Foo.php is its output),
// else Foo.php.
func findCaseInsensitive(dir string, segs []string) (string, bool) {
	cur := dir
	for i, seg := range segs {
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", false
		}
		wants := []string{seg}
		if i == len(segs)-1 {
			wants = []string{seg + ".phn", seg + ".php"}
		}
		found := ""
		for _, want := range wants {
			for _, e := range entries {
				if strings.EqualFold(e.Name(), want) {
					found = e.Name()
					if e.Name() == want {
						break
					}
				}
			}
			if found != "" {
				break
			}
		}
		if found == "" {
			return "", false
		}
		cur = filepath.Join(cur, found)
	}
	return cur, true
}

// Preload implements Loader.
func (l *ComposerLoader) Preload() []string { return l.files }

// Read implements Loader.
func (l *ComposerLoader) Read(path string) ([]byte, error) { return os.ReadFile(path) }

// readMap evaluates a Composer map file: `return array('K' => $vendorDir . '/x', …);`
// where values are strings or arrays of strings.
func readMap(path string, vars map[string]string) (map[string][]string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := parser.Parse(src, lexer.ModePHP)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, s := range f.Stmts {
		ret, ok := s.(*ast.Return)
		if !ok {
			continue
		}
		arr, ok := ret.Expr.(*ast.ArrayLit)
		if !ok {
			continue
		}
		for _, item := range arr.Items {
			if item == nil || item.Key == nil {
				continue
			}
			key, ok := evalString(item.Key, vars)
			if !ok {
				continue
			}
			switch v := item.Value.(type) {
			case *ast.ArrayLit:
				for _, vi := range v.Items {
					if vi == nil {
						continue
					}
					if s, ok := evalString(vi.Value, vars); ok {
						out[key] = append(out[key], s)
					}
				}
			default:
				if s, ok := evalString(v, vars); ok {
					out[key] = append(out[key], s)
				}
			}
		}
	}
	return out, nil
}

func evalString(e ast.Expr, vars map[string]string) (string, bool) {
	switch e := e.(type) {
	case *ast.StringLit:
		return StringValue(e), true
	case *ast.Variable:
		v, ok := vars[e.Name]
		return v, ok
	case *ast.Binary:
		if e.Op != token.Kind('.') {
			return "", false
		}
		l, ok1 := evalString(e.Left, vars)
		r, ok2 := evalString(e.Right, vars)
		return l + r, ok1 && ok2
	case *ast.Paren:
		return evalString(e.Expr, vars)
	}
	return "", false
}

// DirLoader serves classes from a PSR-4 root without Composer (tests, and
// single-package checks); prefix is the namespace prefix with a trailing
// backslash, or "" for the global namespace.
type DirLoader struct {
	Prefix string
	Dir    string
}

// FindClass implements Loader.
func (l DirLoader) FindClass(lower string) (string, bool) {
	lp := strings.ToLower(l.Prefix)
	if !strings.HasPrefix(lower, lp) {
		return "", false
	}
	return findCaseInsensitive(l.Dir, strings.Split(lower[len(lp):], "\\"))
}

// Preload implements Loader.
func (l DirLoader) Preload() []string { return nil }

// Read implements Loader.
func (l DirLoader) Read(path string) ([]byte, error) { return os.ReadFile(path) }

// ClassNames lists the classes the maps can load, declared case: the
// classmap's, and those the PSR-4 directories hold, one per file named
// after its class (a project's output directories are read from their
// sources). The directories are walked once per loader; phn lsp completes
// class names from the list.
func (l *ComposerLoader) ClassNames() []string {
	l.namesOnce.Do(func() {
		seen := map[string]bool{}
		add := func(name string) {
			if k := strings.ToLower(name); !seen[k] {
				seen[k] = true
				l.allNames = append(l.allNames, name)
			}
		}
		for _, n := range l.names {
			add(n)
		}
		for _, prefix := range l.prefixes {
			for _, dir := range l.psr4[prefix] {
				if src, ok := l.source(dir); ok {
					dir = src
				}
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
					ext := filepath.Ext(path)
					if ext != ".php" && ext != ".phn" {
						return nil
					}
					rel, err := filepath.Rel(dir, strings.TrimSuffix(path, ext))
					if err != nil {
						return nil
					}
					parts := strings.Split(filepath.ToSlash(rel), "/")
					for _, part := range parts {
						if !isClassSegment(part) {
							return nil
						}
					}
					add(prefix + strings.Join(parts, `\`))
					return nil
				})
			}
		}
		sort.Strings(l.allNames)
	})
	return l.allNames
}

// isClassSegment reports whether a path segment can be part of a class
// name: an identifier.
func isClassSegment(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= 0x80:
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
