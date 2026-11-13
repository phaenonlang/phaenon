package main

// The compilers a library keeps out (design/roadmap.md D28, design/tooling.md
// §3.7, T18). A library that commits its outputs requires phaenon/phn for
// development only, so nothing in its `require` says which compiler a
// project that installs it may hold, while that project's compiler reads
// the library's phaenon.json. The library says it as a Composer `conflict`
// on phaenon/phn, which has to hold every release from before the newest
// key its project file uses: such a release cannot read the file as it is
// written. It refuses the file, or reads it without the key.
//
// Nothing makes an author write the `conflict`, and one that is missing or
// too low shows only in somebody else's project. So the library's own build
// checks it, where someone can act: `phn build` of the project (no paths)
// and its --check fail on it, after the outputs are written or compared.
// The check is made in a project that commits a root and has a
// composer.json beside its phaenon.json, and never of an installed package.
//
// The question is asked of the releases that exist, not of every version
// below the first that reads the key: Composer installs releases, and those
// from before a key are a closed list, every release being cut from the
// main branch. Each is tried against the constraint as Composer tries it
// (internal/semver). A constraint that cannot be read fails the build as
// one that is too low does, with a message of its own: it is not known to
// keep anything out.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/phaenonlang/phaenon/internal/semver"
)

// compilerPackage is the compiler's Composer package.
const compilerPackage = "phaenon/phn"

// keyReleases gives every key of phaenon.json the first release of
// phaenon/phn that reads it, a nested key under its path. A key is added to
// the project struct together with the release it will come in:
// TestKeyReleases fails for a key without one. The same commit moves
// composer.json's alias of dev-main to that release's line, so that the
// `conflict` the key asks of a library does not keep out a project on the
// main branch, which reads the key (TestBranchAliasCoversTheKeys).
var keyReleases = map[string]string{
	"roots":                      "0.1.0",
	"exclude":                    "0.1.0",
	"php":                        "0.1.0",
	"fmt":                        "0.2.0",
	"unused":                     "0.2.0",
	"unused.reflectedAttributes": "0.2.0",
	"committed":                  "0.5.0",
}

// earlierReleases lists, oldest first, the releases of phaenon/phn from
// before the newest release keyReleases names: the compilers a project
// file can be unreadable to. The list is closed, since every release is cut
// from the main branch and so knows the keys merged before it; a new key
// extends it up to the last release without the key. TestKeyReleases holds
// it to CHANGELOG.md.
var earlierReleases = []string{"0.1.0", "0.2.0", "0.2.1", "0.3.0", "0.4.0", "0.4.1"}

// usedKeys lists the keys the JSON object data writes of those a struct of
// type t reads, in the order of its fields, a nested key under its path
// ("unused.reflectedAttributes").
func usedKeys(data []byte, t reflect.Type, prefix string) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var object map[string]json.RawMessage
	if t.Kind() != reflect.Struct || json.Unmarshal(data, &object) != nil {
		return nil
	}
	var keys []string
	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		value, written := object[tag]
		if tag == "" || !written {
			continue
		}
		keys = append(keys, prefix+tag)
		keys = append(keys, usedKeys(value, t.Field(i).Type, prefix+tag+".")...)
	}
	return keys
}

// floor is the newest of the first releases of the keys the project file
// uses, with the key that asks for it: the oldest compiler that reads the
// file.
func (p *project) floor() (key string, since semver.Version) {
	for _, used := range usedKeys(p.data, reflect.TypeFor[project](), "") {
		release, err := semver.ParseVersion(keyReleases[used])
		if err != nil {
			continue // TestKeyReleases: every key has a release
		}
		if key == "" || since.Compare(release) < 0 {
			key, since = used, release
		}
	}
	return key, since
}

