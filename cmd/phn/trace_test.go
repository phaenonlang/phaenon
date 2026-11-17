package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phaenonlang/phaenon/internal/trace"
)

// TestTraceStream: stdin passes through line by line, byte for byte where
// there is nothing to rewrite, the last line's missing line break and CRLF
// included.
func TestTraceStream(t *testing.T) {
	tr := trace.New(nil)
	tr.ReadFile = func(path string) ([]byte, error) {
		return []byte("<?php\n\necho 1;\n// phaenon-map v1 Foo.phn 1-2=1 3:1\n"), nil
	}
	in := "first\r\nat Foo.php:3\r\n\tno position: Foo.php\nlast without a break"
	want := "first\r\nat Foo.phn:1\r\n\tno position: Foo.php\nlast without a break"
	var out bytes.Buffer
	if err := traceStream(tr, strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != want {
		t.Errorf("got %q\nwant %q", out.String(), want)
	}
}

// TestTraceStreamReport: stdin that opens as an XML report is read whole and
// rewritten as one; a PHP error written as HTML stays text, rewritten a line
// at a time.
func TestTraceStreamReport(t *testing.T) {
	tr := trace.New(nil)
	tr.ReadFile = func(path string) ([]byte, error) {
		return []byte("<?php\n\necho 1;\n// phaenon-map v1 Foo.phn 1-2=1 3:1\n"), nil
	}
	cases := []struct{ in, want string }{
		{"<?xml version=\"1.0\"?>\n<checkstyle>\n<file name=\"Foo.php\">\n  <error line=\"3\" column=\"1\" />\n</file>\n</checkstyle>\n",
			"<?xml version=\"1.0\"?>\n<checkstyle>\n<file name=\"Foo.phn\">\n  <error line=\"1\" />\n</file>\n</checkstyle>\n"},
		{"<testsuite name=\"phpstan\"><testcase name=\"Foo.php:3\"/></testsuite>",
			"<testsuite name=\"phpstan\"><testcase name=\"Foo.phn:1\"/></testsuite>"},
		{"<br />\n<b>Fatal error</b>:  Uncaught Exception in Foo.php:3\n",
			"<br />\n<b>Fatal error</b>:  Uncaught Exception in Foo.phn:1\n"},
		{"<?xml version=\"1.0\"?>\n<log>at Foo.php:3</log>\n", "<?xml version=\"1.0\"?>\n<log>at Foo.phn:1</log>\n"},
	}
	for _, c := range cases {
		var out bytes.Buffer
		if err := traceStream(tr, strings.NewReader(c.in), &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != c.want {
			t.Errorf("got %q\nwant %q", out.String(), c.want)
		}
	}
}

// TestTraceRun: `phn trace -- command` runs the command, rewrites both of
// its streams and exits with its status; the pipe form is unchanged, and a
// command that cannot be started is 127.
func TestTraceRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the command is a shell script")
	}
	bin := phnBinary(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"Foo.php": "<?php\n\necho 1;\n// phaenon-map v1 Foo.phn 1-2=1 3:1\n",
		"run.sh":  "echo 'at Foo.php:3'\necho 'error in Foo.php:3' >&2\nexit 3\n",
	})
	stdout, stderr, err := run(t, root, bin, "trace", "--", "sh", "run.sh")
	var exit *exec.ExitError
	if !errorsAs(err, &exit) || exit.ExitCode() != 3 {
		t.Errorf("status: %v, want 3", err)
	}
	if stdout != "at Foo.phn:1\n" || stderr != "error in Foo.phn:1\n" {
		t.Errorf("streams:\nstdout %q\nstderr %q", stdout, stderr)
	}
	if _, _, err := run(t, root, bin, "trace", "--", "sh", "-c", "exit 0"); err != nil {
		t.Errorf("a command that succeeds: %v", err)
	}
	if _, stderr, err := run(t, root, bin, "trace", "--", "./no-such-command"); !errorsAs(err, &exit) || exit.ExitCode() != 127 {
		t.Errorf("a command that is not found: %v\n%s", err, stderr)
	}
	// Found and not executable is 126, as a shell has it.
	if _, stderr, err := run(t, root, bin, "trace", "--", "./run.sh"); !errorsAs(err, &exit) || exit.ExitCode() != 126 {
		t.Errorf("a command that cannot be run: %v\n%s", err, stderr)
	}
	if stdout, _, err := run(t, root, bin, "trace", "at", "Foo.php:3"); err != nil || stdout != "at Foo.phn:1\n" {
		t.Errorf("arguments: %v %q", err, stdout)
	}
}

// TestTraceRunSignals: with no terminal, as in CI, a termination sent to
// phn ends the command, and phn exits with 128 and the signal's number; an
// interrupt sent to phn alone is not passed on, and both run on.
func TestTraceRunSignals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows process cannot be sent these signals")
	}
	// The command's first line is its process ID, and says that phn has
	// started it, its signals set up before.
	cmd := exec.Command(phnBinary(t), "trace", "--", "sh", "-c", "echo $$; exec sleep 30")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(out)
	line, err := r.ReadString('\n')
	pid, convErr := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || convErr != nil {
		cmd.Process.Kill()
		t.Fatalf("the command's process ID: %q %v %v", line, err, convErr)
	}
	command, _ := os.FindProcess(pid)
	alive := func() bool { return command.Signal(syscall.Signal(0)) == nil }
	done := make(chan error, 1)
	go func() {
		io.Copy(io.Discard, r)
		done <- cmd.Wait()
	}()

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("phn ended on an interrupt sent to it alone: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	if !alive() {
		t.Fatal("the command ended on an interrupt sent to phn alone")
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var exit *exec.ExitError
		if !errorsAs(err, &exit) || exit.ExitCode() != 128+int(syscall.SIGTERM) {
			t.Errorf("status: %v, want %d, the command ended by the termination", err, 128+int(syscall.SIGTERM))
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		command.Kill()
		t.Fatal("phn did not end after a termination")
	}
	if alive() {
		command.Kill()
		t.Error("the command outlived phn")
	}
}

func errorsAs(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
