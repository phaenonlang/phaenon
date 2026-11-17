package emit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/lexer"
	"github.com/phaenonlang/phaenon/internal/program"
)

// A docblock whose lines end with \r\n gains its tags on lines that end so
// too, wherever its first and last lines stand (testdata/
// docblock-closing-line.phn holds the forms with \n), and the output is
// PHP. The source is built here: a golden file with \r\n in it would not
// survive every checkout.
func TestDocblockCRLF(t *testing.T) {
	src := strings.ReplaceAll(`namespace App\DocblockCrlf;

/** @param list<T> $xs
 *  @return list<T> */
function append<T>(array $xs, T $x): array { return [...$xs, $x]; }

/**
 * Gives back what it is given,
 * whatever that is. */
function same<T>(T $x): mixed { return $x; }

/**
 * The first of them.
 * @return T|null
 */
function first<T>(list<T> $xs): mixed { return $xs[0] ?? null; }

/** @return T|null */
function last<T>(list<T> $xs): mixed { return $xs === [] ? null : $xs[count($xs) - 1]; }
`, "\n", "\r\n")
	p := program.New()
	if err := p.LoadStubs(); err != nil {
		t.Fatal(err)
	}
	f, err := p.AddFile("crlf.phn", []byte(src), lexer.ModePhaenon, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range checker.New(p, checker.Options{}).Check() {
		if d.Severity == checker.Error {
			t.Errorf("checker: %d: %s: %s", d.Line, d.Code, d.Message)
		}
	}
	got, err := File([]byte(src), Options{SourceName: "crlf.phn", StrictTypes: true, Program: p, File: f})
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	for _, want := range []string{
		"/**\r\n * @template T\r\n * @param T $x\r\n * @param list<T> $xs\r\n *  @return list<T> */\r\n",
		"/**\r\n * Gives back what it is given,\r\n * whatever that is.\r\n * @template T\r\n * @param T $x\r\n */\r\n",
		"/**\r\n * The first of them.\r\n * @template T\r\n * @param list<T> $xs\r\n * @return T|null\r\n */\r\n",
		" * @return T|null\n * @template T\n * @param list<T> $xs\n */", // a one-line docblock is written anew, as the compiler's own lines are
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%q", want, out)
		}
	}
	if php, err := exec.LookPath("php"); err == nil {
		path := filepath.Join(t.TempDir(), "crlf.php")
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		if msg, err := exec.Command(php, "-l", path).CombinedOutput(); err != nil {
			t.Errorf("php -l: %s", msg)
		}
	}
}
