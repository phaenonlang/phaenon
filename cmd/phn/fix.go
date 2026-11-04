package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/fix"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/linemap"
	"github.com/phaenonlang/phaenon/internal/parser"
)

// fixCmd is `phn fix [--check] -- COMMAND…`: a PHP rewriting tool runs on
// the project's outputs, where its configuration already points, and the
// changes it makes are carried back to the sources (design/tooling.md §4).
//
//  1. The project is built, every output kept with its verbatim spans.
//  2. The command runs as given.
//  3. Each output it changed is diffed against the build's; the changes
//     that lie within verbatim spans are applied to the source, and a
//     change of text the compiler wrote is not. A copied file is a source
//     itself and takes every change.
//  4. The project is built again and the command runs a second time. What
//     it changes now that lies within verbatim spans is carried back too,
//     for at most fixRounds rounds, and what it still changes after the
//     last is reported, the outputs put back as built. A tool and the
//     compiler cannot loop.
//
// With --check no source is written: every change the tool makes is
// reported at its source line, and the outputs are put back.
func fixCmd(args []string) {
	fs := flag.NewFlagSet("fix", flag.ExitOnError)
	check := fs.Bool("check", false, "report the changes the tool makes at their source lines; write no source")
	fs.Parse(args)
	command := fs.Args()
	if len(command) == 0 {
		fail("phn fix: no command (phn fix [--check] -- <command> [<arg>...])")
	}
	proj := loadProject()
	if proj == nil {
		fail("phn fix: no %s here: the tool runs on the outputs of a project's roots", ProjectFile)
	}
	proj.requireRoots("fix")
	status, left := runFix(proj, command, *check)
	exitFix(status, left)
}

// runFix runs phn fix's steps for the command and returns the tool's exit
// status and whether changes are left: with check, any change.
func runFix(proj *project, command []string, check bool) (int, bool) {
	tool := toolName(command)
	command, reporting := withRuleReport(command)
	run := func(quiet bool) (int, *ruleReport) {
		if reporting == "" {
			return runTool(command, quiet, nil), nil
		}
		var stdout bytes.Buffer
		status := runTool(command, quiet, &stdout)
		return status, readRuleReport(reporting, stdout.Bytes())
	}
	built := fixBuild(proj)
	status, rules := run(false)
	after := readOutputs(proj)
	r := built.compare(after, tool, rules)
	switch {
	case check:
		r.print(true, command)
		built.restore(after)
		if r.changed() > 0 {
			fmt.Printf("%s would change %d files: %s\n", tool, r.files, r.counts(true))
		} else {
			fmt.Printf("%s changes nothing\n", tool)
		}
		return status, r.changed() > 0
	case r.changed() == 0:
		built.restore(after)
		fmt.Printf("%s changed nothing\n", tool)
		return status, false
	}
	unparsed := false
	for round := 1; ; round++ {
		unparsed = unparsed || len(r.unparsed) > 0
		r.write()
		prefix := ""
		if round > 1 {
			prefix = fmt.Sprintf("round %d: ", round)
		}
		fmt.Printf("%s%s changed %d files: %s\n", prefix, tool, r.files, r.counts(false))
		built = fixBuild(proj)
		_, rules = run(true)
		after = readOutputs(proj)
		r = built.compare(after, tool, rules)
		if len(r.carried) == 0 || round == fixRounds {
			break
		}
	}
	built.restore(after)
	if r.changed() > 0 {
		r.print(true, command)
		fmt.Printf("after the rebuild, %s still changes %d files: %s\n", tool, r.files, r.counts(true))
	} else {
		fmt.Printf("after the rebuild, %s changes nothing\n", tool)
	}
	return status, r.changed() > 0 || unparsed
}

// fixRounds bounds the carrying: a change of text the compiler derives
// from the source, as the indentation it adds to an expression body's
// continuation lines, is refused while the source's text it follows is
// still the old one, and is a change of the source's text once the rebuild
// has written the derived text anew. Each round runs the tool on the
// rebuilt outputs and carries what has become the source's; the last run
// only reports.
const fixRounds = 3

// exitFix ends phn fix: with the tool's status when it failed, 1 when
// changes are left.
func exitFix(status int, left bool) {
	switch {
	case status != 0:
		os.Exit(status)
	case left:
		os.Exit(1)
	}
}

// toolName is how the report names the command: its program, or the
// script a php binary runs.
func toolName(command []string) string {
	name := filepath.Base(command[0])
	if phpBinary.MatchString(name) && len(command) > 1 {
		name = filepath.Base(command[1])
	}
	return strings.TrimSuffix(name, ".phar")
}

