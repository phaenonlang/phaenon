<?php

declare(strict_types=1);

// --keep-calls: no chain becomes a pipeline, and none is reported.

/** @param list<int> $xs */
function listed(array $xs): string
{
    return sprintf('[%s]', implode(',', array_map(fn (int $x): string => (string) ($x * 2), $xs)));
}

function banner(): string
{
    return strtoupper(trim(str_repeat(' ab', 2)));
}

echo listed([1, 2]), "\n";
echo banner(), "\n";
