package format

import (
	"bytes"
	"testing"
)

// FuzzSource holds Source to what its comment promises: formatting the
// result again changes nothing. A source that does not parse is not
// formatted and is passed over.
//
// The seeds are the two sources the property was first found broken on,
// and go test runs them each time: a heredoc whose closing indentation
// mixes spaces and a tab (internal/tabs), and a docblock that fits its
// line only once the class brace has moved down (migrate's layout). php
// refuses the first; the parser reads it, so phn fmt lays it out.
//
//	go test ./internal/format -run '^$' -fuzz FuzzSource -fuzztime 2m
func FuzzSource(f *testing.F) {
	f.Add([]byte("class A{a(){<<<EOT\n000\t\n  \tEOT;}}"))
	f.Add([]byte("{}interface A{public const A=0;public const A=0;public const A=0;/** 0000000000000000000000000000000000000000000000000\n*/ }"))
	f.Fuzz(func(t *testing.T, src []byte) {
		once, err := Source(src)
		if err != nil {
			return
		}
		twice, err := Source(once)
		if err != nil {
			t.Fatalf("the result does not format: %v\nsource: %q\nresult: %q", err, src, once)
		}
		if !bytes.Equal(once, twice) {
			t.Fatalf("formatting the result changes it again\nsource: %q\n  once: %q\n twice: %q", src, once, twice)
		}
	})
}
