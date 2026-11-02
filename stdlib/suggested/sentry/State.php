<?php

namespace Sentry\State;

use Sentry\ClientInterface;

/**
 * Phaenon's declaration of the Sentry SDK 4 hub and scope API the runtime's
 * integration uses (sentry/sentry 4.x).
 */
interface HubInterface
{
    public function getClient(): ?ClientInterface;
}

class Scope
{
    /**
     * Adds an event processor that runs on every event of every scope.
     */
    public static function addGlobalEventProcessor(callable $eventProcessor): void {}
}
