package emit

import "testing"

func TestTranslateDocBytes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/** @return bytes */", "/** @return string */"},
		{"/** @return bytes the raw bytes */", "/** @return string the raw bytes */"},
		{"/** @param bytes $bytes the bytes */", "/** @param string $bytes the bytes */"},
		{"/** @param $bytes the bytes */", "/** @param $bytes the bytes */"},
		{"/** @var list<bytes> */", "/** @var list<string> */"},
		{"/** @var array<string, Bytes> $m */", "/** @var array<string, string> $m */"},
		{"/** @phpstan-param ?bytes $x */", "/** @phpstan-param ?string $x */"},
		{"/** @psalm-return bytes|false */", "/** @psalm-return string|false */"},
		{"/** @return array{bytes: int, data: bytes} */", "/** @return array{bytes: int, data: string} */"},
		{"/** @return array{bytes?: bytes} */", "/** @return array{bytes?: string} */"},
		{"/** @return ($raw is true ? bytes : string) */", "/** @return ($raw is true ? string : string) */"},
		{"/** @param \\Closure(bytes): bytes $f */", "/** @param \\Closure(string): string $f */"},
		{"/** @return Foo\\bytes */", "/** @return Foo\\bytes */"},
		{"/** @return non-empty-bytes */", "/** @return non-empty-bytes */"},
		{"/** @template T of bytes */", "/** @template T of string */"},
		{"/** @phpstan-type Frame = list<bytes> */", "/** @phpstan-type Frame = list<string> */"},
		{"/** @method bytes read(bytes $into, string $mode = 'bytes') */", "/** @method string read(string $into, string $mode = 'bytes') */"},
		{"/** @method static bytes */", "/** @method static bytes */"},
		{"/**\n * Reads bytes.\n *\n * @param bytes $x\n * @return list<bytes>\n */", "/**\n * Reads bytes.\n *\n * @param string $x\n * @return list<string>\n */"},
		{"/**\n * @return array{\n *     data: bytes,\n * }\n */", "/**\n * @return array{\n *     data: string,\n * }\n */"},
		{"/** Holds the bytes. */", "/** Holds the bytes. */"},
		{"/** @see bytes */", "/** @see bytes */"},
	}
	for _, c := range cases {
		if got := translateDoc(c.in); got != c.want {
			t.Errorf("translateDoc(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// Phaenon's other vocabulary in a written docblock is PHPStan's in the
// output (design/collections.md §4): only the types of tags, in place, so
// prose and line breaks stay.
func TestTranslateDocVocabulary(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/** @param map<string, int> $m */", "/** @param array<string, int> $m */"},
		{"/** @return non-empty-map<string, User> */", "/** @return non-empty-array<string, User> */"},
		{"/** @var map <int, bool> */", "/** @var array <int, bool> */"},
		{"/** @var class<\\Countable> */", "/** @var class-string<\\Countable> */"},
		{"/** @param (string, int) $pair */", "/** @param array{string, int} $pair */"},
		{"/** @return list<(string, ?int)> */", "/** @return list<array{string, ?int}> */"},
		{"/** @return (int, (string, bool)) */", "/** @return array{int, array{string, bool}} */"},
		{"/** @param Closure(int): string $f */", "/** @param \\Closure(int): string $f */"},
		{"/** @param Closure(): (int, int) $f */", "/** @param \\Closure(): array{int, int} $f */"},
		{"/** @param array{by: map<string, int>, pair: (int, int), cls: class<T>} $c */",
			"/** @param array{by: array<string, int>, pair: array{int, int}, cls: class-string<T>} $c */"},
		{"/** @return ($raw is true ? (int, int) : null) */", "/** @return ($raw is true ? array{int, int} : null) */"},
		{"/** @template T of map<string, int> */", "/** @template T of array<string, int> */"},
		{"/** @phpstan-type Row = (string, int) */", "/** @phpstan-type Row = array{string, int} */"},
		{"/** @param map<string, int> $m a map (see below, the keys) */", "/** @param array<string, int> $m a map (see below, the keys) */"},
		{"/**\n * @param array{\n *     by: map<string, int>,\n *     pair: (int, int),\n * } $c\n */",
			"/**\n * @param array{\n *     by: array<string, int>,\n *     pair: array{int, int},\n * } $c\n */"},
		// Left alone: PHPStan's own spelling, names that only look alike,
		// shape keys, grouping, callable parameters and prose.
		{"/** @param \\Closure(int): string $f */", ""},
		{"/** @param Foo\\Closure $f */", ""},
		{"/** @param Foo\\map<int> $f */", ""},
		{"/** @param array{map: int, class: string} $c */", ""},
		{"/** @param (A|B)[] $x */", ""},
		{"/** @param callable(int, string): void $f */", ""},
		{"/** @var Foo::map */", ""},
		{"/** @param array<string, int> $m */", ""},
		{"/** A map<string, int> of names (keys, values). */", ""},
		{"/** @see map<string, int> */", ""},
	}
	for _, c := range cases {
		want := c.want
		if want == "" {
			want = c.in
		}
		if got := translateDoc(c.in); got != want {
			t.Errorf("translateDoc(%q)\n got %q\nwant %q", c.in, got, want)
		}
	}
}
