<?php

namespace App\ListTypesSelf;

abstract class Command
{
    /** @return list<string> */
    abstract protected function args(string $name): array;

    /** @return array<mixed> */
    abstract protected function options(): array;
}

final class Hello extends Command
{
    // The parent's list<string> is what it returns: it takes that type.
    protected function args(string $name): array
    {
        return ['hello', $name];
    }

    // The parent says array<mixed>, this one returns a map of strings.
    protected function options(): array
    {
        return ['verbose' => 'yes'];
    }
}

final class Tree
{
    private array $skipped = [];

    private array $seen = [];

    // A property filtered into itself, and one spread into itself.
    public function skip(string $id, float $at, float $before): void
    {
        $this->skipped = array_filter($this->skipped, static fn (float $t): bool => $t > $before);
        $this->skipped[$id] = $at;
    }

    public function see(string ...$names): int
    {
        $this->seen = [...$this->seen, ...array_values($names)];

        return count($this->seen) + count($this->skipped);
    }

    /** @param array<mixed> $doc */
    public function strings(array $doc): array
    {
        return self::leaves($doc);
    }

    // A recursion: what it returns holds what it returns.
    /** @param array<mixed> $tree */
    private static function leaves(array $tree, string $prefix = ''): array
    {
        $out = [];
        foreach ($tree as $key => $value) {
            $path = '' === $prefix ? (string) $key : $prefix . '.' . $key;
            if (\is_array($value)) {
                $out = [...$out, ...self::leaves($value, $path)];
            } elseif (\is_string($value)) {
                $out[$path] = $value;
            }
        }

        return $out;
    }

    public static function size(object $root): int
    {
        return self::walk($root, []);
    }

    // Two methods passing a parameter on to each other.
    private static function walk(mixed $value, array $visited): int
    {
        if (!\is_object($value) || isset($visited[spl_object_id($value)])) {
            return 0;
        }
        $visited[spl_object_id($value)] = true;

        return 1 + self::walkAll(get_object_vars($value), $visited);
    }

    private static function walkAll(array $values, array $visited): int
    {
        $n = 0;
        foreach ($values as $v) {
            $n += self::walk($v, $visited);
        }

        return $n;
    }

    /** @param array<mixed> $tree */
    public static function deepest(array $tree): int
    {
        return self::depth([], $tree);
    }

    // A path that grows as the recursion descends.
    /** @param array<mixed> $node */
    private static function depth(array $path, array $node): int
    {
        $max = \count($path);
        foreach ($node as $key => $child) {
            if (\is_array($child)) {
                $max = max($max, self::depth([...$path, (string) $key], $child));
            }
        }

        return $max;
    }
}

final class Printer
{
    public function __construct(private Other $other)
    {
    }

    // Other::format() is not this class's: its argument is not one.
    public function print(): string
    {
        return $this->format(['a', 'b']) . $this->other->format(42) . (['x', 'y'] |> $this->joined(...))
            . \count(self::rows());
    }

    private function format(array $words): string
    {
        return implode(' ', $words);
    }

    // Piped in: the pipe's left side is its argument.
    private function joined(array $parts): string
    {
        return implode('', $parts);
    }

    // An unnamed @var types what it returns.
    private static function rows(): array
    {
        /** @var list<array<string, mixed>> */
        return json_decode('[]', true);
    }
}

final class Other
{
    public function format(int $n): string
    {
        return (string) $n;
    }
}
