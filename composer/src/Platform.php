<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * A platform phn is released for, named as its release assets name it: `darwin-arm64`.
 */
final readonly class Platform
{
    private function __construct(
        public string $os,
        public string $arch,
    ) {
    }

    /**
     * The platform this PHP runs on.
     *
     * @throws BinaryUnavailable when phn is not released for it
     */
    public static function current(): self
    {
        return self::of(PHP_OS_FAMILY, php_uname('m'));
    }

    /**
     * @param string $osFamily as PHP_OS_FAMILY says it
     * @param string $machine  as php_uname('m') says it
     *
     * @throws BinaryUnavailable when phn is not released for it
     */
    public static function of(string $osFamily, string $machine): self
    {
        $os = match ($osFamily) {
            'Darwin' => 'darwin',
            'Linux' => 'linux',
            'Windows' => 'windows',
            default => null,
        };
        // Windows says AMD64 and ARM64, Linux x86_64 and aarch64, macOS x86_64 and arm64.
        $arch = match (strtolower($machine)) {
            'x86_64', 'amd64' => 'amd64',
            'arm64', 'aarch64' => 'arm64',
            default => null,
        };
        if ($os === null || $arch === null) {
            throw new BinaryUnavailable(sprintf(
                'phn is not released for %s on %s: set %s to the path of a phn built for this machine',
                $osFamily,
                $machine,
                BinaryResolver::OVERRIDE,
            ));
        }

        return new self($os, $arch);
    }

    public function key(): string
    {
        return $this->os . '-' . $this->arch;
    }

    /**
     * The binary's file name in bin/ on the platform PHP runs on, released for or not: a
     * checkout's own build is named the same way.
     */
    public static function executableName(): string
    {
        return PHP_OS_FAMILY === 'Windows' ? 'phn.exe' : 'phn';
    }
}
