<?php

namespace App\ListTypesHierarchy;

interface Event
{
}

interface DomainEvent extends Event
{
    public function stamped(): bool;
}

interface Limiter
{
}

final class SlidingWindow implements Limiter
{
}

final class Refused extends \RuntimeException
{
}

final class Plane
{
}

final class Outbox
{
    // Narrowed by instanceof in one branch and not in the other, the event
    // joins as DomainEvent|Event, which is the Event an author writes.
    private array $staged = [];

    public function stage(Event $event): void
    {
        if ($event instanceof DomainEvent && !$event->stamped()) {
            $this->stamp($event);
        }
        $this->staged[spl_object_id($event)] = $event;
    }

    private function stamp(DomainEvent $event): void
    {
    }
}

final class Limits
{
    public function total(): int
    {
        return $this->count(['export' => new SlidingWindow()])
            + $this->count(['export' => $this->closed()]);
    }

    // A class and the interface it implements: the interface.
    private function count(array $limiters): int
    {
        return count($limiters);
    }

    private function closed(): Limiter
    {
        return new class () implements Limiter {
        };
    }
}

final class Dialer
{
    public function script(): int
    {
        return $this->dial([new Plane()]) + $this->dial([new Refused('no')]);
    }

    // Classes that neither covers stay a union.
    private function dial(array $links): int
    {
        return count($links);
    }
}
