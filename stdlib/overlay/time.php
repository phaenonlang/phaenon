<?php

/**
 * Phaenon overlay: time functions whose return type follows an argument.
 */

/**
 * @return ($as_float is true ? float : string)
 */
function microtime(bool $as_float = false): string|float {}

/**
 * @return ($as_number is true ? int : array{int, int})
 */
function hrtime(bool $as_number = false): array|int|float|false {}

/**
 * The parse report, false when nothing was parsed yet (PHPStan's shape).
 *
 * @return array{warning_count: int<0, max>, warnings: array<int, string>, error_count: int<0, max>, errors: array<int, string>}|false
 */
function date_get_last_errors(): array|false {}

// getTimezone() returns false only for an object created without its
// constructor having run; PHPStan types it DateTimeZone, and so does this.
interface DateTimeInterface
{
    public function getTimezone(): DateTimeZone {}
}

class DateTime implements DateTimeInterface
{
    public function getTimezone(): DateTimeZone {}

    /**
     * @return array{warning_count: int<0, max>, warnings: array<int, string>, error_count: int<0, max>, errors: array<int, string>}|false
     */
    public static function getLastErrors(): array|false {}
}

class DateTimeImmutable implements DateTimeInterface
{
    public function getTimezone(): DateTimeZone {}

    /**
     * @return array{warning_count: int<0, max>, warnings: array<int, string>, error_count: int<0, max>, errors: array<int, string>}|false
     */
    public static function getLastErrors(): array|false {}
}
