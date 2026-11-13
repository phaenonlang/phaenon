package distribution

// A library that shipped its sources alone and was built where it is
// installed (design/roadmap.md D13), upgraded to a release that commits
// its outputs (D28), under a real Composer: what the earlier build wrote
// is in the way of nothing when the upgrade replaces the directory, stays
// beside the committed files when the upgrade is a git checkout, and stops
// the checkout when the earlier release did not ignore its outputs.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// newReleases is acme/kit as a git repository with two tags. 1.0.0 ships
// its sources alone: a class Greeter, a class Legacy and a file old.txt,
// and ignores its outputs when ignored is set, as a library of D13 does.
// 2.0.0 commits its outputs, built by the real phn, and has dropped Legacy
// and old.txt. The library has one root, so that an install of 2.0.0 has
// nothing to build.
func (w *world) newReleases(name string, ignored bool) *kit {
	w.t.Helper()
	return w.newReleasesWith(name, ignored, nil, nil)
}

// newReleasesWith is newReleases with 2.0.0 shaped by the test: prepare
// changes the tree before the real phn builds it, finish after, and both
// before the commit.
func (w *world) newReleasesWith(name string, ignored bool, prepare, finish func(dir string)) *kit {
	w.t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		w.t.Skip("no git on PATH")
	}
	k := &kit{w: w, dir: filepath.Join(w.base, "kits", name)}
	manifest := func(version string) string {
		return `{"name": "acme/kit", "version": "` + version + `", "require": {"php": ">=8.5"}, "autoload": {"psr-4": {"Acme\\Kit\\": ".src/"}}}` + "\n"
	}
	greeter := func(version string) string {
		return "namespace Acme\\Kit;\n\nfinal class Greeter\n{\n    greet(): string => 'Hello from " + version + "';\n}\n"
	}
	ignore := "/vendor/\n"
	if ignored {
		ignore = "/.src/\n/vendor/\n"
	}
	for path, text := range map[string]string{
		"composer.json":   manifest("1.0.0"),
		"phaenon.json":    `{"roots": {"src": ".src"}}` + "\n",
		".gitignore":      ignore,
		"src/Greeter.phn": greeter("1.0.0"),
		"src/Legacy.phn":  "namespace Acme\\Kit;\n\nfinal class Legacy\n{\n}\n",
		"src/old.txt":     "old\n",
	} {
		writeFile(w.t, filepath.Join(k.dir, path), []byte(text), 0o644)
	}
	k.git("init", "--quiet")
	k.git("add", "--all")
	k.git("commit", "--quiet", "--message", "acme/kit 1.0.0")
	k.git("tag", "1.0.0")

	k.git("rm", "--quiet", "src/Legacy.phn", "src/old.txt")
	for path, text := range map[string]string{
		"composer.json":   manifest("2.0.0"),
		"phaenon.json":    `{"roots": {"src": ".src"}, "committed": ["src"]}` + "\n",
		".gitignore":      ".phn-build\n/vendor/\n",
		".gitattributes":  "/.src/** linguist-generated\n",
		"src/Greeter.phn": greeter("2.0.0"),
	} {
		writeFile(w.t, filepath.Join(k.dir, path), []byte(text), 0o644)
	}
	if prepare != nil {
		prepare(k.dir)
	}
	k.build()
	if finish != nil {
		finish(k.dir)
	}
	k.git("add", "--all")
	k.git("commit", "--quiet", "--message", "acme/kit 2.0.0")
	k.git("tag", "2.0.0")
	if tracked := k.git("ls-files", ".src"); prepare == nil && finish == nil && tracked != ".src/Greeter.php\n" {
		w.t.Fatalf("2.0.0 tracks under .src/:\n%s", tracked)
	}
	return k
}

