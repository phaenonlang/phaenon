package lexer

import (
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/token"
)

// render prints tokens as "KIND(text)" joined by spaces, trivia omitted.
func render(src string, mode Mode) string {
	var parts []string
	for _, t := range Tokenize([]byte(src), mode) {
		if t.Kind == token.EOF || t.Kind.IsTrivia() {
			continue
		}
		parts = append(parts, t.Kind.String()+"("+t.Text([]byte(src))+")")
	}
	return strings.Join(parts, " ")
}

func TestPHPMode(t *testing.T) {
	cases := []struct{ src, want string }{
		{`<?php $a = 1;`, `T_OPEN_TAG(<?php ) T_VARIABLE($a) =(=) T_LNUMBER(1) ;(;)`},
		{"html<?= $x ?>\n", `T_INLINE_HTML(html) T_OPEN_TAG_WITH_ECHO(<?=) T_VARIABLE($x) T_CLOSE_TAG(?>` + "\n" + `)`},
		{`<?php yield  from $g; Foo\Bar; \Foo; namespace\f;`,
			`T_OPEN_TAG(<?php ) T_YIELD_FROM(yield  from) T_VARIABLE($g) ;(;) T_NAME_QUALIFIED(Foo\Bar) ;(;) T_NAME_FULLY_QUALIFIED(\Foo) ;(;) T_NAME_RELATIVE(namespace\f) ;(;)`},
		{`<?php enum A {} enum extends; $o->class; $o?->enum;`,
			`T_OPEN_TAG(<?php ) T_ENUM(enum) T_STRING(A) {({) }(}) T_STRING(enum) T_EXTENDS(extends) ;(;) T_VARIABLE($o) T_OBJECT_OPERATOR(->) T_STRING(class) ;(;) T_VARIABLE($o) T_NULLSAFE_OBJECT_OPERATOR(?->) T_STRING(enum) ;(;)`},
		{`<?php 0x1F 0b11 0o17 017 1_000 9223372036854775807 9223372036854775808 1e3 .5 1. 08`,
			`T_OPEN_TAG(<?php ) T_LNUMBER(0x1F) T_LNUMBER(0b11) T_LNUMBER(0o17) T_LNUMBER(017) T_LNUMBER(1_000) T_LNUMBER(9223372036854775807) T_DNUMBER(9223372036854775808) T_DNUMBER(1e3) T_DNUMBER(.5) T_DNUMBER(1.) T_LNUMBER(08)`},
		{`<?php (int) ( INTEGER ) (void) #[A] /** d */ /**/`,
			`T_OPEN_TAG(<?php ) T_INT_CAST((int)) T_INT_CAST(( INTEGER )) T_VOID_CAST((void)) T_ATTRIBUTE(#[) T_STRING(A) ](])`},
		{`<?php "x $a[b] ${c} {$f} $g->h $i[-1]";`,
			`T_OPEN_TAG(<?php ) "(") T_ENCAPSED_AND_WHITESPACE(x ) T_VARIABLE($a) [([) T_STRING(b) ](]) T_ENCAPSED_AND_WHITESPACE( ) T_DOLLAR_OPEN_CURLY_BRACES(${) T_STRING_VARNAME(c) }(}) T_ENCAPSED_AND_WHITESPACE( ) T_CURLY_OPEN({) T_VARIABLE($f) }(}) T_ENCAPSED_AND_WHITESPACE( ) T_VARIABLE($g) T_OBJECT_OPERATOR(->) T_STRING(h) T_ENCAPSED_AND_WHITESPACE( ) T_VARIABLE($i) [([) -(-) T_NUM_STRING(1) ](]) "(") ;(;)`},
		{"<?php <<<EOT\n  a {$x}\n  EOT;\n",
			"T_OPEN_TAG(<?php ) T_START_HEREDOC(<<<EOT\n) T_ENCAPSED_AND_WHITESPACE(  a ) T_CURLY_OPEN({) T_VARIABLE($x) }(}) T_ENCAPSED_AND_WHITESPACE(\n) T_END_HEREDOC(  EOT) ;(;)"},
		{"<?php <<<'EOT'\nraw $x\nEOT;\n",
			"T_OPEN_TAG(<?php ) T_START_HEREDOC(<<<'EOT'\n) T_ENCAPSED_AND_WHITESPACE(raw $x\n) T_END_HEREDOC(EOT) ;(;)"},
		{`<?php &$x; & $x; &...$y; &x; public(set) private( set )`,
			`T_OPEN_TAG(<?php ) T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG(&) T_VARIABLE($x) ;(;) T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG(&) T_VARIABLE($x) ;(;) T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG(&) T_ELLIPSIS(...) T_VARIABLE($y) ;(;) T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG(&) T_STRING(x) ;(;) T_PUBLIC_SET(public(set)) T_PRIVATE(private) ((() T_STRING(set) )())`},
		{"<?php __halt_compiler(); junk <?php $x",
			`T_OPEN_TAG(<?php ) T_HALT_COMPILER(__halt_compiler) ((() )()) ;(;) T_INLINE_HTML( junk <?php $x)`},
		{`<?php $a |> f(...); $b <=> $c; $d ??= 1;`,
			`T_OPEN_TAG(<?php ) T_VARIABLE($a) T_PIPE(|>) T_STRING(f) ((() T_ELLIPSIS(...) )()) ;(;) T_VARIABLE($b) T_SPACESHIP(<=>) T_VARIABLE($c) ;(;) T_VARIABLE($d) T_COALESCE_EQUAL(??=) T_LNUMBER(1) ;(;)`},
	}
	for _, c := range cases {
		if got := render(c.src, ModePHP); got != c.want {
			t.Errorf("%q:\n got  %s\n want %s", c.src, got, c.want)
		}
	}
}

