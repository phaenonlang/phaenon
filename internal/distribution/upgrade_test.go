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
	build := exec.Command(w.buildPhn(), "build")
	build.Dir = k.dir
	if out, err := build.CombinedOutput(); err != nil {
		w.t.Fatalf("phn build in the library: %v\n%s", err, out)
	}
	k.git("add", "--all")
	k.git("commit", "--quiet", "--message", "acme/kit 2.0.0")
	k.git("tag", "2.0.0")
	if tracked := k.git("ls-files", ".src"); tracked != ".src/Greeter.php\n" {
		w.t.Fatalf("2.0.0 tracks under .src/:\n%s", tracked)
	}
	return k
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
// compiler, and the checkout is then exactly the release: the dropped
// class does not load, git finds nothing to report, ignored files
// included, and the next dump says nothing.
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

	want := earlierBuildLine("2.0.0") + "removed 2 files with no source behind them (.src/Legacy.php, and 1 more) and the stamp\n"
	if !strings.Contains(out, want) || strings.Contains(out, "- removed") {
		t.Errorf("composer's output:\n%s\nwant the line, and no file named without -v:\n%s", out, want)
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
	want := strings.TrimPrefix(earlierBuildLine("2.0.0"), "phaenon/phn: ") + "removed 2 files with no source behind them (.src/Legacy.php, and 1 more) and the stamp\n" +
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
