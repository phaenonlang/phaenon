<?php

namespace App\ListTypesDepth;

final class Report
{
    public function render(string $name): string
    {
        return $this->json(['name' => $name, 'meta' => ['tags' => ['a', 'b'], 'owner' => ['id' => 'x', 'roles' => ['admin']]]]);
    }

    // A document handed to json_encode(): no use reaches into it, and its
    // first level holds strings beside maps, so it holds mixed.
    private function json(array $value): string
    {
        return json_encode($value, JSON_THROW_ON_ERROR);
    }
}

final class Rows
{
    // Rows nobody reads into: they hold mixed, and what the method
    // returns still says they are maps.
    private function rows(): array
    {
        return [['id' => 1, 'tags' => ['a']], ['id' => 2, 'tags' => []]];
    }

    public function count(): int
    {
        return count($this->rows());
    }
}

final class Recorder
{
    // Read three levels deep, through a destructuring: spelled three
    // levels deep.
    private array $calls = [];

    public function get(string $url): void
    {
        $this->calls[] = [$url, ['Accept' => 'application/json', 'Authorization' => 'Bearer x']];
    }

    public function authorized(): bool
    {
        foreach ($this->calls as [, $headers]) {
            if ($headers['Authorization'] === '') {
                return false;
            }
        }

        return true;
    }
}

final class Shapes
{
    // Paths handed one at a time to a parameter that spells them: they are
    // spelled, though no index reaches into them.
    private function shapes(): array
    {
        return ['web' => [['services', 'web', 'image'], ['services', 'web', 'tag']]];
    }

    /**
     * @param array<string, mixed> $tree
     */
    public function leaves(array $tree): int
    {
        $n = 0;
        foreach ($this->shapes() as $paths) {
            foreach ($paths as $path) {
                $n += Tree::holds($tree, $path) ? 1 : 0;
            }
        }

        return $n;
    }
}

final class Tree
{
    /**
     * @param array<string, mixed> $tree
     * @param list<string> $path
     */
    public static function holds(array $tree, array $path): bool
    {
        return $path !== [] && array_key_exists($path[0], $tree);
    }
}

final class Catalogue
{
    // A return another class reads: its reads count.
    public function sections(): array
    {
        return ['main' => [['title' => 'A'], ['title' => 'B']]];
    }

    // The same, only counted: its lists hold mixed.
    public function drafts(): array
    {
        return ['main' => [['title' => 'C']]];
    }
}

final class Page
{
    public function titles(Catalogue $catalogue): string
    {
        $out = '';
        foreach ($catalogue->sections()['main'] as $entry) {
            $out .= $entry['title'];
        }

        return $out . count($catalogue->drafts()['main']);
    }
}
