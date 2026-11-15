package emit

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestShapesAsPHPStanReads holds the erasure of a named shape to what it
// promises (design/collections.md §6.4): the shape the emitter writes into
// a docblock is the type PHPStan reads there. It asks PHPStan itself, with
// \PHPStan\dumpType() on calls of the compiled goldens, and compares what
// it prints with the shape the source declares. PHAENON_PHPSTAN names the
// binary; without it the test skips.
func TestShapesAsPHPStanReads(t *testing.T) {
	phpstan := os.Getenv("PHAENON_PHPSTAN")
	if phpstan == "" {
		t.Skip("PHAENON_PHPSTAN not set")
	}
	dir := t.TempDir()
	copyOutput := func(from, to string) {
		data, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, to), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	copyOutput(filepath.Join("testdata", "shapes.php"), "shapes.php")
	program := filepath.Join("testdata", "programs", "named-shapes")
	copyOutput(filepath.Join(program, "Orders", "Orders.php"), "Orders.php")
	copyOutput(filepath.Join(program, "Http", "OrderController.php"), "OrderController.php")

	// Each probe is an expression and the type the source gives it: the
	// alias's right-hand side, as PHPStan prints a type.
	probes := []struct{ expr, want string }{
		// type Row = array{id: int, name?: string};
		{`$orders->find(1)`, `array{id: int, name?: string}|null`},
		{`$orders->add(...)`, `Closure(array{id: int, name?: string}): void`},
		// type Page<T> = array{items: list<T>, total: int, next?: string};
		{`$orders->page(0)`, `array{items: list<array{id: int, name?: string}>, total: int, next?: string}`},
		{`$orders->first($orders->page(0))`, `array{id: int, name?: string}|null`},
		// type Order = array{id: int, lines: list<array{sku: string, qty: int}>, buyer: ?Customer};
		{`$orders->quantity(...)`, `Closure(array{id: int, lines: list<array{sku: string, qty: int}>, buyer: App\Orders\Customer|null}): int`},
		// type Open = array{id: int, ...};
		{`$orders->open(...)`, `Closure(array{id: int, ...}): mixed`},
		// type Keys = array{class: string, 'a-b': int, '$set'?: bool, 0: int, -1: string, '7': float};
		{`$orders->keys(...)`, `Closure(array{class: string, a-b: int, '$set'?: bool, 0: int, -1: string, 7: float}): string`},
		// type None = array{};
		{`$orders->none()`, `array{}`},
		// @return array{total: int, rows: list<Row>}
		{`$orders->totals()`, `array{total: int, rows: list<array{id: int, name?: string}>}`},
		{`\App\Orders\rows()`, `list<array{id: int, name?: string}>`},
		// Across files and namespaces: type Order = array{id: int, lines: list<Line>, buyer?: Customer};
		{`$controller->show(1)`, `array{id: int, lines: list<array{sku: string, qty: int}>, buyer?: Shop\Orders\Customer}|null`},
		{`$controller->index()`, `array{items: list<array{id: int, lines: list<array{sku: string, qty: int}>, buyer?: Shop\Orders\Customer}>, total: int, next?: string}`},
		{`$controller->summary([])`, `array{count: int, first?: array{sku: string, qty: int}}`},
		{`$shop->place(...)`, `Closure(Shop\Orders\Customer, array{sku: string, qty: int} ...): array{id: int, lines: list<array{sku: string, qty: int}>, buyer?: Shop\Orders\Customer}`},
	}
	var probe strings.Builder
	probe.WriteString("<?php\n\nfunction probe(\\App\\Orders\\Orders $orders, \\Shop\\Orders\\Orders $shop, \\Shop\\Http\\OrderController $controller): void\n{\n")
	first := 5 // the line of the first probe
	for _, p := range probes {
		fmt.Fprintf(&probe, "    \\PHPStan\\dumpType(%s);\n", p.expr)
	}
	probe.WriteString("}\n")
	if err := os.WriteFile(filepath.Join(dir, "probe.php"), []byte(probe.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "phpstan.neon")
	if err := os.WriteFile(cfg, []byte("parameters:\n    level: max\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command(phpstan, "analyse", "--no-progress", "--error-format=json", "-c", cfg, dir).Output() // status 1: the dumps are findings
	var report struct {
		Files map[string]struct {
			Messages []struct {
				Message string `json:"message"`
				Line    int    `json:"line"`
			} `json:"messages"`
		} `json:"files"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("phpstan output: %v\n%s", err, out)
	}
	if len(report.Errors) > 0 {
		t.Fatalf("phpstan: %s", strings.Join(report.Errors, "\n"))
	}
	dumped := map[int]string{}
	for file, f := range report.Files {
		for _, m := range f.Messages {
			text, isDump := strings.CutPrefix(m.Message, "Dumped type: ")
			if filepath.Base(file) != "probe.php" || !isDump {
				t.Errorf("phpstan reports %s:%d: %s", filepath.Base(file), m.Line, m.Message)
				continue
			}
			dumped[m.Line] = text
		}
	}
	for i, p := range probes {
		if got := dumped[first+i]; got != p.want {
			t.Errorf("%s\n\tPHPStan reads %s\n\tthe source says %s", p.expr, got, p.want)
		}
	}
}
