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
    public function getIterator(): Traversable {}
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @extends Iterator<TKey, TValue>
 */
interface SeekableIterator extends Iterator
{
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @extends Iterator<TKey, TValue>
 */
interface RecursiveIterator extends Iterator
{
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @extends Iterator<TKey, TValue>
 */
interface OuterIterator extends Iterator
{
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
     * @return TValue|null
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
 * @implements SeekableIterator<TKey, TValue>
 * @implements ArrayAccess<TKey, TValue>
 */
class ArrayIterator implements SeekableIterator, ArrayAccess, Serializable, Countable
{
    /** @param array<TKey, TValue>|object $array */
    public function __construct(object|array $array = [], int $flags = 0) {}

    // Iterator's TValue and TKey, past the end too: PHPStan's stub declares
    // neither method here, so they are Iterator's there, and a subclass that
    // repeats PHP's signature is compared with Iterator's.
    /** @return TValue */
    public function current(): mixed {}

    /** @return TKey */
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
 * @template TKey of array-key
 * @template TValue
 * @extends ArrayIterator<TKey, TValue>
 * @implements RecursiveIterator<TKey, TValue>
 */
class RecursiveArrayIterator extends ArrayIterator implements RecursiveIterator
{
}

/**
 * Generator<TKey, TValue, TSend, TReturn>: send and return default to mixed,
 * so Generator<K, V> names the key and the value, as PHPStan reads it; a
 * single argument is the value (the resolver's rule for the iterator family).
 *
 * @template-covariant TKey
 * @template-covariant TValue
 * @template TSend = mixed
 * @template-covariant TReturn = mixed
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

/**
 * The outer iterator forwards unknown method calls to the inner one
 * (`getSubPathname()` of a RecursiveDirectoryIterator); the stub does not
 * declare the forwarding. T is the iterator it walks, as PHPStan declares
 * it.
 *
 * @template T of RecursiveIterator|IteratorAggregate
 */
class RecursiveIteratorIterator implements OuterIterator
{
    /** @param T $iterator */
    public function __construct(Traversable $iterator, int $mode = RecursiveIteratorIterator::LEAVES_ONLY, int $flags = 0) {}

    // The directory iterator's path methods, forwarded to the inner iterator
    // by __call(): typed here because that is what a recursive walk reads.
    public function getSubPath(): string {}

    public function getSubPathname(): string {}

    public function __call(string $name, array $arguments): mixed {}
}

/**
 * The iterators that wrap another, generic as PHPStan declares them: the
 * key and the value they yield, covariant where PHPStan has them so, and
 * the iterator they wrap, which their constructor takes.
 *
 * @template-covariant TKey
 * @template-covariant TValue
 * @template TIterator of Traversable<TKey, TValue>
 * @implements OuterIterator<TKey, TValue>
 */
class IteratorIterator implements OuterIterator
{
    /** @param TIterator $iterator */
    public function __construct(Traversable $iterator, ?string $class = null) {}
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @template TIterator of Traversable<TKey, TValue>
 * @extends IteratorIterator<TKey, TValue, TIterator>
 */
abstract class FilterIterator extends IteratorIterator
{
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @template TIterator of Traversable<TKey, TValue>
 * @extends FilterIterator<TKey, TValue, TIterator>
 */
class CallbackFilterIterator extends FilterIterator
{
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @template TIterator of Traversable<TKey, TValue>
 * @extends CallbackFilterIterator<TKey, TValue, TIterator>
 * @implements RecursiveIterator<TKey, TValue>
 */
class RecursiveCallbackFilterIterator extends CallbackFilterIterator implements RecursiveIterator
{
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @template TIterator of RecursiveIterator<TKey, TValue>
 * @extends FilterIterator<TKey, TValue, TIterator>
 * @implements RecursiveIterator<TKey, TValue>
 */
abstract class RecursiveFilterIterator extends FilterIterator implements RecursiveIterator
{
    /** @param TIterator $iterator */
    public function __construct(RecursiveIterator $iterator) {}
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @template TIterator of RecursiveIterator<TKey, TValue>
 * @extends RecursiveFilterIterator<TKey, TValue, TIterator>
 */
class ParentIterator extends RecursiveFilterIterator
{
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @template TIterator of Iterator<TKey, TValue>
 * @implements OuterIterator<TKey, TValue>
 * @extends IteratorIterator<TKey, TValue, TIterator>
 */
class LimitIterator extends IteratorIterator
{
    /** @param TIterator $iterator */
    public function __construct(Iterator $iterator, int $offset = 0, int $limit = -1) {}
}

/**
 * @template TKey
 * @template TValue
 * @template TIterator of Iterator<TKey, TValue>
 * @implements OuterIterator<TKey, TValue>
 * @implements ArrayAccess<TKey, TValue>
 * @extends IteratorIterator<TKey, TValue, TIterator>
 */
class CachingIterator extends IteratorIterator implements ArrayAccess, Countable, Stringable
{
    /** @param TIterator $iterator */
    public function __construct(Iterator $iterator, int $flags = CachingIterator::CALL_TOSTRING) {}
}

/**
 * @template TKey
 * @template TValue
 * @template TIterator of Iterator<TKey, TValue>
 * @extends CachingIterator<TKey, TValue, TIterator>
 * @implements RecursiveIterator<TKey, TValue>
 */
class RecursiveCachingIterator extends CachingIterator implements RecursiveIterator
{
    /** @param TIterator $iterator */
    public function __construct(Iterator $iterator, int $flags = RecursiveCachingIterator::CALL_TOSTRING) {}
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @template TIterator of Iterator<TKey, TValue>
 * @extends IteratorIterator<TKey, TValue, TIterator>
 */
class NoRewindIterator extends IteratorIterator
{
    /** @param TIterator $iterator */
    public function __construct(Iterator $iterator) {}
}

/**
 * @template TKey
 * @template TValue
 * @template TIterator of Iterator<TKey, TValue>
 * @extends IteratorIterator<TKey, TValue, TIterator>
 */
class AppendIterator extends IteratorIterator
{
    /** @param TIterator $iterator */
    public function append(Iterator $iterator): void {}
}

/**
 * @template-covariant TKey
 * @template-covariant TValue
 * @template TIterator of Iterator<TKey, TValue>
 * @extends IteratorIterator<TKey, TValue, TIterator>
 */
class InfiniteIterator extends IteratorIterator
{
    /** @param TIterator $iterator */
    public function __construct(Iterator $iterator) {}
}

/**
 * @template TKey
 * @template TValue
 * @template TIterator of Traversable<TKey, TValue>
 * @extends FilterIterator<TKey, TValue, TIterator>
 */
class RegexIterator extends FilterIterator
{
    /** @param Iterator<TKey, TValue> $iterator */
    public function __construct(Iterator $iterator, string $pattern, int $mode = RegexIterator::MATCH, int $flags = 0, int $pregFlags = 0) {}
}

/**
 * @template TKey
 * @template TValue
 * @template TIterator of RecursiveIterator<TKey, TValue>
 * @extends RegexIterator<TKey, TValue, TIterator>
 * @implements RecursiveIterator<TKey, TValue>
 */
class RecursiveRegexIterator extends RegexIterator implements RecursiveIterator
{
    /** @param TIterator $iterator */
    public function __construct(RecursiveIterator $iterator, string $pattern, int $mode = RecursiveRegexIterator::MATCH, int $flags = 0, int $pregFlags = 0) {}
}

/**
 * The SPL's data structures, generic as PHPStan declares them.
 *
 * @template TValue
 * @implements Iterator<int, TValue>
 * @implements ArrayAccess<int, TValue>
 */
class SplDoublyLinkedList implements Iterator, Countable, ArrayAccess, Serializable
{
    // An index outside the list throws: TValue, not ArrayAccess's
    // TValue|null, as PHPStan's stub says.
    /**
     * @param int $index
     * @return TValue
     */
    public function offsetGet($index): mixed {}
}

/**
 * @template TValue
 * @extends SplDoublyLinkedList<TValue>
 */
class SplQueue extends SplDoublyLinkedList
{
}

/**
 * @template TPriority
 * @template TValue
 * @implements Iterator<int, TValue>
 */
class SplPriorityQueue implements Iterator, Countable
{
    // The value, its priority or both, as setExtractFlags() chose: PHP
    // returns array{priority, data} under EXTR_BOTH.
    /** @return TPriority|TValue|array{priority: TPriority, data: TValue} */
    public function top(): mixed {}

