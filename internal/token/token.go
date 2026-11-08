// Package token defines the lexical tokens of Phaenon.
//
// The token set is PHP 8.5's (Zend/zend_language_parser.y) plus the Phaenon
// additions. Names mirror PHP's token_name() output so that the lexer can be
// checked byte-for-byte against PhpToken::tokenize() on the php-src corpus.
package token

import "fmt"

// Kind identifies a token.
type Kind uint16

// Single-character tokens use their byte value as Kind, exactly like Zend
// (so '(' is Kind('(')). Named tokens start above 255.
const (
	EOF Kind = 0

	firstNamed Kind = 256 + iota - 1

	// Literals and names.
	T_LNUMBER
	T_DNUMBER
	T_STRING
	T_NAME_FULLY_QUALIFIED
	T_NAME_RELATIVE
	T_NAME_QUALIFIED
	T_VARIABLE
	T_ENCAPSED_AND_WHITESPACE
	T_CONSTANT_ENCAPSED_STRING
	T_STRING_VARNAME
	T_NUM_STRING

	// Keywords.
	T_INCLUDE
	T_INCLUDE_ONCE
	T_EVAL
	T_REQUIRE
	T_REQUIRE_ONCE
	T_LOGICAL_OR
	T_LOGICAL_XOR
	T_LOGICAL_AND
	T_PRINT
	T_YIELD
	T_YIELD_FROM
	T_INSTANCEOF
	T_NEW
	T_CLONE
	T_EXIT
	T_IF
	T_ELSEIF
	T_ELSE
	T_ENDIF
	T_ECHO
	T_DO
	T_WHILE
	T_ENDWHILE
	T_FOR
	T_ENDFOR
	T_FOREACH
	T_ENDFOREACH
	T_DECLARE
	T_ENDDECLARE
	T_AS
	T_SWITCH
	T_ENDSWITCH
	T_CASE
	T_DEFAULT
	T_MATCH
	T_BREAK
	T_CONTINUE
	T_GOTO
	T_FUNCTION
	T_FN
	T_CONST
	T_RETURN
	T_TRY
	T_CATCH
	T_FINALLY
	T_THROW
	T_USE
	T_INSTEADOF
	T_GLOBAL
	T_STATIC
	T_ABSTRACT
	T_FINAL
	T_PRIVATE
	T_PROTECTED
	T_PUBLIC
	T_PRIVATE_SET
	T_PROTECTED_SET
	T_PUBLIC_SET
	T_READONLY
	T_VAR
	T_UNSET
	T_ISSET
	T_EMPTY
	T_HALT_COMPILER
	T_CLASS
	T_TRAIT
	T_INTERFACE
	T_ENUM
	T_EXTENDS
	T_IMPLEMENTS
	T_NAMESPACE
	T_LIST
	T_ARRAY
	T_CALLABLE

	// Magic constants.
	T_LINE
	T_FILE
	T_DIR
	T_CLASS_C
	T_TRAIT_C
	T_METHOD_C
	T_FUNC_C
	T_PROPERTY_C
	T_NS_C

	// Operators and punctuation.
	T_ATTRIBUTE
	T_PLUS_EQUAL
	T_MINUS_EQUAL
	T_MUL_EQUAL
	T_DIV_EQUAL
	T_CONCAT_EQUAL
	T_MOD_EQUAL
	T_AND_EQUAL
	T_OR_EQUAL
	T_XOR_EQUAL
	T_SL_EQUAL
	T_SR_EQUAL
	T_COALESCE_EQUAL
	T_BOOLEAN_OR
	T_BOOLEAN_AND
	T_IS_EQUAL
	T_IS_NOT_EQUAL
	T_IS_IDENTICAL
	T_IS_NOT_IDENTICAL
	T_IS_SMALLER_OR_EQUAL
	T_IS_GREATER_OR_EQUAL
	T_SPACESHIP
	T_SL
	T_SR
	T_INC
	T_DEC
	T_INT_CAST
	T_DOUBLE_CAST
	T_STRING_CAST
	T_ARRAY_CAST
	T_OBJECT_CAST
	T_BOOL_CAST
	T_UNSET_CAST
	T_VOID_CAST
	T_OBJECT_OPERATOR
	T_NULLSAFE_OBJECT_OPERATOR
	T_DOUBLE_ARROW
	T_COMMENT
	T_DOC_COMMENT
	T_OPEN_TAG
	T_OPEN_TAG_WITH_ECHO
	T_CLOSE_TAG
	T_WHITESPACE
	T_START_HEREDOC
	T_END_HEREDOC
	T_DOLLAR_OPEN_CURLY_BRACES
	T_CURLY_OPEN
	T_PAAMAYIM_NEKUDOTAYIM
	T_NS_SEPARATOR
	T_ELLIPSIS
	T_COALESCE
	T_POW
	T_POW_EQUAL
	T_PIPE
	T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG
	T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG
	T_BAD_CHARACTER
	T_INLINE_HTML

	// Phaenon additions (never produced in PHP mode).
	T_IS         // 'is'
	T_FUN        // 'fun' (contextual: before a declaration's name)
	T_TYPE       // 'type' (contextual)
	T_OF         // 'of' (contextual)
	T_IN         // 'in' (contextual)
	T_OUT        // 'out' (contextual)
	T_BYTES_CAST // '(bytes)': the retyping of a string as binary data

	lastNamed
)

