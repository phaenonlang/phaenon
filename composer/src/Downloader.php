<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * Fetches a URL into a file. The plugin's goes through Composer's HTTP client and the launcher's
 * through PHP's own streams, since the launcher runs without Composer.
 */
interface Downloader
{
    /**
     * Writes the body of a successful response to $path.
     *
     * @throws \RuntimeException when there is none
     */
    public function download(string $url, string $path): void;
}
