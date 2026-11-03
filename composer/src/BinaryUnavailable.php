<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * No phn binary can be run: none is released for the platform, the download failed, or what was
 * downloaded is not what the package pins. The message says what to do, PHN_BINARY being the way
 * out in every case.
 */
final class BinaryUnavailable extends \RuntimeException
{
}
