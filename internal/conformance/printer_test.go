package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/conformance/phpt"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/parser"
	"github.com/phaenonlang/phaenon/internal/printer"
	"github.com/phaenonlang/phaenon/internal/token"
)

// normTok is a token reduced to what the tree preserves: its kind, and its
// text only for kinds whose spelling is semantic.
type normTok struct {
	Kind token.Kind
	Text string
}

func (n normTok) String() string {
	if n.Text == "" {
		return n.Kind.String()
	}
	return fmt.Sprintf("%s(%q)", n.Kind, n.Text)
}

func textMatters(k token.Kind) bool {
	switch k {
	case token.T_VARIABLE, token.T_STRING, token.T_NAME_QUALIFIED, token.T_NAME_FULLY_QUALIFIED,
		token.T_NAME_RELATIVE, token.T_LNUMBER, token.T_DNUMBER, token.T_CONSTANT_ENCAPSED_STRING,
		token.T_ENCAPSED_AND_WHITESPACE, token.T_INLINE_HTML, token.T_STRING_VARNAME, token.T_NUM_STRING,
		token.T_START_HEREDOC, token.T_END_HEREDOC:
		return true
	}
	return false
}

func isModifierTok(k token.Kind) bool {
	switch k {
	case token.T_PUBLIC, token.T_PROTECTED, token.T_PRIVATE, token.T_PUBLIC_SET, token.T_PROTECTED_SET,
		token.T_PRIVATE_SET, token.T_STATIC, token.T_ABSTRACT, token.T_FINAL, token.T_READONLY, token.T_VAR:
		return true
	}
	return false
}

// normalise lexes src and reduces the token stream to the parts a parse →
// print round trip must preserve. Dropped on purpose, because PHP treats the
// spellings as equivalent and the tree keeps only one of them:
//
//   - trivia (whitespace, comments);
//   - a `;` directly before `?>` (the close tag is a statement terminator);
//   - a trailing `,` before `)`, `]`, `}`;
//   - the order of modifiers in one declaration (sorted on both sides);
//   - the two flavours of `&`.
func normalise(src []byte) []normTok {
	toks := lexer.Tokenize(src, lexer.ModePHP)
	var sig []token.Token
	for _, t := range toks {
		if t.Kind == token.EOF || t.Kind.IsTrivia() {
			continue
		}
		sig = append(sig, t)
	}
	isCloser := func(k token.Kind) bool {
		return k == token.Kind(')') || k == token.Kind(']') || k == token.Kind('}') || k == token.T_DOUBLE_ARROW
	}
	var out []normTok
	for i, t := range sig {
		next := token.EOF
		if i+1 < len(sig) {
			next = sig[i+1].Kind
		}
		if t.Kind == token.Kind(';') && next == token.T_CLOSE_TAG {
			continue
		}
		if t.Kind == token.Kind(',') {
			// Drop a run of commas that only leads to a closer (`[1, 2, ,]`,
			// `list(,,,)`, `match` arm `a, => b`): PHP rejects or ignores it.
			j := i + 1
			for j < len(sig) && sig[j].Kind == token.Kind(',') {
				j++
			}
			if j >= len(sig) || isCloser(sig[j].Kind) {
				continue
			}
		}
		k := t.Kind
		if k == token.T_AMPERSAND_FOLLOWED_BY_VAR_OR_VARARG {
			k = token.T_AMPERSAND_NOT_FOLLOWED_BY_VAR_OR_VARARG
		}
		n := normTok{Kind: k}
		if textMatters(k) {
			n.Text = t.Text(src)
		}
		out = append(out, n)
	}
	// Sort and deduplicate runs of modifiers (the tree keeps a set).
	var dedup []normTok
	for i := 0; i < len(out); {
		if !isModifierTok(out[i].Kind) {
			dedup = append(dedup, out[i])
			i++
			continue
		}
		j := i
		for j < len(out) && isModifierTok(out[j].Kind) {
			j++
		}
		run := append([]normTok(nil), out[i:j]...)
		sort.Slice(run, func(a, b int) bool { return run[a].Kind < run[b].Kind })
		for k, m := range run {
			if k == 0 || m != run[k-1] {
				dedup = append(dedup, m)
			}
		}
		i = j
	}
	return dedup
}

// knownDeviations are corpus files whose printed form is intentionally not
// token-identical: the tree keeps one canonical spelling of an alternative
// PHP allows.
var knownDeviations = map[string]string{
	"tests/lang/033.phpt": "`case X;` (semicolon instead of colon) prints as `case X:`",
}

func firstDiff(a, b []normTok) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			lo := i - 4
			if lo < 0 {
				lo = 0
			}
			ctx := func(s []normTok) string {
				hi := i + 4
				if hi > len(s) {
					hi = len(s)
				}
				var parts []string
				for _, t := range s[lo:hi] {
					parts = append(parts, t.String())
				}
				return strings.Join(parts, " ")
			}
			return fmt.Sprintf("token %d differs:\n  orig: %s\n  prnt: %s", i, ctx(a), ctx(b))
		}
	}
	if len(a) != len(b) {
		return fmt.Sprintf("length differs: orig %d tokens, printed %d", len(a), len(b))
	}
	return ""
}

// roundTrip parses src, prints it and reports the first token-level
// difference between the original and the printed text ("" when equal).
func roundTrip(src []byte) (printed string, diff string, err error) {
	f, err := parser.Parse(src, lexer.ModePHP)
	if err != nil {
		return "", "", err
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("printer panic: %v", r)
		}
	}()
	printed = printer.File(f)
	return printed, firstDiff(normalise(src), normalise([]byte(printed))), nil
}

// TestPrinterRoundTrip checks parse → print → lex over the php-src corpus:
// the printed text must carry the same significant tokens as the original.
// Files our parser rejects are skipped (the parser suite covers those).
func TestPrinterRoundTrip(t *testing.T) {
	src := phpSrc(t)
	var total, ok int
	shown := 0
	limit := 25
	if v := os.Getenv("PHAENON_SHOW_DIFFS"); v != "" {
		fmt.Sscan(v, &limit)
	}
	for _, p := range collectPhpt(t, src) {
		tt, err := phpt.Parse(p)
		if err != nil {
			continue
		}
		_, diff, err := roundTrip(tt.File)
		if err != nil {
			if strings.HasPrefix(err.Error(), "printer panic") {
				t.Errorf("%s: %v", p, err)
			}
			continue
		}
		total++
		if diff == "" {
			ok++
			continue
		}
		if rel, err := filepath.Rel(src, p); err == nil {
			if _, known := knownDeviations[rel]; known {
				ok++
				continue
			}
		}
		if shown < limit {
			t.Errorf("%s: %s", p, diff)
			shown++
		}
	}
	t.Logf("printer round trip: %d/%d files token-identical (%.2f%%)", ok, total, 100*float64(ok)/float64(total))
}
