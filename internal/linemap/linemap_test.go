package linemap

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vectors is testdata/vectors.json, the reference the three readers share.
type vectors struct {
	Valid []struct {
		Name    string   `json:"name"`
		Map     string   `json:"map"`
		Source  string   `json:"source"`
		Forward [][2]int `json:"forward"`
		Reverse [][2]any `json:"reverse"` // [source line, output line or null]
	} `json:"valid"`
	Unsupported []string `json:"unsupported"`
	Invalid     []string `json:"invalid"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.Valid {
		t.Run(c.Name, func(t *testing.T) {
			m, err := Parse(c.Map)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if m.Source != c.Source {
				t.Errorf("source %q, want %q", m.Source, c.Source)
			}
			for _, q := range c.Forward {
				if got, ok := m.Forward(q[0]); !ok || got != q[1] {
					t.Errorf("Forward(%d) = %d, %v; want %d", q[0], got, ok, q[1])
				}
			}
			for _, q := range c.Reverse {
				src := int(q[0].(float64))
				got, ok := m.Reverse(src)
				if q[1] == nil {
					if ok {
						t.Errorf("Reverse(%d) = %d; want no target", src, got)
					}
					continue
				}
				if want := int(q[1].(float64)); !ok || got != want {
					t.Errorf("Reverse(%d) = %d, %v; want %d", src, got, ok, want)
				}
			}
			// The writer spells a map the way the reader parsed it, the
			// inline-HTML prefix aside.
			if want := strings.TrimPrefix(c.Map, "<?php "); m.String() != want {
				t.Errorf("String() = %q, want %q", m.String(), want)
			}
		})
	}
	for _, line := range v.Unsupported {
		if _, err := Parse(line); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%q: got %v, want ErrUnsupported", line, err)
		}
	}
	for _, line := range v.Invalid {
		if m, err := Parse(line); err == nil || errors.Is(err, ErrUnsupported) {
			t.Errorf("%q: accepted (%v, %v)", line, m, err)
		}
	}
}

// TestVectorCopiesFresh compares the reference vectors with the copies the
// other readers test against: the PhpStorm plugin's and phaenon/debug's,
// in the sibling checkouts. A copy whose checkout or file is absent is
// skipped.
func TestVectorCopiesFresh(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join("..", "..", "..")
	copies := map[string]string{
		"jetbrains": filepath.Join("src", "test", "resources", "linemap", "vectors.json"),
		"debug":     filepath.Join("tests", "Debug", "vectors.json"),
	}
	for repo, rel := range copies {
		t.Run(repo, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(workspace, repo)); err != nil {
				t.Skipf("no %s checkout", repo)
			}
			got, err := os.ReadFile(filepath.Join(workspace, repo, rel))
			if err != nil {
				t.Skipf("%s has no copy of the vectors yet", repo)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s/%s differs from internal/linemap/testdata/vectors.json: copy the reference over", repo, rel)
			}
		})
	}
}

func TestBuild(t *testing.T) {
	lines := []Line{
		{1, false}, {1, false}, {1, false}, // the header
		{1, true}, {2, true}, // namespace, blank
		{3, false}, {3, false}, // a docblock the compiler wrote
		{3, true}, {3, false}, // a declaration, its brace moved down
		{4, true}, {6, true}, // line 5 folded away
	}
	m := Build("Foo.phn", lines)
	if got, want := m.String(), "// phaenon-map v1 Foo.phn 1-3=1 4:1 6-7=3 8:3 9-9=3 10:4 11:6"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	again, err := Parse(m.String())
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range lines {
		if got, _ := again.Forward(i + 1); got != l.Src {
			t.Errorf("Forward(%d) = %d, want %d", i+1, got, l.Src)
		}
	}
	if got, _ := again.Reverse(5); got != 11 {
		t.Errorf("Reverse(5) = %d, want 11 (line 6's target)", got)
	}
}

func TestPathEncoding(t *testing.T) {
	for _, p := range []string{"Foo.phn", "../src/My Templates/100%.phn", "%20 literal.phn"} {
		got, err := decodePath(EncodePath(p))
		if err != nil || got != p {
			t.Errorf("%q: round trip gave %q, %v", p, got, err)
		}
	}
	if _, err := decodePath("a%41.phn"); err == nil {
		t.Error("an escape the writer never writes was accepted")
	}
}

func TestFind(t *testing.T) {
	m := "// phaenon-map v1 Foo.phn 1-4=1 5:1"
	cases := []struct {
		name, file string
		found      bool
	}{
		{"last line", "<?php\n\n/* x */\n\necho 1;\n" + m + "\n", true},
		{"last line, no final break", "<?php\necho 1;\n" + m, true},
		{"crlf", "<?php\r\necho 1;\r\n" + m + "\r\n", true},
		{"inline html spelling", "<?php echo 1; ?>\n<p>\n<?php " + m + "\n", true},
		{"before __halt_compiler", "<?php\necho 1;\n" + m + "\n__halt_compiler();\n// phaenon-map is data here\n", true},
		{"no map", "<?php\necho 1;\n", false},
		{"a map line elsewhere is not the map", "<?php\n" + m + "\necho 1;\n", false},
	}
	for _, c := range cases {
		got, err := Find([]byte(c.file))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if (got != nil) != c.found {
			t.Errorf("%s: found = %v, want %v", c.name, got != nil, c.found)
		}
	}
	if _, err := Find([]byte("<?php\n// phaenon-map v2 Foo.phn 1:1\n")); !errors.Is(err, ErrUnsupported) {
		t.Errorf("v2: got %v, want ErrUnsupported", err)
	}
	// One trailing carriage return is stripped before parsing (§3.1).
	if _, err := Parse(m + "\r"); err != nil {
		t.Errorf("a trailing \\r: %v", err)
	}
	if _, err := Parse(m + "\r\r"); err == nil {
		t.Error("two trailing \\r were accepted")
	}
}
