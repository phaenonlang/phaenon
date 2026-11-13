package main

// What a build of an earlier release left in a root an installed package
// now commits (design/tooling.md §3.7, T18).
//
// A package that shipped its sources alone was built where it is installed
// (design/roadmap.md D13). When a later release commits the outputs of a
// root and the upgrade keeps the directory, as the git checkout of a source
// install does, what that build wrote stays beside the committed files:
// its stamp, and the outputs of the sources the release dropped, which
// still load. `phn build --vendor` leaves a committed root as installed,
// with this one exception: where the root's output directory holds a
// stamp, in a package whose directory an earlier build can have survived
// in, it removes the files no source stands behind, then the stamp.
//
// A stamp alone proves little: a file of that name can come with a release
// that tracks one, or with the copy Composer makes of a checkout that
// holds one. It is taken for an earlier build's only where such a build
// can have been made and kept: in a checkout of the package's repository
// ("installation-source": "source" in installed.json), which Composer
// updates in place, and in a linked path repository, which it never
// empties. An archive and a copied path repository are installed into an
// emptied directory, so a stamp there came with the files: it is left
// alone, with everything beside it, and nothing is said. What remains is a
// release that tracks a stamp, installed as a checkout: there the stamp
// goes, and the checkout is modified. The build closes that at its source:
// in a project's own committed root it removes a stamp (mirror,
// project.go), so that no commit that passed the library's gate tracks
// one.
//
// It removes what the build's own rule names (sourcesBehind, project.go)
// and nothing else, and only when it is sure:
//
//   - A file goes when its absence from the root is proved: the name is
//     missing from a directory that can be read and searched, or the path
//     runs through a file. A directory that cannot be read and a link that
//     leads nowhere prove nothing, and then nothing is removed from the
//     root at all: with unreadable sources every output would look like an
//     orphan.
//   - Only regular files go, and the directories their removal empties. A
//     symbolic link in the output directory is neither followed nor
//     removed: a build writes none.
//   - The root and its output directory are directories of the package: if
//     either is a symbolic link or lies behind one, nothing is removed. A
//     link inside a package may lead anywhere, into another package's
//     outputs for one, and the build walks into neither (it fails on a
//     root that is a link, and removes nothing through an output directory
//     that is one). The package's own directory may be a link, a path
//     repository's: the files removed there are the ones `phn build` in
//     that checkout removes.
//   - The whole list is made before the first file goes, and the stamp
//     goes last, so that a removal that fails leaves the stamp for the
//     next run.
//
// Every file removed is named: the line is printed once in a package's
// life, and the files were untracked, so nothing else can say afterwards
// what went. A warning ends with what can be done about it.
//
// It is never the project's own directory that is cleared, whatever
// installed.json says of a package's path.
//
// The Composer plugin does the same without a compiler
// (composer/src/EarlierBuild.php), at every dump and before it resolves a
// binary, so that what it does never depends on which compiler the project
// has. When it then runs this command it sets PHN_EARLIER_BUILDS_CLEARED,
// and the command leaves the committed roots alone: the plugin has cleared
// what it could and said what it could not, and a second pass would say it
// again. The variable is the plugin's and no setting for a project. Run by
// hand, without it, the command clears as described here.
// testdata/earlier-builds.json holds the two to each other, the lines they
// print included.

