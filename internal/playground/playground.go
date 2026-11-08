// Package playground compiles one Phaenon file in memory, for phaenon.dev's
// playground (design/tooling.md §3.6): cmd/phn-wasm, the compiler built for
// WebAssembly, answers the page's phaenon.compile through CompilePage.
//
// What it answers is what phn build writes and phn check reports for the
// same files in a directory without phaenon.json or Composer. The program
// holds the embedded standard library (php-src's stubs, the overlay, the
// debug package's declarations) and the files given, nothing else: no
// vendor code, no extensions of a local php, no disk, so a class declared
// nowhere among them is unknown, as phn check says. Every file given is
// checked, as phn check checks the files it is given; the main file alone
// is compiled, its output withheld when it has an error unless the request
// says otherwise (D10), and never written when it does not parse.
//
// A panic never escapes Compile: it becomes one finding, P0000, and the
// result is not OK. A WebAssembly instance whose Go program panicked is
// dead, and the page would need a reload to compile again.
package playground

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
)

// Internal is the code of a failure that is not a finding about the code:
// a panic, an output the emitter refused (phn build prints that uncoded and
// counts the file failed), a request the entry point cannot read. It lies
// outside the checker's families P1–P4, whose numbers are stable promises
// about the language; this one promises only that something went wrong.
const Internal = "P0000"

// DefaultFilename is the main file's name when the page names none.
const DefaultFilename = "main.phn"

// Request is one compilation.
type Request struct {
	// Files maps paths to sources: Main and the declarations it may use,
	// .phn read as Phaenon and .php as PHP. A .php whose .phn is among them
	// is left out, as phn check leaves out the output beside a source, and
	// so is any other path.
	Files map[string]string
	// Main is the path of the file compiled, a .phn among Files.
	Main string
	// Target is the PHP the output must run on, "8.5" or "8.6"; empty, it
	// is phpversion.Floor, the target of a project that states none.
	Target string
	// EmitOnError is phn build's --emit-on-error: the output of a main file
	// with errors is given all the same.
	EmitOnError bool
}

// Result is what the page shows beside the source.
type Result struct {
	// OK is false when any finding is an error.
	OK bool `json:"ok"`
	// PHP is the main file's output as phn build writes it, line map
	// included; empty when it is withheld or could not be made.
	PHP string `json:"php"`
	// Findings are never nil, so that the page always gets an array.
	Findings []Finding `json:"findings"`
}

// Finding is one diagnostic with the fields of phn check's output: the
// file as the request names it, 1-based lines, and columns counted in
// bytes, as phn check counts them. The end is left out when the
// diagnostic has none.
type Finding struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndLine   int    `json:"endLine,omitempty"`
	EndColumn int    `json:"endColumn,omitempty"`
	Code      string `json:"code"`
	Severity  string `json:"severity"` // "error" or "warning"
	Message   string `json:"message"`
}

// parses keeps the trees of the standard library between compilations:
// the page compiles on every edit, and the stubs, which never change, are
// most of what a program parses. The trees are shared read-only
// (program.ParseCache).
var parses = program.NewParseCache()

// stageHook, when set, is called as each stage of Compile begins; a test
// makes it panic.
var stageHook func(stage string)

