# Phaenon

A typed dialect of PHP 8.5+ that compiles to idiomatic PHP — "TypeScript for PHP".

- `phn` — the compiler (this repository, Go)
- `phaenon/runtime` — the PHP runtime package (`\Phaenon\Type` behind `is` / `as`, the checked
  conversions of `\Phaenon\Bytes`, the line map for Monolog and Sentry)
- `design/` — language design documents

Status: pre-alpha. M0 done — lexer, parser and printer are conformance-tested against php-src's
`.phpt` corpus (token-identical lexing, 100% of PHP-accepted files parse, parse → print round-trips
token-identically, printed files run identically under `php`). M1 done — `phn build` and
`phn migrate` work for the PHP subset with line-preserving output. M2 done — `phn check` is a
type checker for the PHP subset plus the PHPStan docblock vocabulary; on the first two Tequila
libraries it reports exactly what PHPStan reports at level 10. M3 done — generics are syntax:
`class Box<T>`, `list<T>`, `map<K, V>`, `class<T>`, `Closure(T): U`, `is` / `as`, `type` aliases;
they compile to PHP with PHPStan docblocks, and `phn migrate` lifts PHPStan's docblock generics
into that syntax. M4 done — `list<T>`, `map<K, V>`, tuples and `bytes`, `fun` with `public`
implied, closures in attributes, and a project file that builds sources into sibling output
directories. (The method syntax on lists, maps and strings M4 also brought, and its stdlib `Seq`,
`Map` and `Str`, were removed by D20: `->` means what PHP means by it, and these values go through
PHP's functions, chained with pipes.)
Four Tequila libraries build from `.phn` with their suites passing, `tequila/core` among them;
the checker reads the extensions the local `php` has loaded (ext-mongodb and friends) from its
reflection. M5 done — structured concurrency: `concurrently { … }` blocks and `spawn` over amp v3
and the Revolt event loop compile to `Scope::run(…)` / `Scope::spawn(…)` on the
`phaenon/concurrent` package; `tq-cli` (859 `.phn`, fifteen fan-outs as blocks) runs its 2052
tests on the compiled output with `phn check` clean, and `tequila/nats` gained a Revolt transport
so that one connection serves concurrent tasks.

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
phn migrate src tests          # once: Foo.php → Foo.phn, docblock types lifted into syntax, `public function` → `fun`, nested calls as |> pipelines, canonical layout
phn build                      # the roots of phaenon.json → their outputs
phn build --check              # CI: fail when an output is missing, stale or has no source
phn check                      # type-check the roots, minus "exclude"
```

Without a project file, `phn build src tests` writes `Foo.php` next to `Foo.phn` — the adoption
case, a `.phn` at a time in a PHP tree — or under `--out DIR`.

### The PHP target

A project compiles for a **target**, the oldest PHP the output must run on: PHP 8.5 or 8.6. Phaenon
follows PHP (`design/php-versions.md`), so every 8.6 construct parses whatever the target; the
target decides what the output may contain.

```json
{"roots": {"src": ".src", "tests": ".tests"}, "php": "8.6"}
```

- `"php"` in `phaenon.json` states it; `phn migrate --php 8.6 [path...]` writes it there (creating
  the file when there is none, which may then state only the target).
- Without it, the target is the lowest version composer.json's `require.php` admits: `>=8.5`,
  `^8.5`, `~8.5.0`, `8.5.*` and `^8.5 || ^9.0` all mean 8.5.
- Neither: PHP 8.5, with a warning (`P4010`).

A target above what composer.json admits is a warning (`P4008`): an install on the older PHP would
load code it cannot run, so raise `require.php` (`phn` never edits composer.json). A target `phn`
does not compile for — older than 8.5, newer than 8.6 — is an error (`P4009`), and `phn build`
writes nothing then. For an 8.5 target, what PHP 8.6 added is lowered to 8.5 code where the
lowering is exact (partial application in most forms, `#[\Override]` on constants) and reported
otherwise, as "… needs PHP 8.6 (target 8.5)" (`P1002`).

The library is PHP 8.6's whatever the target: a function, class, method, constant or parameter
8.6 added is reported where an 8.5 target uses it, "clamp() needs PHP 8.6 (target 8.5)" (`P4011`),
unless a polyfill declares it or a guard tests for it (`function_exists('clamp')`,
`PHP_VERSION_ID >= 80600`). What PHP deprecates is reported whatever the target, since code compiled
for 8.5 runs on 8.6 too: library symbols (`P4006`, from the stubs' `#[\Deprecated]`) and language
constructs (`P1101`); `--no-deprecations` hides both. Extensions outside php-src are read from the
local `php` (`PHN_PHP` names another), which should be as new as the target (`P4012` otherwise).

`phn migrate` strips `<?php`, `declare(strict_types=1)` and a trailing `?>`, then lifts what
PHPStan code says in comments into the signature: `@template T of Foo` → `<T of Foo>`, `@extends
Base<T>` → `extends Base<T>`, `@param list<T> $xs` → `list<T> $xs`, `@return T` → `: T`, a
property's `@var array<K, V>` → `map<K, V>`. It also spells every named function and method `fun`,
dropping the `public` that Phaenon implies (`public function` → `fun`, `final public static
function` → `final static fun`), drops a constant's `public` (`public const` → `const`) and `public`
before an asymmetric visibility (`public private(set)` → `private(set)`); `--keep-public` keeps all three. A tag is lifted only when its type has a Phaenon
spelling and erases to the type PHP already saw — the generated `.php` keeps the same signatures —
and every tag left in place is reported with the reason (`--keep-docblocks` skips the lift).
"Erases" includes the override-erasure rule: a method that implements a generic signature compiles
with its parameters widened to the implemented method's types, so `@param T $aggregate` over
`Aggregate $aggregate` is lifted when the interface's method takes an `Aggregate`, whatever T's
bound. For that, migrate reads the project's other files and its vendor code through Composer.
Prose survives without its type: `@param T $x prose` becomes `@param $x - prose` (the dash, as in
JSDoc, tells an IDE that expects a type that none follows) and a property's `@var T prose` becomes
its docblock `/** prose */`; the emitter puts the types back and drops the dash. A `@throws X`
without a description becomes `throws X` after the return type, several of them one union
(`throws A|B`); a `@throws` with prose stays. It also runs on `.phn` files, so a project migrated
earlier can be lifted later; a declaration already written in Phaenon keeps its type then.

Next it writes chains of nested calls as pipelines (`design/tooling.md` §8.1): PHP 8.5's `|>`
with PHP 8.6's partial application, which the compiler lowers for an 8.5 target
(`design/php-versions.md` §5.3). `array_values(array_filter(array_map(fn ($u) => $u->email,
$users), fn ($e) => $e !== null))` becomes `$users |> array_map(fn ($u) => $u->email, ?) |>
array_filter(?, fn ($e) => $e !== null) |> array_values(...)`, one stage per line from three
stages (or past 120 columns). The head is the innermost call's one argument that is not a literal,
a constant or a closure, else that call; a stage that takes only the piped value is `f(...)`, any
other `f(a, ?)` or `f(name: ?)`. A chain of three calls or more is rewritten — `--pipes=2` from
two, `--keep-calls` none — and only where the pipeline is exact: the arguments before the nested
call and a method's receiver are literals, constants, variables or closures the inner calls do not
assign, the piped parameter is not by reference, nothing is spread, and the callee resolves
through the program model. Chains in constant expressions and string interpolation, with a
`compact()`-like call, a comment between the calls, another call beside the nested one, or a
layout the pipeline would break (an operand, a stage past 120 columns, one stage per line in the
middle of a line) stay calls. Migrate type-checks the files before and after the rewrite and gives
a file its calls back when its findings, or the type of any expression in it, change; the summary
counts the chains written as pipelines and those left as calls, by reason.

Then it lays the code out in the canonical style (`design/functions.md` §5): the opening brace of a
function, method or closure moves up to the declaration's line, while a class, interface, trait or
enum keeps its brace on a line of its own (moved back down where an earlier migrate moved it up), a
docblock with one line of content becomes one line, and a body that is exactly one `return expr;`
— or one expression statement under `void` or `never` — becomes `=> expr;`, wrapped after the
arrow when the line would pass 120 columns. A body with a comment inside stays a block, as do
constructors, destructors and generators. These and the pipelines are the only edits that change
a file's line count (beside docblock lines whose tags were lifted); `--keep-layout` skips the
layout. Re-running migrate on its own output changes nothing.

The generated file is laid out as PSR-12 wants it wherever the compiler writes or rewrites
something: the open tag, a marker and `declare(strict_types=1)` on lines of their own (Phaenon is
strict by default; a file that declares `strict_types` itself keeps its own), the imports it adds,
the docblocks it writes, expression bodies as blocks, and the opening brace of a function, method
or class-like on the line after its signature. Plain PHP passes through as written. So
php-cs-fixer runs on the outputs with the project's own rules and no exceptions for them. Every
compiled file ends with a **line map**, a comment the tools that report positions read
(`design/line-map.md`):

```php
<?php

/* @generated by phaenon from Foo.phn */

declare(strict_types=1);

namespace App;
…
// phaenon-map v1 ../src/Foo.phn 1-6=1 7:1 15-17=9 18:9
```

`__LINE__` compiles to the `.phn`'s line. Everything else that reports a line — a stack trace,
PHPUnit, PHPStan, a warning — reports the compiled file's, and `phn trace` turns it into the
source's:

```sh
vendor/bin/phpunit | phn trace                    # .tests/FooTest.php:42 → tests/FooTest.phn:37
vendor/bin/phpstan analyse --error-format=raw | phn trace
phn trace --root /app=. < production-trace.txt    # /app/.src/Foo.php(18) → src/Foo.phn(9)
```

It rewrites `file.php:42`, `file.php(42)` and `file.php on line 42` where the file carries a map,
and leaves the rest of the text as it is. In production, `phaenon/runtime`'s Monolog processor and
Sentry integration do the same for logged exceptions, and the PhpStorm plugin reads the map for
navigation and debugging (`design/line-map.md` §4).

A library ships its `.phn` sources and no compiled PHP (`roadmap.md` D13): with `phn` and the
library pinned, the output is reproducible byte for byte. The Composer plugin that builds every
installed package on `composer install` is M6 work; until then a consumer runs `phn build` in
`vendor/<package>`.

## Declarations

```php
final class Account
{
    private int $balance = 0;

    fun balance(): int => $this->balance;

    /** Takes money out, never below zero. */
    fun withdraw(int $amount): void throws InsufficientFunds {
        if ($amount > $this->balance) {
            throw new InsufficientFunds($amount);
        }
        $this->balance -= $amount;
    }

    static fun open(): self => new self();
}
```

`fun` declares a named function or method, `public` unless a visibility is written; `function`
stays valid. A named function may have **one expression** for its body, `=> expr;`: returned, or
only evaluated under `void` and `never`; not on constructors, destructors or generators (`P1004`).
It compiles to the PSR-12 block it stands for. **`throws A|B`** after the return type names
the exceptions the function lets escape; each must be a `Throwable` (`P2319`), and callers are not
held to it (no checked exceptions). It compiles to a `@throws` tag on a line of the declaration's
docblock, or of a new one above the declaration. Design: `design/functions.md`.

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
phn migrate [--keep-docblocks] [--keep-public] [--keep-layout] [--keep-calls] [--pipes=N] [--php X.Y] path... # rewrite .php as .phn (and re-lift .phn); --php writes the target
phn stdlib --out DIR [--check]                         # the compiled stdlib (Bytes, Debug/…), as phaenon/runtime ships it
phn stdlib --concurrent-out DIR [--check]              # the compiled Scope, as phaenon/concurrent ships it
phn lex   [--php] file                    # token stream
phn print [--php] file                    # parse and pretty-print
```

`--php` reads plain PHP (with `<?php` tags); the default reads Phaenon (code from byte 0).

## Development

```sh
go test ./...                                   # unit tests
PHAENON_PHP_SRC=/abs/path/to/php-src go test ./internal/conformance/   # lexer, parser, printer round trip
PHAENON_EXEC=1 PHAENON_PHP_SRC=/abs/path/to/php-src go test ./internal/conformance/ -run Execution -timeout 30m
PHAENON_PHP_SRC=/abs/path/to/php-src-8.6 PHAENON_PHP=/path/to/php8.6 go test ./internal/conformance/   # a release's corpus under its php
PHAENON_PHP_SRC=/abs/path/to/php-src go generate ./stdlib              # refresh the stubs and the version table
```

`stdlib/php-src` holds the `*.stub.php` files of php-src's newest supported release branch, and
`stdlib/versions_gen.go` the release each library symbol appeared in, both written by `stdlib/gen`
from `git archive` of the release branches (the clone is never touched). `go run ./stdlib/gen -php
/path/to/php8.6` also rewrites `stdlib/constants.php` from that php; `-check` verifies instead.

The execution tests run every `Zend/tests` file through `php` twice — once as written and once
after a transformation — and compare the output. `TestPrinterExecution` checks the pretty-printer
(line numbers normalised away); `TestEmitExecution` checks `migrate` + `build` byte for byte, line
numbers included. ≈2.5 min each. `PHAENON_PHPT_FILTER=ext/standard/tests` narrows any suite to a
corpus subset; `PHAENON_SHOW_DIFFS=n` controls how many differences are listed.

`internal/migrate/testdata/lift/*.php` are migrated and compared with the `.phn` and `.notes`
next to them (`PHAENON_UPDATE=1` rewrites them); the output must parse, migrate to itself and
compile to the signatures the input declared. With `PHAENON_PHP86`, the `pipes*` fixtures run as
written and compiled for 8.5 and 8.6, and must print the same. `testdata/lift/vendor/` (PSR-4 `Acme\`) holds the parents
declared in other files, vendor PHP and project `.phn`.

`internal/emit/testdata/*.phn` are compiled through the checker and the emitter and compared
with the `.php` next to them (`PHAENON_UPDATE=1` rewrites them); the output must pass `php -l`,
and PHPStan at level max when `PHAENON_PHPSTAN` is set.

The checker's golden corpus lives in `internal/checker/testdata`: each `.phn` carries the expected
diagnostics as `// want P2103` comments on the offending lines. `PHAENON_PHPSTAN=/path/to/phpstan`
additionally runs PHPStan at level max over the same corpus and fails when PHPStan reports
something the corpus does not expect — the checker may be stricter than PHPStan, never more
lenient. `PHAENON_COMPOSER_PROJECT=/path/to/a/composer/project` exercises the vendor reader.

License: MIT.
