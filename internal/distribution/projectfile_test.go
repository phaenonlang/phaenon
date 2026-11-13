package distribution

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestProjectFileReadersAgree holds the plugin's reader of phaenon.json to
// the compiler's reference, cmd/phn/testdata/project-files.json, which
// cmd/phn's TestProjectFileVectors holds the compiler to: the same roots
// and committed roots for a file both read, a refusal where the compiler
// refuses either key, and an answer where the compiler refuses the file for
// a key the plugin does not read. Of that last kind the plugin must also
// say that the compiler does not read the file, which is what it asks
// before it removes anything from a package: it may say so of a file the
// compiler reads, where the vector marks it, and of no other. It needs
// php, and no Composer.
func TestProjectFileReadersAgree(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php on PATH")
	}
	root := repositoryRoot(t)
	reference := filepath.Join(root, "cmd", "phn", "testdata", "project-files.json")
	data, err := os.ReadFile(reference)
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Vectors []struct {
			Name      string            `json:"name"`
			Refused   bool              `json:"refused"`
			Roots     map[string]string `json:"roots"`
			Committed []string          `json:"committed"`
			Compiler  string            `json:"compiler"`
			Strict    *bool             `json:"strict"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "read.php")
	writeFile(t, script, []byte(`<?php
require $argv[1];
$answers = [];
foreach (json_decode((string) file_get_contents($argv[2]), true)['vectors'] as $vector) {
    $file = Phaenon\Composer\ProjectFile::parse($vector['file']);
    $answers[] = $file === null ? null : ['roots' => (object) $file->roots, 'committed' => $file->committed, 'strict' => $file->strict];
}
echo json_encode($answers);
`), 0o644)
	out, err := exec.Command(php, script, filepath.Join(root, "composer", "src", "ProjectFile.php"), reference).CombinedOutput()
	if err != nil {
		t.Fatalf("php: %v\n%s", err, out)
	}
	var answers []*struct {
		Roots     map[string]string `json:"roots"`
		Committed []string          `json:"committed"`
		Strict    bool              `json:"strict"`
	}
	if err := json.Unmarshal(out, &answers); err != nil {
		t.Fatalf("the reader's answers: %v\n%s", err, out)
	}
	if len(answers) != len(vectors.Vectors) || len(answers) == 0 {
		t.Fatalf("%d answers for %d vectors", len(answers), len(vectors.Vectors))
	}
	for i, v := range vectors.Vectors {
		switch got := answers[i]; {
		case v.Refused && got != nil:
			t.Errorf("%s: read as roots %v, committed %v; the compiler refuses the file", v.Name, got.Roots, got.Committed)
		case v.Refused:
		case got == nil:
			t.Errorf("%s: refused; want roots %v, committed %v", v.Name, v.Roots, v.Committed)
		case !maps.Equal(got.Roots, v.Roots) || !slices.Equal(got.Committed, v.Committed):
			t.Errorf("%s: roots %v, committed %v; want %v, %v", v.Name, got.Roots, got.Committed, v.Roots, v.Committed)
		case got.Strict != (v.Compiler == "" && (v.Strict == nil || *v.Strict)):
			t.Errorf("%s: the plugin says the compiler reads the file: %v, and it does not; or that it does not, and nothing marks the file", v.Name, got.Strict)
		}
	}
}

// TestCommittedOutputChecksAgree holds the plugin's check of an installed
// package's committed roots to the compiler's reference,
// cmd/phn/testdata/committed-outputs.json, which cmd/phn's
// TestCommittedOutputVectors runs `phn build --vendor` over: the same
// errors, whole and in order, the same line after them, and the same answer
// to whether the install has a root to build. It needs php, and no
// Composer.
func TestCommittedOutputChecksAgree(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php on PATH")
	}
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "cmd", "phn", "testdata", "committed-outputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Summary map[string]string `json:"summary"`
		Vectors []struct {
			Name       string            `json:"name"`
			Project    string            `json:"project"`
			Files      map[string]string `json:"files"`
			Links      map[string]string `json:"links"`
			Unreadable []string          `json:"unreadable"`
			Errors     []string          `json:"errors"`
			Builds     bool              `json:"builds"`
			Undecided  bool              `json:"undecided"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Vectors) == 0 || len(vectors.Summary) == 0 {
		t.Fatal("no vectors")
	}
	// Each vector is the package as installed, in a directory of its own.
	base := t.TempDir()
	var dirs []string
	for i, v := range vectors.Vectors {
		dir := filepath.Join(base, strconv.Itoa(i), "acme", "kit")
		dirs = append(dirs, dir)
		writeFile(t, filepath.Join(dir, "phaenon.json"), []byte(v.Project), 0o644)
		for path, text := range v.Files {
			writeFile(t, filepath.Join(dir, path), []byte(text), 0o644)
		}
		for link, target := range v.Links {
			if err := os.Symlink(filepath.FromSlash(target), filepath.Join(dir, link)); err != nil {
				t.Fatal(err)
			}
		}
		for _, path := range v.Unreadable {
			if err := os.Chmod(filepath.Join(dir, path), 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(filepath.Join(dir, path), 0o755) })
		}
	}
	script := filepath.Join(t.TempDir(), "check.php")
	writeFile(t, script, []byte(`<?php
require $argv[1];
$answers = ['summary' => [], 'vectors' => []];
foreach (json_decode((string) $argv[2], true) as $count) {
    $answers['summary'][$count] = Phaenon\Composer\ProjectFile::summary((int) $count);
}
foreach (array_slice($argv, 3) as $dir) {
    $file = Phaenon\Composer\ProjectFile::read($dir . '/phaenon.json');
    $answers['vectors'][] = $file === null ? null : [
        'errors' => $file->missingOutputs('acme/kit', '1.0.0', $dir),
        'builds' => !$file->leavesNothingToBuild($dir),
    ];
}
echo json_encode($answers);
`), 0o644)
	counts, err := json.Marshal(slices.Sorted(maps.Keys(vectors.Summary)))
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{script, filepath.Join(root, "composer", "src", "ProjectFile.php"), string(counts)}, dirs...)
	out, err := exec.Command(php, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("php: %v\n%s", err, out)
	}
	var answers struct {
		Summary map[string]string `json:"summary"`
		Vectors []*struct {
			Errors *[]string `json:"errors"`
			Builds bool      `json:"builds"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(out, &answers); err != nil {
		t.Fatalf("the check's answers: %v\n%s", err, out)
	}
	if !maps.Equal(answers.Summary, vectors.Summary) {
		t.Errorf("the summaries:\n%v\nwant\n%v", answers.Summary, vectors.Summary)
	}
	if len(answers.Vectors) != len(vectors.Vectors) {
		t.Fatalf("%d answers for %d vectors", len(answers.Vectors), len(vectors.Vectors))
	}
	for i, v := range vectors.Vectors {
		got := answers.Vectors[i]
		switch {
		case got == nil:
			t.Errorf("%s: the plugin does not read the project file", v.Name)
		case v.Undecided && os.Getuid() == 0:
			// root reads every directory
		case v.Undecided:
			if got.Errors != nil {
				t.Errorf("%s: the plugin judged a tree it cannot read: %v", v.Name, *got.Errors)
			}
		case got.Errors == nil:
			t.Errorf("%s: the plugin left the package to phn; want the errors %v", v.Name, v.Errors)
		case !slices.Equal(*got.Errors, v.Errors):
			t.Errorf("%s: errors\n\t%s\nwant\n\t%s", v.Name, strings.Join(*got.Errors, "\n\t"), strings.Join(v.Errors, "\n\t"))
		case len(v.Errors) == 0 && got.Builds != v.Builds:
			t.Errorf("%s: a root to build: %v, want %v", v.Name, got.Builds, v.Builds)
		}
	}
}
