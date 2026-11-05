package checker

import "testing"

func TestRegexConsumes(t *testing.T) {
	for pattern, want := range map[string]bool{
		// Every match consumes a character.
		`/\A[a-z0-9](?:[a-z0-9-]{0,37}[a-z0-9])?\z/`: true,
		`/\A[a-z0-9-]{1,32}\z/`:                      true,
		`/^[a-z][a-z0-9_-]{0,31}$/`:                  true,
		`~^(?:get|head)$~i`:                          true,
		`/^(?:ab|c|[x-z]+)$/`:                        true,
		`/\Q.\E/`:                                    true,
		`/[]a]/`:                                     true,
		`/[[:alpha:]]+/u`:                            true,
		`/(?<year>\d{4})-(?P<month>\d\d)/`:           true,
		`/\x41\x{263A}\p{Lu}\pL/`:                    true,
		`/a{2}/`:                                     true,
		`/a{,2}b/`:                                   true,
		`/(*UTF8)a/`:                                 true,
		`/(?i)a/`:                                    true,
		`/(?i:a)/`:                                   true,
		`{a}`:                                        true,
		`/a{x}/`:                                     true, // `{` stands for itself
		// A match may be empty.
		`/^[a-z]*$/`:    false,
		`/^(?:a|)$/`:    false,
		`/(?=a)/`:       false,
		`/(a?)\1/`:      false,
		`/a{0,2}/`:      false,
		`/a{,2}/`:       false,
		`/^$/`:          false,
		`/\b/`:          false,
		`/(?#comment)/`: false,
		`/(?<=a)(?!b)/`: false,
		// Given up: \K (as PHPStan), free spacing, conditionals, broken.
		`/a\K/`:       false,
		`/ a /x`:      false,
		`/(?(1)a|b)/`: false,
		`/(?R)a/`:     false,
		`/[a-z/`:      false,
		`/a)/`:        false,
		`a/`:          false,
		`/a/e`:        false,
	} {
		if got := regexConsumes(pattern); got != want {
			t.Errorf("regexConsumes(%q) = %v, want %v", pattern, got, want)
		}
	}
}
