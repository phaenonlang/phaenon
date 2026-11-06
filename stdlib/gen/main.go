// Command gen refreshes the embedded PHP standard library from php-src and
// generates the table of the PHP release each library symbol appeared in.
//
// It reads php-src only through `git archive`, so the clone's files and
// git state are never touched, and writes, under the stdlib directory:
//
//   - php-src/: every *.stub.php of the newest supported release branch,
//     byte for byte, with php-src's LICENSE;
//   - versions_gen.go: the symbols (functions, classes, methods, constants,
//     class constants, parameters) that each newer supported branch added
//     to the stubs of the oldest one, and the #[\Deprecated] attributes it
//     added;
//   - constants.php, with -php: the values of the constants the stubs leave
//     UNKNOWN, read from that php (it should be the newest release).
//
// Run it from the repository root:
//
//	go generate ./stdlib
//	go run ./stdlib/gen -php php
//
// The php-src clone is the one $PHAENON_PHP_SRC names, else a clone of
// php/php-src beside the repository (../php-src from its root); -php-src
// names another. constants.php is rewritten only when -php names a php:
// `php` takes the one on PATH, which should be the newest supported release.
// -check compares instead of writing and fails when anything is stale.
package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/phpversion"
)

// release is a supported PHP release and the php-src branch that holds it.
type release struct {
	phpversion.Version
	Branch string
}

func (r release) version() string { return r.Version.String() }

// releases are the supported PHP releases, oldest first, from
// phpversion.Floor to phpversion.Newest (main checks that they agree). The
// table records what each came to add to the floor.
var releases = []release{
	{phpversion.PHP85, "PHP-8.5"},
	{phpversion.PHP86, "PHP-8.6"},
}

// testExtensions are php-src's own test fixtures: embedded with the rest
// of the stubs, left out of the version table.
var testExtensions = []string{"ext/zend_test/", "ext/dl_test/", "ext/skeleton/"}

func main() {
	phpSrc := flag.String("php-src", os.Getenv("PHAENON_PHP_SRC"), "the php-src clone (default $PHAENON_PHP_SRC, else php-src beside the repository)")
	remote := flag.String("remote", "origin", "the remote whose release branches are read")
	php := flag.String("php", "", "a php of the newest release, to regenerate constants.php (php: the one on PATH)")
	dir := flag.String("dir", "", "the stdlib directory (default: found from the working directory)")
	check := flag.Bool("check", false, "compare with the committed files instead of writing them")
	flag.Parse()
	if releases[0].Version != phpversion.Floor || releases[len(releases)-1].Version != phpversion.Newest {
		fail(fmt.Sprintf("the releases listed here must run from phpversion.Floor (%s) to phpversion.Newest (%s)", phpversion.Floor, phpversion.Newest))
	}
	if *dir == "" {
		*dir = findStdlib()
	}
	if *phpSrc == "" {
		*phpSrc = siblingPHPSrc(*dir)
	}
	if *phpSrc == "" {
		fail("no php-src clone: pass -php-src, set PHAENON_PHP_SRC or clone php/php-src beside the repository")
	}
	g := &generator{phpSrc: *phpSrc, remote: *remote, dir: *dir, check: *check}
	if err := g.run(*php); err != nil {
		fail(err.Error())
	}
	if g.stale > 0 {
		fail(fmt.Sprintf("%d generated file(s) are stale: run go generate ./stdlib", g.stale))
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "gen:", msg)
	os.Exit(1)
}

// findStdlib returns the stdlib directory: the working directory under go
// generate, ./stdlib from the repository root.
func findStdlib() string {
	for _, d := range []string{".", "stdlib"} {
		if _, err := os.Stat(filepath.Join(d, "stdlib.go")); err == nil {
			return d
		}
	}
	fail("run from the repository root or the stdlib directory")
	return ""
}