import (
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

// earlierBuild is what `phn build --vendor` found in the output directory
// of a committed root that holds a stamp, and what it did there. The paths
// are relative to the package and written with slashes, as its project
// file writes them.
type earlierBuild struct {
	out      string   // the output directory
	removed  []string // the files removed, sorted
	failed   []string // the files that could not be removed, sorted
	obstacle string   // why nothing was removed, when nothing could be
	// unbuilt says of an obstacle that the project's own build does not
	// get past it either: `phn build` looks at the output directory of a
	// root only when the root is a directory, and walks through no link.
	// What to do in a linked checkout depends on it.
	unbuilt bool
}

// earlierBuilds clears what earlier builds left in the committed roots of
// the package, and returns what was found and done, by root.
func (p phaenonPackage) earlierBuilds() []*earlierBuild {
	var found []*earlierBuild
	for _, src := range slices.Sorted(slices.Values(p.proj.Committed)) {
		root, err1 := filepath.Rel(p.dir, src)
		out, err2 := filepath.Rel(p.dir, p.proj.Roots[src])
		if err1 != nil || err2 != nil {
			continue
		}
		if e := clearEarlierBuild(p.dir, root, out); e != nil {
			found = append(found, e)
		}
	}
	return found
}

// clearEarlierBuild removes from the output directory out of the root src,
// both relative to the package in dir, what an earlier build left there:
// the files no source stands behind, the directories that empties, and the
// stamp. It returns nil when the directory holds no stamp, and touches
// nothing then.
func clearEarlierBuild(dir, src, out string) *earlierBuild {
	stamp := filepath.Join(out, stampFile)
	if !isStamp(filepath.Join(dir, stamp)) {
		return nil
	}
	e := &earlierBuild{out: filepath.ToSlash(out)}
	for _, root := range []string{src, out} {
		if link := linkOnPath(dir, root); link != "" {
			e.obstacle, e.unbuilt = filepath.ToSlash(link)+" is a symbolic link", true
			return e
		}
	}
	switch st, err := os.Stat(filepath.Join(dir, src)); {
	case errors.Is(err, fs.ErrNotExist):
		e.obstacle, e.unbuilt = "the package is installed without "+filepath.ToSlash(src), true
		return e
	case err != nil || !st.IsDir():
		e.obstacle, e.unbuilt = filepath.ToSlash(src)+" cannot be read", true
		return e
	}
	var files []string
	if unread, ok := regularFiles(dir, out, &files); !ok {
		e.obstacle = filepath.ToSlash(unread) + " cannot be read"
		return e
	}
	var orphans []string
	for _, file := range files {
		rel, _ := under(file, out)
		if rel == stampFile {
			continue
		}
		state, at := sourceAbsent, ""
		for _, source := range sourcesBehind(rel) {
			s, where := sourceState(filepath.Join(dir, src), source)
			if s == sourceExists {
				state = s
				break
			}
			if s == sourceUnknown && state != sourceUnknown {
				state, at = s, where
			}
		}
		switch state {
		case sourceUnknown:
			e.obstacle = "whether " + filepath.ToSlash(filepath.Join(src, at)) + " exists cannot be told"
			return e
		case sourceAbsent:
			orphans = append(orphans, file)
		}
	}
	// From here on files are removed: the list above is whole.
	sort.Strings(orphans)
	for _, file := range orphans {
		if !removeRegular(filepath.Join(dir, file)) {
			e.failed = append(e.failed, filepath.ToSlash(file))
			continue
		}
		e.removed = append(e.removed, filepath.ToSlash(file))
		// The directories this emptied, never the output directory itself.
		for d := filepath.Dir(file); d != out; d = filepath.Dir(d) {
			full := filepath.Join(dir, d)
			if st, err := os.Lstat(full); err != nil || !st.IsDir() || os.Remove(full) != nil {
				break
			}
		}
	}
	// On a file system that takes one spelling of a name for another, the
	// stamp may have gone with the files above, under the name the
	// directory lists it by.
	if _, err := os.Lstat(filepath.Join(dir, stamp)); len(e.failed) == 0 && !errors.Is(err, fs.ErrNotExist) && !removeRegular(filepath.Join(dir, stamp)) {
		e.failed = append(e.failed, filepath.ToSlash(stamp))
	}
	return e
}

// isStamp reports whether path is a stamp as buildVendor writes one: a
// regular file, not a link to one, whose text begins with the compiler's
// name.
func isStamp(path string) bool {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	begins := make([]byte, len(stampBegins))
	_, err = io.ReadFull(f, begins)
	return err == nil && string(begins) == stampBegins
}

// stampBegins is how every stamp begins (buildVendor: "phn <version> …").
const stampBegins = "phn "

// report is what is printed of it: a line that says what was done, then a
// line for each file, and whether the whole is a warning, which it is when
// something is left that the next run will find again. pkg is the
// package's name and version, dir where it is installed, as shown to the
// reader, and linked whether that directory is a link to a checkout, which
// decides what a warning advises. Each advice is one a reader can follow
// to a run that says nothing (internal/distribution follows each): a
// package that is no link is installed again; in a linked checkout the
// library's own build removes what this command would, once what stands
// in the way is put right, but for a directory that build does not look
// at, which is cleared by hand.
func (e *earlierBuild) report(pkg, dir string, linked bool) (lines []string, warning bool) {
	// A name with a line break in it stays on its line.
	defer func() {
		for i, line := range lines {
			lines[i] = strings.NewReplacer("\n", `\n`, "\r", `\r`).Replace(line)
		}
	}()
	head := fmt.Sprintf("%s: %s/%s is the stamp of a build, in a root the package commits", pkg, e.out, stampFile)
	count := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	remedy := fmt.Sprintf("To clear the directory, remove %s and install the package again", dir)
	switch {
	case linked && e.unbuilt:
		remedy = fmt.Sprintf("The package is a link to a checkout, where no build looks at this directory: remove %s/%s there by hand, and what a build left beside it", e.out, stampFile)
	case linked:
		remedy = "The package is a link to a checkout: with that put right there, `phn build` removes what a build left, the stamp with it"
	}
	switch removed := count(len(e.removed), "file with no source behind it", "files with no source behind them"); {
	case e.obstacle != "":
		return []string{head + ", and nothing was removed: " + e.obstacle + ". What a build left there may still load. " + remedy}, true
	case len(e.failed) > 0 && len(e.removed) > 0:
		lines = []string{head + ": removed " + removed + ", but " + count(len(e.failed), "file", "files") + " could not be removed, so the stamp stays and the removal is tried again next time. " + remedy}
	case len(e.failed) > 0:
		lines = []string{head + ": " + count(len(e.failed), "file", "files") + " could not be removed, so the stamp stays and the removal is tried again next time. " + remedy}
	case len(e.removed) > 0:
		lines = []string{head + ": removed it and " + removed}
	default:
		return []string{head + ": removed it, and no file, since every file there has a source"}, false
	}
	for _, file := range e.removed {
		lines = append(lines, "  - removed "+file)
	}
	for _, file := range e.failed {
		lines = append(lines, "  - could not remove "+file)
	}
	return lines, len(e.failed) > 0
}

// linkOnPath returns the first directory on the path rel under dir that is
// a symbolic link, rel itself included, or "".
func linkOnPath(dir, rel string) string {
	path := ""
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		path = filepath.Join(path, name)
		if st, err := os.Lstat(filepath.Join(dir, path)); err == nil && st.Mode()&fs.ModeSymlink != 0 {
			return path
		}
	}
	return ""
}

