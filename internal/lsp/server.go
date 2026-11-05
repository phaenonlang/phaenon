// Package lsp is phn lsp: the checker in editors, over the Language Server
// Protocol on stdin and stdout (design/tooling.md §6.1).
//
// One server serves one project. It keeps no program model between
// requests; it builds one for every check, which is cheap because the
// parsed files and the extensions' stubs are kept (program.ParseCache):
//
//   - an edit re-checks its own file, against every file of the project,
//     the open buffers as they are, after a pause in typing;
//   - a save re-checks the whole project in the background, publishes every
//     file's diagnostics, and, with build on save, writes the outputs as
//     phn build does. A newer save cancels a check still running.
//
// The answers to hover, go to definition, completion and signature help
// come from the latest check of the document: the type the checker gave
// each expression. PhpStorm asks for diagnostics only (InitOptions), since
// its PSI answers the rest; VS Code gets everything.
//
// Paths are the Host's: relative to the project's root for its own files.
package lsp

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// Host is the project the server serves. phn's command line implements it:
// it knows how a project is read (phaenon.json, Composer, the target) and
// built.
type Host interface {
	// Root is the project's directory, absolute.
	Root() string
	// Files lists the project's sources the checker reads: the .phn of its
	// roots, and the plain .php that no .phn compiles to.
	Files() []string
	// Program builds a program of the files, each read through read,
	// checking those check reports. diags are the files that do not parse
	// (P1000) and what could not be read: vendor code, extensions.
	Program(files []string, read func(path string) ([]byte, error), check func(path string) bool) (p *program.Program, diags []checker.Diagnostic, err error)
	// ProjectDiagnostics are the findings about the project as a whole: its
	// target, its installed packages (P4013, P4014).
	ProjectDiagnostics(p *program.Program, files []string) []checker.Diagnostic
	// Build compiles the project from p, a program of what is on disk,
	// checked with diags: outputs are written as phn build writes them, and
	// a file with errors is withheld (D10).
	Build(p *program.Program, diags []checker.Diagnostic) (BuildParams, error)
	// ClassNames lists the classes Composer's maps can load, which no
	// program declares until code names them: class completion offers them.
	ClassNames() []string
}

// Server is one session with an editor.
type Server struct {
	host Host
	conn *conn
	log  io.Writer

	// Debounce is the pause after an edit before its file is checked, and
	// after a save before the project is.
	Debounce time.Duration

	mu          sync.Mutex
	initialized bool
	shutdown    bool
	diagsOnly   bool
	buildOnSave bool
	snippets    bool
	watch       bool                 // the client registers file watchers on request
	docs        map[string]*document // open documents by path
	published   map[string]bool      // paths a project check published diagnostics for
	projectGen  int                  // the newest project check; an older one stops
	projectT    *time.Timer
	wantBuild   bool // a save asked for a build no check has done yet
	// clientRoot is the project's root as the client spells it (rootUri),
	// which may differ from the Host's through a symbolic link: URIs sent
	// to the client use it. A rootUri naming another directory, such as
	// the workspace around the project, is not kept (sameDir).
	clientRoot string
	requestID  int
	wg         sync.WaitGroup
	// query serialises the requests that read a check's program: the
	// model builds signatures and loads vendor classes on demand, writing
	// into itself.
	query sync.Mutex
}

// document is an open buffer.
type document struct {
	path    string
	uri     string
	version int
	text    []byte
	timer   *time.Timer
	// result is the check of the newest version checked; good, the newest
	// whose file parsed, for the questions a broken buffer cannot answer.
	result, good *fileCheck
}

// fileCheck is one check of a document's text.
type fileCheck struct {
	version int
	text    *text
	prog    *program.Program
	file    *program.File // nil when the text does not parse
	types   map[ast.Expr]types.Type
	diags   []checker.Diagnostic
}

// New returns a server for the host's project.
func New(host Host, log io.Writer) *Server {
	return &Server{host: host, log: log, Debounce: 150 * time.Millisecond,
		docs: map[string]*document{}, published: map[string]bool{}, buildOnSave: true}
}

