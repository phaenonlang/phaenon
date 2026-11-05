<?php

namespace App\ListTypes;

use ArrayObject as Bag;

#[\Attribute(\Attribute::TARGET_PROPERTY)]
final class Mapped
{
}

final class User
{
    public function __construct(public string $name)
    {
    }
}

interface Source
{
    /** @return list<string> */
    public function all(): array;
}

final class Registry implements Source
{
    private array $names = [];

    private array $byName = [];

    private array $lengths = ['root' => 4];

    private array $removed = [];

    private array $sorted = [];

    private array $pairs = [];

    private array $nothing = [];

    private ?array $cache = null;

    private array $bags = [];

    #[Mapped]
    private array $mapped = [];

    public function add(User $user): void
    {
        $this->names[] = $user->name;
        $this->byName[$user->name] = $user;
        $this->lengths[$user->name] = strlen($user->name);
        $this->removed[] = 1;
        unset($this->removed[0]);
        $this->sorted[] = 3;
        sort($this->sorted);
        $this->pairs = [1, 'one'];
        $this->cache = [1, 2];
        $this->bags[] = new Bag();
        $this->mapped[] = $user;
    }

    // A final class: its methods are final. This one returns a property
    // the codemod types first.
    public function names(): array
    {
        return $this->names;
    }

    // The interface's @return stands for this one.
    public function all(): array
    {
        return $this->names;
    }

    private static function keys(): array
    {
        return array_keys(['a' => 1, 'b' => 2]);
    }

    private function total(array $xs): int
    {
        return array_sum($xs) + count($this->nothing);
    }

    private function label(array $parts): string
    {
        return implode(',', $parts);
    }

    // A pair whose positions differ is a tuple.
    private function entry(User $user): array
    {
        return [$user->name, strlen($user->name)];
    }

    // A document of more than three types reads as map<string, mixed>.
    private function document(): array
    {
        return ['id' => 1, 'name' => 'a', 'active' => true, 'score' => 1.5];
    }

    public function report(): string
    {
        $sum = $this->total([1, 2, 3]) + $this->total([4]);
        $labels = array_map([$this, 'label'], [['a'], ['b']]);

        return $sum . implode(';', $labels) . implode(',', self::keys()) . count($this->byName)
            . count($this->lengths) . count($this->removed) . count($this->pairs) . count($this->cache ?? [])
            . count($this->bags) . count($this->mapped) . $this->entry(new User('a'))[1] . count($this->document());
    }
}
