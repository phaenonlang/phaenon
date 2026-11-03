package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/phpversion"
)

// target is the PHP version a run compiles for, with what was found on the
// way (design/php-versions.md §2).
type target struct {
	Version phpversion.Version
	Diags   []checker.Diagnostic
	// Unsupported is set when the stated version is outside what phn can
	// compile for: check goes on with the nearest supported version, build
	// writes nothing.
	Unsupported bool
}

// composerFile is composer.json as far as the target is concerned.
type composerFile struct {
	Path    string
	Data    []byte
	Require struct {
		PHP string `json:"php"`
	} `json:"require"`
}

// findComposerJSON returns the nearest composer.json at or above dir, or nil.
func findComposerJSON(dir string) *composerFile {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	if st, err := os.Stat(abs); err == nil && !st.IsDir() {
		abs = filepath.Dir(abs)
	}
	for {
		path := filepath.Join(abs, "composer.json")
		if data, err := os.ReadFile(path); err == nil {
			c := &composerFile{Path: relativePath(path), Data: data}
			json.Unmarshal(data, c) // a broken composer.json states no PHP
			return c
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return nil
		}
		abs = parent
	}
}

// relativePath shortens an absolute path to one relative to the working
// directory when it lies under it.
func relativePath(path string) string {
	if wd, err := os.Getwd(); err == nil {
		if rel, ok := under(path, wd); ok {
			return rel
		}
	}
	return path
}

// resolveTarget decides the target: phaenon.json's "php" when stated,
// otherwise the lowest version composer.json's require.php admits,
// otherwise the floor. proj and composer may be nil; anchor is the file a
// diagnostic about a missing target attaches to when neither exists.
func resolveTarget(proj *project, composer *composerFile, anchor string) target {
	t := target{Version: phpversion.Floor}
	diag := func(file string, data []byte, path []string, code checker.Code, format string, args ...any) {
		line, col := jsonPosition(data, path...)
		t.Diags = append(t.Diags, checker.Diagnostic{File: file, Line: line, Col: col, EndLine: line, EndCol: col,
			Severity: code.Severity, Code: code.ID, Message: fmt.Sprintf(format, args...)})
	}
	// The floor composer.json states, if it states one.
	var floor phpversion.Version
	floorState := "absent" // absent, set, unbounded, unreadable
	var floorErr error
	if composer != nil && composer.Require.PHP != "" {
		floor, floorErr = phpversion.LowestAdmitted(composer.Require.PHP)
		switch {
		case floorErr == nil:
			floorState = "set"
		case errors.Is(floorErr, phpversion.ErrNoBound):
			floorState = "unbounded"
		default:
			floorState = "unreadable"
		}
	}
	requirePHP := []string{"require", "php"}
	if proj != nil && proj.PHP != "" {
		v, err := phpversion.Parse(proj.PHP)
		switch {
		case err != nil:
			t.Unsupported = true
			diag(proj.path, proj.data, []string{"php"}, checker.P4009, "%v", err)
			return t
		case !v.Supported():
			t.Unsupported = true
			t.Version = v.Clamp()
			diag(proj.path, proj.data, []string{"php"}, checker.P4009,
				"PHP %s is not a target phn compiles for: targets are %s to %s", v, phpversion.Floor, phpversion.Newest)
			return t
		}
		t.Version = v
		switch {
		case floorState == "set" && floor.Less(v):
			diag(proj.path, proj.data, []string{"php"}, checker.P4008,
				"the target is PHP %s, but composer.json admits PHP %s (require.php %q): an install on %s would load code it cannot run; raise require.php to \">=%s\"",
				v, floor, composer.Require.PHP, floor, v)
		case floorState == "unbounded":
			diag(proj.path, proj.data, []string{"php"}, checker.P4008,
				"the target is PHP %s, but composer.json admits every PHP (require.php %q); raise require.php to \">=%s\"",
				v, composer.Require.PHP, v)
		}
		return t
	}
	switch floorState {
	case "set":
		switch {
		case floor.Less(phpversion.Floor):
			diag(composer.Path, composer.Data, requirePHP, checker.P4008,
				"composer.json admits PHP %s (require.php %q), but phn compiles for PHP %s at the oldest: an install on %s would load code it cannot run; raise require.php to \">=%s\", or state the target in %s",
				floor, composer.Require.PHP, phpversion.Floor, floor, phpversion.Floor, ProjectFile)
		case phpversion.Newest.Less(floor):
			t.Unsupported = true
			t.Version = phpversion.Newest
			diag(composer.Path, composer.Data, requirePHP, checker.P4009,
				"composer.json requires PHP %s or newer (require.php %q); phn compiles for PHP %s to %s",
				floor, composer.Require.PHP, phpversion.Floor, phpversion.Newest)
		default:
			t.Version = floor
		}
	case "unbounded":
		diag(composer.Path, composer.Data, requirePHP, checker.P4008,
			"composer.json admits every PHP (require.php %q), but phn compiles for PHP %s at the oldest; raise require.php to \">=%s\", or state the target in %s",
			composer.Require.PHP, phpversion.Floor, phpversion.Floor, ProjectFile)
	case "unreadable":
		diag(composer.Path, composer.Data, requirePHP, checker.P4010,
			"require.php cannot be read (%v); compiling for PHP %s: state the target in %s", floorErr, phpversion.Floor, ProjectFile)
	default:
		msg := fmt.Sprintf("no PHP target: neither %s's \"php\" nor composer.json's require.php states one; compiling for PHP %s", ProjectFile, phpversion.Floor)
		switch {
		case proj != nil:
			diag(proj.path, proj.data, nil, checker.P4010, "%s", msg)
		case composer != nil:
			diag(composer.Path, composer.Data, nil, checker.P4010, "%s", msg)
		case anchor != "":
			t.Diags = append(t.Diags, checker.Diagnostic{File: anchor, Line: 1, Col: 1, Severity: checker.Warning,
				Code: checker.P4010.ID, Message: msg})
		}
	}
	return t
}

