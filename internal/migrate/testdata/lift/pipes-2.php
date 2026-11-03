<?php

declare(strict_types=1);

// --pipes=2: two calls are a chain too.

/** @param list<int> $xs */
function countLine(array $xs): string
{
    return sprintf('%d items', count($xs));
}

function bounded(int $x): int
{
    return max(0, min(10, $x));
}

// One call is not.
function single(string $s): string
{
    return trim($s);
}

// Three calls, as by default.
function three(string $s): string
{
    return strtoupper(trim(str_repeat($s, 2)));
}

echo countLine([1, 2, 3]), "\n";
echo bounded(42), ' ', bounded(-3), "\n";
echo single(' s '), "\n";
echo three(' t '), "\n";
