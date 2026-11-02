package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/emit"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
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

// TestLift converts every testdata/lift/*.php and compares the result with
// the .phn beside it and the notes with the .notes file. PHAENON_UPDATE=1
// rewrites the expectations.
func TestLift(t *testing.T) {
	inputs, _ := filepath.Glob("testdata/lift/*.php")
	if len(inputs) == 0 {
		t.Fatal("no fixtures")
	}
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".php")
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			p, f := liftModel(t, in, src, lexer.ModePHP)
			got, notes, err := Convert(src, Options{Generics: true, Program: p, File: f})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parser.Parse(got, lexer.ModePhaenon); err != nil {
				t.Fatalf("output does not parse: %v\n%s", err, got)
			}
			var b strings.Builder
			for _, n := range notes {
				fmt.Fprintf(&b, "%d: %s\n", n.Line, n.Msg)
			}
			wantFile := strings.TrimSuffix(in, ".php") + ".phn"
			notesFile := strings.TrimSuffix(in, ".php") + ".notes"
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
			// Lifting is idempotent: the output has nothing left to lift.
			p, f = liftModel(t, wantFile, got, lexer.ModePhaenon)
			again, notes2, err := Lift(got, Options{Generics: true, Program: p, File: f})
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != string(got) {
				t.Errorf("second lift changed the output:\n%s", again)
			}
			if len(notes2) != len(notes) {
				t.Errorf("second lift gave %d notes, first %d", len(notes2), len(notes))
			}
			// The lift keeps what PHP sees: compiled, the output declares
			// the native types the input declared.
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
