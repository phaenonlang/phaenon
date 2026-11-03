<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * The root package's `extra.phaenon`. Only the root's counts: an installed package cannot redirect
 * the download or turn off a build.
 */
final readonly class Settings
{
    /**
     * @param ?string $binaryUrl `binary-url`: the mirror the binary is downloaded from
     * @param bool    $buildRoot `build`: false when the project's own workflow builds it
     */
    private function __construct(
        public ?string $binaryUrl,
        public bool $buildRoot,
    ) {
    }

    /**
     * @param array<mixed> $extra the root composer.json's `extra`
     *
     * @throws \UnexpectedValueException when a setting has the wrong type
     */
    public static function fromExtra(array $extra): self
    {
        $phaenon = $extra['phaenon'] ?? [];
        if (!\is_array($phaenon)) {
            throw new \UnexpectedValueException('extra.phaenon in composer.json must be an object');
        }
        $url = $phaenon['binary-url'] ?? null;
        if ($url !== null && (!\is_string($url) || $url === '')) {
            throw new \UnexpectedValueException('extra.phaenon.binary-url in composer.json must be a URL');
        }
        // A string "false" would build all the same, so anything but a boolean is refused.
        $build = $phaenon['build'] ?? true;
        if (!\is_bool($build)) {
            throw new \UnexpectedValueException('extra.phaenon.build in composer.json must be true or false');
        }

        return new self($url, $build);
    }
}
