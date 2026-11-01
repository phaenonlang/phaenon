# Phaenon

A typed dialect of PHP 8.5+ that compiles to idiomatic PHP — "TypeScript for PHP".

- `phn` — the compiler (this repository, Go)
- `phaenon/runtime` — the PHP runtime package (`\Phaenon\Type`, stdlib)
- `design/` — language design documents

Status: pre-alpha. M0 done — lexer, parser and printer are conformance-tested against php-src's
`.phpt` corpus (token-identical lexing, 100% of PHP-accepted files parse, parse → print round-trips
token-identically, printed files run identically under `php`). M1 done — `phn build` and
`phn migrate` work for the PHP subset with line-preserving output. M2 done — `phn check` is a
type checker for the PHP subset plus the PHPStan docblock vocabulary; on the first two Tequila
libraries it reports exactly what PHPStan reports at level 10. M3 done — generics are syntax:
`class Box<T>`, `list<T>`, `map<K, V>`, `class<T>`, `Closure(T): U`, `is` / `as`, `type` aliases;
they compile to PHP with PHPStan docblocks, and `phn migrate` lifts PHPStan's docblock generics
into that syntax. M4 done — `list<T>`, `map<K, V>`, tuples and `bytes` with a stdlib written in
Phaenon behind method syntax (`$xs->map(…)` → `Seq::map($xs, …)`), `fun` with `public` implied,
closures in attributes, and a project file that builds sources into sibling output directories.
Four Tequila libraries build from `.phn` with their suites passing, `tequila/core` among them;
the checker reads the extensions the local `php` has loaded (ext-mongodb and friends) from its
reflection. M5 in progress — structured concurrency: `concurrently { … }` blocks and `spawn`
over amp v3 and the Revolt event loop compile to `Scope::run(…)` / `Scope::spawn(…)` on the
`phaenon/concurrent` package; the compiler and the package are in, the `tq-cli` dogfood is next.

## Type checking

```sh
phn check src tests            # diagnostics with codes, source lines and carets; exit 1 on errors
phn check --json src           # one JSON object per diagnostic, for editors
phn build src                  # type-checks first; a file with errors is not written
phn build --emit-on-error src  # …unless asked to: a codebase adopting the checker builds while it fixes
```

