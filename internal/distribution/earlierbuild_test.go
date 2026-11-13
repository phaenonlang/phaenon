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
	Source      *string           `json:"installation-source"`
	Linked      bool              `json:"linked"`
	IsProject   bool              `json:"is-the-project"`
	Unreadable  []string          `json:"unreadable"`
	Unlisted    []string          `json:"unlisted"`
	Unsearch    []string          `json:"unsearchable"`
	Readonly    []string          `json:"readonly"`
	Permissions bool              `json:"permissions"`
	Folds       bool              `json:"folds"`
	Normalises  bool              `json:"normalises"`
	Said        []string          `json:"said"`
	Warned      []string          `json:"warned"`
	Again       []string          `json:"again"`
	Left        []string          `json:"left"`
	Errors      []string          `json:"errors"`
	Undecided   bool              `json:"undecided"`
	Compiler    string            `json:"compiler"`
	Plugin      string            `json:"plugin"`
}

// takesOneNameForAnother reports whether the file system of dir finds a
// file created as made under the name asked.
func takesOneNameForAnother(t *testing.T, dir, made, asked string) bool {
	t.Helper()
	writeFile(t, filepath.Join(dir, made), nil, 0o644)
	_, err := os.Lstat(filepath.Join(dir, asked))
	return err == nil
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
// package came whole, the clean-up of what earlier builds left, as
// Plugin::build calls the two. Its arguments are ProjectFile.php,
// EarlierBuild.php and then, for each package, where it is installed, what
// installed.json records it was installed from ("" for nothing), and the
// project's directory.
const earlierBuildScript = `<?php
require $argv[1];
require $argv[2];
$answers = [];
foreach (array_chunk(array_slice($argv, 3), 3) as [$dir, $source, $project]) {
    $file = Phaenon\Composer\ProjectFile::read($dir . '/phaenon.json');
    $errors = $file?->missingOutputs('acme/kit', '1.0.0', $dir);
    if ($file === null || $errors === null) {
        $answers[] = ['undecided' => true];
        continue;
    }
    $answer = ['errors' => $errors, 'said' => [], 'warned' => []];
    if ($errors === []) {
        foreach (Phaenon\Composer\EarlierBuild::clear($file, $dir, $source === '' ? null : $source, $project) as $build) {
            $kind = $build->isWarning() ? 'warned' : 'said';
            $answer[$kind] = [...$answer[$kind], ...$build->report('acme/kit 1.0.0', 'vendor/acme/kit')];
        }
    }
    $answers[] = $answer;
}
echo json_encode($answers, JSON_UNESCAPED_UNICODE);
`

// TestEarlierBuildsAgree holds the plugin's clean-up of what an earlier
// build left in a committed root to the compiler's reference,
// cmd/phn/testdata/earlier-builds.json, which cmd/phn's
// TestEarlierBuildVectors runs `phn build --vendor` over: the same lines,
// whole and in order, as information or as a warning, exactly the same
// paths left, and nothing removed where the check of presence fails or is
// left to phn, where no earlier build can have survived, or where phn
// refuses the project file. Where a vector says the plugin is unsure of a
// tree phn clears, it removes nothing and says nothing. A second run says
// nothing where the first cleared the root, and repeats the warning where
// it could not. It needs php, and no Composer.
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
	base := t.TempDir()
	folds := takesOneNameForAnother(t, base, "name", "NAME")
	normalises := takesOneNameForAnother(t, base, "Caf\u00e9", "Cafe\u0301")
	var vectors []earlierBuildVector
	for _, v := range reference.Vectors {
		switch {
		case v.Permissions && os.Getuid() == 0: // root reads and writes every directory
		case v.Folds && !folds, v.Normalises && !normalises:
		default:
			vectors = append(vectors, v)
		}
	}
	if len(vectors) == 0 {
		t.Fatal("no vectors")
	}
	t.Logf("%d vectors run (letter case folded: %v, normalisations folded: %v)", len(vectors), folds, normalises)
	// Each vector is the package as installed, in a directory of its own;
	// for a linked one that is a link to its directory.
	var dirs, installs []string
	var before [][]string
	for i, v := range vectors {
		dir := filepath.Join(base, strconv.Itoa(i), "acme", "kit")
		source := ""
		if v.Source != nil {
			source = *v.Source
		}
		switch {
		case v.Linked:
			// The path as an installer may give it, with a slash at its end.
			installs = append(installs, dir+"/", source, filepath.Dir(filepath.Dir(dir)))
		case v.IsProject:
			installs = append(installs, dir, source, dir)
		default:
			installs = append(installs, dir, source, filepath.Dir(filepath.Dir(dir)))
		}
		if v.Linked {
			dir = filepath.Join(base, strconv.Itoa(i), "checkout")
			if err := os.MkdirAll(filepath.Join(base, strconv.Itoa(i), "acme"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../checkout", filepath.Join(base, strconv.Itoa(i), "acme", "kit")); err != nil {
				t.Fatal(err)
			}
		}
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
		before = append(before, holds(t, dir))
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
	args := append([]string{script, filepath.Join(root, "composer", "src", "ProjectFile.php"), filepath.Join(root, "composer", "src", "EarlierBuild.php")}, installs...)

	for run, name := range []string{"the first run", "a second run"} {
		chmod(true)
		out, err := exec.Command(php, args...).Output()
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
			said, warned, left := v.Said, v.Warned, v.Left
			if run > 0 {
				said = nil
				if v.Again != nil {
					warned = v.Again
				}
			}
			if v.Plugin == "unsure" {
				said, warned, left = nil, nil, before[i]
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
			if got := holds(t, dirs[i]); !slices.Equal(got, left) {
				t.Errorf("%s, %s leaves\n\t%s\nwant\n\t%s", v.Name, name, strings.Join(got, "\n\t"), strings.Join(left, "\n\t"))
			}
		}
	}
}

// TestEarlierBuildUnderOpenBasedir: where PHP runs with open_basedir, a
// source it is not allowed to look at reads as one that does not exist, so
// the plugin removes nothing and says why. Here the source of an output is
// a link that leads out of the allowed directory, under a nested vendor
// directory, which the check of presence does not walk: a link it does
// meet and may not look at makes it leave the whole package to phn. The
// warning advises `phn build --vendor`, which is not PHP: run in the
// project, it clears the root, and the plugin then has nothing to say.
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
	project := filepath.Join(base, "allowed", "app")
	dir := filepath.Join(project, "vendor", "acme", "kit")
	for path, text := range map[string]string{
		"phaenon.json":                     `{"roots": {"src": ".src"}, "committed": ["src"]}`,
		".src/.phn-build":                  "phn 0.4.1\n",
		".src/vendor/data.json":            "{}\n",
		".src/Legacy.php":                  "<?php\n",
		"../../../../../outside/d.js":      "{}\n",
		"../../../composer.json":           `{"name": "acme/app"}`,
		"../../composer/installed.json":    `{"packages": [{"name": "acme/kit", "version": "1.0.0", "install-path": "../acme/kit", "installation-source": "source"}], "dev": true, "dev-package-names": []}`,
		"../../composer/autoload_psr4.php": "<?php\n\nreturn array();\n",
	} {
		writeFile(t, filepath.Join(dir, path), []byte(text), 0o644)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src", "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "outside", "d.js"), filepath.Join(dir, "src", "vendor", "data.json")); err != nil {
		t.Fatal(err)
	}
	before := holds(t, dir)
	script := filepath.Join(base, "allowed", "clear.php")
	writeFile(t, script, []byte(earlierBuildScript), 0o644)
	sources := filepath.Join(root, "composer", "src")
	type answer struct {
		Said   []string `json:"said"`
		Warned []string `json:"warned"`
	}
	confined := func() answer {
		t.Helper()
		// PHP warns of each path it refuses; the answer is what it prints.
		out, err := exec.Command(php, "-d", "display_errors=stderr", "-d", "open_basedir="+filepath.Join(base, "allowed")+string(os.PathListSeparator)+sources,
			script, filepath.Join(sources, "ProjectFile.php"), filepath.Join(sources, "EarlierBuild.php"), dir, "source", project).Output()
		if err != nil {
			t.Fatalf("php: %v\n%s", err, out)
		}
		var answers []answer
		if err := json.Unmarshal(out, &answers); err != nil || len(answers) != 1 {
			t.Fatalf("the plugin's answer (%v):\n%s", err, out)
		}
		return answers[0]
	}

	want := "acme/kit 1.0.0: .src/.phn-build is the stamp of a build, in a root the package commits, and nothing was removed: " +
		"PHP runs with open_basedir, under which a source it may not read looks like one that is gone. What a build left there may still load. " +
		"`phn build --vendor`, run in the project, is not bound by it and clears the directory"
	if got := confined(); !slices.Equal(got.Warned, []string{want}) || len(got.Said) != 0 {
		t.Errorf("the plugin's answer: %+v\nwant the warning\n%s", got, want)
	}
	if after := holds(t, dir); !slices.Equal(after, before) {
		t.Errorf("under open_basedir the package holds\n\t%s\nwant what it held\n\t%s", strings.Join(after, "\n\t"), strings.Join(before, "\n\t"))
	}

	// The advice, followed.
	phn := filepath.Join(base, "phn")
	build := exec.Command("go", "build", "-o", phn, "./cmd/phn")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/phn: %v\n%s", err, out)
	}
	clear := exec.Command(phn, "build", "--vendor")
	clear.Dir = project
	if out, err := clear.CombinedOutput(); err != nil || !strings.Contains(string(out), "  - removed .src/Legacy.php\n") {
		t.Fatalf("phn build --vendor in the project: %v\n%s", err, out)
	}
	for _, kept := range []string{".src/vendor/data.json", "src/vendor/data.json"} {
		if _, err := os.Lstat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("phn removed a file whose source it can see: %v", err)
		}
	}
	if got := confined(); len(got.Warned) != 0 || len(got.Said) != 0 {
		t.Errorf("after the advice was followed the plugin still speaks: %+v", got)
	}
}
