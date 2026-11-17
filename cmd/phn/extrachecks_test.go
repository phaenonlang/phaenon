package main

// The extra checks and their switch (design/roadmap.md D33): on unless
// phaenon.json says "extra-checks": false or the run is given
// --no-extra-checks, which phn check, phn build and phn watch take.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
)

// kindsSource has one finding of each kind: `**` on a string is an extra
// check, where the string is a parameter the function names once, `*` on
// one a parity finding (PHPStan reports it), and `global` a language rule.
const kindsSource = `namespace App;

function kinds(string $s, string $t): float|int
{
    global $g;

    return $s ** 2 + $t * 2;
}
`

// onlyExtraSource is a file whose one finding is an extra check.
const onlyExtraSource = `namespace App;

function power(string $s): float|int
{
    return $s ** 2;
}
`

// extraTree is a project of two files: Kinds.phn with a finding of each
// kind, and Power.phn, whose only finding is an extra check.
func extraTree(t *testing.T, project string) string {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"composer.json":                     `{"name": "acme/app", "require": {"php": ">=8.5"}}`,
		"phaenon.json":                      project,
		"vendor/composer/installed.json":    `{"packages": []}`,
		"vendor/composer/autoload_psr4.php": "<?php\n\nreturn array();\n",
		"src/Kinds.phn":                     kindsSource,
		"src/Power.phn":                     onlyExtraSource,
	})
	return root
}

// finding is a line of phn check --format=json.
type finding struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Code string `json:"code"`
	Kind string `json:"kind"`
}

func kindsOf(t *testing.T, stdout string) (out []string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var f finding
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("not a JSON object: %q: %v", line, err)
		}
		out = append(out, strings.TrimPrefix(f.File, "src/")+" "+f.Code+" "+f.Kind)
	}
	return out
}

// The project file's key reads as a switch, and the checker's options take
// it and the command line's flag over it.
func TestReadProjectExtraChecks(t *testing.T) {
	for _, c := range []struct {
		json string
		flag bool
		off  bool
		err  string
	}{
		{json: `{"roots": {"src": ".src"}}`},
		{json: `{"extra-checks": true}`},
		{json: `{"extra-checks": null}`},
		{json: `{"extra-checks": false}`, off: true},
		{json: `{"roots": {"src": ".src"}}`, flag: true, off: true},
		{json: `{"extra-checks": true}`, flag: true, off: true},
		{json: `{"extra-checks": false}`, flag: true, off: true},
		{json: `{"extra-checks": "off"}`, err: `"extra-checks" is true or false: false leaves out the checker's extra checks, and no key keeps them`},
		{json: `{"extra-checks": 0}`, err: `"extra-checks" is true or false`},
		{json: `{"extra-checks": ["P2301"]}`, err: `"extra-checks" is true or false`},
		{json: `{"Extra-Checks": false}`, err: `the key "Extra-Checks" is written "extra-checks"`},
		{json: `{"extraChecks": false}`, err: `unknown key "extraChecks"`},
		{json: `{"strict": false}`, err: `unknown key "strict"`},
	} {
		path := filepath.Join(t.TempDir(), ProjectFile)
		writeTree(t, filepath.Dir(path), map[string]string{ProjectFile: c.json})
		p, err := readProject(path)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: error %v, want one saying %q", c.json, err, c.err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.json, err)
		}
		noExtraChecks = c.flag
		got := p.checkerOptions(checker.Options{Deprecations: true})
		noExtraChecks = false
		if got.NoExtraChecks != c.off || !got.Deprecations {
			t.Errorf("%s, --no-extra-checks %v: the options are %+v, want the extra checks off: %v", c.json, c.flag, got, c.off)
		}
	}
	if got := (*project)(nil).checkerOptions(checker.Options{}); got.NoExtraChecks {
		t.Error("no project file: the extra checks are off, and they are on unless something says otherwise")
	}
}

