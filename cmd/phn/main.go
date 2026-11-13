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
	"slices"
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
	case "version", "--version":
		fmt.Println("phn " + version)
	case "build":
		build(os.Args[2:])
	case "check":
		checkCmd(os.Args[2:])
	case "fix":
		fixCmd(os.Args[2:])
	case "fmt":
		fmtCmd(os.Args[2:])
	case "lsp":
		lspCmd(os.Args[2:])
	case "watch":
		watchCmd(os.Args[2:])
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
  phn build --vendor                                       compile the installed Composer packages that carry a
                                                           phaenon.json, each for its own target, their findings as
                                                           warnings; a package nothing changed in is skipped, and a
                                                           root a package commits the outputs of is left as installed
  phn check [--format=F] [--no-deprecations] [--exclude PATH]... [<path>...]
                                                           type-check .phn files, write nothing; the findings as
                                                           text, json (an object per line, also --json), github
                                                           (workflow commands: annotations) or checkstyle (XML)
  phn fix [--check] -- <command> [<arg>...]                run a PHP rewriting tool (php-cs-fixer, Rector) on the
                                                           project's outputs and carry its changes back to the .phn;
                                                           a change of text the compiler wrote is not carried; then
                                                           rebuild, run the tool again and report what it still
                                                           changes, with the rule behind each change for php-cs-fixer
                                                           and Rector; --check reports and writes no source
  phn fmt [--check] [<file>...] | --stdin                  lay .phn files out: Phaenon's layout (the withdrawn fun,
                                                           is and as rewritten, function lifted off methods, spaces
                                                           for tabs, braces, {}, one-line docblocks, the spacing of
                                                           type parameters and arguments, throws), then the
                                                           project's fixer through phn fix
                                                           (phaenon.json "fmt", or tools/php-cs-fixer or
                                                           vendor/bin/php-cs-fixer); no files:
                                                           the roots; --check writes nothing and fails on a change;
                                                           --stdin: the first layer, standard input to output
  phn lsp                                                  the language server of the project around the working
                                                           directory, on stdin and stdout: diagnostics, hover, go to
                                                           definition, symbols, completion, signature help,
                                                           formatting; the outputs built on save
  phn watch [--no-build] [--interval D]                    check and build the project each time one of its files
                                                           changes
  phn migrate [--keep-docblocks] [--keep-public] [--keep-layout] [--keep-calls] [--pipes=N] [--list-types] [--php X.Y] <path>...
                                                           rewrite .php files as .phn; lift generic docblocks and bare
                                                           @throws into syntax, function off methods; write chains of
                                                           three nested calls or more as |> pipelines unless
                                                           --keep-calls (--pipes=2: from two), undone in a file whose
                                                           types or findings they change; lay the code out (function
                                                           braces up, class-like braces on their own line, one-line
                                                           docblocks, => bodies) unless --keep-layout;
                                                           --list-types types bare arrays as lists and
                                                           maps from the values they hold;
                                                           --php writes the target into phaenon.json
  phn stdlib --out DIR [--check]                           write the compiled phaenon/debug sources
                                                           (Debug/LineMap.php, …) into DIR
  phn trace [--root PREFIX=LOCAL]... [<text>...]           rewrite the positions in compiled files (stack traces,
                                                           PHPUnit, PHPStan --error-format=raw or github) into .phn
                                                           positions; reads the arguments, or stdin without them,
                                                           where a Checkstyle, JUnit, Clover or Cobertura report is
                                                           rewritten as that report
  phn lex   [--php] <file>                                 dump the token stream
  phn print [--php] <file>                                 parse and pretty-print
  phn version                                              print phn's version

Without paths, build and check read phaenon.json from the working directory:
  {"roots": {"src": ".src", "tests": ".tests"}, "exclude": ["tests/Fixture"], "php": "8.6"}
