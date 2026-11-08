package program

import (
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/stdlib"
)

// overlayOutsidePHPSrc are the overlay files that refine declarations no
// php-src stub makes: PECL extensions and PHPUnit.
var overlayOutsidePHPSrc = map[string]bool{
	"overlay/mongodb.php": true, "overlay/redis.php": true, "overlay/phpunit.php": true,
}

// overlayAdditions are members the overlay declares on purpose although
// no stub does.
var overlayAdditions = map[string]bool{
	// Forwarded by RecursiveIteratorIterator::__call() to the inner
	// RecursiveDirectoryIterator (spl.php says why).
	"recursiveiteratoriterator::getsubpath": true, "recursiveiteratoriterator::getsubpathname": true,
	"recursiveiteratoriterator::__call": true,
}

// TestOverlayMatchesStubs keeps the overlay in step with the embedded
// php-src stubs: every function and method it refines must exist there,
// with the parameters the stub declares (same names, same order), so a
// stub refresh that adds, renames or removes a parameter cannot be hidden
// by an overlay written against the previous release.
func TestOverlayMatchesStubs(t *testing.T) {
	p := New()
	for _, s := range stdlib.Stubs() {
		if _, err := p.AddFile("<stub>/"+s.Path, patchStub(s.Src), lexer.ModePHP, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, o := range stdlib.Overlay() {
		if overlayOutsidePHPSrc[o.Path] {
			continue
		}
		f, err := parser.Parse(o.Src, lexer.ModePHP)
		if err != nil {
			t.Fatalf("%s: %v", o.Path, err)
		}
		ns := ""
		var walk func(stmts []ast.Stmt)
		walk = func(stmts []ast.Stmt) {
			for _, st := range stmts {
				switch st := st.(type) {
				case *ast.Namespace:
					if st.Name != nil {
						ns = strings.Join(st.Name.Parts, "\\") + "\\"
					} else {
						ns = ""
					}
					walk(st.Body)
				case *ast.FunctionDecl:
					fn := p.Function(ns + st.Name.Name)
					if fn == nil {
						t.Errorf("%s: function %s%s() is not in the stubs", o.Path, ns, st.Name.Name)
						continue
					}
					compareParams(t, o.Path, ns+st.Name.Name+"()", st.Params, fn.Decl.Params)
				case *ast.ClassDecl:
					cl := p.Class(ns + st.Name.Name)
					if cl == nil {
						t.Errorf("%s: class %s%s is not in the stubs", o.Path, ns, st.Name.Name)
						continue
					}
					cl.Signature()
					for _, m := range st.Members {
						md, ok := m.(*ast.MethodDecl)
						if !ok {
							continue
						}
						if overlayAdditions[strings.ToLower(cl.Name+"::"+md.Name.Name)] {
							continue
						}
						sm := cl.FindMethod(md.Name.Name)
						if sm == nil || sm.Decl == nil {
							t.Errorf("%s: method %s::%s() is not in the stubs", o.Path, cl.Name, md.Name.Name)
							continue
						}
						compareParams(t, o.Path, cl.Name+"::"+md.Name.Name+"()", md.Params, sm.Decl.Params)
					}
				}
			}
		}
		walk(f.Stmts)
	}
}

func compareParams(t *testing.T, file, callee string, overlay, stub []*ast.Param) {
	t.Helper()
	names := func(ps []*ast.Param) string {
		var out []string
		for _, p := range ps {
			out = append(out, "$"+p.Var.Name)
		}
		return strings.Join(out, ", ")
	}
	if got, want := names(overlay), names(stub); got != want {
		t.Errorf("%s: %s takes (%s) in the overlay, (%s) in the stubs", file, callee, got, want)
	}
}
