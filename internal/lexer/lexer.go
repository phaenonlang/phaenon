// Package lexer turns PHP / Phaenon source into tokens.
//
// It is a hand-written port of the state machine in php-src's
// Zend/zend_language_scanner.l. Every rule that affects which tokens come
// out (as opposed to which error message PHP prints) is mirrored, so the lexer
// can be diffed token-for-token against PhpToken::tokenize() on the php-src
// test corpus. The lexer is lossless: concatenating the text of all tokens
// reproduces the source byte for byte.
package lexer

import (
	"bytes"
	"strconv"

	"github.com/phaenonlang/phaenon/internal/token"
)

// Mode selects the dialect being lexed.
type Mode uint8

const (
	// ModePHP lexes a PHP file: inline HTML until an open tag, close tags
	// return to HTML, short open tags are on (php-src's compiled default).
	ModePHP Mode = iota
	// ModePhaenon lexes a .phn file: code from byte 0, no open/close tags,
	// Phaenon's contextual keywords enabled.
	ModePhaenon
)

type state uint8

const (
	stInitial state = iota
	stScripting
	stDoubleQuotes
	stBackquote
	stHeredoc
	stNowdoc
	stEndHeredoc
	stLookingForProperty
	stLookingForVarname
	stVarOffset
)

type heredoc struct {
	label       []byte
	indentation int // indentation of the closing label, known once seen
}

// Lexer produces tokens one at a time.
type Lexer struct {
	src   []byte
	pos   int
	line  int
	mode  Mode
	state state
	stack []state
	// heredocs is Zend's heredoc_label_stack: nested heredocs appear inside
	// {$...} interpolations.
	heredocs []*heredoc
	// pendingDQ mirrors SET_DOUBLE_QUOTES_SCANNED_LENGTH: the opening '"'
	// rule already found where the first segment ends.
	pendingDQ int
	// haltTokens mirrors ext/tokenizer: after T_HALT_COMPILER the next three
	// non-trivia tokens ("(", ")", ";") are lexed, then the rest of the input
	// is one T_INLINE_HTML.
	haltTokens int
	halted     bool
}

// New returns a lexer over src.
func New(src []byte, mode Mode) *Lexer {
	l := &Lexer{src: src, line: 1, mode: mode}
	if mode == ModePhaenon {
		l.state = stScripting
	}
	return l
}

// Tokenize lexes the whole input. The last token is EOF.
func Tokenize(src []byte, mode Mode) []token.Token {
	l := New(src, mode)
	var toks []token.Token
	for {
		t := l.Next()
		toks = append(toks, t)
		if t.Kind == token.EOF {
			return toks
		}
	}
}

func (l *Lexer) begin(s state) { l.state = s }
func (l *Lexer) push(s state)  { l.stack = append(l.stack, l.state); l.state = s }
func (l *Lexer) pop() {
	if n := len(l.stack); n > 0 {
		l.state = l.stack[n-1]
		l.stack = l.stack[:n-1]
	}
}

func (l *Lexer) at(i int) byte {
	if i < len(l.src) {
		return l.src[i]
	}
	return 0
}

func (l *Lexer) hasPrefixFold(i int, s string) bool {
	if i+len(s) > len(l.src) {
		return false
	}
	for k := 0; k < len(s); k++ {
		if toLower(l.src[i+k]) != s[k] {
			return false
		}
	}
	return true
}

func (l *Lexer) hasPrefix(i int, s string) bool {
	return i+len(s) <= len(l.src) && string(l.src[i:i+len(s)]) == s
}

func toLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

func isLabelStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isLabelChar(c byte) bool { return isLabelStart(c) || (c >= '0' && c <= '9') }
func isDigit(c byte) bool     { return c >= '0' && c <= '9' }
func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
func isWhitespace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// labelEnd returns the end offset of the label starting at i (i itself must
// be a label start).
func (l *Lexer) labelEnd(i int) int {
	for i < len(l.src) && isLabelChar(l.src[i]) {
		i++
	}
	return i
}

// emit produces the token [l.pos, end) and advances.
func (l *Lexer) emit(k token.Kind, end int) token.Token {
	if l.haltTokens > 0 && !k.IsTrivia() && k != token.T_OPEN_TAG {
		l.haltTokens--
		if l.haltTokens == 0 {
			l.halted = true
		}
	} else if k == token.T_HALT_COMPILER {
		l.haltTokens = 3
	}
	t := token.Token{Kind: k, Start: l.pos, End: end, Line: l.line}
	for i := l.pos; i < end; i++ {
		switch l.src[i] {
		case '\n':
			l.line++
		case '\r':
			if i+1 >= end || l.src[i+1] != '\n' {
				l.line++
			}
		}
	}
	l.pos = end
	return t
}

