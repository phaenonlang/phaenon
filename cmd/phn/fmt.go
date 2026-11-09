package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/format"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// fmtCmd is `phn fmt [--check] [<file>...]` (design/tooling.md §5). Its
// first layer is Phaenon's own layout (internal/format), on the .phn; its
// second the project's PHP style: the project's fixer run on the outputs
// through phn fix, which carries what it changes back to the .phn. The
// first layer runs again after the second, since the fixer's changes can
// leave what only it lays out, such as a blank line before the first line.
//
// Without files it formats the roots of phaenon.json. With --check nothing
// is written and the status is 1 when either layer would change a file.
// With --stdin the first layer lays out standard input onto standard
// output: the PhpStorm plugin's Reformat Code runs it after PhpStorm's own
// formatter (design/tooling.md §5.1).
//
// The first layer also rewrites the spellings roadmap D26 and D27 withdrew
// (§5.6). Most rewrites of a type test depend on the operand's type, so in
// a project the files that hold one are checked first, as phn check checks
// them, and the checked program gives each test's PHP; --stdin and files
// outside a project get the rewrites that need no type. A test left as
// written is reported, P1001, and the status is 1; with --stdin it is 0, so
// that an editor that runs the first layer keeps the rest of the layout.
func fmtCmd(args []string) {
	fs := flag.NewFlagSet("fmt", flag.ExitOnError)
	check := fs.Bool("check", false, "report the files that are not formatted; write nothing")
	stdin := fs.Bool("stdin", false, "lay out standard input in Phaenon's layout (the first layer) onto standard output")
	fs.Parse(args)
	if *stdin {
		if fs.NArg() > 0 || *check {
			fail("phn fmt --stdin takes no files and no --check")
		}
		src, err := io.ReadAll(os.Stdin)
		if err != nil {
			fail("phn fmt: %v", err)
		}
		out, left, err := format.SourceWith(src, format.Options{})
		if err != nil {
			fail("phn fmt: %v", err) // nothing on stdout: the caller keeps its text
		}
		os.Stdout.Write(out)
		for _, l := range left {
			line, col := position(src, l.Span.Start)
			fmt.Fprintf(os.Stderr, "<stdin>:%d:%d: error P1001: %s\n", line, col, l.Msg)
		}
		return
	}
	proj := loadProject()
	files := fs.Args()
	if len(files) == 0 {
		proj.requireRoots("fmt")
		files = collect(proj.roots(), proj.isOutput, ".phn")
	}
	changed, withdrawn := layoutWith(files, *check, withdrawnTests(proj, files))
	if *check {
		fmt.Printf("%d files checked, %d not in Phaenon's layout\n", len(files), changed)
	} else {
		fmt.Printf("%d files formatted, %d changed in Phaenon's layout\n", len(files), changed)
	}
	status, left := 0, false
	if fixer := proj.fixer(); fixer != nil {
		command := fixer
		if fs.NArg() > 0 {
			command = append(append([]string(nil), fixer...), outputsOf(proj, files)...)
		}
		status, left = runFix(proj, command, *check)
		if !*check {
			if again := layoutFiles(files, false); len(again) > 0 {
				// The fixer carried a change the first layer undoes: the
				// sources keep Phaenon's layout, the outputs are built from
				// them again, and the two layers disagree on every run.
				fixBuild(proj)
				for _, path := range again {
					fmt.Fprintf(os.Stderr, "%s: the fixer's change undoes Phaenon's layout; laid out again\n", path)
				}
				fmt.Printf("%d files laid out again after the fixer: its configuration conflicts with Phaenon's layout, "+
					"as @PSR12 does on every empty body {} (a PER Coding Style set, @PER-CS3x0, keeps it)\n", len(again))
				left = true
			}
		}
	}
	switch {
	case status != 0:
		os.Exit(status)
	case left || withdrawn || *check && changed > 0:
		os.Exit(1)
	}
}

