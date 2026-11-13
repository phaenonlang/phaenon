package distribution

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// earlierBuildVector is one tree of cmd/phn/testdata/earlier-builds.json.
type earlierBuildVector struct {
	Name        string            `json:"name"`
	Project     string            `json:"project"`
	Files       map[string]string `json:"files"`
	Dirs        []string          `json:"dirs"`
	Links       map[string]string `json:"links"`
	Unreadable  []string          `json:"unreadable"`
	Unlisted    []string          `json:"unlisted"`
	Unsearch    []string          `json:"unsearchable"`
	Readonly    []string          `json:"readonly"`
	Permissions bool              `json:"permissions"`
	Said        []string          `json:"said"`
	Warned      []string          `json:"warned"`
	Again       []string          `json:"again"`
	Left        []string          `json:"left"`
	Errors      []string          `json:"errors"`
	Undecided   bool              `json:"undecided"`
}

// modes are the permissions the vector asks for, by directory.
func (v earlierBuildVector) modes() map[string]fs.FileMode {
	modes := map[string]fs.FileMode{}
	for _, d := range v.Unreadable {
		modes[d] = 0
	}
	for _, d := range v.Unlisted {
		modes[d] = 0o311
	}
	for _, d := range v.Unsearch {
		modes[d] = 0o444
	}
	for _, d := range v.Readonly {
		modes[d] = 0o555
	}
	return modes
}

