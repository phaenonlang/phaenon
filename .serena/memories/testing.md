---
type: reference
tags: [phaenon, testing]
updated: 2026-10-29
---

# Test environment

`AGENTS.md` § Build and test has the everyday commands. This note is the full reference: every
environment variable the tests read (verified against the test sources on 2026-10-28), the slow
suites, and the tools they shell out to. Without its variable a suite **skips**, so a green
`go test ./...` says nothing about conformance or PHPStan parity.

## Variables

| Variable | Read by | Effect | Value behind the recorded results |
|---|---|---|---|
| `PHAENON_PHP_SRC` | `internal/conformance` (all suites) | Absolute path of the php-src checkout whose `.phpt` files are the corpus | `/Users/petr/labs/phaenon/php-src` |
| `PHAENON_EXEC` | `TestPrinterExecution`, `TestEmitExecution` | Any non-empty value runs the execution comparisons (≈2.5 min each; pass `-timeout 30m`) | `1` |
| `PHAENON_PHPT_FILTER` | `internal/conformance` | Only `.phpt` paths containing this substring | e.g. `Zend/tests`, `ext/standard/tests` |
| `PHAENON_SHOW_DIFFS` | printer round trip, execution suites | How many differences to print (default 25) | e.g. `100` |
| `PHAENON_SHOW_FALSE_ACCEPT` | `TestParserAgainstPHP` | Lists (up to 30) files PHP rejects but Phaenon parses | any non-empty |
| `PHAENON_PHPSTAN` | `TestPHPStanParity` (checker), `TestGenerics` (emit) | **Path to a PHPStan binary**; runs it at level max — the parity run with `treatPhpDocTypesAsCertain: false`, Tequila's profile (`../design/checker.md` §9) | `/Users/petr/labs/tequila/object-mapper/tools/phpstan/vendor/bin/phpstan` |
| `PHN_PHP` | `phn` itself (not a test variable) | The `php` whose loaded extensions are reflected into stubs; default `php` on `PATH` | unset |
| `PHAENON_UPDATE` | `TestGenerics` (emit) | Rewrites the expected `.php` goldens from the current output | `1` |
| `PHAENON_GOLDEN` | `TestGolden` (checker) | Only golden files whose name contains this substring | e.g. `generics` |
| `PHAENON_COMPOSER_PROJECT` | `internal/program` | A Composer project to exercise the vendor reader | e.g. `/Users/petr/labs/tequila/object-mapper` |

`../design/checker.md` §8 item 5 still says `PHAENON_PHPSTAN=1`; the code executes the value as a
binary, so it must be a path (listed in [[open-items]]).

## Conformance

- **Corpus:** `../php-src` is `php/php-src` `master` (at `9d94059ce88`, 2026-10-28), i.e. newer than
  the oracle. Files the oracle rejects are not counted as "PHP-accepted"; the parser suite reports
  them separately.
- **Oracle:** the `php` on `PATH`, run with `-n` (no php.ini) — Homebrew PHP 8.5.3 here. The M0
  numbers in [[milestones]] were measured against it; a different `php` changes them.
- `../php-src` is a read-only reference clone: never edit, build or `git` anything in it.

## PHPStan

Three PHPStan installs exist on this machine; use the object-mapper one for parity, because the
recorded results were established with it.

| Install | Version |
|---|---|
| `~/labs/tequila/object-mapper/tools/phpstan/vendor/bin/phpstan` | 2.2.5 — **the parity binary** |
| `~/labs/phaenon/runtime/vendor/bin/phpstan` | 2.3.3 (the runtime package's own dev dependency) |
| `~/.composer/vendor/bin/phpstan` | 2.1.54 (global) |

The PHPStan behaviour the emitter is built around — one docblock tag per line — was verified
against 2.2 ([[gotcha-phpstan-one-tag-per-line]]).

## The stdlib written in Phaenon

`stdlib/runtime/*.phn` is read as the compiler's declaration of `Seq`, `Map`, `Str`, `Bytes`
and compiled for the emit goldens' PHPStan run. `TestRuntimeStubs` (`internal/checker/stdlib_test.go`)
type-checks those bodies: `phn check stdlib/runtime/X.phn` cannot (the class is already declared
by the embedded copy, `P2511`), so the test marks the embedded files as checked instead.

## Extension stubs

`phn check` and `phn build` ask the local `php` for its loaded extensions and generate a stub for
each one php-src does not cover (ext-mongodb, redis, amqp, apcu, igbinary, msgpack here), from
`ReflectionExtension`. The stubs are cached under the user cache directory —
`~/Library/Caches/phaenon/ext/<name>-<version>-php<version>.php` on macOS — and regenerated when
the extension or PHP version changes; delete the directory to force it. `TestExtensionStubsParse`
(`internal/program`) generates a stub for every loaded extension and parses it, so a php with an
exotic extension is worth running the suite on. Without `php` the checker warns `P4007` once.

## Toolchain

`go.mod` says `go 1.27`; the local toolchain is go1.27.2. The module has no third-party
requirements. `phn` is not installed on `PATH`: build it from the repo (`AGENTS.md`), and put it on
`PATH` before running a dogfood target's `composer build` ([[dogfood]]).

← [[Home]]

## Generated files kept fresh by tests (2026-10-31)

- `TestRuntimePackageFresh` (`internal/emit`) compares the compiled stdlib with `../runtime/src`;
  `TestConcurrentPackageFresh` compares the compiled `Scope.php` with `../concurrent/src`
  (2026-11-01); `TestPluginStubsFresh` compares the generated pseudo-classes with
  `../jetbrains/src/main/resources/stubs/phaenon/Phaenon/Internal`. All skip without the checkout;
  `phn stdlib --out ../runtime/src --concurrent-out ../concurrent/src --plugin-stubs ../jetbrains/src/main/resources/stubs/phaenon/Phaenon/Internal`
  rewrites all three (`--check` to verify).
- `TestRuntimeStubs` type-checks `stdlib/concurrent/Scope.phn` as well. The behaviour of the
  scope — joins, cancellation, deadlines — is tested only in `../concurrent` (`composer test`,
  14 tests against the real amp 3.1 / Revolt 1.0; `composer stan` at level max): the compiler's
  goldens see the lowering, not the loop.
