<?php

declare(strict_types=1);

namespace Lift;

use Closure;

/**
 * The docblock above an attribute still belongs to the class; a promoted
 * property's own `@var` is lifted; a closure in a union is spelled last.
 *
 * @template T of object
 */
#[\Attribute(\Attribute::TARGET_CLASS)]
final readonly class Binding
{
    /**
     * @param class-string<T> $class
     * @param T|Closure(): T $instance
     */
    public function __construct(
        public string $class,
        public object|Closure $instance,
        /** @var array<string, int> */
        public array $weights = [],
        /** @var list<string> the tags */
        public array $tags = [],
    ) {
    }
}
