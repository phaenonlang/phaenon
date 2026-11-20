package checker

import (
	"bytes"
	"encoding/json"
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
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// PHPStan parity (opt-in): PHAENON_PHPSTAN=/path/to/phpstan runs PHPStan at
// level max, with treatPhpDocTypesAsCertain off, over every golden file as
// the emitter compiles it, its lines read through the line map, and holds
// the corpus to it in both directions (design/checker.md §8, roadmap D33).
// What is judged is the output, as D33 defines a parity finding by it, and
// not the source read as PHP: the two differ where the emitter rewrites
// plain PHP, as it does the native types of an overriding method.
//
//   - every line PHPStan flags is one the golden file expects a finding
//     on: the checker may be stricter than PHPStan, never more lenient;
//   - every finding the golden file expects on a line PHPStan does not
//     flag is a language rule by its code (kinds.go), or an extra check,
//     marked on its line (`// want P2301 extra`), or one of the findings
//     checkerAlone names with their reason. Any other fails the run: it is
//     to be marked, which means reporting it through reportExtra, fixed as
//     a false finding, or shown to be one of those;
//   - a line whose findings are all marked extra is one PHPStan does not
//     flag: a finding PHPStan reports too is a parity finding, and would
//     be lost with the extra checks off. An extra check is a finding
//     PHPStan does not make, not a line it has nothing to say on: where a
//     rule the lists below allow, because the checker deliberately lacks
//     it, speaks on such a line, the line is named with the identifier
//     (besideExtra), and one that is not named fails the run.
//
// Files are analysed one at a time: PHPStan can crash on deliberately
// invalid code, which must not hide the other files' results.
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
		// unset($a[null]): PHP 8.5 neither deprecates nor refuses null there,
		// as it does in a read or a write (P1101), and the golden holds the
		// line to show it unreported; PHPStan reports the key's type.
		"deprecations-85.phn": {"offsetAccess.invalidOffset": true},
		// `array $x = null`, deprecated since PHP 8.4, is what one case of
		// the file is about: PHP still reads it as `?array $x`. The checker
		// has no finding for the spelling.
		"override-static.phn": {"parameter.implicitlyNullable": true},
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
	// allowAt is allowIn for the lines named and no other: PHPStan sees
	// there, under that identifier, a mistake the checker reports on
	// another line. A line listed that PHPStan does not report so fails
	// the run.
	allowAt := map[string]map[string][]int{
		// The file's findings are overrides whose native types are apart.
		// The emitter rewrites those of the overriding method until they
		// fit (the override-erasure rule), and PHPStan then reports the
		// body the rewritten return type refuses, on the line of its
		// return: the same mistake, two lines below the checker's.
		"override-interfaces.phn": {"return.type": {27, 61, 71, 111, 128, 168, 186, 231}},
	}
	// besideExtra are the lines whose findings are all extra checks and on
	// which PHPStan speaks all the same, under an identifier the lists
	// above allow because the checker deliberately lacks the rule. An
	// extra check is a finding PHPStan does not make of the code, not a
	// line PHPStan has nothing to say on, and the run names those lines
	// instead of passing them: one that is not listed fails it, as does
	// one listed on which PHPStan is silent.
	besideExtra := map[string]map[string][]int{
		// A static method documented to return a string where its parent's
		// is documented an int, both `int|string` to PHP: the extra check
		// is the override, and PHPStan says of the same line that the
		// method never returns an int.
		"override-static.phn": {"return.unusedType": {109}},
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
			expected := parseWants(string(src))
			// marksOnly is a file with syntax only Phaenon has. PHPStan reads
			// a file as PHP, and what the emitter makes of such a file is
			// not comparable finding by finding (PHPStan reports what
			// erasure leaves behind); the emit golden covers those files,
			// compiled output through PHPStan level max. One thing is held
			// here all the same, where the file marks extra checks: compiled,
			// a line whose findings are all marked extra is one PHPStan
			// does not flag.
			marksOnly := usesPhaenonSyntax(src)
			if marksOnly && !slices.ContainsFunc(expected, func(w want) bool { return w.extra }) {
				t.Skip("Phaenon-only syntax; covered by the emit golden")
			}
			if why, ok := skipParity[name]; ok {
				t.Skip(why)
			}
			phpName := strings.TrimSuffix(name, ".phn") + ".php"
			// PHPStan judges the compiled file, its lines read through the
			// line map. A file the emitter cannot compile is read as PHP,
			// the open tag on line 1 so that the lines match, where it is
			// PHP: without the syntax of design/functions.md.
			php := append([]byte("<?php "), src...)
			sourceLine := func(n int) int { return n }
			compiled, isCompiled := compileForParity(t, path, src)
			if isCompiled {
				php = compiled
				m, err := linemap.Find(php)
				if err != nil || m == nil {
					t.Fatalf("compiled without a line map (%v):\n%s", err, php)
				}
				sourceLine = func(n int) int {
					s, _ := m.Forward(n)
					return s
				}
			} else if marksOnly || usesFunctionSyntax(src) {
				t.Fatalf("%s does not compile, and PHPStan cannot read it as PHP", name)
			} else {
				t.Logf("%s does not compile: PHPStan reads it as PHP", name)
			}
			if err := os.WriteFile(filepath.Join(dir, phpName), php, 0o644); err != nil {
				t.Fatal(err)
			}
			wants := map[int]bool{}
			for _, w := range expected {
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
			// runner is the php that loads the compiled file where the
			// run's table says PHP refuses it (phpRefusals).
			runner := os.Getenv("PHAENON_PHP")
			if runner == "" {
				runner = "php"
			}
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
				runner = php
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
			// flagged are the lines PHPStan reports on; allowed, those it
			// reports on only under an identifier allowed for this file,
			// where it sees the same mistake and words or judges it its own
			// way: such a line is not the checker's alone.
			flagged, allowed := map[int]bool{}, map[int]bool{}
			seenAt := map[string]map[int]bool{}
			spoken := map[int][]string{} // the allowed identifiers PHPStan reports, by line
			for file, f := range report.Files {
				// A trait's body is analysed once per user, its findings
				// filed under "X.php (in context of class User)".
				file, _, _ = strings.Cut(file, " (in context of ")
				if filepath.Base(file) != phpName {
					continue
				}
				for _, m := range f.Messages {
					line := sourceLine(m.Line)
					at := slices.Contains(allowAt[name][m.Identifier], line)
					if at {
						if seenAt[m.Identifier] == nil {
							seenAt[m.Identifier] = map[int]bool{}
						}
						seenAt[m.Identifier][line] = true
					}
					if allowIn[name][m.Identifier] || at {
						allowed[line] = true
					}
					if allow[m.Identifier] || allowIn[name][m.Identifier] || at {
						if !slices.Contains(spoken[line], m.Identifier) {
							spoken[line] = append(spoken[line], m.Identifier)
						}
						continue
					}
					flagged[line] = true
					if !wants[line] && !marksOnly {
						t.Errorf("line %d: PHPStan reports %q (%s); the corpus expects nothing there", line, m.Message, m.Identifier)
					}
				}
			}
			for identifier, lines := range allowAt[name] {
				for _, line := range lines {
					if !seenAt[identifier][line] {
						t.Errorf("allowAt names %s on line %d of %s, and PHPStan does not report it there: move the line, or take it out", identifier, line, name)
					}
				}
			}
			// The other direction: what the checker reports alone.
			alone := map[string][]int{} // by code: the lines of the findings to account for
			onlyExtra := map[int]bool{} // the lines whose findings are all marked extra
			otherKind := map[int]bool{} // the lines with a finding that is not
			for _, w := range expected {
				if w.extra {
					onlyExtra[w.line] = true
				} else {
					otherKind[w.line] = true
				}
				if flagged[w.line] || allowed[w.line] || w.extra || IsLanguageRule(w.code) {
					continue
				}
				alone[w.code] = append(alone[w.code], w.line)
			}
			for line := range onlyExtra {
				if otherKind[line] {
					continue
				}
				if flagged[line] {
					t.Errorf("line %d: marked extra, and PHPStan reports the line too: a finding PHPStan has is a parity finding, reported through report", line)
				}
				for _, identifier := range spoken[line] {
					if !slices.Contains(besideExtra[name][identifier], line) {
						t.Errorf("line %d: marked extra, and PHPStan reports %s there, a rule the checker lacks: name the line in besideExtra, "+
							"when the extra check is about something else than that rule, or the mark is wrong", line, identifier)
					}
				}
			}
			for identifier, lines := range besideExtra[name] {
				for _, line := range lines {
					if !onlyExtra[line] || otherKind[line] || !slices.Contains(spoken[line], identifier) {
						t.Errorf("besideExtra names %s on line %d of %s, and the line is not one of extra checks alone on which PHPStan reports it: move the line, or take it out", identifier, line, name)
					}
				}
			}
			if marksOnly {
				return
			}
			// Finding by finding, line by line: a listed finding that is
			// fixed while another of its code appears in the file fails twice.
			named := map[string][]int{}
			var refusedByPHP []int
			for _, a := range checkerAlone[name] {
				named[a.code.ID] = append(named[a.code.ID], a.lines...)
				if a.because == phpRefuses {
					refusedByPHP = append(refusedByPHP, a.lines...)
				}
				if len(a.lines) == 0 || a.because == "" || a.why == "" {
					t.Errorf("checkerAlone: %s of %s names no line or no reason", a.code.ID, name)
				}
				if IsLanguageRule(a.code.ID) {
					t.Errorf("checkerAlone: %s of %s is a language rule by its code, and needs no entry", a.code.ID, name)
				}
			}
			for code, lines := range alone {
				for _, line := range lines {
					if !slices.Contains(named[code], line) {
						t.Errorf("line %d: %s, which PHPStan does not report and checkerAlone does not name: such a finding is an extra check, "+
							"reported through reportExtra and marked `extra` on its line, or a false finding to fix, or it is named in checkerAlone, by its line, with its reason",
							line, code)
					}
				}
			}
			// That PHP refuses the code is not taken on the entry's word:
			// php is given the compiled file.
			if len(refusedByPHP) > 0 {
				if !isCompiled {
					t.Errorf("checkerAlone says PHP refuses lines of %s, which does not compile: nothing can show it", name)
				} else {
					refused := phpRefusals(t, runner, dir, phpName, compiled, sourceLine, thrownWhenRun[name])
					for _, line := range refusedByPHP {
						if refused[line] == "" {
							t.Errorf("checkerAlone says PHP refuses line %d of %s, and php does not: it loads the compiled file without an error on that line, "+
								"and nothing in thrownWhenRun throws there. The reason is another, or the finding is not the checker's alone by right", line, name)
						}
					}
				}
			}
			for code, lines := range named {
				for _, line := range lines {
					if !slices.Contains(alone[code], line) {
						t.Errorf("checkerAlone names %s on line %d of %s, and the file has none there that PHPStan does not flag: "+
							"move the entry with its line, or take it out when the finding is gone or marked", code, line, name)
					}
				}
			}
		})
	}
}