    /** @return TPriority|TValue|array{priority: TPriority, data: TValue} */
    public function extract(): mixed {}

    /** @return TPriority|TValue|array{priority: TPriority, data: TValue} */
    public function current(): mixed {}
}

/**
 * @template TObject of object
 * @template TData
 * @implements Iterator<int, TObject>
 * @implements SeekableIterator<int, TObject>
 * @implements ArrayAccess<TObject, TData>
 */
class SplObjectStorage implements Countable, SeekableIterator, Serializable, ArrayAccess
{
    // Iterator's TValue, which is TObject here, as PHPStan reads it: a
    // subclass that repeats PHP's `current(): object` is compared with it.
    /** @return TObject */
    public function current(): object {}

    // An object not in the storage throws: TData, not ArrayAccess's
    // TData|null, as PHPStan's stub says.
    /**
     * @param object $object
     * @return TData
     */
    public function offsetGet($object): mixed {}
}

/**
 * @template TValue
 * @implements Iterator<int, TValue|null>
 * @implements IteratorAggregate<int, TValue|null>
 * @implements ArrayAccess<int, TValue|null>
 */
class SplFixedArray implements IteratorAggregate, ArrayAccess, Countable, JsonSerializable
{
}

/**
 * WeakReference<T of object>, covariant, as PHPStan declares it.
 *
 * @template-covariant T of object
 */
final class WeakReference
{
    /**
     * @template TIn of object
     * @param TIn $object
     * @return WeakReference<TIn>
     */
    public static function create(object $object): WeakReference {}

    /** @return ?T */
    public function get(): ?object {}
}

/**
 * WeakMap<TKey of object, TValue>, as PHPStan declares it.
 *
 * @template TKey of object
 * @template TValue
 * @implements IteratorAggregate<TKey, TValue>
 * @implements ArrayAccess<TKey, TValue>
 */
final class WeakMap implements ArrayAccess, Countable, IteratorAggregate
{
    /**
     * @param TKey $object
     * @return TValue
     */
    public function offsetGet($object): mixed {}

    /**
     * @param TKey $object
     * @param TValue $value
     */
    public function offsetSet($object, mixed $value): void {}

    /** @param TKey $object */
    public function offsetExists($object): bool {}

    /** @param TKey $object */
    public function offsetUnset($object): void {}

    /** @return Iterator<TKey, TValue> */
    public function getIterator(): Iterator {}
}

/**
 * An archive's entry is a PharFileInfo (chmod(), compress(), …), not the
 * SplFileInfo the php-src stub declares; PHPStan's signature map says so.
 */
class Phar extends RecursiveDirectoryIterator implements Countable, ArrayAccess
{
    public function offsetGet($localName): PharFileInfo {}
}

class PharData extends RecursiveDirectoryIterator implements Countable, ArrayAccess
{
    public function offsetGet($localName): PharFileInfo {}
}
