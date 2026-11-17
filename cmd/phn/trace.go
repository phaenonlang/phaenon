package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/phaenonlang/phaenon/internal/trace"
)

// traceCmd is `phn trace`: it rewrites the positions in compiled files that
// its arguments, or stdin, name into the .phn positions their line maps
// give (design/line-map.md §4.1), so that `vendor/bin/phpunit | phn trace`
// and a pasted production trace point into the sources. Stdin is rewritten
// a line at a time, as it arrives, unless it is an XML report
// (design/tooling.md §7.1). `phn trace -- vendor/bin/phpunit` runs the
// command instead, its standard output and error rewritten in the same
// way, and exits with its status, which a pipe into phn trace loses
// without `set -o pipefail`.
func traceCmd(args []string) {
	fs := flag.NewFlagSet("trace", flag.ExitOnError)
	var roots []trace.Root
	fs.Func("root", "read paths under PREFIX from LOCAL (PREFIX=LOCAL); may be repeated", func(s string) error {
		r, ok := trace.ParseRoot(s)
		if !ok {
			return fmt.Errorf("want PREFIX=LOCAL, as in /app=.")
		}
		roots = append(roots, r)
		return nil
	})
	fs.Parse(args)
	t := trace.New(roots)
	if rest := fs.Args(); len(args) > len(rest) && args[len(args)-len(rest)-1] == "--" {
		if len(rest) == 0 {
			fail("phn trace --: name the command to run, as in `phn trace -- vendor/bin/phpunit`")
		}
		os.Exit(traceRun(t, rest))
	}
	if fs.NArg() > 0 {
		fmt.Println(t.Rewrite(strings.Join(fs.Args(), " ")))
		return
	}
	if err := traceStream(t, os.Stdin, os.Stdout); err != nil {
		fail("phn trace: %v", err)
	}
}

// Bounds of phn trace -- command: how long a termination sent to phn waits
// for the command to end by itself before phn passes it on, and how long,
// once the command has ended, phn reads what it left in the pipes.
const (
	termGrace = time.Second
	drainTime = 200 * time.Millisecond
)

// traceRun runs a command, its standard output and error rewritten as
// traceStream rewrites stdin, the one to phn's standard output and the
// other to its standard error, each a line at a time as it arrives, and
// returns the command's exit status as a shell gives it: 128 and the
// signal's number where a signal ended it, 127 where it was not found, 126
// where it was found and could not be run. The two streams come through
// two pipes, so that a line of one and a line of the other written close
// together may come out in the other order.
//
// Once the command has ended, phn reads what is already in the pipes, for
// drainTime at most, and stops: a process the command left running in the
// background (a server, a sleep) may hold them open, and phn does not wait
// for it. On Windows a pipe takes no deadline, and phn waits for it.
//
// An interrupt, a quit or a hangup reaches the command once, never twice.
// Phn runs it in its own process group, so that what is sent to the group
// reaches both: an interrupt or a quit from a terminal (Ctrl-C, Ctrl-\), a
// terminal's hangup, a termination sent to the group. Phn catches the four
// and passes none on as they come, which keeps it alive to rewrite the
// command's last output and exit with its status. A termination sent to
// phn alone, as a kill or a CI runner's cancellation may send it, has not
// reached the command: where the command is still running termGrace after
// one, phn passes it on. Phn cannot tell that termination from one sent to
// the group, so a command that takes longer than termGrace to end after a
// termination sent to the group is sent it a second time. An interrupt, a
// quit or a hangup sent to phn alone does nothing; the command is not in
// its hands. A signal ignored when phn started (nohup, a script's
// background job) is left ignored, for the command to inherit as it would
// without phn: phn catches the others rather than ignoring them, since an
// ignored signal stays ignored across exec. On Windows the console sends
// Ctrl-C and its close to every process attached to it, the command
// included, and a process cannot be sent either: phn catches them and
// passes nothing on.
func traceRun(t *trace.Tracer, argv []string) int {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	// Pipes of phn's own rather than StdoutPipe: they take a read deadline,
	// and exec.Cmd.Wait does not wait for them.
	outR, outW, err := os.Pipe()
	if err != nil {
		fail("phn trace: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		fail("phn trace: %v", err)
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	signals := make(chan os.Signal, 4)
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP} {
		if !signal.Ignored(sig) {
			signal.Notify(signals, sig)
		}
	}
	defer signal.Stop(signals)
	err = cmd.Start()
	outW.Close()
	errW.Close()
	if err != nil {
		outR.Close()
		errR.Close()
		fmt.Fprintf(os.Stderr, "phn trace: %v\n", err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return 127
		}
		return 126
	}
	var ended atomic.Bool
	go func() {
		for sig := range signals {
			if sig == syscall.SIGTERM {
				time.AfterFunc(termGrace, func() {
					if !ended.Load() {
						cmd.Process.Signal(syscall.SIGTERM) // on Windows an error, and nothing to do
					}
				})
			}
		}
	}()
	var wg sync.WaitGroup
	for _, s := range []struct {
		in  *os.File
		out io.Writer
	}{{outR, os.Stdout}, {errR, os.Stderr}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := traceStream(t, s.in, s.out); err != nil {
				io.Copy(s.out, s.in) // what is left, as it is
			}
		}()
	}
	err = cmd.Wait()
	ended.Store(true)
	deadline := time.Now().Add(drainTime)
	outR.SetReadDeadline(deadline)
	errR.SetReadDeadline(deadline)
	wg.Wait()
	outR.Close()
	errR.Close()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return exit.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "phn trace: %v\n", err)
	return 1
}

// traceStream copies in to out a line at a time, positions rewritten; the
// line breaks, and a last line without one, stay as they are. Input that
// starts as an XML report does (a declaration, or the root element of one
// trace.Report reads) is read whole and rewritten as that report; if it is
// none, as text.
func traceStream(t *trace.Tracer, in io.Reader, out io.Writer) error {
	r := bufio.NewReader(in)
	w := bufio.NewWriter(out)
	first, err := r.ReadString('\n')
	err = endOfStream(err)
	if err != nil && err != io.EOF {
		return err
	}
	if startsReport(first) {
		rest, err := io.ReadAll(r)
		if err = endOfStream(err); err != nil && err != io.EOF {
			return err
		}
		data := append([]byte(first), rest...)
		if doc, _, ok := t.Report(data); ok {
			w.Write(doc)
		} else {
			w.WriteString(t.Rewrite(string(data)))
		}
		return w.Flush()
	}
	line := first
	for {
		if line != "" {
			if _, werr := w.WriteString(t.Rewrite(line)); werr != nil {
				return werr
			}
			if werr := w.Flush(); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		line, err = r.ReadString('\n')
		err = endOfStream(err)
	}
}

// endOfStream reads a read deadline, which ends phn trace -- command's
// reading of what the command left (traceRun), as the end of the stream.
func endOfStream(err error) error {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return io.EOF
	}
	return err
}

// startsReport tells whether a first line opens an XML document: its
// declaration, or the root element of a report trace reads. A PHP error
// written as HTML (<br />) is text.
func startsReport(line string) bool {
	line = strings.TrimLeft(strings.TrimPrefix(line, "\uFEFF"), " \t\r\n")
	for _, p := range []string{"<?xml", "<!DOCTYPE", "<checkstyle", "<testsuites", "<testsuite", "<coverage"} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}
