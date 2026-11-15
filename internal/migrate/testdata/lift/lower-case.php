<?php

declare(strict_types=1);

namespace App;

/**
 * PHPStan reads its own words in any letter case; what the lift writes is
 * Phaenon's, which are lower case (design/roadmap.md D30). `Map` and `Class`
 * in another case are class names to PHPStan, and to Phaenon: they erase to
 * no array and no string, and stay in the docblock.
 *
 * @template-covariant T of \Countable
 */
interface Words
{
    /**
     * @return List<T>
     * @throws \RuntimeException
     */
    public function all(): array;

    /**
     * @return LIST<int>
     * @throws VOID
     */
    public function ints(): array;

    /**
     * @param Array<string, int> $m
     * @param Class-String<T> $c
     */
    public function put(array $m, string $c): void;

    /**
     * @param Map<string, int> $m
     * @param Class<T> $c
     */
    public function classes(array $m, string $c): void;
}
