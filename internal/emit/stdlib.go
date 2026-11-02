package emit

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/stdlib"
)

// Stdlib compiles the stdlib written in Phaenon (stdlib/runtime/**/*.phn)
// to the PHP the phaenon/runtime package ships, keyed by the path below the
// package's src/ ("Seq.php", "Debug/LineMap.php"). The sources are the
// compiler's own declaration of these classes, so they are compiled from
// the embedded copy: a project cannot build them, the embedded copy already
// declares the names. The program also holds the declarations of the
// libraries the runtime suggests (stdlib.Suggested), which the Debug
// integrations are written against.
func Stdlib() (map[string][]byte, error) {
	return compilePackage(stdlib.Runtime(), true)
}

// Concurrent compiles the Phaenon sources of the phaenon/concurrent package
// (`Scope.phn`) the same way; the amp declarations beside them are the
// package's dependency, not its contents.
func Concurrent() (map[string][]byte, error) {
	return compilePackage(stdlib.Concurrent(), false)
}

func compilePackage(stubs []stdlib.Stub, suggested bool) (map[string][]byte, error) {
	p := program.New()
	load := p.LoadStubs
	if suggested {
		load = p.LoadSuggested
	}
	if err := load(); err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, s := range stubs {
		if !strings.HasSuffix(s.Path, ".phn") {
			continue
		}
		f := p.File("<stub>/" + s.Path)
		// No line map: the packages ship without their .phn sources, so
		// their positions are meant as the PHP they are.
		php, err := Tree(s.Src, f.AST, Options{SourceName: filepath.Base(s.Path), StrictTypes: true, NoMap: true, Program: p, File: f})
		if err != nil {
			return nil, err
		}
		out[packagePath(s.Path)] = php
	}
	return out, nil
}

// packagePath is where an embedded stdlib file lands in its package: the
// path below the embedded root, a .phn source as its compiled .php
// ("runtime/Debug/LineMap.phn" → "Debug/LineMap.php"), which mirrors the
// package's src/ and its PSR-4 namespace. Stub paths use forward slashes on
// every system (embed.FS).
func packagePath(stub string) string {
	rel := stub
	if i := strings.IndexByte(stub, '/'); i >= 0 {
		rel = stub[i+1:]
	}
	rel = path.Clean(rel)
	if strings.HasSuffix(rel, ".phn") {
		rel = strings.TrimSuffix(rel, ".phn") + ".php"
	}
	return rel
}
