package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lspSession runs `phn lsp` in a project and speaks to it.
type lspSession struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Reader
	cmd *exec.Cmd
	id  int
}

func startLSP(t *testing.T, bin, dir string, opts string) *lspSession {
	t.Helper()
	cmd := exec.Command(bin, "lsp")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PHN_PHP=/nonexistent-php") // no php: the stubs alone
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &lspSession{t: t, in: in, out: bufio.NewReader(out), cmd: cmd}
	t.Cleanup(func() {
		s.send(`{"jsonrpc":"2.0","id":999,"method":"shutdown"}`)
		s.send(`{"jsonrpc":"2.0","method":"exit"}`)
		in.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			t.Error("phn lsp did not exit")
		}
	})
	s.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"rootUri":"` + fileURI(dir) + `","capabilities":{},"initializationOptions":` + opts + `}}`)
	s.next(func(m map[string]any) bool { return m["id"] == float64(1) })
	s.send(`{"jsonrpc":"2.0","method":"initialized","params":{}}`)
	return s
}

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func (s *lspSession) send(body string) {
	fmt.Fprintf(s.in, "Content-Length: %d\r\n\r\n%s", len(body), body)
}

// next reads messages until one satisfies pred.
func (s *lspSession) next(pred func(map[string]any) bool) map[string]any {
	s.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		n := 0
		for {
			line, err := s.out.ReadString('\n')
			if err != nil {
				s.t.Fatalf("phn lsp: %v", err)
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
				n, _ = strconv.Atoi(v)
			}
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(s.out, body); err != nil {
			s.t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			s.t.Fatalf("not JSON on stdout: %q", body)
		}
		if pred(m) {
			return m
		}
	}
	s.t.Fatal("timed out")
	return nil
}

// phn lsp over stdio in a real project: the opened file's diagnostics, and
// on save the whole project's and its outputs, written as phn build writes
// them; a file with errors is withheld.
func TestLSP(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	broken := "namespace App;\n\nfun broken(): int => 'x';\n"
	writeTree(t, root, map[string]string{"src/Broken.phn": broken})
	s := startLSP(t, bin, root, `{"buildOnSave": true}`)

	uri := fileURI(filepath.Join(root, "src/Broken.phn"))
	open, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": "phaenon", "version": 1, "text": broken}}})
	s.send(string(open))
	m := s.next(func(m map[string]any) bool {
		p, _ := m["params"].(map[string]any)
		return m["method"] == "textDocument/publishDiagnostics" && p["uri"] == uri && p["version"] == float64(1)
	})
	if d := m["params"].(map[string]any)["diagnostics"].([]any); len(d) != 1 || d[0].(map[string]any)["code"] != "P2202" {
		t.Fatalf("diagnostics: %v", d)
	}
	if _, err := os.Stat(filepath.Join(root, ".src/Box.php")); err == nil {
		t.Fatal("opening the project built it")
	}
	s.send(`{"jsonrpc":"2.0","method":"textDocument/didSave","params":{"textDocument":{"uri":"` + uri + `"}}}`)
	m = s.next(func(m map[string]any) bool { return m["method"] == "phaenon/build" })
	p := m["params"].(map[string]any)
	if p["files"] != float64(2) || p["withheld"] != float64(1) {
		t.Errorf("build: %v", p)
	}
	if _, err := os.Stat(filepath.Join(root, ".src/Box.php")); err != nil {
		t.Errorf("the output of Box.phn: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".src/data.php")); err != nil {
		t.Errorf("the copy of data.php: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".src/Broken.php")); err == nil {
		t.Error("the file with an error was compiled")
	}
	if _, stderr, err := run(t, root, bin, "build", "--check", "--exclude", "src/Broken.phn"); err != nil && !strings.Contains(stderr, "Broken") {
		t.Errorf("the outputs are not phn build's: %s", stderr)
	}
}

// phn watch builds at start and again when a source changes.
func TestWatch(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	cmd := exec.Command(bin, "watch", "--interval", "50ms")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PHN_PHP=/nonexistent-php")
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	wait := func(want string) {
		t.Helper()
		timeout := time.After(20 * time.Second)
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("phn watch ended before printing %q", want)
				}
				if strings.Contains(line, want) {
					return
				}
			case <-timeout:
				t.Fatalf("phn watch never printed %q", want)
			}
		}
	}
	wait("waiting for changes")
	if _, err := os.Stat(filepath.Join(root, ".src/Box.php")); err != nil {
		t.Fatalf("the first build: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // a modification time the poll can tell apart
	writeTree(t, root, map[string]string{"src/Box.phn": strings.Replace(boxSource, "$a+$b", "$a-$b", 1)})
	wait("2 files checked") // the second run: Box.phn and data.php
	wait("waiting for changes")
	data, _ := os.ReadFile(filepath.Join(root, ".src/Box.php"))
	if !strings.Contains(string(data), "$a-$b") {
		t.Errorf("the change was not built:\n%s", data)
	}
}
