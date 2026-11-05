package emit

import (
	"bytes"
	"sort"
	"strings"

	"github.com/phaenonlang/phaenon/internal/ast"
	"github.com/phaenonlang/phaenon/internal/docblock"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/token"
)

// Phaenon's vocabulary in the docblocks a .phn writes by hand becomes
// PHPStan's in the output (design/collections.md §4, strings.md §5). The
// tags the emitter generates are spelled for PHPStan already; the tags a
// .phn writes — `@return bytes`, `@param map<string, int> $m`, `@var
// (string, int)`, a shape that holds them — get the same spelling here:
// `bytes` → `string`, `map<` → `array<`, `non-empty-map<` →
// `non-empty-array<`, `class<` → `class-string<`, a tuple `(A, B)` →
// `array{A, B}`, `Closure(` → `\Closure(`. Left alone, PHPStan reads `map`
// or `bytes` as a class of the file's namespace, `Closure` too, and a tuple
// as a syntax error. Only the types of tags are rewritten, word for word
// and bracket for bracket, so prose and line breaks stay where they were.

// translateWrittenDocs translates the types of every docblock of a file
// compiled with type information (phn build). It runs after the lowering
// passes: a span one of them already rewrote (addDoc, replaceVar) holds
// generated text and is left to it.
func (e *emitter) translateWrittenDocs() {
	for _, t := range lexer.Tokenize(e.src, lexer.ModePhaenon) {
		if t.Kind != token.T_DOC_COMMENT {
			continue
		}
		for _, ed := range docTranslations(string(e.src[t.Start:t.End])) {
			span := ast.Span{Start: t.Start + ed.Start, End: t.Start + ed.End}
			if !e.edited(span) {
				e.replace(span, ed.text)
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

// translateDoc is docTranslations applied to a docblock's text (or to the
// content of a one-line docblock, which addDoc copies into a new one).
func translateDoc(doc string) string {
	eds := docTranslations(doc)
	if len(eds) == 0 {
		return doc
	}
	var b strings.Builder
	pos := 0
	for _, ed := range eds {
		b.WriteString(doc[pos:ed.Start])
		b.WriteString(ed.text)
		pos = ed.End
	}
	b.WriteString(doc[pos:])
	return b.String()
}

// docEdit is the translation of one tag's type: its span in the docblock
// and the text that replaces it, with the same line breaks.
type docEdit struct {
	ast.Span
	text string
}

// docTranslations finds the tag types of a docblock that are spelled in
// Phaenon's vocabulary, as offsets into doc, in order.
func docTranslations(doc string) []docEdit {
	blank := blankDecorations(doc)
	var out []docEdit
	off := 0
	for _, line := range strings.SplitAfter(blank, "\n") {
		for _, r := range tagTypeRegions(blank, off, off+len(line)) {
			if text := translateType(doc[r.Start:r.End], blank[r.Start:r.End]); text != doc[r.Start:r.End] {
				out = append(out, docEdit{r, text})
			}
		}
		off += len(line)
	}
	return out
}

// blankDecorations is doc with the comment's own marks — `/**`, `*/` and
// the `*` that opens a line — turned into spaces, so that a type running
// over several lines parses as one, at the same offsets.
func blankDecorations(doc string) string {
	b := []byte(doc)
	if bytes.HasPrefix(b, []byte("/**")) {
		copy(b, "   ")
	}
	if bytes.HasSuffix(b, []byte("*/")) {
		copy(b[len(b)-2:], "  ")
	}
	lineStart := true
	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == '\n':
			lineStart = true
		case lineStart && (b[i] == ' ' || b[i] == '\t'):
		case lineStart && b[i] == '*':
			b[i] = ' '
			lineStart = false
		default:
			lineStart = false
		}
	}
	return string(b)
}

// tagTypeRegions returns the parts of the docblock line blank[start:end]
// that are type syntax, as offsets into blank: the type of @param,
// @return, @var and their kin, a template's bound and default, a type
// alias's body, a @method's return type and parameter list. A type may
// run over the following lines, as a shape written one key per line does;
// a type that does not parse is left alone.
func tagTypeRegions(blank string, start, end int) []ast.Span {
	line := blank[start:end]
	at := strings.IndexByte(line, '@')
	if at < 0 || strings.TrimLeft(line[:at], " \t") != "" {
		return nil
	}
	nameEnd := at
	for nameEnd < len(line) && line[nameEnd] != ' ' && line[nameEnd] != '\t' && line[nameEnd] != '\n' && line[nameEnd] != '\r' {
		nameEnd++
	}
	name := strings.ToLower(line[at:nameEnd])
	for _, p := range []string{"@phpstan-", "@psalm-", "@phan-"} {
		if strings.HasPrefix(name, p) {
			name = "@" + strings.TrimPrefix(name, p)
			break
		}
	}
	// typeAt parses the type starting at line offset i (after blanks) and
	// returns its region in blank and the line offset after it.
	typeAt := func(i int) (ast.Span, int, bool) {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		text := blank[start+i:]
		if strings.TrimSpace(text) == "" || text[0] == '$' || text[0] == '\n' || text[0] == '\r' {
			return ast.Span{}, i, false
		}
		_, rest, err := docblock.ParseTypePrefix(text)
		if err != nil {
			return ast.Span{}, i, false
		}
		n := len(strings.TrimRight(text[:len(text)-len(rest)], " \t\r\n"))
		return ast.Span{Start: start + i, End: start + i + n}, i + n, true
	}
	switch name {
	case "@param", "@param-out", "@return", "@var", "@throws", "@extends", "@implements", "@use",
		"@mixin", "@template-extends", "@template-implements", "@template-use", "@property",
		"@property-read", "@property-write", "@self-out", "@this-out", "@require-extends",
		"@require-implements":
		if r, _, ok := typeAt(nameEnd); ok {
			return []ast.Span{r}
		}
	case "@assert", "@assert-if-true", "@assert-if-false":
		i := nameEnd
		for i < len(line) && strings.IndexByte(" \t!=", line[i]) >= 0 {
			i++
		}
		if r, _, ok := typeAt(i); ok {
			return []ast.Span{r}
		}
	case "@template", "@template-covariant", "@template-contravariant":
		var out []ast.Span
		rest := line[nameEnd:]
		for _, kw := range []string{" of ", " as ", " = "} {
			if i := strings.Index(rest, kw); i >= 0 {
				if r, _, ok := typeAt(nameEnd + i + len(kw)); ok {
					out = append(out, r)
				}
			}
		}
		return out
	case "@type":
		// `@phpstan-type Name = type` or `@phpstan-type Name type`.
		i := nameEnd
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
		i := nameEnd
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
			out = append(out, ast.Span{Start: start + open + 1, End: start + close})
		}
		return out
	}
	return nil
}

// translateType spells a piece of type syntax for PHPStan. orig is the
// text as written, blank the same text with the comment's marks blanked
// (blankDecorations): the words and brackets are read from blank and
// replaced in orig, so every other byte stays.
func translateType(orig, blank string) string {
	type repl struct {
		start, end int
		text       string
	}
	var rs []repl
	s := blank
	next := func(j int) byte {
		for j < len(s) && strings.IndexByte(" \t\r\n", s[j]) >= 0 {
			j++
		}
		if j < len(s) {
			return s[j]
		}
		return 0
	}
	for i := 0; i < len(s); {
		c := s[i]
		if c == '\'' || c == '"' {
			j := strings.IndexByte(s[i+1:], c)
			if j < 0 {
				break
			}
			i += j + 2
			continue
		}
		if c == '(' {
			if close, ok := tupleAt(s, i); ok {
				rs = append(rs, repl{i, i + 1, "array{"}, repl{close, close + 1, "}"})
			}
			i++
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
		if !strings.HasSuffix(s[:i], "::") && !shapeKey(s, i, j) {
			w := strings.ToLower(s[i:j])
			switch {
			case w == "bytes":
				rs = append(rs, repl{i, j, "string"})
			case w == "map" && next(j) == '<':
				rs = append(rs, repl{i, j, "array"})
			case w == "non-empty-map" && next(j) == '<':
				rs = append(rs, repl{i, j, "non-empty-array"})
			case w == "class" && next(j) == '<':
				rs = append(rs, repl{i, j, "class-string"})
			case w == "closure" && next(j) == '(':
				rs = append(rs, repl{i, i, "\\"})
			}
		}
		i = j
	}
	if len(rs) == 0 {
		return orig
	}
	sort.Slice(rs, func(a, b int) bool { return rs[a].start < rs[b].start })
	var b strings.Builder
	pos := 0
	for _, r := range rs {
		b.WriteString(orig[pos:r.start])
		b.WriteString(r.text)
		pos = r.end
	}
	b.WriteString(orig[pos:])
	return b.String()
}

// tupleAt reports whether the `(` at s[i] opens a tuple, `(A, B)`, and
// where its `)` is: a parenthesis where a type starts (not a callable's
// parameter list, which follows a name) holding a comma at its own level.
// A parenthesis without one groups (`(A|B)[]`) or holds a conditional.
func tupleAt(s string, i int) (int, bool) {
	j := i - 1
	for j >= 0 && strings.IndexByte(" \t\r\n", s[j]) >= 0 {
		j--
	}
	if j >= 0 && strings.IndexByte("<,|&?{(:", s[j]) < 0 {
		return 0, false
	}
	depth, comma := 0, false
	for k := i; k < len(s); k++ {
		switch c := s[k]; c {
		case '\'', '"':
			q := strings.IndexByte(s[k+1:], c)
			if q < 0 {
				return 0, false
			}
			k += q + 1
		case '(', '<', '{', '[':
			depth++
		case ')', '>', '}', ']':
			depth--
			if depth == 0 {
				return k, comma && c == ')'
			}
		case ',':
			if depth == 1 {
				comma = true
			}
		}
	}
	return 0, false
}

// wordChar reports whether c belongs to a name in type syntax: a class
// name with its namespace, a hyphenated PHPStan name, a variable.
func wordChar(c byte) bool {
	return c == '_' || c == '\\' || c == '-' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
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
