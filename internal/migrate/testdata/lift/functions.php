<?php

declare(strict_types=1);

namespace App;

final class Account
{
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
}

function helper(): void
{
}
