package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/phaenonlang/phaenon/internal/emit"
)

// aliasImporters lists the project's files outside the run that import a
// docblock alias (`@phpstan-import-type`), for the model: an alias such a
// file imports is not lifted from under it (design/collections.md §6.9).
// The other files of the project are loaded by name, as their classes are
// met, and nothing names a class for importing an alias of the run's.
//
// The project's files are those of the project file's roots; without one,
// of a PHP project migrated for the first time, those of the directories
// the nearest composer.json autoloads, `autoload` and `autoload-dev`.
// known is false when neither says where the project's code is: the lift
// then leaves every alias a tag, since it cannot tell who imports it.
func aliasImporters(proj *project, run []string) (files []string, known bool) {
	if len(run) == 0 {
		return nil, true
	}
	var candidates []string
	if proj != nil {
		candidates = collect(proj.roots(), proj.isOutput, ".php", ".phn")
	} else {
		dirs := autoloadPaths(findComposerJSON(run[0]))
		if len(dirs) == 0 {
			return nil, false
		}
		candidates = collect(dirs, func(string) bool { return false }, ".php", ".phn")
	}
	inRun := map[string]bool{}
	for _, f := range run {
		if abs, err := filepath.Abs(f); err == nil {
			inRun[abs] = true
		}
	}
	seen := map[string]bool{}
	for _, f := range candidates {
		abs, err := filepath.Abs(f)
		if err != nil || inRun[abs] || seen[abs] {
			continue
		}
		seen[abs] = true
		src, err := os.ReadFile(f)
		if err != nil || !bytes.Contains(src, []byte("import-type")) {
			continue
		}
		if strings.HasSuffix(f, ".php") {
			if _, err := os.Stat(strings.TrimSuffix(f, ".php") + ".phn"); err == nil || emit.IsGenerated(src) {
				continue // a compiled file: its .phn declares the same classes
			}
		}
		files = append(files, f)
	}
	return files, true
}

// autoloadPaths lists the directories and files a composer.json autoloads,
// under `autoload` and `autoload-dev`: the PSR-4 and PSR-0 directories, the
// classmap's entries and the `files`. Those that exist, as paths the
// working directory reads; none for a composer.json that is absent, does
// not parse or autoloads nothing.
func autoloadPaths(c *composerFile) []string {
	if c == nil {
		return nil
	}
	type autoload struct {
		PSR4     map[string]json.RawMessage `json:"psr-4"`
		PSR0     map[string]json.RawMessage `json:"psr-0"`
		Classmap []string                   `json:"classmap"`
		Files    []string                   `json:"files"`
	}
	var file struct {
		Autoload    autoload `json:"autoload"`
		AutoloadDev autoload `json:"autoload-dev"`
	}
	if err := json.Unmarshal(c.Data, &file); err != nil {
		return nil
	}
	base := filepath.Dir(c.Path)
	var out []string
	seen := map[string]bool{}
	add := func(rel string) {
		path := filepath.Join(base, rel)
		if _, err := os.Stat(path); err == nil && !seen[path] {
			seen[path] = true
			out = append(out, path)
		}
	}
	// A prefix maps to a directory or to a list of them.
	prefixes := func(m map[string]json.RawMessage) {
		for _, raw := range m {
			var one string
			var many []string
			if json.Unmarshal(raw, &one) == nil {
				add(one)
			} else if json.Unmarshal(raw, &many) == nil {
				for _, dir := range many {
					add(dir)
				}
			}
		}
	}
	for _, a := range []autoload{file.Autoload, file.AutoloadDev} {
		prefixes(a.PSR4)
		prefixes(a.PSR0)
		for _, entry := range a.Classmap {
			add(entry)
		}
		for _, entry := range a.Files {
			add(entry)
		}
	}
	return out
}
