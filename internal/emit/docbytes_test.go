package emit

import "testing"

func TestEraseBytesInDoc(t *testing.T) {
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
		{"/**\n * @return array{\n *     data: bytes,\n * }\n */", "/**\n * @return array{\n *     data: bytes,\n * }\n */"},
		{"/** Holds the bytes. */", "/** Holds the bytes. */"},
		{"/** @see bytes */", "/** @see bytes */"},
	}
	for _, c := range cases {
		if got := eraseBytesInDoc(c.in); got != c.want {
			t.Errorf("eraseBytesInDoc(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}
