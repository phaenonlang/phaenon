package main

// Installed Composer packages written in Phaenon (design/tooling.md §3.3):
// the packages of vendor/composer/installed.json that carry a phaenon.json.
// A project's check and build read them from their sources, so that their
// generic signatures are seen as written, and `phn build --vendor` compiles
// each into its own output directories, for its own target, its findings
// printed as warnings: the package's CI gave its verdict, and a newer phn
// may be stricter than the one it was released with.
//
// A package may commit the outputs of some roots, which its phaenon.json
// lists under "committed" (design/roadmap.md D28, design/tooling.md §3.7,
// T18): a library published for PHP projects at large, installed as plain
// PHP by a project without phn. The build leaves such a root as installed:
// it writes no output and no stamp there, and removes nothing but what a
// build of an earlier release left (earlier.go). A committed root that
// came without its outputs fails the build, which does not compile it
// instead: the same release is broken for every project without phn. The
// project's check and build still read such a package from its sources.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// stampFile records, in each output directory of an installed package, what
// the last build compiled: the compiler, the target, the installed set and
// the sources. A build that would compile the same is skipped.
const stampFile = ".phn-build"

// installedPackage is one package of installed.json.
type installedPackage struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	InstallPath string `json:"install-path"`
	// InstallationSource is "source" for a checkout of the package's
	// repository and "dist" for an archive or a path repository.
	InstallationSource string `json:"installation-source"`
	dir                string // where it is installed, relative to the working directory when under it
}

// isLink reports whether the package's directory is a symbolic link: a
// path repository linked to a checkout.
func (p installedPackage) isLink() bool {
	st, err := os.Lstat(p.dir)
	return err == nil && st.Mode()&fs.ModeSymlink != 0
}

// keepsItsDirectory reports whether a build made in the package's directory
// can have outlived an upgrade of the package. Composer updates an archive
// and a copied path repository by removing the directory and installing
// anew, so nothing of an earlier build is ever there. A checkout of the
// package's repository is updated in place, and a linked path repository
// is a checkout Composer never empties. A package installed.json says
// nothing of is not known to be either.
func (p installedPackage) keepsItsDirectory() bool {
	return p.InstallationSource == "source" || p.isLink()
}

// vendorComposerDir is the vendor/composer directory of the project in the
// working directory: composer.json's config.vendor-dir, or
// COMPOSER_VENDOR_DIR, or vendor.
func vendorComposerDir() string {
	vendor := os.Getenv("COMPOSER_VENDOR_DIR")
	if vendor == "" {
		vendor = "vendor"
		if data, err := os.ReadFile("composer.json"); err == nil {
			var c struct {
				Config struct {
					VendorDir string `json:"vendor-dir"`
				} `json:"config"`
			}
			if json.Unmarshal(data, &c) == nil && c.Config.VendorDir != "" {
				vendor = c.Config.VendorDir
			}
		}
	}
	return filepath.Join(vendor, "composer")
}

// installedPackages reads composerDir/installed.json, Composer 2's object
// or Composer 1's list, sorted by name; nil when there is none.
func installedPackages(composerDir string) ([]installedPackage, error) {
	path := filepath.Join(composerDir, "installed.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Packages []installedPackage `json:"packages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		if json.Unmarshal(data, &doc.Packages) != nil {
			return nil, fmt.Errorf("%s: %v", path, err)
		}
	}
	for i := range doc.Packages {
		p := &doc.Packages[i]
		ip := filepath.FromSlash(p.InstallPath)
		if ip == "" {
			ip = filepath.Join("..", filepath.FromSlash(p.Name)) // Composer 1: vendor/<name>
		}
		if !filepath.IsAbs(ip) {
			ip = filepath.Join(composerDir, ip)
		}
		p.dir = relativePath(filepath.Clean(ip))
	}
	sort.Slice(doc.Packages, func(i, j int) bool { return doc.Packages[i].Name < doc.Packages[j].Name })
	return doc.Packages, nil
}

// phaenonPackage is an installed package with a project file naming roots.
type phaenonPackage struct {
	installedPackage
	proj *project // its roots and excludes relative to the working directory
}