// Next returns the next token; EOF forever once the input is exhausted.
func (l *Lexer) Next() token.Token {
	if l.pos >= len(l.src) {
		if l.state == stEndHeredoc && len(l.heredocs) > 0 {
			// Unreachable in practice: end-heredoc is only entered with input left.
			l.begin(stScripting)
		}
		return token.Token{Kind: token.EOF, Start: len(l.src), End: len(l.src), Line: l.line}
	}
	if l.halted {
		return l.emit(token.T_INLINE_HTML, len(l.src))
	}
	switch l.state {
	case stInitial:
		return l.initial()
	case stScripting:
		return l.scripting()
	case stDoubleQuotes:
		return l.interpolated('"')
	case stBackquote:
		return l.interpolated('`')
	case stHeredoc:
		return l.interpolated(0)
	case stNowdoc:
		return l.nowdoc()
	case stEndHeredoc:
		return l.endHeredoc()
	case stLookingForProperty:
		return l.lookingForProperty()
	case stLookingForVarname:
		return l.lookingForVarname()
	case stVarOffset:
		return l.varOffset()
	}
	panic("lexer: bad state")
}

// ---------------------------------------------------------------- INITIAL

func (l *Lexer) initial() token.Token {
	if k, end := l.openTag(l.pos); k != token.EOF {
		l.begin(stScripting)
		return l.emit(k, end)
	}
	// inline_char_handler: find the next open tag.
	i := l.pos
	for {
		j := bytes.IndexByte(l.src[i:], '<')
		if j < 0 {
			return l.emit(token.T_INLINE_HTML, len(l.src))
		}
		i += j
		if k, _ := l.openTag(i); k != token.EOF {
			return l.emit(token.T_INLINE_HTML, i)
		}
		i++
	}
}

// openTag reports the open tag starting at i, or EOF when there is none.
func (l *Lexer) openTag(i int) (token.Kind, int) {
	if !l.hasPrefix(i, "<?") {
		return token.EOF, 0
	}
	if l.at(i+2) == '=' {
		return token.T_OPEN_TAG_WITH_ECHO, i + 3
	}
	if l.hasPrefixFold(i, "<?php") {
		j := i + 5
		switch l.at(j) {
		case ' ', '\t', '\n':
			return token.T_OPEN_TAG, j + 1
		case '\r':
			if l.at(j+1) == '\n' {
				return token.T_OPEN_TAG, j + 2
			}
			return token.T_OPEN_TAG, j + 1
		}
		if j >= len(l.src) {
			return token.T_OPEN_TAG, j
		}
		// Degenerate case: <?phpX is <? phpX with short tags on.
	}
	return token.T_OPEN_TAG, i + 2 // short open tag
}

// ---------------------------------------------------------------- SCRIPTING

