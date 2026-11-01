---
type: backlog
tags: [phaenon, backlog]
updated: 2026-11-01
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
  `check` since 2026-10-31; ~~a project file (`phaenon.json`) can come when the Composer plugin
  does~~ — `phaenon.json` since 2026-11-01 (`1e8c359`: roots → sibling outputs, `exclude`,
  asset copy, orphan removal; roadmap D12), core's branch on it.
- ~~`bytes` into a `string` parameter that PHP code declares~~ — decided 2026-10-31: an explicit
  `(string)` at the boundary (strings.md §5); the twelve sites in core cast (`016fb9c` on its
  dogfood branch).
- ~~`@phpstan-assert-if-true =T` prefix~~ — parsed since 2026-10-31 (exact form; checker.md §9).
- ~~`json_encode(…, JSON_THROW_ON_ERROR)` typed `string|false`~~ — `string` since 2026-10-31.
- ~~Runtime package review notes~~ — guarded 2026-10-31 in the `.phn` sources: `Str::indexOf`
  answers `null` for an offset outside the string, `Str::replaceAll` throws `ValueError` on an
  empty search string, `Seq::unique` compares strictly (`../runtime` `619873b`).
- PSR-3 `log($level)` implementations: `P2516` wants a type; `mixed $level` is the spelling (the
  interface leaves it untyped, so `mixed` is what it is). No compiler change; core's test loggers
  got it 2026-10-31.
- **PHPStan rules deliberately not matched** (the eleven findings left on core, 2026-10-31):
  regex analysis (`preg_match('/^[a-z]+$/', $s)` making `$s` a `non-empty-string`);
  `Closure::call($obj)` / `bindTo()` rebinding `$this` inside the closure (and its scope, for
  private members); expression-level throw points in a `try` (a by-reference argument defined
  before the call that throws is defined in the catch); a method call on a union receiver
  checking arguments against the union of the members' parameter types; phpredis's
  `multi()->set()->del()` chain, on which PHPStan is silent for reasons its stubs do not show;
  `@template B of self::*_REFERENCE` (a constant wildcard as a bound). Each is a small PHPStan
  extension or scope feature; pick up when a dogfood target makes one matter.

## M5 — concurrency (2026-11-02)

- **`phn check` against `composer.json`**: a file that uses `concurrently` in a project that does
  not require `phaenon/concurrent` fails at runtime, not at check time — the compiler knows the
  amp API from its embedded declarations (`stdlib/concurrent/amp/`), so it cannot tell. A P4xxx
  warning from the project's `composer.json` / `installed.json` is the fix (`concurrency.md` §7).
- **The embedded amp declarations shadow the vendor's**: `LoadStubs` declares `Amp\Future` and
  friends eagerly, so a project's real `amphp/amp` is never consulted for them. Signatures follow
  3.1; a method missing there is a declaration to add, not a checker bug.
- **`Scope::first` / a race** (C6) and a `Scope::settle` that returns results and failures side
  by side: library additions when a use site appears.
- **PhpStorm plugin**: the lexer approximation of `concurrency.md` §11 is in (2026-11-02): a
  statement block parses as a block, `spawn expr` as `expr`, both words coloured as keywords. The
  expression forms (`return concurrently { … };`, `spawn { … }`) are syntax errors in the editor
  until a PSI element for a block exists (M6, with the LSP); `spawn` results are typed as the
  value, not `Future<T>`.
- ~~**Dogfood** `tq-cli`~~ — done 2026-11-02 ([[dogfood]]). `tequila/nats`: the `RevoltTransport`,
  the pump lease and the opt-in reader task are on the local branch `feat/revolt-transport`
  (`~/labs/tequila/nats-revolt`), the ADR amendment on the platform's `docs/nats-revolt-transport`;
  neither pushed, waiting for a maintainer's go-ahead.
- ~~**tq-cli's 378 checker findings**~~ — `src` at 0 (2026-11-02). Left: **213 in `tests/`**, which
  tq-cli's PHPStan never analysed; most are what PHPStan 10 reports too. Whether a dogfood's tests
  must pass `phn check` is open.
- **`map<string, …>` and PHP's key normalisation**: `$out[(string) $k] = …` is typed string-keyed
  (as PHPStan does), but PHP stores a numeric string key as an int, so `map<string, mixed>` from a
  YAML/JSON helper can hold int keys at runtime. Retyping tq-cli's `Val::map()` to
  `map<int|string, mixed>` raised 122 errors. A design question for the type lattice.
