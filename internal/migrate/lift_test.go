package migrate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
	"github.com/phaenonlang/phaenon/internal/types"
)

// liftModel is the program model a fixture is lifted with, as phn migrate
// builds it: the stubs, the classes under testdata/lift/vendor (PSR-4 root
// Acme\, vendor PHP and project files already in Phaenon), and the file.
func liftModel(t *testing.T, path string, src []byte, mode lexer.Mode) (*program.Program, *program.File) {
	t.Helper()
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	p.AddLoader(program.DirLoader{Prefix: "Acme\\", Dir: filepath.Join("testdata", "lift", "vendor", "Acme")})
	f, err := p.AddFile(path, src, mode, false)
	if err != nil {
		t.Fatal(err)
	}
	return p, f
}

// pipeLoad builds the program model of a pipes run over fixtures: the
// stubs, the vendor fixtures, the sources.
func pipeLoad(srcs map[string][]byte, checked map[string]bool) (*program.Program, error) {
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		return nil, err
	}
	p.AddLoader(program.DirLoader{Prefix: "Acme\\", Dir: filepath.Join("testdata", "lift", "vendor", "Acme")})
	paths := make([]string, 0, len(srcs))
	for path := range srcs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if _, err := p.AddFile(path, srcs[path], lexer.ModePhaenon, checked[path]); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return p, nil
}

// fixturePipes is the pipes threshold a fixture's name asks for: off in
// one named keep-calls (--keep-calls), 2 in one named pipes-2 (--pipes=2),
// 3 otherwise.
func fixturePipes(name string) int {
	switch {
	case strings.HasSuffix(name, "keep-calls"):
		return 0
	case strings.HasSuffix(name, "pipes-2"):
		return 2
	}
	return 3
}

// migrateFixture runs what phn migrate runs on a file: Prepare, Pipes,
// Finish. The notes are the lift's, then the pipes' at the lines of the
// lifted source.
func migrateFixture(t *testing.T, in, dst string, src []byte, layout bool, pipes int) ([]byte, []Note) {
	t.Helper()
	p, f := liftModel(t, in, src, lexer.ModePHP)
	opts := Options{Generics: true, Layout: layout, Program: p, File: f}
	pre, notes, closeTag, err := Prepare(src, opts)
	if err != nil {
		t.Fatal(err)
	}
	piped, rep, err := Pipes([]PipeFile{{Path: dst, Src: pre}}, PipeOptions{MinCalls: pipes, Load: pipeLoad})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Finish(piped[0], closeTag, opts)
	if err != nil {
		t.Fatal(err)
	}
	notes = append(notes, rep.Notes[dst]...)
	notes = append(notes, rep.Undone...)
	return got, notes
}

// TestLift converts every testdata/lift/*.php as phn migrate does — tags
// lifted, chains of three calls or more written as pipelines (see
// fixturePipes), the canonical layout applied, except in a fixture named
// keep-layout — and compares the result with the .phn beside it and the
// notes with the .notes file. PHAENON_UPDATE=1 rewrites the expectations.
// With PHAENON_PHP86, a fixture named pipes* runs as written and compiled
// from its .phn, under PHP 8.6: they must print the same.
func TestLift(t *testing.T) {
	inputs, _ := filepath.Glob("testdata/lift/*.php")
	if len(inputs) == 0 {
		t.Fatal("no fixtures")
	}
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".php")
		layout := !strings.HasSuffix(name, "keep-layout")
		pipes := fixturePipes(name)
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			wantFile := strings.TrimSuffix(in, ".php") + ".phn"
			notesFile := strings.TrimSuffix(in, ".php") + ".notes"
			got, notes := migrateFixture(t, in, wantFile, src, layout, pipes)
			if _, err := parser.Parse(got, lexer.ModePhaenon); err != nil {
				t.Fatalf("output does not parse: %v\n%s", err, got)
			}
			var b strings.Builder
			lifted := 0
			for _, n := range notes {
				fmt.Fprintf(&b, "%d: %s\n", n.Line, n.Msg)
				if !strings.Contains(n.Msg, "left as calls") && !strings.Contains(n.Msg, "pipelines undone") {
					lifted++
				}
			}
			if os.Getenv("PHAENON_UPDATE") != "" {
				os.WriteFile(wantFile, got, 0o644)
				os.WriteFile(notesFile, []byte(b.String()), 0o644)
				return
			}
			want, _ := os.ReadFile(wantFile)
			if string(want) != string(got) {
				t.Errorf("output differs from %s (PHAENON_UPDATE=1 to rewrite):\n%s", wantFile, got)
			}
			wantNotes, _ := os.ReadFile(notesFile)
			if string(wantNotes) != b.String() {
				t.Errorf("notes differ from %s (PHAENON_UPDATE=1 to rewrite):\n%s", notesFile, b.String())
			}
			// Migrating is idempotent: the output has nothing left to lift,
			// no chain left to rewrite, nothing to lay out.
			p, f := liftModel(t, wantFile, got, lexer.ModePhaenon)
			again, notes2, err := Lift(got, Options{Generics: true, Program: p, File: f})
			if err != nil {
				t.Fatal(err)
			}
			piped, rep, err := Pipes([]PipeFile{{Path: wantFile, Src: again}}, PipeOptions{MinCalls: pipes, Load: pipeLoad})
			if err != nil {
				t.Fatal(err)
			}
			again = piped[0]
			if rep.Pipelines > 0 {
				t.Errorf("second run wrote %d more pipelines", rep.Pipelines)
			}
			if layout {
				if again, err = Layout(again, nil); err != nil {
					t.Fatal(err)
				}
			}
			if string(again) != string(got) {
				t.Errorf("second run changed the output:\n%s", again)
			}
			if len(notes2) != lifted {
				t.Errorf("second lift gave %d notes, first %d", len(notes2), lifted)
			}
			if strings.HasPrefix(name, "pipes") {
				sameOutput(t, in, wantFile, got)
			}
			// The lift keeps what PHP sees: compiled, the output declares
			// the native types the input declared. Checked, so that the
			// emitter has the checker's records of partial applications.
			f.Checked = true
			checker.New(p, checker.Options{}).Check()
			compiled, err := emit.File(got, emit.Options{SourceName: wantFile, StrictTypes: true, Program: p, File: f})
			if err != nil {
				t.Fatal(err)
			}
			if diff := signatureDiff(t, in, src, compiled); diff != "" {
				t.Errorf("the compiled output changed PHP signatures:\n%s", diff)
			}
		})
	}
}

