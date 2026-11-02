<?php

declare(strict_types=1);

namespace Acme;

/**
 * Vendor PHP: the generic types are docblocks over native object types.
 *
 * @template T of object
 */
interface Repository
{
    /**
     * @return T|null
     */
    public function find(int $id): ?object;

    /**
     * @param T $entity
     */
    public function save(object $entity): void;
}
