// Package phpt reads php-src's .phpt test files.
//
// A .phpt file is a sequence of sections, each introduced by a line of the
// form --NAME-- ; only the sections the conformance suite needs are modelled.
package phpt

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

// Test is one .phpt file.
type Test struct {
	Path     string
	Name     string            // --TEST--
	File     []byte            // --FILE-- or --FILEEOF--
	Expect   string            // --EXPECT--, --EXPECTF-- or --EXPECTREGEX--
	Sections map[string]string // every section, raw
}

// Parse reads a .phpt file.
func Parse(path string) (*Test, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t := &Test{Path: path, Sections: map[string]string{}}
	var cur string
	var buf bytes.Buffer
	flush := func() {
		if cur != "" {
			t.Sections[cur] = buf.String()
		}
		buf.Reset()
	}
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) > 4 && bytes.HasPrefix(trimmed, []byte("--")) && bytes.HasSuffix(trimmed, []byte("--")) &&
			isSectionName(trimmed[2:len(trimmed)-2]) {
			flush()
			cur = string(trimmed[2 : len(trimmed)-2])
			continue
		}
		buf.Write(line)
	}
	flush()

	t.Name = strings.TrimSpace(t.Sections["TEST"])
	if f, ok := t.Sections["FILE"]; ok {
		t.File = []byte(f)
	} else if f, ok := t.Sections["FILEEOF"]; ok {
		t.File = []byte(strings.TrimRight(f, "\r\n"))
	} else {
		return nil, fmt.Errorf("%s: no --FILE-- section", path)
	}
	for _, s := range []string{"EXPECT", "EXPECTF", "EXPECTREGEX"} {
		if e, ok := t.Sections[s]; ok {
			t.Expect = e
			break
		}
	}
	return t, nil
}

func isSectionName(b []byte) bool {
	for _, c := range b {
		if !(c >= 'A' && c <= 'Z' || c == '_') {
			return false
		}
	}
	return true
}

// ExpectsCompileFailure reports whether the test expects PHP to refuse the
// file before running it (parse error or compile-time fatal).
func (t *Test) ExpectsCompileFailure() bool {
	e := t.Expect
	return strings.Contains(e, "Parse error:") ||
		strings.Contains(e, "syntax error") ||
		strings.Contains(e, "Fatal error:") && strings.Contains(e, "Cannot") ||
		strings.Contains(e, "Fatal error:") && strings.Contains(e, "must")
}
