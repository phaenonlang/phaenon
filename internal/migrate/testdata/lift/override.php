<?php

declare(strict_types=1);

namespace Lift\Override;

/**
 * The override-erasure rule: a method that implements a generic signature
 * compiles with its parameters widened to the implemented method's types,
 * so `@param T $aggregate` over `Aggregate $aggregate` is lifted although
 * T's own bound is narrower — the compiled method still takes an Aggregate.
 */
interface Aggregate
{
}

interface EventSourcedAggregate extends Aggregate
{
}

interface AggregateId
{
}

final class Order implements Aggregate
{
}

final class OrderId implements AggregateId
{
}

/**
 * @template Id of AggregateId
 * @template T of Aggregate
 */
interface AggregateRepository
{
    /**
     * @param Id $id
     * @return T|null
     */
    public function get(AggregateId $id): ?Aggregate;

    /**
     * @param T $aggregate
     */
    public function save(Aggregate $aggregate): void;
}

/**
 * @template Id of AggregateId
 * @template T of EventSourcedAggregate
 * @implements AggregateRepository<Id, T>
 */
abstract class AbstractEventSourcedRepository implements AggregateRepository
{
    /**
     * @return class-string<T>
     */
    abstract protected static function aggregateClass(): string;

    /**
     * @param Id $id
     * @return T|null
     */
    public function get(AggregateId $id): ?EventSourcedAggregate
    {
        return null;
    }

    /**
     * @param T $aggregate
     */
    public function save(Aggregate $aggregate): void
    {
        $aggregateClass = static::aggregateClass();
        if (!$aggregate instanceof $aggregateClass) {
            throw new \InvalidArgumentException('wrong aggregate');
        }
    }
}

/**
 * A concrete type argument: the same widening.
 *
 * @implements AggregateRepository<OrderId, Order>
 */
final class OrderRepository implements AggregateRepository
{
    /**
     * @param OrderId $id
     * @return Order|null
     */
    public function get(AggregateId $id): ?Aggregate
    {
        return null;
    }

    /**
     * @param Order $aggregate the order to store
     */
    public function save(Aggregate $aggregate): void
    {
    }
}

/**
 * A parameter wider than the implemented method's: the rule would compile
 * Aggregate, not the object PHP saw. A return narrower than the implemented
 * method's is not narrowed further: T|null compiles to its own erasure,
 * not the declared ?Aggregate. Both tags stay.
 *
 * @template T of EventSourcedAggregate
 * @implements AggregateRepository<AggregateId, T>
 */
final class LenientRepository implements AggregateRepository
{
    /**
     * @param AggregateId $id
     * @return T|null
     */
    public function get(AggregateId $id): ?Aggregate
    {
        return null;
    }

    /**
     * @param T $aggregate
     */
    public function save(object $aggregate): void
    {
    }
}

/**
 * A function overrides nothing: the tag erases to EventSourcedAggregate and
 * stays.
 *
 * @param EventSourcedAggregate $aggregate
 */
function store(Aggregate $aggregate): void
{
}
