package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/lsp"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/program"
)

// lspCmd is `phn lsp`: the language server of the project around the
// working directory, on stdin and stdout (design/tooling.md §6.1).
func lspCmd(args []string) {
	fs := flag.NewFlagSet("lsp", flag.ExitOnError)
	fs.Bool("stdio", true, "speak LSP on stdin and stdout (the only transport; VS Code's clients pass the flag)")
	fs.Parse(args)
	if err := enterProject(); err != nil {
		fail("phn lsp: %v", err)
	}
	host := newProjectHost()
	// The protocol owns stdout: whatever else phn prints goes to stderr,
	// which editors keep as the server's log.
	out := os.Stdout
	os.Stdout = os.Stderr
	srv := lsp.New(host, os.Stderr)
	if err := srv.Run(os.Stdin, out); err != nil {
		fail("phn lsp: %v", err)
	}
}

// enterProject moves to the project's root: the nearest directory above
// the working directory with a phaenon.json, else with a composer.json.
// Either way the process is then long-running: programs share their parsed
// files, and a fail is a job's error, not the process's end.
func enterProject() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	root := ""
	for _, marker := range []string{ProjectFile, "composer.json"} {
		for d := dir; ; d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, marker)); err == nil {
				root = d
				break
			}
			if filepath.Dir(d) == d {
				break
			}
		}
		if root != "" {
			break
		}
	}
	if root == "" {
		root = dir
	}
	if err := os.Chdir(root); err != nil {
		return err
	}
	share = newProgramShare()
	failPanics = true
	return nil
}

// projectHost is the project in the working directory, as phn check and
// phn build read it, for phn lsp and phn watch.
type projectHost struct {
	root  string
	proj  *project // nil without a phaenon.json
	roots []string // nil without a phaenon.json: no files but the open ones
	tgt   target
}

func newProjectHost() *projectHost {
	root, _ := os.Getwd()
	h := &projectHost{root: root}
	proj, err := readProject(ProjectFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "phn: %v\n", err)
	}
	h.proj = proj
	if proj != nil && len(proj.Roots) > 0 {
		h.roots = proj.roots()
		excludes = append(excludes, proj.Exclude...)
		projectSources = proj.sources()
	}
	anchor := h.roots
	if anchor == nil {
		anchor = []string{"."}
	}
	h.tgt = projectTarget(proj, anchor)
	return h
}

func (h *projectHost) Root() string { return h.root }

// Files lists what phn check checks: the roots' .phn, and their plain .php
// that no .phn compiles to.
func (h *projectHost) Files() []string {
	if h.roots == nil {
		return nil
	}
	skip := func(path string) bool { return excluded(path) || h.proj.isOutput(path) }
	var files []string
	for _, f := range collectQuiet(h.roots, skip) {
		if filepath.Ext(f) == ".php" {
			if _, err := os.Stat(strings.TrimSuffix(f, ".php") + ".phn"); err == nil {
				continue
			}
		}
		files = append(files, f)
	}
	return files
}

// collectQuiet is collect for a server: a root that is gone lists nothing
// rather than ending the process.
func collectQuiet(roots []string, skip func(string) bool) (files []string) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(failure); !ok {
				panic(r)
			}
			files = nil
		}
	}()
	return collect(roots, skip, ".phn", ".php")
}

func (h *projectHost) Program(files []string, read func(string) ([]byte, error), check func(string) bool) (p *program.Program, diags []checker.Diagnostic, err error) {
	defer recoverFailure(&err)
	anchor := ""
	if len(files) > 0 {
		anchor = files[0]
	} else {
		anchor = filepath.Join(h.root, "composer.json")
	}
	p, diags = newProgram(anchor, h.tgt.Version)
	for _, f := range files {
		src, err := read(f)
		if err != nil {
			continue // removed since it was listed
		}
		mode := lexer.ModePhaenon
		if filepath.Ext(f) == ".php" {
			mode = lexer.ModePHP
		}
		if _, err := p.AddFile(f, src, mode, check(f)); err != nil {
			line := 1
			if pe, ok := err.(*parser.Error); ok {
				line = pe.Line
			}
			diags = append(diags, checker.Diagnostic{File: f, Line: line, Col: 1, Severity: checker.Error, Code: "P1000", Message: err.Error()})
		}
	}
	return p, diags, nil
}

// Options adds the checker's settings from phaenon.json, as read when the
// host was made.
func (h *projectHost) Options(opts checker.Options) checker.Options {
	return h.proj.checkerOptions(opts)
}

