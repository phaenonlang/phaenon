<?php

declare(strict_types=1);

namespace App\Pipes;

final class User
{
    public function __construct(public readonly ?string $email) {}
}

final class Text
{
    public function __construct(private string $sep) {}

    /** @param list<string> $parts */
    public function join(array $parts): string
    {
        return implode($this->sep, $parts);
    }

    public static function shout(string $s): string
    {
        return strtoupper($s) . '!';
    }

    // A method stage on $this, a static one, first-class callables, a
    // partial application whose subject is the last argument.
    public function line(string $s): string
    {
        return self::shout(trim($this->join(explode(',', $s))));
    }
}

function trace(string $label, string $value): string
{
    echo "eval $label\n";
    return $value;
}

// design/tooling.md §8.1's example: the head is the subject of the
// innermost call, the closures stay where they were.
/**
 * @param list<User> $users
 * @return list<string>
 */
function emails(array $users): array
{
    $emails = array_values(array_filter(array_map(fn (User $u) => $u->email, $users), fn (?string $e) => $e !== null));
    return $emails;
}

// With only literals, the innermost call is the head; two stages fit on
// one line.
function banner(): string
{
    return strtoupper(trim(str_repeat(' ab', 2)));
}

// The head runs first, as it did.
function traced(): string
{
    return strtoupper(trim(trace('head', ' x ')));
}

// A named argument keeps its name.
function padded(string $s): string
{
    return str_pad(string: trim(strtolower($s)), length: 8, pad_string: '.');
}

// Bound arguments that are variables and closures may run after the
// piped value: nothing the inner calls assign.
/** @param list<int> $xs */
function listed(string $glue, array $xs): string
{
    return sprintf('[%s]', implode($glue, array_map(fn (int $x): string => (string) ($x * 2), $xs)));
}

// A method stage on a property its class declares without hooks: the
// pipeline reads it after the inner calls, which do not assign it.
final class Banner
{
    public function __construct(private Text $text) {}

    public function title(string $csv): string
    {
        return $this->text->join(explode(',', strtoupper(trim($csv))));
    }
}

// A method stage on a variable, resolved through the type the checker
// gives it.
function shoutAll(Text $text, string $csv): string
{
    return Text::shout($text->join(explode(',', trim($csv))));
}

// A by-reference parameter splits the chain: the calls above it make a
// pipeline whose head is the call that takes the reference, and the
// variable read before it is not one the pipeline would read late.
function bumped(int &$n): string
{
    $n++;
    return " v$n ";
}

function split(int $n): string
{
    return sprintf('%d %s', $n, strtoupper(trim(bumped($n))));
}

// A chain inside a closure that is a bound argument: both are rewritten,
// the closure's lines re-indented with its stage.
/**
 * @param list<string> $rows
 * @return list<string>
 */
function nested(array $rows): array
{
    return array_values(array_filter(array_map(function (string $row): string {
        return strtoupper(trim(str_replace('-', ' ', $row)));
    }, $rows), fn (string $r): bool => $r !== ''));
}

// A chain written over several lines, trailing commas and all.
/** @param list<string> $words */
function multiline(array $words): string
{
    return implode(
        ' ',
        array_map(
            fn (string $w): string => ucfirst($w),
            array_filter($words, fn (string $w): bool => strlen($w) > 2),
        ),
    );
}

// Two stages that would pass 120 columns on one line.
function long(): string
{
    return htmlspecialchars(strtolower(sprintf('%s and %s', 'a fairly long literal', 'another fairly long one')), ENT_QUOTES | ENT_SUBSTITUTE, 'UTF-8', false);
}

// A head that is an operation goes in parentheses: `(string) $n |> …`
// parses the same, but reads as if the cast took the whole pipeline.
/** @param list<int> $numbers */
function widest(array $numbers): int
{
    $width = 0;
    foreach ($numbers as $n) {
        $width = max($width, \strlen(trim((string) $n)));
    }
    return $width;
}

// An anonymous class moves to the line of its stage: its name, which holds
// its line, is not a type that changed.
/** @param list<int> $xs */
function tagged(array $xs, object $o): int
{
    return count($xs) + count(get_object_vars($o));
}

/** @param list<int> $xs */
function anonymous(array $xs): int
{
    return tagged(array_values(array_filter(array_map(fn (int $x): int => $x - 1, $xs))), new class { public int $a = 1; });
}

// For PHP 8.5 the stage's closure captures what the class's constructor
// arguments read, not its property or its method's variables.
/** @param list<int> $xs */
function captures(array $xs, int $base): int
{
    return tagged(array_values(array_filter(array_map(fn (int $x): int => $x - $base, $xs))), new class ($base) {
        public int $a = 1;

        public function __construct(public int $base)
        {
        }

        public function sum(): int
        {
            $xs = [$this->a, $this->base];
            return array_sum($xs);
        }
    });
}

// A chain that is an argument on a line of its own.
function wrapped(string $s): Text
{
    return new Text(
        strtolower(trim(str_repeat($s, 2))),
    );
}

$text = new Text(', ');
echo $text->line('a, b ,c'), "\n";
echo implode('|', emails([new User('a@x'), new User(null), new User('b@x')])), "\n";
echo banner(), "\n";
echo traced(), "\n";
echo padded('  HeLLo '), "\n";
echo listed('+', [1, 2, 3]), "\n";
echo shoutAll($text, ' x,y '), "\n";
echo (new Banner(new Text('/')))->title(' a,b '), "\n";
echo split(1), "\n";
echo implode('|', nested(['a-b ', ' ', 'c'])), "\n";
echo multiline(['an', 'apple', 'tree']), "\n";
echo long(), "\n";
echo wrapped(' Q ')->join(['1', '2']), "\n";
echo widest([7, 1234, 56]), ' ', anonymous([1, 2, 3]), ' ', captures([1, 2, 3], 1), "\n";