// aloneBecause is why a finding on a line PHPStan does not flag stands
// unmarked: it is no language rule and no extra check the checker can tell.
type aloneBecause string

const (
	// The finding is true and PHPStan does not make it, an extra check by
	// what it says; but the place that reports it has no proof of that
	// (extra.go). It is drawn by the ordinary check of an argument, a
	// return or a receiver from a type the checker inferred earlier and
	// more narrowly than PHPStan, or from a value of which PHPStan may know
	// more than the checker: a local, a parameter a condition has
	// mentioned, a method's parameter that another declaration of its
	// hierarchy may document. It is reported in both modes: a doubt costs a
	// line here, never a finding the switch hides (design/checker.md §10,
	// "Which findings are extra checks").
	notToldApartYet aloneBecause = "an extra check that cannot be told from a parity finding yet"
	// PHP itself refuses the code: a compile error, a fatal error when the
	// class is declared, or an Error at the statement whatever the values.
	// PHPStan has no rule for it or predates the feature. It is a parity
	// finding, reported in both modes: with it silent, the build would
	// write a file php cannot load. The run holds the reason to php
	// (phpRefusals): php refuses to load the compiled file for that line,
	// or throws there when the code thrownWhenRun names is run.
	phpRefuses aloneBecause = "PHP refuses the code"
	// PHPStan reports the mistake elsewhere, or does not know the library
	// the finding is about; the entry says which.
	artifact aloneBecause = "PHPStan reports it elsewhere or cannot know it"
	// The finding is about something the source writes and the output no
	// longer has. The native types of an overriding method that are apart
	// from those it overrides: the emitter rewrites them until they fit
	// (the override-erasure rule), so the output has no such signature
	// for php or PHPStan to refuse; what PHPStan reports of it, if
	// anything, is a body the rewritten signature refuses. The checker
	// holds the body to the types the source writes, so the finding is all
	// that stands before a compiled method whose signature says something
	// else than its source. An attribute the target's PHP does not know in
	// that place, which the emitter leaves out. Each is a parity finding,
	// reported in both modes, and no extra check: nothing at the report
	// proves that the output is what the source means.
	rewrittenAway aloneBecause = "the emitter rewrites what the finding is about"
	// The checker is wrong: nothing can fail. A defect, kept here until it
	// is fixed, and never marked extra.
	falseFinding aloneBecause = "a false finding, to fix"
)

