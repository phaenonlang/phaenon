---
type: backlog
tags: [phaenon, backlog]
updated: 2026-10-29
---

# Open and deferred items

Consciously cut to keep each milestone shippable with PHPStan parity — to be revisited, never
silently dropped. When a milestone touches an item, pick it up; when it is done, strike it here
(`~~item~~ — done YYYY-MM-DD, <commit>`). Sources: `../design/generics.md` §11 *Still open*,
`../design/checker.md` §10, the *Open* lines of each roadmap entry.

## Dogfood

- ~~**`phn migrate` for generics**~~ — done 2026-10-28 (`generics.md` §11.1); object-mapper
  migrated. Left behind by it:
  - ~~A constructor-level `@template` used only by a promoted property stays a docblock~~ — lifted
    natively since 2026-10-29 (`generics.md` §11.2).
  - `tequila/object-mapper` `tests/` carry 16 findings that PHPStan never saw (its `paths` is
    `src` only): Symfony container `get()` typing (needs a phpstan-symfony-like overlay) and
    untyped closure parameters in fixtures. The tests are built with `phn build --emit-on-error tests`.
- **`tequila/mongodb-odm`** (2026-10-29, [[dogfood]]): migrated and built on `phaenon-dogfood`; the
  checker reports 40 findings in `src`, all of PHPStan's level-9/10 kind (`mixed` from Symfony
  config arrays and untyped `array` parameters flowing into typed ones) — PHPStan at level max
  reports the same sites, the repo runs at level 8. ~~Decided 2026-10-29: the library goes to
  level max on `main`~~ — PR tequila/mongodb-odm#45 merged 2026-10-30; the dogfood branch was
  recreated on it and `phn check` reports 0 errors.
- ~~**Should `phn build` emit on type errors by default?**~~ — decided 2026-10-29 (roadmap D10): no
  output for a file with type errors; `--emit-on-error` is the opt-in for a codebase adopting the
  checker.
- ~~**Existential templates**~~ — decided 2026-10-29 (`generics.md` §11.2): a constructor's type
  parameter may type a promoted property; migrate lifts them natively, no new spelling.
- **Erasure that follows the overridden signature**: `DateTimeNormalizer::denormalize(mixed
  $value)` with `@param UTCDateTime $value` must stay `mixed` natively because the interface's
  parameter is a type parameter erased to `mixed`. Phaenon could let the override declare
  `UTCDateTime $value` and erase it to the parent's erased type with a generated `@param`. Design
  question for `generics.md`.
- Trait **properties** used on `$this` in a trait body are not yet looked up in the using classes
  (methods are, since 2026-10-29).

## M4 — collections and strings

- ~~**`P2516` (missing declared type)** as a warning until M4~~ — an error (`collections.md` §7).
- ~~**`array-key` as native syntax**~~ — decided 2026-10-29 (`generics.md` §11.2): hyphenated
  PHPStan names stay docblock vocabulary; `array-key` is written `int|string`, M4 decides `map`'s
  key bound.
- **Array shapes as native syntax** — docblocks and alias bodies only today; a shape cannot be
  tested at runtime (emitter error).
- **Phaenon vocabulary inside user docblocks** in `.phn` (`@var map<int, T>`) passes through
  untranslated, so PHPStan on the output does not know `map`. Decide: translate, or forbid docblock
  types in `.phn`.
- **Shapes vs classes** (`checker.md` §10) — whether shapes survive as a language feature.

## M4 — left after the first slice (2026-10-30)

- ~~`?->` on a list / map / string receiver~~ — done 2026-10-30: a guard `null === $x ? null :
  Str::f($x)` when the receiver is a side-effect-free path, `P2519` otherwise
  (`collections.md` §3.1).
- ~~A `\Stringable` receiver gets the `Str` methods~~ — done 2026-10-30.
- ~~`phn build --no-types`~~ — removed 2026-10-30; `--emit-on-error` builds a file with findings.
- ~~`phn migrate`: `array{A, B}` → `(A, B)`~~ — done 2026-10-30 (two or more elements; `(A)`
  would read as a cast).
- The runtime package `../runtime` gets `Seq`, `Map`, `Str`, `Bytes` from `phn stdlib --out
  src` (the `.phn` sources are the one source; `phn build` cannot compile them, the embedded
  copy already declares the classes). Pending: the package's tests and a freshness check.
- ~~`Bytes.phn`, the `(bytes)` cast, `P2530`, the overlay's binary producers~~ — done 2026-10-30.
- ~~`P2516` to an error, `P2518` (`map` key bound)~~ — done 2026-10-30 (`P2516` had been an error
  since the checker landed; the comment said otherwise).

## tequila/core dogfood (2026-10-30)

- ~~`phn check` has no exclude configuration~~ — `--exclude PATH` (repeatable) on `build` and
  `check` since 2026-10-31; a project file (`phaenon.json`) can come when the Composer plugin does.
- **`bytes` into a `string` parameter that PHP code declares** (restated 2026-10-31; the earlier
  "PHP-mode files" framing was wrong, every file in core is `.phn`): `Uuid::fromBinary(string)`,
  `MessageId::fromBinary(string)`, `assertStringContainsString()` take binary in `string` because
  PHP has no other word; twelve calls in core, eight of them `Uuid::fromBinary($binary->getData())`.
  Today each needs `(string)`; the alternative is that a `string` declared in a PHP-mode file
  accepts `bytes` (strings.md §5, open bullet). A maintainer's decision.
