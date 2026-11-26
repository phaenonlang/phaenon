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
 * indexes a pair. An object is no number: PHP warns "Object of class X
 * could not be converted to int" and makes it 1, a Stringable one too,
 * which PHPStan's function map leaves out (GMP and SimpleXMLElement
 * convert).
 *
 * @param int|float|string|bool|array<mixed>|resource|\GMP|\SimpleXMLElement|null $value
 * @return ($value is bool|null ? int<0, 1> : int)
 */
function intval(mixed $value, int $base = 10): int {}

/**
 * Numbers, as PHPStan's function map has them. PHP converts a numeric
 * string, a bool and null as `**` does, and takes a GMP, whose power is
 * written `$g ** $e`; PHPStan reports each, and so does the checker. The
 * result is the type of `$num ** $exponent` (the checker's specialReturn).
 *
 * @param int|float $num
 * @param int|float $exponent
 * @return int|float
 */
function pow(mixed $num, mixed $exponent): int|float|object {}

/**
 * An object is no number, as for intval().
 *
 * @param int|float|string|bool|array<mixed>|resource|\GMP|\SimpleXMLElement|null $value
 */
function floatval(mixed $value): float {}

/**
 * @param int|float|string|bool|array<mixed>|resource|\GMP|\SimpleXMLElement|null $value
 */
#[\Deprecated(message: "use floatval() instead", since: "8.6")]
function doubleval(mixed $value): float {}