// checkerAlone names the findings of the golden files that stand on lines
// PHPStan does not flag and are neither language rules nor marked extra
// checks: each by its file, its code and its line, with why it stands. The
// run fails for a finding it does not name and for an entry whose line has
// no such finding, so the list is what is left to tell apart or fix, and
// nothing else. A change that moves a listed line moves its entry.
var checkerAlone = map[string][]struct {
	code    Code
	lines   []int
	because aloneBecause
	why     string
}{
	"arithmetic.phn": {
		{P2103, []int{137, 138}, notToldApartYet, "a sum and a power that leave the integers for an operand of int<62, 64>: " +
			"PHPStan does not compute with a range, and the lattice cannot tell a short one from the union of values PHPStan does compute with"},
		{P2301, []int{14, 16}, notToldApartYet, "`**` on a string parameter its function names several times: the proof is the parameter's only occurrence"},
		{P2103, []int{30, 126, 174}, notToldApartYet, "a power of a parameter its function names several times: the proof is the parameter's only occurrence, " +
			"since PHPStan remembers what it was told of the expression as of the variable"},
		{P2202, []int{181}, notToldApartYet, "the same, returned"},
		{P2103, []int{125, 175, 176}, notToldApartYet, "a power whose operands are not an untouched parameter typed by PHP alone beside a number written out or another " +
			"untouched parameter: a local of two values, a range the docblock declares. PHPStan computes with neither, which the lattice cannot tell from the values it does compute with"},
		{P2103, []int{198, 278}, notToldApartYet, "a parameter under `$i > 5`: a condition that names it is a second occurrence, whatever it narrows to; " +
			"PHPStan has a range there and computes with none"},
	},
	"benevolent.phn": {{P2105, []int{15}, notToldApartYet, "DOMDocument::createElement() kept in a variable: PHPStan's union stays benevolent there"}},
	"call-invalidation.phn": {{P2202, []int{103}, notToldApartYet, "a static call of the class forgets what was narrowed of $this->label; " +
		"PHPStan forgets it after an instance call only"}},
	"count-narrowing.phn": {{P2103, []int{215}, notToldApartYet, "the namespace's own count() says nothing of the list; PHPStan narrows by the function's short name"}},
	"numeric-keys.phn": {
		{P2202, []int{49}, notToldApartYet, "(string) of a key that is an int stays an int key; PHPStan reads it as a string key"},
		{P2103, []int{147, 149}, notToldApartYet, "the same"},
	},
	"override-interfaces.phn": {{P2502, []int{30, 114, 252, 272}, rewrittenAway, "a parameter narrower than the one it overrides, compiled to the wider type: " +
		"`at(int $index)` under `at(string $index)` to int|string, a constructor's `string $key` under `int $id` likewise"}},
	"override-static.phn": {{P2502, []int{84, 104, 129, 174, 214}, rewrittenAway, "a static method's `(): iterable` over `(): array`, compiled to array; `array $x` under `iterable $x`, " +
		"under a parameter without a type and under an interface's `string $x`, compiled to iterable, mixed and string|array. Each body fits both signatures"}},
	"override-types.phn": {{P2502, []int{834}, notToldApartYet, "a Traversable class is the iterable its @implements says; " +
		"PHPStan reads getIterator()'s declared type, a bare Iterator"}},
	"unset-offsets.phn": {{P2103, []int{233}, notToldApartYet, "the rows after unset($rows[$k]['name']) may lack the entry; PHPStan keeps it"}},
	"declared-here-alone.phn": {
		{P2202, []int{192}, notToldApartYet, "a method its parent declares too, with no docblock: the rule asks only whether another declaration exists, not what it documents"},
		{P2103, []int{207}, notToldApartYet, "a method the parent names in @method: the same"},
	},
	"shapes-extra.phn": {
		{P2202, []int{122}, notToldApartYet, "the longer tuple returned by an arrow function that is itself the value returned: a closure is no value both tools are known to read alike"},
		{P2103, []int{161}, notToldApartYet, "the longer tuple assigned to an offset: PHPStan passes it under offsetAssign.valueType, which refuses the general array an argument takes, " +
			"so the place reports no extra check at all"},
	},
	"shapes-held.phn": {{P2103, []int{137, 499, 505}, notToldApartYet, "a general array and longer tuples given as variables their functions name several times: " +
		"the proof is the parameter's only occurrence"}},
	"untouched.phn": {
		{P2402, []int{197}, notToldApartYet, "is_float() on a power to -1, a float always; PHPStan has a benevolent (float|int) there and says nothing"},
		{P2301, []int{271, 272}, notToldApartYet, "`**` on a string parameter named twice, where PHPStan learns nothing between the two: the price of proving by the only occurrence"},
	},
	"string-steps.phn": {
		{P2301, []int{82}, notToldApartYet, "the step of a key of array<array-key, int>, an int|string here and a benevolent (int|string) to PHPStan: a foreach's key is no value both read alike"},
		{P2305, []int{97}, notToldApartYet, "the step of a numeric-string is a number, and no '6'; PHPStan has a benevolent (float|int) and passes the comparison"},
		{P2202, []int{100}, notToldApartYet, "the same, returned where string|int is declared"},
	},
	"arrays-functions.phn": {{P2103, []int{124}, notToldApartYet, "array_chunk() with a $preserve_keys that may be true is a list of arrays, not of lists: " +
		"php gives [['a', 'b'], [2 => 'c']] for true, where PHPStan has a list<non-empty-list<string>> whatever the bool. The argument is a call's result, no value the place proves"}},
	"match-caught.phn": {{P2304, []int{120, 155, 168, 220}, notToldApartYet, "a match PHPStan takes as caught where nothing catches its error: PHPStan gives a try's catch names " +
		"to its catch and finally blocks too, reads `namespace\\Error` as the global Error, and takes a @throws tag for a catch. The subject of a match is no place an untouched parameter proves"}},

	"constructor-visibility.phn": {{P2502, []int{264}, phpRefuses, "a constructor narrower than the abstract one a trait of the parent declares: " +
		"\"Access level to …::__construct() must be public\" when the class is declared"}},
	"expression-bodies.phn": {
		{P2514, []int{14}, phpRefuses, "an interface method with a body is a compile error"},
		{P2504, []int{19}, phpRefuses, "an abstract method with a body is a compile error"},
	},
	"named-arguments.phn": {{P2010, []int{57}, phpRefuses, "the variadic parameter's own name given twice: " +
		"\"Named parameter $rest overwrites previous argument\" at the call; PHPStan reports it for a declared parameter and for a collected name"}},
	"self-referencing.phn": {{P2016, []int{22, 38, 43, 48, 53, 58, 60, 65, 75, 76, 81, 82, 83, 91, 92}, phpRefuses, "a constant's or a case's value that reads itself: " +
		"\"Cannot declare self-referencing constant …\" at the fetch when the value is first needed; PHPStan has no rule for it, and of a cycle reports at most a value it types mixed"}},
	"override-constants.phn": {{P2503, []int{35, 52}, rewrittenAway, "#[\\Override] on a constant or an enum case no parent declares: compiled for 8.5, which knows the attribute " +
		"on methods and properties, the emitter leaves it out there, and php 8.5 and 8.6 load the file. Compiled for 8.6 it stays, and is PHP 8.6's compile error"}},

	"library-php86-types.phn": {{P2103, []int{42, 43}, artifact, "PHPStan's maps predate PHP 8.6 (allowIn says so for this file): " +
		"it has Io\\Poll's parameters as bare arrays, where the stubs say non-empty-array<Io\\Poll\\Event>"}},
	"readonly-defaults.phn": {{P2206, []int{21}, artifact, "PHPStan predates readonly property defaults (allowIn) and reports the default itself, not the assignment PHP 8.6 refuses"}},

	"kept-conditions.phn": {
		{P2103, []int{333, 349}, notToldApartYet, "a condition kept in a variable, ended by a write PHPStan does not follow: array_walk_recursive() with a callback " +
			"that takes the value by reference, array_multisort() on an int key it renumbers. php gives takesStd() an int and a null, PHPStan keeps the stdClass; " +
			"the argument is an offset, no value the place proves"},
		{P2103, []int{381, 386, 391, 394, 402}, falseFinding, "$before is a stdClass wherever the kept condition holds, as PHPStan narrows it and the checker does not yet: " +
			"through a match assigned to the variable, in a closure that captures both, after `$loose > 0`, through the assignment inside the test, " +
			"after an if whose branches assign the variable each"},
	},
}

