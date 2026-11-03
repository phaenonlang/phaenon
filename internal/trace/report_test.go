package trace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reportsDir holds reports PHPUnit 13 (under PCOV) and PHPStan 2 wrote on
// testdata/reports/project, a project phn compiled: a folded pipeline laid
// out over six lines, expression bodies, an empty constructor body, a test
// compiled from .phn whose failures name compiled frames, and Plain.php,
// hand-written PHP copied without a map. The absolute path of the project
// is @DIR@ in the XML reports; PHPStan's GitHub output names the files
// relative to the project, where it ran.
const reportsDir = "testdata/reports"

func projectDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join(reportsDir, "project"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func readReport(t *testing.T, name, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(reportsDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "@DIR@", dir)
}

// golden compares got with the file want, PHAENON_UPDATE=1 rewriting it.
func golden(t *testing.T, want, got string) {
	t.Helper()
	path := filepath.Join(reportsDir, want)
	if os.Getenv("PHAENON_UPDATE") != "" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != got {
		t.Errorf("output differs from %s (PHAENON_UPDATE=1 to rewrite):\n%s", path, got)
	}
}

// TestReports rewrites each report onto the sources; the expected files are
// read as diffs against their inputs.
func TestReports(t *testing.T) {
	dir := projectDir(t)
	for _, c := range []struct{ file, format string }{
		{"clover.xml", "clover"},
		{"cobertura.xml", "cobertura"},
		{"junit.xml", "junit"},
		{"phpstan-checkstyle.xml", "checkstyle"},
		{"phpstan-junit.xml", "junit"},
	} {
		out, format, ok := New(nil).Report([]byte(readReport(t, c.file, dir)))
		if !ok || format != c.format {
			t.Errorf("%s: read as %q (%v), want %s", c.file, format, ok, c.format)
			continue
		}
		golden(t, strings.TrimSuffix(c.file, ".xml")+".want.xml", strings.ReplaceAll(string(out), dir, "@DIR@"))
	}
}

// PHPStan's workflow commands name the files relative to the working
// directory, here the project kept in app/ of its repository; traced, they
// name the sources by their paths in the repository, which GitHub matches.
func TestReportGitHub(t *testing.T) {
	compiled, err := os.ReadFile(filepath.Join(projectDir(t), ".src", "Bad.php"))
	if err != nil {
		t.Fatal(err)
	}
	in := readReport(t, "phpstan-github.txt", "")
	repo := t.TempDir()
	mustWrite(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	mustWrite(t, filepath.Join(repo, "app", ".src", "Bad.php"), string(compiled))
	t.Chdir(filepath.Join(repo, "app"))
	want := "::error file=app/src/Bad.phn,line=3::Function App\\bad() should return int but returns string.\n" +
		"::error file=app/src/Bad.phn,line=5::Function App\\alsoBad() should return string but returns int.\n"
	if got := New(nil).Rewrite(in); got != want {
		t.Errorf("got\n%swant\n%s", got, want)
	}
}

// TestReportCoverageMerge pins what the goldens show for the folded
// pipeline: Box::label()'s six compiled lines are one source line, and the
// totals beside the lines lose five statements, all of them covered.
func TestReportCoverageMerge(t *testing.T) {
	dir := projectDir(t)
	out, _, _ := New(nil).Report([]byte(readReport(t, "clover.xml", dir)))
	clover := string(out)
	if n := strings.Count(clover, `<line num="14" type="stmt"`); n != 1 {
		t.Errorf("Clover has %d statements on Box.phn:14, want 1", n)
	}
	if !strings.Contains(clover, `name="`+filepath.Join(dir, "src", "Box.phn")+`"`) {
		t.Error("Clover does not name Box.phn")
	}
	if !strings.Contains(clover, `<file name="`+filepath.Join(dir, ".src", "Plain.php")+`">`) {
		t.Error("Clover should leave Plain.php, which has no map, as it is")
	}

	out, _, _ = New(nil).Report([]byte(readReport(t, "cobertura.xml", dir)))
	cobertura := string(out)
	for _, want := range []string{
		"<source>" + dir + "</source>",
		`filename="src/Box.phn"`,
		`filename=".src/Plain.php"`, // rebased onto the new <source>, not mapped
		`<class name="src/Bad.phn" filename="src/Bad.phn"`,
	} {
		if !strings.Contains(cobertura, want) {
			t.Errorf("Cobertura lacks %s", want)
		}
	}
}

// A Cobertura report without <sources> names its files absolutely.
func TestReportCoberturaWithoutSources(t *testing.T) {
	dir := projectDir(t)
	in := `<coverage line-rate="1" lines-covered="1" lines-valid="1"><packages><package name="p" line-rate="1"><classes>` +
		`<class name="App\Box" filename="` + dir + `/.src/Box.php" line-rate="1"><lines><line number="20" hits="1"/></lines></class>` +
		`</classes></package></packages></coverage>`
	out, format, ok := New(nil).Report([]byte(in))
	want := `filename="` + dir + `/src/Box.phn" line-rate="1"><lines><line number="10" hits="1"/>`
	if !ok || format != "cobertura" || !strings.Contains(string(out), want) {
		t.Errorf("got %s", out)
	}
}

// A report that names no mapped file comes back byte for byte: the parse
// keeps the bytes of everything it does not rewrite.
func TestReportUntouched(t *testing.T) {
	for _, name := range []string{"clover.xml", "cobertura.xml", "junit.xml", "phpstan-checkstyle.xml", "phpstan-junit.xml"} {
		in := readReport(t, name, "/nowhere")
		out, _, ok := New(nil).Report([]byte(in))
		if !ok || string(out) != in {
			t.Errorf("%s changed though it names no mapped file:\n%s", name, out)
		}
	}
}

// Only the four reports are read as reports; other XML, and XML that is not
// well formed, is left to the text rewrite.
func TestReportNotAReport(t *testing.T) {
	for _, in := range []string{
		"<html><body>Foo.php:3</body></html>",
		`<?xml version="1.0"?><checkstyle><file name="a.php">`,
		"<br />\n<b>Fatal error</b>: in /app/Foo.php on line 3",
		"not XML at all",
	} {
		if _, _, ok := New(nil).Report([]byte(in)); ok {
			t.Errorf("read as a report: %q", in)
		}
	}
}

// A rewritten attribute is escaped the way libxml2 writes one, in a tag
// otherwise as it was.
func TestReportEscapes(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, ".src", "A&B.php"), "<?php\necho 1;\n// phaenon-map v1 ../src/A&B.phn 1-1=1 2:1\n")
	in := `<checkstyle><file name="` + dir + `/.src/A&amp;B.php"><error line="2" column="1" message="a &quot;q&quot;"/></file></checkstyle>`
	out, _, ok := New(nil).Report([]byte(in))
	want := `<checkstyle><file name="` + dir + `/src/A&amp;B.phn"><error line="1" message="a &quot;q&quot;"/></file></checkstyle>`
	if !ok || string(out) != want {
		t.Errorf("got  %s\nwant %s", out, want)
	}
}

func TestPHPFloat(t *testing.T) {
	for f, want := range map[float64]string{0: "0", 1: "1", 0.75: "0.75", 4.0 / 7: "0.57142857142857", 0.00001: "1.0E-5", 0.0000125: "1.25E-5"} {
		if got := phpFloat(f); got != want {
			t.Errorf("phpFloat(%v) = %s, want %s", f, got, want)
		}
	}
}