// holds lists every path under dir, sorted: a directory with a trailing
// slash, a symbolic link with a trailing @ and not looked through.
func holds(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		switch rel = filepath.ToSlash(rel); {
		case d.IsDir():
			paths = append(paths, rel+"/")
		case d.Type()&fs.ModeSymlink != 0:
			paths = append(paths, rel+"@")
		default:
			paths = append(paths, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return paths
}

// earlierBuildScript is the plugin's part of a dump for one installed
// package, without Composer: the check of presence, and after it, when the
// package came whole, the clean-up of what earlier builds left. Its
// arguments are ProjectFile.php, EarlierBuild.php and the packages.
const earlierBuildScript = `<?php
require $argv[1];
require $argv[2];
$answers = [];
foreach (array_slice($argv, 3) as $dir) {
    $file = Phaenon\Composer\ProjectFile::read($dir . '/phaenon.json');
    $errors = $file?->missingOutputs('acme/kit', '1.0.0', $dir);
    if ($file === null || $errors === null) {
        $answers[] = ['undecided' => true];
        continue;
    }
    $answer = ['errors' => $errors, 'said' => [], 'warned' => []];
    if ($errors === []) {
        foreach (Phaenon\Composer\EarlierBuild::clear($file, $dir) as $build) {
            $answer[$build->isWarning() ? 'warned' : 'said'][] = $build->report('acme/kit 1.0.0');
        }
    }
    $answers[] = $answer;
}
echo json_encode($answers);
`

// TestEarlierBuildsAgree holds the plugin's clean-up of what an earlier
// build left in a committed root to the compiler's reference,
// cmd/phn/testdata/earlier-builds.json, which cmd/phn's
// TestEarlierBuildVectors runs `phn build --vendor` over: the same lines,
// whole and in order, as information or as a warning, exactly the same
// paths left, and nothing removed where the check of presence fails or is
// left to phn. A second run says nothing where the first cleared the root,
// and repeats the warning where it could not. It needs php, and no
// Composer.
func TestEarlierBuildsAgree(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php on PATH")
	}
	root := repositoryRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "cmd", "phn", "testdata", "earlier-builds.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Vectors []earlierBuildVector `json:"vectors"`
	}
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	var vectors []earlierBuildVector
	for _, v := range reference.Vectors {
		if !v.Permissions || os.Getuid() != 0 { // root reads and writes every directory
			vectors = append(vectors, v)
		}
	}
	if len(vectors) == 0 {
		t.Fatal("no vectors")
	}
	// Each vector is the package as installed, in a directory of its own.
	base := t.TempDir()
	var dirs []string
	for i, v := range vectors {
		dir := filepath.Join(base, strconv.Itoa(i), "acme", "kit")
		dirs = append(dirs, dir)
		writeFile(t, filepath.Join(dir, "phaenon.json"), []byte(v.Project), 0o644)
		for path, text := range v.Files {
			writeFile(t, filepath.Join(dir, path), []byte(text), 0o644)
		}
		for _, d := range v.Dirs {
			if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for link, target := range v.Links {
			if err := os.Symlink(filepath.FromSlash(target), filepath.Join(dir, link)); err != nil {
				t.Fatal(err)
			}
		}
	}
	chmod := func(restrict bool) {
		for i, v := range vectors {
			for d, mode := range v.modes() {
				if !restrict {
					mode = 0o755
				}
				if err := os.Chmod(filepath.Join(dirs[i], d), mode); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	t.Cleanup(func() { chmod(false) })
	script := filepath.Join(t.TempDir(), "clear.php")
	writeFile(t, script, []byte(earlierBuildScript), 0o644)
	args := append([]string{script, filepath.Join(root, "composer", "src", "ProjectFile.php"), filepath.Join(root, "composer", "src", "EarlierBuild.php")}, dirs...)

	for run, name := range []string{"the first run", "a second run"} {
		chmod(true)
		out, err := exec.Command(php, args...).CombinedOutput()
		chmod(false)
		if err != nil {
			t.Fatalf("php: %v\n%s", err, out)
		}
		var answers []struct {
			Undecided bool     `json:"undecided"`
			Errors    []string `json:"errors"`
			Said      []string `json:"said"`
			Warned    []string `json:"warned"`
		}
		if err := json.Unmarshal(out, &answers); err != nil {
			t.Fatalf("the plugin's answers: %v\n%s", err, out)
		}
		if len(answers) != len(vectors) {
			t.Fatalf("%d answers for %d vectors", len(answers), len(vectors))
		}
		for i, v := range vectors {
			got := answers[i]
			said, warned := v.Said, v.Warned
			if run > 0 {
				said = nil
				if v.Again != nil {
					warned = v.Again
				}
			}
			switch {
			case got.Undecided != v.Undecided:
				t.Errorf("%s, %s: left to phn: %v, want %v", v.Name, name, got.Undecided, v.Undecided)
			case !slices.Equal(got.Errors, v.Errors):
				t.Errorf("%s, %s: errors\n\t%s\nwant\n\t%s", v.Name, name, strings.Join(got.Errors, "\n\t"), strings.Join(v.Errors, "\n\t"))
			case !slices.Equal(got.Said, said) || !slices.Equal(got.Warned, warned):
				t.Errorf("%s, %s: said\n\t%s\nwarned\n\t%s\nwant said\n\t%s\nwarned\n\t%s", v.Name, name,
					strings.Join(got.Said, "\n\t"), strings.Join(got.Warned, "\n\t"), strings.Join(said, "\n\t"), strings.Join(warned, "\n\t"))
			}
			if left := holds(t, dirs[i]); !slices.Equal(left, v.Left) {
				t.Errorf("%s, %s leaves\n\t%s\nwant\n\t%s", v.Name, name, strings.Join(left, "\n\t"), strings.Join(v.Left, "\n\t"))
			}
		}
	}
}

// TestEarlierBuildUnderOpenBasedir: where PHP runs with open_basedir, a
// source it is not allowed to look at reads as one that does not exist, so
// the plugin removes nothing and says why. Here the source of an output is
// a link that leads out of the allowed directory.
func TestEarlierBuildUnderOpenBasedir(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php on PATH")
	}
	root := repositoryRoot(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "allowed", "acme", "kit")
	for path, text := range map[string]string{
		"phaenon.json":             `{"roots": {"src": ".src"}, "committed": ["src"]}`,
		".src/.phn-build":          "phn 0.4.1\n",
		".src/data.json":           "{}\n",
		"../../../outside/data.js": "{}\n",
	} {
		writeFile(t, filepath.Join(dir, path), []byte(text), 0o644)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "outside", "data.js"), filepath.Join(dir, "src", "data.json")); err != nil {
		t.Fatal(err)
	}
	before := holds(t, dir)
	script := filepath.Join(base, "allowed", "clear.php")
	writeFile(t, script, []byte(earlierBuildScript), 0o644)
	sources := filepath.Join(root, "composer", "src")
	// PHP warns of each path it refuses; the answer is what it prints.
	out, err := exec.Command(php, "-d", "display_errors=stderr", "-d", "open_basedir="+filepath.Join(base, "allowed")+string(os.PathListSeparator)+sources,
		script, filepath.Join(sources, "ProjectFile.php"), filepath.Join(sources, "EarlierBuild.php"), dir).Output()
	if err != nil {
		t.Fatalf("php: %v\n%s", err, out)
	}
	want := "acme/kit 1.0.0: .src/.phn-build is the stamp of an earlier build of this package, in a root it now commits, and nothing was removed: " +
		"PHP runs with open_basedir, under which a source it may not read looks like one that is gone. What that build left there may still load"
	var answers []struct {
		Warned []string `json:"warned"`
	}
	if err := json.Unmarshal(out, &answers); err != nil || len(answers) != 1 || !slices.Equal(answers[0].Warned, []string{want}) {
		t.Errorf("the plugin's answer (%v):\n%s\nwant the warning\n%s", err, out, want)
	}
	if after := holds(t, dir); !slices.Equal(after, before) {
		t.Errorf("under open_basedir the package holds\n\t%s\nwant what it held\n\t%s", strings.Join(after, "\n\t"), strings.Join(before, "\n\t"))
	}
}