func TestPhaenonMode(t *testing.T) {
	cases := []struct{ src, want string }{
		// Code from byte 0, no tags.
		{`$a = 1;`, `T_VARIABLE($a) =(=) T_LNUMBER(1) ;(;)`},
		// Phaenon keywords.
		{`if ($x is list<int>) {} type Id = string; class R<T of A, in U, out V> {}`,
			`T_IF(if) ((() T_VARIABLE($x) T_IS(is) T_LIST(list) <(<) T_STRING(int) >(>) )()) {({) }(}) T_TYPE(type) T_STRING(Id) =(=) T_STRING(string) ;(;) T_CLASS(class) T_STRING(R) <(<) T_STRING(T) T_OF(of) T_STRING(A) ,(,) T_IN(in) T_STRING(U) ,(,) T_OUT(out) T_STRING(V) >(>) {({) }(})`},
		// No close tags: "?>" is just two operators, and "// ?>" does not end a comment.
		{"$a ?> 1; // c ?> still comment\n$b", `T_VARIABLE($a) ?(?) >(>) T_LNUMBER(1) ;(;) T_VARIABLE($b)`},
		// A stray <?php at byte 0 is one token for a friendly diagnostic.
		{"<?php $a", `T_OPEN_TAG(<?php) T_VARIABLE($a)`},
		// Keywords after -> are still names.
		{`$x->is; $x->type;`, `T_VARIABLE($x) T_OBJECT_OPERATOR(->) T_STRING(is) ;(;) T_VARIABLE($x) T_OBJECT_OPERATOR(->) T_STRING(type) ;(;)`},
	}
	for _, c := range cases {
		if got := render(c.src, ModePhaenon); got != c.want {
			t.Errorf("%q:\n got  %s\n want %s", c.src, got, c.want)
		}
	}
}

func TestLossless(t *testing.T) {
	srcs := []string{
		"<?php\n$a = \"x {$a['b']} ${c}\" . <<<X\n  $y\n  X . `cmd $z`;\n?>\ntail",
		"$a = 'unterminated",
		"<?php /* unterminated",
		"<?php $a->  /* c */ b ?->\n c",
	}
	for _, src := range srcs {
		var sb strings.Builder
		var line = 1
		for _, tk := range Tokenize([]byte(src), ModePHP) {
			if tk.Line < line {
				t.Errorf("%q: line numbers go backwards at %s", src, tk.Kind)
			}
			line = tk.Line
			sb.WriteString(tk.Text([]byte(src)))
		}
		if sb.String() != src {
			t.Errorf("not lossless:\n src %q\n got %q", src, sb.String())
		}
	}
}
