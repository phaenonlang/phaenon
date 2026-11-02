// Command phn is the Phaenon compiler.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/migrate"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/printer"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/stdlib"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "build":
		build(os.Args[2:])
	case "check":
		checkCmd(os.Args[2:])
	case "migrate":
		migrateCmd(os.Args[2:])
	case "stdlib":
		stdlibCmd(os.Args[2:])
	case "trace":
		traceCmd(os.Args[2:])
	case "lex":
		lex(os.Args[2:])
	case "print":
		printCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(`
usage:
  phn build [--out DIR] [--check] [--emit-on-error] [--exclude PATH]... [<path>...]
                                                           type-check and compile .phn files to .php
  phn check [--json] [--no-deprecations] [--exclude PATH]... [<path>...]
                                                           type-check .phn files, write nothing
  phn migrate [--keep-docblocks] [--keep-public] [--keep-layout] [--php X.Y] <path>...
                                                           rewrite .php files as .phn; lift generic docblocks and bare
                                                           @throws into syntax, function into fun; lay the code out
                                                           (function braces up, class-like braces on their own line,
                                                           one-line docblocks, => bodies) unless --keep-layout;
                                                           --php writes the target into phaenon.json
  phn stdlib [--out DIR] [--concurrent-out DIR] [--plugin-stubs DIR] [--check]
                                                           write the compiled stdlib (Seq.php, …) into DIR, the compiled
                                                           phaenon/concurrent package into the second, or the PhpStorm
                                                           plugin's pseudo-classes (ListMethods.php, …) into the third
  phn trace [--root PREFIX=LOCAL]... [<text>...]           rewrite the positions in compiled files (stack traces,
                                                           PHPUnit, PHPStan --error-format=raw) into .phn positions;
                                                           reads the arguments, or stdin without them
  phn lex   [--php] <file>                                 dump the token stream
  phn print [--php] <file>                                 parse and pretty-print

Without paths, build and check read phaenon.json from the working directory:
  {"roots": {"src": ".src", "tests": ".tests"}, "exclude": ["tests/Fixture"], "php": "8.6"}
Each root compiles into its output directory, a sibling with the same structure, where every
other file under the root is copied too (so __DIR__-relative paths resolve as in the source) and
outputs with no source behind them are removed. "exclude" lists paths the checker leaves alone:
check skips them, build compiles them without reporting their diagnostics (--exclude adds more).
"php" is the target, the oldest PHP the output must run on (8.5 or 8.6); without it, the lowest
version composer.json's require.php admits.
Without a project file, build writes Foo.php next to Foo.phn, or under DIR (mirroring the path
given) with --out. A file with type errors is not written unless --emit-on-error is given (the
exit status still reports the errors). --check writes nothing and fails when any output is
missing, stale or orphaned.
stdlib materialises the classes behind the method syntax, as the phaenon/runtime package ships them,
and with --plugin-stubs the editor-only pseudo-classes the PhpStorm plugin bundles.
check reads vendor code through the nearest vendor/composer autoload maps.
Every compiled file ends with a line map (// phaenon-map v1 …) that trace reads: file.php:42,
file.php(42) and "file.php on line 42" become the .phn's path and line; anything else, positions in
files without a map included, passes through unchanged. --root /app=. reads a path under /app (a
production deployment) from the local checkout, so a pasted trace maps too.
--php reads the file as plain PHP (with <?php tags) instead of Phaenon.`))
	os.Exit(2)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// excludes holds the --exclude paths of build and check: directories (or
// files) under the given paths that are not the project's own code to
// check, such as fixtures for another analyser.
var excludes []string

func excludeFlag(fs *flag.FlagSet) {
	fs.Func("exclude", "skip this path; may be repeated", func(s string) error {
		excludes = append(excludes, filepath.Clean(s))
		return nil
	})
}

