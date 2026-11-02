<?php

/**
 * Phaenon overlay for ext-mongodb, whose own stub comes from the local php's
 * reflection and so carries no docblocks: the two declarations whose data is
 * binary carry bytes (design/strings.md §2), and the element type of the
 * write errors is given.
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

namespace MongoDB\Driver;

/**
 * Reflection says `array`; the elements are WriteError, as PHPStan's stubs
 * (`WriteError[]`) say, so `getWriteErrors()[0]->getCode()` is a call on an
 * object, not on mixed.
 */
final class WriteResult
{
    /** @return array<int, WriteError> */
    final public function getWriteErrors(): array {}
}
