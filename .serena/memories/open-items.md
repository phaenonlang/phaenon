---
type: backlog
tags: [phaenon, backlog]
updated: 2026-11-03
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
  - ~~`tequila/object-mapper` `tests/` carry 16 findings that PHPStan never saw (its `paths` is
    `src` only): Symfony container `get()` typing (needs a phpstan-symfony-like overlay) and
    untyped closure parameters in fixtures. The tests are built with `phn build --emit-on-error tests`.~~
    — done 2026-11-02 in the tests (object-mapper `df0e2a4`, [[dogfood]]): the fixtures' closures
    take their source class, the integration tests assert the container's mapper. A
    phpstan-symfony-like overlay for `get(Foo::class)` would still spare other projects the
    assertion.
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
- ~~**Erasure that follows the overridden signature**: `DateTimeNormalizer::denormalize(mixed
  $value)` with `@param UTCDateTime $value` must stay `mixed` natively because the interface's
  parameter is a type parameter erased to `mixed`. Phaenon could let the override declare
  `UTCDateTime $value` and erase it to the parent's erased type with a generated `@param`. Design
  question for `generics.md`.~~ — done 2026-11-02: the override-erasure rule (`generics.md` §4.3
  and the §11 row) already compiles it that way; `phn migrate` now predicts the rule and lifts such
  tags (`74ce488`), the checker holds the widened parameter to what PHP checks (`3490338`), and the
  emitter applies the rule down a chain of Phaenon classes (`c39ae1c`). odm's two normalizers wait
  for the tree's uncommitted re-migration ([[dogfood]]).
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
  (`collections.md` §3.1). Moot since D20 (2026-11-03, `08edf59`): the method syntax is gone,
  `P2519` is retired.
- ~~A `\Stringable` receiver gets the `Str` methods~~ — done 2026-10-30; removed by D20.
- ~~`phn build --no-types`~~ — removed 2026-10-30; `--emit-on-error` builds a file with findings.
- ~~`phn migrate`: `array{A, B}` → `(A, B)`~~ — done 2026-10-30 (two or more elements; `(A)`
  would read as a cast).
- ~~The runtime package `../runtime` gets `Seq`, `Map`, `Str`, `Bytes` from `phn stdlib --out
  src`~~ — since D20 (2026-11-03, runtime `057fcf9`) it holds `Type`, `Bytes` (its six checked
  conversions) and `Debug`, compiled from `stdlib/runtime/**/*.phn`, with its tests and
  `TestRuntimePackageFresh`.
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
  empty search string, `Seq::unique` compares strictly (`../runtime` `619873b`). The three classes
  went with D20 (2026-11-03).
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

## M5 — concurrency (2026-11-01)

- **`phn check` against `composer.json`**: a file that uses `concurrently` in a project that does
  not require `phaenon/concurrent` fails at runtime, not at check time — the compiler knows the
  amp API from its embedded declarations (`stdlib/concurrent/amp/`), so it cannot tell. A P4xxx
  warning from the project's `composer.json` / `installed.json` is the fix (`concurrency.md` §7).
- **The embedded amp declarations shadow the vendor's**: `LoadStubs` declares `Amp\Future` and
  friends eagerly, so a project's real `amphp/amp` is never consulted for them. Signatures follow
  3.1; a method missing there is a declaration to add, not a checker bug.
- **`Scope::first` / a race** (C6) and a `Scope::settle` that returns results and failures side
  by side: library additions when a use site appears.
- ~~**PhpStorm plugin**: the expression forms (`return concurrently { … };`, `spawn { … }`) are
  syntax errors in the editor until a PSI element for a block exists~~ — done 2026-11-02 without
  one (`../jetbrains` branch `feat/class-string-and-php86`, PhpStorm plugin section below): a
  block used as a value and `spawn` reach PhpStorm as the closures they compile to.
- ~~**Dogfood** `tq-cli`~~ — done 2026-11-01 ([[dogfood]]). `tequila/nats`: the `RevoltTransport`,
  the pump lease and the opt-in reader task merged as tequila/nats#13, the ADR amendment as
  tequila/platform#255 (2026-11-01). C8 done; M5 closed the same day.
- ~~**tq-cli's 378 checker findings**~~ — `src` at 0 (2026-11-01), ~~213 in `tests/`~~ at 0
  (2026-11-01, [[dogfood]]). Whether a dogfood's tests must pass `phn check` is still open.