// projectTarget resolves the target of a build or check run: the project
// file's directory (the working directory) or, without one, the first path
// given locates composer.json.
func projectTarget(proj *project, paths []string) target {
	dir := "."
	anchor := ""
	switch {
	case proj != nil && proj.path != ProjectFile:
		dir = filepath.Dir(proj.path) // an installed package's project file
	case proj == nil && len(paths) > 0:
		dir = paths[0]
		anchor = paths[0]
	}
	return resolveTarget(proj, findComposerJSON(dir), anchor)
}

// jsonPosition returns the 1-based line and column of the value at path in
// a JSON document, or of the document's start when path is empty or not
// found.
func jsonPosition(data []byte, path ...string) (line, col int) {
	off := jsonValueOffset(data, path)
	if off < 0 {
		off = 0
	}
	line, col = 1, 1
	for _, b := range data[:off] {
		if b == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

// jsonValueOffset returns the byte offset where the value at path begins,
// or -1.
func jsonValueOffset(data []byte, path []string) int {
	if len(path) == 0 {
		return -1
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	// value reads one value; want is the rest of the path when this value
	// lies on it, nil otherwise.
	var value func(want []string, onPath bool) int
	value = func(want []string, onPath bool) int {
		start := skipJSONSpace(data, int(dec.InputOffset()))
		tok, err := dec.Token()
		if err != nil {
			return -1
		}
		if onPath && len(want) == 0 {
			return start
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return -1
		}
		found := -1
		for dec.More() {
			if delim == '{' {
				kt, err := dec.Token()
				if err != nil {
					return -1
				}
				key, _ := kt.(string)
				if off := value(tail(want), onPath && len(want) > 0 && key == want[0]); off >= 0 && found < 0 {
					found = off
				}
			} else if off := value(nil, false); off >= 0 && found < 0 {
				found = off
			}
		}
		dec.Token() // the closing delimiter
		return found
	}
	return value(path, true)
}

func tail(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s[1:]
}

// skipJSONSpace moves past whitespace and the separators the decoder's
// offset may still point at.
func skipJSONSpace(data []byte, off int) int {
	for off < len(data) && strings.IndexByte(" \t\r\n:,", data[off]) >= 0 {
		off++
	}
	return off
}

// setProjectTarget writes "php": "X.Y" into the project file, editing the
// text so that the rest of the file keeps its layout, and creates the file
// when there is none.
func setProjectTarget(v phpversion.Version) error {
	value := fmt.Sprintf("%q", v.String())
	data, err := os.ReadFile(ProjectFile)
	if errors.Is(err, os.ErrNotExist) {
		return os.WriteFile(ProjectFile, []byte("{\n    \"php\": "+value+"\n}\n"), 0o644)
	}
	if err != nil {
		return err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("%s: %v", ProjectFile, err)
	}
	if old, ok := probe["php"]; ok {
		start := jsonValueOffset(data, []string{"php"})
		end := start + len(bytes.TrimSpace(old))
		if start < 0 || end > len(data) {
			return fmt.Errorf("%s: cannot locate \"php\"", ProjectFile)
		}
		out := append(append(append([]byte{}, data[:start]...), value...), data[end:]...)
		return os.WriteFile(ProjectFile, out, 0o644)
	}
	// Append the member after the last one, in the file's own layout.
	open := bytes.IndexByte(data, '{')
	end := bytes.LastIndexByte(data, '}')
	if open < 0 || end < open {
		return fmt.Errorf("%s: not a JSON object", ProjectFile)
	}
	body := data[open+1 : end]
	member := `"php": ` + value
	if len(bytes.TrimSpace(body)) == 0 {
		out := append(append(append([]byte{}, data[:open+1]...), "\n    "+member+"\n"...), data[end:]...)
		return os.WriteFile(ProjectFile, out, 0o644)
	}
	last := open + 1 + len(bytes.TrimRight(body, " \t\r\n"))
	sep := " "
	if nl := bytes.IndexByte(body, '\n'); nl >= 0 {
		rest := body[nl+1:]
		indent := rest[:len(rest)-len(bytes.TrimLeft(rest, " \t"))]
		sep = "\n" + string(indent)
	}
	out := append(append(append([]byte{}, data[:last]...), ","+sep+member...), data[last:]...)
	return os.WriteFile(ProjectFile, out, 0o644)
}
