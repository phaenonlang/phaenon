<?php

/**
 * Phaenon overlay: ext-redis methods whose reflection says array|false
 * where the elements are known (PHPStan reads the same from its stubs).
 */

class Redis
{
    /** @return list<string>|false */
    public function scan(?int &$iterator, ?string $pattern = null, int $count = 0, ?string $type = null): array|false {}

    /** @return list<string>|Redis|false */
    public function keys(string $pattern): array|Redis|false {}

    /**
     * A transaction is chained: `multi()->set(…)->exec()`. The reflection says
     * Redis|bool; PHPStan lets the chain through, and so does this.
     *
     * @return Redis
     */
    public function multi(int $value = Redis::MULTI): Redis|bool {}
}
