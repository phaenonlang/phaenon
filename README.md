# Phaenon

A typed dialect of PHP 8.5+ that compiles to idiomatic PHP — "TypeScript for PHP". The compiled
output depends on nothing but PHP (`design/roadmap.md` D24): the compiler adds the header,
`declare(strict_types=1)`, docblocks, and PHP's own functions and classes where the source used a
construct it lowers, and never a call into a package. Phaenon adds types and the spelling of
declarations, never an operation PHP lacks (D26): a PHP developer reads a `.phn` and knows what
every line does.

- `phn` — the compiler (this repository, Go)
- `phaenon/debug` — opt-in source positions in production: the line map read for Monolog and
  Sentry
- [`phaenonlang/design`](https://github.com/phaenonlang/design) — the language design documents, a
  repository of their own: a path such as `design/checker.md` below is a file there

Status: pre-alpha; the language and the CLI still change between releases. What works today:

- `phn check`, the type checker: PHP plus Phaenon's syntax (generics, `list<T>` and `map<K, V>`,
  tuples, `bytes`, methods without a keyword, `throws`) and the PHPStan docblock vocabulary;
  it reports what PHPStan reports at level max, and may be stricter, never more lenient.
- `phn build`, the emitter: the PHP you would have written, with PHPStan docblocks, laid out as PER
  Coding Style 3.0, each file ending with a line map back to its `.phn`.
- The `phaenon/phn` Composer plugin: the pinned compiler, and the builds at every autoload dump.
- `phn fix`, `phn fmt`, `phn lsp`, `phn trace` and `phn migrate`, which turns `.php` into `.phn`
  and lifts PHPStan's docblock generics into syntax.
- The PhpStorm plugin and the VS Code extension, and the playground on
  [phaenon.dev](https://phaenon.dev), the compiler built for WebAssembly.

The lexer, parser and printer are conformance-tested against php-src's `.phpt` corpus with `php`
itself as the oracle. The language is developed against a production PHP codebase, the Tequila
Platform's, from its libraries to its services, whose own suites run on the compiled output.

## Type checking

```sh
phn check src tests            # diagnostics with codes, source lines and carets; exit 1 on errors
phn check --json src           # one JSON object per diagnostic, for editors
phn check --format=github      # workflow commands: the findings as annotations on the .phn lines
phn check --format=checkstyle  # Checkstyle XML, for other CI systems
phn build src                  # type-checks first; a file with errors is not written
phn build --emit-on-error src  # …unless asked to: a codebase adopting the checker builds while it fixes
phn build --check --emit-on-error  # in CI: the outputs are current; files with errors pass as that build writes them
```

The checker reads the rest of the program without configuration: project files, vendor code
through the nearest `vendor/composer` autoload maps, and the php-src stubs embedded in the binary
(refined by an overlay for what PHP cannot express: `array_map` returning `array<K, R>`,
`ReflectionClass<T>`, PHPUnit's assertions). Docblocks are the interop contract: `@param`,
`@return`, `@var`, `@template`, `@extends`, `@phpstan-assert` and the PHPStan type grammar are
understood, and `@phpstan-ignore` comments are honoured. A `.phn` docblock may use Phaenon's own
spellings, as an array shape often does (`array{by: map<string, int>, pair: (int, int)}`); the
emitter writes them in PHPStan's. A declared `array` names its element type (`array<V>` for a list
or keyed array of `V`, `array<mixed>` for any), unless the method it overrides documents one: a
bare `array` is `P2516`, as PHPStan at level max reports it. Diagnostic codes are stable: `P1xxx`
removed constructs, `P2xxx` types and flow, `P4xxx` interop (docblocks, vendor). Design:
`design/checker.md`.

Dead code is reported as warnings, as PHPStan reports it at level max: a private method, property
or constant nothing in its class uses (`P2406`), and a private property only written (`P2407`) or
only read (`P2408`). A private property that carries a framework's attribute (a mapping, a
serializer group) is left alone, since the framework reaches it through reflection: by default any
attribute but PHP's own. For a stricter check, `phaenon.json` names the attributes that count,
their subclasses and implementations included; `[]` lets none count:

```json
{"roots": {"src": ".src"}, "unused": {"reflectedAttributes": ["Doctrine\\ORM\\Mapping\\MappingAttribute"]}}
```

Design: `design/tooling.md` §9.2.

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
(fixtures another analyser owns); `phn build` compiles them all the same. `unused` narrows which
attributes spare a private property the dead-code warnings (above).

