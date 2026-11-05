<?php

namespace App\ListTypesNarrow;

final class Settings
{
    private array $values = [];

    public function set(string $name, mixed $value): void
    {
        $this->values[$name] = $value;
    }

    // array_keys() of the property while it is a bare array is a
    // list<int|string>: once the property is typed, a later round narrows
    // it to the list<string> it is.
    public function names(): array
    {
        return array_keys($this->values);
    }
}

final class Walk
{
    // A recursion spread into array_push(): the mixed of its own bare
    // array is not written, the self round finds list<string>.
    private static function files(string $dir): array
    {
        $files = [];
        foreach (scandir($dir) ?: [] as $name) {
            if (is_dir($dir . '/' . $name)) {
                array_push($files, ...self::files($dir . '/' . $name));
            } else {
                $files[] = $name;
            }
        }

        return $files;
    }

    public static function count(string $dir): int
    {
        return count(self::files($dir));
    }
}

final class Trees
{
    private array $trees = [];

    // What the method writes back it read of the property through a
    // local: its own values, which the self round reads.
    public function put(string $sha, string $base, string $path, \SplFileInfo $file): void
    {
        $files = $this->trees[$base] ?? [];
        $files[$path] = $file;
        ksort($files);
        $this->trees[$sha] = $files;
    }
}

final class Templates
{
    private array $template = [];

    private array $words = [];

    public function __construct()
    {
        $this->template = ['a' => [str_repeat('a', 4), "body\n"], 'b' => ['', '']];
        $this->words = ['a' => ['x', 'y']];
    }

    // The template's pairs are taken apart: (string, string). The words
    // are iterated: a list.
    public function body(string $name): string
    {
        [$commit, $body] = $this->template[$name];
        foreach ($this->words[$name] as $word) {
            $body .= $word;
        }

        return $commit . $body;
    }
}

final class Routes
{
    // An answer is a map or a list of records, spelled apart; the records
    // in a row are a list, whatever their fields.
    private function routes(): array
    {
        return [
            'user' => [200, ['login' => 'acme']],
            'repos' => [200, [['name' => 'a'], ['name' => 'b', 'private' => true]]],
            'missing' => [404, []],
        ];
    }

    public function status(string $key): int
    {
        [$status] = $this->routes()[$key];

        return $status;
    }
}

final class Chain
{
    // $object::class names some class: class.
    private static function chainOf(object $client): array
    {
        return [$client::class];
    }

    public static function depth(object $client): int
    {
        return count(self::chainOf($client));
    }
}