// siblingPHPSrc returns the php-src clone beside the repository whose
// stdlib directory is dir, or "" when there is none.
func siblingPHPSrc(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	clone := filepath.Join(filepath.Dir(filepath.Dir(abs)), "php-src")
	if _, err := os.Stat(filepath.Join(clone, ".git")); err != nil {
		return ""
	}
	return clone
}

type generator struct {
	phpSrc, remote, dir string
	check               bool
	stale               int
}

// branch is one release's stubs as read from php-src.
type branch struct {
	release
	commit string
	files  map[string][]byte // path → contents: the *.stub.php files and LICENSE
}

func (g *generator) run(php string) error {
	var branches []*branch
	for _, r := range releases {
		b, err := g.read(r)
		if err != nil {
			return err
		}
		branches = append(branches, b)
	}
	newest := branches[len(branches)-1]
	if err := g.writeStubs(newest); err != nil {
		return err
	}
	var sets []*symbols
	for _, b := range branches {
		s, err := extract(b)
		if err != nil {
			return err
		}
		sets = append(sets, s)
	}
	t := diff(branches, sets)
	if err := g.writeFile(filepath.Join(g.dir, "versions_gen.go"), t.source(branches)); err != nil {
		return err
	}
	if php != "" {
		src, err := constants(php, newest)
		if err != nil {
			return err
		}
		if err := g.writeFile(filepath.Join(g.dir, "constants.php"), src); err != nil {
			return err
		}
	}
	t.summary(os.Stderr, branches)
	return nil
}

// read lists a release branch's stubs with git archive.
func (g *generator) read(r release) (*branch, error) {
	ref := g.remote + "/" + r.Branch
	commit, err := g.git("rev-parse", "--short=11", ref)
	if err != nil {
		return nil, err
	}
	out, err := g.git("archive", "--format=tar", ref)
	if err != nil {
		return nil, err
	}
	b := &branch{release: r, commit: strings.TrimSpace(commit), files: map[string][]byte{}}
	tr := tar.NewReader(strings.NewReader(out))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ref, err)
		}
		if h.Typeflag != tar.TypeReg || (h.Name != "LICENSE" && !strings.HasSuffix(h.Name, ".stub.php")) {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		b.files[h.Name] = data
	}
	if len(b.files) < 2 {
		return nil, fmt.Errorf("%s holds no stubs", ref)
	}
	return b, nil
}

func (g *generator) git(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", g.phpSrc}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// writeStubs replaces stdlib/php-src with the branch's files.
func (g *generator) writeStubs(b *branch) error {
	root := filepath.Join(g.dir, "php-src")
	existing := map[string]bool{}
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			existing[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	for path := range existing {
		if _, keep := b.files[path]; keep {
			continue
		}
		if g.check {
			fmt.Fprintf(os.Stderr, "stale: php-src/%s is not in %s\n", path, b.Branch)
			g.stale++
			continue
		}
		if err := os.Remove(filepath.Join(root, path)); err != nil {
			return err
		}
	}
	paths := make([]string, 0, len(b.files))
	for p := range b.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := g.writeFile(filepath.Join(root, filepath.FromSlash(p)), b.files[p]); err != nil {
			return err
		}
	}
	if !g.check {
		removeEmptyDirs(root)
	}
	return nil
}

func removeEmptyDirs(root string) {
	var dirs []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i]) // fails, as wanted, unless empty
	}
}

