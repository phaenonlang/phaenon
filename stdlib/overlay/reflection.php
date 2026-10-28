<?php

/**
 * Phaenon overlay: generic signatures for the reflection API and enums.
 * A class declared here patches the php-src stub of the same name: the
 * class docblock and the methods listed replace the stub's, everything
 * else stays as the stub declares it.
 */

/**
 * @template T of object
 */
class ReflectionClass
{
    /**
     * @param T|class-string<T> $objectOrClass
     */
    public function __construct(object|string $objectOrClass) {}

    /** @return class-string<T> */
    public function getName(): string {}

    /** @return T */
    public function newInstance(mixed ...$args): object {}

    /** @return T */
    public function newInstanceWithoutConstructor(): object {}

    /** @return T */
    public function newInstanceArgs(array $args = []): ?object {}

    /** @return ReflectionClass<object>|false */
    public function getParentClass(): ReflectionClass|false {}

    /** @return list<ReflectionMethod> */
    public function getMethods(?int $filter = null): array {}

    /** @return list<ReflectionProperty> */
    public function getProperties(?int $filter = null): array {}

    /** @return array<string, mixed> */
    public function getConstants(?int $filter = null): array {}

    /** @return list<ReflectionClassConstant> */
    public function getReflectionConstants(?int $filter = null): array {}

    /** @return list<class-string> */
    public function getInterfaceNames(): array {}

    /** @return array<string, ReflectionClass<object>> */
    public function getInterfaces(): array {}

    /** @return list<class-string> */
    public function getTraitNames(): array {}

    /** @return array<string, ReflectionClass<object>> */
    public function getTraits(): array {}

    /**
     * @template A of object
     * @param class-string<A>|null $name
     * @return list<ReflectionAttribute<A>>
     */
    public function getAttributes(?string $name = null, int $flags = 0): array {}

    /** @return array<string, mixed> */
    public function getStaticProperties(): ?array {}

    /** @return array<string, mixed> */
    public function getDefaultProperties(): array {}

    /** @param class-string|ReflectionClass<object> $class */
    public function isSubclassOf(ReflectionClass|string $class): bool {}

    /** @param class-string|ReflectionClass<object> $interface */
    public function implementsInterface(ReflectionClass|string $interface): bool {}
}

/**
 * @template T of object
 * @extends ReflectionClass<T>
 */
class ReflectionObject extends ReflectionClass
{
    /** @param T $object */
    public function __construct(object $object) {}
}

/**
 * @template T of UnitEnum
 * @extends ReflectionClass<T>
 */
class ReflectionEnum extends ReflectionClass
{
    /** @param T|class-string<T> $objectOrClass */
    public function __construct(object|string $objectOrClass) {}

    /** @return list<ReflectionEnumUnitCase> */
    public function getCases(): array {}
}

/**
 * @template T of object
 */
class ReflectionAttribute
{
    /** @return class-string<T> */
    public function getName(): string {}

    /** @return array<int|string, mixed> */
    public function getArguments(): array {}

    /** @return T */
    public function newInstance(): object {}
}

abstract class ReflectionFunctionAbstract
{
    /**
     * @template A of object
     * @param class-string<A>|null $name
     * @return list<ReflectionAttribute<A>>
     */
    public function getAttributes(?string $name = null, int $flags = 0): array {}

    /** @return list<ReflectionParameter> */
    public function getParameters(): array {}
}

class ReflectionProperty
{
    /**
     * @template A of object
     * @param class-string<A>|null $name
     * @return list<ReflectionAttribute<A>>
     */
    public function getAttributes(?string $name = null, int $flags = 0): array {}

    /** @return ReflectionClass<object> */
    public function getDeclaringClass(): ReflectionClass {}
}

class ReflectionMethod
{
    /**
     * @template A of object
     * @param class-string<A>|null $name
     * @return list<ReflectionAttribute<A>>
     */
    public function getAttributes(?string $name = null, int $flags = 0): array {}

    /** @return ReflectionClass<object> */
    public function getDeclaringClass(): ReflectionClass {}

    /** @return list<ReflectionParameter> */
    public function getParameters(): array {}
}

class ReflectionFunction
{
    /**
     * @template A of object
     * @param class-string<A>|null $name
     * @return list<ReflectionAttribute<A>>
     */
    public function getAttributes(?string $name = null, int $flags = 0): array {}

    /** @return list<ReflectionParameter> */
    public function getParameters(): array {}
}

class ReflectionParameter
{
    /**
     * @template A of object
     * @param class-string<A>|null $name
     * @return list<ReflectionAttribute<A>>
     */
    public function getAttributes(?string $name = null, int $flags = 0): array {}
}

class ReflectionClassConstant
{
    /**
     * @template A of object
     * @param class-string<A>|null $name
     * @return list<ReflectionAttribute<A>>
     */
    public function getAttributes(?string $name = null, int $flags = 0): array {}

    /** @return ReflectionClass<object> */
    public function getDeclaringClass(): ReflectionClass {}
}

interface UnitEnum
{
    /** @return list<static> */
    public static function cases(): array;
}

interface BackedEnum extends UnitEnum
{
    public static function from(int|string $value): static;

    public static function tryFrom(int|string $value): ?static;
}