// "extra-checks" is a key of 0.8.0: a library that commits a root and
// writes it keeps out the releases before, which refuse a key they do not
// know in a project's own file and pass over it in an installed package's.
func TestConflictFloorOfExtraChecks(t *testing.T) {
	const file = `{"roots": {"src": ".src"}, "committed": ["src"], "extra-checks": false}`
	for manifest, want := range map[string]string{
		`{"name": "acme/kit", "conflict": {"phaenon/phn": "<0.8.0"}}`: "",
		`{"name": "acme/kit", "conflict": {"phaenon/phn": "<0.5.0"}}`: `composer.json: "conflict": {"phaenon/phn": "<0.5.0"} does not keep this package from being installed beside phaenon/phn 0.7.2 or one of 4 earlier releases, ` +
			`which cannot read its phaenon.json: the key "extra-checks" is known from 0.8.0 on; write "phaenon/phn": "<0.8.0" under "conflict"`,
	} {
		t.Chdir(t.TempDir())
		writeTree(t, ".", map[string]string{ProjectFile: file, "composer.json": manifest})
		p, err := readProject(ProjectFile)
		if err != nil {
			t.Fatal(err)
		}
		if key, since := p.floor(); key != "extra-checks" || since.String() != "0.8.0" {
			t.Errorf("the floor is %s, for %q; want 0.8.0, for \"extra-checks\"", since, key)
		}
		if got := p.conflictFloor(); got != want {
			t.Errorf("%s:\n got %s\nwant %s", manifest, got, want)
		}
	}
}

// phn check reports the extra checks unless the project file or the flag
// turns them off, and then they are gone whole: no line, no count, no part
// in the exit status. --format=json names each finding's kind; the text is
// what it was.
func TestCheckExtraChecks(t *testing.T) {
	bin := phnBinary(t)
	all := []string{"Kinds.phn P1001 language", "Kinds.phn P2301 extra", "Kinds.phn P2301 parity", "Power.phn P2301 extra"}
	rest := []string{"Kinds.phn P1001 language", "Kinds.phn P2301 parity"}
	for _, c := range []struct {
		project string
		args    []string
		want    []string
		summary string
	}{
		{`{"roots": {"src": ".src"}}`, nil, all, "2 files checked: 4 errors, 0 warnings"},
		{`{"roots": {"src": ".src"}, "extra-checks": true}`, nil, all, "2 files checked: 4 errors, 0 warnings"},
		{`{"roots": {"src": ".src"}, "extra-checks": false}`, nil, rest, "2 files checked: 2 errors, 0 warnings"},
		{`{"roots": {"src": ".src"}}`, []string{"--no-extra-checks"}, rest, "2 files checked: 2 errors, 0 warnings"},
		// The flag wins over the file for the run.
		{`{"roots": {"src": ".src"}, "extra-checks": true}`, []string{"--no-extra-checks"}, rest, "2 files checked: 2 errors, 0 warnings"},
		{`{"roots": {"src": ".src"}, "extra-checks": false}`, []string{"--no-extra-checks"}, rest, "2 files checked: 2 errors, 0 warnings"},
	} {
		root := extraTree(t, c.project)
		stdout, stderr, err := run(t, root, bin, append([]string{"check", "--format=json"}, c.args...)...)
		if got := kindsOf(t, stdout); !slices.Equal(got, c.want) || exitStatus(err) != 1 {
			t.Errorf("%s, phn check %v (%v): the findings are %v, want %v\n%s", c.project, c.args, err, got, c.want, stderr)
		}
		stdout, stderr, _ = run(t, root, bin, append([]string{"check"}, c.args...)...)
		if !strings.Contains(stdout, c.summary) || strings.Contains(stdout+stderr, "extra") || strings.Contains(stdout+stderr, "parity") {
			t.Errorf("%s, phn check %v: want %q and no word of the kinds in the text\n%s%s", c.project, c.args, c.summary, stdout, stderr)
		}
		if reported := strings.Contains(stderr, "src/Power.phn:5:12: error P2301: operator ** on string"); reported != (len(c.want) == len(all)) {
			t.Errorf("%s, phn check %v: the extra check of Power.phn reported: %v\n%s", c.project, c.args, reported, stderr)
		}
	}
}