func (l *Lexer) scripting() token.Token {
	i := l.pos
	c := l.src[i]

	// Whitespace.
	if isWhitespace(c) {
		j := i + 1
		for j < len(l.src) && isWhitespace(l.src[j]) {
			j++
		}
		return l.emit(token.T_WHITESPACE, j)
	}

	// Comments.
	if c == '#' || (c == '/' && l.at(i+1) == '/') {
		if c == '#' && l.at(i+1) == '[' {
			return l.emit(token.T_ATTRIBUTE, i+2)
		}
		return l.emit(token.T_COMMENT, l.singleLineCommentEnd(i))
	}
	if c == '/' && l.at(i+1) == '*' {
		return l.multiLineComment(i)
	}

	// Close tag (PHP mode only).
	if c == '?' && l.at(i+1) == '>' && l.mode == ModePHP {
		j := i + 2
		switch l.at(j) {
		case '\n':
			j++
		case '\r':
			j++
			if l.at(j) == '\n' {
				j++
			}
		}
		l.begin(stInitial)
		return l.emit(token.T_CLOSE_TAG, j)
	}
	if l.mode == ModePhaenon && i == 0 && l.hasPrefixFold(0, "<?php") {
		// Not valid Phaenon, but lex it as one token so the parser can say
		// "remove the <?php tag" instead of three confusing errors.
		return l.emit(token.T_OPEN_TAG, 5)
	}

	// Variables.
	if c == '$' {
		if isLabelStart(l.at(i + 1)) {
			return l.emit(token.T_VARIABLE, l.labelEnd(i+1))
		}
		return l.emit(token.Kind('$'), i+1)
	}

	// Numbers.
	if isDigit(c) || (c == '.' && isDigit(l.at(i+1))) {
		return l.number(i)
	}

	// Strings.
	if c == '\'' || c == '"' || c == '`' {
		return l.stringStart(i, 0)
	}
	if (c == 'b' || c == 'B') && (l.at(i+1) == '\'' || l.at(i+1) == '"') {
		return l.stringStart(i, 1)
	}
	if c == '<' && l.hasPrefix(i, "<<<") {
		if t, ok := l.heredocStart(i, 0); ok {
			return t
		}
	}
	if (c == 'b' || c == 'B') && l.hasPrefix(i+1, "<<<") {
		if t, ok := l.heredocStart(i, 1); ok {
			return t
		}
	}

	// Casts: "(" [ \t]* word [ \t]* ")".
	if c == '(' {
		if k, end := l.cast(i); k != token.EOF {
			return l.emit(k, end)
		}
	}

	// Names and keywords.
	if isLabelStart(c) {
		return l.word(i)
	}
	if c == '\\' {
		if isLabelStart(l.at(i + 1)) {
			return l.emit(token.T_NAME_FULLY_QUALIFIED, l.qualifiedNameEnd(i+1))
		}
		return l.emit(token.T_NS_SEPARATOR, i+1)
	}

	// Operators, longest first.
	for _, op := range operators {
		if l.hasPrefix(i, op.text) {
			switch op.kind {
			case token.T_OBJECT_OPERATOR, token.T_NULLSAFE_OBJECT_OPERATOR:
				l.push(stLookingForProperty)
			case token.Kind('{'):
				l.push(stScripting)
			case token.Kind('}'):
				l.pop()
			case token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG:
				j := l.skipWhitespaceAndComments(i + 1)
				if l.at(j) == '$' || l.hasPrefix(j, "...") {
					return l.emit(token.T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG, i+1)
				}
			}
			return l.emit(op.kind, i+len(op.text))
		}
	}

	return l.emit(token.T_BAD_CHARACTER, i+1)
}

type operator struct {
	text string
	kind token.Kind
}

// operators is ordered longest-first so the first match is the longest match.
var operators = []operator{
	{"<<=", token.T_SL_EQUAL}, {">>=", token.T_SR_EQUAL}, {"**=", token.T_POW_EQUAL},
	{"...", token.T_ELLIPSIS}, {"<=>", token.T_SPACESHIP}, {"===", token.T_IS_IDENTICAL},
	{"!==", token.T_IS_NOT_IDENTICAL}, {"??=", token.T_COALESCE_EQUAL}, {"?->", token.T_NULLSAFE_OBJECT_OPERATOR},
	{"<>", token.T_IS_NOT_EQUAL}, {"!=", token.T_IS_NOT_EQUAL}, {"==", token.T_IS_EQUAL},
	{"<=", token.T_IS_SMALLER_OR_EQUAL}, {">=", token.T_IS_GREATER_OR_EQUAL},
	{"&&", token.T_BOOLEAN_AND}, {"||", token.T_BOOLEAN_OR}, {"??", token.T_COALESCE},
	{"**", token.T_POW}, {"++", token.T_INC}, {"--", token.T_DEC},
	{"+=", token.T_PLUS_EQUAL}, {"-=", token.T_MINUS_EQUAL}, {"*=", token.T_MUL_EQUAL},
	{"/=", token.T_DIV_EQUAL}, {".=", token.T_CONCAT_EQUAL}, {"%=", token.T_MOD_EQUAL},
	{"&=", token.T_AND_EQUAL}, {"|=", token.T_OR_EQUAL}, {"^=", token.T_XOR_EQUAL},
	{"<<", token.T_SL}, {">>", token.T_SR}, {"=>", token.T_DOUBLE_ARROW},
	{"->", token.T_OBJECT_OPERATOR}, {"::", token.T_PAAMAYIM_NEKUDOTAYIM}, {"|>", token.T_PIPE},
	{"&", token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG},
	{";", ';'}, {":", ':'}, {",", ','}, {".", '.'}, {"[", '['}, {"]", ']'}, {"(", '('}, {")", ')'},
	{"|", '|'}, {"^", '^'}, {"+", '+'}, {"-", '-'}, {"/", '/'}, {"*", '*'}, {"=", '='}, {"%", '%'},
	{"!", '!'}, {"~", '~'}, {"<", '<'}, {">", '>'}, {"?", '?'}, {"@", '@'}, {"{", '{'}, {"}", '}'},
}

