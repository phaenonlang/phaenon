package checker

import (
	"encoding/json"
	"fmt"
	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// PHPStan parity (opt-in): PHAENON_PHPSTAN=/path/to/phpstan runs PHPStan at
// level max, with treatPhpDocTypesAsCertain off, over every golden file
// (written as .php with the open tag on line 1, so line numbers match) and
// demands that every line PHPStan flags is one the golden file expects a
// diagnostic on. The checker may be
// stricter than PHPStan; it may not be more lenient. Lines PHPStan does
// not flag but the corpus expects are listed for information. Files are
// analysed one at a time: PHPStan can crash on deliberately invalid code,
// which must not hide the other files' results.
func TestPHPStanParity(t *testing.T) {
	phpstan := os.Getenv("PHAENON_PHPSTAN")
	if phpstan == "" {
		t.Skip("PHAENON_PHPSTAN not set")
	}
	// PHPStan rules Phaenon deliberately does not have (yet).
	allow := map[string]bool{
		"missingType.iterableValue": true, // M4 decides how arrays are typed
		"missingType.generics":      true, // M3
		"function.resultUnused":     true, // purity inference: not a type error
		"method.resultUnused":       true,
		"expr.resultUnused":         true,
		"void.pure":                 true,
		"method.unused":             true, // dead-code analysis is tooling (M6)
		"property.unused":           true,
		"constant.unused":           true,
		"classConstant.unused":      true,
		"phpstan.dumpType":          true,
		"catch.neverThrown":         true, // throw analysis is not in M2
		"return.unusedType":         true, // unused-type analysis is tooling (M6)
		"parameterByRef.unusedType": true,
	}
	files, _ := filepath.Glob(filepath.Join("testdata", "*.phn"))
	vendorSrc, err := os.ReadFile(filepath.Join("testdata", "vendor", "Acme", "Repo.php"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "Repo.php"), vendorSrc, 0o644); err != nil {
				t.Fatal(err)
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if usesPhaenonSyntax(src) {
				// PHPStan reads the file as PHP; Phaenon-only syntax has no
				// PHP form before the emitter runs. The emit golden covers
				// those files (compiled output through PHPStan level max).
				t.Skip("Phaenon-only syntax; covered by the emit golden")
			}
			phpName := strings.TrimSuffix(name, ".phn") + ".php"
			if err := os.WriteFile(filepath.Join(dir, phpName), append([]byte("<?php "), src...), 0o644); err != nil {
				t.Fatal(err)
			}
			wants := map[int]bool{}
			for _, w := range parseWants(string(src)) {
				wants[w.line] = true
			}
			// Tequila's profile (design/checker.md §9): docblock types are not
			// certain, so a check against what PHP may still hand over is not
			// reported as impossible by either side.
			neon := "parameters:\n    treatPhpDocTypesAsCertain: false\n"
			if err := os.WriteFile(filepath.Join(dir, "phpstan.neon"), []byte(neon), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(phpstan, "analyse", "-c", filepath.Join(dir, "phpstan.neon"), "--level", "max", "--no-progress", "--error-format", "json", "--no-ansi", dir)
			cmd.Dir = dir
			out, _ := cmd.Output() // exit status 1 means findings, which is expected
			var report struct {
				Files map[string]struct {
					Messages []struct {
						Message    string `json:"message"`
						Line       int    `json:"line"`
						Identifier string `json:"identifier"`
					} `json:"messages"`
				} `json:"files"`
				Errors []string `json:"errors"`
			}
			if err := json.Unmarshal(out, &report); err != nil {
				t.Fatalf("phpstan output: %v\n%s", err, out)
			}
			for _, e := range report.Errors {
				t.Logf("phpstan could not analyse the file: %s", strings.SplitN(e, "\n", 2)[0])
				return
			}
			flagged := map[int]bool{}
			for file, f := range report.Files {
				if filepath.Base(file) != phpName {
					continue
				}
				for _, m := range f.Messages {
					if allow[m.Identifier] {
						continue
					}
					flagged[m.Line] = true
					if !wants[m.Line] {
						t.Errorf("line %d: PHPStan reports %q (%s); the corpus expects nothing there", m.Line, m.Message, m.Identifier)
					}
				}
			}
			var onlyUs []string
			for line := range wants {
				if !flagged[line] {
					onlyUs = append(onlyUs, fmt.Sprint(line))
				}
			}
			if len(onlyUs) > 0 {
				t.Logf("lines Phaenon flags and PHPStan does not: %s", strings.Join(onlyUs, ", "))
			}
		})
	}
}

// usesPhaenonSyntax reports whether a corpus file has constructs PHP
// cannot parse: type parameters and arguments, is / as, type aliases,
// Closure signatures, hyphenated type names.
func usesPhaenonSyntax(src []byte) bool {
	f, err := parser.Parse(src, lexer.ModePhaenon)
	if err != nil {
		return false
	}
	found := false
	for _, s := range f.Stmts {
		ast.Inspect(s, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.TypeParam, *ast.IsExpr, *ast.AsExpr, *ast.TypeAlias, *ast.ClosureType, *ast.TupleType:
				found = true
			case *ast.Cast:
				found = n.Op == token.T_BYTES_CAST
			case *ast.NamedType:
				if len(n.TypeArgs) > 0 || strings.Contains(n.Name.Parts[0], "-") || strings.EqualFold(n.Name.Parts[0], "bytes") {
					found = true
				}
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}
