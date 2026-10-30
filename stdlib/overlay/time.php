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

// getTimezone() returns false only for an object created without its
// constructor having run; PHPStan types it DateTimeZone, and so does this.
interface DateTimeInterface
{
    public function getTimezone(): DateTimeZone {}
}

class DateTime implements DateTimeInterface
{
    public function getTimezone(): DateTimeZone {}
}

class DateTimeImmutable implements DateTimeInterface
{
    public function getTimezone(): DateTimeZone {}
}
