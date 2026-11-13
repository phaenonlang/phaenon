package semver

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// vectors is testdata/constraints.json: constraints, each with the versions
// of the list that satisfy it as Composer answers. A constraint Composer
// refuses is `refused`; one Composer reads and this package does not is
// `unread`, and has no answer here.
type vectors struct {
	Versions    []string `json:"versions"`
	Constraints []vector `json:"constraints"`
}

type vector struct {
	Constraint string   `json:"constraint"`
	Refused    bool     `json:"refused,omitempty"`
	Unread     bool     `json:"unread,omitempty"`
	Matches    []string `json:"matches,omitempty"`
}

const vectorsFile = "testdata/constraints.json"

func loadVectors(t *testing.T) vectors {
	t.Helper()
	data, err := os.ReadFile(vectorsFile)
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Versions) == 0 || len(v.Constraints) == 0 {
		t.Fatal("no vectors")
	}
	return v
}

// The reader gives Composer's answer for every constraint it reads, and
// refuses the ones the file says it does not read: it never reads one
// differently.
func TestConstraintVectors(t *testing.T) {
	v := loadVectors(t)
	var versions []Version
	for _, s := range v.Versions {
		version, err := ParseVersion(s)
		if err != nil {
			t.Fatalf("the version %s: %v", s, err)
		}
		versions = append(versions, version)
	}
	seen := map[string]bool{}
	for _, c := range v.Constraints {
		if seen[c.Constraint] {
			t.Errorf("%q is listed twice", c.Constraint)
		}
		seen[c.Constraint] = true
		constraint, err := ParseConstraint(c.Constraint)
		if c.Refused || c.Unread {
			if err == nil {
				t.Errorf("%q was read; want an error", c.Constraint)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.Constraint, err)
			continue
		}
		matches := []string{}
		for _, version := range versions {
			if constraint.Matches(version) {
				matches = append(matches, version.String())
			}
		}
		if !slices.Equal(matches, c.Matches) {
			t.Errorf("%q matches\n\t%v\nwant\n\t%v", c.Constraint, matches, c.Matches)
		}
	}
}

