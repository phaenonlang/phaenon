<?php

/**
 * Phaenon overlay: ext-mongodb's binary values carry bytes
 * (design/strings.md §2). The extension's own stub comes from the local
 * php's reflection; this patches the two declarations whose data is binary.
 */

namespace MongoDB\BSON;

interface BinaryInterface extends \Stringable
{
    /** @return bytes */
    public function getData(): string;
}

final class Binary implements \Stringable, BinaryInterface, \JsonSerializable, Type
{
    /** @param string|bytes $data */
    final public function __construct(string $data, int $type = 0) {}

    /** @return bytes */
    final public function getData(): string {}
}
