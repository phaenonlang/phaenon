<?php

declare(strict_types=1);

namespace Lift\MapKeys;

/**
 * A map's key is int|string or within it. PHP stores a null key as '', so
 * null goes where a string or array-key already holds '', and a key that may
 * be null otherwise, a bool or a float has no map key that means it: its tag
 * stays, with a note. A mixed key is any key, as PHPStan reads it.
 *
 * @param array<array-key|null, int> $any
 * @param array<mixed, int> $whatever
 * @param array<string|null, int> $named
 * @param array<?string, int> $short
 * @param array<int, string> $numbered
 * @param array<bool, string> $flags
 * @param array<float, string> $ratios
 * @return array<int|null, int>
 */
function keys(array $any, array $whatever, array $named, array $short, array $numbered, array $flags, array $ratios): array
{
    return [\count($any) + \count($whatever) + \count($named) + \count($short) + \count($numbered) + \count($flags) + \count($ratios)];
}