func (h *projectHost) ProjectDiagnostics(p *program.Program, files []string) []checker.Diagnostic {
	return append(append([]checker.Diagnostic(nil), h.tgt.Diags...), installedDiags(files, p)...)
}

func (h *projectHost) Build(p *program.Program, diags []checker.Diagnostic) (res lsp.BuildParams, err error) {
	defer recoverFailure(&err)
	if h.roots == nil {
		return res, errors.New("no " + ProjectFile + ": nothing to build")
	}
	if h.tgt.Unsupported {
		return res, errors.New("the target is not one phn compiles for")
	}
	job := buildJob{proj: h.proj, roots: h.roots, target: h.tgt, quiet: true, mirror: true, prog: p, diags: diags}
	r := job.run()
	return lsp.BuildParams{Files: r.files, Written: r.written + r.copied + r.removed, Withheld: r.failed}, nil
}

func (h *projectHost) ClassNames() []string {
	l, err := share.composer(h.root)
	if err != nil {
		return nil
	}
	return l.ClassNames()
}

// ------------------------------------------------------------------ phn watch

// watchCmd is `phn watch`: phn check and phn build each time a file of the
// project changes, for terminals and editors without the server
// (design/tooling.md §6.4). The standard library has no file events, so it
// polls the roots' modification times.
func watchCmd(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	noBuild := fs.Bool("no-build", false, "check only; write no outputs")
	every := fs.Duration("interval", 300*time.Millisecond, "how often to look for changes")
	fs.Parse(args)
	if fs.NArg() > 0 {
		fail("phn watch takes no paths: it watches the roots of %s", ProjectFile)
	}
	proj := loadProject()
	proj.requireRoots("watch")
	if err := enterProject(); err != nil {
		fail("phn watch: %v", err)
	}
	host := newProjectHost()
	last := watchStamps(host)
	for {
		watchRun(host, !*noBuild)
		fmt.Println("phn watch: waiting for changes (Ctrl-C to stop)")
		for {
			time.Sleep(*every)
			cur := watchStamps(host)
			if !sameStamps(cur, last) {
				// Let an editor's or a tool's burst of writes settle.
				time.Sleep(*every / 3)
				last = watchStamps(host)
				break
			}
		}
	}
}

// watchRun checks the project, prints the findings as phn check does, and
// builds.
func watchRun(host *projectHost, build bool) {
	started := time.Now()
	err := func() (err error) {
		defer recoverFailure(&err)
		files := host.Files()
		p, diags, err := host.Program(files, os.ReadFile, func(string) bool { return true })
		if err != nil {
			return err
		}
		diags = append(diags, checker.New(p, host.Options(checker.Options{Deprecations: true})).Check()...)
		diags = append(diags, host.ProjectDiagnostics(p, files)...)
		loadedProgram = p // printDiag shows the source lines
		errs, warnings := 0, 0
		for _, d := range diags {
			if excluded(d.File) {
				continue
			}
			printDiag(d)
			if d.Severity == checker.Error {
				errs++
			} else {
				warnings++
			}
		}
		fmt.Printf("%d files checked: %d errors, %d warnings\n", len(files), errs, warnings)
		if !build {
			return nil
		}
		res, err := host.Build(p, diags)
		if err != nil {
			return err
		}
		fmt.Printf("%d files compiled, %d outputs written, %d withheld (%.1fs)\n", res.Files, res.Written, res.Withheld, time.Since(started).Seconds())
		return nil
	}()
	if err != nil {
		fmt.Fprintf(os.Stderr, "phn watch: %v\n", err)
	}
}

type stamp struct {
	mod  time.Time
	size int64
}

// watchStamps records the modification time and size of every file a
// check or build reads: the roots' sources, the project files, Composer's
// install state.
func watchStamps(h *projectHost) map[string]stamp {
	out := map[string]stamp{}
	add := func(path string, info fs.FileInfo) {
		out[path] = stamp{info.ModTime(), info.Size()}
	}
	for _, f := range []string{ProjectFile, "composer.json", "composer.lock", filepath.Join("vendor", "composer", "installed.json")} {
		if info, err := os.Stat(f); err == nil {
			add(f, info)
		}
	}
	for _, root := range h.roots {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != root && (h.proj.isOutput(path) || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if ext := filepath.Ext(path); ext == ".phn" || ext == ".php" {
				if info, err := d.Info(); err == nil {
					add(path, info)
				}
			}
			return nil
		})
	}
	return out
}

func sameStamps(a, b map[string]stamp) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if bv, ok := b[k]; !ok || !bv.mod.Equal(a[k].mod) || bv.size != a[k].size {
			return false
		}
	}
	return true
}
