<?php

namespace Acme;

/**
 * A library class as libraries are written: PHP's types, and docblocks
 * that leave arrays and class names open.
 */
class Client
{
    public function request(string $method, array $options = []): array
    {
        return $options;
    }

    public function configure(array $config): void
    {
    }

    /**
     * @param class-string $class
     */
    public function describe(string $class): string
    {
        return $class;
    }

    /**
     * @return array<string, string>
     */
    public function headers(): array
    {
        return [];
    }

    public function notice(string|array $message): void
    {
    }

    public static function boot(array $options = []): static
    {
        return new static();
    }
}
