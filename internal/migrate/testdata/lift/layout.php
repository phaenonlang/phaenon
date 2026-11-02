<?php

declare(strict_types=1);

namespace App;

use Closure;

/**
 * A greeting.
 */
interface Greets
{
    public function greet(string $name): string;
}

trait Counts
{
    private int $count = 0;

    public function count(): int
    {
        return $this->count;
    }
}

enum Level: int
{
    case Low = 1;

    public function label(): string
    {
        return match ($this) {
            self::Low => 'low',
        };
    }
}

final class Greeter implements
    Greets,
    \Countable
{
    use Counts;

    /**
     * The salutation.
     */
    private string $salutation = 'Hello';

    /**
     * Two lines
     * of prose stay a block.
     */
    private string $punctuation = '!';

    /**
     * A docblock whose only line of content is long enough that collapsing it onto one line would pass the column limit.
     */
    private string $long = '';

    public function __construct(private readonly string $suffix = '')
    {
    }

    public function greet(string $name): string
    {
        return $this->salutation . ', ' . $name . $this->punctuation . $this->suffix;
    }

    public function log(string $message): void
    {
        $this->count += \strlen($message);
    }

    public function fail(string $why): never
    {
        throw new \LogicException($why);
    }

    public function noType()
    {
        $this->count++;
    }

    public function twoStatements(): int
    {
        $this->count++;
        return $this->count;
    }

    public function commented(): int
    {
        // the count so far
        return $this->count;
    }

    public function documented(): int
    {
        /** @var int $count */
        $count = $this->count;
        return $count;
    }

    public function inlineVar(): int
    {
        /**
         * @var int
         */
        return $this->count;
    }

    public function tags(): array
    {
        return [
            'salutation' => $this->salutation,
            'punctuation' => $this->punctuation,
        ];
    }

    public function chained(string $name): string
    {
        return \trim(
            $this->greet($name),
        );
    }

    public function longLine(string $firstName, string $lastName, string $title): string
    {
        return $this->greet($title . ' ' . $firstName . ' ' . $lastName) . $this->salutation . $this->punctuation;
    }

    public function heredoc(string $name): string
    {
        return <<<TEXT
            Dear {$name},
            TEXT;
    }

    public function generator(): \Generator
    {
        return yield $this->count;
    }

    public function &reference(): int
    {
        return $this->count;
    }

    public function oneLine(): int { return $this->count; }

    public function factory(): Closure
    {
        return function (string $name): string
        {
            return $this->greet($name);
        };
    }

    public function anonymous(): \Countable
    {
        return new class () implements \Countable
        {
            public function count(): int
            {
                return 0;
            }
        };
    }

    public function braceComment(): int // a comment before the brace
    {
        return $this->count;
    }

    public function __destruct()
    {
        $this->log('bye');
    }
}

function helper(int $x): int
{
    return $x * 2;
}

function noBody(): void
{
}