// A file whose only finding is an extra check fails the check and is
// withheld by the build, as any file with an error is; with the extra
// checks off the check passes and the file is built.
func TestExtraCheckAloneDecidesTheStatus(t *testing.T) {
	bin := phnBinary(t)
	for _, cmd := range []string{"check", "build"} {
		root := extraTree(t, `{"roots": {"src": ".src"}}`)
		if err := os.Remove(filepath.Join(root, "src/Kinds.phn")); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(root, ".src/Power.php")
		stdout, stderr, err := run(t, root, bin, cmd)
		if _, statErr := os.Stat(output); exitStatus(err) != 1 || !strings.Contains(stderr, "error P2301") || statErr == nil {
			t.Errorf("phn %s (%v): want the extra check, status 1 and no output (%v)\n%s%s", cmd, err, statErr, stdout, stderr)
		}
		stdout, stderr, err = run(t, root, bin, cmd, "--no-extra-checks")
		_, statErr := os.Stat(output)
		if err != nil || strings.Contains(stdout+stderr, "P2301") || (cmd == "build") != (statErr == nil) {
			t.Errorf("phn %s --no-extra-checks (%v): want no finding, status 0, and the output from the build alone (%v)\n%s%s", cmd, err, statErr, stdout, stderr)
		}
		if cmd == "check" && !strings.Contains(stdout, "1 files checked: 0 errors, 0 warnings") {
			t.Errorf("phn check --no-extra-checks counts: %s", stdout)
		}
	}
}

// phn watch takes the flag for its runs, and reads the project file's key
// again when the file changes.
func TestWatchExtraChecks(t *testing.T) {
	bin := phnBinary(t)
	root := extraTree(t, `{"roots": {"src": ".src"}}`)
	wait := startWatch(t, bin, root, "--no-extra-checks")
	wait("2 files checked: 2 errors")

	root = extraTree(t, `{"roots": {"src": ".src"}}`)
	wait = startWatch(t, bin, root)
	wait("2 files checked: 4 errors")
	wait("waiting for changes")
	writeTree(t, root, map[string]string{"phaenon.json": `{"roots": {"src": ".src"}, "extra-checks": false}`})
	wait("2 files checked: 2 errors")
}

// The commands that report no run's findings refuse the flag and say where
// the setting goes; phn build --vendor refuses it too, since each installed
// package is checked as its own project file says.
func TestNoExtraChecksWhereItDoesNotBelong(t *testing.T) {
	bin := phnBinary(t)
	root := extraTree(t, `{"roots": {"src": ".src"}}`)
	for _, args := range [][]string{
		{"fmt", "--no-extra-checks"},
		{"fmt", "--check", "-no-extra-checks", "src"},
		{"lsp", "--no-extra-checks"},
		{"fix", "--no-extra-checks", "--", "true"},
		{"migrate", "--no-extra-checks=true", "src"},
		{"trace", "--no-extra-checks"},
		{"stdlib", "--out", "x", "--no-extra-checks"},
		{"print", "--no-extra-checks", "src/Power.phn"},
		{"lex", "--no-extra-checks", "src/Power.phn"},
	} {
		stdout, stderr, err := run(t, root, bin, args...)
		want := "phn " + args[0] + `: --no-extra-checks is for one run of phn check, phn build or phn watch, which report the findings; ` +
			`"extra-checks": false in phaenon.json turns them off for the project, and phn lsp, phn fix and phn fmt go by it as those three do`
		if exitStatus(err) != 2 || strings.TrimSpace(stderr) != want || stdout != "" {
			t.Errorf("phn %v (%v):\nstdout: %s\nstderr: %s\nwant:   %s", args, err, stdout, stderr, want)
		}
	}
	// What follows -- is the tool's own.
	if _, stderr, _ := run(t, root, bin, "fix", "--", "true", "--no-extra-checks"); strings.Contains(stderr, "is for one run of") {
		t.Errorf("phn fix -- true --no-extra-checks: the tool's argument was taken for phn's: %s", stderr)
	}
	_, stderr, err := run(t, root, bin, "build", "--vendor", "--no-extra-checks")
	if want := `phn build --vendor takes no other options: each installed package is checked as its own phaenon.json says, "extra-checks" included`; exitStatus(err) != 1 || strings.TrimSpace(stderr) != want {
		t.Errorf("phn build --vendor --no-extra-checks (%v): %s\nwant: %s", err, stderr, want)
	}
}