The checker reads the rest of the program without configuration: project files, vendor code
through the nearest `vendor/composer` autoload maps, and the php-src stubs embedded in the binary
(refined by an overlay for what PHP cannot express: `array_map` returning `array<K, R>`,
`ReflectionClass<T>`, PHPUnit's assertions). Docblocks are the interop contract: `@param`,
`@return`, `@var`, `@template`, `@extends`, `@phpstan-assert` and the PHPStan type grammar are
understood, and `@phpstan-ignore` comments are honoured. Diagnostic codes are stable: `P1xxx`
removed constructs, `P2xxx` types and flow, `P4xxx` interop (docblocks, vendor). Design:
`design/checker.md`.

## How it fits into a Composer project

A `.phn` file is PHP without the `<?php` tag. `phn build` compiles each source root into a sibling
output directory with the same structure — `src/` → `.src/`, `tests/` → `.tests/` — named in
`phaenon.json` at the project root:

```json
{"roots": {"src": ".src", "tests": ".tests"}, "exclude": ["tests/Fixture"]}
```

Composer's PSR-4 (`"App\\": ".src/"`), PHPUnit, PHPStan, php-cs-fixer and Xdebug point at the
outputs, which are gitignored; the IDE and the repository hold the sources. The output is a sibling
at the same depth, not a nested mirror, so that a compiled file's `__DIR__` sits where its source's
does: `__DIR__.'/../config/services.yaml'` and `\dirname(__DIR__, 3)` keep resolving. Every other
file under a root (fixtures, YAML) is copied over, so `__DIR__.'/Fixture'` exists there too, and an
output whose source is gone is removed. `exclude` names the paths the checker leaves alone
(fixtures another analyser owns); `phn build` compiles them all the same.

```sh
phn migrate src tests          # once: Foo.php → Foo.phn, generic docblocks lifted into syntax, `public function` → `fun`
phn build                      # the roots of phaenon.json → their outputs
phn build --check              # CI: fail when an output is missing, stale or has no source
phn check                      # type-check the roots, minus "exclude"
```

Without a project file, `phn build src tests` writes `Foo.php` next to `Foo.phn` — the adoption
case, a `.phn` at a time in a PHP tree — or under `--out DIR`.

`phn migrate` strips `<?php`, `declare(strict_types=1)` and a trailing `?>`, then lifts what
PHPStan code says in comments into the signature: `@template T of Foo` → `<T of Foo>`, `@extends
Base<T>` → `extends Base<T>`, `@param list<T> $xs` → `list<T> $xs`, `@return T` → `: T`, a
property's `@var array<K, V>` → `map<K, V>`. It also spells every named function and method `fun`,
dropping the `public` that Phaenon implies (`public function` → `fun`, `final public static
function` → `final static fun`), drops a constant's `public` (`public const` → `const`) and `public`
before an asymmetric visibility (`public private(set)` → `private(set)`); `--keep-public` keeps all three. A tag is lifted only when its type has a Phaenon
spelling and erases to the type PHP already saw — the generated `.php` keeps the same signatures —
and every tag left in place is reported with the reason (`--keep-docblocks` skips the lift).
Prose survives without its type: `@param T $x prose` becomes `@param $x - prose` (the dash, as in
JSDoc, tells an IDE that expects a type that none follows) and a property's `@var T prose` becomes
its docblock `/** prose */`; the emitter puts the types back and drops the dash. It also runs on
`.phn` files, so a project migrated earlier can be lifted later; a declaration already written in
Phaenon keeps its type then.

The generated file keeps **every line number**: line N of `Foo.php` is line N of `Foo.phn`. Stack
traces, `__LINE__`, coverage and PHPStan findings point at the Phaenon source without source maps.
The price is paid on line 1, which carries the open tag, a marker and `declare(strict_types=1)`
(Phaenon is strict by default; a file that declares `strict_types` itself is left alone):

```php
<?php /* @generated by phaenon from Foo.phn */ declare(strict_types=1); namespace App;
```

php-cs-fixer runs on the outputs with the PSR-12 rules about that line off
(`blank_line_after_opening_tag`, `blank_lines_before_namespace`, `linebreak_after_opening_tag`,
`no_multiple_statements_per_line`); everything else it checks is the source's own text.

A library ships its `.phn` sources and no compiled PHP (`roadmap.md` D13): with `phn` and the
library pinned, the output is reproducible byte for byte. The Composer plugin that builds every
installed package on `composer install` is M6 work; until then a consumer runs `phn build` in
`vendor/<package>`.

## Generics

```php
final class Repository<T of Model>
{
    fun __construct(private readonly class<T> $class, private EventStore $store) {}
    fun get(Uuid $id): ?T { … if ($root instanceof $this->class) { return $root; } … }
    fun fresh(Uuid $id): T { return new $this->class($id); }
}
final class UserRepository extends Repository<User>
{
    fun __construct(EventStore $store) { parent::__construct(User::class, $store); }
}

function first<T>(list<T> $items): ?T { return $items[0] ?? null; }
```

Type parameters are erased: `first()` compiles to `function first(array $items): mixed` with
`@template T`, `@param list<T> $items` and `@return ?T` above it, so PHPStan, PhpStorm and a PHP
caller see ordinary typed PHP. A type parameter has no runtime value; when a class needs its `T`
at runtime it takes a `class<T>` — the type of `User::class`, a plain string — the way PHP
projects already write it, and `UserRepository` is an ordinary autowirable service. The compiler
never adds parameters or members: the output contains no identifier you did not write. `$x is
list<int>` compiles to `\Phaenon\Type::list(\Phaenon\Type::int())->check($x)`; `$x is int` to
`\is_int($x)`. Only code that tests a structural type depends on `phaenon/runtime`.

A generic declaration's docblock adds lines to the output (PHPStan reads one tag per line);
every other edit keeps line numbers exact. Design: `design/generics.md`, decisions in §11.

## Concurrency

A `concurrently { … }` block owns every task started inside it with `spawn` and ends only when
all of them have completed; a task's failure cancels the others and is rethrown where the block
ends. The handle is amp's `Future<T>`, the loop is Revolt's, and no function is marked `async`:
the body runs in the calling fiber, so a Symfony command or a request handler enters a block
without starting a loop (`../design/concurrency.md`).

```php
return concurrently (timeout: 5.0) {
    $user = spawn $this->users->find($id);          // Amp\Future<User>
    $orders = spawn $this->orders->forUser($id);    // Amp\Future<list<Order>>

    return new Profile($user->await(), $orders->await());
};
```

```php
return Scope::run(function () use ($id) {
    $user = Scope::spawn(fn () => $this->users->find($id));
    $orders = Scope::spawn(fn () => $this->orders->forUser($id));

    return new Profile($user->await(), $orders->await());
}, timeout: 5.0);
```

The `use` clause is computed the way a person writes it: variables the body reads by value, ones
it assigns and the enclosing scope mentions by reference. `Scope::map($items, $f)` is the
one-line fan-out, `Scope::all($tasks)` awaits handles under the block's cancellation,
`Scope::cancellation()` is what to hand amp APIs so that they stop when the block does. The
checker reports `spawn` outside a block, a handle leaving its block, `break`/`yield`/`goto`
crossing it, a spawn body mutating an outer variable, and `sleep()`/`usleep()` inside a block
(`P2601`–`P2606`). The runtime class is the Composer package `phaenon/concurrent`, separate from
`phaenon/runtime` so that a project without concurrency installs no event loop.

## CLI

```sh
phn build [--out DIR] [--check] [--emit-on-error] [--exclude PATH]... [path...] # type-check and compile .phn → .php; no paths: phaenon.json
phn check [--json] [--no-deprecations] [--exclude PATH]... path...              # type-check .phn (and .php) files
phn migrate [--keep-docblocks] [--keep-public] path... # rewrite .php as .phn
phn stdlib --out DIR [--check]                         # the compiled stdlib (Seq, Map, Str, Bytes), as phaenon/runtime ships it
phn stdlib --concurrent-out DIR [--check]              # the compiled Scope, as phaenon/concurrent ships it
phn stdlib --plugin-stubs DIR  # the PhpStorm plugin's editor-only pseudo-classes (ListMethods.php, …) from the same sources
phn lex   [--php] file                    # token stream
phn print [--php] file                    # parse and pretty-print
```

`--php` reads plain PHP (with `<?php` tags); the default reads Phaenon (code from byte 0).

## Development

```sh
go test ./...                                   # unit tests
PHAENON_PHP_SRC=/abs/path/to/php-src go test ./internal/conformance/   # lexer, parser, printer round trip
PHAENON_EXEC=1 PHAENON_PHP_SRC=/abs/path/to/php-src go test ./internal/conformance/ -run Execution -timeout 30m
```

The execution tests run every `Zend/tests` file through `php` twice — once as written and once
after a transformation — and compare the output. `TestPrinterExecution` checks the pretty-printer
(line numbers normalised away); `TestEmitExecution` checks `migrate` + `build` byte for byte, line
numbers included. ≈2.5 min each. `PHAENON_PHPT_FILTER=ext/standard/tests` narrows any suite to a
corpus subset; `PHAENON_SHOW_DIFFS=n` controls how many differences are listed.

`internal/migrate/testdata/lift/*.php` are migrated and compared with the `.phn` and `.notes`
next to them (`PHAENON_UPDATE=1` rewrites them); the output must parse and lift to itself.

`internal/emit/testdata/*.phn` are compiled through the checker and the emitter and compared
with the `.php` next to them (`PHAENON_UPDATE=1` rewrites them); the output must pass `php -l`,
and PHPStan at level max when `PHAENON_PHPSTAN` is set.

The checker's golden corpus lives in `internal/checker/testdata`: each `.phn` carries the expected
diagnostics as `// want P2103` comments on the offending lines. `PHAENON_PHPSTAN=/path/to/phpstan`
additionally runs PHPStan at level max over the same corpus and fails when PHPStan reports
something the corpus does not expect — the checker may be stricter than PHPStan, never more
lenient. `PHAENON_COMPOSER_PROJECT=/path/to/a/composer/project` exercises the vendor reader.

License: MIT.