// Compile type-checks the files of req and compiles its main file.
func Compile(req Request) (res Result) {
	res.Findings = []Finding{}
	stage := "reading the request"
	defer func() {
		if r := recover(); r != nil {
			res.PHP = ""
			res.Findings = append(res.Findings, Finding{File: req.Main, Line: 1, Column: 1, Code: Internal,
				Severity: checker.Error.String(), Message: fmt.Sprintf("internal error while %s: %v", stage, r)})
		}
		res.OK = !hasError(res.Findings)
	}()
	enter := func(s string) {
		stage = s
		if stageHook != nil {
			stageHook(s)
		}
	}
	enter(stage)
	if msg := misuse(req); msg != "" {
		res.Findings = append(res.Findings, Finding{File: req.Main, Line: 1, Column: 1, Code: Internal,
			Severity: checker.Error.String(), Message: msg})
		return res
	}
	target := phpversion.Floor
	if req.Target != "" {
		v, err := phpversion.Parse(req.Target)
		switch {
		case err != nil:
			res.Findings = append(res.Findings, targetFinding(req.Main, err.Error()))
			return res
		case !v.Supported():
			// phn build writes nothing for a target it does not compile for.
			res.Findings = append(res.Findings, targetFinding(req.Main,
				fmt.Sprintf("PHP %s is not a target phn compiles for: targets are %s to %s", v, phpversion.Floor, phpversion.Newest)))
			return res
		}
		target = v
	}

	enter("loading the standard library")
	p := program.New()
	p.SetTarget(target)
	p.Parses = parses
	if err := p.LoadStubs(); err != nil {
		panic(fmt.Sprintf("stdlib: %v", err))
	}

	enter("parsing")
	var diags []checker.Diagnostic
	for _, name := range sources(req.Files) {
		mode := lexer.ModePhaenon
		if path.Ext(name) == ".php" {
			mode = lexer.ModePHP
		}
		if _, err := p.AddFile(name, []byte(req.Files[name]), mode, true); err != nil {
			// What phn's loadProgram reports for a file that does not parse.
			line := 1
			if pe, ok := err.(*parser.Error); ok {
				line = pe.Line
			}
			diags = append(diags, checker.Diagnostic{File: name, Line: line, Col: 1, Severity: checker.Error, Code: "P1000", Message: err.Error()})
		}
	}

	enter("checking")
	// phn check's options: deprecations are reported. phn build leaves them
	// out, but they are warnings, which withhold no output.
	diags = append(diags, checker.New(p, checker.Options{Deprecations: true}).Check()...)
	broken := false
	for _, d := range diags {
		res.Findings = append(res.Findings, finding(d))
		broken = broken || d.File == req.Main && d.Severity == checker.Error
	}
	f := p.File(req.Main)
	if f == nil || broken && !req.EmitOnError {
		return res
	}

	enter("compiling")
	src := []byte(req.Files[req.Main])
	// The output sits beside its source, as phn build writes it without a
	// project file, so the line map names the source by its base name.
	php, err := emit.File(src, emit.Options{SourceName: req.Main, MapSource: path.Base(req.Main), StrictTypes: true, Program: p, File: f})
	if err != nil {
		line := 1
		msg := err.Error()
		if ee, ok := err.(*emit.Error); ok {
			line, msg = ee.Line, ee.Msg
		}
		res.Findings = append(res.Findings, Finding{File: req.Main, Line: line, Column: 1, Code: Internal,
			Severity: checker.Error.String(), Message: "not compiled: " + msg})
		return res
	}
	res.PHP = string(php)
	return res
}

// misuse says what makes req one Compile cannot answer, "" when nothing
// does.
func misuse(req Request) string {
	switch {
	case req.Main == "":
		return "the request names no file to compile"
	case path.Ext(req.Main) != ".phn":
		return fmt.Sprintf("the file compiled, %q, is not a .phn", req.Main)
	}
	if _, ok := req.Files[req.Main]; !ok {
		return fmt.Sprintf("the file compiled, %q, is not among the files", req.Main)
	}
	for name := range req.Files {
		// The standard library's files are named so; a file of the same
		// name would be taken for the one already loaded.
		if name == "" || strings.HasPrefix(name, "<stub>/") {
			return fmt.Sprintf("%q is not a path a file of the playground can have", name)
		}
	}
	return ""
}

// sources lists the files of a request that phn check would read, in a
// stable order: the .phn and the .php, but a .php whose .phn is there.
func sources(files map[string]string) []string {
	var names []string
	for name := range files {
		switch path.Ext(name) {
		case ".phn":
		case ".php":
			if _, ok := files[strings.TrimSuffix(name, ".php")+".phn"]; ok {
				continue
			}
		default:
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// targetFinding is phn's P4009 for a target it cannot compile for,
// attached to the main file, there being no phaenon.json to point at.
func targetFinding(main, msg string) Finding {
	return Finding{File: main, Line: 1, Column: 1, Code: checker.P4009.ID, Severity: checker.P4009.Severity.String(), Message: msg}
}

func finding(d checker.Diagnostic) Finding {
	return Finding{File: d.File, Line: d.Line, Column: d.Col, EndLine: d.EndLine, EndColumn: d.EndCol,
		Code: d.Code, Severity: d.Severity.String(), Message: d.Message}
}

func hasError(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == checker.Error.String() {
			return true
		}
	}
	return false
}