var phpBinary = regexp.MustCompile(`^php[0-9.]*$`)

// runTool runs the command in the working directory and returns its exit
// status. A second run's output is kept back unless it fails. stdout, when
// set, receives the standard output instead (a report phn reads), which is
// printed only if the tool fails.
func runTool(command []string, quiet bool, stdout *bytes.Buffer) int {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = os.Stdin
	var buf bytes.Buffer
	if quiet {
		cmd.Stdout, cmd.Stderr = &buf, &buf
	} else {
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	}
	if stdout != nil {
		cmd.Stdout = stdout
	}
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		if quiet {
			os.Stderr.Write(buf.Bytes())
		}
		if stdout != nil {
			os.Stderr.Write(stdout.Bytes())
		}
		return exit.ExitCode()
	default:
		fail("phn fix: %v", err)
		return 1
	}
}

// builtTree is the project's output directories as a build left them.
type builtTree struct {
	files    map[string][]byte     // every file under the output roots
	compiled map[string]keptOutput // the outputs compiled from a .phn
	copies   map[string]string     // an output copied from a file of a root → that file
}

// fixBuild builds the project as phn build --emit-on-error does, without
// printing its findings, and keeps what it wrote.
func fixBuild(proj *project) *builtTree {
	roots := proj.roots()
	tgt := projectTarget(proj, roots)
	if tgt.Unsupported {
		for _, d := range tgt.Diags {
			printDiag(d)
		}
		fail("phn fix: nothing compiled: the target is not one phn compiles for")
	}
	excludes = append(excludes[:0:0], proj.Exclude...)
	projectSources = proj.sources()
	t := &builtTree{compiled: map[string]keptOutput{}, copies: map[string]string{}}
	job := buildJob{proj: proj, roots: roots, target: tgt, emitOnError: true, mirror: true, quiet: true, kept: t.compiled}
	job.run()
	t.files = readOutputs(proj)
	for src, out := range proj.Roots {
		for path := range t.files {
			rel, ok := under(path, out)
			if _, compiled := t.compiled[path]; !ok || compiled {
				continue
			}
			if data, err := os.ReadFile(filepath.Join(src, rel)); err == nil && bytes.Equal(data, t.files[path]) {
				t.copies[path] = filepath.Join(src, rel)
			}
		}
	}
	return t
}

// readOutputs reads every file under the project's output directories.
func readOutputs(proj *project) map[string][]byte {
	files := map[string][]byte{}
	for _, out := range proj.Roots {
		filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				fail("%v", err)
			}
			files[path] = data
			return nil
		})
	}
	return files
}

// restore puts the outputs back as the build left them.
func (t *builtTree) restore(now map[string][]byte) {
	for path, data := range t.files {
		if cur, ok := now[path]; !ok || !bytes.Equal(cur, data) {
			writeFile(path, data)
		}
	}
	for path := range now {
		if _, ok := t.files[path]; !ok {
			os.Remove(path)
		}
	}
}

// fixReport is what a tool's run changed, carried back or not.
type fixReport struct {
	tool    string
	files   int               // outputs changed, created or removed
	sources map[string][]byte // a source's new text, carried back
	// carried and refused are the hunks within verbatim spans and those of
	// text the compiler wrote, at their source positions.
	carried, refused []fixPosition
	others           []string // outputs created, removed, or changed without a source
	unparsed         []string // sources the carried changes leave unparseable, not written
}

type fixPosition struct {
	file     string
	line     int
	old, new string
	compiler bool   // the change is of text the compiler wrote
	rule     string // the tool's rule behind the change, when its report says
	// out, data and hunk are the change as the tool made it: the output's
	// path, its text as the tool read it, the hunk in it (narrowFixers).
	out  string
	data []byte
	hunk fix.Hunk
}

func (r *fixReport) changed() int { return len(r.carried) + len(r.refused) + len(r.others) }

func (r *fixReport) counts(would bool) string {
	carried, sources := "carried back to", "sources"
	if would {
		carried = "in"
	}
	n := map[string]bool{}
	for _, p := range r.carried {
		n[p.file] = true
	}
	s := fmt.Sprintf("%d changes %s %d %s, %d in text the compiler wrote", len(r.carried), carried, len(n), sources, len(r.refused))
	if len(r.others) > 0 {
		s += fmt.Sprintf(", %d files outside the build", len(r.others))
	}
	return s
}