// phaenonPackages lists the installed packages that carry a phaenon.json
// naming roots. A project file that cannot be read is passed to warn and
// its package left out.
func phaenonPackages(composerDir string, warn func(error)) []phaenonPackage {
	pkgs, err := installedPackages(composerDir)
	if err != nil {
		warn(err)
		return nil
	}
	var out []phaenonPackage
	for _, p := range pkgs {
		proj, err := readProject(filepath.Join(p.dir, ProjectFile))
		switch {
		case err != nil:
			warn(err)
		case proj != nil && len(proj.Roots) > 0:
			out = append(out, phaenonPackage{p, proj.rebased(p.dir)})
		}
	}
	return out
}

// installedSources maps the output directories of the installed Phaenon
// packages to their source roots, absolute, for the Composer loader: under
// the path Composer installed a package at and, for a path repository's
// symbolic link, the path it points to, since the autoload maps may name
// either.
func installedSources(composerDir string) map[string]string {
	m := map[string]string{}
	for _, p := range phaenonPackages(composerDir, func(error) {}) {
		for out, src := range p.proj.sources() {
			m[out] = src
			if real, err := filepath.EvalSymlinks(filepath.Dir(out)); err == nil {
				m[filepath.Join(real, filepath.Base(out))] = src
			}
		}
	}
	return m
}

// committedGap is the error of a package installed without the outputs of
// the root src, which it commits: missing is what missingOutputs found. The
// paths are relative to the package, as its project file writes them.
//
// This text and committedGapsSummary's are the Composer plugin's too
// (composer/src/ProjectFile.php), which makes the same check before it
// fetches a compiler: testdata/committed-outputs.json holds both to them,
// in full.
func committedGap(p phaenonPackage, src string, missing []string) string {
	rel := func(path string) string {
		if r, err := filepath.Rel(p.dir, path); err == nil {
			return filepath.ToSlash(r)
		}
		return filepath.ToSlash(path)
	}
	more := ""
	if n := len(missing) - 1; n > 0 {
		more = fmt.Sprintf(", and %d more", n)
	}
	return fmt.Sprintf("%s: its %s commits the outputs of the root %s, but this release is installed without them: %s is missing%s",
		strings.TrimSpace(p.Name+" "+p.Version), ProjectFile, rel(src), rel(missing[0]), more)
}

// committedGapsSummary ends the errors of n packages: what the failure
// means for the project that installed them, which cannot repair another
// package's release, and what it can do.
func committedGapsSummary(n int) string {
	return fmt.Sprintf("%d installed packages lack outputs they commit, and nothing was built: a build here would hide a release that is broken for every project without phn. "+
		"This project cannot repair another package's release: report it to that package's maintainers, or require a version of the package that has its outputs", n)
}