// regularFiles appends to files the regular files under the directory rel
// of dir, as paths that start with rel. A symbolic link is passed over,
// whatever it leads to. Where a directory cannot be read it returns that
// directory and false.
func regularFiles(dir, rel string, files *[]string) (unread string, ok bool) {
	entries, err := os.ReadDir(filepath.Join(dir, rel))
	if err != nil {
		return rel, false
	}
	for _, entry := range entries {
		path := filepath.Join(rel, entry.Name())
		st, err := os.Lstat(filepath.Join(dir, path))
		switch {
		case err != nil:
			return rel, false
		case st.IsDir():
			if unread, ok := regularFiles(dir, path, files); !ok {
				return unread, false
			}
		case st.Mode().IsRegular():
			*files = append(*files, path)
		}
	}
	return "", true
}

// What is known of a source: it exists, its absence is proved, or neither.
const (
	sourceExists = iota
	sourceAbsent
	sourceUnknown
)

// sourceState says whether the path rel exists under root, as the build
// asks it (a stat, which follows links), and, when it does not answer,
// whether that is because nothing is there. Absence is proved where the
// name is missing from a directory that can be read and searched, or the
// path runs through a file; where it is not proved, the state is unknown
// and at is the path, relative to root, that could not be examined.
func sourceState(root, rel string) (state int, at string) {
	if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
		return sourceExists, ""
	}
	dir := root
	names := strings.Split(rel, string(filepath.Separator))
	for i, name := range names {
		at = filepath.Join(at, name)
		path := filepath.Join(dir, name)
		st, err := os.Stat(path)
		switch {
		case err == nil && st.IsDir():
			dir = path
			continue
		case err == nil && i == len(names)-1:
			return sourceExists, "" // it has appeared since
		case err == nil:
			return sourceAbsent, "" // the path runs through a file
		}
		// Nothing answers to the name. A link that leads nowhere this
		// process can follow is something; so is a name in a directory
		// that cannot be searched, or cannot be read.
		if _, err := os.Lstat(path); err == nil || !errors.Is(err, fs.ErrNotExist) {
			return sourceUnknown, at
		}
		if _, err := os.ReadDir(dir); err != nil {
			return sourceUnknown, at
		}
		return sourceAbsent, ""
	}
	return sourceExists, ""
}

// removeRegular removes the file at path when it is a regular file still,
// and reports whether it did.
func removeRegular(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.Mode().IsRegular() && os.Remove(path) == nil
}
