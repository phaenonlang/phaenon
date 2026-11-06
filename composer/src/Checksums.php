<?php

declare(strict_types=1);

namespace Phaenon\Composer;

/**
 * composer/checksums.json: the phn release this version of the package pins and the SHA-256 of
 * each platform's binary, so that composer.lock, which pins the package, pins the binary too.
 *
 * The release tooling writes the file:
 * `{"version": "0.1.0", "assets": {"darwin-arm64": {"file": "phn-darwin-arm64", "sha256": "…"}}}`.
 * A package with no release yet says `{"version": null, "assets": {}}`. The playground's assets
 * (`wasm`, `wasm-exec`) are pinned there too, under keys no platform has, so they are read and
 * never chosen.
 */
final readonly class Checksums
{
    /**
     * @param array<string, PinnedBinary> $binaries by Platform::key()
     */
    private function __construct(
        public ?string $version,
        private array $binaries,
    ) {
    }

    /**
     * @throws \UnexpectedValueException when the file cannot be read or is not in the format above
     */
    public static function read(string $path): self
    {
        $json = @file_get_contents($path);
        if ($json === false) {
            throw new \UnexpectedValueException(sprintf('cannot read %s', $path));
        }

        return self::parse($json, $path);
    }

    /**
     * @param string $origin names the file in messages
     *
     * @throws \UnexpectedValueException when the JSON is not in the format above
     */
    public static function parse(string $json, string $origin): self
    {
        try {
            $data = json_decode($json, true, 8, JSON_THROW_ON_ERROR);
        } catch (\JsonException $e) {
            throw new \UnexpectedValueException(sprintf('%s is not JSON: %s', $origin, $e->getMessage()), 0, $e);
        }
        if (!\is_array($data) || !\array_key_exists('version', $data) || !\is_array($data['assets'] ?? null)) {
            throw new \UnexpectedValueException(sprintf('%s must hold "version" and "assets"', $origin));
        }
        $version = $data['version'];
        // The version and the file names become parts of paths and URLs.
        if ($version !== null && (!\is_string($version) || preg_match('/^[0-9A-Za-z][0-9A-Za-z.+-]*$/', $version) !== 1)) {
            throw new \UnexpectedValueException(sprintf('%s: "version" must be null or a version such as "0.1.0"', $origin));
        }
        $binaries = [];
        foreach ($data['assets'] as $key => $asset) {
            $file = \is_array($asset) ? ($asset['file'] ?? null) : null;
            $sha256 = \is_array($asset) ? ($asset['sha256'] ?? null) : null;
            if (
                !\is_string($file) || preg_match('/^[0-9A-Za-z][0-9A-Za-z._-]*$/', $file) !== 1
                || !\is_string($sha256) || preg_match('/^[0-9a-f]{64}$/', $sha256) !== 1
            ) {
                throw new \UnexpectedValueException(sprintf(
                    '%s: asset "%s" must have a "file" name and a lowercase hex "sha256"',
                    $origin,
                    $key,
                ));
            }
            if ($version === null) {
                throw new \UnexpectedValueException(sprintf('%s lists assets but no "version"', $origin));
            }
            $binaries[(string) $key] = new PinnedBinary($version, $file, $sha256);
        }

        return new self($version, $binaries);
    }

    /**
     * @throws BinaryUnavailable when there is no release, or no binary for the platform in it
     */
    public function binaryFor(Platform $platform): PinnedBinary
    {
        if ($this->version === null) {
            throw new BinaryUnavailable(sprintf(
                'this version of phaenon/phn pins no released phn binary (composer/checksums.json names no release): '
                . 'set %s to the path of a phn binary, such as one built with `go build -o bin/phn ./cmd/phn` '
                . 'in a checkout of phaenonlang/phaenon',
                BinaryResolver::OVERRIDE,
            ));
        }

        return $this->binaries[$platform->key()] ?? throw new BinaryUnavailable(sprintf(
            'phn %s has no released binary for %s: set %s to the path of a phn built for this machine',
            $this->version,
            $platform->key(),
            BinaryResolver::OVERRIDE,
        ));
    }
}