// Run serves the session on in and out until the client exits.
func (s *Server) Run(in io.Reader, out io.Writer) error {
	s.conn = newConn(in, out)
	for {
		m, err := s.conn.read()
		if err == io.EOF {
			s.wg.Wait()
			return nil
		}
		if err != nil {
			if re, ok := err.(*rpcError); ok {
				s.conn.replyError(nil, re)
				continue
			}
			return err
		}
		if m.Method == "" {
			continue // the response to a request of the server's own
		}
		if m.Method == "exit" {
			s.wg.Wait()
			return nil
		}
		s.dispatch(m)
	}
}

func (s *Server) logf(format string, args ...any) {
	fmt.Fprintf(s.log, "phn lsp: "+format+"\n", args...)
}

// dispatch handles a request or notification. Requests that need a check
// are answered on their own goroutine, so a slow one holds up no other.
func (s *Server) dispatch(m *message) {
	isRequest := len(m.ID) > 0
	s.mu.Lock()
	ready, down := s.initialized, s.shutdown
	s.mu.Unlock()
	if !ready && m.Method != "initialize" {
		if isRequest {
			s.conn.replyError(m.ID, &rpcError{Code: codeNotInitialized, Message: "the server is not initialized"})
		}
		return
	}
	if down && isRequest && m.Method != "shutdown" {
		s.conn.replyError(m.ID, &rpcError{Code: codeInvalidParams, Message: "the server is shutting down"})
		return
	}
	handle := func(f func() (any, error)) {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			result, err := s.safely(f)
			if !isRequest {
				if err != nil {
					s.logf("%s: %v", m.Method, err)
				}
				return
			}
			if err != nil {
				re, ok := err.(*rpcError)
				if !ok {
					re = &rpcError{Code: codeInternalError, Message: err.Error()}
				}
				s.conn.replyError(m.ID, re)
				return
			}
			s.conn.reply(m.ID, result)
		}()
	}
	switch m.Method {
	case "initialize":
		var p InitializeParams
		if err := json.Unmarshal(m.Params, &p); err != nil {
			s.conn.replyError(m.ID, &rpcError{Code: codeInvalidParams, Message: err.Error()})
			return
		}
		s.conn.reply(m.ID, s.initialize(p))
	case "initialized":
		s.registerWatchers()
		s.scheduleProject(0, false)
	case "shutdown":
		s.mu.Lock()
		s.shutdown = true
		s.projectGen++ // stops a running check
		s.mu.Unlock()
		s.conn.reply(m.ID, nil)
	case "textDocument/didOpen":
		var p DidOpenTextDocumentParams
		if json.Unmarshal(m.Params, &p) == nil {
			s.didOpen(p)
		}
	case "textDocument/didChange":
		var p DidChangeTextDocumentParams
		if json.Unmarshal(m.Params, &p) == nil {
			s.didChange(p)
		}
	case "textDocument/didSave":
		s.scheduleProject(s.Debounce*2, true)
	case "textDocument/didClose":
		var p DidCloseTextDocumentParams
		if json.Unmarshal(m.Params, &p) == nil {
			s.didClose(p)
		}
	case "workspace/didChangeWatchedFiles":
		s.scheduleProject(s.Debounce*2, false)
	case "workspace/didChangeConfiguration":
		var p DidChangeConfigurationParams
		if json.Unmarshal(m.Params, &p) == nil && p.Settings.Phaenon != nil && p.Settings.Phaenon.BuildOnSave != nil {
			s.mu.Lock()
			s.buildOnSave = *p.Settings.Phaenon.BuildOnSave
			s.mu.Unlock()
		}
	case "$/cancelRequest", "$/setTrace":
	case "textDocument/hover":
		var p TextDocumentPositionParams
		if s.params(m, &p) {
			handle(s.serial(func() (any, error) { return s.hover(p) }))
		}
	case "textDocument/definition":
		var p TextDocumentPositionParams
		if s.params(m, &p) {
			handle(s.serial(func() (any, error) { return s.definition(p) }))
		}
	case "textDocument/documentSymbol":
		var p struct {
			TextDocument TextDocumentIdentifier `json:"textDocument"`
		}
		if s.params(m, &p) {
			handle(s.serial(func() (any, error) { return s.documentSymbols(p.TextDocument.URI) }))
		}
	case "textDocument/completion":
		var p CompletionParams
		if s.params(m, &p) {
			handle(s.serial(func() (any, error) { return s.completion(p.TextDocumentPositionParams) }))
		}
	case "textDocument/signatureHelp":
		var p TextDocumentPositionParams
		if s.params(m, &p) {
			handle(s.serial(func() (any, error) { return s.signatureHelp(p) }))
		}
	case "textDocument/formatting":
		var p DocumentFormattingParams
		if s.params(m, &p) {
			handle(func() (any, error) { return s.formatting(p.TextDocument.URI) })
		}
	case "textDocument/codeAction":
		var p CodeActionParams
		if s.params(m, &p) {
			handle(func() (any, error) { return s.codeActions(p) })
		}
	default:
		if isRequest {
			s.conn.replyError(m.ID, &rpcError{Code: codeMethodNotFound, Message: "unknown method " + m.Method})
		}
	}
}

