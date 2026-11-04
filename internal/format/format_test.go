package format

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGolden formats each testdata/X.phn into testdata/X.golden.phn
// (PHAENON_UPDATE=1 rewrites it), and the golden formats to itself.
func TestGolden(t *testing.T) {
	inputs, _ := filepath.Glob("testdata/*.phn")
	for _, in := range inputs {
		if strings.HasSuffix(in, ".golden.phn") {
			continue
		}
		src, err := os.ReadFile(in)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Source(src)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		golden := strings.TrimSuffix(in, ".phn") + ".golden.phn"
		if os.Getenv("PHAENON_UPDATE") != "" {
			os.WriteFile(golden, got, 0o644)
		} else if want, _ := os.ReadFile(golden); !bytes.Equal(got, want) {
			t.Errorf("%s differs from %s (PHAENON_UPDATE=1 to rewrite):\n%s", in, golden, got)
		}
		if again, err := Source(got); err != nil || !bytes.Equal(again, got) {
			t.Errorf("%s: formatting the result changes it again (%v):\n%s", in, err, again)
		}
	}
}

// TestTree formats every .phn under PHAENON_FMT_TREE in memory: each one
// must format, and format to itself the second time. The files it would
// change are listed.
func TestTree(t *testing.T) {
	root := os.Getenv("PHAENON_FMT_TREE")
	if root == "" {
		t.Skip("PHAENON_FMT_TREE names a tree of .phn files")
	}
	files, changed := 0, 0
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && (d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".") && path != root) {
				return filepath.SkipDir
			}
			return err
		}
		if filepath.Ext(path) != ".phn" {
			return nil
		}
		files++
		src, _ := os.ReadFile(path)
		got, err := Source(src)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		if !bytes.Equal(got, src) {
			changed++
			t.Logf("%s: changed", path)
		}
		if again, err := Source(got); err != nil || !bytes.Equal(again, got) {
			t.Errorf("%s: not idempotent (%v)", path, err)
		}
		return nil
	})
	t.Logf("%d files, %d would change", files, changed)
}
