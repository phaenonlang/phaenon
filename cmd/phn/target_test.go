package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/phpversion"
)

func composerWith(t *testing.T, require string) *composerFile {
	t.Helper()
	data := []byte("{\n    \"name\": \"acme/x\",\n    \"require\": {\n        \"php\": \"" + require + "\"\n    }\n}\n")
	c := &composerFile{Path: "composer.json", Data: data}
	if err := json.Unmarshal(data, c); err != nil {
		t.Fatal(err)
	}
	return c
}

func projectWith(php string) *project {
	data := []byte("{\n    \"roots\": {\"src\": \".src\"},\n    \"php\": \"" + php + "\"\n}\n")
	return &project{Roots: map[string]string{"src": ".src"}, PHP: php, data: data}
}

func TestResolveTarget(t *testing.T) {
	cases := []struct {
		name        string
		proj        *project
		composer    *composerFile
		want        phpversion.Version
		codes       string // the diagnostic codes, joined
		unsupported bool
	}{
		{"composer floor", nil, composerWith(t, ">=8.5"), phpversion.PHP85, "", false},
		{"composer caret", nil, composerWith(t, "^8.6"), phpversion.PHP86, "", false},
		{"composer or", nil, composerWith(t, "^8.6 || ^8.5"), phpversion.PHP85, "", false},
		{"stated wins", projectWith("8.6"), composerWith(t, ">=8.6"), phpversion.PHP86, "", false},
		{"stated above composer", projectWith("8.6"), composerWith(t, "^8.5"), phpversion.PHP86, "P4008", false},
		{"stated below composer", projectWith("8.5"), composerWith(t, ">=8.6"), phpversion.PHP85, "", false},
		{"stated unsupported", projectWith("8.4"), composerWith(t, ">=8.4"), phpversion.PHP85, "P4009", true},
		{"stated too new", projectWith("8.7"), nil, phpversion.PHP86, "P4009", true},
		{"stated malformed", projectWith("^8.6"), nil, phpversion.PHP85, "P4009", true},
		{"composer below floor", nil, composerWith(t, ">=8.3"), phpversion.PHP85, "P4008", false},
		{"composer unbounded", nil, composerWith(t, "*"), phpversion.PHP85, "P4008", false},
		{"composer too new", nil, composerWith(t, ">=8.7"), phpversion.PHP86, "P4009", true},
		{"composer unreadable", nil, composerWith(t, "dev-master"), phpversion.PHP85, "P4010", false},
		{"neither", nil, nil, phpversion.PHP85, "P4010", false},
	}
	for _, tc := range cases {
		got := resolveTarget(tc.proj, tc.composer, "x.phn")
		var codes []string
		for _, d := range got.Diags {
			codes = append(codes, d.Code)
		}
		if got.Version != tc.want || strings.Join(codes, ",") != tc.codes || got.Unsupported != tc.unsupported {
			t.Errorf("%s: got %s %v unsupported=%v, want %s [%s] unsupported=%v", tc.name, got.Version, codes, got.Unsupported, tc.want, tc.codes, tc.unsupported)
		}
	}
	// The warning points at the "php" member of phaenon.json.
	d := resolveTarget(projectWith("8.6"), composerWith(t, "^8.5"), "").Diags[0]
	if d.File != ProjectFile || d.Line != 3 || d.Col != 12 {
		t.Errorf("P4008 at %s:%d:%d, want phaenon.json:3:12", d.File, d.Line, d.Col)
	}
	d = resolveTarget(nil, composerWith(t, ">=8.3"), "").Diags[0]
	if d.File != "composer.json" || d.Line != 4 {
		t.Errorf("P4008 at %s:%d, want composer.json:4", d.File, d.Line)
	}
}

func TestSetProjectTarget(t *testing.T) {
	t.Chdir(t.TempDir())
	write := func(s string) { os.WriteFile(ProjectFile, []byte(s), 0o644) }
	read := func() string { b, _ := os.ReadFile(ProjectFile); return string(b) }
	cases := []struct{ before, after string }{
		{"", "{\n    \"php\": \"8.6\"\n}\n"},
		{"{\n    \"roots\": {\n        \"src\": \".src\"\n    }\n}\n", "{\n    \"roots\": {\n        \"src\": \".src\"\n    },\n    \"php\": \"8.6\"\n}\n"},
		{`{"roots": {"src": ".src"}}`, `{"roots": {"src": ".src"}, "php": "8.6"}`},
		{"{\n  \"php\": \"8.5\",\n  \"roots\": {\"src\": \".src\"}\n}\n", "{\n  \"php\": \"8.6\",\n  \"roots\": {\"src\": \".src\"}\n}\n"},
		{"{}\n", "{\n    \"php\": \"8.6\"\n}\n"},
	}
	for _, tc := range cases {
		os.Remove(ProjectFile)
		if tc.before != "" {
			write(tc.before)
		}
		if err := setProjectTarget(phpversion.PHP86); err != nil {
			t.Fatalf("%q: %v", tc.before, err)
		}
		if got := read(); got != tc.after {
			t.Errorf("%q:\ngot  %q\nwant %q", tc.before, got, tc.after)
		}
	}
}
