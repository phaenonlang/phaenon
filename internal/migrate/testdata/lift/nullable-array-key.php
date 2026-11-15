<?php

declare(strict_types=1);

namespace Lift\NullableArrayKey;

/**
 * array-key is written as the union int|string, which `?` cannot prefix:
 * array-key|null is int|string|null wherever it stands, as ?array-key is.
 *
 * @implements \ArrayAccess<array-key, string>
 */
final class Bag implements \ArrayAccess
{
    /** @var array<array-key, string> */
    private array $items = [];

    /**
     * @param array-key $offset
     */
    public function offsetExists(mixed $offset): bool
    {
        return isset($this->items[$offset]);
    }

    /**
     * @param array-key $offset
     */
    public function offsetGet(mixed $offset): string
    {
        return $this->items[$offset];
    }

    /**
     * @param array-key|null $offset
     * @param string $value
     */
    public function offsetSet(mixed $offset, mixed $value): void
    {
        if ($offset === null) {
            $this->items[] = $value;
        } else {
            $this->items[$offset] = $value;
        }
    }

    /**
     * @param array-key $offset
     */
    public function offsetUnset(mixed $offset): void
    {
        unset($this->items[$offset]);
    }
}

/**
 * @param list<null|array-key> $keys
 * @param ?array-key $first
 * @return array-key|null
 */
function last(array $keys, int|string|null $first): int|string|null
{
    return $keys === [] ? $first : $keys[\count($keys) - 1];
}
