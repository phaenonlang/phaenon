<?php

namespace App\Arrays;

interface Source
{
    /** @return list<string> */
    public function names(): array;
}

final class Arrays implements Source
{
    /** @var array<array-key, mixed> */
    private array $any = [];

    /** @var array<int|string, string> */
    private array $strings = [];

    /** @var array<string, int> */
    private array $counts = [];

    /** @var list<array-key> */
    private array $keys = [];

    /** @var mixed[] */
    private array $legacy = [];

    private array $bare = [];

    /**
     * @param array<array-key, mixed> $config the configuration, as given
     * @param array<array-key, int>|null $maybe
     */
    public function __construct(array $config, ?array $maybe, private array $promoted)
    {
        $this->any = $config;
        $this->keys = array_keys($maybe ?? []);
    }

    /** @return array<array-key, mixed> everything */
    public function all(): array
    {
        return $this->any + $this->strings + $this->counts + $this->legacy + $this->bare + $this->promoted;
    }

    public function untyped(array $x, ?iterable $it): array
    {
        return $x + $this->keys + [$it];
    }

    // The interface's @return stands for this one.
    public function names(): array
    {
        return [];
    }

    /** @phpstan-ignore missingType.iterableValue */
    public function ignored(array $config): void
    {
    }

    /** @param array-key $key */
    public function has(int|string $key): bool
    {
        return isset($this->any[$key]);
    }
}