- ~~**Checker more lenient than PHPStan** (seen on tq-cli's compiled tests, 2026-11-01): offset
  reads on `mixed` are silent (PHPStan `offsetAccess.nonOffsetAccessible`, 88 sites there); a
  variadic `string ...$args` is `list<string>`, where PHPStan says `array<int|string, string>`
  because named arguments collect under their names (`ControlTest.php:75`, `LockChangeTest`).~~
  — done 2026-11-02, `546e2b0` (`../design/checker.md` §9): an offset on `mixed`, `object`,
  `iterable` or an open type parameter is `P2302`, destructuring `mixed` too, a `foreach` over
  them `P2303`; a variadic is `array<int<0, max>|string, T>` in its body. Of the dogfood trees
  only tq-cli's `tests/` gain findings, 85, each on a line PHPStan reports on the compiled
  output: 29 reads of a `json_decode()` result, 50 reads and writes down `map<string, mixed>`
  fixtures, 2 `foreach` over decoded values, 4 variadics used as a `list<string>`. Their fixes
  in the tree are pending.
- ~~**Parity gaps of the same family still open** (found 2026-11-02 while probing PHPStan 2.2.5
  at level max): method calls and property access on `mixed` (`method.nonObject`,
  `property.nonObject`; 8 property reads in tq-cli's compiled tests); spreading `mixed`
  (`arrayUnpacking.nonIterable`, `argument.unpackNonIterable`); destructuring `int`, `string`,
  `null`, `?array` or `iterable` (`offsetAccess.nonArray`); an offset read on an interface or
  abstract class that is not `ArrayAccess` (exempted on 2026-10-30, `32e5ded`, on the belief
  that PHPStan is silent; 2.2.5 reports it); `static $cache = []` typed from its initialiser
  where PHPStan says `mixed`.~~ — done 2026-11-02, `d9123cb` (`../design/checker.md` §9): also
  members of `object` (P2006/P2005 unless `method_exists()`/`property_exists()` proved them),
  `unset()` on what has no offsets, class offsets under `isset()`/`??`; a static variable is
  its `@var`, `mixed` without one. Dogfood: only object-mapper's tests gain findings (14 for 8
  mixed arguments). The 8 property reads in tq-cli's compiled tests are PHPStan not reading the
  `@param list<SourceSpec>` the emitter writes on a closure (the item below), not a gap.
  Deliberately not matched: PHPStan reports `$r = []; $r['a']['b'] = 1` as an offset on
  `mixed`, which PHP runs without complaint.
- **Still more lenient than PHPStan at level max** (2026-11-02): operators on `mixed` —
  `$n++`, `$a + 1`, `.=` (`postInc.type`, `binaryOp.invalid`, `assignOp.invalid`; 6 binary
  operations in tq-cli's compiled tests); an undefined property of a final class under
  `isset()` / `??` (`property.notFound`); `method_exists()` on `mixed`, which PHPStan narrows
  to `class-string|object` and still reports a call on.
- **PHPStan ignores `@param` / `@return` docblocks on closures and arrow functions** (2.2.5,
  any placement): the docblock the emitter writes before a closure with a `map<…>` or tuple
  parameter is invisible to PHPStan, which sees the erased `array`.
- **A method-call path keeps its narrowing across impure calls**: `assertTrue($repo->isDirty('x'))`,
  a `Run::git()` commit, then `assertFalse($repo->isDirty('x'))` is "always false" and the rest
  unreachable (P2401 on tq-cli's `GitopsRepoTest.phn:69`). PHPStan remembers the same `true`
  but has no rule that reports it. Needs a purity notion (PHPStan's functionMetadata) to
  invalidate on calls with side effects.
- **A property narrowed by assignment stays narrowed across a closure call** (`$this->github = [];
  $go();`): unsound in both checkers; tq-cli's tests reset through a void method instead.
- Loop widening: `for ($try = 0; $try < 50; ++$try)` gives `int<min, 49>` inside, PHPStan
  `int<0, 49>`. Precision only.
- ~~**`map<string, …>` and PHP's key normalisation**~~ — decided and done 2026-11-01
  (`../design/checker.md` §9, `1d9d6ff`): a key whose type is known numeric is an int key
  (literals, `decimal-int-string` = `(string)` of an int, the int part of `numeric-string`, a
  `(string)` cast looked through member by member); a general `string` key stays a string key
  on purpose. tq-cli's thirteen findings closed the same day (`87fdad7`, `ad9ca25`): names are
  refused at the boundary with the loader's own messages (a numeric key in a build set threw
  `TypeError` before), data stays `map<int|string, …>` (the free-form `config:` trees, `unpack()`
  results), `Val::map()` leaves int-keyed entries out and the callers that see lists use
  `Val::arr()`. Quoting does not help: PHP turns `"5"` into `5` either way.
- **bytes ergonomics** (from tq-cli's zip builder, relay frames, archives): ~~no `.` / `.=` for
  `bytes`~~ — `bytes . bytes` is `bytes` since 2026-11-01 (`strings.md` §2, §6; `07557a3`),
  a buffer starts from `(bytes) ''`; tq-cli's builders use it (`b66fabf`, output byte-identical).
  Left: a text header inside binary needs
  `('tree '.\strlen($b)."\0")->bytes()`; `file_get_contents()` returns `string`, so every
  binary read is `(bytes) $content`.
- ~~**The emitter copies `bytes` verbatim from written docblock tags**~~ — done 2026-11-01
  (`5d4e3a1`): the type of a written tag erases as a generated one does, on its line. A type
  spread over several docblock lines is still copied as written; `map<…>` and the other
  Phaenon vocabulary in written tags are the M4 item above, still undecided.
- **Closure types cannot spell refined ints**: `Closure(positive-int): bytes` and
  `Closure(int<1, max>): bytes` are P1000 (hyphenated names are docblock vocabulary,
  `generics.md` §11.2, and `int<a, b>` has no native spelling).
- ~~**The emitter's automatic import collides with a file's own `use` block**~~ — done
  2026-11-01 (roadmap D11, `39079ed`): the import shares the line of its neighbour in the
  block (alphabetical place when sorted). One php-cs-fixer rule, `single_line_after_imports`,
  still fails on an output that gets an import (plus `blank_line_between_import_groups` when
  the block holds only function or constant imports): a dogfood's cs config turns it off beside
  the four line-1 rules, or the import is written by hand (`5519a67` made that count as used).
  tq-cli has one such file, in `tests/` (`StoredZipTest`), outside its cs finder.
- The emitter's `use` clause is syntactic: a variable passed by reference to a function inside
  the block (`sort($rows)`) is not seen as assigned and is captured by value; a variable assigned
  in the block and conditionally assigned after it before being read is not captured. The
  checker's flow analysis knows both; a hint from it would make the clause exact.

## PHP 8.6 — partial application (2026-11-02)

Decisions are in `../design/php-versions.md` §9; these are what the first build left.

- A closure lowered for 8.5 with two or more parameters whose types PHP cannot declare carries one
  inline `@var` each on one line, and PHPStan reads the first tag of a line only
  ([[gotcha-phpstan-one-tag-per-line]]): the second goes unread. Needs a way to say it without a
  line, or a recorded exception.
- In a pipe, a bound argument that writes through a by-reference parameter of a call nested in it
  (`$x |> f(g($m), ?)` with `g(&$m)`) writes the lowered closure's copy; only direct assignments,
  `++`/`--` and `yield` are refused.
- ~~Method syntax on lists and strings with placeholders (`$names->map(?)`) is not lowered to the
  `Seq`/`Str` call; in a pipe the stage's method target is kept, outside a pipe nothing is done.~~ — moot since D20 (2026-11-03): the method syntax is gone.
- PHP's "named parameter overwrites previous argument" is reported for partial applications
  (`P2010`) but not for ordinary calls.
- An internal function's `= UNKNOWN` default that `...` or a named argument would need (PHP:
  "must be passed explicitly, because the default value is not known") is not reported; the
  lowering falls back to `mixed ...$args`.
- A callee returning by reference: PHP 8.6's closure returns by reference, the lowered one by value.
- Once the parity PHPStan parses PHP 8.6, run it on the 8.6 emitter output and on the checker
  goldens with placeholders (both skipped now).
- `../design/php-versions.md` §5.4 still says a `void` stage feeding another warns; §9 made it
  `P2205`. Align the draft text when the milestone closes.
- tq-cli on PHP 8.6 meets one 8.6 deprecation, the class constant `StatusCommand::NAMESPACE`
  (`namespace` is case-insensitive); the deprecation checks belong to the library branch.

## M6 — tooling, analysis rules

- **Throw analysis** — dead `catch` blocks, `@throws` checking (PHPStan `catch.neverThrown`;
  allow-listed in the parity test). Since D18 (2026-11-02) it should read the `throws` clause
  (`../design/functions.md` §4.1), and match PHPStan 2's `throws.unusedType`, which level max
  reports for a clause the body never throws (allow-listed beside `catch.neverThrown`).
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
  layout is part of this item. ~~`phn build --check` should also pass when the only "failures" are
  `--emit-on-error` files whose output is current~~ — done 2026-11-03 (slice 2): `--check
  --emit-on-error` passes them, core's and tq-cli's `build:check` pass the flag.
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
- **Emitter goldens are only `php -l`-ed** (2026-11-02): a signature PHP rejects when it links the
  classes — an override that narrows a parameter, as the emitter compiled a grandchild of a generic
  class until the override-erasure rule followed the parent's compiled signature — passes `php -l`
  and is caught only by PHPStan's `method.childParameterType`. Loading each output with `php` (where
  it declares only classes) would catch it without PHPStan.

## PHP 8.6 — library and checker (2026-11-02)

Decisions are in `../design/php-versions.md` §9; what was left:

- **`stdlib/constants.php` comes from PHP 8.6.0-dev**: regenerate with the 8.6.0 release
  (`go run ./stdlib/gen -php …`, [[testing]]).
- **P2515 (uninitialised readonly property) is declared but never reported**: no analysis checks
  that a readonly property is assigned before the constructor returns. When it is written, a
  default (PHP 8.6) counts as the initialisation.
- **`#[\Override]` on properties** (PHP 8.5) is not checked; methods and, since 8.6, constants are.
- **Version guards** are read in `if`/`elseif`/`else` and early exits only, not in `?:`, `&&`
  operands or `match (true)`; a guard in those positions still leaves P4011.
- **PHPStan 2.2 lags PHP 8.6** (refuses readonly defaults, `#[\Override]` on constants,
  `__debugInfo()` on enums; misses members 8.6 added): the per-file allowances in
  `phpstan_test.go` go once the parity PHPStan supports 8.6.
- **Library deprecations need the stubs' attribute**: runtime-only ones (`SessionHandlerInterface`
  without `validateId()`, objects passed to `array_walk()`, `http_build_query()`,
  `mb_convert_variables()`, `ReflectionProperty::setValue()` with a foreign object, 16+
  `php://filter` filters) are not reported.

## D18 — expression bodies, `throws`, canonical layout (2026-11-02)

Built in the compiler (decisions: `../design/functions.md` §8). Left:

- ~~**Re-lift of the dogfood trees** (phase 2)~~ — done 2026-11-02, once the PhpStorm plugin read
  `=>` bodies and `throws`: reflector `d4f21d2`, object-mapper `725aaea`, mongodb-odm `24c0b66`,
  core `148c08ec`, tq-cli `29bdcf3`, each followed by a `.git-blame-ignore-revs` commit; counts,
  checks and the token comparison in [[dogfood]]. Core's and tq-cli's php-cs-fixer configs turn
  `braces_position` and `class_definition` off; the in-place libraries' cs skips generated files.
  Core's `tests/PHPStan/Data` fixtures had nothing to lift under `--keep-layout` and were left out
  (the rule tests assert the line of each finding in them).
- **core: two sources not re-lifted** — `src/EventSourcing/Repository/AbstractEventSourcedRepository.phn`
  and `src/Infrastructure/CQRS/Command/MetadataFactory/AttributeBasedCommandMetadataFactory.phn`
  carried uncommitted edits during the re-lift and keep the old layout. Once those edits are
  committed, `phn migrate` the two files and commit the result (measured on the last commit's
  versions: 2 bodies as `=>`, 8 braces, 1 `@throws`, 119 → 103 lines), and add its hash to
  `.git-blame-ignore-revs`.
- ~~**core: `single_line_after_imports` on a compiler import**~~ — gone with D19 (2026-11-02): a
  compiler import takes a line of its own, so the uncommitted edit to
  `AbstractEventSourcedRepository.phn` that drops `use …\Aggregate` no longer fails cs.
- **Prefix expressions' spans end at their operator** — `throw $e`, `print $x`, `clone $o`, `!$x`,
  casts, `include`, `yield from`, `$$x` (and a `->` property fetch's name): the struct literals in
  `internal/parser/expr.go` compute `S` before parsing the operand. Found while building D18;
  `internal/migrate/layout.go` measures an expression to the statement's `;` instead. Fixing the
  parser needs a look at every pass that reads those spans' ends.
- **An empty body keeps two lines** after the brace moves up (`fun __construct(private Foo $f) {`
  / `}`); collapsing it to `{}` is `phn fmt`'s choice (PER-CS's `single_line_empty_body`).
- **A docblock below the attributes** (`#[A] /** … */ fun f()`) is read by PHP's reflection but not
  by PHPStan; the emitter writes generated tags into a docblock of their own above the attributes
  (`4d6898d`). `phn migrate` could move such a docblock above the attributes.

## D19 — the line map in production (2026-11-02)

`phaenon/runtime`'s `Phaenon\Debug` (compiler `efdf21b`, runtime `6b0a83d`; design
`../design/line-map.md` §4.3):
- ~~**Checker parity gap**: the checker kept a narrowed static property across a static call~~ —
  fixed 2026-11-02 (`testdata/call-invalidation.phn`): a void (or constructor) method of the class's
  own hierarchy, called on `$this` or statically, forgets `$this->…` and the hierarchy's static
  members, keeping readonly properties and another class's private ones, as PHPStan does.