// sameOutput runs a fixture as written and its migrated .phn compiled for
// PHP 8.6 and for PHP 8.5 under PHAENON_PHP86, and the original and the 8.5
// output under the php on PATH when it is an 8.5: each pair must print the
// same, so a pipeline behaves as the nested calls did, evaluation order
// included.
func sameOutput(t *testing.T, in, phnPath string, phn []byte) {
	t.Helper()
	php86 := os.Getenv("PHAENON_PHP86")
	if php86 == "" {
		return
	}
	run := func(php, path string) string {
		out, err := exec.Command(php, "-n", "-d", "display_errors=1", "-d", "error_reporting=-1", path).CombinedOutput()
		if err != nil {
			t.Errorf("%s %s: %v\n%s", php, path, err, out)
		}
		return string(out)
	}
	dir := t.TempDir()
	compiled := map[phpversion.Version]string{}
	for _, target := range []phpversion.Version{phpversion.PHP85, phpversion.PHP86} {
		p, err := pipeLoad(map[string][]byte{phnPath: phn}, map[string]bool{phnPath: true})
		if err != nil {
			t.Fatal(err)
		}
		p.SetTarget(target)
		f := p.File(phnPath)
		for _, d := range checker.New(p, checker.Options{}).Check() {
			// P2112 is the by-reference refusal's own call, a call result
			// passed by reference, which PHP runs with a reference-returning
			// function.
			if d.File == phnPath && d.Severity == checker.Error && d.Code != checker.P2112.ID {
				t.Errorf("checker (target %s): %s:%d: %s: %s", target, d.File, d.Line, d.Code, d.Message)
			}
		}
		out, err := emit.File(phn, emit.Options{SourceName: phnPath, StrictTypes: true, Program: p, File: f})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, target.String()+".php")
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
		compiled[target] = path
	}
	want := run(php86, in)
	if want == "" {
		t.Errorf("%s prints nothing", in)
	}
	for _, target := range []phpversion.Version{phpversion.PHP86, phpversion.PHP85} {
		if got := run(php86, compiled[target]); got != want {
			t.Errorf("compiled for %s, under PHP 8.6, prints differently from the original:\n--- got\n%s\n--- want\n%s", target, got, want)
		}
	}
	t.Logf("%s: %d lines, the same under PHP 8.6 as written and compiled for 8.5 and 8.6", in, strings.Count(want, "\n"))
	php, _ := exec.LookPath("php")
	if php == "" {
		return
	}
	if v, err := exec.Command(php, "-n", "-r", "echo PHP_MAJOR_VERSION, '.', PHP_MINOR_VERSION;").Output(); err != nil || string(v) != "8.5" {
		return
	}
	if err := exec.Command(php, "-n", "-l", in).Run(); err != nil {
		return // the fixture is written in PHP 8.6
	}
	if got, want := run(php, compiled[phpversion.PHP85]), run(php, in); got != want {
		t.Errorf("compiled for 8.5, under PHP 8.5, prints differently from the original:\n--- got\n%s\n--- want\n%s", got, want)
	}
	t.Logf("%s: the same under PHP 8.5 as written and compiled for 8.5", in)
}