// Versions are ordered as version_compare orders Composer's normalised
// form; the spellings of one line are one version.
func TestVersionOrder(t *testing.T) {
	ascending := [][]string{
		{"0.0.3"},
		{"0.4.1", "v0.4.1", "V0.4.1.0", "0.4.1+build.5", "0.4.1-stable"},
		{"0.4.99"},
		{"0.4.100"},
		{"0.5.0-dev", "0.5.0.dev", "0.5-dev", "0.5.0dev"},
		{"0.5.0-alpha", "0.5.0-a", "0.5.0alpha"},
		{"0.5.0-alpha1", "0.5.0-a1", "0.5.0-alpha.1", "0.5.0.alpha-1"},
		{"0.5.0-beta1-dev"},
		{"0.5.0-beta1", "0.5.0-b1", "0.5.0-BETA1", "0.5.0_beta1"},
		{"0.5.0-beta1.1"},
		{"0.5.0-beta2"},
		{"0.5.0-RC1", "0.5.0-rc1", "0.5.0RC1"},
		{"0.5.0", "0.5", "0.5.0.0"},
		{"0.5.0-patch1", "0.5.0-p1", "0.5.0-pl1"},
		{"0.5.0.1"},
		{"0.5.1"},
		{"1.0.0"},
		{"10.0.0"},
	}
	parse := func(s string) Version {
		v, err := ParseVersion(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for i, same := range ascending {
		for _, a := range same {
			for j, others := range ascending {
				want := 0
				switch {
				case i < j:
					want = -1
				case i > j:
					want = 1
				}
				for _, b := range others {
					if got := parse(a).Compare(parse(b)); got != want {
						t.Errorf("%s against %s: %d, want %d", a, b, got, want)
					}
				}
			}
		}
	}
}

func TestParseVersion(t *testing.T) {
	for _, s := range []string{"", "foo", "1.0.0.0.0", "1.x", "1.0-STABLE", "1.0 as 2.0", "123456.0", "1.1234567890", "<1.0", "dev-", "dev-a b"} {
		if v, err := ParseVersion(s); err == nil {
			t.Errorf("%q was read as %v", s, v.parts)
		}
	}
	branch, err := ParseVersion("dev-feature/x")
	if err != nil || !branch.IsBranch() || branch.String() != "dev-feature/x" {
		t.Errorf("dev-feature/x: %v, %v", branch, err)
	}
}

// probe is the root package of a Composer project whose script asks
// Composer's own composer/semver, inside Composer's process, which of the
// versions satisfy each constraint.
var probe = map[string]string{
	"composer.json": `{"name": "phaenon/semver-probe", "autoload": {"classmap": ["Probe.php"]}, "scripts": {"probe": "Phaenon\\Probe::run"}}` + "\n",
	"Probe.php": `<?php

namespace Phaenon;

use Composer\Semver\Semver;

final class Probe
{
    public static function run(): void
    {
        $vectors = json_decode((string) file_get_contents((string) getenv('PHAENON_PROBE_VECTORS')), true, 16, JSON_THROW_ON_ERROR);
        $answers = [];
        foreach ($vectors['constraints'] as $vector) {
            $matches = [];
            try {
                foreach ($vectors['versions'] as $version) {
                    if (Semver::satisfies($version, $vector['constraint'])) {
                        $matches[] = $version;
                    }
                }
                $answers[] = ['matches' => $matches];
            } catch (\UnexpectedValueException $e) {
                $answers[] = ['refused' => true, 'matches' => []];
            }
        }
        file_put_contents((string) getenv('PHAENON_PROBE_ANSWERS'), json_encode($answers, JSON_THROW_ON_ERROR));
    }
}
`,
}

// Composer is the oracle of the vectors: each constraint is refused by it
// or satisfied by exactly the versions the file lists. PHAENON_UPDATE
// rewrites the file's answers from Composer's, keeping what it says this
// package does not read.
func TestComposerAgrees(t *testing.T) {
	composer := os.Getenv("PHAENON_COMPOSER")
	if composer == "" {
		t.Skip("PHAENON_COMPOSER names no composer binary")
	}
	v := loadVectors(t)
	dir := t.TempDir()
	for name, text := range probe {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	file, err := filepath.Abs(vectorsFile)
	if err != nil {
		t.Fatal(err)
	}
	answersFile := filepath.Join(dir, "answers.json")
	cmd := exec.Command(composer, "run-script", "probe")
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "COMPOSER") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env,
		"COMPOSER_HOME="+filepath.Join(dir, "home"),
		"COMPOSER_NO_INTERACTION=1",
		"COMPOSER_DISABLE_NETWORK=1",
		"COMPOSER_ROOT_VERSION=1.0.0",
		"PHAENON_PROBE_VECTORS="+file,
		"PHAENON_PROBE_ANSWERS="+answersFile,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("composer run-script probe: %v\n%s", err, out)
	}
	data, err := os.ReadFile(answersFile)
	if err != nil {
		t.Fatal(err)
	}
	var answers []vector
	if err := json.Unmarshal(data, &answers); err != nil {
		t.Fatal(err)
	}
	if len(answers) != len(v.Constraints) {
		t.Fatalf("%d answers for %d constraints", len(answers), len(v.Constraints))
	}
	if os.Getenv("PHAENON_UPDATE") != "" {
		for i, answer := range answers {
			c := &v.Constraints[i]
			c.Refused, c.Matches = answer.Refused, answer.Matches
			if c.Unread = c.Unread && !c.Refused; c.Unread {
				c.Matches = nil
			}
		}
		if err := os.WriteFile(vectorsFile, v.encode(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	for i, answer := range answers {
		c := v.Constraints[i]
		switch {
		case answer.Refused != c.Refused:
			t.Errorf("%q: Composer refuses it: %v; the file says %v", c.Constraint, answer.Refused, c.Refused)
		case !c.Unread && !slices.Equal(answer.Matches, c.Matches):
			t.Errorf("%q: Composer matches\n\t%v\nthe file says\n\t%v", c.Constraint, answer.Matches, c.Matches)
		}
	}
	if written, err := os.ReadFile(vectorsFile); err != nil || string(written) != string(v.encode()) {
		t.Errorf("%s is not laid out as PHAENON_UPDATE writes it (%v)", vectorsFile, err)
	}
}

// encode lays the file out a constraint to a line.
func (v vectors) encode() []byte {
	quote := func(value any) string {
		var text strings.Builder
		enc := json.NewEncoder(&text)
		enc.SetEscapeHTML(false) // < and > as they are written
		if err := enc.Encode(value); err != nil {
			panic(err)
		}
		return strings.ReplaceAll(strings.TrimSpace(text.String()), `","`, `", "`)
	}
	var b strings.Builder
	b.WriteString("{\n  \"versions\": " + quote(v.Versions) + ",\n  \"constraints\": [\n")
	for i, c := range v.Constraints {
		b.WriteString("    {\"constraint\": " + quote(c.Constraint))
		switch {
		case c.Refused:
			b.WriteString(`, "refused": true`)
		case c.Unread:
			b.WriteString(`, "unread": true`)
		case c.Matches == nil:
			b.WriteString(`, "matches": []`)
		default:
			b.WriteString(`, "matches": ` + quote(c.Matches))
		}
		b.WriteString("}")
		if i < len(v.Constraints)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("  ]\n}\n")
	return []byte(b.String())
}
