<?php

/**
 * Phaenon overlay: what preg_match() and preg_match_all() leave in $matches,
 * and what the replace functions leave in $count — the reference is written
 * on every call, so `$count = 0; preg_replace(…, $count)` leaves an int, not
 * the 0 it started as.
 */

/**
 * @param-out array<int|string, string> $matches
 */
function preg_match(string $pattern, string $subject, &$matches = null, int $flags = 0, int $offset = 0): int|false {}

/**
 * @param-out array<int|string, list<string>> $matches
 */
function preg_match_all(string $pattern, string $subject, &$matches = null, int $flags = 0, int $offset = 0): int|false {}

/**
 * @param-out int $count
 * @return ($subject is array ? array<int|string, string>|null : string|null)
 */
function preg_replace(string|array $pattern, string|array $replacement, string|array $subject, int $limit = -1, &$count = null): string|array|null {}

/**
 * The pieces; the checker refines them under PREG_SPLIT_NO_EMPTY and
 * PREG_SPLIT_OFFSET_CAPTURE from the flags written at the call.
 *
 * @return list<string>|false
 */
function preg_split(string $pattern, string $subject, int $limit = -1, int $flags = 0): array|false {}

/**
 * The callback receives the match the way preg_match() fills $matches.
 *
 * @param callable(array<int|string, string>): string $callback
 * @param-out int $count
 * @return ($subject is array ? array<int|string, string>|null : string|null)
 */
function preg_replace_callback(string|array $pattern, callable $callback, string|array $subject, int $limit = -1, &$count = null, int $flags = 0): string|array|null {}