// serial runs f holding the query lock.
func (s *Server) serial(f func() (any, error)) func() (any, error) {
	return func() (any, error) {
		s.query.Lock()
		defer s.query.Unlock()
		return f()
	}
}

// params decodes a request's parameters, answering a bad one itself.
func (s *Server) params(m *message, v any) bool {
	if err := json.Unmarshal(m.Params, v); err != nil {
		if len(m.ID) > 0 {
			s.conn.replyError(m.ID, &rpcError{Code: codeInvalidParams, Message: err.Error()})
		}
		return false
	}
	return true
}

// safely runs f, turning a panic into an error: a bug answering one
// request must not end the session.
func (s *Server) safely(f func() (any, error)) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logf("panic: %v\n%s", r, debug.Stack())
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return f()
}

func (s *Server) initialize(p InitializeParams) any {
	s.mu.Lock()
	s.initialized = true
	if o := p.InitializationOptions; o != nil {
		s.diagsOnly = o.Features == "diagnostics"
		if o.BuildOnSave != nil {
			s.buildOnSave = *o.BuildOnSave
		}
	}
	s.snippets = p.Capabilities.TextDocument.Completion.CompletionItem.SnippetSupport
	if root, ok := pathOf(p.RootURI); ok && p.RootURI != "" && sameDir(root, s.host.Root()) {
		s.clientRoot = root
	}
	s.watch = p.Capabilities.Workspace.DidChangeWatchedFiles.DynamicRegistration
	diagsOnly := s.diagsOnly
	s.mu.Unlock()
	caps := map[string]any{
		// Full text on every change: .phn files are small, and nothing can
		// drift.
		"textDocumentSync":   map[string]any{"openClose": true, "change": 1, "save": map[string]any{"includeText": false}},
		"codeActionProvider": map[string]any{"codeActionKinds": []string{"quickfix"}},
	}
	if !diagsOnly {
		caps["hoverProvider"] = true
		caps["definitionProvider"] = true
		caps["documentSymbolProvider"] = true
		caps["documentFormattingProvider"] = true
		caps["completionProvider"] = map[string]any{"triggerCharacters": []string{">", ":", "$", " "}}
		caps["signatureHelpProvider"] = map[string]any{"triggerCharacters": []string{"(", ","}}
	}
	return map[string]any{
		"capabilities": caps,
		"serverInfo":   map[string]any{"name": "phn"},
	}
}

// registerWatchers asks the client to report changes on disk that no open
// buffer shows: a git checkout, phn fmt, composer install.
func (s *Server) registerWatchers() {
	s.mu.Lock()
	watch := s.watch
	s.requestID++
	id := s.requestID
	s.mu.Unlock()
	if !watch {
		return
	}
	s.conn.request(id, "client/registerCapability", map[string]any{
		"registrations": []any{map[string]any{
			"id":     "phaenon-files",
			"method": "workspace/didChangeWatchedFiles",
			"registerOptions": map[string]any{"watchers": []any{
				map[string]any{"globPattern": "**/*.phn"},
				map[string]any{"globPattern": "**/phaenon.json"},
				map[string]any{"globPattern": "**/composer.{json,lock}"},
				map[string]any{"globPattern": "**/vendor/composer/installed.json"},
			}},
		}},
	})
}