func (l *Lexer) singleLineCommentEnd(i int) int {
	for i < len(l.src) {
		switch l.src[i] {
		case '\n', '\r':
			return i
		case '?':
			if l.at(i+1) == '>' && l.mode == ModePHP {
				return i
			}
		}
		i++
	}
	return i
}

func (l *Lexer) multiLineComment(i int) token.Token {
	kind := token.T_COMMENT
	if l.at(i+2) == '*' && isWhitespace(l.at(i+3)) {
		kind = token.T_DOC_COMMENT
	}
	j := bytes.Index(l.src[i+2:], []byte("*/"))
	if j < 0 {
		return l.emit(kind, len(l.src)) // unterminated comment
	}
	return l.emit(kind, i+2+j+2)
}

var casts = []struct {
	word string
	kind token.Kind
}{
	{"int", token.T_INT_CAST}, {"integer", token.T_INT_CAST},
	{"float", token.T_DOUBLE_CAST}, {"double", token.T_DOUBLE_CAST}, {"real", token.T_DOUBLE_CAST},
	{"string", token.T_STRING_CAST}, {"binary", token.T_STRING_CAST},
	{"array", token.T_ARRAY_CAST}, {"object", token.T_OBJECT_CAST},
	{"bool", token.T_BOOL_CAST}, {"boolean", token.T_BOOL_CAST},
	{"unset", token.T_UNSET_CAST}, {"void", token.T_VOID_CAST},
}

func (l *Lexer) cast(i int) (token.Kind, int) {
	j := i + 1
	for l.at(j) == ' ' || l.at(j) == '\t' {
		j++
	}
	try := func(word string, kind token.Kind) (token.Kind, int) {
		if !l.hasPrefixFold(j, word) {
			return token.EOF, 0
		}
		k := j + len(word)
		for l.at(k) == ' ' || l.at(k) == '\t' {
			k++
		}
		if l.at(k) == ')' {
			return kind, k + 1
		}
		return token.EOF, 0
	}
	for _, c := range casts {
		if k, end := try(c.word, c.kind); k != token.EOF {
			return k, end
		}
	}
	if l.mode == ModePhaenon {
		// (bytes) retypes a string as binary data (design/strings.md §2);
		// in PHP it would be a parenthesised constant.
		return try("bytes", token.T_BYTES_CAST)
	}
	return token.EOF, 0
}

func (l *Lexer) qualifiedNameEnd(i int) int {
	i = l.labelEnd(i)
	for l.at(i) == '\\' && isLabelStart(l.at(i+1)) {
		i = l.labelEnd(i + 1)
	}
	return i
}

// word lexes a label: keyword, name, or qualified name.
func (l *Lexer) word(i int) token.Token {
	end := l.labelEnd(i)
	if l.at(end) == '\\' && isLabelStart(l.at(end+1)) {
		qend := l.qualifiedNameEnd(i)
		if bytes.EqualFold(l.src[i:end], []byte("namespace")) {
			return l.emit(token.T_NAME_RELATIVE, qend)
		}
		return l.emit(token.T_NAME_QUALIFIED, qend)
	}
	lower := bytes.ToLower(l.src[i:end])
	switch string(lower) {
	case "yield":
		// "yield" WHITESPACE "from" not followed by a label char.
		j := end
		for j < len(l.src) && isWhitespace(l.src[j]) {
			j++
		}
		if j > end && l.hasPrefixFold(j, "from") && !isLabelChar(l.at(j+4)) {
			return l.emit(token.T_YIELD_FROM, j+4)
		}
		return l.emit(token.T_YIELD, end)
	case "enum":
		// "enum" is a keyword only before a label that is not extends/implements.
		j := l.skipWhitespaceAndComments(end)
		if j > end && isLabelStart(l.at(j)) {
			w := l.labelEnd(j)
			if !bytes.EqualFold(l.src[j:w], []byte("extends")) && !bytes.EqualFold(l.src[j:w], []byte("implements")) {
				return l.emit(token.T_ENUM, end)
			}
		}
		return l.emit(token.T_STRING, end)
	case "public", "protected", "private":
		if l.hasPrefixFold(end, "(set)") {
			switch string(lower) {
			case "public":
				return l.emit(token.T_PUBLIC_SET, end+5)
			case "protected":
				return l.emit(token.T_PROTECTED_SET, end+5)
			default:
				return l.emit(token.T_PRIVATE_SET, end+5)
			}
		}
	}
	if k, ok := token.Keywords[string(lower)]; ok {
		return l.emit(k, end)
	}
	if l.mode == ModePhaenon {
		if k, ok := phaenonKeywords[string(lower)]; ok {
			return l.emit(k, end)
		}
		if string(lower) == "fun" && l.declaresName(end) {
			return l.emit(token.T_FUN, end)
		}
	}
	return l.emit(token.T_STRING, end)
}

