//go:build js && wasm

// Command phn-wasm is the compiler built for WebAssembly, for phaenon.dev's
// playground (design/tooling.md §3.6). Run by Go's wasm_exec.js, it sets
//
//	globalThis.phaenon = {version, compile}
//
// and stays alive for the page's calls: compile(source, options?) answers
// {ok, php, findings} synchronously, the work done by package playground.
// This file only carries values across the boundary: the arguments in as a
// string and JSON, the result out as JSON parsed into plain objects, so
// that the contract lives in Go that native tests run.
package main

import (
	"fmt"
	"syscall/js"

	"github.com/phaenonlang/phaenon/internal/playground"
)

// version is phn's release, set when a release is built
// (-ldflags "-X main.version=0.4.2"), as cmd/phn's is.
var version = "dev"

func main() {
	compile := js.FuncOf(func(_ js.Value, args []js.Value) any {
		return js.Global().Get("JSON").Call("parse", string(call(args).JSON()))
	})
	js.Global().Set("phaenon", js.ValueOf(map[string]any{"version": version, "compile": compile}))
	// Returning would end the Go program and every call after it.
	select {}
}

// call reads compile's arguments and compiles. A panic here, past
// playground's own recovery, would end the instance: JSON.stringify throws
// on a cyclic or BigInt option, and a thrown exception reaches Go as one.
func call(args []js.Value) (res playground.Result) {
	defer func() {
		if r := recover(); r != nil {
			res = playground.Failure(fmt.Sprintf("phaenon.compile: %v", r))
		}
	}()
	if len(args) == 0 || args[0].Type() != js.TypeString {
		got := "nothing"
		if len(args) > 0 {
			got = args[0].Type().String()
		}
		return playground.Failure("phaenon.compile: the source is a string, not " + got)
	}
	var options []byte
	if len(args) > 1 && !args[1].IsUndefined() && !args[1].IsNull() {
		options = []byte(js.Global().Get("JSON").Call("stringify", args[1]).String())
	}
	return playground.CompilePage(args[0].String(), options)
}