Each root compiles into its output directory, a sibling with the same structure, where every
other file under the root is copied too (so __DIR__-relative paths resolve as in the source) and
outputs with no source behind them are removed. "exclude" lists paths the checker leaves alone:
check skips them, build compiles them without reporting their diagnostics (--exclude adds more).
"php" is the target, the oldest PHP the output must run on (8.5 or 8.6); without it, the lowest
version composer.json's require.php admits.
"committed": ["src"] lists the roots whose outputs the project's repository tracks, each a key of
"roots": a library installed as plain PHP by projects without phn. Here every command builds such
a root as any other. Where the project is an installed package, build --vendor
leaves the root as installed, writing and removing nothing, and fails when it came without its
outputs: its output directory, or the .php of one of its .phn.
A project that commits a root is installed by projects whose phn reads its phaenon.json, so its
composer.json keeps out the releases that cannot: "conflict": {"phaenon/phn": "<0.5.0"} for
"committed", which 0.5.0 is the first to know. build of the project, and its --check, fail while
a release from before the newest key the file uses can be installed beside the package, or the
constraint is one phn cannot judge; the outputs are written all the same.
Without a project file, build writes Foo.php next to Foo.phn, or under DIR (mirroring the path
given) with --out. A file with type errors is not written unless --emit-on-error is given (the
exit status still reports the errors). --check writes nothing and fails when any output is
missing, stale or orphaned; with --emit-on-error, a file with errors passes when its output is
what that build writes.
stdlib materialises the classes written in Phaenon (stdlib/runtime) as the phaenon/debug
package ships them.
check and build read vendor code through the nearest vendor/composer autoload maps, an installed
package with a phaenon.json from its .phn sources.
Every compiled file ends with a line map (// phaenon-map v1 …) that trace reads: file.php:42,
file.php(42) and "file.php on line 42" become the .phn's path and line; anything else, positions in
files without a map included, passes through unchanged. --root /app=. reads a path under /app (a
production deployment) from the local checkout, so a pasted trace maps too. A GitHub workflow
command (::error file=…,line=…::) gets the source's file and lines, without its columns. A report
on stdin is read whole; in coverage, the compiled lines that come from one source line count as
that line, covered when any of them is.
--php reads the file as plain PHP (with <?php tags) instead of Phaenon.`))
	os.Exit(2)
}

func fail(format string, args ...any) {
	if failPanics {
		panic(failure(fmt.Sprintf(format, args...)))
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// failPanics turns fail into a panic with a failure, which a long-running
// command (phn lsp, phn watch) recovers from: a file that vanished between
// listing and reading must not end the server.
var failPanics bool

// failure is the message of a fail recovered by recoverFailure.
type failure string

// recoverFailure, deferred, turns a fail's panic into *err; any other panic
// goes on.
func recoverFailure(err *error) {
	switch r := recover().(type) {
	case nil:
	case failure:
		*err = errors.New(string(r))
	default:
		panic(r)
	}
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
	vendor := fs.Bool("vendor", false, "compile the installed Composer packages that carry a phaenon.json")
	excludeFlag(fs)
	fs.Parse(args)
	if *vendor {
		if fs.NArg() > 0 || *out != "" || *check || *emitOnError || len(excludes) > 0 {
			fail("phn build --vendor takes no paths and no other options")
		}
		buildVendor()
		return
	}
	proj := loadProject()
	roots := fs.Args()
	if len(roots) == 0 {
		proj.requireRoots("build")
		if roots = proj.roots(); len(roots) == 0 {
			fail("phn build: none of the roots %s names is in this directory", ProjectFile)
		}
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
	job := buildJob{proj: proj, roots: roots, target: tgt, out: *out, check: *check, emitOnError: *emitOnError,
		mirror: proj != nil && *out == "" && fs.NArg() == 0}
	res := job.run()
	// The project's whole build also answers for its composer.json: the
	// compilers that cannot read the project file are kept out (conflict.go).
	// The outputs are written all the same, and the exit status reports it:
	// in the command's last line, or before the failure that is.
	unguarded := ""
	if job.mirror {
		unguarded = proj.conflictFloor()
	}
	if unguarded != "" && (res.failed > 0 || *check && res.stale+res.copied+res.removed > 0) {
		fmt.Fprintln(os.Stderr, unguarded)
	}
	switch {
	case res.failed > 0:
		fail("%d of %d files failed to compile", res.failed, res.files)
	case *check && res.stale+res.copied+res.removed > 0:
		fail("%d outputs are stale, %d copies are stale, %d outputs have no source; run `phn build`", res.stale, res.copied, res.removed)
	case *check && res.findings > 0:
		fmt.Printf("%d files up to date, %d of them built with their errors (--emit-on-error)\n", res.files, res.findings)
	case *check:
		fmt.Printf("%d files up to date\n", res.files)
	case proj != nil:
		fmt.Printf("%d files compiled, %d written, %d files copied, %d stale outputs removed\n", res.files, res.written, res.copied, res.removed)
	default:
		fmt.Printf("%d files compiled, %d written\n", res.files, res.written)
	}
	if unguarded != "" {
		fail("%s", unguarded)
	}
}

// buildJob is one compilation of a project's roots, or of the paths given.
type buildJob struct {
	proj        *project // nil without a project file
	roots       []string
	target      target
	out         string // write under this directory instead (--out)
	check       bool
	emitOnError bool
	// mirror brings the roots' output directories in step: the project's
	// whole build, not one of some paths.
	mirror bool
	// dependency builds an installed package: its findings are printed as
	// warnings and never withhold an output.
	dependency bool
	// quiet prints no findings: phn fix builds to run a tool, not to check.
	quiet bool
	// kept, when set, receives every output the build compiles, written or
	// already current, by output path (phn fix).
	kept map[string]keptOutput
	// prog and diags, when set, are the project already checked, with its
	// findings: phn lsp builds on save from the check it just ran.
	prog  *program.Program
	diags []checker.Diagnostic
}

// keptOutput is a compiled output as the build wrote it, with the source
// it came from and the output's verbatim spans.
type keptOutput struct {
	source    string
	src, data []byte
	spans     []emit.Verbatim
}

type buildResult struct {
	files, failed, written, stale, copied, removed int
	// findings counts the files --check --emit-on-error passed with errors.
	findings int
}

func (j buildJob) run() buildResult {
	proj := j.proj
	skip := func(path string) bool { return proj != nil && proj.isOutput(path) }
	outputPath := func(in string) string {
		dst := strings.TrimSuffix(in, ".phn") + ".php"
		if j.out != "" {
			return filepath.Join(j.out, dst)
		}
		if proj != nil {
			return proj.outputPath(in)
		}
		return dst
	}
	files := collect(j.roots, skip, ".phn")
	res := buildResult{files: len(files)}
	prog := j.prog
	diags := append([]checker.Diagnostic(nil), j.target.Diags...)
	if prog == nil {
		diags = append(diags, typeCheck(files, j.target.Version, proj.checkerOptions(checker.Options{}))...)
		prog = loadedProgram
	} else {
		diags = append(diags, j.diags...)
	}
	broken := map[string]bool{}
	for _, d := range diags {
		if excluded(d.File) {
			continue
		}
		if j.dependency {
			d.Severity = checker.Warning
		}
		if !j.quiet {
			printDiag(d)
		}
		if d.Severity == checker.Error {
			broken[d.File] = true
		}
	}
	for _, in := range files {
		if broken[in] {
			switch {
			case !j.emitOnError:
				res.failed++
				continue
			case j.check:
				// --check asks whether the outputs are what the build writes,
				// and with --emit-on-error it writes this one too: current, it
				// passes, its findings printed above.
				res.findings++
			default:
				res.failed++ // written, and the exit status still reports it
			}
		}
		src, err := os.ReadFile(in)
		if err != nil {
			fail("%v", err)
		}
		dst := outputPath(in)
		opts := emit.Options{SourceName: in, MapSource: mapSource(in, dst), StrictTypes: true, Program: prog, File: prog.File(in)}
		php, spans, err := emit.FileVerbatim(src, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			res.failed++
			continue
		}
		if j.kept != nil {
			j.kept[dst] = keptOutput{source: in, src: src, data: php, spans: spans}
		}
		cur, readErr := os.ReadFile(dst)
		if readErr == nil && bytes.Equal(cur, php) {
			continue
		}
		if j.check {
			fmt.Fprintf(os.Stderr, "%s: stale or missing\n", dst)
			res.stale++
			continue
		}
		writeFile(dst, php)
		res.written++
	}
	if j.mirror {
		for _, src := range j.roots {
			c, r := proj.mirror(src, j.check)
			res.copied += c
			res.removed += r
		}
	}
	return res
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
	check := fs.Bool("check", false, "verify the files there are up to date; write nothing")
	fs.Parse(args)
	if *out == "" || fs.NArg() != 0 {
		usage()
	}
	compiled, err := emit.Stdlib()
	if err != nil {
		fail("%v", err)
	}
	materialise("stdlib", compiled, *out, *check)
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
	// composerDir, when set, is the vendor/composer directory every program
	// reads the vendor code through, instead of the nearest one above the
	// files: an installed package's build reads the root project's.
	composerDir string
	// version is phn's release, set when a release is built
	// (-ldflags "-X main.version=0.4.2").
	version = "dev"
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
	anchor := ""
	if len(files) > 0 {
		anchor = files[0]
	}
	p, diags := newProgram(anchor, target)
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

// newProgram is a program with the standard library, the extensions the
// local php loads and, found from anchor (a file of the project), the
// vendor code; without an anchor, the standard library alone. diags are
// the warnings about what could not be read.
func newProgram(anchor string, target phpversion.Version) (*program.Program, []checker.Diagnostic) {
	p := program.New()
	p.SetTarget(target)
	if share != nil {
		p.Parses = share.parses
	}
	if err := p.LoadStubs(); err != nil {
		fail("stdlib: %v", err)
	}
	var diags []checker.Diagnostic
	if anchor != "" {
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
		var err2 error
		if share != nil {
			_, err2 = p.AddExtensions(share.extensions(php, cacheDir))
		} else {
			embedded := stdlib.EmbeddedExtensions()
			_, err2 = p.LoadExtensions(php, cacheDir, func(lower string) bool { return embedded[lower] })
		}
		if err := err2; err != nil {
			diags = append(diags, checker.Diagnostic{File: anchor, Line: 1, Col: 1, Severity: checker.Warning, Code: "P4007",
				Message: "PHP extensions could not be read (" + err.Error() + "); only php-src's extensions are known"})
		}
		// php-src's library is embedded for every target; the other
		// extensions are read from php, which then must be as new as the
		// target (design/php-versions.md §6).
		if v, err := phpversion.Parse(releaseLine(p.ExtensionsPHP())); err == nil && v.Less(target) {
			diags = append(diags, checker.Diagnostic{File: anchor, Line: 1, Col: 1, Severity: checker.Warning, Code: checker.P4012.ID,
				Message: fmt.Sprintf("%s is PHP %s, older than the target %s: the extensions it loads are known as they are on PHP %s (PHN_PHP names another php)",
					php, p.ExtensionsPHP(), target, v)})
		}
		var l *program.ComposerLoader
		switch {
		case share != nil:
			l, err = share.composer(filepath.Dir(anchor))
		case composerDir != "":
			l, err = program.LoadComposer(composerDir)
		default:
			l, err = program.FindComposer(filepath.Dir(anchor))
		}
		if err == nil {
			// The project's own outputs map to its roots, an installed
			// Phaenon package's to its sources.
			if share == nil {
				setSources(l)
			}
			p.AddLoader(l)
		} else {
			diags = append(diags, checker.Diagnostic{File: anchor, Line: 1, Col: 1, Severity: checker.Warning, Code: "P4005",
				Message: "no vendor/composer directory found above " + anchor + "; only the PHP standard library is known"})
		}
	}
	return p, diags
}

func checkCmd(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	format := fs.String("format", "text", "how to write the findings: "+strings.Join(checkFormats, ", "))
	asJSON := fs.Bool("json", false, "the same as --format=json")
	noDeprecations := fs.Bool("no-deprecations", false, "do not report deprecated calls")
	excludeFlag(fs)
	fs.Parse(args)
	if *asJSON {
		*format = "json"
	}
	if !slices.Contains(checkFormats, *format) {
		fail("phn check: --format is one of %s, not %q", strings.Join(checkFormats, ", "), *format)
	}
	proj := loadProject()
	roots := fs.Args()
	if len(roots) == 0 {
		proj.requireRoots("check")
		if roots = proj.roots(); len(roots) == 0 {
			fail("phn check: none of the roots %s names is in this directory", ProjectFile)
		}
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
	diags := append(tgt.Diags, typeCheck(files, tgt.Version, proj.checkerOptions(checker.Options{Deprecations: !*noDeprecations}))...)
	errors, warnings := 0, 0
	for _, d := range diags {
		if d.Severity == checker.Error {
			errors++
		} else {
			warnings++
		}
	}
	switch *format {
	case "json":
		writeJSON(os.Stdout, diags)
	case "github":
		writeGitHub(os.Stdout, diags)
	case "checkstyle":
		writeCheckstyle(os.Stdout, diags)
	default:
		for _, d := range diags {
			printDiag(d)
		}
	}
	// The summary goes with the formats a person reads, a job's log among
	// them; the others are documents a tool reads whole.
	if *format == "text" || *format == "github" {
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
	keepPublic := fs.Bool("keep-public", false, "keep the `public` of a method lifted out of `function`, and before private(set) (PHP implies it)")
	keepLayout := fs.Bool("keep-layout", false, "keep braces, docblocks and bodies as they are (no canonical layout)")
	keepCalls := fs.Bool("keep-calls", false, "keep nested calls as they are (no pipelines)")
	pipes := fs.Int("pipes", 3, "write chains of this many nested calls or more as pipelines (2 or more)")
	listTypes := fs.Bool("list-types", false, "type bare array declarations as list<T> or map<K, V> where every value they hold says so")
	phpTarget := fs.String("php", "", "write this target (X.Y) into phaenon.json, creating it if needed")
	fs.Parse(args)
	if fs.NArg() == 0 && *phpTarget == "" {
		usage()
	}
	if *pipes < 2 {
		fail("phn migrate --pipes: a chain is two calls or more (--keep-calls writes none)")
	}
	minCalls := *pipes
	if *keepCalls {
		minCalls = 0
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
	proj := loadProject()
	if proj != nil {
		projectSources = proj.sources()
	}
	target := projectTarget(proj, fs.Args()).Version
	// The lift predicts the override-erasure rule, which needs the method
	// a declaration overrides: the program model of the files as they are
	// before the migration, the project's other files and the vendor code
	// found through Composer. Files phn generated stay out of it; their
	// .phn declares the same classes.
	var prog *program.Program
	if !*keepDoc {
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
		prog, _ = loadProgram(model, target, false)
	}
	// Each file is prepared — tags stripped, docblocks lifted — then all of
	// them go through the pipes codemod together, which type-checks them
	// before and after the rewrite, and each is laid out and written.
	type job struct {
		in, dst  string
		src, phn []byte
		notes    []migrate.Note
		closeTag bool
		opts     migrate.Options
	}
	var jobs []*job
	skipped := 0
	var stats migrate.Stats
	for _, in := range files {
		src, err := os.ReadFile(in)
		if err != nil {
			fail("%v", err)
		}
		j := &job{in: in, dst: in, src: src}
		j.opts = migrate.Options{Generics: !*keepDoc, KeepPublic: *keepPublic, Layout: !*keepLayout, Stats: &stats, Program: prog, File: modelFile(prog, in)}
		if strings.HasSuffix(in, ".phn") {
			j.phn = src
			j.opts.KeepLines = true // a .phn keeps its first lines
			if !*keepDoc {
				if j.phn, j.notes, err = migrate.Lift(src, j.opts); err != nil {
					fail("%s: %v", in, err)
				}
			}
		} else {
			j.dst = strings.TrimSuffix(in, ".php") + ".phn"
			j.phn, j.notes, j.closeTag, err = migrate.Prepare(src, j.opts)
			if err != nil {
				if errors.Is(err, migrate.ErrNotPlainCode) || errors.Is(err, migrate.ErrGenerated) {
					fmt.Fprintf(os.Stderr, "%s: skipped: %v\n", in, err)
					skipped++
					continue
				}
				fail("%s: %v", in, err)
			}
		}
		jobs = append(jobs, j)
	}
	var report *migrate.PipeReport
	sources := func(yield func(path string, src *[]byte)) {
		for _, j := range jobs {
			yield(j.dst, &j.phn)
		}
	}
	if minCalls > 0 && len(jobs) > 0 {
		report = migratePipes(jobs[0].in, target, minCalls, sources)
	}
	var typed *migrate.ArrayTypeReport
	if *listTypes && len(jobs) > 0 {
		typed = migrateListTypes(jobs[0].in, target, sources)
	}
	arrays := 0
	if !*keepDoc && len(jobs) > 0 {
		arrays = migrateArrays(jobs[0].in, target, sources)
	}
	migrated, lifted, kept := 0, 0, 0
	linesBefore, linesAfter := 0, 0
	for _, j := range jobs {
		phn, err := migrate.Finish(j.phn, j.closeTag, j.opts)
		if err != nil {
			fail("%s: %v", j.in, err)
		}
		if j.dst == j.in {
			if bytes.Equal(phn, j.src) {
				continue
			}
			lifted++
		} else {
			migrated++
		}
		for _, n := range j.notes {
			fmt.Fprintf(os.Stderr, "%s:%d: %s\n", j.in, n.Line, n.Msg)
			kept++
		}
		linesBefore += bytes.Count(j.src, []byte("\n"))
		linesAfter += bytes.Count(phn, []byte("\n"))
		if err := os.WriteFile(j.dst, phn, 0o644); err != nil {
			fail("%v", err)
		}
		if j.dst != j.in {
			if err := os.Remove(j.in); err != nil {
				fail("%v", err)
			}
		}
	}
	fmt.Printf("%d files migrated, %d skipped, %d .phn files updated, %d docblock tags kept\n", migrated, skipped, lifted, kept)
	if stats != (migrate.Stats{}) {
		fmt.Printf("%d @throws lifted, %d bodies written as =>, %d braces moved up, %d class braces moved down, %d empty bodies written as {}, %d docblocks collapsed; %d lines became %d\n",
			stats.Throws, stats.Arrows, stats.Braces, stats.Down, stats.Empty, stats.Docblocks, linesBefore, linesAfter)
	}
	if typed != nil {
		line := fmt.Sprintf("%d arrays typed as lists and maps, %d left", typed.Written, typed.LeftCount())
		if r := typed.Reasons(); r != "" {
			line += " (" + r + ")"
		}
		fmt.Println(line)
	}
	if arrays > 0 {
		fmt.Printf("%d arrays and iterables declared without an element type written <mixed>\n", arrays)
	}
	if report != nil {
		line := fmt.Sprintf("%d chains as pipelines, %d left as calls", report.Pipelines, report.LeftCalls())
		if r := report.Reasons(); r != "" {
			line += " (" + r + ")"
		}
		fmt.Println(line)
	}
}

// migratePipes runs the pipes codemod over the prepared files, which
// sources yields with their destination paths, and replaces each source
// with its rewrite. The program models are built as check builds them,
// from anchor, with the sources in memory.
func migratePipes(anchor string, target phpversion.Version, minCalls int, sources func(yield func(path string, src *[]byte))) *migrate.PipeReport {
	in, srcs := migrateSources(sources)
	out, rep, err := migrate.Pipes(in, migrate.PipeOptions{MinCalls: minCalls, Load: migrateLoad(anchor, target)})
	if err != nil {
		fail("phn migrate: %v", err)
	}
	for i, src := range srcs {
		*src = out[i]
	}
	for i, n := range rep.Undone {
		fmt.Fprintf(os.Stderr, "%s: %s\n", rep.UndoneIn[i], n.Msg)
	}
	return rep
}

// migrateArrays writes `<mixed>` after the arrays and iterables the
// prepared files declare without an element type (design/collections.md
// §5), and replaces each source with its rewrite; it returns how many.
func migrateArrays(anchor string, target phpversion.Version, sources func(yield func(path string, src *[]byte))) int {
	in, srcs := migrateSources(sources)
	out, n, err := migrate.Arrays(in, migrateLoad(anchor, target))
	if err != nil {
		fail("phn migrate: %v", err)
	}
	for i, src := range srcs {
		*src = out[i]
	}
	return n
}

// migrateListTypes runs the list-types codemod over the prepared files
// (design/tooling.md §8.2) and replaces each source with its rewrite.
func migrateListTypes(anchor string, target phpversion.Version, sources func(yield func(path string, src *[]byte))) *migrate.ArrayTypeReport {
	in, srcs := migrateSources(sources)
	out, rep, err := migrate.ArrayTypes(in, migrateLoad(anchor, target))
	if err != nil {
		fail("phn migrate: %v", err)
	}
	for i, src := range srcs {
		*src = out[i]
	}
	for i, n := range rep.Undone {
		fmt.Fprintf(os.Stderr, "%s: %s\n", rep.UndoneIn[i], n.Msg)
	}
	for _, path := range sortedKeys(rep.Notes) {
		for _, n := range rep.Notes[path] {
			fmt.Fprintf(os.Stderr, "%s:%d: %s\n", path, n.Line, n.Msg)
		}
	}
	return rep
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// migrateSources lists the prepared files sources yields, and where each
// one's source is kept.
func migrateSources(sources func(yield func(path string, src *[]byte))) ([]migrate.PipeFile, []*[]byte) {
	var in []migrate.PipeFile
	var srcs []*[]byte
	sources(func(path string, src *[]byte) {
		in = append(in, migrate.PipeFile{Path: path, Src: *src})
		srcs = append(srcs, src)
	})
	return in, srcs
}

// migrateLoad builds the program models of a codemod as check builds them,
// from anchor, with the sources in memory. A codemod builds one for every
// check it makes, so they share what phn lsp's programs share: the trees of
// the files whose text has not changed, the vendor maps, php's extensions.
func migrateLoad(anchor string, target phpversion.Version) func(map[string][]byte, map[string]bool) (*program.Program, error) {
	if share == nil {
		share = newProgramShare()
	}
	return func(files map[string][]byte, checked map[string]bool) (*program.Program, error) {
		p, _ := newProgram(anchor, target)
		paths := make([]string, 0, len(files))
		for path := range files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			if _, err := p.AddFile(path, files[path], lexer.ModePhaenon, checked[path]); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
		return p, nil
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
