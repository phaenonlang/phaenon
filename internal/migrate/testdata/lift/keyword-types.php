<?php

declare(strict_types=1);

namespace Lift\KeywordTypes;

/**
 * Psalm's `empty` is a keyword to PHP's lexer, no type's name a declaration
 * reads: a tag that names it stays a tag, as webmozart/assert's Mixin has.
 */
final class Checks
{
    /**
     * @param iterable<mixed> $value
     * @return iterable<empty>
     */
    public static function allEmpty(iterable $value): iterable
    {
        foreach ($value as $entry) {
            if (!empty($entry)) {
                throw new \InvalidArgumentException('not empty');
            }
        }

        return $value;
    }

    /**
     * @param list<string> $names
     * @return list<string>
     */
    public static function names(array $names): array
    {
        return $names;
    }
}
