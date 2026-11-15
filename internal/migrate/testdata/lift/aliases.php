<?php

declare(strict_types=1);

namespace App\Orders;

use Acme\Item;
use Acme\Legacy;

/**
 * Orders of the shop.
 *
 * @phpstan-type Row array{id: int, name?: string}
 * @phpstan-type Line = array{sku: string, qty: int, unit-price: float, 'class': class-string<Item>, ...}
 * @psalm-type Json array<string, mixed>
 * @phpstan-type Rows list<Row>
 * @phpstan-type Pair array{int, string}
 * @phpstan-type Numbered array{0: int, -1?: string, '7': array<int, Row>}
 * @phpstan-type Nothing array{}
 * @phpstan-type Status 'open'|'closed'
 * @phpstan-type Legacy array{id: int}
 * @phpstan-type Report array{rows: Rows}
 * @phpstan-type Nested array{of: Status}
 * @phpstan-type Described array{id: int} a row with a description
 * @phpstan-type Positional list{int, string}
 * @phpstan-import-type Price from \Acme\Prices
 * @phpstan-import-type Old from Legacy
 */
final class Orders
{
    /** @var list<Row> */
    private array $rows = [];

    /**
     * @return Row|null
     */
    public function find(int $id): ?array
    {
        return $this->rows[$id] ?? null;
    }

    /**
     * @param Row $row the row to add
     * @param Line ...$lines
     */
    public function add(array $row, array ...$lines): void
    {
        $this->rows[] = $row;
    }

    /**
     * @return Rows
     */
    public function all(): array
    {
        return $this->rows;
    }

    /**
     * @param Json $json
     * @return Price
     */
    public function price(array $json): array
    {
        return ['amount' => count($json), 'currency' => 'EUR'];
    }

    /**
     * @param Status $status
     * @param Old $old
     * @return Pair
     */
    public function status(string $status, array $old): array
    {
        return [$old['x'], $status];
    }
}

/**
 * @phpstan-import-type Row from Orders
 * @phpstan-import-type Line from Orders as OrderLine
 * @phpstan-import-type Status from Orders
 * @phpstan-import-type Price from \Acme\Prices as Money
 * @phpstan-type Dup array{a: int}
 */
final class Report
{
    /**
     * @param Row $row
     * @param OrderLine $line
     * @return Money
     */
    public function total(array $row, array $line): array
    {
        return ['amount' => $row['id'] * $line['qty'], 'currency' => 'EUR'];
    }

    /**
     * @param Dup $dup
     * @param Status $status
     */
    public function dup(array $dup, string $status): int
    {
        return $dup['a'];
    }
}

/**
 * @template T
 * @phpstan-type Dup array{b: int}
 * @phpstan-type Boxed array{value: T}
 * @phpstan-type cased array{a: int}
 * @phpstan-type Cased array{b: int}
 */
final class Other
{
}
