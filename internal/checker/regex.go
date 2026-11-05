package checker

import "strings"

// The subject a successful preg_match() narrows (PHPStan's
// RegexGroupParser::parseGroups): a constant pattern whose every match
// consumes a character makes the subject a non-empty-string. What is
// computed here is the least number of subject characters a match
// consumes, a lower bound: where the pattern does something this reading
// does not follow (a conditional, recursion, the x modifier) or PHPStan
// does not narrow (\K), it gives up, which narrows nothing.

// regexConsumes reports whether every match of a PCRE pattern, delimiters
// and modifiers included, consumes at least one character of the subject.
func regexConsumes(pattern string) bool {
	body, mods, ok := regexBody(pattern)
	if !ok || strings.ContainsRune(mods, 'x') {
		return false
	}
	r := regexReader{s: body}
	n, ok := r.alternation()
	return ok && r.i == len(r.s) && n > 0
}

// regexBody splits a pattern into what its delimiters hold and its
// modifiers. Brackets delimit as pairs: (…), {…}, […], <…>.
func regexBody(pattern string) (body, mods string, ok bool) {
	p := strings.TrimLeft(pattern, " \t\n\r\v\f")
	if p == "" {
		return "", "", false
	}
	open := p[0]
	if open == '\\' || open >= '0' && open <= '9' || open >= 'a' && open <= 'z' || open >= 'A' && open <= 'Z' {
		return "", "", false
	}
	close := open
	switch open {
	case '(':
		close = ')'
	case '{':
		close = '}'
	case '[':
		close = ']'
	case '<':
		close = '>'
	}
	end := strings.LastIndexByte(p, close)
	if end <= 0 {
		return "", "", false
	}
	mods = p[end+1:]
	for _, m := range mods {
		if !strings.ContainsRune("imsxuADSUXJnr\n", m) {
			return "", "", false
		}
	}
	return p[1:end], mods, true
}

// regexReader reads a pattern's body, computing least match lengths.
type regexReader struct {
	s string
	i int
}

const regexLots = 1 << 20 // a cap, so that nested counts cannot overflow

func (r *regexReader) alternation() (int, bool) {
	least, ok := r.sequence()
	if !ok {
		return 0, false
	}
	for r.i < len(r.s) && r.s[r.i] == '|' {
		r.i++
		n, ok := r.sequence()
		if !ok {
			return 0, false
		}
		least = min(least, n)
	}
	return least, true
}

func (r *regexReader) sequence() (int, bool) {
	total := 0
	for r.i < len(r.s) && r.s[r.i] != '|' && r.s[r.i] != ')' {
		n, ok := r.atom()
		if !ok {
			return 0, false
		}
		times, ok := r.quantifier()
		if !ok {
			return 0, false
		}
		total = min(total+n*times, regexLots)
	}
	return total, true
}

// quantifier reads the quantifier after an atom, if any, and returns the
// least number of times it repeats the atom.
func (r *regexReader) quantifier() (int, bool) {
	if r.i >= len(r.s) {
		return 1, true
	}
	least := 1
	switch r.s[r.i] {
	case '*', '?':
		r.i++
		least = 0
	case '+':
		r.i++
	case '{':
		j := r.i + 1
		k := j
		for k < len(r.s) && r.s[k] >= '0' && r.s[k] <= '9' {
			k++
		}
		rest := k
		if rest < len(r.s) && r.s[rest] == ',' {
			rest++
			for rest < len(r.s) && r.s[rest] >= '0' && r.s[rest] <= '9' {
				rest++
			}
		}
		if rest >= len(r.s) || r.s[rest] != '}' || rest == j || (k == j && r.s[j] != ',') {
			return 1, true // not a quantifier: `{` stands for itself
		}
		least = 0
		for _, c := range r.s[j:k] {
			least = min(least*10+int(c-'0'), regexLots)
		}
		r.i = rest + 1
	default:
		return 1, true
	}
	if r.i < len(r.s) && (r.s[r.i] == '+' || r.s[r.i] == '?') {
		r.i++ // possessive or lazy
	}
	return least, true
}

func (r *regexReader) atom() (int, bool) {
	c := r.s[r.i]
	switch c {
	case '(':
		return r.group()
	case '[':
		return r.class()
	case '\\':
		return r.escape()
	case '^', '$':
		r.i++
		return 0, true
	}
	r.i++
	return 1, true
}

