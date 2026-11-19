<?php

declare(strict_types=1);

// Runs the compiled program: PHP checks a parameter's type when a closure
// is called, so each closure a partial application was lowered to is
// called with what the checker let its callers pass.

require __DIR__ . '/Shop/Picks.php';

$picks = new \Shop\Picks(new \Shop\Sku('sku-1'));

echo ($picks->orZero)('a'), ' ', $picks->named['pick']('b'), ' ', $picks->named['last']('c', 1.5), "\n";
echo var_export($picks->listed[0](null), true), ' ', var_export($picks->listed[1]('d'), true), "\n";
echo implode(',', $picks->mapped(['e', 'f'])), ' ', implode(',', $picks->labelled(['g', 'h'])), "\n";
echo $picks->applied('i'), ' ', $picks->made()('j'), ' ', $picks->tuple()[0]('k'), "\n";
echo $picks->defaulted('l'), ' ', $picks->defaulted('m', static fn(string $s): int => strlen($s)), "\n";
echo $picks->unexpected(4), "\n";
