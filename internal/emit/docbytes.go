package emit

import (
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/token"
)

// bytes erases to string in docblocks (design/strings.md §5): the tags the
// emitter generates spell it string already, and the tags a .phn writes by
// hand — `/** @return bytes */`, `@var list<bytes>`, `@param bytes $x` —
// get the same treatment here. Left alone, the word reaches PHPStan as a
// class name, and as Phaenon\Bytes when the file imports that class. Only
// the type of a tag is rewritten, one word for another on the same line,
// so prose that says "bytes" and the lines are untouched.

// eraseWrittenBytes rewrites `bytes` to `string` in the types of every
// docblock of a file compiled with type information (phn build). It runs
// after the lowering passes: a span one of them already rewrote (addDoc,
// replaceVar) holds generated text and is left to it.
func (e *emitter) eraseWrittenBytes() {
	for _, t := range lexer.Tokenize(e.src, lexer.ModePhaenon) {
		if t.Kind != token.T_DOC_COMMENT {
			continue
		}
		for _, s := range docBytesSpans(string(e.src[t.Start:t.End])) {
			span := ast.Span{Start: t.Start + s.Start, End: t.Start + s.End}
			if !e.edited(span) {
				e.replace(span, "string")
			}
		}
	}
}

// edited reports whether an edit already touches s.
func (e *emitter) edited(s ast.Span) bool {
	for _, ed := range e.edits {
		if ed.Start < s.End && s.Start < ed.End || ed.Start == ed.End && s.Start < ed.Start && ed.Start < s.End {
			return true
		}
	}
	return false
}

// eraseBytesInDoc is docBytesSpans applied to a docblock's text (or to the
// content of a one-line docblock, which addDoc copies into a new one).
func eraseBytesInDoc(doc string) string {
	spans := docBytesSpans(doc)
	if len(spans) == 0 {
		return doc
	}
	var b strings.Builder
	pos := 0
	for _, s := range spans {
		b.WriteString(doc[pos:s.Start])
		b.WriteString("string")
		pos = s.End
	}
	b.WriteString(doc[pos:])
	return b.String()
}

// docBytesSpans finds the words `bytes` that name the type in the tags of
// a docblock, as offsets into doc, in order.
func docBytesSpans(doc string) []ast.Span {
	var out []ast.Span
	off := 0
	for _, line := range strings.SplitAfter(doc, "\n") {
		for _, r := range tagTypeRegions(line) {
			for _, s := range bytesWords(line[r.Start:r.End]) {
				out = append(out, ast.Span{Start: off + r.Start + s.Start, End: off + r.Start + s.End})
			}
		}
		off += len(line)
	}
	return out
}