- ~~`@phpstan-assert-if-true =T` prefix~~ — parsed since 2026-10-31 (exact form; checker.md §9).
- ~~`json_encode(…, JSON_THROW_ON_ERROR)` typed `string|false`~~ — `string` since 2026-10-31.
- ~~Runtime package review notes~~ — guarded 2026-10-31 in the `.phn` sources: `Str::indexOf`
  answers `null` for an offset outside the string, `Str::replaceAll` throws `ValueError` on an
  empty search string, `Seq::unique` compares strictly (`../runtime` `619873b`).
- PSR-3 `log($level)` implementations: `P2516` wants a type; `mixed $level` is the spelling (the
  interface leaves it untyped, so `mixed` is what it is). No compiler change; core's test loggers
  got it 2026-10-31.

## M6 — tooling, analysis rules

- **Throw analysis** — dead `catch` blocks, `@throws` checking (PHPStan `catch.neverThrown`;
  allow-listed in the parity test).
- **Purity / unused result** — PHPStan `function/method/expr.resultUnused`, `void.pure`,
  `@phpstan-pure`; allow-listed.
- **Dead code / unused members** — `method/property/constant/classConstant.unused`; allow-listed.
- **LSP**, `phn fmt` (`.phn` files read as inline HTML to php-cs-fixer and plain editors), a
  persistent cache for vendor parsing (`checker.md` §10 *Speed*).

## Generics follow-ups (unscheduled)

- ~~Symfony DI warning for reified services~~ — moot since 2026-10-29: `reified` is gone
  (`generics.md` §11.2).
- ~~`$x is class<User>` lowered to the runtime~~ — native since 2026-10-29: `\is_string($x) &&
  \is_a($x, User::class, true)`; bare `class` tests `class_exists || interface_exists`. Only a
  non-class argument (`class<object>`, a union) still uses the runtime descriptor.

- **`--runtime-guards=boundary|all|off`** (`generics.md` §6) — not built.
- **Descriptor hoisting** — `static $__t ??= Type::list(...)` for `is` / `as` in hot paths.
- **Warning for an erased type parameter nothing constrains** (§3.4 item 4, a `P3010` candidate) —
  silent for now, like PHPStan.
- **`callable(int): R`** signatures are docblock-only; `Closure(int): R` is native (the parser
  handles the `(int)` cast token after `Closure`).
- **Line drift** below generic declarations ([[gotcha-phpstan-one-tag-per-line]]) — a `phn` line-map
  subcommand or plugin support could map `.php` stack-trace lines back to `.phn` if it bites.
- **Monomorphisation** (§7) — open question.

## Small things

- **PHPUnit overlay:** `assertSame` / `assertNotEmpty` narrowing was removed because the docblock
  reader does not support `@phpstan-assert =T` or the `!empty` forms; add when needed.
- ~~**Runtime stub drift**~~ — `classString()` and `resource()` added to the stub 2026-10-28; nothing
  checks the stub against `../runtime/src/Type.php` yet ([[gotcha-runtime-stub-drift]]).
- ~~**`checker.md` §8 item 5**~~ — fixed 2026-10-28.
- ~~**`treatPhpDocTypesAsCertain`**~~ — decided 2026-10-29 (`checker.md` §9 *Docblock types are
  not certain*): impossibility diagnostics need the PHP types to agree; the parity harness runs
  PHPStan with the option off.
- **`design` repo tracks GoLand files** — `.idea/*.xml`, `.idea/.gitignore` and a `go.mod` reading
  `module design` (found 2026-10-28; cf. [[gotcha-goland-rewrites-go-mod]]). Whether to untrack them is a
  maintainer's call.

## PhpStorm plugin (`../jetbrains`)

- No Phaenon-only syntax yet: generics, `is` / `as`, `type` aliases show as PHP syntax errors.
- ~~**M4 ships with editor support for the method syntax**~~ — done 2026-10-31 (`../jetbrains`
  `43db968`, `bbb8bc4`, `f7a05b0`): Phaenon syntax is erased to whitespace tokens in front of
  PhpStorm's parser (`PhaenonLexer`, eager function bodies), `$x is Foo` becomes `instanceof`,
  `(bytes)` a string cast; `list`/`map`/`string`/`bytes`/`\Stringable` receivers get
  `Seq`/`Map`/`Str`/`Bytes` completion, resolution and chain typing through bundled pseudo-classes
  (`src/main/resources/stubs/phaenon/Phaenon/Internal/*Methods.php`) and a `PhpTypeProvider4`;
  29 tests, verifier compatible with 2025.1. Left: the pseudo-class stubs are **hand-written from
  `stdlib/runtime/*.phn` and will drift** — generate them (a `phn` subcommand); `is`/`as` do not
  narrow beyond a class name; user generics' type arguments (`Box<User>`) are dropped; `type`
  aliases are erased whole; the provider uses undocumented PhpStorm API (noted in its README).
- The implied `declare(strict_types=1)` is not modelled.
- Editor colouring relies on the internal extension point `com.jetbrains.php.baseLexerProvider`.

## Before the repositories go public

- `AGENTS.md`, `.mcp.json` and this vault name paths on the development machine (`~/labs/…`,
  `~/.local/bin/serena`). Generalise or move them before publishing.
- `tequila/reflection`'s `phaenon-dogfood` branch exists only locally.

← [[Home]]
