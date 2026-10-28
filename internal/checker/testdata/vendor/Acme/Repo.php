<?php

namespace Acme;

/**
 * @template T of object
 */
class Repo
{
    /** @var list<T> */
    private array $items = [];

    /**
     * @param class-string<T> $class
     */
    public function __construct(private string $class)
    {
    }

    /** @return T|null */
    public function find(int $id): ?object
    {
        return $this->items[$id] ?? null;
    }

    /** @return list<T> */
    public function all(): array
    {
        return $this->items;
    }

    /**
     * @template R
     * @param callable(T): R $fn
     * @return list<R>
     */
    public function map(callable $fn): array
    {
        return array_map($fn, $this->items);
    }

    /** @param T $item */
    public function add(object $item): static
    {
        $this->items[] = $item;

        return $this;
    }

    /**
     * @phpstan-assert-if-true T $value
     */
    public function owns(mixed $value): bool
    {
        return $value instanceof $this->class;
    }
}
