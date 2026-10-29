<?php

/**
 * Phaenon overlay: a class-existence check proves a class-string, as PHPStan knows.
 */

/**
 * @phpstan-assert-if-true class-string $class
 */
function class_exists(string $class, bool $autoload = true): bool {}

/**
 * @phpstan-assert-if-true class-string $interface
 */
function interface_exists(string $interface, bool $autoload = true): bool {}

/**
 * @phpstan-assert-if-true class-string<\UnitEnum> $enum
 */
function enum_exists(string $enum, bool $autoload = true): bool {}

/**
 * @phpstan-assert-if-true class-string $trait
 */
function trait_exists(string $trait, bool $autoload = true): bool {}
