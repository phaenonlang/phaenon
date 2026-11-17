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
	script := `h=0
trap 'h=$((h+1)); echo hup $h' HUP
trap 'echo term; exit 3' TERM
echo ready
while :; do :; done
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
	// The command runs its traps at once (a loop of the builtin :, not a
	// sleep it waits for), so that a second hangup is seen as one.
	syscall.Kill(group, syscall.SIGHUP)
	time.Sleep(300 * time.Millisecond)
	syscall.Kill(group, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var got []string
	timeout := time.After(10 * time.Second)
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
	if strings.Join(got, "") != "hup 1\nterm\n" {
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
