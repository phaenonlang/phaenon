package emit

import "testing"

// TestTranslateDocAliases: the name of a type alias in a docblock's type
// becomes what the alias stands for, with the type arguments written
// after it; every other byte stays (design/collections.md §6.5).
func TestTranslateDocAliases(t *testing.T) {
	expand := func(mention string, _ mentionContext) (string, bool) {
		switch mention {
		case "Row":
			return "array{id: int}", true
		case "Page<Row>":
			return "array{items: list<array{id: int}>}", true
		case `Orders\Row`:
			return "array{id: int}", true
		}
		return "", false
	}
	cases := []struct{ in, want string }{
		{"/** @return Row */", "/** @return array{id: int} */"},
		{"/** @param list<Row> $rows the rows */", "/** @param list<array{id: int}> $rows the rows */"},
		{"/** @return Page<Row>|null */", "/** @return array{items: list<array{id: int}>}|null */"},
		{"/** @var array{row: Row, Row: int, rows?: list<Orders\\Row>} */", "/** @var array{row: array{id: int}, Row: int, rows?: list<array{id: int}>} */"},
		{"/** @return map<string, Row> */", "/** @return array<string, array{id: int}> */"},
		{"/** @param Closure(Row $row): Row $f */", "/** @param \\Closure(array{id: int} $row): array{id: int} $f */"},
		// A name that only looks like the alias: a constant's class, a
		// variable, a longer name, prose.
		{"/** @return Row::KIND */", "/** @return Row::KIND */"},
		{"/** @param Rows $Row the Row */", "/** @param Rows $Row the Row */"},
		{"/** @return Other\\Row */", "/** @return Other\\Row */"},
		// A generic mention that runs over lines keeps its name: an edit
		// in place keeps its line breaks, and an expansion has none of
		// its own. The arguments are names on one line, and expand.
		{"/**\n * @return Page<\n *     Row\n * >\n */", "/**\n * @return Page<\n *     array{id: int}\n * >\n */"},
	}
	for _, c := range cases {
		if got := translateDocWith(c.in, expand); got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	// Without type information nothing is expanded.
	if in := "/** @return Row */"; translateDoc(in) != in {
		t.Errorf("%q changed without an expander", in)
	}
}

// TestTranslateDocAliasPlace: the expansion is asked for where the mention
// stands, and `?` goes with it.
func TestTranslateDocAliasPlace(t *testing.T) {
	expand := func(mention string, in mentionContext) (string, bool) {
		if mention != "Value" {
			return "", false
		}
		switch {
		case in.nullable && in.union:
			return "<nullable in a union>", true
		case in.nullable:
			return "<nullable>", true
		case in.union && in.intersection:
			return "<both>", true
		case in.union:
			return "<in a union>", true
		case in.intersection:
			return "<in an intersection>", true
		}
		return "<alone>", true
	}
	cases := []struct{ in, want string }{
		{"/** @return Value */", "/** @return <alone> */"},
		{"/** @return ?Value */", "/** @return <nullable> */"},
		{"/** @return Value|null */", "/** @return <in a union>|null */"},
		{"/** @return null | Value */", "/** @return null | <in a union> */"},
		{"/** @return A&Value */", "/** @return A&<in an intersection> */"},
		{"/** @return list<?Value> */", "/** @return list<<nullable>> */"},
		{"/** @return list<Value>|null */", "/** @return list<<alone>>|null */"},
		{"/** @return array{a: ?Value, b?: Value|int, c: Value} */", "/** @return array{a: <nullable>, b?: <in a union>|int, c: <alone>} */"},
		{"/** @param Closure(?Value, Value): Value $f */", "/** @param \\Closure(<nullable>, <alone>): <alone> $f */"},
		{"/** @return ($x is int ? Value : null) */", "/** @return ($x is int ? <alone> : null) */"},
	}
	for _, c := range cases {
		if got := translateDocWith(c.in, expand); got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}
