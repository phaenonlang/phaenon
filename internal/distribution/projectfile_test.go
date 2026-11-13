package distribution

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// TestProjectFileReadersAgree holds the plugin's reader of phaenon.json to
// the compiler's reference, cmd/phn/testdata/project-files.json, which
// cmd/phn's TestProjectFileVectors holds the compiler to: the same roots
// and committed roots for a file both read, a refusal where the compiler
// refuses either key, and an answer where the compiler refuses the file for
// a key the plugin does not read. It needs php, and no Composer.
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
    $answers[] = $file === null ? null : ['roots' => (object) $file->roots, 'committed' => $file->committed];
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
		}
	}
}
