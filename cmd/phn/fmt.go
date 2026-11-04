package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/phaenonlang/phaenon/internal/format"
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
		out, err := format.Source(src)
		if err != nil {
			fail("phn fmt: %v", err) // nothing on stdout: the caller keeps its text
		}
		os.Stdout.Write(out)
		return
	}
	proj := loadProject()
	files := fs.Args()
	if len(files) == 0 {
		proj.requireRoots("fmt")
		files = collect(proj.roots(), proj.isOutput, ".phn")
	}
	changed := len(layoutFiles(files, *check))
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
	case left || *check && changed > 0:
		os.Exit(1)
	}
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
