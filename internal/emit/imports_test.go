package emit

import (
	"fmt"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/linemap"
	"github.com/phaenonlang/phaenon/internal/program"
)

// source is the interface the cases implement. Its parameter's bound,
// Random\Engine, is what the override-erasure rule widens pick()'s
// parameter to (design/generics.md §4.3): a class the compiler names and
// imports when it can (roadmap D11).
const source = "namespace Lib;\n\ninterface Source<T of \\Random\\Engine>\n{\n    public function pick(T $engine): int;\n}\n"

// dice implements it with body as pick()'s statement; diced is the compiled
// class, the widened parameter named engine.
func dice(body string) string {
	return "final class Dice implements \\Lib\\Source<\\Random\\Engine\\Mt19937>\n{\n    public function pick(\\Random\\Engine\\Mt19937 $engine): int\n    {\n        " + body + "\n    }\n}\n"
}

func diced(engine string) string {
	return "/**\n * @implements \\Lib\\Source<\\Random\\Engine\\Mt19937>\n */\nfinal class Dice implements \\Lib\\Source\n{\n    /**\n     * @param \\Random\\Engine\\Mt19937 $engine\n     */\n    public function pick(" + engine + " $engine): int\n    {\n        return \\strlen($engine->generate());\n    }\n}\n"
}

const roll = "return \\strlen($engine->generate());"

// TestImportPlacement pins where an import the compiler adds goes (roadmap
// D11, line-map.md §2): on a line of its own, beside its neighbour in the
// file's use block, or in a block of its own after the namespace. The
// goldens imports-*.phn show the common cases under PHPStan; these are the
// edges. want is the output between the header and the line map.
func TestImportPlacement(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{
			name: "sorts before the first import",
			src:  "namespace App;\n\nuse Stringable;\n\n" + dice(roll),
			want: "namespace App;\n\nuse Random\\Engine;\nuse Stringable;\n\n" + diced("Engine"),
		},
		{
			name: "sorts after the last import",
			src:  "namespace App;\n\nuse ArrayObject;\nuse Countable;\n\n" + dice(roll),
			want: "namespace App;\n\nuse ArrayObject;\nuse Countable;\nuse Random\\Engine;\n\n" + diced("Engine"),
		},
		{
			name: "after a neighbour with a comment: below the comment's line",
			src:  "namespace App;\n\nuse ArrayObject; // why\nuse Stringable;\n\n" + dice(roll),
			want: "namespace App;\n\nuse ArrayObject; // why\nuse Random\\Engine;\nuse Stringable;\n\n" + diced("Engine"),
		},
		{
			name: "a group use sorts by its prefix",
			src:  "namespace App;\n\nuse App\\Model\\{User, Group};\nuse Stringable;\n\n" + dice(roll),
			want: "namespace App;\n\nuse App\\Model\\{User, Group};\nuse Random\\Engine;\nuse Stringable;\n\n" + diced("Engine"),
		},
		{
			name: "only function imports: the class import goes first, a group of its own",
			src:  "namespace App;\n\nuse function strlen;\n\n" + dice(roll),
			want: "namespace App;\n\nuse Random\\Engine;\n\nuse function strlen;\n\n" + diced("Engine"),
		},
		{
			name: "no imports: a block after the namespace, before a generated docblock",
			src:  "namespace App;\n\n" + dice(roll),
			want: "namespace App;\n\nuse Random\\Engine;\n\n" + diced("Engine"),
		},
		{
			name: "no imports, no blank line after the namespace: the block gets one",
			src:  "namespace App;\n" + dice(roll),
			want: "namespace App;\n\nuse Random\\Engine;\n\n" + diced("Engine"),
		},
		{
			name: "no imports, the statement on the namespace's line",
			src:  "namespace App; const SIDES = 6;\n\n" + dice(roll),
			want: "namespace App;\n\nuse Random\\Engine;\n\nconst SIDES = 6;\n\n" + diced("Engine"),
		},
		{
			name: "no namespace: at the start",
			src:  dice(roll),
			want: "use Random\\Engine;\n\n" + diced("Engine"),
		},
		{
			name: "no namespace, an own declare: after it",
			src:  "declare(strict_types=1);\n\n" + dice(roll),
			want: "declare(strict_types=1);\n\nuse Random\\Engine;\n\n" + diced("Engine"),
		},
		{
			name: "the file imports the class itself: nothing added",
			src:  "namespace App;\n\nuse Random\\Engine as Rng;\n\n" + dice(roll),
			want: "namespace App;\n\nuse Random\\Engine as Rng;\n\n" + diced("Rng"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := program.New()
			if err := p.LoadStubs(); err != nil {
				t.Fatal(err)
			}
			if _, err := p.AddFile("lib.phn", []byte(source), lexer.ModePhaenon, true); err != nil {
				t.Fatal(err)
			}
			f, err := p.AddFile("x.phn", []byte(c.src), lexer.ModePhaenon, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range checker.New(p, checker.Options{}).Check() {
				if d.Severity == checker.Error {
					t.Fatalf("checker: %s:%d: %s: %s", d.File, d.Line, d.Code, d.Message)
				}
			}
			out, w, err := compile([]byte(c.src), f.AST, Options{SourceName: "x.phn", StrictTypes: true, Program: p, File: f})
			if err != nil {
				t.Fatal(err)
			}
			body := strings.TrimPrefix(string(out), "<?php\n\n/* @generated by phaenon from x.phn */\n\n")
			if !strings.HasPrefix(c.src, "declare") {
				body = strings.TrimPrefix(body, "declare(strict_types=1);\n\n")
			}
			if i := strings.LastIndex(body, "// phaenon-map "); i >= 0 {
				body = body[:i]
			}
			if body != c.want {
				t.Errorf("got:\n%s\nwant:\n%s", body, c.want)
			}
			checkLineMap(t, "x.phn", []byte(c.src), out, w)
		})
	}
}

