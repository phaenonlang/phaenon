<?php

namespace Phaenon;

/**
 * Phaenon overlay: the runtime type descriptor shipped as phaenon/runtime.
 * A reified type parameter is carried by one of these at runtime; `is` and
 * `as` on structural types compile to its methods.
 *
 * @template-covariant T
 */
final class Type
{
    /**
     * @template U of object
     * @param class-string<U> $class
     * @param Type<mixed> ...$args
     * @return Type<U>
     */
    public static function of(string $class, Type ...$args): Type {}

    /** @return Type<int> */
    public static function int(): Type {}

    /** @return Type<float> */
    public static function float(): Type {}

    /** @return Type<string> */
    public static function string(): Type {}

    /** @return Type<bool> */
    public static function bool(): Type {}

    /** @return Type<null> */
    public static function null(): Type {}

    /** @return Type<mixed> */
    public static function mixed(): Type {}

    /** @return Type<object> */
    public static function object(): Type {}

    /** @return Type<array<mixed>> */
    public static function array(): Type {}

    /** @return Type<iterable<mixed>> */
    public static function iterable(): Type {}

    /** @return Type<callable> */
    public static function callable(): Type {}

    /**
     * @template V
     * @param Type<V> $element
     * @return Type<list<V>>
     */
    public static function list(Type $element): Type {}

    /**
     * @template K of array-key
     * @template V
     * @param Type<K> $key
     * @param Type<V> $value
     * @return Type<array<K, V>>
     */
    public static function map(Type $key, Type $value): Type {}

    /**
     * @template V
     * @param Type<V> $type
     * @return Type<V|null>
     */
    public static function nullable(Type $type): Type {}

    /**
     * @param Type<mixed> ...$members
     * @return Type<mixed>
     */
    public static function union(Type ...$members): Type {}

    /**
     * @param Type<mixed> ...$members
     * @return Type<mixed>
     */
    public static function intersection(Type ...$members): Type {}

    /**
     * @template V of scalar
     * @param V $value
     * @return Type<V>
     */
    public static function literal(mixed $value): Type {}

    /**
     * @template U of object
     * @param Type<U> $of
     * @return Type<class-string<U>>
     */
    public static function classString(Type $of): Type {}

    /** @return Type<resource> */
    public static function resource(): Type {}

    /** @phpstan-assert-if-true T $value */
    public function check(mixed $value): bool {}

    /**
     * @return T
     * @throws \TypeError
     */
    public function assert(mixed $value): mixed {}

    /** @return T|null */
    public function orNull(mixed $value): mixed {}

    /** @return class-string<T> */
    public function className(): string {}

    public function name(): string {}

    /** @param Type<mixed> $other */
    public function equals(Type $other): bool {}

    /** @param Type<mixed> $other */
    public function isSubtypeOf(Type $other): bool {}
}
