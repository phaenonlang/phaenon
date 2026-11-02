package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phaenonlang/phaenon/internal/trace"
)

// traceCmd is `phn trace`: it rewrites the positions in compiled files that
// its arguments, or stdin, name into the .phn positions their line maps
// give (design/line-map.md §4.1), so that `vendor/bin/phpunit | phn trace`
// and a pasted production trace point into the sources. Stdin is rewritten
// a line at a time, as it arrives.
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
	if fs.NArg() > 0 {
		fmt.Println(t.Rewrite(strings.Join(fs.Args(), " ")))
		return
	}
	if err := traceStream(t, os.Stdin, os.Stdout); err != nil {
		fail("phn trace: %v", err)
	}
}

// traceStream copies in to out a line at a time, positions rewritten; the
// line breaks, and a last line without one, stay as they are.
func traceStream(t *trace.Tracer, in io.Reader, out io.Writer) error {
	r := bufio.NewReader(in)
	w := bufio.NewWriter(out)
	for {
		line, err := r.ReadString('\n')
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
	}
}
