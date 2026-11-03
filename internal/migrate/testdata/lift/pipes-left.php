<?php

declare(strict_types=1);

namespace App\Left;

// Every chain here stays a nest of calls; the .notes file says why.

function trace(string $label, string $value): string
{
    echo "eval $label\n";
    return $value;
}

function pair(string $a, string $b): string
{
    return "[$a|$b]";
}

function bump(int &$n): int
{
    return ++$n;
}

final class Store
{
    /** @var list<int> */
    public static array $numbers = [3, 1, 2];
}

/** @return list<int> */
function &numbers(): array
{
    return Store::$numbers;
}

/**
 * @param list<int> $xs
 * @return list<int>
 */
function sorted(array &$xs): array
{
    sort($xs);
    return $xs;
}

final class Labeller
{
    public function label(string $s): string
    {
        return "<$s>";
    }
}

final class Shelf
{
    public function __construct(private Labeller $labeller) {}

    // A property with hooks is no receiver: a pipeline would run its get
    // hook after the inner calls.
    public Labeller $hooked {
        get => $this->labeller;
    }

    public function shelve(string $s): string
    {
        return $this->hooked->label(strtoupper(trim($s)));
    }

    // Nor is a property the inner calls assign.
    public function relabel(string $s): string
    {
        return $this->labeller->label(strtoupper(trim(($this->labeller = new Labeller()) ? $s : '')));
    }
}

// A call before the nested one would run after it.
function order(): string
{
    return pair(trace('first', 'F'), trim(trace('inner', ' i ')));
}

// Another call beside the nested one: which of them is the data is not
// for the codemod to choose.
function several(string $a, string $b): string
{
    return sprintf('%s %s', strtoupper(trim($a)), trim($b));
}

// A variable read before the nested call, which the inner calls assign.
function assigned(int $n): string
{
    return sprintf('%d %d', $n, abs(bump($n)));
}

// The piped parameter is by reference (the checker reports P2112 on the
// call as written: it needs a variable).
function byReference(): string
{
    return implode(',', sorted(numbers()));
}

// The nested call is spread.
function spread(): string
{
    return implode(',', array_merge(...array_map(fn (int $x): array => [$x, $x], [1, 2])));
}

// A constant expression.
const SHOUT = static function (string $s): string {
    return strtoupper(trim(str_repeat($s, 2)));
};

// A call that reads its scope cannot become a callable.
function joined(string $a, string $b): string
{
    return strtoupper(implode(',', func_get_args()));
}

// A dynamic callee resolves to nothing the model knows.
function dynamic(string $s): string
{
    $f = trim(...);
    return strtoupper($f(str_repeat($s, 2)));
}

// A nullsafe call skips its arguments on null.
function nullsafe(?Labeller $l, string $s): ?string
{
    return $l?->label(strtoupper(trim($s)));
}

// An operand: the outer call cannot be a stage, and two calls are left.
function operand(): string
{
    return 'x: ' . strtoupper(trim(str_repeat(' a', 2)));
}

// Inside a string.
function interpolated(Labeller $l, string $s): string
{
    return "label: {$l->label(strtoupper(trim($s)))}";
}

// A comment between the calls.
function commented(): string
{
    return strtoupper(/* trimmed first */ trim(str_repeat(' a', 2)));
}

// __LINE__ would move to another line.
function line(): string
{
    return sprintf('%d: %s', __LINE__, strtoupper(trim(' x ')));
}

function frame(string $s, string $frame): string
{
    return sprintf($frame, $s);
}

// Laid out one stage per line, the pipeline would start inside the
// constructor's brackets, in the middle of its line.
final class Note
{
    public function __construct(public string $text) {}
}

function wrapped(string $s): Note
{
    return new Note(strtolower(trim(str_repeat($s, 2))));
}

// A multi-line string would have to move sideways with its stage.
function heredoc(string $s): string
{
    return frame(strtolower(trim(strtoupper($s))), <<<TXT
        [%s]
        TXT);
}

echo (new Shelf(new Labeller()))->shelve(' s '), ' ', (new Shelf(new Labeller()))->relabel(' r '), "\n";
echo order(), "\n";
echo several(' a ', ' b '), "\n";
echo assigned(1), "\n";
echo byReference(), "\n";
echo spread(), "\n";
echo (SHOUT)(' c '), "\n";
echo joined('a', 'b'), "\n";
echo dynamic(' d'), "\n";
echo nullsafe(null, 'n') ?? 'null', ' ', nullsafe(new Labeller(), ' n '), "\n";
echo operand(), "\n";
echo interpolated(new Labeller(), ' i '), "\n";
echo commented(), "\n";
echo heredoc(' a '), "\n";
echo wrapped(' w ')->text, "\n";