// thrownWhenRun is, for a line checkerAlone says PHP refuses and php
// loads all the same, the code that makes php throw there: an Error at the
// statement whatever the values shows only when the statement runs.
var thrownWhenRun = map[string]map[int]string{
	"named-arguments.phn": {57: `\NamedArguments\variadicsOwnName();`},
	// php names the declaration of a cycle whose value it evaluates first.
	// Code that fetches Crossed::K or Crossed::X names Crossed::K's line,
	// cases() and a constant expression that fetches Crossed::K name
	// Crossed::X's.
	"self-referencing.phn": {
		22: `\App\SelfReferencing\ItsName::N;`,
		38: `\App\SelfReferencing\First::X;`,
		43: `\App\SelfReferencing\Second::Y;`,
		48: `\App\SelfReferencing\Left::X;`,
		53: `\App\SelfReferencing\Right::Y;`,
		58: `\App\SelfReferencing\Crossed::K;`,
		60: `\App\SelfReferencing\Crossed::cases();`,
		65: `\App\SelfReferencing\Base::X;`,
		75: `\App\SelfReferencing\Sides::UP;`,
		76: `\App\SelfReferencing\Sides::DOWN;`,
		81: `\App\SelfReferencing\Chain::ONE;`,
		82: `\App\SelfReferencing\Chain::TWO;`,
		83: `\App\SelfReferencing\Chain::THREE;`,
		91: `\App\SelfReferencing\Into::LOOP;`,
		92: `\App\SelfReferencing\Into::BACK;`,
	},
}

