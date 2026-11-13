package main

// The `conflict` a library that commits its outputs declares on phaenon/phn
// (design/tooling.md §3.7): the build of its own project fails unless
// composer.json keeps out every release that cannot read its phaenon.json.

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/semver"
)

// kitManifest is the composer.json of a library that commits its outputs:
// it keeps out the releases from before "committed".
const kitManifest = `{"name": "acme/kit", "require": {"php": ">=8.5"}, "conflict": {"phaenon/phn": "<0.5.0"}}`

// knownKeys lists the keys a struct of type t reads from JSON, a nested
// key under its path.
func knownKeys(t reflect.Type, prefix string) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var keys []string
	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		keys = append(keys, prefix+tag)
		keys = append(keys, knownKeys(t.Field(i).Type, prefix+tag+".")...)
	}
	return keys
}

// Every key the project file's reader knows has the release it came in, a
// release that was made or the next one; and the releases from before the
// newest of them are the ones CHANGELOG.md lists.
func TestKeyReleases(t *testing.T) {
	known := knownKeys(reflect.TypeFor[project](), "")
	for _, key := range known {
		if _, ok := keyReleases[key]; !ok {
			t.Errorf("the key %q of %s has no first release in keyReleases", key, ProjectFile)
		}
	}
	for key := range keyReleases {
		if !slices.Contains(known, key) {
			t.Errorf("keyReleases names %q, which is no key of %s", key, ProjectFile)
		}
	}
	parse := func(s string) semver.Version {
		t.Helper()
		v, err := semver.ParseVersion(s)
		if err != nil || !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(s) {
			t.Fatalf("%q is not a release of %s (%v)", s, compilerPackage, err)
		}
		return v
	}
	changelog, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	var released []semver.Version
	for _, m := range regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`).FindAllStringSubmatch(string(changelog), -1) {
		released = append(released, parse(m[1]))
	}
	slices.SortFunc(released, semver.Version.Compare)
	if len(released) == 0 {
		t.Fatal("CHANGELOG.md lists no release")
	}
	newest := parse("0.0.0")
	for key, release := range keyReleases {
		since := parse(release)
		if newest.Compare(since) < 0 {
			newest = since
		}
		made := slices.ContainsFunc(released, func(r semver.Version) bool { return r.Compare(since) == 0 })
		if !made && released[len(released)-1].Compare(since) > 0 {
			t.Errorf("%q is given %s, which CHANGELOG.md does not list among the releases made", key, release)
		}
		if parent, _, nested := strings.Cut(key, "."); nested && parse(keyReleases[parent]).Compare(since) > 0 {
			t.Errorf("%q is given %s, a release from before its object %q came", key, release, parent)
		}
	}
	var earlier []string
	for _, r := range released {
		if r.Compare(newest) < 0 {
			earlier = append(earlier, r.String())
		}
	}
	if !slices.Equal(earlierReleases, earlier) {
		t.Errorf("earlierReleases is %v; the releases before %s in CHANGELOG.md are %v", earlierReleases, newest, earlier)
	}
}

// The floor is the newest first release among the keys the file writes,
// the keys of its objects included.
func TestProjectFloor(t *testing.T) {
	cases := []struct{ file, key, since string }{
		{`{"roots": {"src": ".src"}}`, "roots", "0.1.0"},
		{`{"php": "8.6", "exclude": ["x"], "roots": {"src": ".src"}}`, "roots", "0.1.0"},
		{`{"roots": {"src": ".src"}, "fmt": []}`, "fmt", "0.2.0"},
		{`{"roots": {"src": ".src"}, "unused": {}}`, "unused", "0.2.0"},
		{`{"unused": {"reflectedAttributes": []}, "roots": {"src": ".src"}, "committed": []}`, "committed", "0.5.0"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{ProjectFile: c.file})
		p, err := readProject(filepath.Join(dir, ProjectFile))
		if err != nil {
			t.Fatal(err)
		}
		if key, since := p.floor(); key != c.key || since.String() != c.since {
			t.Errorf("%s: the floor is %s, for %q; want %s, for %q", c.file, since, key, c.since, c.key)
		}
	}
	file := `{"roots": {"src": ".src"}, "unused": {"reflectedAttributes": []}, "php": "8.5"}`
	want := []string{"roots", "php", "unused", "unused.reflectedAttributes"}
	if got := usedKeys([]byte(file), reflect.TypeFor[project](), ""); !slices.Equal(got, want) {
		t.Errorf("%s uses %v, want %v", file, got, want)
	}
}

const (
	committedFile = `{"roots": {"src": ".src"}, "committed": ["src"]}`
	// The ends of the messages: what the file is for, and the line to write.
	conflictWhy   = `, which cannot read its phaenon.json: the key "committed" is known from 0.5.0 on; `
	conflictMust  = `, and it has to keep this package from being installed beside the releases of phaenon/phn that cannot read its phaenon.json: the key "committed" is known from 0.5.0 on; `
	conflictWhole = `write "conflict": {"phaenon/phn": "<0.5.0"}`
	conflictUnder = `write "phaenon/phn": "<0.5.0" under "conflict"`
	everyRelease  = `phaenon/phn 0.4.1 or one of 5 earlier releases`
)

// conflictCases are composer.json files of a project that commits a root,
// each with what the check says of it, "" for nothing.
var conflictCases = []struct {
	name, manifest, want string
}{
	// The constraint holds every release from before the key.
	{"below the release, in two numbers", `{"conflict": {"phaenon/phn": "<0.5"}}`, ""},
	{"below the release", `{"conflict": {"phaenon/phn": "<0.5.0"}}`, ""},
	{"below its first development version", `{"conflict": {"phaenon/phn": "<0.5.0-dev"}}`, ""},
	{"below one of its pre-releases", `{"conflict": {"phaenon/phn": "<0.5.0-beta1"}}`, ""},
	{"up to a version no release has", `{"conflict": {"phaenon/phn": "<=0.4.99"}}`, ""},
	{"up to the last release before it", `{"conflict": {"phaenon/phn": "<=0.4.1"}}`, ""},
	{"with an alternative", `{"conflict": {"phaenon/phn": "<0.5 || >=9"}}`, ""},
	{"with a space after the operator", `{"conflict": {"phaenon/phn": "< 0.5"}}`, ""},
	{"with a v", `{"conflict": {"phaenon/phn": "<v0.5.0"}}`, ""},
	{"with a stability flag", `{"conflict": {"phaenon/phn": "<0.5@dev"}}`, ""},
	{"as a range from the first release", `{"conflict": {"phaenon/phn": ">=0.1 <0.5"}}`, ""},
	{"as a hyphen range", `{"conflict": {"phaenon/phn": "0.1 - 0.4"}}`, ""},
	{"as release lines", `{"conflict": {"phaenon/phn": "0.1.* || 0.2.* || ~0.3.0 || ^0.4"}}`, ""},
	{"release by release", `{"conflict": {"phaenon/phn": "0.1.0 || 0.2.0 || 0.2.1 || 0.3.0 || 0.4.0 || 0.4.1"}}`, ""},
	{"higher than it need be", `{"conflict": {"phaenon/phn": "<1.0"}}`, ""},
	{"every version", `{"conflict": {"phaenon/phn": "*"}}`, ""},
	{"among other packages", `{"conflict": {"acme/other": "*", "phaenon/phn": "<0.5", "phaenon/runtime": "<0.4"}}`, ""},
	{"the name in another letter case", `{"conflict": {"Phaenon/PHN": "<0.5"}}`, ""},
	{"in a project that says it is one", `{"type": "project", "conflict": {"phaenon/phn": "<0.5"}}`, ""},

	// It lets a release in.
	{"none at all", `{"name": "acme/kit"}`,
		`composer.json: nothing keeps this package from being installed beside ` + everyRelease + conflictWhy + conflictWhole},
	{"an empty object", `{"conflict": {}}`,
		`composer.json: nothing keeps this package from being installed beside ` + everyRelease + conflictWhy + conflictUnder},
	{"the empty list PHP writes", `{"conflict": []}`,
		`composer.json: nothing keeps this package from being installed beside ` + everyRelease + conflictWhy + conflictWhole},
	{"null", `{"conflict": null}`,
		`composer.json: nothing keeps this package from being installed beside ` + everyRelease + conflictWhy + conflictWhole},
	{"for another package only", `{"conflict": {"phaenon/runtime": "<0.4", "acme/phn": "<0.5"}}`,
		`composer.json: nothing keeps this package from being installed beside ` + everyRelease + conflictWhy + conflictUnder},
	{"under a key Composer does not read", `{"Conflict": {"phaenon/phn": "<0.5"}}`,
		`composer.json: nothing keeps this package from being installed beside ` + everyRelease + conflictWhy + conflictWhole},
	{"in a project that says it is one, none", `{"type": "project"}`,
		`composer.json: nothing keeps this package from being installed beside ` + everyRelease + conflictWhy + conflictWhole},
	{"a release line too low", `{"conflict": {"phaenon/phn": "<0.4"}}`,
		`composer.json: "conflict": {"phaenon/phn": "<0.4"} does not keep this package from being installed beside phaenon/phn 0.4.1 or 0.4.0` + conflictWhy + conflictUnder},
	{"one release too low", `{"conflict": {"phaenon/phn": "<0.4.1"}}`,
		`composer.json: "conflict": {"phaenon/phn": "<0.4.1"} does not keep this package from being installed beside phaenon/phn 0.4.1` + conflictWhy + conflictUnder},
	{"one release line", `{"conflict": {"phaenon/phn": "0.3.*"}}`,
		`composer.json: "conflict": {"phaenon/phn": "0.3.*"} does not keep this package from being installed beside phaenon/phn 0.4.1 or one of 4 earlier releases` + conflictWhy + conflictUnder},
	{"a range that starts too late", `{"conflict": {"phaenon/phn": ">=0.2 <0.5"}}`,
		`composer.json: "conflict": {"phaenon/phn": ">=0.2 <0.5"} does not keep this package from being installed beside phaenon/phn 0.1.0` + conflictWhy + conflictUnder},
	{"one release left out", `{"conflict": {"phaenon/phn": "<0.5 !=0.2.1"}}`,
		`composer.json: "conflict": {"phaenon/phn": "<0.5 !=0.2.1"} does not keep this package from being installed beside phaenon/phn 0.2.1` + conflictWhy + conflictUnder},
	{"the release itself", `{"conflict": {"phaenon/phn": "0.5.0"}}`,
		`composer.json: "conflict": {"phaenon/phn": "0.5.0"} does not keep this package from being installed beside ` + everyRelease + conflictWhy + conflictUnder},
	{"from the release on", `{"conflict": {"phaenon/phn": ">=0.5"}}`,
		`composer.json: "conflict": {"phaenon/phn": ">=0.5"} does not keep this package from being installed beside ` + everyRelease + conflictWhy + conflictUnder},
	{"a branch", `{"conflict": {"phaenon/phn": "dev-main"}}`,
		`composer.json: "conflict": {"phaenon/phn": "dev-main"} does not keep this package from being installed beside ` + everyRelease + conflictWhy + conflictUnder},
	{"the name in another letter case, too low", `{"conflict": {"Phaenon/Phn": "<0.4"}}`,
		`composer.json: "conflict": {"Phaenon/Phn": "<0.4"} does not keep this package from being installed beside phaenon/phn 0.4.1 or 0.4.0` + conflictWhy + conflictUnder},
	{"two spellings of the name, one too low", `{"conflict": {"phaenon/phn": "<0.5", "Phaenon/Phn": "<0.4"}}`,
		`composer.json: "conflict": {"Phaenon/Phn": "<0.4"} does not keep this package from being installed beside phaenon/phn 0.4.1 or 0.4.0` + conflictWhy + conflictUnder},

	// It cannot be judged: nothing is known to be kept out.
	{"an operator Composer does not have", `{"conflict": {"phaenon/phn": "~>0.4"}}`,
		`composer.json: "conflict": {"phaenon/phn": "~>0.4"} cannot be judged ("~>0.4" uses the operator ~>, which Composer does not have: ~ is its spelling)` + conflictMust + conflictUnder},
	{"a numeric branch", `{"conflict": {"phaenon/phn": "<0.5.x-dev"}}`,
		`composer.json: "conflict": {"phaenon/phn": "<0.5.x-dev"} cannot be judged ("<0.5.x-dev" is not a version constraint phn reads)` + conflictMust + conflictUnder},
	{"a wildcard after an operator", `{"conflict": {"phaenon/phn": "<=0.4.*"}}`,
		`composer.json: "conflict": {"phaenon/phn": "<=0.4.*"} cannot be judged ("<=0.4.*" is not a version constraint phn reads)` + conflictMust + conflictUnder},
	{"an alias", `{"conflict": {"phaenon/phn": "<0.5 as 1.0"}}`,
		`composer.json: "conflict": {"phaenon/phn": "<0.5 as 1.0"} cannot be judged ("as" stands beside another constraint, where Composer reads the letters ` + "`as`" + ` as an alias)` + conflictMust + conflictUnder},
	{"no constraint", `{"conflict": {"phaenon/phn": ""}}`,
		`composer.json: "conflict": {"phaenon/phn": ""} cannot be judged (it is empty)` + conflictMust + conflictUnder},
	{"a constraint that is no string", `{"conflict": {"phaenon/phn": ["<0.5"]}}`,
		`composer.json: the "conflict" on "phaenon/phn" is not a version constraint, a string` + conflictMust + conflictUnder},
	{"a conflict that is no object", `{"conflict": "phaenon/phn <0.5"}`,
		`composer.json: "conflict" is not an object of package names and constraints` + conflictMust + conflictWhole},
	{"a file that is no object", `["phaenon/phn"]`,
		`composer.json: the file is not a JSON object` + conflictMust + conflictWhole},
}

func TestConflictFloor(t *testing.T) {
	for _, c := range conflictCases {
		t.Run(c.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeTree(t, ".", map[string]string{ProjectFile: committedFile, "composer.json": c.manifest})
			p, err := readProject(ProjectFile)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.conflictFloor(); got != c.want {
				t.Errorf("%s\n got: %s\nwant: %s", c.manifest, got, c.want)
			}
		})
	}
}

// A composer.json that cannot be read says nothing certain, and fails as a
// constraint that cannot be judged does: with what stopped the reader.
func TestConflictFloorOfAFileThatCannotBeRead(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTree(t, ".", map[string]string{ProjectFile: committedFile, "composer.json": `{"conflict": {"phaenon/phn": "<0.5"},}`})
	p, err := readProject(ProjectFile)
	if err != nil {
		t.Fatal(err)
	}
	unread := func(what string) {
		t.Helper()
		got := p.conflictFloor()
		reason, found := strings.CutPrefix(got, "composer.json: the file cannot be read (")
		reason, ended := strings.CutSuffix(reason, ")"+conflictMust+conflictWhole)
		if !found || !ended || reason == "" {
			t.Errorf("%s: %s", what, got)
		}
	}
	unread("a file that is not JSON")
	if err := os.Remove("composer.json"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("composer.json", 0o755); err != nil {
		t.Fatal(err)
	}
	unread("a directory")
}

// Nothing is asked of a project that commits no root, whatever keys it
// uses, of one without a composer.json, and of no project at all.
func TestConflictFloorWhereItDoesNotApply(t *testing.T) {
	if got := (*project)(nil).conflictFloor(); got != "" {
		t.Errorf("without a project file: %s", got)
	}
	for _, file := range []string{
		`{"roots": {"src": ".src"}}`,
		`{"roots": {"src": ".src"}, "committed": []}`,
		`{"roots": {"src": ".src"}, "fmt": [], "unused": {"reflectedAttributes": []}}`,
	} {
		t.Chdir(t.TempDir())
		writeTree(t, ".", map[string]string{ProjectFile: file, "composer.json": `{"name": "acme/app"}`})
		p, err := readProject(ProjectFile)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.conflictFloor(); got != "" {
			t.Errorf("%s: %s", file, got)
		}
	}
	t.Chdir(t.TempDir())
	writeTree(t, ".", map[string]string{ProjectFile: committedFile})
	p, err := readProject(ProjectFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.conflictFloor(); got != "" {
		t.Errorf("without a composer.json: %s", got)
	}
}

// The line every message tells the author to write passes the check.
func TestConflictFloorTakesItsOwnAdvice(t *testing.T) {
	line := regexp.MustCompile(`write ("conflict": \{.*\})$`)
	t.Chdir(t.TempDir())
	writeTree(t, ".", map[string]string{ProjectFile: committedFile, "composer.json": `{}`})
	p, err := readProject(ProjectFile)
	if err != nil {
		t.Fatal(err)
	}
	m := line.FindStringSubmatch(p.conflictFloor())
	if m == nil {
		t.Fatalf("no line to write in %q", p.conflictFloor())
	}
	writeTree(t, ".", map[string]string{"composer.json": "{" + m[1] + "}"})
	if got := p.conflictFloor(); got != "" {
		t.Errorf("with %s: %s", m[1], got)
	}
}

// `phn build` and its --check fail on a composer.json that lets an earlier
// release in, with one line, after doing everything else they do; the
// commands that are not the project's whole build say nothing of it.
func TestBuildChecksTheConflict(t *testing.T) {
	bin := phnBinary(t)
	const manifest = `{"name": "acme/kit", "require": {"php": ">=8.5"}, "conflict": {"phaenon/phn": "<0.4"}}`
	message := `composer.json: "conflict": {"phaenon/phn": "<0.4"} does not keep this package from being installed beside phaenon/phn 0.4.1 or 0.4.0` +
		conflictWhy + conflictUnder + "\n"
	// Two copies of one project with that composer.json; only the second
	// commits its root.
	plain, kit := fixTree(t), fixTree(t)
	writeTree(t, plain, map[string]string{"composer.json": manifest})
	writeTree(t, kit, map[string]string{"composer.json": manifest, ProjectFile: committedFile})
	for i, step := range []struct {
		args   []string
		failed bool // by the conflict alone
	}{
		{[]string{"build", "--check"}, false}, // nothing is built yet: stale either way, and the line
		{[]string{"build"}, true},
		{[]string{"build", "--check"}, true},
		{[]string{"build", "--emit-on-error"}, true},
		{[]string{"build", "--check", "--emit-on-error"}, true},
	} {
		wantOut, wantErr, wantStatus := run(t, plain, bin, step.args...)
		stdout, stderr, status := run(t, kit, bin, step.args...)
		if step.failed && wantStatus != nil {
			t.Fatalf("step %d, phn %s without the key: %v\n%s", i, strings.Join(step.args, " "), wantStatus, wantErr)
		}
		var exit *exec.ExitError
		if !asExit(status, &exit) || exit.ExitCode() != 1 {
			t.Errorf("step %d, phn %s: %v, want status 1", i, strings.Join(step.args, " "), status)
		}
		if stdout != wantOut {
			t.Errorf("step %d, phn %s: printed\n%swithout the key\n%s", i, strings.Join(step.args, " "), stdout, wantOut)
		}
		// The line follows the findings and comes before the command's own
		// last line, when it has one.
		if !step.failed {
			if strings.Count(stderr, message) != 1 || strings.Replace(stderr, message, "", 1) != wantErr {
				t.Errorf("step %d, phn %s: stderr\n%swithout the key\n%s", i, strings.Join(step.args, " "), stderr, wantErr)
			}
		} else if stderr != wantErr+message {
			t.Errorf("step %d, phn %s: stderr\n%swant\n%s", i, strings.Join(step.args, " "), stderr, wantErr+message)
		}
	}
	got, want := snapshot(t, kit), snapshot(t, plain)
	delete(got, ProjectFile)
	delete(want, ProjectFile)
	if !maps.Equal(got, want) {
		t.Errorf("the two trees differ:\nwith the key    %v\nwithout the key %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	if _, err := os.Stat(filepath.Join(kit, ".src/Box.php")); err != nil {
		t.Errorf("the build that failed on the conflict wrote no output: %v", err)
	}
	// What is not the project's whole build does not ask.
	for _, args := range [][]string{{"check"}, {"build", "src"}, {"build", "--out", t.TempDir()}, {"build", "--vendor"}, {"fmt", "--check"}} {
		wantOut, wantErr, wantStatus := run(t, plain, bin, args...)
		stdout, stderr, status := run(t, kit, bin, args...)
		if strings.Contains(stderr, "conflict") || stderr != wantErr || (status == nil) != (wantStatus == nil) {
			t.Errorf("phn %s: (%v)\n%s%swithout the key (%v)\n%s%s", strings.Join(args, " "), status, stdout, stderr, wantStatus, wantOut, wantErr)
		}
	}
	// With the line the message gives, the build passes and is silent.
	writeTree(t, kit, map[string]string{"composer.json": kitManifest})
	writeTree(t, plain, map[string]string{"composer.json": kitManifest})
	for _, args := range [][]string{{"build"}, {"build", "--check"}} {
		wantOut, wantErr, _ := run(t, plain, bin, args...)
		stdout, stderr, status := run(t, kit, bin, args...)
		if status != nil || stdout != wantOut || stderr != wantErr {
			t.Errorf("phn %s with the conflict written (%v):\n%s%s", strings.Join(args, " "), status, stdout, stderr)
		}
	}
}

// An installed package is never asked: its release is not this project's
// to repair, and Composer has applied whatever its composer.json says.
func TestBuildDoesNotCheckAnInstalledPackagesConflict(t *testing.T) {
	bin := phnBinary(t)
	root := committedTree(t) // acme/kit commits src, and its composer.json has no conflict
	for _, args := range [][]string{{"build", "--vendor"}, {"build"}, {"build", "--check"}} {
		stdout, stderr, err := run(t, root, bin, args...)
		if err != nil || strings.Contains(stderr, "conflict") {
			t.Errorf("phn %s (%v):\n%s%s", strings.Join(args, " "), err, stdout, stderr)
		}
	}
}
