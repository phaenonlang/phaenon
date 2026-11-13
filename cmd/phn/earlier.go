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
// with this one exception: where the root's output directory holds the
// stamp, it removes the files no source stands behind, then the stamp. The
// stamp is the proof that the root was built here: only this command
// writes one, a library's repository ignores the file, and no build writes
// one into a committed root.
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
// The Composer plugin does the same without a compiler
// (composer/src/EarlierBuild.php), before it fetches one:
// testdata/earlier-builds.json holds the two to each other, the lines they
// print included.

import (
	"errors"
	"fmt"
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
	if st, err := os.Lstat(filepath.Join(dir, stamp)); err != nil || !st.Mode().IsRegular() {
		return nil
	}
	e := &earlierBuild{out: filepath.ToSlash(out)}
	for _, root := range []string{src, out} {
		if link := linkOnPath(dir, root); link != "" {
			e.obstacle = filepath.ToSlash(link) + " is a symbolic link"
			return e
		}
	}
	switch st, err := os.Stat(filepath.Join(dir, src)); {
	case errors.Is(err, fs.ErrNotExist):
		e.obstacle = "the package is installed without " + filepath.ToSlash(src)
		return e
	case err != nil || !st.IsDir():
		e.obstacle = filepath.ToSlash(src) + " cannot be read"
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
	if len(e.failed) == 0 && !removeRegular(filepath.Join(dir, stamp)) {
		e.failed = append(e.failed, filepath.ToSlash(stamp))
	}
	return e
}

// report is the line that says what was done, and whether it is a warning:
// something is left that the next run will find again.
func (e *earlierBuild) report(pkg string) (line string, warning bool) {
	head := fmt.Sprintf("%s: %s/%s is the stamp of an earlier build of this package, in a root it now commits", pkg, e.out, stampFile)
	list := func(paths []string) string {
		if n := len(paths) - 1; n > 0 {
			return fmt.Sprintf("%s, and %d more", paths[0], n)
		}
		return paths[0]
	}
	removed := ""
	switch n := len(e.removed); {
	case n == 1:
		removed = fmt.Sprintf("removed 1 file with no source behind it (%s)", e.removed[0])
	case n > 1:
		removed = fmt.Sprintf("removed %d files with no source behind them (%s)", n, list(e.removed))
	}
	switch {
	case e.obstacle != "":
		return head + ", and nothing was removed: " + e.obstacle + ". What that build left there may still load", true
	case len(e.failed) > 0 && removed != "":
		return head + ": " + removed + ", but " + list(e.failed) + " could not be removed, so the stamp stays and the next build tries again", true
	case len(e.failed) > 0:
		return head + ": " + list(e.failed) + " could not be removed, so the stamp stays and the next build tries again", true
	case removed != "":
		return head + ": " + removed + " and the stamp", false
	}
	return head + ": removed the stamp alone, since every file there has a source", false
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