// phpRefusals gives php a compiled golden and returns the source lines php
// refuses it for, each with php's message. php stops at its first error,
// so the file is loaded again and again, each time without the top-level
// declaration the last error was in, until it loads. Only an error php
// places in the golden's own compiled file counts, and one that only says
// a declaration taken out is missing does not. A line of thrown is one php
// is to throw at, in that file, when the code beside it runs after the
// file has been loaded as it is.
func phpRefusals(t *testing.T, php, dir, phpName string, compiled []byte, sourceLine func(int) int, thrown map[int]string) map[int]string {
	t.Helper()
	refused := map[int]string{}
	fatal := regexp.MustCompile(`(?m)^(?:PHP )?(?:Fatal|Parse) error:\s+(.*?) in (\S+?)(?: on line |:)(\d+)`)
	// run loads a file and runs code after it; it returns php's first
	// error where that error is in the file itself, and "" for an error
	// php places anywhere else (the vendor fixture, the code run).
	run := func(file, code string) (message string, line int, failed bool) {
		script := "require " + strconv.Quote("Repo.php") + "; require " + strconv.Quote(file) + "; " + code
		cmd := exec.Command(php, "-d", "display_errors=1", "-d", "log_errors=0", "-d", "error_reporting=-1", "-r", script)
		cmd.Dir = dir
		out, _ := cmd.CombinedOutput()
		m := fatal.FindSubmatch(out)
		if m == nil {
			return "", 0, false
		}
		if filepath.Base(string(m[2])) != file {
			return "", 0, true
		}
		n, _ := strconv.Atoi(string(m[3]))
		return string(m[1]), n, true
	}
	missing := regexp.MustCompile(`^(?:Uncaught Error: )?(?:Class|Interface|Trait|Enum) "([^"]+)" not found|^(?:Uncaught Error: )?Call to undefined function ([^(]+)\(`)
	variant := slices.Clone(compiled)
	gone := map[string]bool{} // the declarations taken out, by short name
	for range 64 {
		if err := os.WriteFile(filepath.Join(dir, "load.php"), variant, 0o644); err != nil {
			t.Fatal(err)
		}
		message, line, _ := run("load.php", "")
		if line == 0 {
			break
		}
		// An error that only misses a declaration taken out is no refusal
		// of the line it stands on.
		artifact := false
		if m := missing.FindStringSubmatch(message); m != nil {
			name := m[1] + m[2]
			if i := strings.LastIndexByte(name, '\\'); i >= 0 {
				name = name[i+1:]
			}
			artifact = gone[strings.ToLower(name)]
		}
		if !artifact {
			refused[sourceLine(line)] = message
		}
		next, name, ok := withoutDeclarationAt(variant, line)
		if !ok {
			break
		}
		if name != "" {
			gone[strings.ToLower(name)] = true
		}
		variant = next
	}
	for line, code := range thrown {
		if message, at, _ := run(phpName, code); at != 0 && sourceLine(at) == line {
			refused[line] = message
		}
	}
	return refused
}