// declaresName reports whether a name (or `&` and a name) follows offset i
// after trivia: `fun id(` declares, `fun(` calls a function called fun and
// `fun = 1` names a constant (design/functions.md §1.1).
func (l *Lexer) declaresName(i int) bool {
	j := l.skipWhitespaceAndComments(i)
	if l.at(j) == '&' {
		j = l.skipWhitespaceAndComments(j + 1)
	}
	return isLabelStart(l.at(j))
}

// phaenonKeywords are reserved only in Phaenon mode. The contextual ones
// (type, of, in, out) are lexed as keywords and re-interpreted as
// identifiers by the parser where the grammar allows a name.
var phaenonKeywords = map[string]token.Kind{
	"is": token.T_IS, "type": token.T_TYPE, "of": token.T_OF, "in": token.T_IN, "out": token.T_OUT,
}

func (l *Lexer) skipWhitespaceAndComments(i int) int {
	for i < len(l.src) {
		c := l.src[i]
		switch {
		case isWhitespace(c):
			i++
		case c == '#' && l.at(i+1) != '[', c == '/' && l.at(i+1) == '/':
			i = l.singleLineCommentEnd(i)
		case c == '/' && l.at(i+1) == '*':
			j := bytes.Index(l.src[i+2:], []byte("*/"))
			if j < 0 {
				return len(l.src)
			}
			i += 2 + j + 2
		default:
			return i
		}
	}
	return i
}

// ---------------------------------------------------------------- numbers

// digitsEnd scans digits of the given class with single underscores between
// digits, starting at i which must be a digit.
func (l *Lexer) digitsEnd(i int, ok func(byte) bool) int {
	for i < len(l.src) {
		c := l.src[i]
		switch {
		case ok(c):
			i++
		case c == '_' && ok(l.at(i+1)):
			i += 2
		default:
			return i
		}
	}
	return i
}

func stripUnderscores(b []byte) []byte {
	if bytes.IndexByte(b, '_') < 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c != '_' {
			out = append(out, c)
		}
	}
	return out
}

func (l *Lexer) number(i int) token.Token {
	c := l.src[i]
	if c == '0' {
		switch l.at(i + 1) {
		case 'x', 'X':
			if isHexDigit(l.at(i + 2)) {
				end := l.digitsEnd(i+2, isHexDigit)
				return l.emit(l.prefixedIntKind(l.src[i+2:end], 16), end)
			}
		case 'b', 'B':
			if l.at(i+2) == '0' || l.at(i+2) == '1' {
				end := l.digitsEnd(i+2, func(c byte) bool { return c == '0' || c == '1' })
				return l.emit(l.prefixedIntKind(l.src[i+2:end], 2), end)
			}
		case 'o', 'O':
			if l.at(i+2) >= '0' && l.at(i+2) <= '7' {
				end := l.digitsEnd(i+2, func(c byte) bool { return c >= '0' && c <= '7' })
				return l.emit(l.prefixedIntKind(l.src[i+2:end], 8), end)
			}
		}
	}

	// LNUM, DNUM or EXPONENT_DNUM.
	end := i
	isFloat := false
	if isDigit(c) {
		end = l.digitsEnd(i, isDigit)
	}
	if l.at(end) == '.' {
		if isDigit(l.at(end + 1)) {
			end = l.digitsEnd(end+1, isDigit)
			isFloat = true
		} else if end > i {
			end++ // "1." is a float
			isFloat = true
		}
	}
	if e := l.at(end); e == 'e' || e == 'E' {
		j := end + 1
		if l.at(j) == '+' || l.at(j) == '-' {
			j++
		}
		if isDigit(l.at(j)) {
			end = l.digitsEnd(j, isDigit)
			isFloat = true
		}
	}
	if isFloat {
		return l.emit(token.T_DNUMBER, end)
	}

	// LNUM: decide int vs float overflow the way Zend does.
	digits := stripUnderscores(l.src[i:end])
	isOctal := digits[0] == '0'
	if isOctal {
		for k, d := range digits {
			if d == '8' || d == '9' {
				digits = digits[:k] // "Invalid numeric literal" — value truncated, still T_LNUMBER
				break
			}
		}
	}
	const maxLengthOfLong = 20 // digits in -9223372036854775808
	if len(digits) < maxLengthOfLong-1 {
		return l.emit(token.T_LNUMBER, end)
	}
	base := 10
	if isOctal {
		base = 8
	}
	if _, err := strconv.ParseInt(string(digits), base, 64); err != nil {
		return l.emit(token.T_DNUMBER, end)
	}
	return l.emit(token.T_LNUMBER, end)
}