func excluded(path string) bool {
	path = filepath.Clean(path)
	for _, e := range excludes {
		if path == e || strings.HasPrefix(path, e+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// collect expands paths to the files with one of the given extensions,
// walking directories and leaving out what skip says to.
func collect(paths []string, skip func(string) bool, exts ...string) []string {
	wanted := map[string]bool{}
	for _, e := range exts {
		wanted[e] = true
	}
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fail("%v", err)
		}
		if !info.IsDir() {
			if !skip(p) {
				files = append(files, p)
			}
			continue
		}
		filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				fail("%v", err)
			}
			if skip(path) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() && d.Name() == "vendor" && path != p {
				return filepath.SkipDir
			}
			if !d.IsDir() && wanted[filepath.Ext(path)] {
				files = append(files, path)
			}
			return nil
		})
	}
	return files
}

// ------------------------------------------------------------------ build

func build(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	out := fs.String("out", "", "write outputs under this directory instead of beside the sources")
	check := fs.Bool("check", false, "verify outputs are up to date; write nothing")
	emitOnError := fs.Bool("emit-on-error", false, "write files that have type errors too; the exit status still reports them")
	excludeFlag(fs)
	fs.Parse(args)
	proj := loadProject()
	roots := fs.Args()
	if len(roots) == 0 {
		proj.requireRoots("build")
		roots = proj.roots()
	}
	tgt := projectTarget(proj, roots)
	if tgt.Unsupported {
		for _, d := range tgt.Diags {
			printDiag(d)
		}
		fail("phn build: nothing compiled: the target is not one phn compiles for")
	}
	// Excluded paths are compiled all the same: they are code the project runs,
	// only analysed by another tool (PHPStan's rule fixtures, a Rector config).
	if proj != nil {
		excludes = append(excludes, proj.Exclude...)
		projectSources = proj.sources()
	}
	skip := func(path string) bool { return proj != nil && proj.isOutput(path) }
	outputPath := func(in string) string {
		dst := strings.TrimSuffix(in, ".phn") + ".php"
		if *out != "" {
			return filepath.Join(*out, dst)
		}
		if proj != nil {
			return proj.outputPath(in)
		}
		return dst
	}
	files := collect(roots, skip, ".phn")
	failed, written, stale := 0, 0, 0
	diags := append(tgt.Diags, typeCheck(files, tgt.Version, checker.Options{})...)
	broken := map[string]bool{}
	for _, d := range diags {
		if excluded(d.File) {
			continue
		}
		printDiag(d)
		if d.Severity == checker.Error {
			broken[d.File] = true
		}
	}
	for _, in := range files {
		if broken[in] {
			failed++
			if !*emitOnError {
				continue
			}
		}
		src, err := os.ReadFile(in)
		if err != nil {
			fail("%v", err)
		}
		dst := outputPath(in)
		opts := emit.Options{SourceName: in, MapSource: mapSource(in, dst), StrictTypes: true, Program: loadedProgram, File: loadedProgram.File(in)}
		php, err := emit.File(src, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed++
			continue
		}
		cur, readErr := os.ReadFile(dst)
		upToDate := readErr == nil && bytes.Equal(cur, php)
		if upToDate {
			continue
		}
		if *check {
			fmt.Fprintf(os.Stderr, "%s: stale or missing\n", dst)
			stale++
			continue
		}
		writeFile(dst, php)
		written++
	}
	copied, removed := 0, 0
	if proj != nil && *out == "" && fs.NArg() == 0 {
		for _, src := range proj.roots() {
			c, r := proj.mirror(src, *check)
			copied += c
			removed += r
		}
	}
	switch {
	case failed > 0:
		fail("%d of %d files failed to compile", failed, len(files))
	case *check && stale+copied+removed > 0:
		fail("%d outputs are stale, %d copies are stale, %d outputs have no source; run `phn build`", stale, copied, removed)
	case *check:
		fmt.Printf("%d files up to date\n", len(files))
	case proj != nil:
		fmt.Printf("%d files compiled, %d written, %d files copied, %d stale outputs removed\n", len(files), written, copied, removed)
	default:
		fmt.Printf("%d files compiled, %d written\n", len(files), written)
	}
}