// writeFile writes a generated file, or in check mode reports it stale.
func (g *generator) writeFile(path string, data []byte) error {
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, data) {
		return nil
	}
	if g.check {
		fmt.Fprintf(os.Stderr, "stale: %s\n", path)
		g.stale++
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ------------------------------------------------------------------ symbols

// symbols are the declarations of one release's stubs, keyed as the
// version table keys them (see stdlib/versions.go): "function clamp",
// "class time\duration", "method splfileobject::fgetcsv", "const
// PHP_VERSION", "classconst sortdirection::Ascending".
type symbols struct {
	decls map[string]*decl
}

type decl struct {
	key        string
	params     []string // functions and methods, in order
	deprecated string   // the since: of a #[\Deprecated] attribute, "" without one
	private    bool
	owner      string // the class key of a member
}

var reStubKeywordFn = regexp.MustCompile(`\bfunction (clone|exit|die)\(`)

func extract(b *branch) (*symbols, error) {
	s := &symbols{decls: map[string]*decl{}}
	paths := make([]string, 0, len(b.files))
	for p := range b.files {
		if strings.HasSuffix(p, ".stub.php") {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		// Functions named after keywords are valid for php-src's generator,
		// not for a PHP parser; the program renames them the same way.
		src := reStubKeywordFn.ReplaceAll(b.files[p], []byte("function __phn_$1("))
		f, err := parser.Parse(src, lexer.ModePHP)
		if err != nil {
			return nil, fmt.Errorf("%s:%s: %w", b.Branch, p, err)
		}
		test := false
		for _, t := range testExtensions {
			test = test || strings.HasPrefix(p, t)
		}
		w := &walker{s: s, test: test}
		w.stmts(f.Stmts)
	}
	return s, nil
}

type walker struct {
	s    *symbols
	ns   string // the current namespace, as written
	test bool
}

func (w *walker) stmts(stmts []ast.Stmt) {
	for _, st := range stmts {
		switch st := st.(type) {
		case *ast.Namespace:
			name := ""
			if st.Name != nil {
				name = strings.Join(st.Name.Parts, "\\")
			}
			if st.Braced {
				outer := w.ns
				w.ns = name
				w.stmts(st.Body)
				w.ns = outer
			} else {
				w.ns = name
			}
		case *ast.Block:
			w.stmts(st.Stmts)
		case *ast.FunctionDecl:
			d := w.add("function "+strings.ToLower(w.fqn(st.Name.Name)), deprecatedSince(st.Attrs))
			d.params = paramNames(st.Params)
		case *ast.ConstDecl:
			for _, c := range st.Consts {
				w.add("const "+w.constName(c.Name.Name), deprecatedSince(st.Attrs))
			}
		case *ast.ClassDecl:
			w.class(st)
		}
	}
}

func (w *walker) fqn(name string) string {
	if w.ns == "" {
		return name
	}
	return w.ns + "\\" + name
}

// constName keys a global constant: the namespace is case-insensitive,
// the name is not.
func (w *walker) constName(name string) string {
	if w.ns == "" {
		return name
	}
	return strings.ToLower(w.ns) + "\\" + name
}

func (w *walker) add(key, deprecated string) *decl {
	if d, ok := w.s.decls[key]; ok {
		// The same symbol under several #if branches or SAPIs: keep the
		// widest parameter list and any deprecation.
		if d.deprecated == "" {
			d.deprecated = deprecated
		}
		return d
	}
	d := &decl{key: key, deprecated: deprecated}
	if !w.test {
		w.s.decls[key] = d
	}
	return d
}

func (w *walker) class(c *ast.ClassDecl) {
	if c.Name == nil {
		return
	}
	cls := strings.ToLower(w.fqn(c.Name.Name))
	w.add("class "+cls, deprecatedSince(c.Attrs))
	for _, m := range c.Members {
		switch m := m.(type) {
		case *ast.MethodDecl:
			d := w.add("method "+cls+"::"+strings.ToLower(m.Name.Name), deprecatedSince(m.Attrs))
			if ps := paramNames(m.Params); len(ps) > len(d.params) {
				d.params = ps
			}
			d.private = m.Modifiers&ast.ModPrivate != 0
			d.owner = "class " + cls
		case *ast.ClassConstDecl:
			for _, k := range m.Consts {
				d := w.add("classconst "+cls+"::"+k.Name.Name, deprecatedSince(m.Attrs))
				d.private = m.Modifiers&ast.ModPrivate != 0
				d.owner = "class " + cls
			}
		case *ast.EnumCase:
			d := w.add("classconst "+cls+"::"+m.Name.Name, deprecatedSince(m.Attrs))
			d.owner = "class " + cls
		}
	}
}

func paramNames(ps []*ast.Param) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if p.Var != nil {
			out = append(out, p.Var.Name)
		}
	}
	return out
}