// signatureDiff compares the native signatures of the functions and methods
// declared in two PHP sources, each read against the vendor fixtures.
func signatureDiff(t *testing.T, path string, before, after []byte) string {
	t.Helper()
	signatures := func(src []byte) map[string]string {
		p, f := liftModel(t, path, src, lexer.ModePHP)
		out := map[string]string{}
		native := func(ty types.Type) string {
			if ty == nil {
				return "-"
			}
			s, _ := types.Native(ty)
			return s
		}
		for _, cl := range p.Classes() {
			if cl.File != f {
				continue
			}
			cl.Signature()
			for _, m := range cl.Methods {
				if m.Class != cl || m.Magic {
					continue
				}
				var ps []string
				for _, pr := range m.Params {
					ps = append(ps, native(pr.NativeType))
				}
				out[cl.Name+"::"+m.Name] = "(" + strings.Join(ps, ", ") + "): " + native(m.NativeReturn)
			}
		}
		for _, fn := range p.Functions() {
			if fn.File != f {
				continue
			}
			fn.Signature()
			var ps []string
			for _, pr := range fn.Params {
				ps = append(ps, native(pr.NativeType))
			}
			out[fn.Name] = "(" + strings.Join(ps, ", ") + ")"
		}
		return out
	}
	was, is := signatures(before), signatures(after)
	var b strings.Builder
	for name, sig := range was {
		if is[name] != sig {
			fmt.Fprintf(&b, "%s: %s became %s\n", name, sig, is[name])
		}
	}
	return b.String()
}

// A .phn lifted a second time keeps the types its declarations spell: a
// docblock left over from an earlier edit is not their source.
func TestLiftKeepsAWrittenType(t *testing.T) {
	src := "namespace App;\n\nfinal class Metrics\n{\n    /** @var \\ArrayObject<int, mixed> */\n    private \\ArrayObject<int, Metric> $exported;\n\n    /** @var string the raw frame */\n    private bytes $frame;\n}\n"
	want := "namespace App;\n\nfinal class Metrics\n{\n    private \\ArrayObject<int, Metric> $exported;\n\n    /** the raw frame */\n    private bytes $frame;\n}\n"
	got, notes, err := Lift([]byte(src), Options{Generics: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if len(notes) != 0 {
		t.Errorf("unexpected notes: %v", notes)
	}
}

// A .phn whose class-like braces an earlier migrate moved up gets them back
// on lines of their own (design/line-map.md §7); a function's and an
// anonymous class's stay up, and a second run changes nothing.
func TestLayoutMovesClassBracesDown(t *testing.T) {
	src := "final class A implements \\Countable {\n    fun count(): int {\n        $n = 1;\n        return $n;\n    }\n\n    fun make(): object {\n        $o = new class {\n        };\n        return $o;\n    }\n}\n\ninterface I {\n    fun f(): void;\n}\n\n    enum E: string {\n        case X = 'x';\n    }\n"
	want := "final class A implements \\Countable\n{\n    fun count(): int {\n        $n = 1;\n        return $n;\n    }\n\n    fun make(): object {\n        $o = new class {\n        };\n        return $o;\n    }\n}\n\ninterface I\n{\n    fun f(): void;\n}\n\n    enum E: string\n    {\n        case X = 'x';\n    }\n"
	var stats Stats
	got, err := Layout([]byte(src), &stats)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if stats.Down != 3 || stats.Braces != 0 {
		t.Errorf("stats %+v, want 3 class braces down and none up", stats)
	}
	again, err := Layout(got, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(got) {
		t.Errorf("a second run changed the layout:\n%s", again)
	}
}

// An empty class-like is `{}` on its header's line; one with a comment
// inside keeps its lines.
func TestLayoutWritesEmptyClassesAsBraces(t *testing.T) {
	src := "final class NotFound extends \\RuntimeException\n{\n}\n\ninterface Marker {\n}\n\nclass Kept\n{\n    // reserved\n}\n"
	want := "final class NotFound extends \\RuntimeException {}\n\ninterface Marker {}\n\nclass Kept\n{\n    // reserved\n}\n"
	var stats Stats
	got, err := Layout([]byte(src), &stats)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if stats.Empty != 2 || stats.Down != 0 {
		t.Errorf("stats %+v, want 2 empty bodies and no brace moved down", stats)
	}
	if again, _ := Layout(got, nil); string(again) != string(got) {
		t.Errorf("a second run changed the layout:\n%s", again)
	}
}

// A prose-only @param whose description starts on the next line keeps it:
// only a tag with no prose at all goes.
func TestLiftKeepsParamProseOnLaterLines(t *testing.T) {
	src := "final class Api\n{\n    /**\n     * @param $transport -\n     *                   [status, body] — test seam\n     * @param $other\n     *               the other seam\n     * @param $none\n     */\n    fun __construct(?Closure $transport = null, ?Closure $other = null, int $none = 0)\n    {\n    }\n}\n"
	want := "final class Api\n{\n    /**\n     * @param $transport -\n     *                   [status, body] — test seam\n     * @param $other -\n     *               the other seam\n     */\n    fun __construct(?Closure $transport = null, ?Closure $other = null, int $none = 0)\n    {\n    }\n}\n"
	got, notes, err := Lift([]byte(src), Options{Generics: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if len(notes) != 0 {
		t.Errorf("unexpected notes: %v", notes)
	}
}
