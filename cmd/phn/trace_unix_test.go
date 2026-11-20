//go:build unix

package main

import (
	"bufio"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTraceRunGroupSignals: what is sent to the whole process group, as a
// terminal's hangup is and a control group's kill can be, reaches the
// command once: phn passes none of it on again, and exits with the status
// the command ends with.
func TestTraceRunGroupSignals(t *testing.T) {
	// The loop runs builtins only, so that the traps run at once, and
	// says when a hangup's trap has returned: the termination is sent
	// only then. A signal that arrives while a trap runs is not handled
	// as one that arrives between commands by every sh (bash 3.2, the
	// /bin/sh of macOS, starts the running trap over or leaves it).
	script := `h=0 seen=0
trap 'h=$((h+1)); echo hup $h' HUP
trap 'echo term; exit 3' TERM
echo ready
while :; do
	if [ "$h" != "$seen" ]; then seen=$h; echo "back $h"; fi
done
`
	cmd := exec.Command(phnBinary(t), "trace", "--", "sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // a group of its own, as a terminal gives a job
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(out)
	if line, err := r.ReadString('\n'); err != nil || line != "ready\n" {
		cmd.Process.Kill()
		t.Fatalf("the command's first line: %q %v", line, err)
	}
	lines := make(chan string, 16)
	go func() {
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				lines <- line
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	group := -cmd.Process.Pid
	var got []string
	timeout := time.After(10 * time.Second)
	syscall.Kill(group, syscall.SIGHUP)
	for back := false; !back; {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the command ended before its hangup trap returned; it wrote %q", got)
			}
			got = append(got, line)
			back = line == "back 1\n"
		case <-timeout:
			syscall.Kill(group, syscall.SIGKILL)
			t.Fatalf("the command's hangup trap did not return; it wrote %q", got)
		}
	}
	// A hangup phn passed on as well would come in now: its trap writes
	// hup 2. The window only bounds how late one is seen; the command
	// is in its loop throughout, however slowly it runs.
	for window := time.After(300 * time.Millisecond); window != nil; {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("the command ended before its termination; it wrote %q", got)
			}
			got = append(got, line)
		case <-window:
			window = nil
		}
	}
	syscall.Kill(group, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for collecting := true; collecting; {
		select {
		case line, ok := <-lines:
			if !ok {
				collecting = false
				continue
			}
			got = append(got, line)
		case <-timeout:
			syscall.Kill(group, syscall.SIGKILL)
			t.Fatalf("phn did not end; the command wrote %q", got)
		}
	}
	err = <-done
	var exit *exec.ExitError
	if !errorsAs(err, &exit) || exit.ExitCode() != 3 {
		t.Errorf("status: %v, want the command's 3", err)
	}
	if strings.Join(got, "") != "hup 1\nback 1\nterm\n" {
		t.Errorf("the command saw %q, want one hangup and one termination", got)
	}
}

// TestTraceRunLeavesTheBackground: a process the command leaves running in
// the background, holding its standard output, does not keep phn waiting
// once the command has ended.
func TestTraceRunLeavesTheBackground(t *testing.T) {
	cmd := exec.Command(phnBinary(t), "trace", "--", "sh", "-c", "sleep 5 & echo done")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// phn's own end, not the pipe's: the sleep holds that one too.
	first, _ := bufio.NewReader(out).ReadString('\n')
	go io.Copy(io.Discard, out)
	err = cmd.Wait()
	if elapsed := time.Since(started); err != nil || first != "done\n" || elapsed > 3*time.Second {
		t.Errorf("phn: %v, %q, after %v; want status 0 and done well before the background sleep ends", err, first, elapsed)
	}
}
