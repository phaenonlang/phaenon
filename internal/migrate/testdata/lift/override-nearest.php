<?php

declare(strict_types=1);

namespace Lift\OverrideNearest;

/**
 * A method is held to the declaration in its parent's method table, the
 * nearest one up the parent chain however far it is, and to the
 * interfaces its class implements itself. Leaf::take() overrides
 * Base::take(mixed), not the Takes::take(int) its parent implements, so
 * `@param int $x` over `mixed $x` is lifted: the rule compiles it back to
 * mixed.
 *
 * @template T
 */
class Base
{
    /**
     * @param T $x
     */
    public function take(mixed $x): void
    {
    }
}

/**
 * @extends Base<int>
 */
class Mid extends Base
{
}

interface Takes
{
    public function take(int $x): void;
}

abstract class Impl extends Mid implements Takes
{
}

class Leaf extends Impl
{
    /**
     * @param int $x
     */
    public function take(mixed $x): void
    {
    }
}

final class Name implements \Stringable
{
    public function __toString(): string
    {
        return 'name';
    }
}

/**
 * @template T of \Stringable
 */
class Labels
{
    /**
     * @param T $label
     */
    public function add(\Stringable $label): void
    {
    }
}

interface Counts
{
    public function add(int|Name $label): void;
}

/**
 * The parent demands a Stringable and the interface an int or a Name: the
 * rule compiles `@param int|Name` to int|Stringable, the type declared.
 *
 * @extends Labels<Name>
 */
final class NameCounts extends Labels implements Counts
{
    /**
     * @param int|Name $label
     */
    public function add(int|\Stringable $label): void
    {
    }
}

/**
 * A constructor is held to the nearest abstract one, through the concrete
 * constructor that implements it: LoudGreet's compiles to mixed as
 * Greet's does.
 *
 * @template T
 */
abstract class Command
{
    /**
     * @param T $input
     */
    abstract public function __construct(mixed $input);
}

/**
 * @extends Command<string>
 */
class Greet extends Command
{
    /**
     * @param string $input
     */
    public function __construct(mixed $input)
    {
    }
}

final class LoudGreet extends Greet
{
    /**
     * @param string $input
     */
    public function __construct(mixed $input)
    {
        parent::__construct($input);
    }
}