// ------------------------------------------------------------------ documents

// path is the Host's path of a URI: relative to the root under it. The
// root is matched as the client spells it, as the Host does, and with
// symbolic links resolved (macOS's /var is /private/var).
func (s *Server) path(uri string) (string, bool) {
	abs, ok := pathOf(uri)
	if !ok {
		return "", false
	}
	s.mu.Lock()
	roots := []string{s.clientRoot, s.host.Root()}
	s.mu.Unlock()
	for _, root := range roots {
		if rel, ok := under(abs, root); ok {
			return rel, true
		}
	}
	real, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err == nil {
		real = filepath.Join(real, filepath.Base(abs))
		for _, root := range roots {
			if r, err := filepath.EvalSymlinks(root); err == nil {
				if rel, ok := under(real, r); ok {
					return rel, true
				}
			}
		}
	}
	return abs, true
}

// sameDir is whether two paths name one directory, spelled alike or
// through symbolic links.
func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// under is path relative to root, when it lies under it.
func under(path, root string) (string, bool) {
	if root == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// uri is the URI of a Host's path, under the root as the client spells it.
func (s *Server) uri(path string) string {
	if !filepath.IsAbs(path) {
		s.mu.Lock()
		root := s.clientRoot
		s.mu.Unlock()
		if root == "" {
			root = s.host.Root()
		}
		path = filepath.Join(root, path)
	}
	return uriOf(path)
}

func (s *Server) didOpen(p DidOpenTextDocumentParams) {
	path, ok := s.path(p.TextDocument.URI)
	if !ok {
		return
	}
	s.mu.Lock()
	d := &document{path: path, uri: p.TextDocument.URI, version: p.TextDocument.Version, text: []byte(p.TextDocument.Text)}
	s.docs[path] = d
	s.mu.Unlock()
	s.scheduleFile(d, 0)
}

func (s *Server) didChange(p DidChangeTextDocumentParams) {
	path, ok := s.path(p.TextDocument.URI)
	if !ok {
		return
	}
	s.mu.Lock()
	d := s.docs[path]
	if d == nil {
		s.mu.Unlock()
		return
	}
	for _, c := range p.ContentChanges {
		if c.Range == nil {
			d.text = []byte(c.Text)
			continue
		}
		t := newText(d.text)
		start, end := t.offset(c.Range.Start), t.offset(c.Range.End)
		d.text = append(append(append([]byte(nil), d.text[:start]...), c.Text...), d.text[end:]...)
	}
	d.version = p.TextDocument.Version
	s.mu.Unlock()
	s.scheduleFile(d, s.Debounce)
}

func (s *Server) didClose(p DidCloseTextDocumentParams) {
	path, ok := s.path(p.TextDocument.URI)
	if !ok {
		return
	}
	s.mu.Lock()
	d := s.docs[path]
	delete(s.docs, path)
	if d != nil && d.timer != nil {
		d.timer.Stop()
	}
	s.mu.Unlock()
}

// reader reads a path from the open buffers, else from disk.
func (s *Server) reader() func(path string) ([]byte, error) {
	s.mu.Lock()
	open := make(map[string][]byte, len(s.docs))
	for path, d := range s.docs {
		open[path] = d.text
	}
	s.mu.Unlock()
	root := s.host.Root()
	return func(path string) ([]byte, error) {
		if src, ok := open[path]; ok {
			return src, nil
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		return os.ReadFile(path)
	}
}

// ------------------------------------------------------------------ checking a file

// scheduleFile checks a document after a pause.
func (s *Server) scheduleFile(d *document, after time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(after, func() {
		s.wg.Add(1)
		defer s.wg.Done()
		if r, err := s.current(d.path); err != nil {
			s.logf("check %s: %v", d.path, err)
		} else if r != nil {
			s.publishFile(d, r)
		}
	})
}

// current returns the check of a document's newest version, checking it
// now if it has not been: a request asked about it, or the pause ended.
func (s *Server) current(path string) (*fileCheck, error) {
	s.mu.Lock()
	d := s.docs[path]
	if d == nil {
		s.mu.Unlock()
		return nil, nil
	}
	if d.result != nil && d.result.version == d.version {
		r := d.result
		s.mu.Unlock()
		return r, nil
	}
	version, src := d.version, d.text
	s.mu.Unlock()
	r, err := s.checkText(path, src, version)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if d.version == version {
		d.result = r
		if r.file != nil {
			d.good = r
		}
	}
	s.mu.Unlock()
	return r, nil
}

// checkText checks src as the text of path, against the project's other
// files as the buffers and the disk have them, and records the type of
// every expression in it.
func (s *Server) checkText(path string, src []byte, version int) (*fileCheck, error) {
	read := s.reader()
	files := s.host.Files()
	if !contains(files, path) {
		files = append(files, path) // a file outside the roots, or a new one
	}
	p, diags, err := s.host.Program(files,
		func(q string) ([]byte, error) {
			if q == path {
				return src, nil
			}
			return read(q)
		},
		func(q string) bool { return q == path })
	if err != nil {
		return nil, err
	}
	r := &fileCheck{version: version, text: newText(src), prog: p, file: p.File(path), types: map[ast.Expr]types.Type{}}
	for _, d := range diags {
		if d.File == path {
			r.diags = append(r.diags, d)
		}
	}
	if r.file == nil {
		return r, nil
	}
	c := checker.New(p, checker.Options{Deprecations: true, Observe: func(f *program.File, e ast.Expr, t types.Type) {
		if f == r.file {
			r.types[e] = t
		}
	}})
	for _, d := range c.Check() {
		if d.File == path {
			r.diags = append(r.diags, d)
		}
	}
	return r, nil
}

// publishFile publishes a document's diagnostics, unless it has changed
// since: the newer version's check publishes its own.
func (s *Server) publishFile(d *document, r *fileCheck) {
	s.mu.Lock()
	stale := s.docs[d.path] != d || d.version != r.version
	s.mu.Unlock()
	if stale {
		return
	}
	version := r.version
	s.conn.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI: d.uri, Version: &version, Diagnostics: s.diagnostics(r.text, r.diags, r.prog),
	})
}