// deprecatedSince returns the since: argument of a #[\Deprecated]
// attribute, "?" when it has none, "" without the attribute.
func deprecatedSince(groups []*ast.AttrGroup) string {
	for _, g := range groups {
		for _, a := range g.Attrs {
			if !strings.EqualFold(a.Name.Parts[len(a.Name.Parts)-1], "Deprecated") {
				continue
			}
			if a.Args != nil {
				for _, arg := range a.Args.Args {
					if arg.Name != nil && arg.Name.Name == "since" {
						if lit, ok := arg.Value.(*ast.StringLit); ok {
							return strings.Trim(lit.Raw, `'"`) // a version number: no escapes
						}
					}
				}
			}
			return "?"
		}
	}
	return ""
}

// ------------------------------------------------------------------ the table

type entry struct {
	key, version string
}

type table struct {
	since      []entry // symbols and parameters newer than the oldest release
	deprecated []entry // #[\Deprecated] attributes added after the oldest release
	removed    []entry // symbols a newer release dropped: reported, not tabled
}

func diff(branches []*branch, sets []*symbols) *table {
	t := &table{}
	known := map[string]bool{}
	for k := range sets[0].decls {
		known[k] = true
	}
	for i := 1; i < len(sets); i++ {
		old, cur, v := sets[i-1], sets[i], branches[i].version()
		for k, d := range cur.decls {
			if d.private {
				continue // not callable from outside the class
			}
			if prev, ok := old.decls[k]; ok {
				for _, p := range d.params {
					if !contains(prev.params, p) && !known[k+" $"+p] {
						known[k+" $"+p] = true
						t.since = append(t.since, entry{"param " + k + " $" + p, v})
					}
				}
				if d.deprecated != "" && prev.deprecated == "" {
					t.deprecated = append(t.deprecated, entry{k, v})
				}
				continue
			}
			if known[k] {
				continue
			}
			known[k] = true
			if d.owner != "" && !known[d.owner] {
				continue // a member of a class that is itself new
			}
			if d.owner != "" {
				if _, ok := old.decls[d.owner]; !ok {
					continue
				}
			}
			t.since = append(t.since, entry{k, v})
			if d.deprecated != "" {
				t.deprecated = append(t.deprecated, entry{k, v})
			}
		}
		for k := range old.decls {
			if _, ok := cur.decls[k]; !ok {
				t.removed = append(t.removed, entry{k, v})
			}
		}
	}
	for _, es := range [][]entry{t.since, t.deprecated, t.removed} {
		sort.Slice(es, func(i, j int) bool { return es[i].key < es[j].key })
	}
	return t
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if strings.EqualFold(x, y) {
			return true
		}
	}
	return false
}

func (t *table) source(branches []*branch) []byte {
	var b bytes.Buffer
	b.WriteString("// Code generated by stdlib/gen; DO NOT EDIT.\n\n")
	b.WriteString("package stdlib\n\nimport \"github.com/phaenonlang/phaenon/internal/phpversion\"\n\n")
	b.WriteString("// The stubs of the supported releases, as read from php-src:\n")
	for _, br := range branches {
		fmt.Fprintf(&b, "//   - PHP %s: %s at %s\n", br.version(), br.Branch, br.commit)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "var releases = []phpversion.Version{")
	for i, br := range branches {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "phpversion.PHP%d%d", br.Major, br.Minor)
	}
	b.WriteString("}\n\n")
	b.WriteString("// since maps a symbol the oldest release lacks to the release that\n// added it.\n")
	b.WriteString("var since = map[string]phpversion.Version{\n")
	writeEntries(&b, t.since)
	b.WriteString("}\n\n")
	b.WriteString("// deprecatedIn maps a symbol to the release whose stubs added its\n// #[\\Deprecated] attribute, for the releases after the oldest.\n")
	b.WriteString("var deprecatedIn = map[string]phpversion.Version{\n")
	writeEntries(&b, t.deprecated)
	b.WriteString("}\n")
	out, err := format.Source(b.Bytes())
	if err != nil {
		panic(err) // the generator wrote invalid Go
	}
	return out
}

