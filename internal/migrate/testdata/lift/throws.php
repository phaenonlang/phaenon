<?php

declare(strict_types=1);

namespace App;

use InvalidArgumentException;

final class NotFound extends \RuntimeException
{
}

interface Store
{
    /**
     * @throws NotFound
     */
    public function get(int $id): string;

    /**
     * Removes the entry.
     *
     * @throws NotFound
     * @throws \RuntimeException|\LogicException
     */
    public function remove(int $id): void;
}

/**
 * @template T of object
 */
final class MemoryStore implements Store
{
    /** @var array<int, string> */
    private array $items = [];

    /**
     * @throws NotFound
     */
    public function get(int $id): string
    {
        return $this->items[$id] ?? throw new NotFound((string) $id);
    }

    /**
     * Removes the entry.
     *
     * @throws NotFound
     * @throws \RuntimeException|\LogicException
     */
    public function remove(int $id): void
    {
        unset($this->items[$id]);
    }

    /**
     * Stores a value.
     *
     * @throws InvalidArgumentException when the value is empty
     * @throws \LengthException
     */
    public function put(int $id, string $value)
    {
        $this->items[$id] = $value;
    }

    /**
     * @throws void
     */
    public function count(): int
    {
        return \count($this->items);
    }

    /**
     * @param T $item
     *
     * @throws T
     */
    public function rethrow(object $item): void
    {
    }

    /**
     * @template E of \Throwable
     *
     * @param E $e
     *
     * @throws E
     */
    public function raise(\Throwable $e): never
    {
        throw $e;
    }

    /**
     * @throws NotFound
     * @throws NotFound
     */
    public function twice(int $id): string
    {
        return $this->get($id);
    }

    /**
     * @phpstan-throws NotFound
     */
    public function prefixed(int $id): string
    {
        return $this->get($id);
    }
}

/**
 * @throws NotFound
 */
function load(Store $store, int $id): string
{
    return $store->get($id);
}

/**
 * @throws \LogicException
 */
function untyped($x)
{
    throw new \LogicException((string) $x);
}
