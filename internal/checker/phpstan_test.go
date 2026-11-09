package checker

import (
	"encoding/json"
	"fmt"
	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/linemap"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// PHPStan parity (opt-in): PHAENON_PHPSTAN=/path/to/phpstan runs PHPStan at
// level max, with treatPhpDocTypesAsCertain off, over every golden file
// (written as .php with the open tag on line 1, so line numbers match, or
// compiled, its lines translated through the line map) and demands that
// every line PHPStan flags is one the golden file expects a diagnostic on. The checker may be
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
		"missingType.generics":      true, // M3
		"function.resultUnused":     true, // purity inference: not a type error
		"method.resultUnused":       true,
		"expr.resultUnused":         true,
		"void.pure":                 true,
		"phpstan.dumpType":          true,
		"return.unusedType":         true, // unused-type analysis is tooling (M6)
		"parameterByRef.unusedType": true,
	}
	// PHPStan findings ignored in one file only.
	allowIn := map[string]map[string]bool{
		// instanceof, catch and ::class of a class PHP 8.6 added fail nothing
		// on PHP 8.5 and guard code meant for 8.6 (php-versions.md §9);
		// PHPStan on 8.5 cannot tell them from a misspelt name.
		"library-target85.phn": {"class.notFound": true},
		// PHPStan reads PHP's own functions and classes from its maps, which
		// predate PHP 8.6 (2.2 and 2.3 alike): run on 8.6 it knows the new
		// functions and classes through reflection, not the methods,
		// constants and parameters 8.6 added to existing ones, and types the
		// unknown constant as mixed.
		"library-target86.phn": {"method.notFound": true, "classConstant.notFound": true, "argument.unknown": true,
			"arguments.count": true, "echo.nonString": true},
		// The same maps leave PHPStan with reflection alone for 8.6's new
		// functions and classes: clamp() returns mixed and
		// Io\Poll\Context::wait() an array of mixed, where the overlay and
		// the stub's docblock say T and list<Watcher>.
		"library-php86-types.phn": {"argument.type": true, "method.nonObject": true},
		// PHPStan 2.2 predates readonly property defaults (PHP 8.6) and
		// refuses them on every PHP version.
		"readonly-defaults.phn": {"property.readOnlyDefaultValue": true},
		// PHPStan 2.2 knows #[\Override] as PHP 8.5 declares it, for methods
		// and properties; PHP 8.6 allows it on constants, and compiled for
		// 8.5 the emitter drops it there.
		"override-constants.phn": {"attribute.target": true},
		// PHPStan 2.2 predates __debugInfo() on enums (PHP 8.6) and refuses
		// it on every version.
		"enum-magic.phn": {"enum.magicMethod": true},
		// A trait's body serves every user: a comparison that one user's
		// declaration alone decides is not impossible in the trait
		// (design/checker.md §9, "A trait sees its users"), where PHPStan,
		// analysing once per user, reports it in that user's context.
		"trait-properties.phn": {"identical.alwaysFalse": true},
		// Properties a mapping attribute marks: PHPStan needs the framework's
		// extension to know they are read and written (design/tooling.md §9.2).
		"unused-attributes.phn":        {"property.onlyWritten": true, "property.unused": true, "property.onlyRead": true},
		"unused-attributes-listed.phn": {"property.onlyWritten": true, "property.unused": true, "property.onlyRead": true},
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
			if why, ok := skipParity[name]; ok {
				t.Skip(why)
			}
			phpName := strings.TrimSuffix(name, ".phn") + ".php"
			php := append([]byte("<?php "), src...)
			sourceLine := func(n int) int { return n }
			if usesFunctionSyntax(src) {
				// Methods without a keyword, expression bodies and `throws`
				// need no type information (design/functions.md): PHPStan judges the
				// compiled file, its lines read through the line map.
				php = compileForParity(t, path, src)
				m, err := linemap.Find(php)
				if err != nil || m == nil {
					t.Fatalf("compiled without a line map (%v):\n%s", err, php)
				}
				sourceLine = func(n int) int {
					s, _ := m.Forward(n)
					return s
				}
			}
			if err := os.WriteFile(filepath.Join(dir, phpName), php, 0o644); err != nil {
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
			// A file compiled for a newer PHP than the floor is analysed by
			// PHPStan running on that PHP, which reads the new library from
			// its own reflection. (phpVersion stays unset: the parity
			// binary, PHPStan 2.2, accepts no version above 8.5.)
			argv := []string{phpstan}
			if target, _ := goldenSetup(t, src); target != phpversion.Floor {
				// PHAENON_PHP86 names a php 8.6; PHAENON_PHP, the
				// conformance oracle, serves when it is one.
				variable := "PHAENON_PHP" + strings.ReplaceAll(target.String(), ".", "")
				php := os.Getenv(variable)
				if php == "" {
					if oracle := os.Getenv("PHAENON_PHP"); oracle != "" {
						out, err := exec.Command(oracle, "-r", `echo PHP_MAJOR_VERSION, ".", PHP_MINOR_VERSION;`).Output()
						if err == nil && string(out) == target.String() {
							php = oracle
						}
					}
				}
				if php == "" {
					t.Skipf("target %s: neither %s nor PHAENON_PHP names a php %s", target, variable, target)
				}
				argv = []string{php, phpstan}
			}
			if err := os.WriteFile(filepath.Join(dir, "phpstan.neon"), []byte(neon), 0o644); err != nil {
				t.Fatal(err)
			}
			argv = append(argv, "analyse", "-c", filepath.Join(dir, "phpstan.neon"), "--level", "max", "--no-progress", "--error-format", "json", "--no-ansi", dir)
			cmd := exec.Command(argv[0], argv[1:]...)
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
				// A trait's body is analysed once per user, its findings
				// filed under "X.php (in context of class User)".
				file, _, _ = strings.Cut(file, " (in context of ")
				if filepath.Base(file) != phpName {
					continue
				}
				for _, m := range f.Messages {
					if allow[m.Identifier] || allowIn[name][m.Identifier] {
						continue
					}
					line := sourceLine(m.Line)
					flagged[line] = true
					if !wants[line] {
						t.Errorf("line %d: PHPStan reports %q (%s); the corpus expects nothing there", line, m.Message, m.Identifier)
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

// skipParity are the golden files PHPStan cannot judge, with the reason.
var skipParity = map[string]string{
	"concurrency-withdrawn.phn": "syntax that left the language (P1001), which neither PHP nor the emitter reads",
	"library-guards.phn": "PHPStan runs on one PHP: on 8.5 the 8.6 side of each version guard does not exist " +
		"(unknown symbols, PHP_VERSION_ID < 80600 always true), on 8.6 the 8.5 side is dead code",
}

// usesFunctionSyntax reports whether a corpus file declares with the
// syntax of design/functions.md — a method without a keyword, an
// expression body, `throws` — which the emitter lowers without type
// information.
func usesFunctionSyntax(src []byte) bool {
	f, err := parser.Parse(src, lexer.ModePhaenon)
	if err != nil {
		return false
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FunctionDecl:
			found = found || n.Fun || n.Arrow != nil || n.Throws != nil
		case *ast.MethodDecl:
			found = found || n.Bare() || n.Fun || n.Arrow != nil || n.Throws != nil
		}
		return !found
	})
	return found
}

// compileForParity compiles a golden file whose only Phaenon syntax is that
// of design/functions.md, without declare(strict_types=1) as the PHP
// copies of the other files are analysed.
func compileForParity(t *testing.T, path string, src []byte) []byte {
	t.Helper()
	p := newProgram(t)
	target, opts := goldenSetup(t, src)
	p.SetTarget(target)
	f, err := p.AddFile(path, src, lexer.ModePhaenon, true)
	if err != nil {
		t.Fatal(err)
	}
	New(p, opts).Check()
	out, err := emit.File(src, emit.Options{SourceName: filepath.Base(path), Program: p, File: f})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// usesPhaenonSyntax reports whether a corpus file has constructs PHPStan
// cannot parse and the parity run does not compile (the emit goldens do): type
// parameters and arguments, the withdrawn is / as, type aliases, Closure signatures,
// hyphenated type names, PHP 8.6 placeholders.
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
			case *ast.Placeholder:
				found = true // PHP 8.6 syntax, which the parity PHPStan (2.2) does not parse
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