// An installed package is checked with its own project file's setting,
// whatever the root project's says: the package that turns its extra
// checks off draws none, and the one that does not draws its own, as a
// warning like every finding of an installed package.
func TestBuildVendorExtraChecks(t *testing.T) {
	bin := phnBinary(t)
	for _, c := range []struct {
		root, lib string
		warned    bool
	}{
		{`{"roots": {"src": ".src"}}`, `{"roots": {"src": ".src"}}`, true},
		{`{"roots": {"src": ".src"}, "extra-checks": false}`, `{"roots": {"src": ".src"}}`, true},
		{`{"roots": {"src": ".src"}}`, `{"roots": {"src": ".src"}, "extra-checks": false}`, false},
	} {
		root := t.TempDir()
		writeTree(t, root, map[string]string{
			"composer.json": `{"name": "acme/root", "require": {"php": ">=8.5", "acme/lib": "*"}}`,
			"phaenon.json":  c.root,
			"src/Use.phn":   "namespace App;\n",
			"vendor/composer/installed.json": `{"packages": [
				{"name": "acme/lib", "version": "1.2.0", "install-path": "../acme/lib"}
			], "dev": true, "dev-package-names": []}`,
			"vendor/composer/autoload_psr4.php": "<?php\n\nreturn array();\n",
			"vendor/acme/lib/composer.json":     `{"name": "acme/lib", "require": {"php": ">=8.5"}}`,
			"vendor/acme/lib/phaenon.json":      c.lib,
			"vendor/acme/lib/src/Power.phn":     strings.Replace(onlyExtraSource, "namespace App;", "namespace Acme\\Lib;", 1),
		})
		stdout, stderr, err := run(t, root, bin, "build", "--vendor")
		warned := strings.Contains(stderr, "vendor/acme/lib/src/Power.phn:5:12: warning P2301: operator ** on string")
		if _, statErr := os.Stat(filepath.Join(root, "vendor/acme/lib/.src/Power.php")); err != nil || statErr != nil || warned != c.warned || strings.Contains(stderr, "error") {
			t.Errorf("the root's %s, the package's %s (%v, %v): the extra check warned of: %v, want %v\n%s%s", c.root, c.lib, err, statErr, warned, c.warned, stdout, stderr)
		}
	}
}

// phn lsp reads the key, and gives each diagnostic its kind in `data`,
// which a client keeps and shows nothing of.
func TestLSPExtraChecks(t *testing.T) {
	bin := phnBinary(t)
	for project, want := range map[string][]string{
		`{"roots": {"src": ".src"}}`:                        {"P1001 language", "P2301 extra", "P2301 parity"},
		`{"roots": {"src": ".src"}, "extra-checks": false}`: {"P1001 language", "P2301 parity"},
	} {
		root := extraTree(t, project)
		s := startLSP(t, bin, root, `{}`)
		path := filepath.Join(root, "src/Kinds.phn")
		uri := fileURI(path)
		open, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{
			"textDocument": map[string]any{"uri": uri, "languageId": "phaenon", "version": 1, "text": kindsSource}}})
		s.send(string(open))
		m := s.next(func(m map[string]any) bool {
			p, _ := m["params"].(map[string]any)
			return m["method"] == "textDocument/publishDiagnostics" && p["uri"] == uri && p["version"] == float64(1)
		})
		var got []string
		for _, d := range m["params"].(map[string]any)["diagnostics"].([]any) {
			d := d.(map[string]any)
			data, _ := d["data"].(map[string]any)
			code, _ := d["code"].(string)
			kind, _ := data["kind"].(string)
			if code == "P4007" && kind == "language" {
				continue // the server is started without a php to ask for its extensions
			}
			got = append(got, code+" "+kind)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("phn lsp with %s: the diagnostics are %v, want %v", project, got, want)
		}
	}
}