// mapSource is the path a compiled file's line map names: the source
// relative to the output's directory, with forward slashes
// (design/line-map.md §3.1), so that it resolves wherever the two are
// deployed together.
func mapSource(src, dst string) string {
	absSrc, err1 := filepath.Abs(src)
	absDir, err2 := filepath.Abs(filepath.Dir(dst))
	if err1 == nil && err2 == nil {
		if rel, err := filepath.Rel(absDir, absSrc); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(src)
}

// ------------------------------------------------------------------ stdlib

func stdlibCmd(args []string) {
	fs := flag.NewFlagSet("stdlib", flag.ExitOnError)
	out := fs.String("out", "", "directory to write the compiled stdlib into")
	concurrentOut := fs.String("concurrent-out", "", "directory to write the compiled phaenon/concurrent package into")
	plugin := fs.String("plugin-stubs", "", "directory to write the PhpStorm plugin's pseudo-classes into")
	check := fs.Bool("check", false, "verify the files there are up to date; write nothing")
	fs.Parse(args)
	if (*out == "" && *concurrentOut == "" && *plugin == "") || fs.NArg() != 0 {
		usage()
	}
	if *out != "" {
		compiled, err := emit.Stdlib()
		if err != nil {
			fail("%v", err)
		}
		materialise("stdlib", compiled, *out, *check)
	}
	if *concurrentOut != "" {
		compiled, err := emit.Concurrent()
		if err != nil {
			fail("%v", err)
		}
		materialise("concurrent package", compiled, *concurrentOut, *check)
	}
	if *plugin != "" {
		stubs, err := emit.PluginStubs()
		if err != nil {
			fail("%v", err)
		}
		materialise("plugin stub", stubs, *plugin, *check)
	}
}

// materialise writes the generated files into dir, or with check only
// reports the stale and missing ones.
func materialise(what string, files map[string][]byte, dir string, check bool) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	written, stale := 0, 0
	for _, name := range names {
		dst := filepath.Join(dir, filepath.FromSlash(name))
		cur, readErr := os.ReadFile(dst)
		if readErr == nil && bytes.Equal(cur, files[name]) {
			continue
		}
		if check {
			fmt.Fprintf(os.Stderr, "%s: stale or missing\n", dst)
			stale++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			fail("%v", err)
		}
		if err := os.WriteFile(dst, files[name], 0o644); err != nil {
			fail("%v", err)
		}
		written++
	}
	switch {
	case check && stale > 0:
		fail("%d of %d %s files are stale; run phn stdlib to rewrite them", stale, len(names), what)
	case check:
		fmt.Printf("%d %s files, all fresh\n", len(names), what)
	default:
		fmt.Printf("%d %s files, %d written\n", len(names), what, written)
	}
}

// ------------------------------------------------------------------ check

var (
	loadedProgram *program.Program
	loadedChecker *checker.Checker
	// projectSources is the project file's output → source root map, absolute.
	projectSources map[string]string
)

// releaseLine cuts a PHP_VERSION ("8.5.3", "8.6.0-dev") to its release
// line ("8.5").
func releaseLine(version string) string {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return version
	}
	return parts[0] + "." + parts[1]
}

// typeCheck loads the stubs, the nearest Composer maps and the given files,
// and returns the diagnostics of checking them for the target.
func typeCheck(files []string, target phpversion.Version, opts checker.Options) []checker.Diagnostic {
	p, diags := loadProgram(files, target, true)
	loadedProgram = p
	loadedChecker = checker.New(p, opts)
	return append(diags, loadedChecker.Check()...)
}

