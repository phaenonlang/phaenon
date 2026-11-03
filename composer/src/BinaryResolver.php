<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * Finds the phn binary a run uses, and installs the pinned one when it is missing
 * (design/tooling.md §3.2).
 *
 * `PHN_BINARY` wins and is used as it is. A package that is a checkout of the compiler, as through
 * a symlinked path repository, runs the developer's own build in its bin/, and nothing is ever
 * written into it. Otherwise the binary is the release asset that composer/checksums.json pins for
 * the platform, installed as bin/phn in the package. It is downloaded once into Composer's cache,
 * and every copy is checked against the pinned SHA-256 before it is used, so the binary that runs
 * is the one composer.lock pins.
 *
 * Nothing here needs Composer: the launcher uses it with a downloader of its own.
 */
final readonly class BinaryResolver
{
    public const string OVERRIDE = 'PHN_BINARY';

    private const string RELEASES = 'https://github.com/phaenonlang/phaenon/releases/download/v{version}/{file}';

    /**
     * @param string                 $packageDir the installed phaenon/phn: its composer/checksums.json pins
     *                                           the binary, its bin/ receives it
     * @param ?string                $cacheDir   Composer's cache directory; null downloads without caching
     * @param ?string                $binaryUrl  the root package's extra.phaenon.binary-url
     * @param \Closure(string): void $notify     told about each download, to show it in the output
     */
    public function __construct(
        private string $packageDir,
        private Downloader $downloader,
        private ?string $cacheDir,
        private ?string $binaryUrl,
        private \Closure $notify,
    ) {
    }

    /**
     * The binary to run: PHN_BINARY, otherwise a checkout's own build, otherwise the pinned
     * binary, installed if it is not.
     *
     * @throws BinaryUnavailable
     * @throws \UnexpectedValueException when composer/checksums.json is broken
     */
    public function resolve(): string
    {
        return self::override() ?? self::checkoutBuild($this->packageDir) ?? $this->install();
    }

    /**
     * PHN_BINARY, the phn a developer chose: a build from a checkout, a distribution's package.
     * No download and no checksum.
     *
     * @return ?string null when it is not set
     *
     * @throws BinaryUnavailable when it names no executable file
     */
    public static function override(): ?string
    {
        $path = getenv(self::OVERRIDE);
        if ($path === false || $path === '') {
            return null;
        }
        if (!is_file($path) || !is_executable($path)) {
            throw new BinaryUnavailable(sprintf('%s is %s, which is not an executable file', self::OVERRIDE, $path));
        }

        return $path;
    }

    /**
     * In a checkout of the compiler the package's bin/ is the developer's: `go build -o bin/phn
     * ./cmd/phn` writes there. Its build is used as it is, without a checksum, and nothing is
     * downloaded or written into the checkout.
     *
     * @return ?string null when the package is not a checkout
     *
     * @throws BinaryUnavailable when the checkout has no build to run
     */
    public static function checkoutBuild(string $packageDir): ?string
    {
        if (!self::isCheckout($packageDir)) {
            return null;
        }
        $build = self::installedPath($packageDir);
        if (is_file($build) && is_executable($build)) {
            return $build;
        }
        throw new BinaryUnavailable(sprintf(
            'phaenon/phn is installed from a checkout of the compiler, %s, which has no executable bin/%s: '
            . 'run `go build -o bin/phn ./cmd/phn` there, or set %s to the path of a phn binary',
            $packageDir,
            Platform::executableName(),
            self::OVERRIDE,
        ));
    }

    public static function installedPath(string $packageDir): string
    {
        return $packageDir . '/bin/' . Platform::executableName();
    }

    /**
     * Where the asset is downloaded from: the root's binary-url, a template with `{version}` and
     * `{file}` or a base URL the file name is appended to, otherwise the GitHub release.
     */
    public static function url(?string $template, PinnedBinary $binary): string
    {
        $template ??= self::RELEASES;
        if (!str_contains($template, '{file}')) {
            $template = rtrim($template, '/') . '/{file}';
        }

        return strtr($template, ['{version}' => $binary->version, '{file}' => $binary->file]);
    }

    /**
     * Puts the pinned binary in place as bin/phn, unless an identical one is there already.
     * Reached only through resolve(), past the checkout's rule.
     *
     * @return string its path
     *
     * @throws BinaryUnavailable
     * @throws \UnexpectedValueException when composer/checksums.json is broken
     */
    private function install(): string
    {
        $binary = Checksums::read($this->packageDir . '/composer/checksums.json')->binaryFor(Platform::current());
        $target = self::installedPath($this->packageDir);
        if ($binary->matches($target)) {
            return $target;
        }
        self::ensureDirectory(\dirname($target));
        $cached = $this->cacheEntry($binary);
        if ($cached === null) {
            $this->download($binary, $target);

            return $target;
        }
        if (is_file($cached) && !$binary->matches($cached)) {
            ($this->notify)(sprintf('%s does not match its checksum: downloading it again', $cached));
            @unlink($cached);
        }
        if (!is_file($cached)) {
            $this->download($binary, $cached);
        }
        $temporary = self::temporary($target);
        if (!@copy($cached, $temporary)) {
            throw new BinaryUnavailable(sprintf('cannot copy %s to %s', $cached, $temporary));
        }
        self::place($temporary, $target);

        return $target;
    }

    /**
     * The binary's place in the cache, or null when there is no cache to keep it in: a cache that
     * cannot be written is no reason to fail the install.
     */
    private function cacheEntry(PinnedBinary $binary): ?string
    {
        if ($this->cacheDir === null || $this->cacheDir === '') {
            return null;
        }
        $dir = $this->cacheDir . '/phaenon-phn/' . $binary->version;
        if (!is_dir($dir) && !@mkdir($dir, 0777, true) && !is_dir($dir)) {
            return null;
        }
        $entry = $dir . '/' . $binary->file;

        return is_writable($dir) || $binary->matches($entry) ? $entry : null;
    }

    /**
     * Downloads the asset to $destination by way of a temporary file beside it, so that a failed
     * or concurrent download never leaves a partial file under the final name.
     *
     * @throws BinaryUnavailable when the download fails or is not the pinned binary
     */
    private function download(PinnedBinary $binary, string $destination): void
    {
        $url = self::url($this->binaryUrl, $binary);
        ($this->notify)(sprintf('Downloading phn %s (%s) from %s', $binary->version, $binary->file, $url));
        self::ensureDirectory(\dirname($destination));
        $temporary = self::temporary($destination);
        try {
            $this->downloader->download($url, $temporary);
        } catch (\RuntimeException $e) {
            @unlink($temporary);
            throw new BinaryUnavailable(
                sprintf('cannot download phn %s from %s: %s', $binary->version, $url, $e->getMessage()),
                0,
                $e,
            );
        }
        $actual = is_file($temporary) ? hash_file('sha256', $temporary) : false;
        if ($actual === false || !hash_equals($binary->sha256, $actual)) {
            @unlink($temporary);
            throw new BinaryUnavailable(sprintf(
                'the phn %s downloaded from %s has SHA-256 %s, but phaenon/phn pins %s: '
                . 'it is not the released binary and was not installed',
                $binary->version,
                $url,
                $actual === false ? '(none: nothing was written)' : $actual,
                $binary->sha256,
            ));
        }
        self::place($temporary, $destination);
    }

    /**
     * A checkout holds the compiler's Go module, which the dist archive leaves out.
     */
    private static function isCheckout(string $packageDir): bool
    {
        $goMod = @file_get_contents($packageDir . '/go.mod');

        return \is_string($goMod) && preg_match('{^module\s+github\.com/phaenonlang/phaenon\s*$}m', $goMod) === 1;
    }

    private static function temporary(string $path): string
    {
        return $path . '.' . bin2hex(random_bytes(6)) . '.part';
    }

    /**
     * @throws BinaryUnavailable
     */
    private static function place(string $temporary, string $destination): void
    {
        if (!@chmod($temporary, 0755) || !@rename($temporary, $destination)) {
            @unlink($temporary);
            throw new BinaryUnavailable(sprintf('cannot write %s', $destination));
        }
    }

    /**
     * @throws BinaryUnavailable
     */
    private static function ensureDirectory(string $dir): void
    {
        if (!is_dir($dir) && !@mkdir($dir, 0777, true) && !is_dir($dir)) {
            throw new BinaryUnavailable(sprintf('cannot create %s', $dir));
        }
    }
}
