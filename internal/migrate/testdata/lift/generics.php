<?php

declare(strict_types=1);

namespace App;

use App\Model\Row;

/**
 * A cache of reflections, keyed by class name.
 *
 * @template ClassType of object
 * @template-covariant EnumType of \UnitEnum
 * @implements ReflectorInterface<ClassType, EnumType>
 */
final class CachedReflector implements \Countable, ReflectorInterface
{
    /**
     * @var array<class-string<ClassType>, \ReflectionClass<ClassType>>
     */
    private array $cache = [];

    /** @var list<string> */
    private array $names = [];

    /** @var array<string, int> the hit counts, by name */
    private array $hits = [];

    /** @var int|string */
    private $untyped = 0;

    /**
     * @var array{id: int}
     */
    private array $shape = ['id' => 1];

    /**
     * @template T of object
     * @param class-string<T> $attributeType FQN of an attribute class
     * @param class-string $className FQN of a class we look for attributes in
     * @return T[]
     */
    public function allOfType(string $attributeType, string $className): array
    {
        return [];
    }

    /**
     * Finds the first one.
     *
     * @template T of object
     * @param class-string<T> $attributeType
     * @return T|null
     * @throws \ReflectionException
     */
    public function first(string $attributeType): object|null
    {
        return null;
    }

    /**
     * @param T|class-string<T> $target
     * @return T the mapped object
     * @template T of object
     */
    public function map(object $source, object|string $target): object
    {
        return $source;
    }

    /**
     * @param (\Closure(object, object): object)|null $mapping
     * @param \Closure(int): string $f
     * @param callable(int): string $g
     * @param int<0, max> $n
     * @param array{id: int} $row
     * @param non-empty-string ...$names
     * @param string|null $maybe
     */
    public function closures(?\Closure $mapping, \Closure $f, callable $g, int $n, array $row, string ...$names, ?string $maybe = null): void
    {
    }

    /**
     * @param list<int> $xs
     * @return list<int>
     */
    public function widened(array $xs): iterable
    {
        return $xs;
    }

    /**
     * @param \App\Model\Row $row
     * @param Row $other
     */
    public function qualified(Row $row, Row $other): void
    {
    }

    /**
     * @param int $n
     * @return iterable<string>
     */
    public function untypedParts($n)
    {
    }

    /**
     * @template ClassType of object
     * @param ReflectorInterface<ClassType, \UnitEnum> $reflector
     * @param string $name
     */
    public function __construct(private ReflectorInterface $reflector, string $name = '')
    {
    }

    /**
     * @return $this
     */
    public function fluent(): static
    {
        return $this;
    }

    /** @phpstan-param positive-int $n */
    public function prefixed(int $n): void
    {
    }

    public function count(): int
    {
        return 0;
    }
}

/**
 * @template T
 * @extends Base<T>
 */
abstract class Repo extends Base
{
}

/**
 * @template T of object
 * @param list<T> $items
 * @return T|null
 */
function firstOf(array $items): ?object
{
    return $items[0] ?? null;
}

/**
 * @param array{string, int} $pair
 * @param list<array{string, int}> $pairs
 * @param array{name: string, age: int} $named
 * @param array{string} $single
 * @return array{int, string}
 */
function tuples(array $pair, array $pairs, array $named, array $single): array
{
    return [$pair[1], $pair[0]];
}

/**
 * @param array<int, string> $map
 * @param non-empty-array<string, \stdClass> $objects
 * @param \Closure(int, int=): int $withOptional
 */
function keyed(array $map, array $objects, \Closure $withOptional): void
{
}

/**
 * A cluster reached over kubectl. PHPStan's `T...` and `T=` markers are
 * Phaenon's too; a parameter's name is dropped; a by-reference parameter
 * keeps its docblock.
 */
final class KubeAccess
{
    /** @var \Closure(string ...$args): array{int, string} */
    private \Closure $kubectl;
    /** @var \Closure(string $stdin, string ...$args): array{int, string} */
    private \Closure $kubectlStdin;
    /** @var \Closure(int &$n): void */
    private \Closure $byRef;

    /**
     * @param \Closure(string ...$args): array{int, string} $kubectl
     * @param (\Closure(string, int=): void)|null $log
     */
    public function __construct(\Closure $kubectl, ?\Closure $log = null)
    {
        $this->kubectl = $kubectl;
        $this->kubectlStdin = static fn (string $stdin, string ...$args) => [0, $stdin];
        $this->byRef = static function (int &$n): void {
            $n++;
        };
    }
}
