<?php

declare(strict_types=1);

namespace Acme;

/**
 * Vendor PHP whose template is wider than the native return type.
 *
 * @template T
 */
interface Factory
{
    /**
     * @return T
     */
    public function create(): object;
}