func writeEntries(b *bytes.Buffer, es []entry) {
	kind := ""
	for _, e := range es {
		k := strings.SplitN(e.key, " ", 2)[0]
		if k != kind {
			if kind != "" {
				b.WriteString("\n")
			}
			fmt.Fprintf(b, "\t// %s\n", plural[k])
			kind = k
		}
		var major, minor int
		fmt.Sscanf(e.version, "%d.%d", &major, &minor)
		fmt.Fprintf(b, "\t%q: phpversion.PHP%d%d,\n", e.key, major, minor)
	}
}

var plural = map[string]string{
	"class": "classes, interfaces, traits, enums", "classconst": "class constants and enum cases",
	"const": "constants", "function": "functions", "method": "methods", "param": "parameters added to existing functions and methods",
}

func (t *table) summary(w io.Writer, branches []*branch) {
	counts := map[string]int{}
	for _, e := range t.since {
		counts[strings.SplitN(e.key, " ", 2)[0]]++
	}
	fmt.Fprintf(w, "stubs: %s at %s\n", branches[len(branches)-1].Branch, branches[len(branches)-1].commit)
	fmt.Fprintf(w, "since the floor: %d classes, %d functions, %d methods, %d constants, %d class constants, %d parameters; %d deprecations\n",
		counts["class"], counts["function"], counts["method"], counts["const"], counts["classconst"], counts["param"], len(t.deprecated))
	for _, e := range t.removed {
		fmt.Fprintf(w, "removed in %s: %s\n", e.version, e.key)
	}
}

// ------------------------------------------------------------------ constants.php

// installPaths are the constants that say where the generating php is
// installed. Their values are that machine's, and the checker reads only
// their type (string: dynamicConstants in internal/checker), so
// constants.php gives them the paths of a php built with
// `./configure --prefix=/usr`, the extension directory without its API
// subdirectory. Generation fails when another string constant holds one of
// the real paths: a constant a newer PHP adds goes here.
var installPaths = map[string]string{
	"DEFAULT_INCLUDE_PATH":     ".:/usr/lib/php",
	"PEAR_EXTENSION_DIR":       "/usr/lib/php/extensions",
	"PEAR_INSTALL_DIR":         "/usr/lib/php",
	"PHP_BINARY":               "/usr/bin/php",
	"PHP_BINDIR":               "/usr/bin",
	"PHP_CONFIG_FILE_PATH":     "/usr/lib",
	"PHP_CONFIG_FILE_SCAN_DIR": "",
	"PHP_DATADIR":              "/usr/share/php",
	"PHP_EXTENSION_DIR":        "/usr/lib/php/extensions",
	"PHP_LIBDIR":               "/usr/lib/php",
	"PHP_LOCALSTATEDIR":        "/usr/var",
	"PHP_MANDIR":               "/usr/php/man",
	"PHP_PREFIX":               "/usr",
	"PHP_SBINDIR":              "/usr/sbin",
	"PHP_SYSCONFDIR":           "/usr/etc",
}

// installPathsPHP is installPaths as a PHP array literal, $neutral.
func installPathsPHP() string {
	names := make([]string, 0, len(installPaths))
	for name := range installPaths {
		names = append(names, name)
	}
	sort.Strings(names)
	quote := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	var b strings.Builder
	b.WriteString("$neutral = [")
	for _, name := range names {
		fmt.Fprintf(&b, "'%s' => '%s', ", name, quote.Replace(installPaths[name]))
	}
	b.WriteString("];")
	return b.String()
}

