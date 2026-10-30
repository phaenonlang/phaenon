<?php

/**
 * Phaenon overlay: string functions whose result PHPStan knows better than
 * their signature says.
 */

/**
 * @return ($flags is 0 ? list<string>|false : list<string|array{string, int}>|false)
 */
function preg_split(string $pattern, string $subject, int $limit = -1, int $flags = 0): array|false {}

/**
 * @return ($subject is array ? array<string> : string)
 */
function str_replace(array|string $search, array|string $replace, string|array $subject, &$count = null): string|array {}

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