`committed` lists the roots whose outputs the repository tracks, each a key of `roots`; an entry
that is not one is an error of the project file:

```json
{"roots": {"src": ".src", "tests": ".tests"}, "committed": ["src"]}
```

It is for a library published for PHP projects at large, which a project that has no `phn` then
installs as the plain PHP it compiles to (`roadmap.md` D28). In the library's own project the key
changes nothing: `phn build`, `phn watch` and the editor build a committed root as any other, which
is how its outputs are made and kept current. It speaks where the library is an installed package:
`phn build --vendor` leaves such a root as installed, with no output written, no stamp and nothing
removed, and builds the package's other roots as before. A committed root that came without its
outputs (no output directory, or a `.phn` with no `.php` there) fails the command with an error
naming the package and the root, and nothing is built in its place: the same release is broken for
every project without `phn`. A project written in Phaenon still reads such a package from its
`.phn`, as it reads any installed Phaenon package.

```sh
phn migrate src tests          # once: Foo.php → Foo.phn, docblock types lifted into syntax, `public function title()` → `title()`, nested calls as |> pipelines, canonical layout
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
property's `@var array<K, V>` → `map<K, V>`, `@param array<array-key, V> $xs` → `array<V> $xs`, and
a declared `array` that has no element type and inherits none → `array<mixed>`. It also lifts
`function` off every method, dropping the `public` that PHP implies (`public function title()` →
`title()`, `final public static function create()` → `final static create()`), while a function
and a method named after a modifier, `var`, `const`, `Closure`, `function` or `fn` keep
`function`; it drops a constant's `public` (`public const` → `const`) and `public` before an
asymmetric visibility (`public private(set)` → `private(set)`); `--keep-public` keeps all three. A tag is lifted only when its type has a Phaenon
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

`--list-types` (opt-in, for code below PHPStan level 6) types a bare `array` declaration from the
values it holds: a private property's writes, a private or final method's returns, a private
method's arguments become `list<T>`, `map<K, V>`, `array<V>` or a tuple, in up to five rounds. A
file whose findings grow gets its arrays back, and every candidate left is reported with the
reason (`design/tooling.md` §8.2).

Then it lays the code out in the canonical style (`design/functions.md` §5): the opening brace of a
function, method or closure moves up to the declaration's line, while a class, interface, trait or
enum keeps its brace on a line of its own (moved back down where an earlier migrate moved it up), a
docblock with one line of content becomes one line, and a body that is exactly one `return expr;`
— or one expression statement under `void` or `never` — becomes `=> expr;`, wrapped after the
arrow when the line would pass 120 columns. A body with a comment inside stays a block, as do
constructors, destructors and generators. These and the pipelines are the only edits that change
a file's line count (beside docblock lines whose tags were lifted); `--keep-layout` skips the
layout. Re-running migrate on its own output changes nothing.

The generated file is laid out as PER Coding Style 3.0 wants it wherever the compiler writes or
rewrites something: the open tag, a marker and `declare(strict_types=1)` on lines of their own
(Phaenon is strict by default; a file that declares `strict_types` itself keeps its own), the
imports it adds, the docblocks it writes, expression bodies as blocks, the opening brace of a
function, method or class-like on the line after its signature, an empty body as `{}` on the
declaration's line, and `fn(` in the arrow functions it writes. Plain PHP passes through as
written. So php-cs-fixer runs on the outputs with a PER set (`@PER-CS3x0`) and no exceptions for
them; `@PSR12` would expand every `{}`, which is the source's text, and `phn fix` would carry
that into the `.phn` (design D22). Every
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
vendor/bin/phpstan analyse --error-format=github | phn trace   # annotations GitHub can show
phn trace < clover.xml > clover.phn.xml           # coverage on the .phn lines
phn trace --root /app=. < production-trace.txt    # /app/.src/Foo.php(18) → src/Foo.phn(9)
```

It rewrites `file.php:42`, `file.php(42)` and `file.php on line 42` where the file carries a map,
and leaves the rest of the text as it is. A GitHub workflow command (`::error file=…,line=…::`)
gets the source's file and lines; its columns go, since the map holds lines. GitHub shows no
annotation on a file the repository lacks, and the compiled outputs are gitignored, so PHPStan's
annotations show only once traced. A report on stdin — Checkstyle, JUnit (PHPUnit's
`--log-junit`, PHPStan's `junit`), Clover or Cobertura — is read whole and rewritten as that
report, byte for byte where nothing points into a compiled file. In coverage, the compiled lines
that come from one source line count as that line: covered when any of them is. In production, `phaenon/debug`'s Monolog processor and
Sentry integration do the same for logged exceptions, and the PhpStorm plugin reads the map for
navigation and debugging (`design/line-map.md` §4).

PHP's rewriting tools run on the outputs too, where their configuration already points, and
`phn fix` carries what they change back to the `.phn`:

```sh
phn fix -- tools/php-cs-fixer/vendor/bin/php-cs-fixer fix   # the fixer's changes, in the .phn
phn fix -- vendor/bin/rector process                        # Rector's
phn fix --check -- tools/php-cs-fixer/vendor/bin/php-cs-fixer fix   # each change at its .phn line; nothing written
```

A change of what the source wrote is applied to the source, and so is a deletion whose ends lie
in it. A change of text the compiler wrote — the header, a docblock, the `function` of a method
declared without it, an erased type — is not: the rebuild writes that text anew. `phn fix` then
rebuilds and runs the tool again, carrying what has become the source's, for three rounds at
most, and reports what the tool still changes; the exit status is 1 then (`design/tooling.md`
§4). For php-cs-fixer and Rector, each reported change names its rule, `php-cs-fixer
(binary_operator_spaces) changes: …`, from the tool's own JSON report, which `phn fix` asks for.

`phn fmt` formats the `.phn` in two layers. The first is Phaenon's layout, with no options:
spaces for tabs (a tab outside a string or a comment's text doesn't compile, `P1005`), a
function's brace on its declaration's line, a class-like's on its own, `{}` for an empty body,
one-line docblocks, and the spacing of what only Phaenon has (`class Box<T of Item>`,
`map<string, list<int>>`, `Closure(int): bool`, `throws A|B`).
The second is the project's own PHP style: its php-cs-fixer, run through `phn fix`.

The first layer also rewrites the spellings the language withdrew on 2026-11-09, so that a tree
crosses the change with one command (`design/tooling.md` §5.6): `fun` goes before a method's name
and becomes `function` before a function's, `function` is lifted off every method that does not
need it (the modifiers stay as written), and a leftover `is`, `as` or `as?` becomes the PHP test
it compiled to — `$x is int` → `is_int($x)`, `$b is string` on `bytes` →
`mb_check_encoding($b, 'UTF-8')`. Most of those depend on the operand's type, so `phn fmt` in a
project checks the files that hold one first; `phn fmt --stdin`, with no program, rewrites only
`fun`, `function` and `$x is Foo` with a class name, and reports the rest (`P1001`).

```sh
phn fmt                 # the roots: Phaenon's layout, then the fixer (phaenon.json "fmt", or tools/php-cs-fixer)
phn fmt src/Foo.phn     # given files only
phn fmt --check         # CI: fail when either layer would change a file; nothing written
```

In editors, `phn lsp` is the checker as a language server (LSP over stdio, `design/tooling.md`
§6). It checks a file as it is typed, against the whole project with the open buffers as they
are, and the whole project on each save, building the outputs after it as `phn build` does. It
answers hover, go to definition, document symbols, completion (after `|>`, the functions that
take the left side), signature help and formatting (the first layer). The PhpStorm plugin
(`../jetbrains`) starts it for diagnostics and build on save only, since PhpStorm's own PHP
support answers the rest. `phn watch` checks and builds after each change, for a terminal.

```sh
phn lsp                 # the project around the working directory, on stdin and stdout
phn watch               # check and build the roots each time a file changes; --no-build checks only
phn fmt --stdin         # the first layer, standard input to standard output (the plugin's Reformat Code)
```

A library ships its `.phn` sources and no compiled PHP (`roadmap.md` D13): with `phn` and the
library pinned, the output is reproducible byte for byte, and the Composer plugin below builds it
where it is installed.

### Installing

A Composer project takes it as a plugin:

```sh
composer require phaenon/phn
```

`phaenon/phn` is a Composer plugin (`design/tooling.md` §3). It brings the `phn` binary for the
platform, pinned by its SHA-256 in the package, so `composer.lock` pins the compiler too, and links
it as `vendor/bin/phn`. When Composer writes the autoloader, it compiles every installed package
that has a `phaenon.json` (each for its own target, its findings shown as warnings, skipped while
nothing changed: `phn build --vendor`), then the project itself (`phn build`), so that
`--classmap-authoritative` finds the outputs. With no `phaenon.json` anywhere it does nothing.
A root an installed package lists under `committed` (above) is left as installed, and a package
whose installed roots are all committed is nothing to build: alone, it makes the plugin neither
fetch the binary nor run it. A package that came without the outputs it commits fails the command
before any binary is fetched, with the error `phn build --vendor` gives.
It requires no other package, since the compiled output needs none, and suggests
`phaenon/debug`, which points production errors at `.phn` positions (`design/line-map.md` §4.3).
It conflicts with `phaenon/runtime` below 0.4, that package's name until then, which declares the
same classes.

- `PHN_BINARY=/path/to/phn` uses another binary, such as one built from a checkout.
- `"extra": {"phaenon": {"binary-url": "https://mirror/{version}/{file}"}}` fetches the binary from
  a mirror, with the credentials of `auth.json`.
- `"extra": {"phaenon": {"build": false}}` leaves the project's own build to its workflow.
- `composer install --no-autoloader` builds nothing; the `dump-autoload` that follows does
  (`--no-scripts` does not skip the plugin).

Without Composer, `phn` is one static binary. Each
[release](https://github.com/phaenonlang/phaenon/releases) carries it for Linux and macOS (amd64,
arm64) and Windows (amd64), with the playground's WebAssembly build and the source tree; their
SHA-256 sums are in `composer/checksums.json` at the release's tag. From source, with the Go
toolchain `go.mod` names:

```sh
go install github.com/phaenonlang/phaenon/cmd/phn@latest
```

A build from source calls itself `dev` in `phn version`.

## Declarations

```php
final class Account
{
    private int $balance = 0;

    balance(): int => $this->balance;

    /** Takes money out, never below zero. */
    withdraw(int $amount): void throws InsufficientFunds {
        if ($amount > $this->balance) {
            throw new InsufficientFunds($amount);
        }
        $this->balance -= $amount;
    }

    static open(): self => new self();
}

function opened(): Account => Account::open();
```

A method is declared without a keyword: after its modifiers, a name and its parameters, `public`
unless a visibility is written. It compiles to `public function balance(): int`, PER's head. A
function keeps PHP's `function`, and so does a method named after a modifier, `var`, `const`,
`Closure`, `function` or `fn` (`public function static()`); `function` stays valid on any method.
`fun`, the keyword before 2026-11-09, is `P1001`, which `phn fmt` rewrites.
A named function may have **one expression** for its body, `=> expr;`: returned, or
only evaluated under `void` and `never`; not on constructors, destructors or generators (`P1004`).
It compiles to the block it stands for. **`throws A|B`** after the return type names
the exceptions the function lets escape; each must be a `Throwable` (`P2319`), and callers are not
held to it (no checked exceptions). It compiles to a `@throws` tag on a line of the declaration's
docblock, or of a new one above the declaration. The throw analysis reads it, as it reads
`@throws`: a catch nothing in its `try` block can reach (`P2403`), a member the body never throws
(`P2404`, on what no override can replace), and `@throws void` on a body that throws (`P2405`).
Design: `design/functions.md`, `design/tooling.md` §9.1.

## Generics

```php
final class Repository<T of Model>
{
    __construct(private readonly class<T> $class, private EventStore $store) {}
    get(Uuid $id): ?T { … if ($root instanceof $this->class) { return $root; } … }
    fresh(Uuid $id): T { return new $this->class($id); }
}
final class UserRepository extends Repository<User>
{
    __construct(EventStore $store) { parent::__construct(User::class, $store); }
}

function first<T>(list<T> $items): ?T { return $items[0] ?? null; }
```

Type parameters are erased: `first()` compiles to `function first(array $items): mixed` with
`@template T`, `@param list<T> $items` and `@return ?T` above it, so PHPStan, PhpStorm and a PHP
caller see ordinary typed PHP. A type parameter has no runtime value; when a class needs its `T`
at runtime it takes a `class<T>` — the type of `User::class`, a plain string — the way PHP
projects already write it, and `UserRepository` is an ordinary autowirable service. The compiler
never adds parameters or members: the output contains no identifier you did not write.

A type test is PHP's own — `is_int($x)`, `$x instanceof User`, `is_array($x) &&
array_is_list($x)`, `$x === null`, `in_array($x, ['a', 'b'], true)` — and the checker narrows
through it as PHPStan does; a checked value is PHP 8's `is_int($x) ? $x : throw new
\TypeError(…)`. A guard of your own is a function or method with `@phpstan-assert`,
`@phpstan-assert-if-true` or `@phpstan-assert-if-false`, which the checker honours. A tuple is a
type only, with no runtime test. The operators `is`, `as` and `as?` that Phaenon had until
2026-11-09 are `P1001`, and `phn fmt` writes the PHP test each compiled to. Design:
`design/generics.md` §11.5.

`bytes` is a PHP string at runtime, so PHP cannot tell it from text. The checked conversion to
text is PHP's own test: `mb_check_encoding($b, 'UTF-8')`, with the encoding written as a literal,
narrows a `bytes` operand to text, `string&bytes`, where it holds —
`mb_check_encoding($b, 'UTF-8') ? $b : throw new \ValueError('not valid UTF-8')`.
`(bytes) $s` retypes a string as binary data and `(string) $b` is the unchecked way back, both
free. `hex2bin()` returns `bytes|false`, and
`bin2hex()`, `base64_encode()` and `mb_scrub()` take bytes; `base64_decode()` keeps PHP's
`string`, as base64 carries text as often as binary, and `(bytes) base64_decode($s)` retypes the
binary case. The check needs `ext-mbstring`, or `symfony/polyfill-mbstring` where it is missing.
Design: `design/strings.md` §6.

A generic declaration's docblock adds lines to the output (PHPStan reads one tag per line);
every other edit keeps line numbers exact. Design: `design/generics.md`, decisions in §11.

## CLI

```sh
phn build [--out DIR] [--check] [--emit-on-error] [--exclude PATH]... [path...] # type-check and compile .phn → .php; no paths: phaenon.json
phn check [--format=text|json|github|checkstyle] [--no-deprecations] [--exclude PATH]... path... # type-check .phn (and .php) files
phn fix [--check] -- command [arg...]                 # run php-cs-fixer, Rector… on the outputs; carry their changes back to the .phn
phn fmt [--check] [file...]                           # Phaenon's layout, then the project's fixer through phn fix; no files: the roots
phn fmt --stdin                                       # Phaenon's layout of standard input, onto standard output
phn lsp                                               # the language server: diagnostics, hover, definition, completion, build on save
phn watch [--no-build] [--interval D]                 # check and build the roots each time a file changes
phn migrate [--keep-docblocks] [--keep-public] [--keep-layout] [--keep-calls] [--pipes=N] [--list-types] [--php X.Y] path... # rewrite .php as .phn (and re-lift .phn); --php writes the target
phn stdlib --out DIR [--check]                         # the compiled stdlib (Debug/…), as phaenon/debug ships it
phn build --vendor                        # the installed packages with a phaenon.json, as the plugin builds them, but for the roots they commit
phn trace [--root PREFIX=LOCAL]... [text...]  # positions in compiled files → .phn: text, workflow commands, XML reports on stdin
phn version                               # the version a release sets, dev otherwise
phn lex   [--php] file                    # token stream
phn print [--php] file                    # parse and pretty-print
```

`--php` reads plain PHP (with `<?php` tags); the default reads Phaenon (code from byte 0).

## Repositories

The language's other parts live beside this repository in the
[`phaenonlang`](https://github.com/phaenonlang) organisation; [phaenon.dev](https://phaenon.dev) is
its site.

- [`design`](https://github.com/phaenonlang/design) — the language design and every decision
- [`debug`](https://github.com/phaenonlang/debug) — the Composer package `phaenon/debug`, source
  positions in production for Monolog and Sentry
- [`jetbrains`](https://github.com/phaenonlang/jetbrains) — the PhpStorm plugin
- [`vscode`](https://github.com/phaenonlang/vscode) — the VS Code extension
- [`website`](https://github.com/phaenonlang/website) — phaenon.dev: the landing page, the
  playground and the release downloads

## Development

[`CONTRIBUTING.md`](CONTRIBUTING.md) says what a pull request must satisfy; [`AGENTS.md`](AGENTS.md)
maps the code, its tests and every test variable.

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

`PHAENON_COMPOSER=$(which composer) go test ./internal/distribution` runs the `phaenon/phn` plugin
under a real Composer in temporary projects: the builds at the autoload dump, the download and its
checksum, the launcher. `go run ./tools/release -version X.Y.Z -out dist` builds the release
binaries and writes their checksums (`-verify` compares instead); two builds from the same sources
and toolchain give the same bytes.

License: [MIT](LICENSE).
