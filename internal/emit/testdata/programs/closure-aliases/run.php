<?php

declare(strict_types=1);

// Runs the compiled program: PHP checks a parameter's type when the
// function is called, so the closures are passed, not only declared.

require __DIR__ . '/Orders/types.php';
require __DIR__ . '/Http/Runner.php';

$runner = new \Shop\Http\Runner();
$double = static fn(int $x): int => $x * 2;
$inc = $runner->make(1);

echo $runner->run(['name' => 'double', 'run' => $double], 3), "\n";
echo $runner->run(['name' => 'both', 'run' => $double, 'then' => $inc], 3), "\n";
echo $runner->step($inc, 3), "\n";
echo $runner->maybe(null, 3), ' ', $runner->maybe($double, 3), "\n";
$chosen = $runner->choose(static fn(\Closure $a, \Closure $b): \Closure => $b, $inc, $double);
echo $chosen === null ? 'none' : $chosen(5), "\n";
echo $runner->summary([$inc, $double])['count'], "\n";
echo $runner->encode(['body' => 'ab', 'encode' => static fn(string $b): string => strtoupper($b)]), "\n";
