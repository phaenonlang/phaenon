<?php

declare(strict_types=1);

namespace App;

/**
 * With --keep-layout the tags are still lifted; braces, docblocks and
 * bodies stay as they are.
 */
final class Account
{
    /**
     * The balance.
     */
    private int $balance = 0;

    /**
     * Withdraws money.
     *
     * @throws \DomainException
     */
    public function withdraw(int $amount): void
    {
        $this->balance -= $amount;
    }

    public function balance(): int
    {
        return $this->balance;
    }
}
