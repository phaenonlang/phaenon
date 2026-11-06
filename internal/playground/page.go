package playground

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/phaenonlang/phaenon/internal/checker"
)

// pageOptions is phaenon.compile's second argument, as JSON.stringify
// writes it. An option the page leaves out, or passes as null, takes the
// page's default; one this version does not know is a mistake of the
// page's, reported rather than ignored.
type pageOptions struct {
	Filename    *string           `json:"filename"`
	Target      *string           `json:"target"`
	Files       map[string]string `json:"files"`
	EmitOnError *bool             `json:"emitOnError"`
}

// CompilePage answers phaenon.compile(source, options): source is the main
// file's text and options the second argument as JSON, empty or "null"
// when the page passed none. The defaults are the page's, not phn build's:
// the file is DefaultFilename, the target phn's floor, there are no further
// files, and the output is given even when the file has errors (emitOnError
// true), so that the playground shows the PHP beside the findings whenever
// the parser got through.
func CompilePage(source string, options []byte) Result {
	var opts pageOptions
	if trimmed := bytes.TrimSpace(options); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&opts); err != nil {
			return Failure(fmt.Sprintf("phaenon.compile: the options are an object of filename, target, files and emitOnError: %v", err))
		}
	}
	req := Request{Main: DefaultFilename, EmitOnError: true, Files: map[string]string{}}
	if opts.Filename != nil {
		req.Main = *opts.Filename
	}
	if opts.Target != nil {
		req.Target = *opts.Target
	}
	if opts.EmitOnError != nil {
		req.EmitOnError = *opts.EmitOnError
	}
	if _, ok := opts.Files[req.Main]; ok {
		return Failure(fmt.Sprintf("phaenon.compile: options.files holds %q, the file the source is", req.Main))
	}
	for name, src := range opts.Files {
		req.Files[name] = src
	}
	req.Files[req.Main] = source
	return Compile(req)
}

// Failure is the result of a call that compiled nothing: one P0000 finding
// with the message, on the default file. cmd/phn-wasm answers with it when
// the page's arguments cannot be read at all.
func Failure(message string) Result {
	return Result{Findings: []Finding{{File: DefaultFilename, Line: 1, Column: 1, Code: Internal, Severity: checker.Error.String(), Message: message}}}
}

// JSON is the result as phaenon.compile hands it to the page, which
// JSON.parse turns into plain objects.
func (r Result) JSON() []byte {
	data, err := json.Marshal(r)
	if err != nil {
		// Strings, ints and booleans only: Marshal cannot fail on them.
		panic(err)
	}
	return data
}
