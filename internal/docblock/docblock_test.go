package docblock

import "testing"

func TestParseType(t *testing.T) {
	// input → canonical printing ("" = same as input)
	cases := map[string]string{
		"int":                              "",
		"?Foo":                             "",
		"int|string|null":                  "",
		"array<string, int>":               "",
		"array<int>":                       "",
		"list<\\App\\User>":                "",
		"non-empty-list<non-empty-string>": "",
		"class-string<T>":                  "",
		"int<0, max>":                      "",
		"int<min, -1>":                     "",
		"array{a: int, b?: string}":        "",
		"array{'a-b': int, 0: bool}":       "array{a-b: int, 0: bool}",
		"array{int, string}":               "",
		"array{foo: int, ...}":             "",
		"list{int, string}":                "",
		"callable(int, string=): bool":     "",
		"Closure(T): R":                    "",
		"callable(): void":                 "",
		"callable(int $a, string ...$rest): void": "",
		"callable(array &$x): void":               "",
		"pure-callable(): int":                    "",
		"'a'|'b'":                                 "",
		"\"a\"":                                   "'a'",
		"Foo::BAR":                                "",
		"Foo::*":                                  "",
		"Foo::STATUS_*":                           "",
		"int[]":                                   "array<int>",
		"Foo[][]":                                 "array<array<Foo>>",
		"(A&B)|null":                              "",
		"A&B":                                     "",
		"$this":                                   "",
		"static":                                  "",
		"3|4":                                     "",
		"-1":                                      "",
		"1.5":                                     "",
		"true|false":                              "",
		"T is int ? string : bool":                "(T is int ? string : bool)",
		"$x is not null ? int : null":             "($x is not null ? int : null)",
		"iterable<string, Foo>":                   "",
		"\\Traversable<int, Foo>|list<Foo>":       "",
		"array<string, array{id: int, tags: list<string>}>": "",
		"callable(int): (int|string)":                       "",
		"key-of<Foo::MAP>":                                  "",
		"value-of<Foo::MAP>":                                "",
		"int-mask<1, 2, 4>":                                 "",
		"array<string,int>":                                 "array<string, int>",
	}
	for in, want := range cases {
		if want == "" {
			want = in
		}
		n, err := ParseType(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got := n.String(); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "array<", "array{a: }", "|int", "int|", "callable(int"} {
		if _, err := ParseType(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestParseDoc(t *testing.T) {
	d := Parse(`/**
     * Maps a thing.
     *
     * @template T of object
     * @template-covariant U
     * @param T|class-string<T> $target the target
     * @param array<string, mixed> ...$rest
     * @param-out int $out
     * @phpstan-param non-empty-string $name
     * @param string $name
     * @return T
     * @throws \RuntimeException
     * @var list<int> $items
     * @deprecated
     * @phpstan-type Row = array{id: int}
     * @phpstan-import-type Row from \App\Repo as Alias
     * @method static Foo make(int $a, string ...$b)
     * @property-read int $count
     * @extends Base<T>
     * @implements \Countable
     * @phpstan-assert-if-true !null $x
     * @nonsense
     */`)
	if d.Summary != "Maps a thing." {
		t.Errorf("summary %q", d.Summary)
	}
	if len(d.Templates) != 2 || d.Templates[0].Name != "T" || d.Templates[0].Bound.String() != "object" || d.Templates[1].Variance != Covariant {
		t.Errorf("templates %+v", d.Templates)
	}
	if len(d.Params) != 3 {
		t.Fatalf("params %+v", d.Params)
	}
	if d.Params[0].Name != "target" || d.Params[0].Type.String() != "T|class-string<T>" {
		t.Errorf("param 0 %+v", d.Params[0])
	}
	if d.Params[1].Name != "rest" || !d.Params[1].Variadic {
		t.Errorf("param 1 %+v", d.Params[1])
	}
	if d.Params[2].Name != "name" || d.Params[2].Type.String() != "non-empty-string" {
		t.Errorf("phpstan-param should win: %+v", d.Params[2])
	}
	if len(d.ParamOut) != 1 || d.ParamOut[0].Name != "out" {
		t.Errorf("param-out %+v", d.ParamOut)
	}
	if d.Return == nil || d.Return.String() != "T" {
		t.Errorf("return %v", d.Return)
	}
	if len(d.Throws) != 1 || d.Throws[0].String() != "\\RuntimeException" {
		t.Errorf("throws %v", d.Throws)
	}
	if len(d.Vars) != 1 || d.Vars[0].Name != "items" || d.Vars[0].Type.String() != "list<int>" {
		t.Errorf("vars %+v", d.Vars)
	}
	if !d.Deprecated {
		t.Error("deprecated")
	}
	if len(d.TypeAliases) != 1 || d.TypeAliases[0].Name != "Row" || d.TypeAliases[0].Type.String() != "array{id: int}" {
		t.Errorf("type alias %+v", d.TypeAliases)
	}
	if len(d.ImportTypes) != 1 || d.ImportTypes[0].Alias != "Alias" || d.ImportTypes[0].From.String() != "\\App\\Repo" {
		t.Errorf("import type %+v", d.ImportTypes)
	}
	if len(d.Methods) != 1 || !d.Methods[0].Static || d.Methods[0].Name != "make" || len(d.Methods[0].Params) != 2 || !d.Methods[0].Params[1].Variadic {
		t.Errorf("method %+v", d.Methods)
	}
	if len(d.Properties) != 1 || d.Properties[0].Name != "count" || d.Properties[0].Write {
		t.Errorf("property %+v", d.Properties)
	}
	if len(d.Extends) != 1 || d.Extends[0].String() != "Base<T>" || len(d.Implements) != 1 {
		t.Errorf("extends/implements %v %v", d.Extends, d.Implements)
	}
	if len(d.Asserts) != 1 || !d.Asserts[0].IfTrue || !d.Asserts[0].Negated || d.Asserts[0].Var != "x" {
		t.Errorf("asserts %+v", d.Asserts)
	}
	if len(d.Errors) != 0 {
		t.Errorf("errors %+v", d.Errors)
	}
}

func TestMultilineAndErrors(t *testing.T) {
	d := Parse(`/**
 * @param array{
 *     id: int,
 *     name: string
 * } $row
 * @return array<
 * @var int
 */`)
	if len(d.Params) != 1 || d.Params[0].Type.String() != "array{id: int, name: string}" {
		t.Errorf("multiline param %+v", d.Params)
	}
	if d.Return != nil || len(d.Errors) != 1 || d.Errors[0].Tag != "@return" {
		t.Errorf("bad return should be an error: %v %+v", d.Return, d.Errors)
	}
	if len(d.Vars) != 1 || d.Vars[0].Name != "" {
		t.Errorf("var %+v", d.Vars)
	}
	one := Parse("/** @var \\Foo $x */")
	if len(one.Vars) != 1 || one.Vars[0].Name != "x" || one.Vars[0].Type.String() != "\\Foo" {
		t.Errorf("one-line var %+v", one.Vars)
	}
}