// tagTypeRegions returns the parts of one docblock line that are type
// syntax, as offsets into the line: the type of @param, @return, @var and
// their kin, a template's bound and default, a type alias's body, a
// @method's return type and parameter list. A type that does not parse
// on the line (one that runs over several) is left alone.
func tagTypeRegions(line string) []ast.Span {
	at := strings.IndexByte(line, '@')
	if at < 0 || strings.Trim(line[:at], " \t/*") != "" {
		return nil
	}
	end := at
	for end < len(line) && line[end] != ' ' && line[end] != '\t' && line[end] != '\n' && line[end] != '\r' {
		end++
	}
	name := strings.ToLower(line[at:end])
	for _, p := range []string{"@phpstan-", "@psalm-", "@phan-"} {
		if strings.HasPrefix(name, p) {
			name = "@" + strings.TrimPrefix(name, p)
			break
		}
	}
	// typeAt parses the type starting at i (after blanks) and returns its
	// region and the offset after it.
	typeAt := func(i int) (ast.Span, int, bool) {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		text := strings.TrimRight(line[i:], "\r\n")
		if strings.HasSuffix(strings.TrimRight(text, " \t"), "*/") {
			text = strings.TrimSuffix(strings.TrimRight(text, " \t"), "*/")
		}
		if text == "" || text[0] == '$' {
			return ast.Span{}, i, false
		}
		_, rest, err := docblock.ParseTypePrefix(text)
		if err != nil {
			return ast.Span{}, i, false
		}
		n := len(text) - len(rest)
		return ast.Span{Start: i, End: i + n}, i + n, true
	}
	switch name {
	case "@param", "@param-out", "@return", "@var", "@throws", "@extends", "@implements", "@use",
		"@mixin", "@template-extends", "@template-implements", "@template-use", "@property",
		"@property-read", "@property-write", "@self-out", "@this-out", "@require-extends",
		"@require-implements":
		if r, _, ok := typeAt(end); ok {
			return []ast.Span{r}
		}
	case "@assert", "@assert-if-true", "@assert-if-false":
		i := end
		for i < len(line) && strings.IndexByte(" \t!=", line[i]) >= 0 {
			i++
		}
		if r, _, ok := typeAt(i); ok {
			return []ast.Span{r}
		}
	case "@template", "@template-covariant", "@template-contravariant":
		var out []ast.Span
		rest := line[end:]
		for _, kw := range []string{" of ", " as ", " = "} {
			if i := strings.Index(rest, kw); i >= 0 {
				if r, _, ok := typeAt(end + i + len(kw)); ok {
					out = append(out, r)
				}
			}
		}
		return out
	case "@type":
		// `@phpstan-type Name = type` or `@phpstan-type Name type`.
		i := end
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		for i < len(line) && line[i] != ' ' && line[i] != '\t' && line[i] != '=' {
			i++
		}
		for i < len(line) && (line[i] == ' ' || line[i] == '\t' || line[i] == '=') {
			i++
		}
		if r, _, ok := typeAt(i); ok {
			return []ast.Span{r}
		}
	case "@method":
		// `@method [static] [type] name(params)`: the return type is what
		// parses before a name followed by `(`; the parameters are scanned
		// whole (their names are variables, their defaults quoted).
		var out []ast.Span
		i := end
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if strings.HasPrefix(line[i:], "static ") {
			i += len("static ")
		}
		open := strings.IndexByte(line[i:], '(')
		if open < 0 {
			return nil
		}
		open += i
		if r, after, ok := typeAt(i); ok && after < open && strings.TrimSpace(line[after:open]) != "" {
			out = append(out, r)
		}
		if close := strings.LastIndexByte(line, ')'); close > open {
			out = append(out, ast.Span{Start: open + 1, End: close})
		}
		return out
	}
	return nil
}

// bytesWords finds the type name `bytes` in a piece of type syntax: a
// whole word (not part of `Foo\bytes`, `non-empty-bytes` or `$bytes`),
// outside quotes, and not a shape key (`array{bytes: int}`).
func bytesWords(s string) []ast.Span {
	var out []ast.Span
	wordChar := func(c byte) bool {
		return c == '_' || c == '\\' || c == '-' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
	}
	for i := 0; i < len(s); {
		c := s[i]
		if c == '\'' || c == '"' {
			j := strings.IndexByte(s[i+1:], c)
			if j < 0 {
				return out
			}
			i += j + 2
			continue
		}
		if !wordChar(c) {
			i++
			continue
		}
		j := i
		for j < len(s) && wordChar(s[j]) {
			j++
		}
		if strings.EqualFold(s[i:j], "bytes") && !strings.HasSuffix(s[:i], "::") && !shapeKey(s, i, j) {
			out = append(out, ast.Span{Start: i, End: j})
		}
		i = j
	}
	return out
}

// shapeKey reports whether the word s[i:j] is a key of a shape: after `{`
// or `,`, before `:` or `?:` (and not `::`).
func shapeKey(s string, i, j int) bool {
	before := strings.TrimRight(s[:i], " \t")
	if before == "" || (before[len(before)-1] != '{' && before[len(before)-1] != ',') {
		return false
	}
	after := strings.TrimLeft(s[j:], " \t")
	after = strings.TrimPrefix(after, "?")
	return strings.HasPrefix(after, ":") && !strings.HasPrefix(after, "::")
}
