package program

import (
	"strconv"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
)

// The value of a string literal is the string PHP's scanner makes of it
// (zend_language_scanner.l), not its source text: a type, an array key, a
// format or a pattern read from a literal holds what PHP holds at run
// time, as PHPStan, whose parser decodes literals the same way, has it.

// StringValue returns the value of a constant string literal, 'x' or "x"
// with an optional b prefix. A single-quoted string decodes \\ and \'
// alone; a double-quoted one decodes the escapes of zend_scan_escape_string
// (escapeString).
func StringValue(lit *ast.StringLit) string {
	raw := lit.Raw
	if len(raw) > 0 && (raw[0] == 'b' || raw[0] == 'B') {
		raw = raw[1:]
	}
	if len(raw) < 2 {
		return raw
	}
	body := raw[1 : len(raw)-1]
	switch raw[0] {
	case '\'':
		if strings.IndexByte(body, '\\') < 0 {
			return body
		}
		return strings.NewReplacer(`\\`, `\`, `\'`, `'`).Replace(body)
	case '"':
		return escapeString(body, '"')
	}
	return raw
}

// HeredocValue returns the value of a heredoc or a nowdoc whose body is
// text alone, and false for one with an interpolation or for another kind
// of string. The newline before the closing marker is no part of the
// value, every line loses the closing marker's indentation (which the
// parser has checked: no line is indented less, but a line of whitespace
// alone), and then a heredoc's escapes are decoded as a double-quoted
// string's, but for \", which a heredoc keeps as written; a nowdoc's body
// is its text.
func HeredocValue(s *ast.InterpolatedString) (string, bool) {
	if s.Kind != ast.StringHeredoc {
		return "", false
	}
	var body strings.Builder
	for _, p := range s.Parts {
		part, ok := p.(*ast.StringPart)
		if !ok {
			return "", false
		}
		body.WriteString(part.Raw)
	}
	text := body.String()
	switch {
	case strings.HasSuffix(text, "\r\n"):
		text = text[:len(text)-2]
	case strings.HasSuffix(text, "\n"), strings.HasSuffix(text, "\r"):
		text = text[:len(text)-1]
	}
	indent := len(s.Close) - len(strings.TrimLeft(s.Close, " \t"))
	if indent > 0 {
		text = stripIndentation(text, indent)
	}
	label := strings.TrimLeft(strings.TrimLeft(s.Open, "bB")[len("<<<"):], " \t")
	if strings.HasPrefix(label, "'") {
		return text, true
	}
	return escapeString(text, 0), true
}

// stripIndentation removes up to n spaces or tabs from the start of every
// line of text (strip_multiline_string_indentation). Lines end at \r\n,
// \n or a \r alone, as PHP's scanner ends them.
func stripIndentation(text string, n int) string {
	var b strings.Builder
	b.Grow(len(text))
	lineStart := true
	col := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		if lineStart && col < n && (c == ' ' || c == '\t') {
			col++
			continue
		}
		lineStart = false
		b.WriteByte(c)
		if c == '\n' || c == '\r' && (i+1 == len(text) || text[i+1] != '\n') {
			lineStart, col = true, 0
		}
	}
	return b.String()
}

// escapeString decodes the escapes of a double-quoted string or a
// heredoc as zend_scan_escape_string does: \n \r \t \f \v \e, \\ and \$,
// the quote that delimits the string (quote, 0 in a heredoc), one or two
// hex digits after \x or \X, one to three octal digits (the byte's low
// eight bits, as PHP's char cast keeps them: \400 is \0), and \u{…}, a
// code point PHP writes as UTF-8. A backslash before anything else, \u
// without a brace and \x without a hex digit among them, stays with what
// follows it.
func escapeString(s string, quote byte) string {
	i := strings.IndexByte(s, '\\')
	if i < 0 {
		return s
	}
	b := make([]byte, 0, len(s))
	b = append(b, s[:i]...)
	for ; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b = append(b, c)
			continue
		}
		i++
		c = s[i]
		switch c {
		case 'n':
			b = append(b, '\n')
		case 'r':
			b = append(b, '\r')
		case 't':
			b = append(b, '\t')
		case 'f':
			b = append(b, '\f')
		case 'v':
			b = append(b, '\v')
		case 'e':
			b = append(b, 0x1b)
		case '\\', '$':
			b = append(b, c)
		case '"', '`':
			if c != quote {
				b = append(b, '\\')
			}
			b = append(b, c)
		case 'x', 'X':
			if i+1 == len(s) || !isHexDigit(s[i+1]) {
				b = append(b, '\\', c)
				break
			}
			end := i + 2
			if end < len(s) && isHexDigit(s[end]) {
				end++
			}
			v, _ := strconv.ParseUint(s[i+1:end], 16, 8)
			b = append(b, byte(v))
			i = end - 1
		case 'u':
			cp, end, ok := codePointEscape(s, i+1)
			if !ok {
				b = append(b, '\\', c)
				break
			}
			b = appendUTF8(b, cp)
			i = end - 1
		default:
			if c < '0' || c > '7' {
				b = append(b, '\\', c)
				break
			}
			v := uint(c - '0')
			for k := 0; k < 2 && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '7'; k++ {
				i++
				v = v*8 + uint(s[i]-'0')
			}
			b = append(b, byte(v))
		}
	}
	return string(b)
}

// codePointEscape reads the {hex} of a \u{hex} escape at s[i:]: the code
// point and the index after the brace. PHP refuses a \u{ that holds no
// hex digit, another character or a code point past U+10FFFF, as a parse
// error; such text is left as written here.
func codePointEscape(s string, i int) (uint64, int, bool) {
	if i == len(s) || s[i] != '{' {
		return 0, 0, false
	}
	j := i + 1
	for j < len(s) && isHexDigit(s[j]) {
		j++
	}
	if j == i+1 || j == len(s) || s[j] != '}' {
		return 0, 0, false
	}
	cp, err := strconv.ParseUint(s[i+1:j], 16, 32)
	if err != nil || cp > 0x10FFFF {
		return 0, 0, false
	}
	return cp, j + 1, true
}

// appendUTF8 writes a code point as PHP's scanner does: the UTF-8 bytes of
// its value, a surrogate's included, which Go's encoder would replace.
func appendUTF8(b []byte, cp uint64) []byte {
	switch {
	case cp < 0x80:
		return append(b, byte(cp))
	case cp <= 0x7FF:
		return append(b, byte(cp>>6)+0xC0, byte(cp&0x3F)+0x80)
	case cp <= 0xFFFF:
		return append(b, byte(cp>>12)+0xE0, byte(cp>>6&0x3F)+0x80, byte(cp&0x3F)+0x80)
	}
	return append(b, byte(cp>>18)+0xF0, byte(cp>>12&0x3F)+0x80, byte(cp>>6&0x3F)+0x80, byte(cp&0x3F)+0x80)
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
