<?php

declare(strict_types=1);

namespace Phaenon\Composer;

use Composer\Util\HttpDownloader;

/**
 * Downloads through Composer's HTTP client, which sends the credentials of auth.json and honours
 * Composer's proxy and TLS settings: a mirror in a private package registry takes the
 * credentials the project already has for it.
 */
final readonly class ComposerDownloader implements Downloader
{
    public function __construct(
        private HttpDownloader $http,
    ) {
    }

    public function download(string $url, string $path): void
    {
        $this->http->copy($url, $path);
    }
}
