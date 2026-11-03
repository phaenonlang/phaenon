<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * The phn release asset this version of phaenon/phn pins for one platform.
 */
final readonly class PinnedBinary
{
    /**
     * @param string $version the release, without the tag's `v`
     * @param string $file    the asset's file name, the same on GitHub and on a mirror
     * @param string $sha256  lowercase hex
     */
    public function __construct(
        public string $version,
        public string $file,
        public string $sha256,
    ) {
    }

    public function matches(string $path): bool
    {
        if (!is_file($path)) {
            return false;
        }
        $actual = hash_file('sha256', $path);

        return $actual !== false && hash_equals($this->sha256, $actual);
    }
}
