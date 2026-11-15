package main

// The `conflict` a library that commits its outputs declares on phaenon/phn
// (design/tooling.md §3.7): the build of its own project fails unless
// composer.json keeps out every release that cannot read its phaenon.json.

import (
	"encoding/json"
	"fmt"
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

// repairs is where a message sends for what to edit when a release got
// another number than the table of keys assumed.
const repairs = `AGENTS.md, "Git and commits"`

// tableProblems holds a table of the keys' releases and a list of the
// releases from before the newest of them to the releases a changelog
// lists, and says what is wrong, each with its repair.
//
// What it cannot see is when a key was merged: nothing in the tree says it.
// So a key given a release that was already made when the key was merged
// passes, although that release does not read the key and a library's
// `conflict` would then let it in. Whoever reviews a change that adds a key
// checks that the release it is given is not in CHANGELOG.md yet, and
// again when the change is merged, should a release have come in between.
func tableProblems(table map[string]string, earlier []string, changelog string) []string {
	var problems []string
	parse := func(s string) (semver.Version, bool) {
		v, err := semver.ParseVersion(s)
		if err != nil || !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(s) {
			problems = append(problems, fmt.Sprintf("%q is not a release of %s: X.Y.Z", s, compilerPackage))
			return v, false
		}
		return v, true
	}
	var released []semver.Version
	for _, m := range regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`).FindAllStringSubmatch(changelog, -1) {
		if v, ok := parse(m[1]); ok {
			released = append(released, v)
		}
	}
	slices.SortFunc(released, semver.Version.Compare)
	if len(released) == 0 {
		return append(problems, "CHANGELOG.md lists no release")
	}
	made := func(v semver.Version) bool {
		return slices.ContainsFunc(released, func(r semver.Version) bool { return r.Compare(v) == 0 })
	}
	var newest semver.Version
	var newestKeys []string
	for _, key := range slices.Sorted(maps.Keys(table)) {
		since, ok := parse(table[key])
		if !ok {
			continue
		}
		switch order := newest.Compare(since); {
		case newestKeys == nil || order < 0:
			newest, newestKeys = since, []string{key}
		case order == 0:
			newestKeys = append(newestKeys, key)
		}
		// A release that was never made, with one made after it: the
		// number the next release got is not the one the table assumed.
		if after := slices.IndexFunc(released, func(r semver.Version) bool { return r.Compare(since) > 0 }); after >= 0 && !made(since) {
			problems = append(problems, fmt.Sprintf("%q is given %s, and no %s was made: CHANGELOG.md lists %s after it. The first release made after a key is merged is the one that reads it. Give %q %s in keyReleases, and change what names %s with it (%s)",
				key, table[key], table[key], released[after], key, released[after], table[key], repairs))
		}
		if parent, _, nested := strings.Cut(key, "."); nested {
			if of, ok := parse(table[parent]); ok && of.Compare(since) > 0 {
				problems = append(problems, fmt.Sprintf("%q is given %s, a release from before its object %q came", key, table[key], parent))
			}
		}
	}
	if newestKeys == nil {
		return problems
	}
	var before, missing []string
	for _, r := range released {
		if r.Compare(newest) < 0 {
			before = append(before, r.String())
			if !slices.Contains(earlier, r.String()) {
				missing = append(missing, r.String())
			}
		}
	}
	for _, r := range earlier {
		switch v, ok := parse(r); {
		case !ok:
		case v.Compare(newest) >= 0:
			problems = append(problems, fmt.Sprintf("earlierReleases names %s, which is not before %s, the newest release keyReleases names: take it out", r, newest))
		case !made(v):
			problems = append(problems, fmt.Sprintf("earlierReleases names %s, which CHANGELOG.md does not list: take it out", r))
		}
	}
	// A release the list lacks is one of two things, and the tree does not
	// say which: the two repairs go opposite ways.
	if len(missing) > 0 {
		keys := strings.Join(newestKeys, ", ")
		problems = append(problems, fmt.Sprintf("CHANGELOG.md lists %s before %s, the release keyReleases gives %s, and earlierReleases lacks it. The repair depends on when it was cut, which `git tag --contains` of the commit that added the key says. "+
			"Cut before the key was merged, it does not read the key: add it to earlierReleases. "+
			"Cut after, under another number than the table assumed, it reads the key, and %s is what is wrong: give %s the first such release in keyReleases, and change what names %s with it (%s)",
			strings.Join(missing, ", "), newest, keys, newest, keys, newest, repairs))
	} else if len(problems) == 0 && !slices.Equal(earlier, before) {
		problems = append(problems, fmt.Sprintf("earlierReleases is %v; oldest first, the releases before %s are %v", earlier, newest, before))
	}
	return problems
}

// Every key the project file's reader knows has the release it came in, a
// release that was made or the next one; and the releases from before the
// newest of them are the ones CHANGELOG.md lists.
func TestKeyReleases(t *testing.T) {
	known := knownKeys(reflect.TypeFor[project](), "")
	for _, key := range known {
		if _, ok := keyReleases[key]; !ok {
			t.Errorf("the key %q of %s has no first release in keyReleases: give it the next release, extend earlierReleases to the last release without it and move composer.json's alias of dev-main (%s)", key, ProjectFile, repairs)
		}
	}
	for key := range keyReleases {
		if !slices.Contains(known, key) {
			t.Errorf("keyReleases names %q, which is no key of %s", key, ProjectFile)
		}
	}
	changelog, err := os.ReadFile(filepath.Join("..", "..", "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range tableProblems(keyReleases, earlierReleases, string(changelog)) {
		t.Error(problem)
	}
}

// Each way the table can disagree with the releases made gets the repair
// that is right for it: a release under another number than the table
// assumed is repaired in the table, not in the list of earlier releases.
func TestKeyReleasesSayTheRepair(t *testing.T) {
	log := func(releases ...string) string {
		var b strings.Builder
		for _, r := range slices.Backward(releases) {
			b.WriteString("## [" + r + "](https://example.test/" + r + ") (2026-11-13)\n\n* an entry\n\n")
		}
		return b.String()
	}
	table := map[string]string{"roots": "0.1.0", "fmt": "0.2.0", "committed": "0.5.0"}
	earlier := []string{"0.1.0", "0.2.0", "0.2.1", "0.3.0", "0.4.0", "0.4.1"}
	and := func(more ...string) []string { return append(slices.Clone(earlier), more...) }
	given := func(key, release string) map[string]string {
		changed := maps.Clone(table)
		changed[key] = release
		return changed
	}
	cases := []struct {
		name      string
		table     map[string]string
		earlier   []string
		changelog string
		want      []string // what the one message holds; none for no message
	}{
		{"the key not released yet", table, earlier, log(earlier...), nil},
		{"the release the table assumed", table, earlier, log(and("0.5.0")...), nil},
		{"releases after it", table, earlier, log(and("0.5.0", "0.5.1", "0.6.0", "1.0.0")...), nil},
		{"the next release in a lower line than assumed", table, earlier, log(and("0.4.2")...),
			[]string{"CHANGELOG.md lists 0.4.2 before 0.5.0", "Cut before the key was merged", "add it to earlierReleases", "Cut after", "0.5.0 is what is wrong", "give committed the first such release in keyReleases", repairs}},
		{"that repaired in the table", given("committed", "0.4.2"), earlier, log(and("0.4.2")...), nil},
		{"the next release a minor further than assumed", table, earlier, log(and("0.6.0")...),
			[]string{`"committed" is given 0.5.0, and no 0.5.0 was made`, "CHANGELOG.md lists 0.6.0 after it", `Give "committed" 0.6.0 in keyReleases`, repairs}},
		{"that repaired in the table", given("committed", "0.6.0"), earlier, log(and("0.6.0")...), nil},
		{"the next release a major", table, earlier, log(and("1.0.0")...),
			[]string{`no 0.5.0 was made`, `Give "committed" 1.0.0 in keyReleases`}},
		{"a key added without the list extended", given("later", "0.7.0"), earlier, log(and("0.5.0", "0.6.0")...),
			[]string{"CHANGELOG.md lists 0.5.0, 0.6.0 before 0.7.0", "the release keyReleases gives later", "add it to earlierReleases"}},
		{"that repaired in the list", given("later", "0.7.0"), and("0.5.0", "0.6.0"), log(and("0.5.0", "0.6.0")...), nil},
		{"a release in the list that was never made", table, and("0.4.5"), log(earlier...),
			[]string{"earlierReleases names 0.4.5, which CHANGELOG.md does not list: take it out"}},
		{"a release in the list that is not earlier", table, and("0.5.0"), log(and("0.5.0")...),
			[]string{"earlierReleases names 0.5.0, which is not before 0.5.0"}},
		{"the list out of order", table, []string{"0.2.0", "0.1.0", "0.2.1", "0.3.0", "0.4.0", "0.4.1"}, log(earlier...),
			[]string{"oldest first"}},
		{"a key of an object from before the object", given("fmt.later", "0.1.0"), earlier, log(earlier...),
			[]string{`"fmt.later" is given 0.1.0, a release from before its object "fmt" came`}},
		// What the check cannot see (tableProblems): 0.4.1 was made before
		// this key was merged and does not read it. The case stands here so
		// that a guard, once there is a sound one, has its place.
		{"a key given a release already made", given("later", "0.4.1"), earlier, log(earlier...), nil},
	}
	for _, c := range cases {
		problems := tableProblems(c.table, c.earlier, c.changelog)
		switch {
		case c.want == nil && len(problems) > 0:
			t.Errorf("%s: %v", c.name, problems)
		case c.want != nil && len(problems) != 1:
			t.Errorf("%s: %d messages, want one: %v", c.name, len(problems), problems)
		case c.want != nil:
			for _, part := range c.want {
				if !strings.Contains(problems[0], part) {
					t.Errorf("%s: the message does not say %q:\n%s", c.name, part, problems[0])
				}
			}
		}
	}
}

// aliasProblem says what is wrong with the alias composer.json gives
// dev-main, for a table of the keys' releases; "" for nothing.
func aliasProblem(alias string, table map[string]string) string {
	line := regexp.MustCompile(`^(\d+\.\d+)\.x-dev$`).FindStringSubmatch(alias)
	if line == nil {
		return fmt.Sprintf(`composer.json's extra.branch-alias names dev-main %q, want "X.Y.x-dev"`, alias)
	}
	// Release lines are compared: X.Y of the alias with X.Y of the release.
	aliased, err := semver.ParseVersion(line[1])
	if err != nil {
		return err.Error()
	}
	for _, key := range slices.Sorted(maps.Keys(table)) {
		minor := regexp.MustCompile(`^\d+\.\d+`).FindString(table[key])
		since, err := semver.ParseVersion(minor)
		if err != nil {
			return err.Error()
		}
		if aliased.Compare(since) < 0 {
			return fmt.Sprintf("composer.json aliases dev-main to %s, a line below %s, the release keyReleases gives %q. "+
				"On the main branch the alias is what is wrong: the branch reads the key, and a library's conflict below %s would keep a project on it out; move the alias to %s.x-dev in the commit that adds the key. "+
				"On a release PR, whose workflow wrote the alias, the release is of a lower line than the table assumed and reads the key, and the table is what is wrong: give %q that release in keyReleases (%s)",
				alias, table[key], key, table[key], minor, key, repairs)
		}
	}
	return ""
}

