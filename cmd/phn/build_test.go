package main

import "testing"

// TestMapSource pins the path a compiled file's line map names: the source
// relative to the output's directory, forward slashes.
func TestMapSource(t *testing.T) {
	cases := []struct{ src, dst, want string }{
		{"src/Domain/Foo.phn", ".src/Domain/Foo.php", "../../src/Domain/Foo.phn"},
		{"src/Foo.phn", "src/Foo.php", "Foo.phn"},
		{"lib/Foo.phn", "out/lib/Foo.php", "../../lib/Foo.phn"},
	}
	for _, c := range cases {
		if got := mapSource(c.src, c.dst); got != c.want {
			t.Errorf("mapSource(%q, %q) = %q, want %q", c.src, c.dst, got, c.want)
		}
	}
}
