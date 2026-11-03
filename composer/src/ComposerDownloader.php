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
        // copy() rethrows a failed download but leaves its promise's rejection unhandled, which
        // Composer then prints with a stack trace; the rejection is handled here instead.
        $failure = null;
        $this->http->addCopy($url, $path)->then(null, static function (\Throwable $e) use (&$failure): void {
            $failure = $e;
        });
        $this->http->wait();
        if ($failure instanceof \Throwable) {
            throw $failure;
        }
    }
}
