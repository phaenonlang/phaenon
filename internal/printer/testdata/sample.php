<?php
declare(strict_types=1);

namespace App\Service;

use App\{Foo, Bar as Baz};
use function strlen;

#[Attribute(Attribute::TARGET_CLASS)]
final readonly class UserService extends Base implements \Countable, Loggable
{
    use HasLogger, Timestamps { HasLogger::log as protected writeLog; }
    public const int MAX = 10;
    private array $cache = [];
    public string $name { get => strtoupper($this->name); set($v) { $this->name = $v; } }

    public function __construct(private readonly Repo $repo, int ...$ids) {}

    /** Finds a user. */
    public static function find(int|string $id, ?callable $cb = null): ?User
    {
        if ($id === null) { return null; } elseif (is_int($id)) { $x = -$id; } else throw new \RuntimeException("bad $id {$id} ${id}");
        $r = match(true) { $id > 1, $id < -1 => 'big', default => 'small', };
        $f = fn(int $x): int => $x * 2;
        $g = static function () use (&$r, $f) { return $f(1) + $r <=> 3; };
        foreach ($this->cache as $k => &$v) { $v = $k ?? $v ?: 0; }
        $s = <<<EOT
          hello $k[0] {$v->x()}
          EOT;
        return self::$repo?->load($id, name: 'x', ...$rest) instanceof User ? (object)['a' => 1] : null;
    }
}
?>
tail html <?= $x ?>
