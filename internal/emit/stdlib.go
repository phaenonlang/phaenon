package emit

import (
	"path/filepath"
	"strings"

	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/stdlib"
)

// Stdlib compiles the stdlib written in Phaenon (stdlib/runtime/*.phn) to
// the PHP the phaenon/runtime package ships, keyed by file name
// ("Seq.php"). The sources are the compiler's own declaration of these
// classes, so they are compiled from the embedded copy: a project cannot
// build them, the embedded copy already declares the names.
func Stdlib() (map[string][]byte, error) {
	return compilePackage(stdlib.Runtime())
}

// Concurrent compiles the Phaenon sources of the phaenon/concurrent package
// (`Scope.phn`) the same way; the amp declarations beside them are the
// package's dependency, not its contents.
func Concurrent() (map[string][]byte, error) {
	return compilePackage(stdlib.Concurrent())
}

func compilePackage(stubs []stdlib.Stub) (map[string][]byte, error) {
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, s := range stubs {
		name := filepath.Base(s.Path)
		if !strings.HasSuffix(name, ".phn") {
			continue
		}
		f := p.File("<stub>/" + s.Path)
		php, err := Tree(s.Src, f.AST, Options{SourceName: name, StrictTypes: true, Program: p, File: f})
		if err != nil {
			return nil, err
		}
		out[strings.TrimSuffix(name, ".phn")+".php"] = php
	}
	return out, nil
}