// TestImportShadowing pins when a short name is not free for an import the
// compiler adds (roadmap D11): a class of the file's namespace with that
// name, and a relative qualified name — in code or in a docblock — whose
// first segment is that name, which PHP and PHPStan would resolve through
// the import. A name that merely starts with the same letters
// (`Engines\Check` against `Engine`) or is fully qualified leaves it free.
// want is the compiled line of pick()'s signature, found through the line
// map.
func TestImportShadowing(t *testing.T) {
	check := "namespace App\\Engine;\n\nfinal class Check\n{\n    public static fun size(object $o): int\n    {\n        return 1;\n    }\n}\n"
	checks := "namespace App\\Engines;\n\nfinal class Check\n{\n    public static fun size(object $o): int\n    {\n        return 1;\n    }\n}\n"
	sameName := "namespace App;\n\nfinal class Engine\n{\n}\n"
	cases := []struct {
		name   string
		others []string
		src    string
		want   string
	}{
		{
			name:   "a relative qualified name begins with the short name: FQCN",
			others: []string{check},
			src:    "namespace App;\n\n" + dice("return Engine\\Check::size($engine);"),
			want:   "    public function pick(\\Random\\Engine $engine): int",
		},
		{
			name:   "a docblock's relative qualified name begins with it: FQCN",
			others: []string{check},
			src:    "namespace App;\n\n/** @param Engine\\Check $c */\nfun checked(object $c): object\n{\n    return $c;\n}\n\n" + dice(roll),
			want:   "    public function pick(\\Random\\Engine $engine): int",
		},
		{
			name:   "a class of the namespace has the short name: FQCN",
			others: []string{sameName},
			src:    "namespace App;\n\n" + dice(roll),
			want:   "    public function pick(\\Random\\Engine $engine): int",
		},
		{
			name:   "a longer first segment is another name: imported",
			others: []string{checks},
			src:    "namespace App;\n\n" + dice("return Engines\\Check::size($engine);"),
			want:   "    public function pick(Engine $engine): int",
		},
		{
			name:   "a fully qualified name goes through no import: imported",
			others: []string{check},
			src:    "namespace App;\n\n" + dice("return \\App\\Engine\\Check::size($engine);"),
			want:   "    public function pick(Engine $engine): int",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := program.New()
			if err := p.LoadStubs(); err != nil {
				t.Fatal(err)
			}
			for i, other := range append([]string{source}, c.others...) {
				if _, err := p.AddFile(fmt.Sprintf("other%d.phn", i), []byte(other), lexer.ModePhaenon, true); err != nil {
					t.Fatal(err)
				}
			}
			f, err := p.AddFile("x.phn", []byte(c.src), lexer.ModePhaenon, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range checker.New(p, checker.Options{}).Check() {
				if d.Severity == checker.Error {
					t.Fatalf("checker: %s:%d: %s: %s", d.File, d.Line, d.Code, d.Message)
				}
			}
			out, err := File([]byte(c.src), Options{SourceName: "x.phn", StrictTypes: true, Program: p, File: f})
			if err != nil {
				t.Fatal(err)
			}
			m, err := linemap.Find(out)
			if err != nil || m == nil {
				t.Fatalf("no line map: %v\n%s", err, out)
			}
			line := strings.Count(c.src[:strings.Index(c.src, "public function pick(")], "\n") + 1
			n, _ := m.Reverse(line)
			if got := lineText(out, n); got != c.want {
				t.Errorf("source line %d, output line %d:\n got %q\nwant %q\n%s", line, n, got, c.want, out)
			}
			imported := strings.Contains(string(out), "use Random\\Engine;")
			if wantImport := !strings.Contains(c.want, "\\Random\\Engine"); imported != wantImport {
				t.Errorf("use Random\\Engine; present = %v, want %v\n%s", imported, wantImport, out)
			}
		})
	}
}
