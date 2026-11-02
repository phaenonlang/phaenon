<?php

declare(strict_types=1);

namespace Lift\Vendor;

use Acme\Box;
use Acme\BoxStore;
use Acme\Factory;
use Acme\Item;
use Acme\Repository;
use Acme\SmallBox;
use Acme\Store;

/**
 * The override-erasure rule against parents in other files, found through
 * the program model: vendor PHP (Acme\Repository, Acme\Factory) and project
 * files already in Phaenon (Acme\Store, Acme\BoxStore).
 */
final class User
{
}

/**
 * Vendor PHP takes an object: save() is lifted, find() is not — User|null
 * compiles to ?User, narrower than the ?object PHP saw.
 *
 * @implements Repository<User>
 */
final class UserRepository implements Repository
{
    /**
     * @return User|null
     */
    public function find(int $id): ?object
    {
        return null;
    }

    /**
     * @param User $entity
     */
    public function save(object $entity): void
    {
    }
}

/**
 * A return narrows to the overridden method's when its own erasure is
 * wider: T compiles to object, as declared.
 *
 * @template T
 * @implements Factory<T>
 */
final class PrototypeFactory implements Factory
{
    /**
     * @param T $prototype
     */
    public function __construct(private object $prototype)
    {
    }

    /**
     * @return T
     */
    public function create(): object
    {
        return clone $this->prototype;
    }
}

/**
 * A parent in Phaenon compiles put(Item $item).
 *
 * @implements Store<Box>
 */
final class Shelf implements Store
{
    /**
     * @param Box $item
     */
    public function put(Item $item): void
    {
    }
}

/**
 * Down a chain the parent's compiled signature counts: BoxStore::put() takes
 * an Item, although its own T erases to Box.
 *
 * @extends BoxStore<SmallBox>
 */
final class SmallBoxStore extends BoxStore
{
    /**
     * @param SmallBox $item
     */
    public function put(Item $item): void
    {
    }
}
