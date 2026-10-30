<?php

/**
 * Phaenon overlay: the generic shapes of PHP's iteration and array-access
 * interfaces, as PHPStan declares them. A class patched here keeps every
 * method the php-src stub declares; only the docblocks and the methods
 * listed are replaced.
 */

/**
 * @template-covariant TKey
 * @template-covariant TValue
 */
interface Traversable
{
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @extends Traversable<TKey, TValue>
 */
interface Iterator extends Traversable
{
    /** @return TValue */
    public function current(): mixed {}

    /** @return TKey */
    public function key(): mixed {}
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @extends Traversable<TKey, TValue>
 */
interface IteratorAggregate extends Traversable
{
    /** @return Traversable<TKey, TValue> */
    public function getIterator(): Iterator {}
}

/**
 * @template TKey
 * @template TValue
 */
interface ArrayAccess
{
    /** @param TKey $offset */
    public function offsetExists(mixed $offset): bool {}

    /**
     * @param TKey $offset
     * @return TValue
     */
    public function offsetGet(mixed $offset): mixed {}

    /**
     * @param TKey|null $offset
     * @param TValue $value
     */
    public function offsetSet(mixed $offset, mixed $value): void {}

    /** @param TKey $offset */
    public function offsetUnset(mixed $offset): void {}
}

/**
 * @template TKey of array-key
 * @template TValue
 * @implements IteratorAggregate<TKey, TValue>
 * @implements ArrayAccess<TKey, TValue>
 */
class ArrayObject implements IteratorAggregate, ArrayAccess, Serializable, Countable
{
    /** @param array<TKey, TValue>|object $array */
    public function __construct(object|array $array = [], int $flags = 0, string $iteratorClass = ArrayIterator::class) {}

    /** @param TKey $key */
    public function offsetExists($key): bool {}

    /**
     * @param TKey $key
     * @return TValue
     */
    public function offsetGet($key): mixed {}

    /**
     * @param TKey|null $key
     * @param TValue $value
     */
    public function offsetSet($key, mixed $value): void {}

    /** @param TKey $key */
    public function offsetUnset($key): void {}

    /** @param TValue $value */
    public function append(mixed $value): void {}

    /** @return array<TKey, TValue> */
    public function getArrayCopy(): array {}

    /** @return Iterator<TKey, TValue> */
    public function getIterator(): Iterator {}
}

/**
 * @template TKey of array-key
 * @template TValue
 * @implements Iterator<TKey, TValue>
 * @implements ArrayAccess<TKey, TValue>
 */
class ArrayIterator implements SeekableIterator, ArrayAccess, Serializable, Countable
{
    /** @param array<TKey, TValue>|object $array */
    public function __construct(object|array $array = [], int $flags = 0) {}

    /** @return TValue|null */
    public function current(): mixed {}

    /** @return TKey|null */
    public function key(): string|int|null {}

    /**
     * @param TKey $key
     * @return TValue
     */
    public function offsetGet($key): mixed {}

    /** @return array<TKey, TValue> */
    public function getArrayCopy(): array {}
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @template TSend
 * @template-covariant TReturn
 * @implements Iterator<TKey, TValue>
 */
final class Generator implements Iterator
{
    /** @return TValue */
    public function current(): mixed {}

    /** @return TKey */
    public function key(): mixed {}

    /**
     * @param TSend $value
     * @return TValue|null
     */
    public function send(mixed $value): mixed {}

    /** @return TReturn */
    public function getReturn(): mixed {}
}