// prefixedIntKind decides T_LNUMBER vs T_DNUMBER for 0x / 0b / 0o literals
// (digits exclude the prefix, may contain underscores).
func (l *Lexer) prefixedIntKind(digits []byte, base int) token.Kind {
	d := stripUnderscores(digits)
	for len(d) > 0 && d[0] == '0' {
		d = d[1:]
	}
	var maxLen int
	switch base {
	case 16:
		maxLen = 16
	case 2:
		maxLen = 64
	default:
		maxLen = 22
	}
	if len(d) < maxLen {
		return token.T_LNUMBER
	}
	if _, err := strconv.ParseInt(string(d), base, 64); err != nil {
		return token.T_DNUMBER
	}
	return token.T_LNUMBER
}

// ---------------------------------------------------------------- strings

// stringStart lexes a quote at i+bprefix (bprefix is 1 for b"...").
func (l *Lexer) stringStart(i, bprefix int) token.Token {
	q := l.src[i+bprefix]
	j := i + bprefix + 1
	switch q {
	case '\'':
		for j < len(l.src) {
			switch l.src[j] {
			case '\'':
				return l.emit(token.T_CONSTANT_ENCAPSED_STRING, j+1)
			case '\\':
				j++
			}
			j++
		}
		// Unclosed single-quoted string: the rest of the input.
		return l.emit(token.T_ENCAPSED_AND_WHITESPACE, len(l.src))
	case '"':
		for j < len(l.src) {
			switch l.src[j] {
			case '"':
				return l.emit(token.T_CONSTANT_ENCAPSED_STRING, j+1)
			case '$':
				if isLabelStart(l.at(j+1)) || l.at(j+1) == '{' {
					l.pendingDQ = j - (i + bprefix + 1)
					l.begin(stDoubleQuotes)
					return l.emit(token.Kind('"'), i+bprefix+1)
				}
			case '{':
				if l.at(j+1) == '$' {
					l.pendingDQ = j - (i + bprefix + 1)
					l.begin(stDoubleQuotes)
					return l.emit(token.Kind('"'), i+bprefix+1)
				}
			case '\\':
				j++
			}
			j++
		}
		// Unterminated: the rest becomes one encapsed segment.
		l.pendingDQ = len(l.src) - (i + bprefix + 1)
		l.begin(stDoubleQuotes)
		return l.emit(token.Kind('"'), i+bprefix+1)
	default: // '`'
		l.begin(stBackquote)
		return l.emit(token.Kind('`'), i+1)
	}
}

// heredocStart lexes b?"<<<"[ \t]*('LABEL'|"LABEL"|LABEL)NEWLINE at i.
func (l *Lexer) heredocStart(i, bprefix int) (token.Token, bool) {
	j := i + bprefix + 3
	for l.at(j) == ' ' || l.at(j) == '\t' {
		j++
	}
	quote := byte(0)
	if l.at(j) == '\'' || l.at(j) == '"' {
		quote = l.src[j]
		j++
	}
	if !isLabelStart(l.at(j)) {
		return token.Token{}, false
	}
	labelStart := j
	j = l.labelEnd(j)
	label := l.src[labelStart:j]
	if quote != 0 {
		if l.at(j) != quote {
			return token.Token{}, false
		}
		j++
	}
	switch l.at(j) {
	case '\n':
		j++
	case '\r':
		j++
		if l.at(j) == '\n' {
			j++
		}
	default:
		return token.Token{}, false
	}

	h := &heredoc{label: label}
	l.heredocs = append(l.heredocs, h)
	if quote == '\'' {
		l.begin(stNowdoc)
	} else {
		l.begin(stHeredoc)
	}
	// Closing label on the very next line → empty body.
	if ind, ok := l.closingLabelAt(j, h); ok {
		h.indentation = ind
		l.begin(stEndHeredoc)
	}
	return l.emit(token.T_START_HEREDOC, j), true
}

