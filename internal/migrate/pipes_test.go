package migrate

import (
	"testing"

	"github.com/phaenonlang/phaenon/internal/phpversion"
	"github.com/phaenonlang/phaenon/internal/program"
)

// pipes runs the codemod over one Phaenon source, for target.
func pipes(t *testing.T, src string, target phpversion.Version, min int) (string, *PipeReport) {
	t.Helper()
	load := func(srcs map[string][]byte, checked map[string]bool) (*program.Program, error) {
		p, err := pipeLoad(srcs, checked)
		if p != nil {
			p.SetTarget(target)
		}
		return p, err
	}
	out, rep, err := Pipes([]PipeFile{{Path: "t.phn", Src: []byte(src)}}, PipeOptions{MinCalls: min, Load: load})
	if err != nil {
		t.Fatal(err)
	}
	return string(out[0]), rep
}

// An anonymous class in a bound argument moves to its stage's line; its
// type, whose name holds the line, is not a change the check undoes. For
// PHP 8.5 the stage is lowered as any other: the class's members are no
// variables of the closure (TestLift's pipes fixture runs one).
func TestPipesMoveAnAnonymousClass(t *testing.T) {
	src := "/** @param list<int> $xs */\nfun tagged(array $xs, object $o): int => count($xs);\n\n/** @param list<int> $xs */\nfun f(array $xs): int {\n    return tagged(array_values(array_filter(array_map(fn (int $x): int => $x - 1, $xs))), new class { public int $a = 1; });\n}\n"
	want := "/** @param list<int> $xs */\nfun tagged(array $xs, object $o): int => count($xs);\n\n/** @param list<int> $xs */\nfun f(array $xs): int {\n    return $xs\n        |> array_map(fn (int $x): int => $x - 1, ?)\n        |> array_filter(...)\n        |> array_values(...)\n        |> tagged(?, new class { public int $a = 1; });\n}\n"
	for _, target := range []phpversion.Version{phpversion.PHP85, phpversion.PHP86} {
		got, rep := pipes(t, src, target, 3)
		if got != want || rep.Pipelines != 1 {
			t.Errorf("target %s: %d pipelines, %v; got:\n%s", target, rep.Pipelines, rep.Undone, got)
		}
	}
}

// An anonymous class's constructor may take a variable by reference, as any
// constructor may: an argument written before the nested call that reads
// the variable keeps the chain as calls.
func TestPipesKeepOrderAroundAnAnonymousClass(t *testing.T) {
	src := "fun f(string $s): string => str_repeat($s, strlen(implode(',', array_keys(get_object_vars(new class ($s) { fun __construct(string &$t) { $t = 'x'; } })))));\n"
	got, rep := pipes(t, src, phpversion.PHP86, 3)
	if got != src || rep.Left[ReasonOrder] != 1 {
		t.Errorf("left %v; got:\n%s", rep.Left, got)
	}
}

// A call with explicit type arguments is no stage.
func TestPipesKeepTypeArguments(t *testing.T) {
	src := "fun first<T>(list<T> $items): T => $items[0];\n\n/** @param list<string> $xs */\nfun f(array $xs): string => first<string>(array_values(array_filter($xs)));\n"
	got, rep := pipes(t, src, phpversion.PHP85, 3)
	if got != src || rep.Left[ReasonTypeArgs] != 1 {
		t.Errorf("left %v; got:\n%s", rep.Left, got)
	}
}

// A file indented with tabs gets its stages one tab in.
func TestPipesIndentWithTabs(t *testing.T) {
	src := "fun f(string $s): string {\n\treturn strtoupper(trim(str_repeat($s, 2)));\n}\n"
	want := "fun f(string $s): string {\n\treturn $s\n\t\t|> str_repeat(?, 2)\n\t\t|> trim(...)\n\t\t|> strtoupper(...);\n}\n"
	if got, _ := pipes(t, src, phpversion.PHP85, 3); got != want {
		t.Errorf("got:\n%s", got)
	}
}

// The summary counts chains by reason, the most frequent first.
func TestPipeReportReasons(t *testing.T) {
	r := &PipeReport{Left: map[string]int{ReasonOrder: 2, ReasonByRef: 5, ReasonSpread: 2}}
	if got, want := r.Reasons(), "5 by-reference parameter, 2 evaluation order, 2 spread"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if r.LeftCalls() != 9 {
		t.Errorf("LeftCalls %d", r.LeftCalls())
	}
}

// Nothing to rewrite, nothing checked: Load is not even called.
func TestPipesWithoutChains(t *testing.T) {
	called := false
	load := func(map[string][]byte, map[string]bool) (*program.Program, error) {
		called = true
		return nil, nil
	}
	src := "fun f(string $s): string => trim(strtoupper($s));\n"
	out, rep, err := Pipes([]PipeFile{{Path: "t.phn", Src: []byte(src)}}, PipeOptions{MinCalls: 3, Load: load})
	if err != nil || called || string(out[0]) != src || rep.LeftCalls() != 0 {
		t.Errorf("err %v, called %v, left %d, out %q", err, called, rep.LeftCalls(), out[0])
	}
}
