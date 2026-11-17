package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// devRoots are the project's development roots: those whose output
// directory Composer's `autoload-dev` maps and its `autoload` does not, so
// that `composer install --no-dev` loads nothing from them (tests/ and its
// .tests/, which name PHPUnit, a development requirement). The project
// file says nothing of it: composer.json, beside it, already does. A root
// neither section maps is no development root.
func (p *project) devRoots() map[string]bool {
	data, err := os.ReadFile(filepath.Join(filepath.Dir(p.path), "composer.json"))
	if err != nil {
		return nil
	}
	var manifest struct {
		Autoload    json.RawMessage `json:"autoload"`
		AutoloadDev json.RawMessage `json:"autoload-dev"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return nil
	}
	prod, dev := sectionPaths(manifest.Autoload), sectionPaths(manifest.AutoloadDev)
	out := map[string]bool{}
	for src, dst := range p.Roots {
		rel := dst
		if filepath.IsAbs(dst) {
			var err error
			if rel, err = filepath.Rel(filepath.Dir(p.path), dst); err != nil {
				continue
			}
		}
		if mapsDir(dev, rel) && !mapsDir(prod, rel) {
			out[src] = true
		}
	}
	return out
}

// sectionPaths lists the paths an autoload section of composer.json names,
// relative to the project and cleaned: PSR-4 and PSR-0 directories, a class
// map's paths and files.
func sectionPaths(raw json.RawMessage) []string {
	var section struct {
		PSR4     map[string]json.RawMessage `json:"psr-4"`
		PSR0     map[string]json.RawMessage `json:"psr-0"`
		Classmap []string                   `json:"classmap"`
		Files    []string                   `json:"files"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &section) != nil {
		return nil
	}
	var paths []string
	for _, m := range []map[string]json.RawMessage{section.PSR4, section.PSR0} {
		for _, v := range m {
			var one string
			var many []string
			switch {
			case json.Unmarshal(v, &one) == nil:
				paths = append(paths, one)
			case json.Unmarshal(v, &many) == nil:
				paths = append(paths, many...)
			}
		}
	}
	paths = append(paths, section.Classmap...)
	paths = append(paths, section.Files...)
	for i, path := range paths {
		paths[i] = filepath.Clean(filepath.FromSlash(path))
	}
	return paths
}

// mapsDir reports whether one of the paths lies in dir (relative, cleaned),
// or is dir, or holds it: the project's own directory maps every root.
func mapsDir(paths []string, dir string) bool {
	dir = filepath.Clean(dir)
	for _, path := range paths {
		if path == dir || path == "." || strings.HasPrefix(path, dir+string(filepath.Separator)) ||
			strings.HasPrefix(dir, path+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// leaveOutDevRoots drops the development roots from roots, and says in one
// line which it left out.
func (p *project) leaveOutDevRoots(roots []string) []string {
	dev := p.devRoots()
	var kept, left []string
	for _, r := range roots {
		if dev[r] {
			left = append(left, r)
		} else {
			kept = append(kept, r)
		}
	}
	if len(left) > 0 {
		sort.Strings(left)
		outs := make([]string, len(left))
		for i, r := range left {
			outs[i] = p.Roots[r]
		}
		os.Stdout.WriteString("phn build: --no-dev leaves out " + strings.Join(left, ", ") + ", whose output " +
			strings.Join(outs, ", ") + " composer.json maps under autoload-dev alone\n")
	}
	return kept
}