// loadProgram builds the program model of the given files for the target:
// the stubs, the local php's extensions, the nearest Composer maps (through
// the project file's roots) and the files themselves, .php in PHP mode. A
// file that does not parse is reported as P1000 and left out.
func loadProgram(files []string, target phpversion.Version, checked bool) (*program.Program, []checker.Diagnostic) {
	p := program.New()
	p.SetTarget(target)
	if err := p.LoadStubs(); err != nil {
		fail("stdlib: %v", err)
	}
	var diags []checker.Diagnostic
	if len(files) > 0 {
		// The extensions the local php has loaded beyond php-src's: php is
		// the oracle for what exists (PHN_PHP names another binary).
		php := os.Getenv("PHN_PHP")
		if php == "" {
			php = "php"
		}
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			cacheDir = os.TempDir()
		}
		embedded := stdlib.EmbeddedExtensions()
		if _, err := p.LoadExtensions(php, cacheDir, func(lower string) bool { return embedded[lower] }); err != nil {
			diags = append(diags, checker.Diagnostic{File: files[0], Line: 1, Col: 1, Severity: checker.Warning, Code: "P4007",
				Message: "PHP extensions could not be read (" + err.Error() + "); only php-src's extensions are known"})
		}
		// php-src's library is embedded for every target; the other
		// extensions are read from php, which then must be as new as the
		// target (design/php-versions.md §6).
		if v, err := phpversion.Parse(releaseLine(p.ExtensionsPHP())); err == nil && v.Less(target) {
			diags = append(diags, checker.Diagnostic{File: files[0], Line: 1, Col: 1, Severity: checker.Warning, Code: checker.P4012.ID,
				Message: fmt.Sprintf("%s is PHP %s, older than the target %s: the extensions it loads are known as they are on PHP %s (PHN_PHP names another php)",
					php, p.ExtensionsPHP(), target, v)})
		}
		if l, err := program.FindComposer(filepath.Dir(files[0])); err == nil {
			l.Sources = projectSources
			p.AddLoader(l)
		} else {
			diags = append(diags, checker.Diagnostic{File: files[0], Line: 1, Col: 1, Severity: checker.Warning, Code: "P4005",
				Message: "no vendor/composer directory found above " + files[0] + "; only the PHP standard library is known"})
		}
	}
	for _, in := range files {
		src, err := os.ReadFile(in)
		if err != nil {
			fail("%v", err)
		}
		mode := lexer.ModePhaenon
		if filepath.Ext(in) == ".php" {
			mode = lexer.ModePHP
		}
		if _, err := p.AddFile(in, src, mode, checked); err != nil {
			line := 1
			if pe, ok := err.(*parser.Error); ok {
				line = pe.Line
			}
			diags = append(diags, checker.Diagnostic{File: in, Line: line, Col: 1, Severity: checker.Error, Code: "P1000", Message: err.Error()})
		}
	}
	return p, diags
}

func checkCmd(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print diagnostics as JSON lines")
	noDeprecations := fs.Bool("no-deprecations", false, "do not report deprecated calls")
	excludeFlag(fs)
	fs.Parse(args)
	proj := loadProject()
	roots := fs.Args()
	if len(roots) == 0 {
		proj.requireRoots("check")
		roots = proj.roots()
	}
	tgt := projectTarget(proj, roots)
	if proj != nil {
		excludes = append(excludes, proj.Exclude...)
		projectSources = proj.sources()
	}
	skip := func(path string) bool { return excluded(path) || (proj != nil && proj.isOutput(path)) }
	// Plain PHP is checked too, in PHP mode — except the .php a .phn
	// compiles to, which would only duplicate its declarations.
	var files []string
	for _, f := range collect(roots, skip, ".phn", ".php") {
		if filepath.Ext(f) == ".php" {
			if _, err := os.Stat(strings.TrimSuffix(f, ".php") + ".phn"); err == nil {
				continue
			}
		}
		files = append(files, f)
	}
	diags := append(tgt.Diags, typeCheck(files, tgt.Version, checker.Options{Deprecations: !*noDeprecations})...)
	errors, warnings := 0, 0
	for _, d := range diags {
		if *asJSON {
			fmt.Printf(`{"file":%q,"line":%d,"col":%d,"endLine":%d,"endCol":%d,"severity":%q,"code":%q,"message":%q}`+"\n",
				d.File, d.Line, d.Col, d.EndLine, d.EndCol, d.Severity, d.Code, d.Message)
		} else {
			printDiag(d)
		}
		if d.Severity == checker.Error {
			errors++
		} else {
			warnings++
		}
	}
	if !*asJSON {
		fmt.Printf("%d files checked: %d errors, %d warnings\n", len(files), errors, warnings)
	}
	if errors > 0 {
		os.Exit(1)
	}
}

