package emit

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/linemap"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/stdlib"
)

// TestLineMapProperty is the emitter's line invariant since the line rule
// went (design/line-map.md §4.1): every source byte that reaches the
// output unchanged, whitespace aside, sits on an output line whose forward
// lookup gives the byte's source line. It runs over the emitter goldens,
// the stdlib written in Phaenon and the checker's corpus (each file that
// compiles), and checks the map itself: valid, naming the source, covering
// the output, with reverse targets that look up forward to their line.
func TestLineMapProperty(t *testing.T) {
	type input struct {
		path string
		src  []byte
	}
	var inputs []input
	globs := []string{filepath.Join("testdata", "*.phn"), filepath.Join("..", "checker", "testdata", "*.phn")}
	for _, g := range globs {
		files, _ := filepath.Glob(g)
		for _, path := range files {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			inputs = append(inputs, input{path, src})
		}
	}
	for _, s := range stdlib.Runtime() {
		if strings.HasSuffix(s.Path, ".phn") {
			inputs = append(inputs, input{"<stub>/" + s.Path, s.Src})
		}
	}
	compiled := 0
	for _, in := range inputs {
		p := program.New()
		// The stdlib is compiled against the libraries it suggests, as
		// phn stdlib compiles it.
		if err := p.LoadSuggested(); err != nil {
			t.Fatal(err)
		}
		var f *program.File
		if strings.HasPrefix(in.path, "<stub>/") {
			f = p.File(in.path)
		} else {
			var err error
			if f, err = p.AddFile(in.path, in.src, lexer.ModePhaenon, true); err != nil {
				continue // a corpus file that is a syntax error on purpose
			}
			checker.New(p, checker.Options{}).Check()
		}
		out, w, err := compileRecovered(t, in.path, in.src, p, f)
		if err != nil || out == nil {
			t.Logf("%s: not compiled: %v", in.path, err)
			continue
		}
		compiled++
		checkLineMap(t, in.path, in.src, out, w)
		checkVerbatim(t, in.path, in.src, out, w.verbatim())
	}
	if compiled < 50 {
		t.Errorf("only %d files compiled: the property needs a corpus", compiled)
	}
}

// compileRecovered compiles one file, reporting a panic as a failure of
// that file.
func compileRecovered(t *testing.T, path string, src []byte, p *program.Program, f *program.File) (out []byte, w *lineWriter, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: emitter panicked: %v", path, r)
			out, w = nil, nil
		}
	}()
	return compile(src, f.AST, Options{SourceName: path, StrictTypes: true, Program: p, File: f})
}

func checkLineMap(t *testing.T, path string, src, out []byte, w *lineWriter) {
	t.Helper()
	m, err := linemap.Find(out)
	if err != nil || m == nil {
		t.Errorf("%s: no valid line map (%v):\n%s", path, err, out)
		return
	}
	if want := filepath.Base(path); m.Source != want {
		t.Errorf("%s: the map names %q", path, m.Source)
	}
	outLines := bytes.Count(out, []byte("\n")) // the map's own line ends the file
	if last := m.Entries[len(m.Entries)-1]; last.Out > outLines-1 || last.Hold && last.End > outLines-1 {
		t.Errorf("%s: the last entry %v lies beyond the %d mapped lines", path, last, outLines-1)
	}
	srcLine := lineIndex(src)
	outLine := lineIndex(out)
	reported := 0
	for _, s := range w.segments {
		for i := s.Start; i < s.End; i++ {
			if isBlank(src[i]) || src[i] == '\n' {
				continue
			}
			want := srcLine(i)
			o := outLine(s.Out + i - s.Start)
			if got, _ := m.Forward(o); got != want && reported < 5 {
				reported++
				t.Errorf("%s: output line %d maps to %d, but holds %q from source line %d\n%s", path, o, got, lineText(out, o), want, m)
			}
		}
	}
	srcLines := bytes.Count(src, []byte("\n")) + 1
	for s := 1; s <= srcLines; s++ {
		o, ok := m.Reverse(s)
		if !ok {
			continue
		}
		if back, _ := m.Forward(o); back < s && reported < 5 {
			reported++
			t.Errorf("%s: source line %d goes to output line %d, which maps back to %d", path, s, o, back)
		}
	}
}

// checkVerbatim: every verbatim span holds the source bytes it names, in
// output order, none overlapping another on either side (phn fix relies on
// it); the source order is the output's but for the runs a folded pipe
// moves.
func checkVerbatim(t *testing.T, path string, src, out []byte, vs []Verbatim) {
	t.Helper()
	prev := Verbatim{}
	for i, v := range vs {
		if v.Len <= 0 || v.Out+v.Len > len(out) || v.Src+v.Len > len(src) ||
			!bytes.Equal(out[v.Out:v.Out+v.Len], src[v.Src:v.Src+v.Len]) {
			t.Errorf("%s: verbatim span %+v does not hold its source bytes", path, v)
			return
		}
		if i > 0 && v.Out < prev.Out+prev.Len {
			t.Errorf("%s: verbatim span %+v overlaps or precedes %+v", path, v, prev)
			return
		}
		if i > 0 && v.Out == prev.Out+prev.Len && v.Src == prev.Src+prev.Len {
			t.Errorf("%s: verbatim spans %+v and %+v continue each other", path, prev, v)
		}
		prev = v
	}
	bySrc := slices.Clone(vs)
	slices.SortFunc(bySrc, func(a, b Verbatim) int { return a.Src - b.Src })
	for i := 1; i < len(bySrc); i++ {
		if a, b := bySrc[i-1], bySrc[i]; b.Src < a.Src+a.Len {
			t.Errorf("%s: verbatim spans %+v and %+v share source bytes", path, a, b)
			return
		}
	}
}

// lineIndex returns a function giving the 1-based line of a byte offset.
func lineIndex(text []byte) func(int) int {
	var starts []int
	for i, c := range text {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return func(off int) int {
		lo, hi := 0, len(starts)
		for lo < hi {
			mid := (lo + hi) / 2
			if starts[mid] <= off {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		return lo + 1
	}
}

func lineText(text []byte, n int) string {
	lines := strings.Split(string(text), "\n")
	if n < 1 || n > len(lines) {
		return ""
	}
	return lines[n-1]
}

// The map line written before __halt_compiler() moves the spans after it.
func TestVerbatimHaltCompiler(t *testing.T) {
	src := []byte("namespace App;\n\necho 1;\n__halt_compiler();raw data\n")
	out, vs, err := FileVerbatim(src, Options{SourceName: "a.phn", StrictTypes: true})
	if err != nil {
		t.Fatal(err)
	}
	checkVerbatim(t, "a.phn", src, out, vs)
	if last := vs[len(vs)-1]; string(out[last.Out:last.Out+last.Len]) != "__halt_compiler();raw data\n" {
		t.Errorf("last span %q:\n%s", out[last.Out:last.Out+last.Len], out)
	}
}