// withoutDeclarationAt blanks, in a PHP file, the top-level statement that
// holds a line, keeping every line where it was, and says what that
// statement declared: a class or a function by its name, "" otherwise.
func withoutDeclarationAt(php []byte, line int) ([]byte, string, bool) {
	f, err := parser.Parse(php, lexer.ModePHP)
	if err != nil {
		return nil, "", false
	}
	start := 0
	for n := 1; n < line; n++ {
		i := bytes.IndexByte(php[start:], '\n')
		if i < 0 {
			return nil, "", false
		}
		start += i + 1
	}
	end := len(php)
	if i := bytes.IndexByte(php[start:], '\n'); i >= 0 {
		end = start + i
	}
	var blank func(stmts []ast.Stmt) ([]byte, string, bool)
	blank = func(stmts []ast.Stmt) ([]byte, string, bool) {
		for _, s := range stmts {
			span := s.Span()
			if span.Start >= end || span.End <= start {
				continue // the statement does not reach the line
			}
			name := ""
			switch s := s.(type) {
			case *ast.Namespace:
				return blank(s.Body)
			case *ast.ClassDecl:
				if s.Name != nil {
					name = s.Name.Name
				}
			case *ast.FunctionDecl:
				name = s.Name.Name
			}
			out := slices.Clone(php)
			for i := span.Start; i < span.End; i++ {
				if out[i] != '\n' {
					out[i] = ' '
				}
			}
			return out, name, true
		}
		return nil, "", false
	}
	return blank(f.Stmts)
}

