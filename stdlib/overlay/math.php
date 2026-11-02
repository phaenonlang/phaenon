<?php

/**
 * Phaenon overlay: arithmetic whose result is one of its arguments.
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