// diagnostics converts the checker's findings in a text to LSP's; their
// related declarations are positions in p's files.
func (s *Server) diagnostics(t *text, diags []checker.Diagnostic, p *program.Program) []Diagnostic {
	out := []Diagnostic{}
	for _, d := range diags {
		start := t.lineCol(d.Line, d.Col)
		end := start
		if d.EndLine > 0 {
			end = t.lineCol(d.EndLine, d.EndCol)
		}
		if end <= start {
			end = start + 1
			if end > len(t.src) {
				end = len(t.src)
			}
		}
		severity := 1
		if d.Severity != checker.Error {
			severity = 2
		}
		diag := Diagnostic{Range: t.rangeOf(start, end), Severity: severity, Code: d.Code, Source: "phn", Message: d.Message}
		for _, rel := range d.Related {
			if loc := s.relatedLocation(rel, p); loc != nil {
				diag.RelatedInformation = append(diag.RelatedInformation, DiagnosticRelatedInformation{Location: *loc, Message: rel.Message})
			}
		}
		out = append(out, diag)
	}
	return out
}

// relatedLocation is a related declaration's position as LSP's: a stub's
// file is written where the editor can open it, as go to definition does.
func (s *Server) relatedLocation(rel checker.Related, p *program.Program) *Location {
	if p == nil {
		return nil
	}
	f := p.File(rel.File)
	if f == nil {
		return nil
	}
	t := newText(f.Src)
	start := t.lineCol(rel.Line, rel.Col)
	end := t.lineCol(rel.EndLine, rel.EndCol)
	if end < start {
		end = start
	}
	loc, err := s.location(&location{file: f, span: ast.Span{Start: start, End: end}})
	if err != nil {
		return nil
	}
	return loc
}

// ------------------------------------------------------------------ checking the project