// skipParity are the golden files PHPStan cannot judge, with the reason.
var skipParity = map[string]string{
	"concurrency-withdrawn.phn": "syntax that left the language (P1001), which neither PHP nor the emitter reads",
	"ignore-comments.phn": "its findings are silenced by @phaenon-ignore, which PHPStan does not read, and its " +
		"@phpstan-ignore lines hold what the checker does with them, not what PHPStan does",
	"ignore-comments-left-out.phn": "its findings are silenced by @phaenon-ignore, which PHPStan does not read",
	"ignore-extra-checks.phn":      "its findings are silenced by @phaenon-ignore, which PHPStan does not read",
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

// compileForParity compiles a golden file as phn build would, and says
// whether it could.
func compileForParity(t *testing.T, path string, src []byte) ([]byte, bool) {
	t.Helper()
	p := newProgram(t)
	target, opts := goldenSetup(t, src)
	p.SetTarget(target)
	f, err := p.AddFile(path, src, lexer.ModePhaenon, true)
	if err != nil {
		return nil, false
	}
	New(p, opts).Check()
	out, err := emit.File(src, emit.Options{SourceName: filepath.Base(path), Program: p, File: f})
	if err != nil {
		return nil, false
	}
	return out, true
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

// TestGeneratedHeader: the opening the checker takes for a compiled output
// left behind (declaredAt) is the one the emitter writes.
func TestGeneratedHeader(t *testing.T) {
	if want := "<?php\n\n/* " + emit.Marker; generatedHeader != want {
		t.Errorf("generatedHeader is %q, the emitter writes %q", generatedHeader, want)
	}
}