// The main branch reads every key of keyReleases, so the version Composer
// takes it for, composer.json's alias of dev-main, is not below the newest
// release the table names: a library's `conflict` below that release would
// keep out a project on the compiler's main branch, whose compiler reads
// the key. The release PR's workflow writes the alias at each release's
// minor version (tools/release); a key that comes in the next minor moves
// it there in the commit that adds the key.
func TestBranchAliasCoversTheKeys(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "composer.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Extra struct {
			BranchAlias map[string]string `json:"branch-alias"`
		} `json:"extra"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if problem := aliasProblem(manifest.Extra.BranchAlias["dev-main"], keyReleases); problem != "" {
		t.Error(problem)
	}
	table := map[string]string{"roots": "0.1.0", "committed": "0.5.0"}
	for alias, want := range map[string]string{
		"0.5.x-dev": "",
		"0.6.x-dev": "",
		"1.0.x-dev": "",
		"0.4.x-dev": `a line below 0.5.0, the release keyReleases gives "committed"`,
		"dev-main":  `want "X.Y.x-dev"`,
	} {
		if got := aliasProblem(alias, table); (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("the alias %s: %q, want it to say %q", alias, got, want)
		}
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
type conflictCase struct {
	name, manifest, want string
}

// unjudged is the case of a constraint the reader does not read, with the
// reason it gives; the constraint may hold characters that do not show.
func unjudged(name, constraint, reason string) conflictCase {
	manifest, err := json.Marshal(map[string]map[string]string{"conflict": {"phaenon/phn": constraint}})
	if err != nil {
		panic(err)
	}
	return conflictCase{name, string(manifest),
		fmt.Sprintf(`composer.json: "conflict": {"phaenon/phn": %q} cannot be judged (%s)`, constraint, reason) + conflictMust + conflictUnder}
}

const (
	joined   = ` beside another alternative, and Composer joins neighbouring ranges into one`
	notPlain = `it holds a tab, a line break, a control character or a space that is not the plain one`
	notRead  = ` is not a version constraint phn reads`
)

var conflictCases = append([]conflictCase{
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
		`composer.json: "conflict": {"Phaenon/Phn": "<0.4"} does not keep this package from being installed beside phaenon/phn 0.4.1 or 0.4.0` + conflictWhy + `write "Phaenon/Phn": "<0.5.0" under "conflict"`},

	// Of two spellings of the name Composer takes the last, and of a name
	// written twice the last value at the place of the first.
	{"two spellings of the name, the last sufficient", `{"conflict": {"Phaenon/Phn": "<0.4", "phaenon/phn": "<0.5"}}`, ""},
	{"two spellings of the name, the one passed over not read", `{"conflict": {"Phaenon/Phn": "<0.5.x-dev", "phaenon/phn": "<0.5"}}`, ""},
	{"the name written twice, the last sufficient", `{"conflict": {"phaenon/phn": "<0.4", "phaenon/phn": "<0.5"}}`, ""},
	{"two spellings of the name, the last too low", `{"conflict": {"phaenon/phn": "<0.5", "Phaenon/Phn": "<0.4"}}`,
		`composer.json: "conflict": {"Phaenon/Phn": "<0.4"} does not keep this package from being installed beside phaenon/phn 0.4.1 or 0.4.0` + conflictWhy + `write "Phaenon/Phn": "<0.5.0" under "conflict"`},
	{"the name written twice, the last too low", `{"conflict": {"phaenon/phn": "<0.5", "phaenon/phn": "<0.4"}}`,
		`composer.json: "conflict": {"phaenon/phn": "<0.4"} does not keep this package from being installed beside phaenon/phn 0.4.1 or 0.4.0` + conflictWhy + conflictUnder},
	{"the name written again after another spelling", `{"conflict": {"phaenon/phn": "<0.4", "Phaenon/Phn": "<0.3", "phaenon/phn": "<0.5"}}`,
		`composer.json: "conflict": {"Phaenon/Phn": "<0.3"} does not keep this package from being installed beside phaenon/phn 0.4.1 or one of 2 earlier releases` + conflictWhy + `write "Phaenon/Phn": "<0.5.0" under "conflict"`},
	{"the key written twice, the last too low", `{"conflict": {"phaenon/phn": "<0.5"}, "conflict": {"phaenon/phn": "<0.4"}}`,
		`composer.json: "conflict": {"phaenon/phn": "<0.4"} does not keep this package from being installed beside phaenon/phn 0.4.1 or 0.4.0` + conflictWhy + conflictUnder},

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
		`composer.json: the "conflict" on "phaenon/phn" is ["<0.5"], not a version constraint` + conflictMust + conflictUnder},
	{"null for a constraint", `{"conflict": {"phaenon/phn": null}}`,
		`composer.json: the "conflict" on "phaenon/phn" is null, not a version constraint` + conflictMust + conflictUnder},
	{"an object for a constraint, under another spelling", `{"conflict": {"Phaenon/PHN": {"version": "<0.5", "why": "it cannot read the project file as written"}}}`,
		`composer.json: the "conflict" on "Phaenon/PHN" is no string, not a version constraint` + conflictMust + `write "Phaenon/PHN": "<0.5.0" under "conflict"`},
	{"a conflict that is no object", `{"conflict": "phaenon/phn <0.5"}`,
		`composer.json: "conflict" is not an object of package names and constraints` + conflictMust + conflictWhole},
	{"a file that is no object", `["phaenon/phn"]`,
		`composer.json: the file is not a JSON object` + conflictMust + conflictWhole},
},
	// What Composer makes of these is not what their alternatives say: it
	// joins the neighbouring ranges, and lets releases in that the
	// alternatives, read one by one, keep out.
	unjudged("a range from above its end beside the one that starts there", ">=0.3 <0.1 || >=0.1 <0.5", `">=0.3 <0.1" is a range that ends before it begins,`+joined),
	unjudged("the same as hyphen ranges", "0.3 - 0.0 || 0.1 - 0.4", `"0.3 - 0.0" is a range that ends before it begins,`+joined),
	unjudged("the same after the ranges that hold every release", "<0.4 || ^0.4 || >=0.5 <0.3", `">=0.5 <0.3" is a range that ends before it begins,`+joined),
	unjudged("a range bounded by a branch beside another", "<0.5 || >=0.5 <dev-main", `">=0.5 <dev-main" is a range bounded by a branch,`+joined),
	// Composer refuses these; so does the reader.
	unjudged("a space inside an operator", "< =0.4.99", `"< =0.4.99"`+notRead),
	unjudged("a space inside an operator, before a second constraint", "> =0.1 <0.5", `"> =0.1"`+notRead),
	unjudged("a no-break space after the constraint", "<0.5"+string(rune(0xa0)), notPlain),
	unjudged("a no-break space between two constraints", ">=0.1"+string(rune(0xa0))+"<0.5", notPlain),
	unjudged("a form feed after the constraint", "<0.5\f", notPlain),
	// Composer reads these, another way than their parts say.
	unjudged("a comma after an operator's character", "<0.5+b=,>=0.2", `a comma after <, > or = parts nothing`),
	unjudged("a line break inside", "<0.5\n>=0.1", notPlain),
)

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

// pair is a member of a JSON object, in the order the object writes them.
type pair struct {
	name  string
	value json.RawMessage
}

// pairs reads the members of a JSON object in their order; nil for what is
// no object.
func pairs(raw []byte) []pair {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if open, err := dec.Token(); err != nil || open != json.Delim('{') {
		return nil
	}
	list := []pair{}
	for dec.More() {
		token, _ := dec.Token()
		name, _ := token.(string)
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return nil
		}
		list = append(list, pair{name, value})
	}
	return list
}

// with returns the object with value under name: in the place of every
// member that has the name, written as it is, else as a last member.
func with(object []pair, name string, value []byte) []byte {
	found := false
	for i := range object {
		if object[i].name == name {
			object[i].value, found = value, true
		}
	}
	if !found {
		object = append(object, pair{name, value})
	}
	var b strings.Builder
	for i, member := range object {
		if i > 0 {
			b.WriteString(", ")
		}
		quoted, _ := json.Marshal(member.name)
		b.WriteString(string(quoted) + ": " + string(member.value))
	}
	return []byte("{" + b.String() + "}")
}

var (
	adviceWhole = regexp.MustCompile(`; write "conflict": (\{"[^"]+": "[^"]+"\})$`)
	adviceUnder = regexp.MustCompile(`; write ("[^"]+"): ("[^"]+") under "conflict"$`)
)

// follow does to a composer.json what the message says to write, as an
// author would with no thought: the whole key, or the one entry under the
// name the message gives, everything else left as it is.
func follow(t *testing.T, manifest, message string) string {
	t.Helper()
	if m := adviceWhole.FindStringSubmatch(message); m != nil {
		return string(with(pairs([]byte(manifest)), "conflict", []byte(m[1])))
	}
	m := adviceUnder.FindStringSubmatch(message)
	if m == nil {
		t.Fatalf("the message names no line to write: %s", message)
	}
	var name string
	if err := json.Unmarshal([]byte(m[1]), &name); err != nil {
		t.Fatal(err)
	}
	object := pairs([]byte(manifest))
	conflict := []byte("{}")
	for _, member := range object {
		if member.name == "conflict" {
			conflict = member.value
		}
	}
	if pairs(conflict) == nil {
		t.Fatalf("the message says to write under a \"conflict\" that is no object: %s", message)
	}
	return string(with(object, "conflict", with(pairs(conflict), name, []byte(m[2]))))
}

// The line a message tells the author to write passes the check: for every
// composer.json of the table that fails, with the line written as said.
func TestConflictFloorTakesItsOwnAdvice(t *testing.T) {
	followed := 0
	for _, c := range conflictCases {
		if c.want == "" {
			continue
		}
		t.Chdir(t.TempDir())
		writeTree(t, ".", map[string]string{ProjectFile: committedFile, "composer.json": c.manifest})
		p, err := readProject(ProjectFile)
		if err != nil {
			t.Fatal(err)
		}
		advised := follow(t, c.manifest, p.conflictFloor())
		writeTree(t, ".", map[string]string{"composer.json": advised})
		if got := p.conflictFloor(); got != "" {
			t.Errorf("%s: %s\nwith the line written: %s\nthe check still says: %s", c.name, c.manifest, advised, got)
		}
		followed++
	}
	if followed < 20 {
		t.Errorf("the advice of %d messages was followed; the table has more that fail", followed)
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
