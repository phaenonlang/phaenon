package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// exitStatus is the status a command ended with, 0 for none.
func exitStatus(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return 0
}

// Asked for help, phn prints its usage on standard output and succeeds;
// with no command or an unknown one it prints the usage on standard error
// and fails. lex and print answer their own --help as the other commands
// do.
func TestHelp(t *testing.T) {
	bin := phnBinary(t)
	dir := t.TempDir()
	for _, arg := range []string{"--help", "-h", "help"} {
		stdout, stderr, err := run(t, dir, bin, arg)
		if err != nil || !strings.HasPrefix(stdout, "usage:") || !strings.Contains(stdout, "phn build") || stderr != "" {
			t.Errorf("phn %s (%v):\nstdout: %s\nstderr: %s", arg, err, stdout, stderr)
		}
	}
	for _, args := range [][]string{nil, {"frobnicate"}} {
		stdout, stderr, err := run(t, dir, bin, args...)
		if exitStatus(err) != 2 || stdout != "" || !strings.HasPrefix(stderr, "usage:") {
			t.Errorf("phn %v (%v):\nstdout: %s\nstderr: %s", args, err, stdout, stderr)
		}
	}
	for _, cmd := range []string{"lex", "print"} {
		for _, arg := range []string{"--help", "-h"} {
			stdout, stderr, err := run(t, dir, bin, cmd, arg)
			if err != nil || !strings.Contains(stdout+stderr, "usage: phn "+cmd+" [--php] <file>") || strings.Contains(stderr, "no such file") {
				t.Errorf("phn %s %s (%v):\nstdout: %s\nstderr: %s", cmd, arg, err, stdout, stderr)
			}
		}
		if _, stderr, err := run(t, dir, bin, cmd); exitStatus(err) != 2 || !strings.Contains(stderr, "usage: phn "+cmd) {
			t.Errorf("phn %s without a file (%v): %s", cmd, err, stderr)
		}
	}
}