var names = [...]string{
	T_LNUMBER - firstNamed:                                 "T_LNUMBER",
	T_DNUMBER - firstNamed:                                 "T_DNUMBER",
	T_STRING - firstNamed:                                  "T_STRING",
	T_NAME_FULLY_QUALIFIED - firstNamed:                    "T_NAME_FULLY_QUALIFIED",
	T_NAME_RELATIVE - firstNamed:                           "T_NAME_RELATIVE",
	T_NAME_QUALIFIED - firstNamed:                          "T_NAME_QUALIFIED",
	T_VARIABLE - firstNamed:                                "T_VARIABLE",
	T_ENCAPSED_AND_WHITESPACE - firstNamed:                 "T_ENCAPSED_AND_WHITESPACE",
	T_CONSTANT_ENCAPSED_STRING - firstNamed:                "T_CONSTANT_ENCAPSED_STRING",
	T_STRING_VARNAME - firstNamed:                          "T_STRING_VARNAME",
	T_NUM_STRING - firstNamed:                              "T_NUM_STRING",
	T_INCLUDE - firstNamed:                                 "T_INCLUDE",
	T_INCLUDE_ONCE - firstNamed:                            "T_INCLUDE_ONCE",
	T_EVAL - firstNamed:                                    "T_EVAL",
	T_REQUIRE - firstNamed:                                 "T_REQUIRE",
	T_REQUIRE_ONCE - firstNamed:                            "T_REQUIRE_ONCE",
	T_LOGICAL_OR - firstNamed:                              "T_LOGICAL_OR",
	T_LOGICAL_XOR - firstNamed:                             "T_LOGICAL_XOR",
	T_LOGICAL_AND - firstNamed:                             "T_LOGICAL_AND",
	T_PRINT - firstNamed:                                   "T_PRINT",
	T_YIELD - firstNamed:                                   "T_YIELD",
	T_YIELD_FROM - firstNamed:                              "T_YIELD_FROM",
	T_INSTANCEOF - firstNamed:                              "T_INSTANCEOF",
	T_NEW - firstNamed:                                     "T_NEW",
	T_CLONE - firstNamed:                                   "T_CLONE",
	T_EXIT - firstNamed:                                    "T_EXIT",
	T_IF - firstNamed:                                      "T_IF",
	T_ELSEIF - firstNamed:                                  "T_ELSEIF",
	T_ELSE - firstNamed:                                    "T_ELSE",
	T_ENDIF - firstNamed:                                   "T_ENDIF",
	T_ECHO - firstNamed:                                    "T_ECHO",
	T_DO - firstNamed:                                      "T_DO",
	T_WHILE - firstNamed:                                   "T_WHILE",
	T_ENDWHILE - firstNamed:                                "T_ENDWHILE",
	T_FOR - firstNamed:                                     "T_FOR",
	T_ENDFOR - firstNamed:                                  "T_ENDFOR",
	T_FOREACH - firstNamed:                                 "T_FOREACH",
	T_ENDFOREACH - firstNamed:                              "T_ENDFOREACH",
	T_DECLARE - firstNamed:                                 "T_DECLARE",
	T_ENDDECLARE - firstNamed:                              "T_ENDDECLARE",
	T_AS - firstNamed:                                      "T_AS",
	T_SWITCH - firstNamed:                                  "T_SWITCH",
	T_ENDSWITCH - firstNamed:                               "T_ENDSWITCH",
	T_CASE - firstNamed:                                    "T_CASE",
	T_DEFAULT - firstNamed:                                 "T_DEFAULT",
	T_MATCH - firstNamed:                                   "T_MATCH",
	T_BREAK - firstNamed:                                   "T_BREAK",
	T_CONTINUE - firstNamed:                                "T_CONTINUE",
	T_GOTO - firstNamed:                                    "T_GOTO",
	T_FUNCTION - firstNamed:                                "T_FUNCTION",
	T_FN - firstNamed:                                      "T_FN",
	T_CONST - firstNamed:                                   "T_CONST",
	T_RETURN - firstNamed:                                  "T_RETURN",
	T_TRY - firstNamed:                                     "T_TRY",
	T_CATCH - firstNamed:                                   "T_CATCH",
	T_FINALLY - firstNamed:                                 "T_FINALLY",
	T_THROW - firstNamed:                                   "T_THROW",
	T_USE - firstNamed:                                     "T_USE",
	T_INSTEADOF - firstNamed:                               "T_INSTEADOF",
	T_GLOBAL - firstNamed:                                  "T_GLOBAL",
	T_STATIC - firstNamed:                                  "T_STATIC",
	T_ABSTRACT - firstNamed:                                "T_ABSTRACT",
	T_FINAL - firstNamed:                                   "T_FINAL",
	T_PRIVATE - firstNamed:                                 "T_PRIVATE",
	T_PROTECTED - firstNamed:                               "T_PROTECTED",
	T_PUBLIC - firstNamed:                                  "T_PUBLIC",
	T_PRIVATE_SET - firstNamed:                             "T_PRIVATE_SET",
	T_PROTECTED_SET - firstNamed:                           "T_PROTECTED_SET",
	T_PUBLIC_SET - firstNamed:                              "T_PUBLIC_SET",
	T_READONLY - firstNamed:                                "T_READONLY",
	T_VAR - firstNamed:                                     "T_VAR",
	T_UNSET - firstNamed:                                   "T_UNSET",
	T_ISSET - firstNamed:                                   "T_ISSET",
	T_EMPTY - firstNamed:                                   "T_EMPTY",
	T_HALT_COMPILER - firstNamed:                           "T_HALT_COMPILER",
	T_CLASS - firstNamed:                                   "T_CLASS",
	T_TRAIT - firstNamed:                                   "T_TRAIT",
	T_INTERFACE - firstNamed:                               "T_INTERFACE",
	T_ENUM - firstNamed:                                    "T_ENUM",
	T_EXTENDS - firstNamed:                                 "T_EXTENDS",
	T_IMPLEMENTS - firstNamed:                              "T_IMPLEMENTS",
	T_NAMESPACE - firstNamed:                               "T_NAMESPACE",
	T_LIST - firstNamed:                                    "T_LIST",
	T_ARRAY - firstNamed:                                   "T_ARRAY",
	T_CALLABLE - firstNamed:                                "T_CALLABLE",
	T_LINE - firstNamed:                                    "T_LINE",
	T_FILE - firstNamed:                                    "T_FILE",
	T_DIR - firstNamed:                                     "T_DIR",
	T_CLASS_C - firstNamed:                                 "T_CLASS_C",
	T_TRAIT_C - firstNamed:                                 "T_TRAIT_C",
	T_METHOD_C - firstNamed:                                "T_METHOD_C",
	T_FUNC_C - firstNamed:                                  "T_FUNC_C",
	T_PROPERTY_C - firstNamed:                              "T_PROPERTY_C",
	T_NS_C - firstNamed:                                    "T_NS_C",
	T_ATTRIBUTE - firstNamed:                               "T_ATTRIBUTE",
	T_PLUS_EQUAL - firstNamed:                              "T_PLUS_EQUAL",
	T_MINUS_EQUAL - firstNamed:                             "T_MINUS_EQUAL",
	T_MUL_EQUAL - firstNamed:                               "T_MUL_EQUAL",
	T_DIV_EQUAL - firstNamed:                               "T_DIV_EQUAL",
	T_CONCAT_EQUAL - firstNamed:                            "T_CONCAT_EQUAL",
	T_MOD_EQUAL - firstNamed:                               "T_MOD_EQUAL",
	T_AND_EQUAL - firstNamed:                               "T_AND_EQUAL",
	T_OR_EQUAL - firstNamed:                                "T_OR_EQUAL",
	T_XOR_EQUAL - firstNamed:                               "T_XOR_EQUAL",
	T_SL_EQUAL - firstNamed:                                "T_SL_EQUAL",
	T_SR_EQUAL - firstNamed:                                "T_SR_EQUAL",
	T_COALESCE_EQUAL - firstNamed:                          "T_COALESCE_EQUAL",
	T_BOOLEAN_OR - firstNamed:                              "T_BOOLEAN_OR",
	T_BOOLEAN_AND - firstNamed:                             "T_BOOLEAN_AND",
	T_IS_EQUAL - firstNamed:                                "T_IS_EQUAL",
	T_IS_NOT_EQUAL - firstNamed:                            "T_IS_NOT_EQUAL",
	T_IS_IDENTICAL - firstNamed:                            "T_IS_IDENTICAL",
	T_IS_NOT_IDENTICAL - firstNamed:                        "T_IS_NOT_IDENTICAL",
	T_IS_SMALLER_OR_EQUAL - firstNamed:                     "T_IS_SMALLER_OR_EQUAL",
	T_IS_GREATER_OR_EQUAL - firstNamed:                     "T_IS_GREATER_OR_EQUAL",
	T_SPACESHIP - firstNamed:                               "T_SPACESHIP",
	T_SL - firstNamed:                                      "T_SL",
	T_SR - firstNamed:                                      "T_SR",
	T_INC - firstNamed:                                     "T_INC",
	T_DEC - firstNamed:                                     "T_DEC",
	T_INT_CAST - firstNamed:                                "T_INT_CAST",
	T_DOUBLE_CAST - firstNamed:                             "T_DOUBLE_CAST",
	T_STRING_CAST - firstNamed:                             "T_STRING_CAST",
	T_ARRAY_CAST - firstNamed:                              "T_ARRAY_CAST",
	T_OBJECT_CAST - firstNamed:                             "T_OBJECT_CAST",
	T_BOOL_CAST - firstNamed:                               "T_BOOL_CAST",
	T_UNSET_CAST - firstNamed:                              "T_UNSET_CAST",
	T_VOID_CAST - firstNamed:                               "T_VOID_CAST",
	T_OBJECT_OPERATOR - firstNamed:                         "T_OBJECT_OPERATOR",
	T_NULLSAFE_OBJECT_OPERATOR - firstNamed:                "T_NULLSAFE_OBJECT_OPERATOR",
	T_DOUBLE_ARROW - firstNamed:                            "T_DOUBLE_ARROW",
	T_COMMENT - firstNamed:                                 "T_COMMENT",
	T_DOC_COMMENT - firstNamed:                             "T_DOC_COMMENT",
	T_OPEN_TAG - firstNamed:                                "T_OPEN_TAG",
	T_OPEN_TAG_WITH_ECHO - firstNamed:                      "T_OPEN_TAG_WITH_ECHO",
	T_CLOSE_TAG - firstNamed:                               "T_CLOSE_TAG",
	T_WHITESPACE - firstNamed:                              "T_WHITESPACE",
	T_START_HEREDOC - firstNamed:                           "T_START_HEREDOC",
	T_END_HEREDOC - firstNamed:                             "T_END_HEREDOC",
	T_DOLLAR_OPEN_CURLY_BRACES - firstNamed:                "T_DOLLAR_OPEN_CURLY_BRACES",
	T_CURLY_OPEN - firstNamed:                              "T_CURLY_OPEN",
	T_PAAMAYIM_NEKUDOTAYIM - firstNamed:                    "T_DOUBLE_COLON",
	T_NS_SEPARATOR - firstNamed:                            "T_NS_SEPARATOR",
	T_ELLIPSIS - firstNamed:                                "T_ELLIPSIS",
	T_COALESCE - firstNamed:                                "T_COALESCE",
	T_POW - firstNamed:                                     "T_POW",
	T_POW_EQUAL - firstNamed:                               "T_POW_EQUAL",
	T_PIPE - firstNamed:                                    "T_PIPE",
	T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG - firstNamed:     "T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG",
	T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG - firstNamed: "T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG",
	T_BAD_CHARACTER - firstNamed:                           "T_BAD_CHARACTER",
	T_INLINE_HTML - firstNamed:                             "T_INLINE_HTML",
	T_IS - firstNamed:                                      "T_IS",
	T_FUN - firstNamed:                                     "T_FUN",
	T_TYPE - firstNamed:                                    "T_TYPE",
	T_OF - firstNamed:                                      "T_OF",
	T_IN - firstNamed:                                      "T_IN",
	T_OUT - firstNamed:                                     "T_OUT",
	T_BYTES_CAST - firstNamed:                              "T_BYTES_CAST",
}

