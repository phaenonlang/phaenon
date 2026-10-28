<?php

namespace PHPUnit\Framework;

/**
 * Phaenon overlay: the PHPUnit assertions that narrow their argument, as
 * phpstan/phpunit describes them. Applied as a patch when the vendor class
 * is loaded.
 */
abstract class Assert
{
    /** @phpstan-assert true $condition */
    public static function assertTrue(mixed $condition, string $message = ''): void {}

    /** @phpstan-assert false $condition */
    public static function assertFalse(mixed $condition, string $message = ''): void {}

    /** @phpstan-assert null $actual */
    public static function assertNull(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert !null $actual */
    public static function assertNotNull(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert !false $condition */
    public static function assertNotFalse(mixed $condition, string $message = ''): void {}

    /** @phpstan-assert !true $condition */
    public static function assertNotTrue(mixed $condition, string $message = ''): void {}

    /**
     * @template T of object
     * @param class-string<T> $expected
     * @phpstan-assert T $actual
     */
    public static function assertInstanceOf(string $expected, mixed $actual, string $message = ''): void {}

    /**
     * @template T of object
     * @param class-string<T> $expected
     * @phpstan-assert !T $actual
     */
    public static function assertNotInstanceOf(string $expected, mixed $actual, string $message = ''): void {}

    /** @phpstan-assert array $actual */
    public static function assertIsArray(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert bool $actual */
    public static function assertIsBool(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert float $actual */
    public static function assertIsFloat(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert int $actual */
    public static function assertIsInt(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert numeric $actual */
    public static function assertIsNumeric(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert object $actual */
    public static function assertIsObject(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert resource $actual */
    public static function assertIsResource(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert string $actual */
    public static function assertIsString(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert scalar $actual */
    public static function assertIsScalar(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert callable $actual */
    public static function assertIsCallable(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert iterable $actual */
    public static function assertIsIterable(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert !array $actual */
    public static function assertIsNotArray(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert !string $actual */
    public static function assertIsNotString(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert !int $actual */
    public static function assertIsNotInt(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert !object $actual */
    public static function assertIsNotObject(mixed $actual, string $message = ''): void {}

    /** @phpstan-assert array|\Countable $haystack */
    public static function assertCount(int $expectedCount, \Countable|iterable $haystack, string $message = ''): void {}
}
