package main

// What a build of an earlier release left in a root an installed package
// now commits (design/tooling.md §3.7, T18): `phn build --vendor` removes
// the files no source stands behind and the stamp, and nothing it is not
// sure of.

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// earlierBuildVector is one tree of testdata/earlier-builds.json, the
// reference the Composer plugin's clean-up is held to as well.
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

// write makes the vector's package in dir, but for the permissions.
func (v earlierBuildVector) write(t *testing.T, dir string) {
	t.Helper()
	writeTree(t, dir, map[string]string{ProjectFile: v.Project})
	writeTree(t, dir, v.Files)
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

// restrict sets the permissions the vector asks for, and returns what
// gives the directories back to the test.
func (v earlierBuildVector) restrict(t *testing.T, dir string) (restore func()) {
	t.Helper()
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
	restore = func() {
		for d := range modes {
			os.Chmod(filepath.Join(dir, d), 0o755)
		}
	}
	t.Cleanup(restore)
	for d, mode := range modes {
		if err := os.Chmod(filepath.Join(dir, d), mode); err != nil {
			t.Fatal(err)
		}
	}
	return restore
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
		rel, _ := filepath.Rel(dir, path)
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

func earlierBuildVectors(t *testing.T) []earlierBuildVector {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "earlier-builds.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors struct {
		Vectors []earlierBuildVector `json:"vectors"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	return vectors.Vectors
}

// The vectors, each as a package installed in a project of its own: `phn
// build --vendor` prints the lines, whole, and leaves exactly the paths
// listed; a second run finds no stamp and says nothing, or repeats its
// warning and removes nothing more.
func TestEarlierBuildVectors(t *testing.T) {
	bin := phnBinary(t)
	summary := committedGapsSummary(1)
	for _, v := range earlierBuildVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			if v.Permissions && os.Getuid() == 0 {
				t.Skip("root reads and writes every directory")
			}
			root := t.TempDir()
			kit := filepath.Join(root, "vendor/acme/kit")
			writeTree(t, root, map[string]string{
				"composer.json":                     `{"name": "acme/root", "require": {"php": ">=8.5", "acme/kit": "*"}}`,
				"vendor/composer/installed.json":    `{"packages": [{"name": "acme/kit", "version": "1.0.0", "install-path": "../acme/kit"}], "dev": true, "dev-package-names": []}`,
				"vendor/composer/autoload_psr4.php": "<?php\n\nreturn array();\n",
			})
			v.write(t, kit)
			warned := v.Warned
			for i, name := range []string{"the first run", "a second run"} {
				restore := v.restrict(t, kit)
				stdout, stderr, err := run(t, root, bin, "build", "--vendor")
				restore()

				// The lines before the summary of what was built.
				said := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
				said = said[:len(said)-1]
				var exit *exec.ExitError
				switch {
				case v.Undecided:
					if !asExit(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(stderr, "permission denied") || stdout != "" {
						t.Errorf("%s: %v, want exit status 1 and the directory's error\n%s%s", name, err, stdout, stderr)
					}
				case len(v.Errors) > 0:
					want := strings.Join(v.Errors, "\n") + "\n" + summary + "\n"
					if !asExit(err, &exit) || exit.ExitCode() != 1 || stderr != want || stdout != "" {
						t.Errorf("%s: %v\nstdout:\n%s\nstderr:\n%s\nwant exit status 1 and on stderr\n%s", name, err, stdout, stderr, want)
					}
				default:
					want := ""
					for _, line := range warned {
						want += "warning: " + line + "\n"
					}
					wantSaid := v.Said
					if i > 0 {
						wantSaid = nil
					}
					if err != nil || stderr != want || !slices.Equal(said, wantSaid) {
						t.Errorf("%s: %v\nstdout:\n%s\nstderr:\n%s\nwant on stdout, before the summary,\n%s\nand on stderr\n%s", name, err, stdout, stderr, strings.Join(wantSaid, "\n"), want)
					}
				}
				if got := holds(t, kit); !slices.Equal(got, v.Left) {
					t.Errorf("%s leaves\n\t%s\nwant\n\t%s", name, strings.Join(got, "\n\t"), strings.Join(v.Left, "\n\t"))
				}
				if v.Again != nil {
					warned = v.Again
				}
			}
		})
	}
}

// A package whose stamp is cleared beside a package that is built: the
// line comes before the build, and the root the package does not commit is
// built as before, its own stamp written and left.
func TestBuildVendorClearsAnEarlierBuild(t *testing.T) {
	bin := phnBinary(t)
	root := committedTree(t)
	kit := filepath.Join(root, "vendor/acme/kit")
	writeTree(t, kit, map[string]string{
		".src/" + stampFile: "phn 0.4.1\n",
		".src/Legacy.php":   "<?php\n",
		"tests/BoxTest.phn": "namespace Acme\\Kit\\Tests;\n\nuse Acme\\Kit\\Box;\n\nfunction unbox(Box<int> $b): int => $b->get();\n",
	})
	shipped := snapshot(t, filepath.Join(kit, ".src"))
	// committedTree's release ships a file no source stands behind: with
	// the stamp there it goes too, as it would from the library's own
	// checkout at its next build.
	for _, gone := range []string{stampFile, "Legacy.php", "Orphan.php"} {
		delete(shipped, gone)
	}

	stdout, stderr, err := run(t, root, bin, "build", "--vendor")

	if err != nil {
		t.Fatalf("phn build --vendor: %v\n%s%s", err, stdout, stderr)
	}
	want := "acme/kit 1.0.0: .src/.phn-build is the stamp of an earlier build of this package, in a root it now commits: removed 2 files with no source behind them (.src/Legacy.php, and 1 more) and the stamp\n"
	if !strings.HasPrefix(stdout, want) || !strings.HasSuffix(stdout, "2 installed packages built, 0 up to date\n") {
		t.Errorf("stdout:\n%s\nwant first\n%s", stdout, want)
	}
	expectSnapshot(t, "acme/kit's committed root", filepath.Join(kit, ".src"), shipped)
	if _, err := os.Stat(filepath.Join(kit, ".tests", stampFile)); err != nil {
		t.Errorf("the stamp of the root that is built: %v", err)
	}

	stdout, _, err = run(t, root, bin, "build", "--vendor")
	if err != nil || stdout != "0 installed packages built, 2 up to date\n" {
		t.Errorf("a second build should say nothing of an earlier one (%v):\n%s", err, stdout)
	}
	expectSnapshot(t, "acme/kit's committed root", filepath.Join(kit, ".src"), shipped)
}
