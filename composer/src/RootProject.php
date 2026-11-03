<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * The root project as the launcher finds it without Composer.
 */
final readonly class RootProject
{
    /**
     * @param array<mixed> $manifest its composer.json, decoded; empty when there is none to read
     */
    private function __construct(
        public string $dir,
        private array $manifest,
    ) {
    }

    /**
     * @param ?string $autoloadPath vendor/autoload.php, which Composer's bin proxy puts in
     *                              $_composer_autoload_path; null when the launcher runs directly
     * @param string  $packageDir   the installed phaenon/phn
     */
    public static function locate(?string $autoloadPath, string $packageDir): self
    {
        // The vendor directory can be configured to be anywhere. The bin proxy knows it; without
        // the proxy, the package is <vendor-dir>/phaenon/phn.
        $vendorDir = $autoloadPath !== null ? \dirname($autoloadPath) : \dirname($packageDir, 2);
        $dir = self::recordedRoot($vendorDir) ?? \dirname($vendorDir);
        $json = @file_get_contents($dir . '/composer.json');
        $manifest = \is_string($json) ? json_decode($json, true) : null;

        return new self($dir, \is_array($manifest) ? $manifest : []);
    }

    /**
     * @throws \UnexpectedValueException when extra.phaenon is malformed
     */
    public function settings(): Settings
    {
        $extra = $this->manifest['extra'] ?? [];

        return Settings::fromExtra(\is_array($extra) ? $extra : []);
    }

    /**
     * The root project's directory as Composer records it in vendor/composer/installed.php,
     * which holds it whatever the vendor directory is configured to be.
     */
    private static function recordedRoot(string $vendorDir): ?string
    {
        $file = $vendorDir . '/composer/installed.php';
        if (!is_file($file)) {
            return null;
        }
        $installed = require $file;
        $root = \is_array($installed) ? ($installed['root'] ?? null) : null;
        $path = \is_array($root) ? ($root['install_path'] ?? null) : null;
        $real = \is_string($path) ? realpath($path) : false;

        return $real === false ? null : $real;
    }
}
