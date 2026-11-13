package lsp

import (
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phaenonlang/phaenon/internal/checker"
	"github.com/phaenonlang/phaenon/internal/program"
)

// memLoader is a project's Composer maps in memory, by lower-cased class
// name: a vendor class is found by its name and read on first use, into the
// program that asked, as phn's programs load vendor code.
type memLoader map[string]string

func (l memLoader) FindClass(lower string) (string, bool) {
	if _, ok := l[lower]; !ok {
		return "", false
	}
	return "vendor/" + strings.ReplaceAll(lower, `\`, "/") + ".php", true
}

func (l memLoader) Preload() []string { return nil }

func (l memLoader) Read(path string) ([]byte, error) {
	lower := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(path, "vendor/"), ".php"), "/", `\`)
	if src, ok := l[lower]; ok {
		return []byte(src), nil
	}
	return nil, os.ErrNotExist
}

// A shop whose findings point at declarations elsewhere, which publishing
// them looks up in the check's program, and which imports a vendor class
// nothing in it uses: the check never loads it, a hover on the import
// does, into the same program.
const vendorShopSrc = `namespace App;

use Vendor\Lib\Widget;

final class Shop
{
    greet(User $u): string {
        $a = strlen(1);
        $b = strlen(2);
        return $u->rename(1)->name();
    }
}
`

// A hover right after an edit checks the new text itself; the pause after
// the edit then ends, and its timer publishes that same check, reading the
// related declarations from the program the hover loaded a vendor class
// into. Nothing orders the two unless the timer takes the requests' lock,
// and a request that loads while the timer reads writes a map being read.
// Run with -race, which reports the unordered accesses whenever they
// happen.
func TestPublishingAndRequestsShareACheck(t *testing.T) {
	host := newFakeHost(t, map[string]string{"src/User.phn": userSrc, "src/Shop.phn": vendorShopSrc})
	host.vendor = memLoader{`vendor\lib\widget`: "<?php\n\nnamespace Vendor\\Lib;\n\nfinal class Widget {}\n"}
	srv := New(host, io.Discard)
	// Longer than a check takes under the race detector, so that the hover's
	// check is done when the timer looks for one.
	srv.Debounce = 1500 * time.Millisecond
	c := startHost(t, host, srv, nil, nil)
	c.open("src/Shop.phn", vendorShopSrc)
	c.diagnostics("src/Shop.phn", 1)
	c.notify("textDocument/didChange", map[string]any{"textDocument": map[string]any{"uri": c.uri("src/Shop.phn"), "version": 2},
		"contentChanges": []any{map[string]any{"text": vendorShopSrc + "// edited\n"}}})
	if h := jsonOf(c.at("textDocument/hover", "src/Shop.phn", 2, 18)); !strings.Contains(h, `Vendor\\Lib\\Widget`) {
		t.Fatalf("hover on the import: %s", h)
	}
	d := c.diagnostics("src/Shop.phn", 2)
	related := 0
	for _, x := range d {
		if rel, _ := x.(map[string]any)["relatedInformation"].([]any); len(rel) > 0 {
			related++
		}
	}
	if related != 3 {
		t.Fatalf("want three findings with a related declaration: %s", jsonOf(d))
	}
}

// overlapHost builds slowly and counts the builds that began while another
// one ran: two builds at once both write the project's outputs.
type overlapHost struct {
	*fakeHost
	running, overlaps atomic.Int32
	started           chan struct{} // a build began
	overlapped        chan struct{} // closed when a build begins while one runs
	once              sync.Once
	// outputs stands for the output directory, written without a lock as
	// phn build writes its files.
	outputs int
}

func (h *overlapHost) Build(p *program.Program, diags []checker.Diagnostic) (BuildParams, error) {
	if h.running.Add(1) > 1 {
		h.overlaps.Add(1)
		h.once.Do(func() { close(h.overlapped) })
	}
	defer h.running.Add(-1)
	select {
	case h.started <- struct{}{}:
	default:
	}
	h.outputs++
	// Writing takes long enough for a newer save's check to end and build.
	select {
	case <-h.overlapped:
	case <-time.After(time.Second):
	}
	h.outputs++
	return h.fakeHost.Build(p, diags)
}

// A save while the previous save's build is still writing starts a check
// of its own, which ends long before that build does: its build must wait
// for the one running.
func TestBuildsDoNotOverlap(t *testing.T) {
	host := newFakeHost(t, map[string]string{"src/User.phn": userSrc, "src/Shop.phn": shopSrc})
	builder := &overlapHost{fakeHost: host, started: make(chan struct{}, 1), overlapped: make(chan struct{})}
	srv := New(builder, io.Discard)
	srv.Debounce = 5 * time.Millisecond
	c := startHost(t, host, srv, nil, nil)
	save := map[string]any{"textDocument": map[string]any{"uri": c.uri("src/Shop.phn")}}
	c.notify("textDocument/didSave", save)
	select {
	case <-builder.started:
	case <-time.After(15 * time.Second):
		t.Fatal("the first save did not build")
	}
	c.notify("textDocument/didSave", save)
	if c.wait(func(m map[string]any) bool { return m["method"] == "phaenon/build" }) == nil {
		t.Fatal("no phaenon/build after the second save")
	}
	if n := builder.overlaps.Load(); n != 0 {
		t.Errorf("%d builds ran while another one did", n)
	}
}
