<?php

namespace Sentry\Integration;

/**
 * Phaenon's declaration of the Sentry SDK 4 integration contract
 * (sentry/sentry 4.x).
 */
interface IntegrationInterface
{
    /**
     * Initializes the current integration by registering it once.
     */
    public function setupOnce(): void;
}