// buildVendor is `phn build --vendor`: every installed Phaenon package is
// compiled whose sources, compiler, target or installed set changed since
// its last build, but for the roots it commits, where it removes only what
// an earlier build left. It fails when an output cannot be written, and
// before it builds or removes anything when a committed root came without
// its outputs.
func buildVendor() {
	dir := vendorComposerDir()
	installed := filepath.Join(dir, "installed.json")
	installedData, err := os.ReadFile(installed)
	if err != nil {
		fail("phn build --vendor: %s cannot be read (%v): run composer install first", installed, err)
	}
	// Every package's program reads the vendor code through the root
	// project's autoload maps, not through a vendor directory of the
	// package's own (a path repository's checkout has one).
	composerDir = dir
	pkgs := phaenonPackages(dir, func(err error) { fmt.Fprintf(os.Stderr, "warning: %v\n", err) })
	// The committed roots are checked first, and for presence only: the
	// Composer plugin makes the same check before it fetches a compiler,
	// and builds nothing either when it fails.
	broken := 0
	for _, p := range pkgs {
		gaps := 0
		for _, r := range p.proj.roots() {
			if !p.proj.commits(r) {
				continue
			}
			if missing := p.proj.missingOutputs(r); len(missing) > 0 {
				fmt.Fprintln(os.Stderr, committedGap(p, r, missing))
				gaps++
			}
		}
		if gaps > 0 {
			broken++
		}
	}
	if broken > 0 {
		fail("%s", committedGapsSummary(broken))
	}
	// Then, and only when every package came whole, what an earlier build
	// left in a root its package now commits is removed (earlier.go), in
	// the packages an earlier build can have survived in.
	for _, p := range pkgs {
		if !p.keepsItsDirectory() {
			continue
		}
		for _, e := range p.earlierBuilds() {
			lines, warning := e.report(strings.TrimSpace(p.Name+" "+p.Version), filepath.ToSlash(p.dir), p.isLink())
			if warning {
				fmt.Fprintf(os.Stderr, "warning: %s\n", strings.Join(lines, "\n"))
			} else {
				fmt.Println(strings.Join(lines, "\n"))
			}
		}
	}
	compiler := compilerStamp()
	built, current, failed, left := 0, 0, 0, 0
	for _, p := range pkgs {
		present := p.proj.roots() // only the roots present: a dist archive may leave out tests/
		roots := slices.DeleteFunc(slices.Clone(present), p.proj.commits)
		if len(roots) == 0 {
			if len(present) > 0 {
				left++ // every installed root is committed: nothing to build
			}
			continue
		}
		tgt := projectTarget(p.proj, roots)
		// The roots that are built are compiled with the committed ones,
		// whose declarations they read from the sources: a change there
		// makes their outputs stale too. A package that commits nothing
		// keeps the stamp it had.
		with := ""
		for _, r := range present {
			if p.proj.commits(r) {
				with += fmt.Sprintf("with %s\n", treeDigest(r))
			}
		}
		stamps := map[string]string{}
		fresh := true
		for _, r := range roots {
			stamps[r] = fmt.Sprintf("phn %s\ntarget %s\ninstalled %s\nsources %s\n%s", compiler, tgt.Version, digest(installedData), treeDigest(r), with)
			cur, err := os.ReadFile(filepath.Join(p.proj.Roots[r], stampFile))
			fresh = fresh && err == nil && string(cur) == stamps[r]
		}
		if fresh {
			current++
			continue
		}
		if tgt.Unsupported {
			for _, d := range tgt.Diags {
				printDiag(d)
			}
			fmt.Fprintf(os.Stderr, "%s: not compiled: its target is not one phn compiles for\n", p.Name)
			failed++
			continue
		}
		excludes = append([]string(nil), p.proj.Exclude...)
		projectSources = p.proj.sources()
		res := buildJob{proj: p.proj, roots: roots, target: tgt, mirror: true, dependency: true}.run()
		if res.failed > 0 {
			fmt.Fprintf(os.Stderr, "%s: %d of %d files could not be compiled\n", p.Name, res.failed, res.files)
			failed++
			continue
		}
		for _, r := range roots {
			writeFile(filepath.Join(p.proj.Roots[r], stampFile), []byte(stamps[r]))
		}
		built++
		fmt.Printf("%s: %d files compiled, %d written\n", p.Name, res.files, res.written)
	}
	summary := fmt.Sprintf("%d installed packages built, %d up to date", built, current)
	if left > 0 {
		summary += fmt.Sprintf(", %d left as installed with their committed outputs", left)
	}
	fmt.Println(summary)
	if failed > 0 {
		fail("%d installed packages could not be built", failed)
	}
}

// compilerStamp names the running compiler: its version and, since a
// development build keeps one version across changes, the digest of its
// executable.
func compilerStamp() string {
	exe, err := os.Executable()
	if err != nil {
		return version
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return version
	}
	return version + " " + digest(data)
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// treeDigest is the digest of the files under a source root, by path and
// content, without nested vendor directories (which neither collect nor
// mirror reads).
func treeDigest(root string) string {
	var paths []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == "vendor" && path != root {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		rel, _ := filepath.Rel(root, p)
		io.WriteString(h, filepath.ToSlash(rel)+"\x00")
		if f, err := os.Open(p); err == nil {
			io.Copy(h, f)
			f.Close()
		}
		io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil))
}
