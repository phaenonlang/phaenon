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
	"strings"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/migrate"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/printer"
	"github.com/phaenonlang/phaenon/internal/program"
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
  phn build [--out DIR] [--check] [--no-types] <path>...   type-check and compile .phn files to .php
  phn check [--json] [--no-deprecations] <path>...         type-check .phn files, write nothing
  phn migrate [--keep-docblocks] <path>...                 rewrite .php files as .phn; lift generic docblocks into syntax
  phn lex   [--php] <file>                                 dump the token stream
  phn print [--php] <file>                                 parse and pretty-print

build writes Foo.php next to Foo.phn, or under DIR (mirroring the path given) with --out;
a file with type errors is not written. --check writes nothing and fails when any output
is missing or stale. --no-types skips the type checker.
check reads vendor code through the nearest vendor/composer autoload maps.
--php reads the file as plain PHP (with <?php tags) instead of Phaenon.`))
	os.Exit(2)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// collect expands paths to the files with one of the given extensions, walking directories.
func collect(paths []string, exts ...string) []string {
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
			files = append(files, p)
			continue
		}
		filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				fail("%v", err)
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
	noTypes := fs.Bool("no-types", false, "skip the type checker")
	fs.Parse(args)
	if fs.NArg() == 0 {
		usage()
	}
	files := collect(fs.Args(), ".phn")
	failed, written, stale := 0, 0, 0
	var broken map[string]bool
	if !*noTypes {
		diags := typeCheck(files, checker.Options{})
		broken = map[string]bool{}
		for _, d := range diags {
			printDiag(d)
			if d.Severity == checker.Error {
				broken[d.File] = true
			}
		}
	}
	for _, in := range files {
		if broken[in] {
			failed++
			continue
		}
		src, err := os.ReadFile(in)
		if err != nil {
			fail("%v", err)
		}
		opts := emit.Options{SourceName: in, StrictTypes: true}
		if loadedChecker != nil {
			opts.Program, opts.File, opts.Reified = loadedProgram, loadedProgram.File(in), loadedChecker.ReifiedArgs
		}
		php, err := emit.File(src, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed++
			continue
		}
		dst := strings.TrimSuffix(in, ".phn") + ".php"
		if *out != "" {
			dst = filepath.Join(*out, dst)
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
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			fail("%v", err)
		}
		if err := os.WriteFile(dst, php, 0o644); err != nil {
			fail("%v", err)
		}
		written++
	}
	switch {
	case failed > 0:
		fail("%d of %d files failed to compile", failed, len(files))
	case *check && stale > 0:
		fail("%d of %d outputs are stale; run `phn build`", stale, len(files))
	case *check:
		fmt.Printf("%d files up to date\n", len(files))
	default:
		fmt.Printf("%d files compiled, %d written\n", len(files), written)
	}
}

// ------------------------------------------------------------------ check

var (
	loadedProgram *program.Program
	loadedChecker *checker.Checker
)

// typeCheck loads the stubs, the nearest Composer maps and the given files,
// and returns the diagnostics.
func typeCheck(files []string, opts checker.Options) []checker.Diagnostic {
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		fail("stdlib: %v", err)
	}
	var diags []checker.Diagnostic
	if len(files) > 0 {
		if l, err := program.FindComposer(filepath.Dir(files[0])); err == nil {
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
		if _, err := p.AddFile(in, src, mode, true); err != nil {
			line := 1
			if pe, ok := err.(*parser.Error); ok {
				line = pe.Line
			}
			diags = append(diags, checker.Diagnostic{File: in, Line: line, Col: 1, Severity: checker.Error, Code: "P1000", Message: err.Error()})
		}
	}
	loadedProgram = p
	loadedChecker = checker.New(p, opts)
	return append(diags, loadedChecker.Check()...)
}

func checkCmd(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print diagnostics as JSON lines")
	noDeprecations := fs.Bool("no-deprecations", false, "do not report deprecated calls")
	fs.Parse(args)
	if fs.NArg() == 0 {
		usage()
	}
	// Plain PHP is checked too, in PHP mode — except the .php a .phn
	// compiles to, which would only duplicate its declarations.
	var files []string
	for _, f := range collect(fs.Args(), ".phn", ".php") {
		if filepath.Ext(f) == ".php" {
			if _, err := os.Stat(strings.TrimSuffix(f, ".php") + ".phn"); err == nil {
				continue
			}
		}
		files = append(files, f)
	}
	diags := typeCheck(files, checker.Options{Deprecations: !*noDeprecations})
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
	fs.Parse(args)
	if fs.NArg() == 0 {
		usage()
	}
	files := collect(fs.Args(), ".php", ".phn")
	skipped, migrated, lifted, kept := 0, 0, 0, 0
	for _, in := range files {
		src, err := os.ReadFile(in)
		if err != nil {
			fail("%v", err)
		}
		var phn []byte
		var notes []migrate.Note
		if strings.HasSuffix(in, ".phn") {
			if *keepDoc {
				continue
			}
			phn, notes, err = migrate.Lift(src)
			if err != nil {
				fail("%s: %v", in, err)
			}
			if bytes.Equal(phn, src) {
				continue
			}
			lifted++
		} else {
			phn, notes, err = migrate.Convert(src, migrate.Options{Generics: !*keepDoc})
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
