<?php

/**
 * Phaenon overlay: what preg_match() and preg_match_all() leave in $matches.
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
 * @return ($subject is array ? array<int|string, string>|null : string|null)
 */
function preg_replace(string|array $pattern, string|array $replacement, string|array $subject, int $limit = -1, &$count = null): string|array|null {}

/**
 * @return ($subject is array ? array<int|string, string>|null : string|null)
 */
function preg_replace_callback(string|array $pattern, callable $callback, string|array $subject, int $limit = -1, &$count = null, int $flags = 0): string|array|null {}
