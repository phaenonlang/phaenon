<?php

/**
 * Phaenon overlay: arithmetic whose result is one of its arguments, or a
 * range its argument's type decides.
 */

/**
 * The value, or the bound it lies beyond (PHP 8.6). PHP compares the three
 * with its comparison operators, so any comparable values work, and returns
 * one of them unchanged: the result has the type of all three
 * (design/php-versions.md §6). clamp(1.5, 0, 1) is the int 1.
 *
 * @template T
 * @param T $value
 * @param T $min
 * @param T $max
 * @return T
 */
function clamp(mixed $value, mixed $min, mixed $max): mixed {}

/**
 * A bool is 0 or 1, and null is 0, as PHPStan has it: the int of a flag
 * indexes a pair.
 *
 * @return ($value is bool|null ? int<0, 1> : int)
 */
function intval(mixed $value, int $base = 10): int {}