func (r *regexReader) group() (int, bool) {
	r.i++ // (
	inner := func(counts bool) (int, bool) {
		n, ok := r.alternation()
		if !ok || r.i >= len(r.s) || r.s[r.i] != ')' {
			return 0, false
		}
		r.i++
		if !counts {
			return 0, true
		}
		return n, true
	}
	rest := r.s[r.i:]
	switch {
	case strings.HasPrefix(rest, "*"):
		// A verb, (*UTF8), (*SKIP): it matches nothing.
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return 0, false
		}
		r.i += end + 1
		return 0, true
	case !strings.HasPrefix(rest, "?"):
		return inner(true)
	}
	r.i++ // ?
	rest = r.s[r.i:]
	switch {
	case strings.HasPrefix(rest, "#"):
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return 0, false
		}
		r.i += end + 1
		return 0, true
	case strings.HasPrefix(rest, ":"), strings.HasPrefix(rest, ">"), strings.HasPrefix(rest, "|"):
		r.i++
		return inner(true)
	case strings.HasPrefix(rest, "="), strings.HasPrefix(rest, "!"):
		r.i++
		return inner(false)
	case strings.HasPrefix(rest, "<="), strings.HasPrefix(rest, "<!"):
		r.i += 2
		return inner(false)
	case strings.HasPrefix(rest, "<"), strings.HasPrefix(rest, "P<"), strings.HasPrefix(rest, "'"):
		// A named group: (?<name>…), (?P<name>…), (?'name'…).
		closeName := byte('>')
		if rest[0] == '\'' {
			closeName = '\''
		}
		end := strings.IndexByte(rest[1:], closeName)
		if end < 0 {
			return 0, false
		}
		r.i += end + 2
		return inner(true)
	}
	// Options, (?i) or (?i-s), or a group with them, (?i:…).
	j := r.i
	for j < len(r.s) && strings.IndexByte("imsxUXJn-^", r.s[j]) >= 0 {
		if r.s[j] == 'x' {
			return 0, false // free spacing from here on
		}
		j++
	}
	if j == r.i || j >= len(r.s) {
		return 0, false // a conditional, a recursion, a callout
	}
	r.i = j
	switch r.s[j] {
	case ')':
		r.i++
		return 0, true
	case ':':
		r.i++
		return inner(true)
	}
	return 0, false
}

// class reads a character class, which matches one character.
func (r *regexReader) class() (int, bool) {
	j := r.i + 1
	if j < len(r.s) && r.s[j] == '^' {
		j++
	}
	if j < len(r.s) && r.s[j] == ']' {
		j++ // a leading ] is a member
	}
	for j < len(r.s) {
		switch {
		case r.s[j] == '\\':
			j += 2
		case strings.HasPrefix(r.s[j:], "[:"):
			end := strings.Index(r.s[j+2:], ":]")
			if end < 0 {
				return 0, false
			}
			j += end + 4
		case r.s[j] == ']':
			r.i = j + 1
			return 1, true
		default:
			j++
		}
	}
	return 0, false
}

func (r *regexReader) escape() (int, bool) {
	if r.i+1 >= len(r.s) {
		return 0, false
	}
	c := r.s[r.i+1]
	r.i += 2
	braced := func() bool {
		if r.i < len(r.s) && r.s[r.i] == '{' {
			end := strings.IndexByte(r.s[r.i:], '}')
			if end < 0 {
				return false
			}
			r.i += end + 1
		}
		return true
	}
	switch {
	case c == 'Q':
		// \Q…\E: the characters between stand for themselves.
		end := strings.Index(r.s[r.i:], `\E`)
		if end < 0 {
			end = len(r.s) - r.i
			r.i = len(r.s)
		} else {
			r.i += end + 2
		}
		return end, true
	case c == 'E':
		return 0, true
	case c == 'K':
		// \K resets the match: PHPStan narrows nothing then, and the
		// output must pass PHPStan.
		return 0, false
	case strings.IndexByte("bBAzZG", c) >= 0:
		return 0, true // assertions
	case c >= '1' && c <= '9', c == 'g', c == 'k':
		// A back reference may stand for an empty capture.
		switch {
		case c >= '1' && c <= '9':
			for r.i < len(r.s) && r.s[r.i] >= '0' && r.s[r.i] <= '9' {
				r.i++
			}
		case r.i < len(r.s) && strings.IndexByte("{<'", r.s[r.i]) >= 0:
			closeRef := map[byte]byte{'{': '}', '<': '>', '\'': '\''}[r.s[r.i]]
			end := strings.IndexByte(r.s[r.i+1:], closeRef)
			if end < 0 {
				return 0, false
			}
			r.i += end + 2
		default:
			if r.i < len(r.s) && (r.s[r.i] == '-' || r.s[r.i] == '+') {
				r.i++
			}
			for r.i < len(r.s) && r.s[r.i] >= '0' && r.s[r.i] <= '9' {
				r.i++
			}
		}
		return 0, true
	case c == 'p', c == 'P':
		if r.i < len(r.s) && r.s[r.i] != '{' {
			r.i++ // \pL
		} else if !braced() {
			return 0, false
		}
		return 1, true
	case c == 'x', c == 'o', c == 'N':
		if !braced() {
			return 0, false
		}
		if c == 'x' && (r.i >= len(r.s) || r.s[r.i-1] != '}') {
			for k := 0; k < 2 && r.i < len(r.s) && strings.IndexByte("0123456789abcdefABCDEF", r.s[r.i]) >= 0; k++ {
				r.i++
			}
		}
		return 1, true
	case c == 'c':
		if r.i < len(r.s) {
			r.i++
		}
		return 1, true
	case c == '0':
		for k := 0; k < 2 && r.i < len(r.s) && r.s[r.i] >= '0' && r.s[r.i] <= '7'; k++ {
			r.i++
		}
		return 1, true
	}
	// \d, \w, \s, \R, \X, \h, \v, \n, \t, an escaped metacharacter: one
	// character.
	return 1, true
}
