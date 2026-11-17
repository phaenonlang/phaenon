<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * A phn build the plugin ran exited with a non-zero status, the exception's code.
 */
final class BuildFailed extends \RuntimeException
{
    /**
     * @param list<string> $arguments the arguments phn was run with, which tell the build of the
     *                                installed packages (`build --vendor`) from the project's
     */
    public function __construct(string $message, int $code, public readonly array $arguments)
    {
        parent::__construct($message, $code);
    }
}
