package program

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/token"
)

// ComposerLoader finds classes through Composer's generated autoload maps.
type ComposerLoader struct {
	// Dir is the vendor/composer directory the maps were read from.
	Dir      string
	psr4     map[string][]string // prefix (with trailing \) → directories
	psr0     map[string][]string
	classmap map[string]string // lower FQN → path
	files    []string
	prefixes []string // psr4 prefixes, longest first
}

// ErrNoComposer is returned when no vendor/composer directory is found.
var ErrNoComposer = errors.New("no vendor/composer directory found")

// FindComposer walks up from dir to the nearest vendor/composer directory
// and loads its maps.
func FindComposer(dir string) (*ComposerLoader, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err == nil && !st.IsDir() {
		abs = filepath.Dir(abs)
	}
	for {
		cand := filepath.Join(abs, "vendor", "composer")
		if st, err := os.Stat(filepath.Join(cand, "autoload_psr4.php")); err == nil && !st.IsDir() {
			return LoadComposer(cand)
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return nil, ErrNoComposer
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

// findCaseInsensitive walks dir through the segments, matching each
// case-insensitively; the last segment gets ".php" appended.
func findCaseInsensitive(dir string, segs []string) (string, bool) {
	cur := dir
	for i, seg := range segs {
		want := seg
		if i == len(segs)-1 {
			want += ".php"
		}
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", false
		}
		found := ""
		for _, e := range entries {
			if strings.EqualFold(e.Name(), want) {
				found = e.Name()
				if e.Name() == want {
					break
				}
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