// closingLabelAt reports whether a line starting at i is the closing label
// (optional indentation, label, non-label char or EOF), and its indentation.
func (l *Lexer) closingLabelAt(i int, h *heredoc) (int, bool) {
	ind := 0
	for l.at(i) == ' ' || l.at(i) == '\t' {
		i++
		ind++
	}
	if i >= len(l.src) {
		return 0, false
	}
	if !isLabelStart(l.src[i]) || !bytes.HasPrefix(l.src[i:], h.label) {
		return 0, false
	}
	if isLabelChar(l.at(i + len(h.label))) {
		return 0, false
	}
	return ind, true
}

func (l *Lexer) endHeredoc() token.Token {
	h := l.heredocs[len(l.heredocs)-1]
	l.heredocs = l.heredocs[:len(l.heredocs)-1]
	l.begin(stScripting)
	return l.emit(token.T_END_HEREDOC, l.pos+h.indentation+len(h.label))
}

func (l *Lexer) nowdoc() token.Token {
	h := l.heredocs[len(l.heredocs)-1]
	i := l.pos
	for i < len(l.src) {
		c := l.src[i]
		i++
		if c == '\r' && l.at(i) == '\n' {
			i++
		}
		if c == '\n' || c == '\r' {
			if ind, ok := l.closingLabelAt(i, h); ok {
				h.indentation = ind
				l.begin(stEndHeredoc)
				return l.emit(token.T_ENCAPSED_AND_WHITESPACE, i)
			}
		}
	}
	return l.emit(token.T_ENCAPSED_AND_WHITESPACE, len(l.src))
}

// interpolated lexes the body of a double-quoted string (closer '"'),
// a backquote string (closer '`') or a heredoc (closer 0: label on its own line).
func (l *Lexer) interpolated(closer byte) token.Token {
	i := l.pos
	c := l.src[i]

	// Closing delimiter.
	if closer != 0 && c == closer {
		l.begin(stScripting)
		return l.emit(token.Kind(closer), i+1)
	}

	// Interpolation starts.
	if c == '$' {
		if isLabelStart(l.at(i + 1)) {
			end := l.labelEnd(i + 1)
			switch {
			case l.at(end) == '[':
				l.push(stVarOffset)
			case l.at(end) == '-' && l.at(end+1) == '>' && isLabelStart(l.at(end+2)):
				l.push(stLookingForProperty)
			case l.at(end) == '?' && l.at(end+1) == '-' && l.at(end+2) == '>' && isLabelStart(l.at(end+3)):
				l.push(stLookingForProperty)
			}
			return l.emit(token.T_VARIABLE, end)
		}
		if l.at(i+1) == '{' {
			l.push(stLookingForVarname)
			return l.emit(token.T_DOLLAR_OPEN_CURLY_BRACES, i+2)
		}
	}
	if c == '{' && l.at(i+1) == '$' {
		l.push(stScripting)
		return l.emit(token.T_CURLY_OPEN, i+1)
	}

	// A text segment.
	if l.pendingDQ > 0 && closer == '"' {
		end := i + l.pendingDQ
		l.pendingDQ = 0
		return l.emit(token.T_ENCAPSED_AND_WHITESPACE, end)
	}
	l.pendingDQ = 0
	var h *heredoc
	if closer == 0 {
		h = l.heredocs[len(l.heredocs)-1]
	}
	j := i
	if c == '\\' {
		j++ // an escaped first char never starts anything
		if j < len(l.src) && (h == nil || (l.src[j] != '\n' && l.src[j] != '\r')) {
			j++
		}
	}
	for j < len(l.src) {
		c := l.src[j]
		j++
		switch c {
		case '"', '`':
			if c == closer {
				return l.emit(token.T_ENCAPSED_AND_WHITESPACE, j-1)
			}
		case '$':
			if isLabelStart(l.at(j)) || l.at(j) == '{' {
				return l.emit(token.T_ENCAPSED_AND_WHITESPACE, j-1)
			}
		case '{':
			if l.at(j) == '$' {
				return l.emit(token.T_ENCAPSED_AND_WHITESPACE, j-1)
			}
		case '\\':
			if j < len(l.src) && (h == nil || (l.src[j] != '\n' && l.src[j] != '\r')) {
				j++
			}
		case '\r', '\n':
			if h == nil {
				continue
			}
			if c == '\r' && l.at(j) == '\n' {
				j++
			}
			if ind, ok := l.closingLabelAt(j, h); ok {
				h.indentation = ind
				l.begin(stEndHeredoc)
				return l.emit(token.T_ENCAPSED_AND_WHITESPACE, j)
			}
		}
	}
	return l.emit(token.T_ENCAPSED_AND_WHITESPACE, len(l.src))
}

