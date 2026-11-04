package fix

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/emit"
)

// myers finds a longest common subsequence: checked against dynamic
// programming on sequences small enough for it.
func TestMyers(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	seq := func() []string {
		s := make([]string, r.IntN(30))
		for i := range s {
			s[i] = string(rune('a' + r.IntN(4)))
		}
		return s
	}
	for range 2000 {
		a, b := seq(), seq()
		pairs := myers(a, b)
		px, py := -1, -1
		for _, p := range pairs {
			if p[0] <= px || p[1] <= py || a[p[0]] != b[p[1]] {
				t.Fatalf("%v / %v: bad pairs %v", a, b, pairs)
			}
			px, py = p[0], p[1]
		}
		if want := lcsLen(a, b); len(pairs) != want {
			t.Fatalf("%v / %v: %d pairs, the LCS has %d", a, b, len(pairs), want)
		}
	}
}

func lcsLen(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	return dp[0][0]
}

// A change is the text between two tokens both sides keep, narrowed to the
// bytes that differ.
func TestDiff(t *testing.T) {
	old := "<?php\nfunction f($a,$b) {\n    return array($a+$b);\n}\n"
	new := "<?php\nfunction f($a, $b)\n{\n    return [$a + $b];\n}\n"
	var got []string
	for _, h := range Diff([]byte(old), []byte(new)) {
		got = append(got, old[h.Start:h.End]+"→"+h.Text)
	}
	want := []string{"→ ", " →\n", "array(→[", "→ ", "→ ", ")→]"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if Diff([]byte(old), []byte(old)) != nil {
		t.Error("no change, no hunks")
	}
}

const sample = "namespace App;\n\nfun add(int $a,int $b): int => $a+$b;\n\n" +
	"final class Box {\n    fun __construct(private int $n) {}\n\n    fun get(): int => $this->n;\n}\n"

// A change of what the source wrote is carried back; a change of what the
// compiler wrote is refused, and the rebuilt output is the tool's but for
// it.
func TestCarry(t *testing.T) {
	src := []byte(sample)
	old, spans, err := emit.FileVerbatim(src, emit.Options{SourceName: "a.phn", StrictTypes: true})
	if err != nil {
		t.Fatal(err)
	}
	r := strings.NewReplacer(
		"int $a,int $b", "int $a, int $b", // the source's
		"$a+$b", "$a + $b", // the source's, in a body the compiler laid out
		"): int\n    {", "): int {", // the compiler's: an expression body's brace
		"public function get", "final public function get", // inserted where the source begins
	)
	tool := r.Replace(string(old))
	got, refused := Carry(src, old, spans, Diff(old, []byte(tool)))
	want := strings.NewReplacer("int $a,int $b", "int $a, int $b", "$a+$b", "$a + $b", "fun get", "final fun get").Replace(sample)
	if string(got) != want {
		t.Errorf("source\n%s\nwant\n%s", got, want)
	}
	if len(refused) != 1 || string(old[refused[0].Start:refused[0].End]) != "\n   " {
		t.Errorf("refused %+v", refused) // the line break before the brace
	}
	rebuilt, err := emit.File(got, emit.Options{SourceName: "a.phn", StrictTypes: true})
	if err != nil {
		t.Fatal(err)
	}
	unmapped := func(s string) string { return s[:strings.LastIndex(strings.TrimSuffix(s, "\n"), "\n")] }
	if wantOut := strings.Replace(tool, "): int {", "): int\n    {", 1); unmapped(string(rebuilt)) != unmapped(wantOut) {
		t.Errorf("rebuilt\n%s\nwant\n%s", rebuilt, wantOut)
	}
}

// Plain PHP copied as written is one span: every change is carried.
func TestCarryCopy(t *testing.T) {
	src := []byte("<?php\nfunction f($a,$b) {\n    return array($a+$b);\n}\n")
	tool := []byte("<?php\nfunction f($a, $b)\n{\n    return [$a + $b];\n}\n")
	got, refused := Carry(src, src, []emit.Verbatim{{Src: 0, Out: 0, Len: len(src)}}, Diff(src, tool))
	if string(got) != string(tool) || refused != nil {
		t.Errorf("got %q, refused %v", got, refused)
	}
}

// An inserted or deleted run of one byte slides left into a span; an
// insertion where the emitter dropped source text between two spans has no
// one place.
func TestCarrySlide(t *testing.T) {
	// The output AAAA␣␣··BBBB: the source's AAAA␣␣, two blanks the
	// compiler wrote, the source's BBBB; the source held XX in between.
	src := []byte("AAAA  XXBBBB")
	old := []byte("AAAA    BBBB")
	spans := []emit.Verbatim{{Src: 0, Out: 0, Len: 6}, {Src: 8, Out: 8, Len: 4}}
	got, refused := Carry(src, old, spans, []Hunk{{Start: 6, End: 8}})
	if string(got) != "AAAAXXBBBB" || refused != nil {
		t.Errorf("deletion: %q, refused %v", got, refused)
	}
	got, refused = Carry(src, old, spans, []Hunk{{Start: 7, End: 7, Text: "  "}})
	if string(got) != "AAAA    XXBBBB" || refused != nil {
		t.Errorf("insertion: %q, refused %v", got, refused)
	}
	old = []byte("AAAA  BBBB")
	spans = []emit.Verbatim{{Src: 0, Out: 0, Len: 6}, {Src: 8, Out: 6, Len: 4}}
	if _, refused := Carry(src, old, spans, []Hunk{{Start: 6, End: 6, Text: "-"}}); len(refused) != 1 {
		t.Errorf("an insertion between spans apart in the source was carried")
	}
	if got, refused := Carry(src, old, spans, []Hunk{{Start: 6, End: 7, Text: "b"}}); string(got) != "AAAA  XXbBBB" || refused != nil {
		t.Errorf("a change after the junction: %q, refused %v", got, refused)
	}
}

// A deletion whose ends lie in verbatim text takes the source between them
// with it: a removed method goes with the head and the body the compiler
// wrote for it.
func TestCarryDeletion(t *testing.T) {
	src := []byte(sample)
	old, spans, err := emit.FileVerbatim(src, emit.Options{SourceName: "a.phn", StrictTypes: true})
	if err != nil {
		t.Fatal(err)
	}
	method := "\n\n    public function get(): int\n    {\n        return $this->n;\n    }"
	if !strings.Contains(string(old), method) {
		t.Fatalf("no get() in\n%s", old)
	}
	tool := strings.Replace(string(old), method, "", 1)
	got, refused := Carry(src, old, spans, Diff(old, []byte(tool)))
	if want := strings.Replace(sample, "\n\n    fun get(): int => $this->n;", "", 1); string(got) != want || refused != nil {
		t.Errorf("source\n%s\nrefused %v", got, refused)
	}
}
