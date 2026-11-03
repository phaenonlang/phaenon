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
			src:  "namespace App;\n\nuse Stringable;\n\nfun f(mixed $m): bool\n{\n    return $m is list<string>;\n}\n",
			want: "namespace App;\n\nuse Phaenon\\Type;\nuse Stringable;\n\nfunction f(mixed $m): bool\n{\n    return Type::list(Type::string())->check($m);\n}\n",
		},
		{
			name: "sorts after the last import",
			src:  "namespace App;\n\nuse ArrayObject;\nuse Countable;\n\nfun f(mixed $m): bool\n{\n    return $m is list<string>;\n}\n",
			want: "namespace App;\n\nuse ArrayObject;\nuse Countable;\nuse Phaenon\\Type;\n\nfunction f(mixed $m): bool\n{\n    return Type::list(Type::string())->check($m);\n}\n",
		},
		{
			name: "after a neighbour with a comment: below the comment's line",
			src:  "namespace App;\n\nuse ArrayObject; // why\nuse Stringable;\n\nfun f(mixed $m): bool\n{\n    return $m is list<string>;\n}\n",
			want: "namespace App;\n\nuse ArrayObject; // why\nuse Phaenon\\Type;\nuse Stringable;\n\nfunction f(mixed $m): bool\n{\n    return Type::list(Type::string())->check($m);\n}\n",
		},
		{
			name: "a group use sorts by its prefix",
			src:  "namespace App;\n\nuse App\\Model\\{User, Group};\nuse Stringable;\n\nfun f(mixed $m): bool\n{\n    return $m is list<string>;\n}\n",
			want: "namespace App;\n\nuse App\\Model\\{User, Group};\nuse Phaenon\\Type;\nuse Stringable;\n\nfunction f(mixed $m): bool\n{\n    return Type::list(Type::string())->check($m);\n}\n",
		},
		{
			name: "only function imports: the class import goes first, a group of its own",
			src:  "namespace App;\n\nuse function is_array;\n\nfun f(mixed $m): bool\n{\n    return is_array($m) && $m is list<string>;\n}\n",
			want: "namespace App;\n\nuse Phaenon\\Type;\n\nuse function is_array;\n\nfunction f(mixed $m): bool\n{\n    return is_array($m) && Type::list(Type::string())->check($m);\n}\n",
		},
		{
			name: "no imports: a block after the namespace, before a generated docblock",
			src:  "namespace App;\n\nfinal class Box<T>\n{\n    public fun holds(mixed $m): bool\n    {\n        return $m is list<string>;\n    }\n}\n",
			want: "namespace App;\n\nuse Phaenon\\Type;\n\n/**\n * @template T\n */\nfinal class Box\n{\n    public function holds(mixed $m): bool\n    {\n        return Type::list(Type::string())->check($m);\n    }\n}\n",
		},
		{
			name: "no imports, no blank line after the namespace: the block gets one",
			src:  "namespace App;\nfun f(mixed $m): bool\n{\n    return $m is list<string>;\n}\n",
			want: "namespace App;\n\nuse Phaenon\\Type;\n\nfunction f(mixed $m): bool\n{\n    return Type::list(Type::string())->check($m);\n}\n",
		},
		{
			name: "no imports, the statement on the namespace's line",
			src:  "namespace App; fun f(mixed $m): bool { return $m is list<string>; }\n",
			want: "namespace App;\n\nuse Phaenon\\Type;\n\nfunction f(mixed $m): bool\n{\n    return Type::list(Type::string())->check($m);\n}\n",
		},
		{
			name: "no namespace: at the start",
			src:  "fun f(mixed $m): bool\n{\n    return $m is list<string>;\n}\n",
			want: "use Phaenon\\Type;\n\nfunction f(mixed $m): bool\n{\n    return Type::list(Type::string())->check($m);\n}\n",
		},
		{
			name: "no namespace, an own declare: after it",
			src:  "declare(strict_types=1);\n\nfun f(mixed $m): bool\n{\n    return $m is list<string>;\n}\n",
			want: "declare(strict_types=1);\n\nuse Phaenon\\Type;\n\nfunction f(mixed $m): bool\n{\n    return Type::list(Type::string())->check($m);\n}\n",
		},
		{
			name: "the file imports the class itself: nothing added",
			src:  "namespace App;\n\nuse Phaenon\\Type;\n\nfun f(mixed $m): bool\n{\n    return Type::int()->check($m) || $m is list<string>;\n}\n",
			want: "namespace App;\n\nuse Phaenon\\Type;\n\nfunction f(mixed $m): bool\n{\n    return Type::int()->check($m) || Type::list(Type::string())->check($m);\n}\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := program.New()
			if err := p.LoadStubs(); err != nil {
				t.Fatal(err)
			}
			f, err := p.AddFile("x.phn", []byte(c.src), lexer.ModePhaenon, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range checker.New(p, checker.Options{}).Check() {
				if d.Severity == checker.Error {
					t.Fatalf("checker: %d: %s: %s", d.Line, d.Code, d.Message)
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
// (`Types\Check` against `Type`) or is fully qualified leaves it free. line
// is the source line whose compiled line is checked, found through the
// line map.
func TestImportShadowing(t *testing.T) {
	check := "namespace App\\Type;\n\nfinal class Check\n{\n    public static fun both(bool $a, bool $b): bool\n    {\n        return $a && $b;\n    }\n}\n"
	checks := "namespace App\\Types;\n\nfinal class Check\n{\n    public static fun both(bool $a, bool $b): bool\n    {\n        return $a && $b;\n    }\n}\n"
	sameName := "namespace App;\n\nfinal class Type\n{\n}\n"
	cases := []struct {
		name   string
		others []string
		src    string
		line   int
		want   string
	}{
		{
			name:   "a relative qualified name begins with the short name: FQCN",
			others: []string{check},
			src:    "namespace App;\n\nfun f(mixed $m): bool\n{\n    return Type\\Check::both(true, $m is list<string>);\n}\n",
			line:   5,
			want:   "    return Type\\Check::both(true, \\Phaenon\\Type::list(\\Phaenon\\Type::string())->check($m));",
		},
		{
			name:   "a docblock's relative qualified name begins with it: FQCN",
			others: []string{check},
			src:    "namespace App;\n\n/** @param Type\\Check $c */\nfun f(object $c, mixed $m): bool\n{\n    return $m is list<string>;\n}\n",
			line:   6,
			want:   "    return \\Phaenon\\Type::list(\\Phaenon\\Type::string())->check($m);",
		},
		{
			name:   "a class of the namespace has the short name: FQCN",
			others: []string{sameName},
			src:    "namespace App;\n\nfun f(mixed $m): bool\n{\n    return $m is list<string>;\n}\n",
			line:   5,
			want:   "    return \\Phaenon\\Type::list(\\Phaenon\\Type::string())->check($m);",
		},
		{
			name:   "a longer first segment is another name: imported",
			others: []string{checks},
			src:    "namespace App;\n\nfun f(mixed $m): bool\n{\n    return Types\\Check::both(true, $m is list<string>);\n}\n",
			line:   5,
			want:   "    return Types\\Check::both(true, Type::list(Type::string())->check($m));",
		},
		{
			name:   "a fully qualified name goes through no import: imported",
			others: []string{check},
			src:    "namespace App;\n\nfun f(mixed $m): bool\n{\n    return \\App\\Type\\Check::both(true, $m is list<string>);\n}\n",
			line:   5,
			want:   "    return \\App\\Type\\Check::both(true, Type::list(Type::string())->check($m));",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := program.New()
			if err := p.LoadStubs(); err != nil {
				t.Fatal(err)
			}
			for i, other := range c.others {
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
			n, _ := m.Reverse(c.line)
			if got := lineText(out, n); got != c.want {
				t.Errorf("source line %d, output line %d:\n got %q\nwant %q\n%s", c.line, n, got, c.want, out)
			}
			imported := strings.Contains(string(out), "use Phaenon\\Type;")
			if wantImport := !strings.Contains(c.want, "\\Phaenon\\Type::"); imported != wantImport {
				t.Errorf("use Phaenon\\Type; present = %v, want %v\n%s", imported, wantImport, out)
			}
		})
	}
}
