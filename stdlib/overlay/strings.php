<?php

/**
 * Phaenon overlay: string functions whose result PHPStan knows better than
 * their signature says.
 */

/**
 * Only PREG_SPLIT_OFFSET_CAPTURE (4) makes the pieces [string, int] pairs;
 * PREG_SPLIT_NO_EMPTY (1) and PREG_SPLIT_DELIM_CAPTURE (2) keep them strings.
 *
 * @return ($flags is 0|1|2|3 ? list<string>|false : list<string|array{string, int}>|false)
 */
function preg_split(string $pattern, string $subject, int $limit = -1, int $flags = 0): array|false {}

/**
 * @param-out int $count
 * @return ($subject is array ? array<string> : string)
 */
function str_replace(array|string $search, array|string $replace, string|array $subject, &$count = null): string|array {}

/**
 * @param-out int $count
 * @return ($subject is array ? array<string> : string)
 */
function str_ireplace(array|string $search, array|string $replace, string|array $subject, &$count = null): string|array {}

/**
 * @return ($string is array ? array<string> : string)
 */
function substr_replace(array|string $string, array|string $replace, array|int $offset, array|int|null $length = null): string|array {}

/**
 * @return list<string>
 */
function mb_str_split(string $string, int $length = 1, ?string $encoding = null): array {}

/**
 * @return list<string>|false
 */
function grapheme_str_split(string $string, int $length = 1): array|false {}

/**
 * @return ($substitute_character is null ? string|int : bool)
 */
function mb_substitute_character(string|int|null $substitute_character = null): string|int|bool {}

/**
 * One component asked for is a string, or the port (PHP_URL_PORT, 2) an
 * int; null when the URL has none, false when it does not parse.
 *
 * @return ($component is 2 ? int<0, 65535>|null|false : ($component is int<0, max> ? string|null|false : array{scheme?: string, host?: string, port?: int<0, 65535>, user?: string, pass?: string, path?: string, query?: string, fragment?: string}|false))
 */
function parse_url(string $url, int $component = -1): int|string|array|null|false {}

/**
 * With an operator the comparison is answered; without one it is -1, 0 or 1.
 *
 * @return ($operator is null ? -1|0|1 : bool)
 */
function version_compare(string $version1, string $version2, ?string $operator = null): int|bool {}

/**
 * @return ($return is true ? string : null)
 */
function var_export(mixed $value, bool $return = false): ?string {}