// String returns the token's name as PHP's token_name() prints it; single-character
// tokens print as themselves.
func (k Kind) String() string {
	switch {
	case k == EOF:
		return "EOF"
	case k < 256:
		return string(rune(k))
	case k > firstNamed && k < lastNamed:
		return names[k-firstNamed]
	}
	return fmt.Sprintf("Kind(%d)", uint16(k))
}

// Pos is a byte offset into the source plus its 1-based line.
type Pos struct {
	Offset int
	Line   int
}

// Token is a lexeme: its kind and its byte range in the source. The lexer is
// lossless — concatenating the text of every token reproduces the source.
type Token struct {
	Kind  Kind
	Start int // byte offset, inclusive
	End   int // byte offset, exclusive
	Line  int // 1-based line of Start
}

// Text returns the token's source text.
func (t Token) Text(src []byte) string { return string(src[t.Start:t.End]) }

// IsTrivia reports whether the token carries no syntax (whitespace and comments).
func (k Kind) IsTrivia() bool {
	return k == T_WHITESPACE || k == T_COMMENT || k == T_DOC_COMMENT
}

// Keywords maps a lower-cased PHP keyword to its token. Case-insensitive like PHP.
var Keywords = map[string]Kind{
	"include": T_INCLUDE, "include_once": T_INCLUDE_ONCE, "eval": T_EVAL,
	"require": T_REQUIRE, "require_once": T_REQUIRE_ONCE,
	"or": T_LOGICAL_OR, "xor": T_LOGICAL_XOR, "and": T_LOGICAL_AND,
	"print": T_PRINT, "yield": T_YIELD, "instanceof": T_INSTANCEOF,
	"new": T_NEW, "clone": T_CLONE, "exit": T_EXIT, "die": T_EXIT,
	"if": T_IF, "elseif": T_ELSEIF, "else": T_ELSE, "endif": T_ENDIF,
	"echo": T_ECHO, "do": T_DO, "while": T_WHILE, "endwhile": T_ENDWHILE,
	"for": T_FOR, "endfor": T_ENDFOR, "foreach": T_FOREACH, "endforeach": T_ENDFOREACH,
	"declare": T_DECLARE, "enddeclare": T_ENDDECLARE, "as": T_AS,
	"switch": T_SWITCH, "endswitch": T_ENDSWITCH, "case": T_CASE, "default": T_DEFAULT,
	"match": T_MATCH, "break": T_BREAK, "continue": T_CONTINUE, "goto": T_GOTO,
	"function": T_FUNCTION, "fn": T_FN, "const": T_CONST, "return": T_RETURN,
	"try": T_TRY, "catch": T_CATCH, "finally": T_FINALLY, "throw": T_THROW,
	"use": T_USE, "insteadof": T_INSTEADOF, "global": T_GLOBAL, "static": T_STATIC,
	"abstract": T_ABSTRACT, "final": T_FINAL,
	"private": T_PRIVATE, "protected": T_PROTECTED, "public": T_PUBLIC,
	"readonly": T_READONLY, "var": T_VAR, "unset": T_UNSET, "isset": T_ISSET, "empty": T_EMPTY,
	"__halt_compiler": T_HALT_COMPILER,
	"class":           T_CLASS, "trait": T_TRAIT, "interface": T_INTERFACE,
	"extends": T_EXTENDS, "implements": T_IMPLEMENTS, "namespace": T_NAMESPACE,
	"list": T_LIST, "array": T_ARRAY, "callable": T_CALLABLE,
	"__line__": T_LINE, "__file__": T_FILE, "__dir__": T_DIR, "__class__": T_CLASS_C,
	"__trait__": T_TRAIT_C, "__method__": T_METHOD_C, "__function__": T_FUNC_C,
	"__property__": T_PROPERTY_C, "__namespace__": T_NS_C,
}
