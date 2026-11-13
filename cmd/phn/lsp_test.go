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
	t    *testing.T
	in   io.WriteCloser
	msgs chan map[string]any // what the server writes, read as it comes
	cmd  *exec.Cmd
	id   int
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
	s := &lspSession{t: t, in: in, msgs: make(chan map[string]any, 100), cmd: cmd}
	go s.read(bufio.NewReader(out))
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

// read passes the server's messages to msgs until its standard output ends;
// what is not a message ends it too, and next reports it.
func (s *lspSession) read(out *bufio.Reader) {
	defer close(s.msgs)
	for {
		n := 0
		for {
			line, err := out.ReadString('\n')
			if err != nil {
				return
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
		if _, err := io.ReadFull(out, body); err != nil {
			return
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			m = map[string]any{"notJSON": string(body)}
		}
		s.msgs <- m
	}
}

// next waits for a message that satisfies pred, passing over the others.
func (s *lspSession) next(pred func(map[string]any) bool) map[string]any {
	s.t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case m, ok := <-s.msgs:
			switch {
			case !ok:
				s.t.Fatal("phn lsp ended its output")
			case m["notJSON"] != nil:
				s.t.Fatalf("not JSON on stdout: %q", m["notJSON"])
			case pred(m):
				return m
			}
		case <-timeout:
			s.t.Fatal("timed out")
		}
	}
}

// phn lsp over stdio in a real project: the opened file's diagnostics, and
// on save the whole project's and its outputs, written as phn build writes
// them; a file with errors is withheld.
func TestLSP(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	broken := "namespace App;\n\nfunction broken(): int => 'x';\n"
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

// phn lsp reads the project again when the client reports a change of
// phaenon.json or composer.json: the next check leaves out what "exclude"
// names now. A project file that has become invalid is shown to the user,
// and the server goes on, and reads it again once it is valid.
func TestLSPRereadsTheProject(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	writeTree(t, root, map[string]string{"src/Broken.phn": brokenSource})
	s := startLSP(t, bin, root, `{}`)
	uri := fileURI(filepath.Join(root, "src/Broken.phn"))
	diagnostics := func() []any {
		t.Helper()
		m := s.next(func(m map[string]any) bool {
			p, _ := m["params"].(map[string]any)
			return m["method"] == "textDocument/publishDiagnostics" && p["uri"] == uri
		})
		return m["params"].(map[string]any)["diagnostics"].([]any)
	}
	changed := func(project string) {
		writeTree(t, root, map[string]string{"phaenon.json": project})
		s.send(`{"jsonrpc":"2.0","method":"workspace/didChangeWatchedFiles","params":{"changes":[{"uri":"` +
			fileURI(filepath.Join(root, "phaenon.json")) + `","type":2}]}}`)
	}
	if d := diagnostics(); len(d) != 1 {
		t.Fatalf("the check at start: %v", d)
	}
	changed(`{"roots": {"src": ".src"}, "exclude": ["src/Broken.phn"]}`)
	if d := diagnostics(); len(d) != 0 {
		t.Errorf("an excluded file is still reported: %v", d)
	}
	changed(`{"roots": {"src": "src"}}`)
	m := s.next(func(m map[string]any) bool { return m["method"] == "window/showMessage" })
	if p := m["params"].(map[string]any); p["type"] != float64(1) || !strings.Contains(p["message"].(string), `the output of "src" must be another directory`) {
		t.Errorf("the invalid project file: %v", p)
	}
	changed(`{"roots": {"src": ".src"}}`)
	if d := diagnostics(); len(d) != 1 {
		t.Errorf("the project read again after its file was fixed: %v", d)
	}
}

// startWatch runs `phn watch` in dir until the test ends, and returns a
// function that waits for a line of its output, standard output and error
// together, holding want.
func startWatch(t *testing.T, bin, dir string) (wait func(want string)) {
	t.Helper()
	cmd := exec.Command(bin, "watch", "--interval", "50ms")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PHN_PHP=/nonexistent-php")
	out, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close() // the child's copy keeps it open while phn watch runs
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		out.Close()
	})
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	return func(want string) {
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
}

// phn watch builds at start and again when a source changes.
func TestWatch(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	wait := startWatch(t, bin, root)
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

const brokenSource = "namespace App;\n\nfunction broken(): int => 'x';\n"

// phn watch reads phaenon.json and composer.json again when either changes:
// the next run leaves out what "exclude" names now, and checks for the
// target composer.json states now. A project file that has become invalid
// is reported at every run, which checks the project as last read and
// builds nothing, and the watcher goes on.
func TestWatchRereadsTheProject(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	writeTree(t, root, map[string]string{"src/Broken.phn": brokenSource})
	wait := startWatch(t, bin, root)
	change := func(files map[string]string) {
		t.Helper()
		wait("waiting for changes")
		time.Sleep(100 * time.Millisecond) // a modification time the poll can tell apart
		writeTree(t, root, files)
	}
	wait("3 files checked: 1 errors")
	change(map[string]string{"phaenon.json": `{"roots": {"src": ".src"}, "exclude": ["src/Broken.phn"]}`})
	wait("2 files checked: 0 errors")
	change(map[string]string{"composer.json": `{"name": "acme/app", "require": {"php": ">=8.4"}}`})
	wait("warning P4008") // composer.json admits PHP 8.4, older than phn compiles for
	change(map[string]string{"phaenon.json": `{"roots": {"src": "src"}}`})
	wait(`phaenon.json: the output of "src" must be another directory; until it is fixed`)
	change(map[string]string{"phaenon.json": `{"roots": {"src": ".src"}}`})
	wait("3 files checked: 1 errors")
}

// phn watch started in a directory of the project watches the project, as
// phn lsp serves it: the nearest phaenon.json above (design/tooling.md
// §6.4, §6.5).
func TestWatchFromASubdirectory(t *testing.T) {
	bin := phnBinary(t)
	root := fixTree(t)
	wait := startWatch(t, bin, filepath.Join(root, "src"))
	wait("waiting for changes")
	if _, err := os.Stat(filepath.Join(root, ".src/Box.php")); err != nil {
		t.Errorf("the project was not built: %v", err)
	}
}