// printDiag prints one diagnostic with its source line and a caret.
func printDiag(d checker.Diagnostic) {
	fmt.Fprintln(os.Stderr, d.String())
	if loadedProgram == nil {
		return
	}
	f := loadedProgram.File(d.File)
	if f == nil {
		return
	}
	text := f.Line(d.Line)
	if text == "" {
		return
	}
	width := 1
	if d.EndLine == d.Line && d.EndCol > d.Col {
		width = d.EndCol - d.Col
	}
	num := fmt.Sprintf("%5d", d.Line)
	fmt.Fprintf(os.Stderr, "%s | %s\n", num, text)
	fmt.Fprintf(os.Stderr, "%s | %s%s\n", strings.Repeat(" ", len(num)), strings.Repeat(" ", d.Col-1), strings.Repeat("^", width))
}

// ------------------------------------------------------------------ migrate

func migrateCmd(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	keepDoc := fs.Bool("keep-docblocks", false, "leave generic docblocks as they are")
	keepPublic := fs.Bool("keep-public", false, "write `public fun` and keep `public` before private(set) (Phaenon implies it)")
	keepLayout := fs.Bool("keep-layout", false, "keep braces, docblocks and bodies as they are (no canonical layout)")
	phpTarget := fs.String("php", "", "write this target (X.Y) into phaenon.json, creating it if needed")
	fs.Parse(args)
	if fs.NArg() == 0 && *phpTarget == "" {
		usage()
	}
	if *phpTarget != "" {
		v, err := phpversion.Parse(*phpTarget)
		if err != nil {
			fail("phn migrate --php: %v", err)
		}
		if !v.Supported() {
			fail("phn migrate --php: PHP %s is not a target phn compiles for: targets are %s to %s", v, phpversion.Floor, phpversion.Newest)
		}
		if err := setProjectTarget(v); err != nil {
			fail("phn migrate --php: %v", err)
		}
		fmt.Printf("%s: target PHP %s\n", ProjectFile, v)
		// What the new target means against composer.json.
		for _, d := range projectTarget(loadProject(), nil).Diags {
			fmt.Fprintln(os.Stderr, d.String())
		}
		if fs.NArg() == 0 {
			return
		}
	}
	files := collect(fs.Args(), excluded, ".php", ".phn")
	// The lift predicts the override-erasure rule, which needs the method
	// a declaration overrides: the program model of the files as they are
	// before the migration, the project's other files and the vendor code
	// found through Composer. Files phn generated stay out of it; their
	// .phn declares the same classes.
	var prog *program.Program
	if !*keepDoc {
		proj := loadProject()
		if proj != nil {
			projectSources = proj.sources()
		}
		var model []string
		for _, in := range files {
			if strings.HasSuffix(in, ".php") {
				if _, err := os.Stat(strings.TrimSuffix(in, ".php") + ".phn"); err == nil {
					continue
				}
				if src, err := os.ReadFile(in); err == nil && emit.IsGenerated(src) {
					continue
				}
			}
			model = append(model, in)
		}
		prog, _ = loadProgram(model, projectTarget(proj, fs.Args()).Version, false)
	}
	skipped, migrated, lifted, kept := 0, 0, 0, 0
	var stats migrate.Stats
	linesBefore, linesAfter := 0, 0
	for _, in := range files {
		src, err := os.ReadFile(in)
		if err != nil {
			fail("%v", err)
		}
		var phn []byte
		var notes []migrate.Note
		opts := migrate.Options{Generics: !*keepDoc, KeepPublic: *keepPublic, Layout: !*keepLayout, Stats: &stats, Program: prog, File: modelFile(prog, in)}
		if strings.HasSuffix(in, ".phn") {
			phn = src
			if !*keepDoc {
				if phn, notes, err = migrate.Lift(src, opts); err != nil {
					fail("%s: %v", in, err)
				}
			}
			if !*keepLayout {
				if phn, err = migrate.Layout(phn, &stats); err != nil {
					fail("%s: %v", in, err)
				}
			}
			if bytes.Equal(phn, src) {
				continue
			}
			lifted++
		} else {
			phn, notes, err = migrate.Convert(src, opts)
			if err != nil {
				if errors.Is(err, migrate.ErrNotPlainCode) || errors.Is(err, migrate.ErrGenerated) {
					fmt.Fprintf(os.Stderr, "%s: skipped: %v\n", in, err)
					skipped++
					continue
				}
				fail("%s: %v", in, err)
			}
			migrated++
		}
		for _, n := range notes {
			fmt.Fprintf(os.Stderr, "%s:%d: %s\n", in, n.Line, n.Msg)
			kept++
		}
		linesBefore += bytes.Count(src, []byte("\n"))
		linesAfter += bytes.Count(phn, []byte("\n"))
		dst := in
		if strings.HasSuffix(in, ".php") {
			dst = strings.TrimSuffix(in, ".php") + ".phn"
		}
		if err := os.WriteFile(dst, phn, 0o644); err != nil {
			fail("%v", err)
		}
		if dst != in {
			if err := os.Remove(in); err != nil {
				fail("%v", err)
			}
		}
	}
	fmt.Printf("%d files migrated, %d skipped, %d .phn files updated, %d docblock tags kept\n", migrated, skipped, lifted, kept)
	if stats != (migrate.Stats{}) {
		fmt.Printf("%d @throws lifted, %d bodies written as =>, %d braces moved up, %d class braces moved down, %d docblocks collapsed; %d lines became %d\n",
			stats.Throws, stats.Arrows, stats.Braces, stats.Down, stats.Docblocks, linesBefore, linesAfter)
	}
}

// modelFile returns the file at path in the program model, nil without one.
func modelFile(p *program.Program, path string) *program.File {
	if p == nil {
		return nil
	}
	return p.File(path)
}

// ------------------------------------------------------------------ lex / print

// modeAndFile parses the shared [--php] <file> arguments.
func modeAndFile(args []string) (lexer.Mode, []byte) {
	mode := lexer.ModePhaenon
	if len(args) > 0 && args[0] == "--php" {
		mode = lexer.ModePHP
		args = args[1:]
	}
	if len(args) != 1 {
		usage()
	}
	src, err := os.ReadFile(args[0])
	if err != nil {
		fail("%v", err)
	}
	return mode, src
}

// lex prints the token stream of a file.
func lex(args []string) {
	mode, src := modeAndFile(args)
	for _, t := range lexer.Tokenize(src, mode) {
		fmt.Printf("%d\t%s\t%q\n", t.Line, t.Kind, t.Text(src))
	}
}

// printCmd parses a file and writes the canonical pretty-printed form.
func printCmd(args []string) {
	mode, src := modeAndFile(args)
	f, err := parser.Parse(src, mode)
	if err != nil {
		fail("%v", err)
	}
	os.Stdout.WriteString(printer.File(f))
}
