<?php

declare(strict_types=1);

// Pipelines and partial applications already written stay as they are:
// migrate on its own output changes nothing.

function shout(string $s): string
{
    return $s |> trim(...) |> strtoupper(...);
}

/** @param list<int> $xs */
function doubled(array $xs): string
{
    return $xs
        |> array_map(fn (int $x): int => $x * 2, ?)
        |> implode(',', ?)
        |> sprintf('[%s]', ?);
}

// A chain inside a stage stays: one stage per line, its pipeline would end
// in the middle of the outer one.
/** @param list<string> $xs */
function inner(array $xs): string
{
    return $xs |> array_map(fn (string $x): string => strtoupper(trim(str_repeat($x, 2))), ?) |> implode('/', ?);
}

echo shout(' a '), ' ', doubled([1, 2]), ' ', inner([' p', 'q ']), "\n";