- **bytes ergonomics** (from tq-cli's zip builder, relay frames, archives): no `.` / `.=` for
  `bytes`, so builders become a `list<bytes>` and `Bytes::concat()`; a text header inside binary
  needs `('tree '.\strlen($b)."\0")->bytes()`; `file_get_contents()` returns `string`, so every
  binary read is `(bytes) $content`.
- **The emitter copies `bytes` verbatim from written docblock tags** (`@return bytes`,
  `@var list<bytes>`), while the docblocks it generates erase it to `string`; with
  `use Phaenon\Bytes;` in the file, PHPStan reads the tag as the class (`strings.md` §5 says it
  erases).
- **Closure types cannot spell refined ints**: `Closure(positive-int): bytes` and
  `Closure(int<1, max>): bytes` are P1000 (hyphenated names are docblock vocabulary,
  `generics.md` §11.2, and `int<a, b>` has no native spelling).
- **The emitter's automatic import collides with a file's own `use` block**: it writes
  `use Phaenon\Str;` on line 1, and php-cs-fixer then fails `blank_line_after_namespace` /
  `no_extra_blank_lines` in files that have their own imports. Writing the import by hand works
  (`5519a67` made such an import count as used).
- The emitter's `use` clause is syntactic: a variable passed by reference to a function inside
  the block (`sort($rows)`) is not seen as assigned and is captured by value; a variable assigned
  in the block and conditionally assigned after it before being read is not captured. The
  checker's flow analysis knows both; a hint from it would make the clause exact.

## M6 — tooling, analysis rules

- **Throw analysis** — dead `catch` blocks, `@throws` checking (PHPStan `catch.neverThrown`;
  allow-listed in the parity test).
- **Purity / unused result** — PHPStan `function/method/expr.resultUnused`, `void.pure`,
  `@phpstan-pure`; allow-listed.
- **Dead code / unused members** — `method/property/constant/classConstant.unused`; allow-listed.
- **LSP**, `phn fmt` (`.phn` files read as inline HTML to php-cs-fixer and plain editors), a
  persistent cache for vendor parsing (`checker.md` §10 *Speed*).
- **Composer plugin + `phn` as a Composer package** (roadmap D13, decided 2026-11-01): libraries
  ship `.phn` only; the plugin runs `phn build` in every installed package with a `phaenon.json`
  on install/update (`config.allow-plugins` answers the trust question once), the binary pinned
  per platform. Until then the odm/object-mapper/reflector dogfood branches still commit
  side-by-side `.php` and a consumer builds `vendor/<pkg>` by hand; moving them to the sibling
  layout is part of this item. `phn build --check` should also pass when the only "failures" are
  `--emit-on-error` files whose output is current (core: 8 files, 11 deliberate findings).
- **The `.phn` beside `.php` adoption case in the IDE**: without a project file both are indexed;
  the plugin prefers the source for navigation and the duplicate inspection, but find-usages and
  rename still see both. A `FileTypeOverrider` for a `.php` with a `.phn` sibling would hide it
  entirely.

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

- `phn migrate` could rewrite `static function (…) { return …; }` in attribute arguments and constants back
  to `fn (…) => …` (design/functions.md §2); today it leaves closures as written.
- `fun` keyword completion in the plugin is the contributor's own lookup; PhpStorm's keyword completion
  does not know it, nor do its live templates.

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
  29 tests, verifier compatible with 2025.1. ~~The pseudo-class stubs are hand-written and will
  drift~~ — generated since 2026-10-31 by `phn stdlib --plugin-stubs` from `stdlib/runtime/*.phn`
  (`TestPluginStubsFresh`); the plugin parses every dogfood tree (`PHAENON_TREES=…`). Left:
  `is`/`as` do not narrow beyond a class name; user generics' type arguments (`Box<User>`) are
  dropped; `type` aliases are erased whole; the provider uses undocumented PhpStorm API (noted in
  its README). 2026-11-01 (`dd2af30`): `phaenon.json` outputs excluded from the index
  (`PhaenonExcludePolicy`, re-applied on project-file changes), a `.phn` declaration wins over its
  `@generated` copy in navigation and the duplicate-declaration inspection (34 → 39 tests). A
  typeless `@param $x prose` is mis-parsed by PhpStorm itself (`prose` as the type): the language
  writes `@param $x - prose` (D14) rather than the plugin filtering three inspections. The
  "missing type declaration" inspections on erased properties, parameters and return types are
  filtered since `2a9b153` (the readonly error already was). ~~Names inside erased types are
  text, not references~~ — since `0a22b5d` erasure goes to a token of its own
  (`PHAENON_ERASED`, a comment to the parser) whose leaf carries a reference per class name:
  navigation, find-usages, rename and the unused-import inspection work; since `65a70f3`
  completion inside `<…>` (classes with import insertion, type parameters, keywords, and the
  members of a namespace after a qualifier) and no spell-checking of the erased leaf; a type parameter navigates to its `<T of …>` declaration, the declaration shows its usages (erased constructs and docblock types), and the hierarchy check skips methods with erased types (found on core's `AbstractAggregate::id(): T`); `fun` is the `function` keyword to the parser and the highlighter, with the PSR-12 visibility finding and the constant-expression findings filtered (`603db11`). A class constant without `public` is public too since `208cb21` / `4689034`. Left: the leaf is a
  `PsiComment`, so any other comment-based feature sees Phaenon types as comments. PhpStorm 2025.1 does
  not parse `[...$a][0]` in plain PHP either (core's `DeadlineMetricsTest.phn:114`); the
  real-tree test lists such files instead of counting them.
- The implied `declare(strict_types=1)` is not modelled.
- Editor colouring relies on the internal extension point `com.jetbrains.php.baseLexerProvider`.

## Before the repositories go public

- `AGENTS.md`, `.mcp.json` and this vault name paths on the development machine (`~/labs/…`,
  `~/.local/bin/serena`). Generalise or move them before publishing.
- `tequila/reflection`'s `phaenon-dogfood` branch exists only locally.

← [[Home]]