// constantsScript prints `const NAME = value;` for every constant of the
// extensions named on stdin (one lower-cased name per line), grouped by
// namespace and sorted, the installation paths in $neutral replaced. true,
// false and null are syntax, not constants, to the checker, and resources
// (STDIN…) have no constant expression.
const constantsScript = `
$exts = array_flip(explode("\n", trim(stream_get_contents(STDIN))));
$machine = [];
foreach ($neutral as $name => $_) {
    if (defined($name) && is_string(constant($name)) && strlen(constant($name)) > 1) {
        $machine[] = constant($name);
    }
}
$byNs = [];
foreach (get_defined_constants(true) as $module => $consts) {
    if (!isset($exts[strtolower($module)])) {
        continue;
    }
    foreach ($consts as $fqn => $value) {
        if (is_resource($value) || in_array(strtolower($fqn), ['true', 'false', 'null'], true)) {
            continue;
        }
        $i = strrpos($fqn, '\\');
        $ns = $i === false ? '' : substr($fqn, 0, $i);
        $name = $i === false ? $fqn : substr($fqn, $i + 1);
        if ($ns === '' && array_key_exists($name, $neutral)) {
            $value = $neutral[$name];
        } elseif (is_string($value)) {
            foreach ($machine as $path) {
                if (str_contains($value, $path)) {
                    fwrite(STDERR, "$fqn holds the installation path $path: add it to installPaths\n");
                    exit(1);
                }
            }
        }
        $byNs[$ns][$name] = $value;
    }
}
ksort($byNs, SORT_STRING);
$out = [];
foreach ($byNs as $ns => $consts) {
    ksort($consts, SORT_STRING);
    $lines = [];
    foreach ($consts as $name => $value) {
        $lines[] = 'const ' . $name . ' = ' . var_export($value, true) . ';';
    }
    $out[] = ($ns === '' ? "namespace {\n" : "namespace $ns {\n") . implode("\n", $lines) . "\n}\n";
}
echo PHP_VERSION, "\n", implode("\n", $out);
`

// constants reads, from php, the values of the constants of the embedded
// extensions: the ones whose stubs are in the newest branch, and Core.
func constants(php string, b *branch) ([]byte, error) {
	exts := map[string]bool{"core": true}
	for p := range b.files {
		if rest, ok := strings.CutPrefix(p, "ext/"); ok {
			if i := strings.IndexByte(rest, '/'); i > 0 {
				exts[rest[:i]] = true
			}
		}
	}
	names := make([]string, 0, len(exts))
	for e := range exts {
		names = append(names, e)
	}
	sort.Strings(names)
	cmd := exec.Command(php, "-d", "display_errors=stderr", "-r", installPathsPHP()+constantsScript)
	cmd.Stdin = strings.NewReader(strings.Join(names, "\n") + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %v: %s", php, err, stderr.String())
	}
	version, body, ok := strings.Cut(string(out), "\n")
	if !ok {
		return nil, errors.New(php + ": no output")
	}
	var buf bytes.Buffer
	buf.WriteString("<?php\n\n/*\n")
	buf.WriteString(" * The values of the constants php-src declares UNKNOWN in its stubs, read\n")
	fmt.Fprintf(&buf, " * from PHP %s (get_defined_constants(), the embedded extensions only).\n", version)
	buf.WriteString(" * A stub keeps its @var type; the value is what a conditional type decides\n")
	buf.WriteString(" * on. Machine-dependent constants (PHP_OS, PHP_EOL, …) are typed, not\n")
	buf.WriteString(" * valued, by the checker. The installation paths (PHP_PREFIX, PHP_BINARY,\n")
	buf.WriteString(" * …) are those of a build with --prefix=/usr, not the generating php's.\n */\n\n")
	buf.WriteString(body)
	return buf.Bytes(), nil
}