// conflictFloor checks that the composer.json beside the project file keeps
// out, with a `conflict` on phaenon/phn, every release that cannot read the
// file. It returns the message of what it found, or "": for a project that
// commits no root, one without a composer.json, and one whose `conflict`
// holds them all.
func (p *project) conflictFloor() string {
	if p == nil || len(p.Committed) == 0 {
		return ""
	}
	key, since := p.floor()
	var earlier []semver.Version // newest first
	for _, r := range slices.Backward(earlierReleases) {
		if release, err := semver.ParseVersion(r); err == nil && release.Compare(since) < 0 {
			earlier = append(earlier, release)
		}
	}
	if len(earlier) == 0 {
		return ""
	}
	path := filepath.Join(filepath.Dir(p.path), "composer.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	entry := fmt.Sprintf("%q: %q", compilerPackage, "<"+since.String())
	whole := fmt.Sprintf(`write "conflict": {%s}`, entry)
	under := fmt.Sprintf(`write %s under "conflict"`, entry)
	// must is what the file has to do and why, the end of the two messages
	// about a file or a constraint that says nothing certain.
	must := fmt.Sprintf("it has to keep this package from being installed beside the releases of %s that cannot read its %s: the key %q is known from %s on",
		compilerPackage, ProjectFile, key, since)
	if err != nil {
		return fmt.Sprintf("%s: the file cannot be read (%v), and %s; %s", path, err, must, whole)
	}
	// Composer reads the names of composer.json's own keys as written and
	// the names of packages in any letter case.
	var manifest map[string]json.RawMessage
	switch err := json.Unmarshal(data, &manifest); {
	case err != nil && json.Valid(data):
		return fmt.Sprintf("%s: the file is not a JSON object, and %s; %s", path, must, whole)
	case err != nil:
		return fmt.Sprintf("%s: the file cannot be read (%v), and %s; %s", path, err, must, whole)
	}
	var conflicts map[string]json.RawMessage
	if raw, stated := manifest["conflict"]; stated {
		// An empty list is what PHP writes for an object with no members.
		var none []json.RawMessage
		if json.Unmarshal(raw, &none) != nil || len(none) > 0 {
			if err := json.Unmarshal(raw, &conflicts); err != nil {
				return fmt.Sprintf(`%s: "conflict" is not an object of package names and constraints, and %s; %s`, path, must, whole)
			}
		}
	}
	if conflicts != nil {
		whole = under
	}
	// Composer takes one of two spellings of the package's name; each has
	// to keep the releases out, and the first that does not is reported.
	var names []string
	for name := range conflicts {
		if strings.EqualFold(name, compilerPackage) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		var text string
		if err := json.Unmarshal(conflicts[name], &text); err != nil {
			return fmt.Sprintf(`%s: the "conflict" on %q is not a version constraint, a string, and %s; %s`, path, name, must, whole)
		}
		says := fmt.Sprintf(`"conflict": {%q: %q}`, name, text)
		constraint, err := semver.ParseConstraint(text)
		if err != nil {
			return fmt.Sprintf("%s: %s cannot be judged (%v), and %s; %s", path, says, err, must, whole)
		}
		var admitted []semver.Version
		for _, release := range earlier {
			if !constraint.Matches(release) {
				admitted = append(admitted, release)
			}
		}
		if len(admitted) > 0 {
			return fmt.Sprintf("%s: %s does not keep this package from being installed beside %s %s, which cannot read its %s: the key %q is known from %s on; %s",
				path, says, compilerPackage, oneOf(admitted), ProjectFile, key, since, whole)
		}
	}
	if len(names) == 0 {
		return fmt.Sprintf("%s: nothing keeps this package from being installed beside %s %s, which cannot read its %s: the key %q is known from %s on; %s",
			path, compilerPackage, oneOf(earlier), ProjectFile, key, since, whole)
	}
	return ""
}

// oneOf names the releases, newest first, as the ones a project may hold:
// the newest, the two when there are two, and the count of the rest.
func oneOf(releases []semver.Version) string {
	switch n := len(releases); n {
	case 1:
		return releases[0].String()
	case 2:
		return fmt.Sprintf("%s or %s", releases[0], releases[1])
	default:
		return fmt.Sprintf("%s or one of %d earlier releases", releases[0], n-1)
	}
}
