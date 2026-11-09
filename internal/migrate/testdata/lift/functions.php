<?php

declare(strict_types=1);

namespace App;

final class Account
{
    public const int LIMIT = 10;

    final public const string NAME = 'account';

    private const string SECRET = 's';

    public private(set) int $balance = 0;

    public protected(set) static ?string $owner = null;

    public function __construct(public private(set) string $id, protected string $note = '')
    {
    }

    public function id(): string
    {
        return $this->id;
    }

    final public static function open(string $id): self
    {
        return new self($id);
    }

    protected function touch(): void
    {
    }

    private static function hidden(): void
    {
    }

    public
    function split(): void
    {
    }

    public function static(): string
    {
        return static::class;
    }

    function list(): array
    {
        function inner(): int
        {
            return 1;
        }

        return [inner()];
    }

    public function &cached(): array
    {
        static $c = [];

        return $c;
    }

    public function counter(): object
    {
        return new class {
            public function next(): int
            {
                return 1;
            }
        };
    }
}

function helper(): void
{
}
