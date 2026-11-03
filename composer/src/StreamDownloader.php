<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * Downloads with PHP's own HTTP streams, for the launcher, which runs without Composer.
 *
 * It follows redirects itself so that credentials reach only the host they are for: GitHub's
 * release downloads redirect to a storage host, and PHP's streams would send the Authorization
 * header there too.
 */
final readonly class StreamDownloader implements Downloader
{
    private const int REDIRECTS = 10;

    public function __construct(
        private Credentials $credentials,
    ) {
    }

    public function download(string $url, string $path): void
    {
        for ($redirects = 0; ; ++$redirects) {
            $authorization = $this->credentials->for(self::origin($url));
            $headers = ['User-Agent: phaenon-phn'];
            if ($authorization !== null) {
                $headers[] = 'Authorization: ' . $authorization;
            }
            $context = stream_context_create(['http' => [
                'header' => $headers,
                'follow_location' => 0,
                'ignore_errors' => true,
                'timeout' => 60,
            ]]);
            $response = @fopen($url, 'rb', false, $context);
            if ($response === false) {
                throw new \RuntimeException(error_get_last()['message'] ?? 'the request failed');
            }
            try {
                $wrapperData = stream_get_meta_data($response)['wrapper_data'] ?? [];
                [$status, $location] = self::statusAndLocation(\is_array($wrapperData) ? $wrapperData : []);
                if ($status >= 300 && $status < 400 && $location !== null) {
                    if ($redirects === self::REDIRECTS) {
                        throw new \RuntimeException('too many redirects');
                    }
                    $url = self::resolve($url, $location);
                    continue;
                }
                if ($status !== 200) {
                    throw new \RuntimeException(sprintf('the server answered with HTTP status %d', $status));
                }
                $file = @fopen($path, 'wb');
                if ($file === false) {
                    throw new \RuntimeException(sprintf('cannot write %s', $path));
                }
                try {
                    if (stream_copy_to_stream($response, $file) === false) {
                        throw new \RuntimeException('the transfer broke off');
                    }
                } finally {
                    fclose($file);
                }

                return;
            } finally {
                fclose($response);
            }
        }
    }

    /**
     * The key auth.json files credentials under: the host, with the port when there is one.
     */
    private static function origin(string $url): string
    {
        $host = parse_url($url, PHP_URL_HOST);
        $port = parse_url($url, PHP_URL_PORT);

        return (\is_string($host) ? $host : '') . (\is_int($port) ? ':' . $port : '');
    }

    /**
     * @param array<mixed> $headers the response's header lines, status line first
     *
     * @return array{int, ?string}
     */
    private static function statusAndLocation(array $headers): array
    {
        $status = 0;
        $location = null;
        foreach ($headers as $line) {
            if (!\is_string($line)) {
                continue;
            }
            if (preg_match('{^HTTP/\S+\s+(\d{3})}', $line, $match) === 1) {
                $status = (int) $match[1];
                $location = null;
            } elseif (preg_match('{^Location:\s*(.+)$}i', $line, $match) === 1) {
                $location = trim($match[1]);
            }
        }

        return [$status, $location];
    }

    private static function resolve(string $base, string $location): string
    {
        if (preg_match('{^[a-z][a-z0-9+.-]*://}i', $location) === 1) {
            return $location;
        }
        $parts = parse_url($base);
        $scheme = \is_array($parts) ? ($parts['scheme'] ?? 'https') : 'https';
        if (str_starts_with($location, '//')) {
            return $scheme . ':' . $location;
        }
        $origin = $scheme . '://' . (\is_array($parts) ? ($parts['host'] ?? '') : '')
            . (\is_array($parts) && isset($parts['port']) ? ':' . $parts['port'] : '');
        if (str_starts_with($location, '/')) {
            return $origin . $location;
        }
        $path = \is_array($parts) ? ($parts['path'] ?? '/') : '/';

        return $origin . substr($path, 0, (int) strrpos($path, '/') + 1) . $location;
    }
}