// build builds the library as its own project is built, with the real phn,
// and returns what phn printed.
func (k *kit) build(args ...string) string {
	k.w.t.Helper()
	build := exec.Command(k.w.buildPhn(), append([]string{"build"}, args...)...)
	build.Dir = k.dir
	out, err := build.CombinedOutput()
	if err != nil {
		k.w.t.Fatalf("phn build %s in the library: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// path is the library's directory as a path repository of 2.0.0, linked
// into the project's vendor directory or copied there.
func (k *kit) path(link bool) dependency {
	return dependency{name: "acme/kit", constraint: "*", repository: pathRepository(k.dir, "acme/kit", "2.0.0", link)}
}

// internals gives the library a directory of sources that its archive
// leaves out, while it ships their outputs: the one inconsistency a
// release can have that the check of presence does not object to.
func internals(dir string) {
	for path, text := range map[string]string{
		"src/Internal/Debug.phn": "namespace Acme\\Kit\\Internal;\n\nfinal class Debug\n{\n}\n",
		".gitattributes":         "/.src/** linguist-generated\n/src/Internal export-ignore\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0o644); err != nil {
			panic(err)
		}
	}
}

// stamped leaves in the library's committed root the stamp of a build of
// its checkout as an installed package, made before the root was
// committed.
func stamped(dir string) {
	if err := os.WriteFile(filepath.Join(dir, ".src", ".phn-build"), []byte("phn 0.4.1\n"), 0o644); err != nil {
		panic(err)
	}
}

// repository is the library's git repository as Composer reads it: every
// tag a release, installed as a checkout by a project that prefers sources.
func (k *kit) repository(constraint string) dependency {
	return dependency{name: "acme/kit", constraint: constraint, repository: map[string]any{"type": "vcs", "url": k.dir}}
}

// archives are the library's two releases as dist archives, served by an
// artifact repository.
func (k *kit) archives(constraint string) dependency {
	k.w.t.Helper()
	dir := k.dir + "-artifacts"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		k.w.t.Fatal(err)
	}
	for _, tag := range []string{"1.0.0", "2.0.0"} {
		k.git("archive", "--format=zip", "--output", filepath.Join(dir, "acme-kit-"+tag+".zip"), tag)
	}
	return dependency{name: "acme/kit", constraint: constraint, repository: map[string]any{"type": "artifact", "url": dir}}
}

// require changes the version of acme/kit the project asks for.
func (p *project) require(constraint string) {
	p.w.t.Helper()
	for i := range p.dependencies {
		if p.dependencies[i].name == "acme/kit" {
			p.dependencies[i].constraint = constraint
		}
	}
	p.write()
}

// vendored runs git in the project's checkout of acme/kit.
func (p *project) vendored(args ...string) string {
	p.w.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = filepath.Join(p.dir, "vendor", "acme", "kit")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		p.w.t.Fatalf("git %s in vendor/acme/kit: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// expectKit fails the test unless the project's acme/kit, loaded through
// Composer's autoloader, greets as the release does, and has the class
// Legacy or not.
func (p *project) expectKit(release string, legacy bool) {
	t := p.w.t
	t.Helper()
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("no php on PATH")
	}
	writeFile(t, filepath.Join(p.dir, "kit.php"), []byte(`<?php
require 'vendor/autoload.php';
echo (new Acme\Kit\Greeter())->greet(), class_exists(Acme\Kit\Legacy::class) ? ', with Legacy' : ', without Legacy', "\n";
`), 0o644)
	run := exec.Command(php, "kit.php")
	run.Dir = p.dir
	out, err := run.CombinedOutput()
	want := "Hello from " + release + ", without Legacy\n"
	if legacy {
		want = "Hello from " + release + ", with Legacy\n"
	}
	if err != nil || string(out) != want {
		t.Errorf("php kit.php: %v\n%s\nwant\n%s", err, out, want)
	}
}

// installBuilt installs 1.0.0 with the real phn, which builds it where it
// is installed, and checks what that build left in the package.
func (p *project) installBuilt() {
	t := p.w.t
	t.Helper()
	p.binary = p.w.buildPhn()
	if out := p.mustComposer("install"); !strings.Contains(out, "acme/kit: 2 files compiled, 2 written\n") {
		t.Fatalf("1.0.0 was not built where it is installed:\n%s", out)
	}
	for _, f := range []string{".src/Greeter.php", ".src/Legacy.php", ".src/old.txt", ".src/.phn-build"} {
		if _, err := os.Stat(filepath.Join(p.dir, "vendor", "acme", "kit", f)); err != nil {
			t.Fatalf("the build of 1.0.0: %v", err)
		}
	}
	p.expectKit("1.0.0", true)
	// From here on a compiler that is fetched or run fails the command:
	// no release is pinned, and PHN_BINARY names none.
	p.binary = ""
}

// TestSourceInstallUpgradedToCommittedOutputs: the upgrade the clean-up is
// for. A source install is a git checkout: git writes what 2.0.0 tracks
// over the earlier build's outputs, which 1.0.0 ignored, and leaves the
// rest of that build beside them, untracked: the output of the class the
// release dropped, the copy of a file it dropped, and the stamp. The
// plugin removes them at the dump of the same `composer update`, with no
// compiler, naming each file, and the checkout is then exactly the
// release: the dropped class does not load, git finds nothing to report,
// ignored files included, and the next dump says nothing.
func TestSourceInstallUpgradedToCommittedOutputs(t *testing.T) {
	w := newWorld(t, "")
	k := w.newReleases("kit", true)
	p := w.newProject("app", project{dependencies: []dependency{k.repository("^1.0")}, config: map[string]any{"preferred-install": "source"}})
	p.installBuilt()
	if status := p.vendored("status", "--porcelain", "--ignored"); status != "!! .src/\n" {
		t.Fatalf("the checkout of 1.0.0 after its build:\n%s", status)
	}

	p.require("^2.0")
	out := p.mustComposer("update")

	want := earlierBuildLine("2.0.0") + "removed it and 2 files with no source behind them\n  - removed .src/Legacy.php\n  - removed .src/old.txt\n"
	if !strings.Contains(out, want) {
		t.Errorf("composer's output:\n%s\nwant the line, and each file named:\n%s", out, want)
	}
	if tag := p.vendored("describe", "--tags", "--exact-match"); tag != "2.0.0\n" {
		t.Errorf("the checkout is at %s", tag)
	}
	if status := p.vendored("status", "--porcelain", "--ignored"); status != "" {
		t.Errorf("the checkout of 2.0.0 is not the release:\n%s", status)
	}
	p.expectKit("2.0.0", false)
	w.expectRuns()
	expectAbsent(t, p.installed())

	if out := p.mustComposer("dump-autoload"); strings.Contains(out, "phaenon/phn:") {
		t.Errorf("the dump after the clean-up should say nothing:\n%s", out)
	}
	if status := p.vendored("status", "--porcelain", "--ignored"); status != "" {
		t.Errorf("the checkout after a second dump:\n%s", status)
	}
}

// TestSourceUpgradeClearedByHand: the same upgrade made without the plugin
// (`--no-plugins`) leaves what the earlier build wrote, and the dropped
// class loads. `phn build --vendor`, run by hand, removes it with the
// plugin's line, and says nothing the second time.
func TestSourceUpgradeClearedByHand(t *testing.T) {
	w := newWorld(t, "")
	k := w.newReleases("kit", true)
	p := w.newProject("app", project{dependencies: []dependency{k.repository("^1.0")}, config: map[string]any{"preferred-install": "source"}})
	p.installBuilt()

	p.require("^2.0")
	p.mustComposer("update", "--no-plugins")

	if status := p.vendored("status", "--porcelain", "--ignored"); status != "?? .src/Legacy.php\n?? .src/old.txt\n!! .src/.phn-build\n" {
		t.Errorf("the checkout of 2.0.0 before the clean-up:\n%s", status)
	}
	p.expectKit("2.0.0", true)

	stdout, stderr, err := p.phn("build", "--vendor")
	want := strings.TrimPrefix(earlierBuildLine("2.0.0"), "phaenon/phn: ") + "removed it and 2 files with no source behind them\n  - removed .src/Legacy.php\n  - removed .src/old.txt\n" +
		"0 installed packages built, 0 up to date, 1 left as installed with their committed outputs\n"
	if err != nil || stdout != want || stderr != "" {
		t.Errorf("phn build --vendor: %v\n%s%s\nwant\n%s", err, stdout, stderr, want)
	}
	if status := p.vendored("status", "--porcelain", "--ignored"); status != "" {
		t.Errorf("the checkout of 2.0.0 is not the release:\n%s", status)
	}
	p.mustComposer("dump-autoload", "--no-plugins")
	p.expectKit("2.0.0", false)

	stdout, stderr, err = p.phn("build", "--vendor")
	if want := "0 installed packages built, 0 up to date, 1 left as installed with their committed outputs\n"; err != nil || stdout != want || stderr != "" {
		t.Errorf("a second phn build --vendor: %v\n%s%s\nwant\n%s", err, stdout, stderr, want)
	}
}

// TestSourceUpgradeGitRefuses: where 1.0.0 did not ignore its outputs, the
// files its build wrote are untracked files that 2.0.0's checkout would
// overwrite, and git refuses the checkout. Composer's update fails before
// any dump, so the plugin is never asked: the package stays at 1.0.0 with
// its build, and what clears the way is removing the package's directory.
// A library of D13 ignores its outputs (D12), so this is a release that
// was already out of step, and nothing here can reach it.
func TestSourceUpgradeGitRefuses(t *testing.T) {
	w := newWorld(t, "")
	k := w.newReleases("kit", false)
	p := w.newProject("app", project{dependencies: []dependency{k.repository("^1.0")}, config: map[string]any{"preferred-install": "source"}})
	p.installBuilt()
	if status := p.vendored("status", "--porcelain", "--ignored"); status != "?? .src/\n" {
		t.Fatalf("the checkout of 1.0.0 after its build:\n%s", status)
	}

	p.require("^2.0")
	out, err := p.composer("update")

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("composer update: %v, want it to fail on git's checkout\n%s", err, out)
	}
	if strings.Contains(out, "phaenon/phn: acme/kit") {
		t.Errorf("the plugin spoke of a package Composer could not update:\n%s", out)
	}
	if tag := p.vendored("describe", "--tags", "--exact-match"); tag != "1.0.0\n" {
		t.Errorf("the checkout is at %s", tag)
	}
	for _, f := range []string{".src/Greeter.php", ".src/Legacy.php", ".src/old.txt", ".src/.phn-build"} {
		if _, err := os.Stat(filepath.Join(p.dir, "vendor", "acme", "kit", f)); err != nil {
			t.Errorf("the build of 1.0.0 after the failed update: %v", err)
		}
	}

	// The way out: the package's directory removed, and installed anew.
	if err := os.RemoveAll(filepath.Join(p.dir, "vendor", "acme", "kit")); err != nil {
		t.Fatal(err)
	}
	if out := p.mustComposer("install"); strings.Contains(out, "phaenon/phn:") {
		t.Errorf("a fresh checkout of 2.0.0 has nothing for the plugin to say:\n%s", out)
	}
	if status := p.vendored("status", "--porcelain", "--ignored"); status != "" {
		t.Errorf("the fresh checkout of 2.0.0:\n%s", status)
	}
	p.expectKit("2.0.0", false)
}

// TestDistUpgradeToCommittedOutputs: an upgrade from an archive replaces
// the package's directory, the earlier build with it: no stamp is left,
// the plugin has nothing to remove and says nothing.
func TestDistUpgradeToCommittedOutputs(t *testing.T) {
	w := newWorld(t, "")
	k := w.newReleases("kit", true)
	p := w.newProject("app", project{dependencies: []dependency{k.archives("^1.0")}})
	p.installBuilt()

	p.require("^2.0")
	out := p.mustComposer("update")

	if strings.Contains(out, "phaenon/phn:") {
		t.Errorf("the plugin spoke with nothing to build and nothing to remove:\n%s", out)
	}
	want := []string{".gitattributes", ".gitignore", ".src/", ".src/Greeter.php", "composer.json", "phaenon.json", "src/", "src/Greeter.phn"}
	if got := holds(t, filepath.Join(p.dir, "vendor", "acme", "kit")); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("vendor/acme/kit holds\n\t%s\nwant the archive of 2.0.0\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
	p.expectKit("2.0.0", false)
	w.expectRuns()
	expectAbsent(t, p.installed())
}

// TestArchiveThatShipsAStamp: a stamp is an earlier build's only where
// such a build can have survived. This release tracks a stamp and leaves
// the sources of one directory out of its archive while it ships their
// outputs. Installed from the archive, into a directory Composer emptied,
// nothing was ever built there: the plugin removes nothing and says
// nothing, the package is byte for byte its archive, as it is for a
// project without the plugin, and phn run by hand does the same.
func TestArchiveThatShipsAStamp(t *testing.T) {
	w := newWorld(t, "")
	k := w.newReleasesWith("kit", true, func(dir string) {
		internals(dir)
		writeFile(t, filepath.Join(dir, ".gitignore"), []byte("/vendor/\n"), 0o644) // the entry for the stamp forgotten
	}, stamped)
	if tracked := k.git("ls-files", ".src"); tracked != ".src/.phn-build\n.src/Greeter.php\n.src/Internal/Debug.php\n" {
		t.Fatalf("2.0.0 tracks under .src/:\n%s", tracked)
	}
	p := w.newProject("app", project{dependencies: []dependency{k.archives("2.0.0")}})

	out := p.mustComposer("install")

	if strings.Contains(out, "phaenon/phn:") {
		t.Errorf("the plugin spoke of an archive's stamp:\n%s", out)
	}
	want := []string{".gitattributes", ".gitignore", ".src/", ".src/.phn-build", ".src/Greeter.php", ".src/Internal/", ".src/Internal/Debug.php", "composer.json", "phaenon.json", "src/", "src/Greeter.phn"}
	installed := filepath.Join(p.dir, "vendor", "acme", "kit")
	if got := holds(t, installed); !slices.Equal(got, want) {
		t.Errorf("vendor/acme/kit holds\n\t%s\nwant the archive of 2.0.0\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
	w.expectRuns()
	expectAbsent(t, p.installed())

	stdout, stderr, err := p.phn("build", "--vendor")
	if err != nil || stdout != "0 installed packages built, 0 up to date, 1 left as installed with their committed outputs\n" || stderr != "" {
		t.Errorf("phn build --vendor: %v\n%s%s", err, stdout, stderr)
	}
	if got := holds(t, installed); !slices.Equal(got, want) {
		t.Errorf("after phn build --vendor vendor/acme/kit holds\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}

// TestCopiedCheckoutThatHoldsAStamp: the library is as the design has it,
// its .gitignore lists the stamp, and its checkout holds one, ignored: it
// was once built as a linked package. Composer's copy of a path repository
// leaves out what the archive leaves out and takes what git ignores, so
// the copy has the stamp and the outputs of sources it lacks. No build was
// made in the copy: nothing is removed and nothing said, by the plugin or
// by phn.
func TestCopiedCheckoutThatHoldsAStamp(t *testing.T) {
	w := newWorld(t, "")
	k := w.newReleasesWith("kit", true, internals, nil)
	stamped(k.dir)
	if status := k.git("status", "--porcelain", "--ignored"); status != "!! .src/.phn-build\n" {
		t.Fatalf("the library's checkout:\n%s", status)
	}
	p := w.newProject("app", project{dependencies: []dependency{k.path(false)}})

	out := p.mustComposer("install")

	if strings.Contains(out, "phaenon/phn:") {
		t.Errorf("the plugin spoke of a stamp Composer copied:\n%s", out)
	}
	installed := filepath.Join(p.dir, "vendor", "acme", "kit")
	for _, f := range []string{".src/.phn-build", ".src/Internal/Debug.php", ".src/Greeter.php"} {
		if _, err := os.Stat(filepath.Join(installed, f)); err != nil {
			t.Errorf("the copy: %v", err)
		}
	}
	expectAbsent(t, filepath.Join(installed, "src", "Internal"))
	w.expectRuns()
	expectAbsent(t, p.installed())

	if stdout, stderr, err := p.phn("build", "--vendor"); err != nil || strings.Contains(stdout+stderr, "acme/kit") {
		t.Errorf("phn build --vendor (%v) spoke of acme/kit:\n%s%s", err, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(installed, ".src", "Internal", "Debug.php")); err != nil {
		t.Errorf("after phn build --vendor: %v", err)
	}
}

// TestLinkedCheckoutIsCleared: a path repository linked to a checkout is
// the other place an earlier build survives, since Composer never empties
// it. The plugin clears it without a compiler, and the files removed are
// the ones the library's own build removes: `phn build --check` there
// reports the same file before, and nothing after. Under --quiet the
// removal is made and nothing is printed. `dump-autoload --dry-run` does
// what a dump does: Composer sends the event without saying that it is a
// dry run, so the project is built and the package cleared.
func TestLinkedCheckoutIsCleared(t *testing.T) {
	w := newWorld(t, "")
	k := w.newReleases("kit", true)
	leave := func() {
		t.Helper()
		stamped(k.dir)
		writeFile(t, filepath.Join(k.dir, ".src", "Legacy.php"), []byte("<?php\n"), 0o644)
	}
	leave()
	// With the stamp in a root it commits, the library's own build would
	// remove both: the file that has no source and the stamp.
	check := exec.Command(w.buildPhn(), "build", "--check")
	check.Dir = k.dir
	if found, err := check.CombinedOutput(); err == nil || !strings.Contains(string(found), ".src/Legacy.php: no source behind it\n") ||
		!strings.Contains(string(found), ".src/.phn-build: no source behind it\n") || strings.Count(string(found), "no source behind it") != 2 {
		t.Fatalf("phn build --check in the library (%v):\n%s", err, found)
	}
	p := w.newProject("app", project{phaenonJSON: true, dependencies: []dependency{k.path(true)}, override: true})

	out := p.mustComposer("install")

	want := earlierBuildLine("2.0.0") + "removed it and 1 file with no source behind it\n  - removed .src/Legacy.php\n"
	if !strings.Contains(out, want) || strings.Count(out, "is the stamp of a build") != 1 {
		t.Errorf("composer's output:\n%s\nwant, once,\n%s", out, want)
	}
	if status := k.git("status", "--porcelain", "--ignored", "--", ".src"); status != "" {
		t.Errorf("the library's .src/ is not as committed:\n%s", status)
	}
	k.build("--check")
	w.expectRuns(run{p.dir, []string{"build"}})

	leave()
	if out := p.mustComposer("dump-autoload", "--quiet"); out != "" {
		t.Errorf("composer dump-autoload --quiet printed:\n%s", out)
	}
	if status := k.git("status", "--porcelain", "--ignored", "--", ".src"); status != "" {
		t.Errorf("under --quiet the library's .src/ was not cleared:\n%s", status)
	}

	leave()
	out = p.mustComposer("dump-autoload", "--dry-run")
	if !strings.Contains(out, want) {
		t.Errorf("composer dump-autoload --dry-run:\n%s\nwant what a dump prints:\n%s", out, want)
	}
	if status := k.git("status", "--porcelain", "--ignored", "--", ".src"); status != "" {
		t.Errorf("in a dry run the library's .src/ was not cleared, though the project was built:\n%s", status)
	}
	w.expectRuns(run{p.dir, []string{"build"}}, run{p.dir, []string{"build"}}, run{p.dir, []string{"build"}})
}

// TestOneDumpSaysItOnce: a package whose stamp cannot be acted on, here
// for a committed root the checkout lacks, is warned of once in a dump, by
// the plugin, whether or not the dump goes on to build an installed
// package: the phn it then runs is told that the plugin has been there,
// and says nothing of it. The library's own build does not look at the
// output directory of a root it lacks, so the warning advises clearing it
// by hand; with that done the next dump says nothing.
func TestOneDumpSaysItOnce(t *testing.T) {
	w := newWorld(t, "")
	k := w.newReleasesWith("kit", true, func(dir string) {
		writeFile(t, filepath.Join(dir, "phaenon.json"), []byte(`{"roots": {"src": ".src", "gen": ".gen"}, "committed": ["src", "gen"]}`+"\n"), 0o644)
	}, nil)
	writeFile(t, filepath.Join(k.dir, ".gen", ".phn-build"), []byte("phn 0.4.1\n"), 0o644)
	writeFile(t, filepath.Join(k.dir, ".gen", "Old.php"), []byte("<?php\n"), 0o644)
	const warning = "phaenon/phn: acme/kit 2.0.0: .gen/.phn-build is the stamp of a build, in a root the package commits, and nothing was removed: " +
		"the package is installed without gen. What a build left there may still load. " +
		"The package is a link to a checkout, where `phn build` does not get to this directory: remove .gen/.phn-build there by hand, and what a build left beside it\n"

	alone := w.newProject("alone", project{dependencies: []dependency{k.path(true)}})
	out := alone.mustComposer("install")
	if !strings.Contains(out, warning) || strings.Count(out, "is the stamp of a build") != 1 {
		t.Errorf("with nothing to build, composer's output:\n%s\nwant the plugin's warning, once:\n%s", out, warning)
	}
	w.expectRuns()
	expectAbsent(t, alone.installed())

	beside := w.newProject("beside", project{dependencies: []dependency{k.path(true)}, libraries: []string{"acme/lib"}, binary: w.buildPhn()})
	for _, command := range []string{"install", "dump-autoload"} {
		out = beside.mustComposer(command)
		if !strings.Contains(out, warning) || strings.Count(out, "is the stamp of a build") != 1 || !strings.Contains(out, "> phn build --vendor\n") {
			t.Errorf("with a package to build, composer %s:\n%s\nwant the plugin's warning, once, and phn run:\n%s", command, out, warning)
		}
	}
	for _, f := range []string{".phn-build", "Old.php"} {
		if _, err := os.Stat(filepath.Join(k.dir, ".gen", f)); err != nil {
			t.Errorf("nothing was to be removed: %v", err)
		}
	}

	// The library's own build leaves the directory as it is, as the
	// warning says: what it advises is the way.
	k.build()
	if _, err := os.Stat(filepath.Join(k.dir, ".gen", ".phn-build")); err != nil {
		t.Errorf("phn build in the library looked at .gen after all: %v", err)
	}
	for _, f := range []string{".phn-build", "Old.php"} {
		if err := os.Remove(filepath.Join(k.dir, ".gen", f)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []*project{alone, beside} {
		if out := p.mustComposer("dump-autoload"); strings.Contains(out, "is the stamp of a build") {
			t.Errorf("after the advice was followed, composer dump-autoload in %s:\n%s", filepath.Base(p.dir), out)
		}
	}
}

// TestLinkedWarningSaysWhatToDo: in a linked checkout a link among the
// sources that leads nowhere proves nothing of the file behind an output,
// and nothing is removed. The library's own build fails on the same link,
// so the warning advises putting it right and running `phn build` there:
// that removes what a build left and the stamp, and the next dump says
// nothing.
func TestLinkedWarningSaysWhatToDo(t *testing.T) {
	w := newWorld(t, "")
	k := w.newReleases("kit", true)
	stamped(k.dir)
	writeFile(t, filepath.Join(k.dir, ".src", "Legacy.php"), []byte("<?php\n"), 0o644)
	writeFile(t, filepath.Join(k.dir, ".src", "notes.txt"), []byte("copied when the link led somewhere\n"), 0o644)
	if err := os.Symlink("../nowhere", filepath.Join(k.dir, "src", "notes.txt")); err != nil {
		t.Fatal(err)
	}
	p := w.newProject("app", project{dependencies: []dependency{k.path(true)}})

	out := p.mustComposer("install")

	want := "phaenon/phn: acme/kit 2.0.0: .src/.phn-build is the stamp of a build, in a root the package commits, and nothing was removed: " +
		"whether src/notes.txt exists cannot be told. What a build left there may still load. " +
		"The package is a link to a checkout: with that put right there, `phn build` removes what a build left, the stamp with it\n"
	if !strings.Contains(out, want) {
		t.Errorf("composer's output:\n%s\nwant the warning\n%s", out, want)
	}
	if _, err := os.Stat(filepath.Join(k.dir, ".src", "Legacy.php")); err != nil {
		t.Errorf("nothing was to be removed: %v", err)
	}

	// The advice, followed: the link put right, then the library's build.
	if err := os.Remove(filepath.Join(k.dir, "src", "notes.txt")); err != nil {
		t.Fatal(err)
	}
	k.build()
	if status := k.git("status", "--porcelain", "--ignored", "--", ".src"); status != "" {
		t.Errorf("after phn build in the library, its .src/ is not as committed:\n%s", status)
	}
	if out := p.mustComposer("dump-autoload"); strings.Contains(out, "phaenon/phn:") {
		t.Errorf("after the advice was followed, composer dump-autoload:\n%s", out)
	}
}

// TestClearedWhateverTheCompiler: the plugin clears in PHP at every dump,
// so what an earlier build left goes whichever phn the project has. Here
// the dump builds another installed package and the project, with a binary
// standing in that knows nothing of earlier builds, as a compiler older
// than the plugin does not: the class the library dropped is gone all the
// same, the plugin said so, and it told the `phn build --vendor` it ran
// which package it had looked at, through PHN_EARLIER_BUILDS_CLEARED,
// which the project's own build is not given. A value of that name in the
// environment Composer was started in is not the plugin's, and is there
// again when phn has run.
func TestClearedWhateverTheCompiler(t *testing.T) {
	w := newWorld(t, "")
	k := w.newReleases("kit", true)
	stamped(k.dir)
	writeFile(t, filepath.Join(k.dir, ".src", "Legacy.php"), []byte("<?php\n\nnamespace Acme\\Kit;\n\nfinal class Legacy\n{\n}\n"), 0o644)
	p := w.newProject("app", project{phaenonJSON: true, dependencies: []dependency{k.path(true)}, libraries: []string{"acme/lib"}, override: true})

	out := p.mustComposer("install")

	want := earlierBuildLine("2.0.0") + "removed it and 1 file with no source behind it\n  - removed .src/Legacy.php\n"
	if !strings.Contains(out, want) || strings.Count(out, "is the stamp of a build") != 1 {
		t.Errorf("composer's output:\n%s\nwant, once,\n%s", out, want)
	}
	if strings.Index(out, want) > strings.Index(out, "> phn build --vendor") {
		t.Errorf("the plugin cleared after it ran phn:\n%s", out)
	}
	w.expectRuns(run{p.dir, []string{"build", "--vendor"}}, run{p.dir, []string{"build"}})
	if got := w.cleared(); !slices.Equal(got, []string{"acme/kit", "unset"}) {
		t.Errorf("PHN_EARLIER_BUILDS_CLEARED at the two runs of phn: %v, want it to name acme/kit for build --vendor alone", got)
	}
	if status := k.git("status", "--porcelain", "--ignored", "--", ".src"); status != "" {
		t.Errorf("the library's .src/ is not as committed:\n%s", status)
	}
	p.expectKit("2.0.0", false)

	p.environment = []string{"PHN_EARLIER_BUILDS_CLEARED=mine"}
	p.mustComposer("dump-autoload")
	if got := w.cleared(); !slices.Equal(got, []string{"acme/kit", "unset", "acme/kit", "mine"}) {
		t.Errorf("PHN_EARLIER_BUILDS_CLEARED at the four runs of phn: %v, want the value Composer was started with back for the project's build", got)
	}
}

// TestPackagesThePluginCannotRead: the plugin names to phn the packages it
// has looked at, and no other, so that what it could not read is phn's in
// the same dump. acme/kit is a linked checkout that holds an earlier build.
//
// Where its project file is one both readers refuse for its roots, here a
// key written twice, the plugin reads nothing of it and leaves the package
// to phn: phn is run, the variable does not name the package, and phn
// warns of the file.
//
// Where the file is refused for a key beside the roots, here null in the
// list of "fmt", phn leaves the package out and so does the plugin. The
// package has nothing to build, and the dump fetches no compiler to say
// so: none is to be had in this project, and the install passes. Nothing
// is removed, by the plugin or by phn run by hand, which warns of the
// file; under -v the plugin names the key.
func TestPackagesThePluginCannotRead(t *testing.T) {
	leftovers := func(k *kit) {
		t.Helper()
		stamped(k.dir)
		writeFile(t, filepath.Join(k.dir, ".src", "Legacy.php"), []byte("<?php\n"), 0o644)
	}
	kept := func(k *kit, when string) {
		t.Helper()
		for _, f := range []string{".phn-build", "Legacy.php"} {
			if _, err := os.Stat(filepath.Join(k.dir, ".src", f)); err != nil {
				t.Errorf("%s: %v", when, err)
			}
		}
	}

	t.Run("a file whose roots cannot be told", func(t *testing.T) {
		w := newWorld(t, "")
		k := w.newReleasesWith("kit", true, nil, func(dir string) {
			writeFile(t, filepath.Join(dir, "phaenon.json"), []byte(`{"roots": {"src": ".src"}, "committed": ["src"], "committed": ["src"]}`+"\n"), 0o644)
		})
		leftovers(k)
		base := filepath.Join(w.base, "libraries", "acme", "base")
		for path, text := range map[string]string{
			"composer.json":   `{"name": "acme/base", "autoload": {"psr-4": {"Acme\\Base\\": ".src/"}}}` + "\n",
			"phaenon.json":    `{"roots": {"src": ".src"}, "committed": ["src"]}` + "\n",
			"src/Thing.phn":   "namespace Acme\\Base;\n\nfinal class Thing\n{\n}\n",
			".src/Thing.php":  "<?php\n\nnamespace Acme\\Base;\n\nfinal class Thing\n{\n}\n",
			".src/Legacy.php": "<?php\n",
			".src/.phn-build": "phn 0.4.1\n",
		} {
			writeFile(t, filepath.Join(base, path), []byte(text), 0o644)
		}
		whole := dependency{name: "acme/base", constraint: "*", repository: pathRepository(base, "acme/base", "1.0.0", true)}

		// With a phn standing in: what the plugin tells it.
		told := w.newProject("told", project{dependencies: []dependency{k.path(true), whole}, override: true})
		out := told.mustComposer("install")
		if !strings.Contains(out, "phaenon/phn: acme/base 1.0.0: .src/.phn-build is the stamp of a build") || strings.Contains(out, "acme/kit 2.0.0: .src/.phn-build") {
			t.Errorf("composer's output:\n%s\nwant the plugin's line for acme/base, which it read, and none for acme/kit", out)
		}
		w.expectRuns(run{told.dir, []string{"build", "--vendor"}})
		if got := w.cleared(); !slices.Equal(got, []string{"acme/base"}) {
			t.Errorf("PHN_EARLIER_BUILDS_CLEARED: %v, want it to name acme/base alone: acme/kit is phn's", got)
		}
		kept(k, "the package left to phn, after a dump with a phn that does nothing")

		// With the real phn: it says what is wrong with the file.
		real := w.newProject("real", project{dependencies: []dependency{k.path(true)}, binary: w.buildPhn()})
		out = real.mustComposer("install")
		if want := `warning: vendor/acme/kit/phaenon.json: the key "committed" is written twice`; !strings.Contains(out, want) {
			t.Errorf("composer's output lacks phn's warning %q:\n%s", want, out)
		}
		kept(k, "a package whose file phn refuses")
	})

	t.Run("a file refused for a key beside the roots", func(t *testing.T) {
		w := newWorld(t, "")
		k := w.newReleasesWith("kit", true, nil, func(dir string) {
			writeFile(t, filepath.Join(dir, "phaenon.json"), []byte(`{"roots": {"src": ".src"}, "committed": ["src"], "fmt": [null]}`+"\n"), 0o644)
		})
		leftovers(k)
		p := w.newProject("app", project{dependencies: []dependency{k.path(true)}})

		out := p.mustComposer("install")

		if strings.Contains(out, "phaenon/phn:") {
			t.Errorf("the plugin spoke of a package phn leaves out:\n%s", out)
		}
		w.expectRuns()
		expectAbsent(t, p.installed())
		kept(k, "a package phn leaves out")

		out = p.mustComposer("dump-autoload", "-v")
		if want := `phaenon/phn: acme/kit: phn refuses its phaenon.json for the key "fmt", and the package is left out, as installed` + "\n"; !strings.Contains(out, want) {
			t.Errorf("composer dump-autoload -v lacks\n%s\nin\n%s", want, out)
		}

		stdout, stderr, err := p.phn("build", "--vendor")
		if want := `warning: vendor/acme/kit/phaenon.json: "fmt" lists strings, and null is not one` + "\n"; err != nil || stderr != want || strings.Contains(stdout, "acme/kit") {
			t.Errorf("phn build --vendor: %v\n%s%s\nwant the warning\n%s", err, stdout, stderr, want)
		}
		kept(k, "phn build --vendor by hand")
	})

	t.Run("a file that is not UTF-8", func(t *testing.T) {
		w := newWorld(t, "")
		k := w.newReleasesWith("kit", true, nil, func(dir string) {
			writeFile(t, filepath.Join(dir, "phaenon.json"), []byte("{\"roots\": {\"src\": \".src\"}, \"committed\": [\"src\"], \"exclude\": [\"src/caf\xe9\"]}\n"), 0o644)
		})
		leftovers(k)
		p := w.newProject("app", project{dependencies: []dependency{k.path(true)}, binary: w.buildPhn()})

		out := p.mustComposer("install")

		if want := "warning: vendor/acme/kit/phaenon.json: the project file is not UTF-8 text\n"; !strings.Contains(out, want) || strings.Contains(out, "is the stamp of a build") {
			t.Errorf("composer's output:\n%s\nwant phn's warning of the file, and no word of a stamp:\n%s", out, want)
		}
		kept(k, "a package whose file neither reader takes")
	})
}

// TestReleaseThatTracksAStamp: the limit that is left, as it behaves. A
// release tracks a stamp because its gate was skipped: `phn build` in the
// library removes a stamp from a root the project commits, so the tree
// differs from the commit and the gate fails. Installed as a checkout all
// the same, by a project that never built it, the stamp is taken for an
// earlier build's and removed: the checkout is modified, and the next
// `composer update` of the package, made without a person to answer,
// fails on "has uncommitted changes".
func TestReleaseThatTracksAStamp(t *testing.T) {
	w := newWorld(t, "")
	k := w.newReleasesWith("kit", true, func(dir string) {
		writeFile(t, filepath.Join(dir, ".gitignore"), []byte("/vendor/\n"), 0o644) // the entry for the stamp forgotten
	}, stamped)
	if tracked := k.git("ls-files", ".src"); tracked != ".src/.phn-build\n.src/Greeter.php\n" {
		t.Fatalf("2.0.0 tracks under .src/:\n%s", tracked)
	}
	// The gate would have caught it: a build, then the state of the tree.
	k.build()
	if status := k.git("status", "--porcelain", "--", ".src"); status != " D .src/.phn-build\n" {
		t.Errorf("after phn build in the library, git status -- .src:\n%s\nwant the stamp removed", status)
	}
	k.git("checkout", "--", ".src")
	writeFile(t, filepath.Join(k.dir, "composer.json"), []byte(strings.Replace(mustRead(t, filepath.Join(k.dir, "composer.json")), "2.0.0", "2.0.1", 1)), 0o644)
	k.git("commit", "--quiet", "--all", "--message", "acme/kit 2.0.1")
	k.git("tag", "2.0.1")

	p := w.newProject("app", project{dependencies: []dependency{k.repository("2.0.0")}, config: map[string]any{"preferred-install": "source"}})
	out := p.mustComposer("install")
	if want := earlierBuildLine("2.0.0") + "removed it, and no file, since every file there has a source\n"; !strings.Contains(out, want) {
		t.Errorf("composer's output:\n%s\nwant\n%s", out, want)
	}
	if status := p.vendored("status", "--porcelain", "--ignored"); status != " D .src/.phn-build\n" {
		t.Errorf("the checkout of 2.0.0:\n%s", status)
	}

	p.require("2.0.1")
	out, err := p.composer("update")
	var exit *exec.ExitError
	// Composer boxes the message, and breaks its lines inside words.
	if !errors.As(err, &exit) || !strings.Contains(strings.Join(strings.Fields(out), ""), "hasuncommittedchanges") {
		t.Errorf("composer update: %v, want it to fail on the modified checkout\n%s", err, out)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestWarningSaysWhatToDo: where the leftovers of an earlier build cannot
// be removed, here because a link among the sources leads nowhere and so
// proves nothing of the file behind an output, the warning names the
// package's directory and the way to clear it, and following it works:
// with the directory removed, the install that follows is the release, and
// nothing is said.
func TestWarningSaysWhatToDo(t *testing.T) {
	w := newWorld(t, "")
	k := w.newReleases("kit", true)
	p := w.newProject("app", project{dependencies: []dependency{k.repository("^1.0")}, config: map[string]any{"preferred-install": "source"}})
	p.installBuilt()
	installed := filepath.Join(p.dir, "vendor", "acme", "kit")
	if err := os.Symlink("../nowhere", filepath.Join(installed, "src", "old.txt")); err == nil {
		t.Fatal("1.0.0 has src/old.txt: the link should not fit")
	}

	p.require("^2.0")
	p.mustComposer("update", "--no-plugins")
	if err := os.Symlink("../nowhere", filepath.Join(installed, "src", "old.txt")); err != nil {
		t.Fatal(err)
	}
	out := p.mustComposer("dump-autoload")

	want := "phaenon/phn: acme/kit 2.0.0: .src/.phn-build is the stamp of a build, in a root the package commits, and nothing was removed: " +
		"whether src/old.txt exists cannot be told. What a build left there may still load. " +
		"To clear the directory, remove vendor/acme/kit and install the package again\n"
	if !strings.Contains(out, want) {
		t.Errorf("composer's output:\n%s\nwant the warning\n%s", out, want)
	}
	p.expectKit("2.0.0", true)

	if err := os.RemoveAll(installed); err != nil {
		t.Fatal(err)
	}
	if out := p.mustComposer("install"); strings.Contains(out, "phaenon/phn:") {
		t.Errorf("the install that follows the advice should leave the plugin nothing to say:\n%s", out)
	}
	if status := p.vendored("status", "--porcelain", "--ignored"); status != "" {
		t.Errorf("the checkout after the advice was followed:\n%s", status)
	}
	p.expectKit("2.0.0", false)
}