// layoutWith is layoutFiles with what a checked program knows of each
// file's withdrawn type tests (withdrawnTests), and the number of files
// changed and whether a withdrawn test was left as written, which it
// reports.
func layoutWith(files []string, check bool, checked map[string]format.Options) (changed int, left bool) {
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			fail("%v", err)
		}
		opts := checked[path]
		opts.Program = checked != nil
		out, lefts, err := format.SourceWith(src, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			continue
		}
		for _, l := range lefts {
			line, col := position(src, l.Span.Start)
			fmt.Fprintf(os.Stderr, "%s:%d:%d: error P1001: %s\n", path, line, col, l.Msg)
			left = true
		}
		if bytes.Equal(out, src) {
			continue
		}
		changed++
		if check {
			fmt.Fprintf(os.Stderr, "%s: not in Phaenon's layout\n", path)
			continue
		}
		writeFile(path, out)
	}
	return changed, left
}

// withdrawnTests is what the checked program knows of the withdrawn type
// tests `is`, `as` and `as?` that the files hold (roadmap D26, design
// tooling.md §5.6), by file: each test's PHP, or why it has none. The
// files holding one are checked, as phn check checks them. nil without a
// project, when there is no program to read types from, and with nothing
// to rewrite.
func withdrawnTests(proj *project, files []string) map[string]format.Options {
	if proj == nil {
		return nil
	}
	var holding []string
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if f, err := parser.Parse(src, lexer.ModePhaenon); err == nil && holdsTest(f) {
			holding = append(holding, path)
		}
	}
	if len(holding) == 0 {
		return nil
	}
	tgt := projectTarget(proj, proj.roots())
	excludes = append(excludes, proj.Exclude...)
	projectSources = proj.sources()
	p, _ := loadProgram(holding, tgt.Version, true)
	checker.New(p, proj.checkerOptions(checker.Options{})).Check()
	out := map[string]format.Options{}
	for _, path := range holding {
		if f := p.File(path); f != nil {
			out[path] = format.TestsOf(f)
		}
	}
	return out
}

// holdsTest reports a withdrawn `is`, `as` or `as?` in f.
func holdsTest(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.IsExpr, *ast.AsExpr:
			found = true
		}
		return !found
	})
	return found
}

// position is the 1-based line and column of an offset in src.
func position(src []byte, off int) (line, col int) {
	line = 1 + bytes.Count(src[:off], []byte("\n"))
	return line, off - bytes.LastIndexByte(src[:off], '\n')
}

// layoutFiles applies the first layer to the files and returns those it
// changed, or with check, would change.
func layoutFiles(files []string, check bool) []string {
	var changed []string
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			fail("%v", err)
		}
		out, err := format.Source(src)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			continue
		}
		if bytes.Equal(out, src) {
			continue
		}
		changed = append(changed, path)
		if check {
			fmt.Fprintf(os.Stderr, "%s: not in Phaenon's layout\n", path)
			continue
		}
		writeFile(path, out)
	}
	return changed
}

// fixer is the command of phn fmt's second layer: phaenon.json's "fmt", or
// php-cs-fixer's fix where the template and core keep it, or where Composer
// installs it. nil without a project or a fixer.
func (p *project) fixer() []string {
	if p == nil || len(p.Roots) == 0 {
		return nil
	}
	if p.Fmt != nil {
		if len(*p.Fmt) == 0 {
			return nil
		}
		return *p.Fmt
	}
	for _, bin := range []string{"tools/php-cs-fixer/vendor/bin/php-cs-fixer", "vendor/bin/php-cs-fixer"} {
		if _, err := os.Stat(bin); err == nil {
			return []string{bin, "fix"}
		}
	}
	return nil
}

// outputsOf are the outputs of the given sources, for a fixer told to fix
// those files only.
func outputsOf(proj *project, files []string) []string {
	var out []string
	for _, f := range files {
		out = append(out, proj.outputPath(f))
	}
	return out
}