- `Throwable::getTrace()` is plain `array` to the checker; `ThrowableRewriter.phn` carries an
  `@var` with PHPStan's frame shape. Refining the overlay would change dogfood findings, so it waits
  for a run over the five trees.
- Not translated yet: positions inside exception messages (a `TypeError`'s "called in X.php on
  line N"), Symfony's `SilencedErrorContext` (deprecation logs) and Monolog `ErrorHandler`'s plain
  `context.file`/`context.line`, anonymous class names, Sentry profiling and span locations.
- The per-process map cache does not notice a compiled file changing under a long-running worker;
  acceptable for deployed code, not for a dev server that rebuilds under a running worker.
- Windows paths are handled lexically but untested.

## D19 — the compiled layout and the line map in the compiler (2026-11-02)

The emitter's side of `../design/line-map.md` (§2, §3, §4.1), `phn trace`, and migrate's class
braces (§7). Left:

- **Coverage** (§5): ~~PHPUnit's Clover/Cobertura output reports compiled lines~~ — done
  2026-11-03, `phn trace` reads both (§ M6 — CI formats below). PhpStorm's coverage view
  still reports compiled lines; the plugin reading the map for its view is left.
- ~~**Dogfood rebuild**~~ — done 2026-11-02 ([[dogfood]] § D19: re-lifted, empty bodies `{}`, no
  compiled-tree cs exceptions, every pipeline green). Was: the five trees' compiled outputs change layout; rebuild them, drop the
  compiled-tree exceptions from core's and tq-cli's `.php-cs-fixer.dist.php` (the four line-1
  rules, `braces_position`, `class_definition`, `single_line_after_imports`), rerun each suite, and
  re-migrate the `.phn` so class-like braces go back down ([[dogfood]]). Measured beforehand on
  the trees as they are: core's 812 and tq-cli's 585 compiled sources pass their projects' rules
  with no exception ([[testing]] § The line map). Core's rule tests assert the lines of
  `tests/PHPStan/Data` fixtures, which go back to `.php` and are copied (§5).
- **php-cs-fixer on compiled output, beyond layout** (measured on the emitter goldens with core's
  rules, no exceptions): `use_arrow_functions` (`@PHP74Migration:risky`) turns a closure in a
  constant expression into `static fn`, which PHP 8.5 refuses there — a project with such closures
  needs the rule off; `void_return` fires on a `concurrently` block in statement position
  (`Scope::run(function () use (…) {`). Neither is the layout's; core and tq-cli have neither.
- **A one-line body with several statements** (`{ $a = 1; return $a; }`) gets its braces on lines
  of their own, the statements stay on one line: `no_multiple_statements_per_line` then reports the
  source's own layout. `phn fmt` (M6) territory.
- **`__LINE__` inside text another lowering copies** (a pipe's left operand moved into a closure)
  stays `__LINE__` and reports the output's line.
- **`phn stdlib` writes no map**: the runtime and concurrent packages ship without their `.phn`, so
  their positions are PHP positions. Revisit if the packages ever ship sources.
- **The `<?php // phaenon-map` spelling is never written**: Phaenon source has no close tag, so a
  compiled file always ends in PHP mode; the readers accept the spelling (vectors) for a future
  writer.
- **Conformance**: the emit execution suite compares tests that read a line as a value (an error
  handler's `$errline`, `getLine()`, reflection's start line, `debug_backtrace()` taken as data, an
  assertion callback) with digits blinded, counted apart ([[testing]]); the map cannot reach those
  numbers.

## Checker strictness noted with D19 (2026-11-02)

- `self::assertNotNull(self::make()); self::make()->go();` — a static call path narrowed by an
  assertion — is P2105 in `phn check` and accepted by PHPStan (it remembers a possibly impure
  call's value until the next impure call). Stricter than PHPStan, so not a parity bug; predates
  fef6091.
- `../jetbrains/src/test/testData/reflector/*.phn` are older copies of the reflector sources; half
  differ from the tree since D18/D19. Refresh them when the plugin's fixtures are next touched.

## D20 — no method syntax on values that are not objects (2026-11-03)

Decision: `../design/roadmap.md` D20. Compiler `46e4c93`, `08edf59`, `98b663d`; runtime `057fcf9`;
plugin `77b1d1b`, `b2a79cb`; tq-cli's nine sites `725f47a` ([[dogfood]]). Left:
- **The plugin's hover for variables**: a variable holding a `list`, `map` or `bytes` reads
  PhpStorm's `User[]` / `string`; declarations still print as written. The pseudo-classes carried
  the Phaenon spelling; getting it back needs another marker in the type that is not a class.
- **`map<K, V>` has no key type in the editor**: PhpStorm holds none for an array, and the
  pseudo-class's type argument was where it lived.
- **Pipe-aware completion** in the plugin and the LSP (after `|>`, the functions whose first
  parameter takes the left side's type, inserted as `f(?, …)`): `../design/tooling.md` §6.1,
  M6 slice 4. PhpStorm 2025.1 does not parse `|>` (PHP 8.5 language level).
- **`phn stdlib` deletes no file whose source is gone**: the runtime's `Seq.php`, `Map.php`,
  `Str.php` were removed by hand (AGENTS.md says so).
- **The runtime's PHP 8.3 floor** in its composer.json has lost its stated reason
  (`mb_str_pad`, used by `Str`); align it with the compiler's floor when slice 1 versions the
  packages.
- Core's call on `bool|string|Redis` (`RedisIdempotencyStore.phn:66`) is now `P2105`, one of the
  eleven deliberate findings: PHPStan is silent on it because of its phpredis stubs, the overlay
  types the chain as `bool|string|Redis`.
- **The `pipes` codemod** (compiler `ecbde14`, `a8230c1`; `../design/tooling.md` §8.1, decisions
  T11–T13) is merged and on by default. 2026-11-03: the 8.5 output folds (compiler `0b7af16`,
  `93ed297`: the checker records `File.Folds` with the shared rule of `internal/evalorder`, the
  emitter writes a folding run as nested calls, `fold.go`); the codemod follows the data and takes
  hook-free property receivers (`c57c0a0`); the check no longer undoes pipelines over renumbered
  anonymous classes (`9027c50`, core had lost 23). Applied to the five dogfood trees and
  committed after review (core 72 pipelines `eb69b190`, tq-cli 594 `32c98c1`, mongodb-odm 4
  `27486f7`, each with a blame-ignore entry; reflector and object-mapper had no chain that
  qualifies; suites, PHPStan, cs green, findings unchanged). Open:
  - ~~The 8.5 output of a pipeline reads poorly~~ — done 2026-11-03 (T11).
  - ~~Anonymous-class capture bug~~ — done 2026-11-03, `0c1ecf9`, `a22893c`, `8b4495b`
    (`ast.InspectScope`). Still skipping anonymous classes whole: the yield and goto walks
    (`bodyHasYield`, `firstYield`, `noCrossing`), so a `yield` in a constructor argument goes
    unseen.
  - ~~Rule questions~~ — decided 2026-11-03 (T12, T13).
  - A prefix expression's span ended at its operator (`(string) $n`, `!$x`, `throw $e`): fixed in
    the parser (`cb5a93b`); the codemod's `fullSpan` workaround is gone.
  - Single-argument assertions end pipelines (`… |> self::assertFalse(...)`: 22 in core's tests,
    21 in tq-cli's), which the data rule allows: the value is the first parameter.
  - A folded run's lines are one hold of the pipeline's first source line in the line map: a
    breakpoint on a later stage line goes to the next statement.
  - Per-chain notes reach the API (`PipeReport.Notes`) and the goldens, not the CLI.
  - Unconfirmed checker issue: a promoted parameter's `new X()` default, typed while another file
    is checked, resolved `X` in that file's scope (`Ci\Validate\NewSecretReads` against the imported
    `Secrets\NewSecretReads`).

## M6 — distribution (slice 1, 2026-11-03)

`../design/tooling.md` §3. Left after slice 1:
- **v0.1.0 is released** (2026-11-03): phaenonlang/phaenon#1, tag `v0.1.0` (five binaries and
  `phn-0.1.0-src.tar.gz`, verified by the tag's workflow), `phaenon/runtime` and
  `phaenon/concurrent` `v0.1.0`. The first PR showed CI's setup-go building with go1.27.3 against a
  local go1.27.2, so `tools/release` forces go.mod's toolchain line (`ec90670`); with it a macOS
  build matches the Linux one byte for byte. Fixed after the tag, for 0.1.1: a failed download
  printed a promise stack trace (`b3b8d30`).
- **No download path for a private repository**: `releases/download/…` answers 404 even with a
  GitHub token (private assets come only through the API's asset endpoint), so until the
  repositories are public the plugin downloads only from a mirror. Publishing the binaries on the
  registry's release-asset door for Tequila's CI (`extra.phaenon.binary-url`) is a Tequila-side
  step not taken yet; locally, `PHN_BINARY` or a compiler checkout's `bin/phn`.
- A source install of `phaenon/phn` (`--prefer-source`, a dev version from VCS) is a checkout too:
  it holds `go.mod`, so the plugin wants `PHN_BINARY` or a `bin/phn` built there.
- PHPStan on the plugin against Composer 2.3 (the floor `composer-plugin-api ^2.3` admits) reports
  two deliberate findings: `AutoloadGenerator::dump()` takes an eighth argument from 2.4 on (the
  locker, which keeps the autoloader suffix the lock's hash), and 2.3 types `getInstallPath()` as
  `string`. Against 2.11 it is clean.
- The dogfood trees take `phaenon/phn` from this checkout (core `3b57e3bd`, tq-cli `eccc939`), so
  the plugin runs `bin/phn`: rebuild it (`go build -o bin/phn ./cmd/phn`) after changing the
  compiler, or set `PHN_BINARY`.

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

- ~~**D18 in the editor: expression bodies, `throws`, the canonical layout**~~ — done 2026-11-02
  (`../jetbrains` branch `feat/d18`, not merged: `4db044d` parsing, `4451c65` `throws`, `05e2afd`
  layout, `3de6c95` generated code, docs `eeceaa1`):
  - Parsing: an expression body reaches PhpStorm's parser as the block it compiles to, the arrow's
    two characters split into `{` and `return` (`{` and an erased `>` under `void` / `never`),
    the block closed by `PhaenonPsiBuilder` after its one statement; `throws A|B` after a named
    function's parameters or return type is erased whole. The lexer follows a named function's
    header and ends it at the first token no return type has, so an array's `=>` below a signature
    still being typed stays PHP's. `PSI_VERSION` 6.
  - The body is code (completion, navigation, types), its value checked as a `return`'s; nothing
    reports a missing `return`. `throws` names are references (find usages, rename, Optimize
    Imports); completion after `throws` offers Throwables.
  - PhpStorm's exception analysis reads `@throws` only, with no extension point: the filter drops
    "Unhandled X" / "Unhandled exceptions" and the missing-`@throws` finding where the clauses the
    compiled tags would carry answer every exception PhpStorm computed (its own sets, through
    `PhpUnhandledExceptionInspection.Companion`'s `getExceptionClasses` and
    `PhpDocMissingThrowsInspection.getAllUndocumentedExceptions`): the function's clause, an
    enclosing function's for a closure, the overridden method's for a method with neither a
    docblock nor a clause — PhpStorm's own reading of tags.
  - Layout: the `.phn` formatter gets the project's PHP style with class, method, function and
    closure braces at the end of the line; a wrapped arrow stays one level in. Reformat, Enter,
    Complete Current Statement, Implement / Override Methods and override completion follow it;
    docblock stubs and generated docblocks are one line when they hold one; overrides write the
    parent's clause and lift a PHP parent's bare `@throws`.
  - Real trees (`PHAENON_TREES`, 2025.1 and 2026.2.3): scratch copies of core and tq-cli re-lifted
    with `phn migrate --keep-public` (core 849 arrows, 3,159 braces, 64 `@throws`; tq-cli 1,295 /
    5,048 / 24; `phn check` as before) plus the five dogfood trees as they are: no syntax error, no
    stub mismatch, no override finding; Reformat Code moves no brace and no arrow `phn migrate`
    laid out (core's 73 moved braces are the `tests/PHPStan/Data` fixtures kept with
    `--keep-layout`). The plugin no longer holds up the re-lift of the dogfood trees (D18 item
    above).
  - Left: a call of a `.phn` function is not "unhandled" for its clause (PhpStorm reads callees'
    tags only); PhpStorm's quick-fixes add `@throws` tags, not clause members; Introduce Variable
    on part of an expression body writes its statement between the arrow's two characters (no
    conversion between arrow and block body yet); the three 2026.2 failures of `main` stay.
- ~~**`class<T>` hovered as `string|string`; PHP 8.6 and the value forms of `concurrently` and
  `spawn` were errors in the editor**~~ — done 2026-11-02 (`../jetbrains` branch
  `feat/class-string-and-php86`, not merged: `16b7897` class-strings, `b573956` PHP 8.6,
  `0db2717` blocks, docs `9625f1b`):
  - `class<T>` was `string` plus `\class-string<Bound>`. PhpStorm prints every class-string as
    `string`, and 2026.2 prints a docblock class-string beside a native `string` as
    `string|string` in plain PHP too (2025.1: `string`). Now `\class-string<Bound>` alone, the type
    PhpStorm gives a docblock-only `@return class-string<Foo>`; static calls, `new`, constants,
    `::class`, string parameters and `->` Str methods keep working. PhpStorm's type printer has no
    extension point, so quick documentation, the Ctrl+hover hint and parameter info print
    Phaenon's spelling through a documentation provider and a parameter-info handler ahead of
    PhpStorm's (`aggregateClass(): class<T>`, `$class: class<EventSourcedAggregate>`); inline hints,
    completion's type column and inspection messages stay PhpStorm's.
  - The report "Ctrl+hover on `aggregateClass` shows the interface" did not reproduce in the test
    framework on 2025.1 or 2026.2.3 — old or new type, the whole core tree, the compiled copy
    indexed: every position shows and goes to the method, and nothing in the plugin maps a getter
    to its class-string's class. Left open; candidates are the goto handlers of the third-party
    plugins in that IDE (Symfony, Laravel Idea, PHP Annotations) or a stale index.
  - PHP 8.6: no PhpStorm parses it (levels stop at 8.4 and 8.5). A placeholder `?` goes to the
    parser as an identifier; partial applications are typed as their closures and pipe stages as
    their calls; PhpStorm's findings on 8.6 code are dropped by target (`phaenon.json` `"php"`,
    else composer.json's floor): readonly defaults, enum `__debugInfo()`, writes through a constant
    on 8.6 only, `#[\Override]` on constants on both.
  - `concurrently` as a value, `spawn { … }`, `spawn expr`: closures to PhpStorm (the keyword's
    characters split into `function ( )` / `fn ( ) =>`), undefined and unused findings across a
    block's boundary filtered, block values and `Amp\Future<T>` typed on the variables they are
    assigned to; a statement block is formatted as one statement. tq-cli is back in the real-tree
    test: 859 files, no syntax error or stub mismatch. PSI_VERSION 5.
  - Left: to PhpStorm a partial application is also its callee's result, and a block or a spawn
    also a `\Closure`, whose value is not typed as an argument or an array-literal element; a
    `return` in a statement block reads as the function's; the options of a value block are erased;
    `#[\Override]` on a constant that overrides nothing (`P2503`) and the lowering's 8.5 refusals are
    not reported; on an 8.5 target the remaining findings are in PhpStorm's words; quick
    documentation's docblock sections show an erased parameter type as missing. The three tests
    that fail on 2026.2 on `main` still do (constant-expression closure finding, `map()` chain
    typing, the spell-checker inspection).
- ~~**Reformat Code does nothing to a `.phn`**~~ — fixed 2026-11-02 (`../jetbrains` branch
  `fix/editing-actions`: formatter `ce18349`, docblock stub `b61b652`, overrides `e8f1469`, test IDE
  `9d7e667`, pipe `532075f`, docs `88a2d3c`), with the editing actions that relied on it:
  - Cause: PhpStorm's `PhpFormattingModelBuilder` is a template formatter that builds PHP blocks
    only inside the HTML model of a template view provider; for the single-root `.phn` provider
    its base class formatted the whole file as one read-only leaf. Reformat Code, the indent of a
    new line, the ` * ` of a docblock and the reformatting after generated code (Implement Methods,
    quick-fixes, imports added by completion) all went through that leaf.
    `PhaenonFormattingModelBuilder` (a `CustomFormattingModelBuilder`, `.phn` only) hands the file
    to the builder's public `createTemplateFormattingModel`; PHP formats exactly as in `.php`, and
    wrapped blocks keep erased constructs against what they belong to and never change their text.
    The import optimiser's own layout of the `use` block went: the PHP code style lays it out. The
    real-tree test reformats all four dogfood trees (1,846 files) without changing a construct or
    breaking a file.
  - `/**` + Enter: PhpStorm's stub minus the `@param` / `@return` / `@var` a declared or erased
    type makes redundant; an empty stub is one line with the caret.
  - Implement / Override Methods and override completion write `fun` / `protected fun` with the
    parent's declared types, the parent's type parameters replaced by the class's type arguments
    along `extends` / `implements` (`fun get(UserId $id): ?User`), the classes they name imported.
    The second `fun` in completion was PhpStorm's bundled PHP live template `fun`; it is left out in
    `.phn` (Tab still expands it to `function`).
  - The pipe quick-fix bug is PhpStorm's (2026.2, `.php` too): a one-argument call becomes its first
    PSI child plus `(...)`, the qualifier of a method call. In `.phn` the plugin hides it and offers
    its own conversion (`Style::line(...)`, `$this->output->writeln(...)`).
  - `./gradlew test -PtestIdePath=/Applications/PhpStorm.app/Contents` runs the suite against the
    installed PhpStorm; the pipe tests need one with PHP 8.5 (2026.2). Three tests fail there on `main` too
    (the constant-expression closure finding, `map()` chain typing, the spell-checker inspection
    not registered).
  - Left: the text inside an erased construct is not formatted (`:?T` stays); a construct counts
    towards the line length, so a long `implements I<A, B>` line wraps; PhpStorm's PSR-12 style
    differs from the packages' php-cs-fixer config (concat and cast spacing, `fn (`, 120-column
    wraps) — a project code style matching it would make Reformat agree with cs; the pipe fix
    applied from Inspect Code results is PhpStorm's; report the pipe bug upstream.
- **Batch Inspect Code** still reports imports used only by erased constructs as unused, and
  PhpStorm's hierarchy findings on overrides with an erased type (the filter covers editor
  highlighting only); an `InspectionSuppressor` would cover batch runs.
- **A group-use member** used only by an erased construct comes back from Optimize Imports as
  its own statement next to the shrunk group.
- No Phaenon-only syntax yet: generics, `is` / `as`, `type` aliases show as PHP syntax errors.
- ~~**M4 ships with editor support for the method syntax**~~ — superseded by D20 (2026-11-03,
  `../jetbrains` `77b1d1b`: the pseudo-classes, the receiver types, the redirect to `Seq` and
  `phn stdlib --plugin-stubs` are gone; what follows is history). Done 2026-10-31 (`../jetbrains`
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
  members of a namespace after a qualifier) and no spell-checking of the erased leaf; a type parameter navigates to its `<T of …>` declaration, the declaration shows its usages (erased constructs and docblock types), and ~~the hierarchy check skips methods with erased types~~ (found on core's `AbstractAggregate::id(): T`; replaced 2026-11-02 by the plugin's own check, next item); `fun` is the `function` keyword to the parser and the highlighter, with the PSR-12 visibility finding and the constant-expression findings filtered (`603db11`). A class constant without `public` is public too since `208cb21` / `4689034`. Left: the leaf is a
  `PsiComment`, so any other comment-based feature sees Phaenon types as comments. PhpStorm 2025.1 does
  not parse `[...$a][0]` in plain PHP either (core's `DeadlineMetricsTest.phn:114`); the
  real-tree test lists such files instead of counting them.
- ~~**Erased types in PhpStorm's own checks**~~ — done 2026-11-02 (`../jetbrains` branch
  `fix/erased-types`: `6be6db9`, `97c3c9c`, `5c14fd5`, docs `861d2dd`), found on core's
  `AbstractEventSourcedRepository` and tq-cli's `DevUp`:
  - A type parameter is typed as its bound (`mixed` without one), `?T`, unions and `list<T>`
    included. PhpStorm's hierarchy check reads `Parameter.getDeclaredType()`, which no extension
    point reaches, and saw `save(T $aggregate)` as `save(mixed)`. `PhaenonHierarchyInspection` now
    judges every override with an erased type on either side (a `.phn`, or a `.php` implementing a
    Phaenon interface) as the checker does (P2502): the parent's type parameters substituted along
    `extends` / `implements`, type parameters by their bounds, a native child type equal to the
    parent's erasure inheriting (`generics.md` §11.1). The filter drops PhpStorm's finding on those
    overrides only, so real incompatibilities are reported. The real-tree test asserts no finding
    on the dogfood trees: 2,219 overrides, core's `save(T $aggregate)` and odm's lifted normalizers
    included.
  - `class<T>` is `string` plus `class-string<Bound>`: `$c::m()`, `new $c`, `$c::CONST` resolve
    against the class; `->` keeps the `Str` methods (the compiler lowers `$c->length()`), `::`
    never gets them.
  - `Closure(A, B=, C...): R` carries its signature in PhpStorm's docblock encoding (the variadic
    element-typed, an optional parameter plain). PhpStorm's own encoding of a variadic literal or
    first-class callable is `string[]` and may win its call check by hash order; the filter drops
    such a finding where the argument fits the declared parameter.
  - `PSI_VERSION` 3. Left: a tuple destructures to the union of its elements (`[$code] = …` is
    `int|string`; PhpStorm keeps positions only for docblock shapes it reads from the PSI); a
    method's own type parameters are compared by their bounds; Batch Inspect Code (above); quick
    documentation of a closure call shows the stub's `Closure::__invoke(...$_)`.
- The implied `declare(strict_types=1)` is not modelled.
- Editor colouring relies on the internal extension point `com.jetbrains.php.baseLexerProvider`.

## PhpStorm plugin — line map and debugging (D19, 2026-11-02)

jetbrains `702058a`; the reasoning is the plugin's README § Debugging and the plugin guide:
- **Internal API**: debugging goes through `com.intellij.php.debug.templateLanguage`
  (`PhpTemplateLanguagePathMapper`, `@ApiStatus.Internal`, as Twig and Blade use it); the compiled
  file's editor wrapper forwards `TextEditor.isEditorLoaded()`, internal too. The Plugin Verifier
  reports both; Marketplace publication may object.
- **The breakpoint gate** depends on `PhpLineBreakpointHandler.registerBreakpoint` calling
  `isTemplateFileType` right after `getInstance` asked `isGeneratedFile`; if PhpStorm changes that,
  `.phn` breakpoints go unmapped (fails closed, plain PHP unaffected).
- The Frames list labels compiled frames with the compiled name and line (`PhpStackFrame`'s
  presentation is final); selecting one opens the `.phn` line.
- A breakpoint set before its file is compiled stays invalid until toggled or a new session; a map is
  as stale as the last build (the compiled file's banner warns).
- Source-line stepping keys on the debugger's action ids; a step issued otherwise steps per
  compiled line.
- Not verified: pass/fail icons on `.phn` test methods (PhpStorm likely keys them on the compiled
  location). Coverage reports compiled lines.
- Xdebug 3.5's own path maps (`xdebug.path_mapping`) could serve other IDEs from the compiler side;
  their format rejects a run and a hold on one source line, so it would need its own encoding.

## Before the repositories go public

- `AGENTS.md`, `.mcp.json` and this vault name paths on the development machine (`~/labs/…`,
  `~/.local/bin/serena`). Generalise or move them before publishing.
- `tequila/reflection`'s `phaenon-dogfood` branch exists only locally.

## M6 — CI formats (slice 2, 2026-11-03)

`../design/tooling.md` §7.1 (the settlements under "Done in slice 2"). Left:
- **The steps in Tequila's reusable workflows** (§7.2: `phn check --format=github`, PHPStan and
  PHPUnit through `phn trace`) are not written; they are Tequila-side and come with the template
  (slice 8).
- **Annotation paths are relative to the working directory**, as PHPStan's are: a project in a
  subdirectory of its repository gets paths GitHub cannot match. `GITHUB_WORKSPACE` could anchor
  them when a project needs it.
- **Clover's `loc` and `ncloc` stay the compiled file's**, and a class's metrics follow a merge only
  in a file that declares one class (Clover does not say which class a line belongs to).
- **php-code-coverage counts comment lines inside a multi-line method chain as statements**
  (tq-cli `InitCommand.phn:39–41`, three comments between `->addOption()` calls). The compiled
  report says so too; the trace carries it over faithfully.
- A report is read from stdin only (`phn trace < clover.xml > clover.phn.xml`); there is no
  in-place mode.
- PhpStorm's coverage view: see § D19.

← [[Home]]
