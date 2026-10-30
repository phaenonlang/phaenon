<?php

/**
 * Phaenon overlay: refined signatures for the array functions. A function
 * declared here replaces the php-src stub of the same name; the docblock
 * types are the point, the native types mirror the stub.
 */

/**
 * @return int<0, max>
 */
function count(Countable|array $value, int $mode = COUNT_NORMAL): int {}

/**
 * @template K of array-key
 * @template V
 * @template R
 * @param callable(V): R $callback
 * @param array<K, V> $array
 * @return ($array is list ? list<R> : array<K, R>)
 */
function array_map(?callable $callback, array $array, array ...$arrays): array {}

/**
 * @phpstan-assert-if-true list<mixed> $array
 */
function array_is_list(array $array): bool {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @param (callable(V): bool)|(callable(V, K): bool)|(callable(K): bool)|null $callback
 * @return array<K, V>
 */
function array_filter(array $array, ?callable $callback = null, int $mode = 0): array {}

/**
 * @template K of array-key
 * @param array<K, mixed> $array
 * @return list<K>
 */
function array_keys(array $array, mixed $filter_value = UNKNOWN, bool $strict = false): array {}

/**
 * @template V
 * @param array<V> $array
 * @return list<V>
 */
function array_values(array $array): array {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> ...$arrays
 * @return ($arrays is list<list<V>> ? list<V> : array<K, V>)
 */
function array_merge(array ...$arrays): array {}

/**
 * @template V
 * @param array<V> $array
 * @return list<V>
 */
function array_slice(array $array, int $offset, ?int $length = null, bool $preserve_keys = false): array {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @return ($preserve_keys is true ? array<K, V> : ($array is list ? list<V> : array<K, V>))
 */
function array_reverse(array $array, bool $preserve_keys = false): array {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @return array<K, V>
 */
function array_unique(array $array, int $flags = SORT_STRING): array {}

/**
 * @template V
 * @param array<V> $array
 * @param-out ($array is list ? list<V> : array<V>) $array
 * @return ($array is non-empty-array ? V : V|null)
 */
function array_pop(array &$array): mixed {}

/**
 * @template V
 * @param array<V> $array
 * @param-out ($array is list ? list<V> : array<V>) $array
 * @return ($array is non-empty-array ? V : V|null)
 */
function array_shift(array &$array): mixed {}

/**
 * @template V
 * @param array<V>|object $array
 * @return V|false
 */
function reset(array|object &$array): mixed {}

/**
 * @template V
 * @param array<V>|object $array
 * @return V|false
 */
function end(array|object &$array): mixed {}

/**
 * @template V
 * @param array<V>|object $array
 * @return V|false
 */
function current(array|object $array): mixed {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @return K|null
 */
function key(array|object $array): int|string|null {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @return K|false
 */
function array_search(mixed $needle, array $haystack, bool $strict = false): int|string|false {}

/**
 * @template K of array-key
 * @template V
 * @param iterable<K, V> $iterator
 * @return ($preserve_keys is true ? array<K, V> : list<V>)
 */
function iterator_to_array(Traversable|array $iterator, bool $preserve_keys = true): array {}

/**
 * @template K of array-key
 * @template V
 * @param array<K> $keys
 * @param array<V> $values
 * @return array<K, V>
 */
function array_combine(array $keys, array $values): array {}

/**
 * @template K of array-key
 * @template V of array-key
 * @param array<K, V> $array
 * @return array<V, K>
 */
function array_flip(array $array): array {}

/**
 * @template V
 * @param V $value
 * @return list<V>
 */
function array_fill(int $start_index, int $count, mixed $value): array {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @param array<K, mixed> ...$arrays
 * @return array<K, V>
 */
function array_diff_key(array $array, array ...$arrays): array {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @param array<K, mixed> ...$arrays
 * @return array<K, V>
 */
function array_intersect_key(array $array, array ...$arrays): array {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @param array<V> ...$arrays
 * @return array<K, V>
 */
function array_diff(array $array, array ...$arrays): array {}

/**
 * @template V
 * @param array<V> $array
 * @return list<list<V>>
 */
function array_chunk(array $array, int $length, bool $preserve_keys = false): array {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @param callable(V, K): mixed $callback
 */
function array_walk(array|object &$array, callable $callback, mixed $arg = UNKNOWN): true {}

/**
 * @template V
 * @template R
 * @param array<V> $array
 * @param callable(R, V): R $callback
 * @param R $initial
 * @return R
 */
function array_reduce(array $array, callable $callback, mixed $initial = null): mixed {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @return ($array is non-empty-array ? K : K|null)
 */
function array_key_first(array $array): int|string|null {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @return ($array is non-empty-array ? K : K|null)
 */
function array_key_last(array $array): int|string|null {}

/**
 * @return ($limit is int<min, -1> ? list<string> : non-empty-list<string>)
 */
function explode(string $separator, string $string, int $limit = PHP_INT_MAX): array {}

/**
 * @return list<string>
 */
function str_split(string $string, int $length = 1): array {}

/**
 * @return array<string, mixed>
 */
function get_object_vars(object $object): array {}

/**
 * @param string|bytes $string
 * @return int<0, max>
 */
function strlen(string $string): int {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @param callable(V, K): bool $callback
 * @return V|null
 */
function array_find(array $array, callable $callback): mixed {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @param callable(V, K): bool $callback
 * @return K|null
 */
function array_find_key(array $array, callable $callback): mixed {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @param callable(V, K): bool $callback
 */
function array_any(array $array, callable $callback): bool {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @param callable(V, K): bool $callback
 */
function array_all(array $array, callable $callback): bool {}

/**
 * @template V
 * @param array<V> $array
 * @param-out list<V> $array
 */
function sort(array &$array, int $flags = SORT_REGULAR): true {}

/**
 * @template V
 * @param array<V> $array
 * @param-out list<V> $array
 */
function rsort(array &$array, int $flags = SORT_REGULAR): true {}

/**
 * @template V
 * @param array<V> $array
 * @param callable(V, V): int $callback
 * @param-out list<V> $array
 */
function usort(array &$array, callable $callback): true {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @param callable(V, V): int $callback
 * @param-out array<K, V> $array
 */
function uasort(array &$array, callable $callback): true {}

/**
 * @template K of array-key
 * @template V
 * @param array<K, V> $array
 * @param callable(K, K): int $callback
 * @param-out array<K, V> $array
 */
function uksort(array &$array, callable $callback): true {}

/**
 * @template K of array-key
 * @template V
 * @template X
 * @param array<K, V> $array
 * @param X ...$values
 * @param-out ($array is list ? non-empty-list<V|X> : non-empty-array<K, V|X>) $array
 */
function array_push(array &$array, mixed ...$values): int {}

/**
 * @template K of array-key
 * @template V
 * @template X
 * @param array<K, V> $array
 * @param X ...$values
 * @param-out ($array is list ? non-empty-list<V|X> : non-empty-array<K, V|X>) $array
 */
function array_unshift(array &$array, mixed ...$values): int {}