// ---------------------------------------------------------------- sub-states

func (l *Lexer) lookingForProperty() token.Token {
	i := l.pos
	c := l.src[i]
	if isWhitespace(c) {
		j := i + 1
		for j < len(l.src) && isWhitespace(l.src[j]) {
			j++
		}
		return l.emit(token.T_WHITESPACE, j)
	}
	if c == '#' && l.at(i+1) != '[' || c == '/' && l.at(i+1) == '/' {
		return l.emit(token.T_COMMENT, l.singleLineCommentEnd(i))
	}
	if c == '/' && l.at(i+1) == '*' {
		return l.multiLineComment(i)
	}
	if c == '-' && l.at(i+1) == '>' {
		return l.emit(token.T_OBJECT_OPERATOR, i+2)
	}
	if c == '?' && l.at(i+1) == '-' && l.at(i+2) == '>' {
		return l.emit(token.T_NULLSAFE_OBJECT_OPERATOR, i+3)
	}
	if isLabelStart(c) {
		l.pop()
		return l.emit(token.T_STRING, l.labelEnd(i))
	}
	l.pop()
	return l.Next()
}

func (l *Lexer) lookingForVarname() token.Token {
	i := l.pos
	if isLabelStart(l.src[i]) {
		end := l.labelEnd(i)
		if l.at(end) == '[' || l.at(end) == '}' {
			l.pop()
			l.push(stScripting)
			return l.emit(token.T_STRING_VARNAME, end)
		}
	}
	l.pop()
	l.push(stScripting)
	return l.Next()
}

func (l *Lexer) varOffset() token.Token {
	i := l.pos
	c := l.src[i]
	switch {
	case c == ']':
		l.pop()
		return l.emit(token.Kind(']'), i+1)
	case isDigit(c):
		// Any numeric literal shape is one T_NUM_STRING.
		j := i + 1
		if c == '0' && (l.at(j) == 'x' || l.at(j) == 'X') && isHexDigit(l.at(j+1)) {
			j = l.digitsEnd(j+1, isHexDigit)
		} else if c == '0' && (l.at(j) == 'b' || l.at(j) == 'B') && (l.at(j+1) == '0' || l.at(j+1) == '1') {
			j = l.digitsEnd(j+1, func(c byte) bool { return c == '0' || c == '1' })
		} else if c == '0' && (l.at(j) == 'o' || l.at(j) == 'O') && l.at(j+1) >= '0' && l.at(j+1) <= '7' {
			j = l.digitsEnd(j+1, func(c byte) bool { return c >= '0' && c <= '7' })
		} else {
			j = l.digitsEnd(i, isDigit)
		}
		return l.emit(token.T_NUM_STRING, j)
	case c == '$' && isLabelStart(l.at(i+1)):
		return l.emit(token.T_VARIABLE, l.labelEnd(i+1))
	case isLabelStart(c):
		return l.emit(token.T_STRING, l.labelEnd(i))
	case c == ' ', c == '\n', c == '\r', c == '\t', c == '\\', c == '\'', c == '#':
		// Invalid offset: Zend pops and returns an empty T_ENCAPSED_AND_WHITESPACE
		// so the parser reports the error at the right place.
		l.pop()
		return l.emit(token.T_ENCAPSED_AND_WHITESPACE, i)
	}
	// TOKENS | [[(){}"`] — every other byte is itself.
	return l.emit(token.Kind(c), i+1)
}