// scheduleProject checks the whole project after a pause; a later call
// replaces an earlier one, and stops one that is running. build, with
// build on save on, writes the outputs afterwards: a save does, opening the
// project and a change on disk do not.
func (s *Server) scheduleProject(after time.Duration, build bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shutdown {
		return
	}
	s.projectGen++
	gen := s.projectGen
	if s.projectT != nil {
		s.projectT.Stop()
	}
	// A save's build waits for the check that completes: a later check
	// that replaces the save's still builds.
	s.wantBuild = s.wantBuild || build
	s.projectT = time.AfterFunc(after, func() {
		s.wg.Add(1)
		defer s.wg.Done()
		if _, err := s.safely(func() (any, error) { return nil, s.checkProject(gen) }); err != nil {
			s.logf("project: %v", err)
		}
	})
}

func (s *Server) stale(gen int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return gen != s.projectGen || s.shutdown
}

// checkProject checks every file of the project, the open buffers as they
// are, and publishes each file's diagnostics; then builds, if asked to and
// every open buffer is saved.
func (s *Server) checkProject(gen int) error {
	s.mu.Lock()
	versions := map[string]int{}
	for path, d := range s.docs {
		versions[path] = d.version
	}
	s.mu.Unlock()
	read := s.reader()
	files := s.host.Files()
	dirty := false
	for path := range versions {
		if src, err := read(path); err == nil {
			disk, err := os.ReadFile(filepath.Join(s.host.Root(), path))
			if err != nil || string(disk) != string(src) {
				dirty = true
			}
		}
	}
	started := time.Now()
	p, diags, err := s.host.Program(files, read, func(string) bool { return true })
	if err != nil {
		return err
	}
	c := checker.New(p, checker.Options{Deprecations: true, Cancelled: func() bool { return s.stale(gen) }})
	diags = append(diags, c.Check()...)
	if s.stale(gen) {
		return nil
	}
	diags = append(diags, s.host.ProjectDiagnostics(p, files)...)
	s.publishProject(p, diags, versions)

	s.mu.Lock()
	build := s.buildOnSave && s.wantBuild && gen == s.projectGen
	if build {
		s.wantBuild = false
	}
	s.mu.Unlock()
	if !build {
		return nil
	}
	if dirty {
		// The outputs are compiled from what is saved: a program of the
		// disk alone.
		disk := func(path string) ([]byte, error) {
			if !filepath.IsAbs(path) {
				path = filepath.Join(s.host.Root(), path)
			}
			return os.ReadFile(path)
		}
		if p, diags, err = s.host.Program(files, disk, func(string) bool { return true }); err != nil {
			return err
		}
		diags = append(diags, checker.New(p, checker.Options{Cancelled: func() bool { return s.stale(gen) }}).Check()...)
		if s.stale(gen) {
			return nil
		}
	}
	res, err := s.host.Build(p, diags)
	res.DurationMs = time.Since(started).Milliseconds()
	if err != nil {
		res.Error = err.Error()
	}
	if !s.stale(gen) {
		s.conn.notify("phaenon/build", res)
	}
	return nil
}

// publishProject publishes every file's diagnostics from a project check,
// and clears the files a previous one published for that have none now. An
// open document edited since the check started keeps its own check's.
func (s *Server) publishProject(p *program.Program, diags []checker.Diagnostic, versions map[string]int) {
	byFile := map[string][]checker.Diagnostic{}
	for _, d := range diags {
		byFile[d.File] = append(byFile[d.File], d)
	}
	s.mu.Lock()
	var paths []string
	for path := range byFile {
		paths = append(paths, path)
	}
	for path := range s.published {
		if _, ok := byFile[path]; !ok {
			paths = append(paths, path)
		}
	}
	s.published = map[string]bool{}
	for path := range byFile {
		s.published[path] = true
	}
	edited := map[string]bool{}
	for path, v := range versions {
		if d := s.docs[path]; d == nil || d.version != v {
			edited[path] = true
		}
	}
	s.mu.Unlock()
	sort.Strings(paths)
	for _, path := range paths {
		if edited[path] {
			continue
		}
		var t *text
		if f := p.File(path); f != nil {
			t = newText(f.Src)
		} else if src, err := s.reader()(path); err == nil {
			t = newText(src)
		} else {
			t = newText(nil)
		}
		s.conn.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
			URI: s.uri(path), Diagnostics: s.diagnostics(t, byFile[path], p),
		})
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