// compare diffs the outputs as the tool left them against the build.
func (t *builtTree) compare(now map[string][]byte, tool string, rules *ruleReport) *fixReport {
	r := &fixReport{tool: tool, sources: map[string][]byte{}}
	var paths []string
	for path := range t.files {
		paths = append(paths, path)
	}
	for path := range now {
		if _, ok := t.files[path]; !ok {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		was, before := t.files[path]
		cur, after := now[path]
		switch {
		case before && after && bytes.Equal(was, cur):
			continue
		case !after:
			r.files++
			r.others = append(r.others, path+": removed by "+tool+"; the rebuild writes it again")
			continue
		case !before:
			r.files++
			r.others = append(r.others, path+": created by "+tool+"; not carried back, the rebuild removes it")
			continue
		}
		r.files++
		if k, ok := t.compiled[path]; ok {
			hunks := fix.Diff(k.data, cur)
			src, refused := fix.Carry(k.src, k.data, k.spans, hunks)
			m, _ := linemap.Find(k.data)
			at := func(h fix.Hunk) fixPosition {
				line := bytes.Count(k.data[:h.Start], []byte("\n")) + 1
				if m != nil {
					line, _ = m.Forward(line)
				}
				return fixPosition{file: k.source, line: line, old: string(k.data[h.Start:h.End]), new: h.Text,
					rule: rules.rules(path, k.data, h), out: path, data: k.data, hunk: h}
			}
			isRefused := map[fix.Hunk]bool{}
			for _, h := range refused {
				isRefused[h] = true
				p := at(h)
				p.compiler = true
				r.refused = append(r.refused, p)
			}
			for _, h := range hunks {
				if !isRefused[h] {
					r.carried = append(r.carried, at(h))
				}
			}
			if !bytes.Equal(src, k.src) {
				if _, err := parser.Parse(src, lexer.ModePhaenon); err != nil {
					r.unparsed = append(r.unparsed, fmt.Sprintf("%s: %s's changes would not parse (%v); not carried back", k.source, tool, err))
				} else {
					r.sources[k.source] = src
				}
			}
			continue
		}
		if source, ok := t.copies[path]; ok {
			for _, h := range fix.Diff(was, cur) {
				line := bytes.Count(was[:h.Start], []byte("\n")) + 1
				r.carried = append(r.carried, fixPosition{file: source, line: line, old: string(was[h.Start:h.End]), new: h.Text,
					rule: rules.rules(path, was, h), out: path, data: was, hunk: h})
			}
			r.sources[source] = cur
			continue
		}
		r.others = append(r.others, path+": changed by "+tool+", but the build did not write it; not carried back")
	}
	return r
}

// write writes the sources the changes were carried back to.
func (r *fixReport) write() {
	for path, data := range r.sources {
		writeFile(path, data)
	}
	for _, s := range r.unparsed {
		fmt.Fprintln(os.Stderr, s)
	}
}

// print reports the changes: with all, the ones carried back too. The
// php-cs-fixer changes its report leaves at "one of …" are narrowed first,
// by running the candidate fixers alone (narrowFixers) with command.
func (r *fixReport) print(all bool, command []string) {
	positions := r.refused
	if all {
		positions = append(append([]fixPosition(nil), r.carried...), r.refused...)
	}
	if toolName(command) == "php-cs-fixer" {
		ptrs := make([]*fixPosition, len(positions))
		for i := range positions {
			ptrs[i] = &positions[i]
		}
		narrowFixers(command, ptrs)
	}
	sort.SliceStable(positions, func(i, j int) bool {
		if positions[i].file != positions[j].file {
			return positions[i].file < positions[j].file
		}
		return positions[i].line < positions[j].line
	})
	for _, p := range positions {
		what := "changes"
		if p.compiler {
			what = "changes text the compiler wrote"
		}
		tool := r.tool
		if p.rule != "" {
			tool += " (" + p.rule + ")"
		}
		fmt.Fprintf(os.Stderr, "%s:%d: %s %s: %s → %s\n", reportPath(p.file), p.line, tool, what, clip(p.old), clip(p.new))
	}
	for _, s := range r.others {
		fmt.Fprintln(os.Stderr, s)
	}
	for _, s := range r.unparsed {
		fmt.Fprintln(os.Stderr, s)
	}
}

// clip quotes a change's text, cut to a readable length.
func clip(s string) string {
	if len(s) > 60 {
		s = s[:57] + "…"
	}
	return fmt.Sprintf("%q", s)
}
