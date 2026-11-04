---
type: reference
tags: [phaenon, testing]
updated: 2026-11-04
---

# Test environment

`AGENTS.md` § Build and test has the everyday commands. This note is the full reference: every
environment variable the tests read (verified against the test sources on 2026-10-28), the slow
suites, and the tools they shell out to. Without its variable a suite **skips**, so a green
`go test ./...` says nothing about conformance or PHPStan parity.

## Variables

| Variable | Read by | Effect | Value behind the recorded results |
|---|---|---|---|
| `PHAENON_PHP_SRC` | `internal/conformance` (all suites) | Absolute path of the php-src tree whose `.phpt` files are the corpus: the checkout, or a release branch extracted into the cache (§ Conformance) | `/Users/petr/labs/phaenon/php-src`; `~/labs/phaenon/.cache/php-src-8.6` |
| `PHAENON_PHP` | `internal/conformance` (all suites) | The php binary taken as the oracle; default `php` on `PATH`. The emit execution suite compiles the corpus for this php's release line | unset (8.5.3); `/opt/homebrew/opt/php@8.6/bin/php` for the 8.6 corpus |
| `PHAENON_PHP86` | `TestGenerics` (emit) | A PHP 8.6 binary: `php -l` of the goldens compiled for 8.6, and the behaviour check of a golden compiled for both targets (§ Targets in goldens) | `/opt/homebrew/opt/php@8.6/bin/php` |
| `PHAENON_EXEC` | `TestPrinterExecution`, `TestEmitExecution` | Any non-empty value runs the execution comparisons (≈2.5 min each; pass `-timeout 30m`) | `1` |
| `PHAENON_PHPT_FILTER` | `internal/conformance` | Only `.phpt` paths containing this substring | e.g. `Zend/tests`, `ext/standard/tests` |
| `PHAENON_SHOW_DIFFS` | printer round trip, execution suites | How many differences to print (default 25) | e.g. `100` |
| `PHAENON_SHOW_FALSE_ACCEPT` | `TestParserAgainstPHP` | Lists (up to 30) files PHP rejects but Phaenon parses | any non-empty |
| `PHAENON_PHPSTAN` | `TestPHPStanParity` (checker), `TestGenerics` (emit) | **Path to a PHPStan binary**; runs it at level max — the parity run with `treatPhpDocTypesAsCertain: false`, Tequila's profile (`../design/checker.md` §9) | `/Users/petr/labs/tequila/object-mapper/tools/phpstan/vendor/bin/phpstan` |
| `PHN_PHP` | `phn` itself (not a test variable) | The `php` whose loaded extensions are reflected into stubs; default `php` on `PATH` | unset |
| `PHAENON_UPDATE` | `TestGenerics` (emit) | Rewrites the expected `.php` goldens from the current output | `1` |
| `PHAENON_UPDATE` | `TestGolden` (format) | Rewrites `internal/format/testdata/X.golden.phn` | `1` |
| `PHAENON_FMT_TREE` | `TestTree` (format) | A tree of `.phn` (a dogfood checkout) that `phn fmt`'s first layer formats in memory: each file must format, and to itself a second time; the files it would change are logged (`-v`) | `/Users/petr/labs/tequila/core` |
| `PHAENON_PHN` | `PhaenonLayoutTest` (`../jetbrains`) | A `phn` the plugin's Reformat Code runs (`phn fmt --stdin`); with it the layout tests reformat `internal/format/testdata` too. Skipped without | `../phaenon/bin/phn` (`go build -o bin/phn ./cmd/phn`) |
| `PHAENON_GOLDEN` | `TestGolden` (checker) | Only golden files whose name contains this substring | e.g. `generics` |
| `PHAENON_COMPOSER_PROJECT` | `internal/program` | A Composer project to exercise the vendor reader | e.g. `/Users/petr/labs/tequila/object-mapper` |
| `PHAENON_COMPOSER` | `internal/distribution` | A composer binary: the `phaenon/phn` plugin runs under it in temporary projects (a fake `phn`, an `httptest` mirror; Packagist off); needs the `runtime` checkout beside the repository or the main checkout | `$(which composer)` |
| `PHAENON_PHP86` | `TestPHPStanParity` (checker) | A php 8.6: PHPStan runs on it for the goldens whose header says `// target: 8.6` (they skip without it, unless `PHAENON_PHP` is an 8.6) | `/opt/homebrew/opt/php@8.6/bin/php` |
| `PHAENON_PHP_SRC` | `TestFresh` (`stdlib/gen`) too | Compares the committed stubs and version table with the clone's release branches | as above |

`../design/checker.md` §8 item 5 still says `PHAENON_PHPSTAN=1`; the code executes the value as a
binary, so it must be a path (listed in [[open-items]]).

## Conformance

- **Corpus:** `../php-src` is `php/php-src` `master` (at `9d94059ce88`, 2026-10-28), i.e. newer than
  the oracle. Files the oracle rejects are not counted as "PHP-accepted"; the parser suite reports
  them separately.
- **Oracle:** the `php` on `PATH`, run with `-n` (no php.ini) — Homebrew PHP 8.5.3 here — or the one
  `PHAENON_PHP` names. The M0 numbers in [[milestones]] were measured against 8.5.3 and the master
  corpus; a different `php` changes them.
- `../php-src` is a read-only reference clone: never edit, build or `git` anything in it, beyond
  reading (`git show`, `git diff`, `git archive`).

### A release branch's corpus (2026-11-02)

Each PHP release is tested with its own branch's `.phpt` files under that release's `php`
(`../design/php-versions.md` §7). The branches are extracted with `git archive`, never checked out,
into `~/labs/phaenon/.cache/php-src-<version>` (outside the clone, not in any repository):

```sh
git -C ~/labs/phaenon/php-src archive -o ~/labs/phaenon/.cache/php-src-8.6.tar origin/PHP-8.6 -- ':(glob)**/*.phpt' ':(glob)**/*.inc'
mkdir -p ~/labs/phaenon/.cache/php-src-8.6
tar -x -f ~/labs/phaenon/.cache/php-src-8.6.tar -C ~/labs/phaenon/.cache/php-src-8.6
rm ~/labs/phaenon/.cache/php-src-8.6.tar
# the same with PHP-8.5 → php-src-8.5
PHAENON_PHP_SRC=$HOME/labs/phaenon/.cache/php-src-8.6 PHAENON_PHP=/opt/homebrew/opt/php@8.6/bin/php \
  go test ./internal/conformance/
```

The branches as extracted: `PHP-8.6` at `0761161562c`, `PHP-8.5` at `a1a9f70cb2d` (both
2026-10-28; the clone is not fetched). PHP 8.6 is `/opt/homebrew/opt/php@8.6/bin/php`
(8.6.0-dev).

| Corpus, oracle (2026-11-02) | Lexer | Parser (PHP-accepted) | Printer round trip | Printer execution | Emit execution |
|---|---|---|---|---|---|
| `PHP-8.6`, PHP 8.6.0-dev | 23,445/23,445 | 23,285/23,285 (84 of 160 rejected files accepted) | 23,369/23,369 | 4,584/4,584 | 4,891/4,891 |
| `PHP-8.5`, PHP 8.5.3 | 22,233/22,233 | 22,075/22,075 (84 of 158) | 22,159/22,159 | 4,361/4,361 | 4,644/4,644 |
| master checkout, PHP 8.5.3 | 23,536/23,536 | 23,226/23,226 (234 of 310: the 8.6 placeholder tests) | 23,460/23,460 | — | — |

Since D19 (2026-11-02) the emit execution suite translates the compiled run's positions through
its line map before comparing (`translateLines` in `internal/conformance/emit_test.go`). Re-run on
both branches that day: emit execution **4,615/4,615** (`PHP-8.5`, PHP 8.5.3) and **4,862/4,862**
(`PHP-8.6`, PHP 8.6.0-dev), plus **29** tests in each that read a line as a value (an error
handler's `$errline`, `getLine()`, reflection's start line, `debug_backtrace()` as data, an
assertion callback): the compiled file reports its own line there, so they must match with digits
blinded and are counted apart. Lexer, parser, printer round trip and printer execution unchanged
from the table.

The parse oracle is `TOKEN_PARSE`, the grammar alone: PHP's compile errors (a misplaced
placeholder, say) are accepted by it, so the parser accepts them too and the checker reports them.
The emit execution suite skips a test with placeholders when the oracle is older than 8.6 ("newer
than the oracle").

## Targets in goldens (2026-11-02)

- Checker goldens are checked for PHP 8.5 unless the first line says `// target: 8.6`
  (`partial.phn`).
- Emitter goldens are compiled for the targets the first line names, `// targets: 8.5, 8.6`: the
  first one's output is `X.php`, another's `X.8.6.php` (`partial.phn`). With `PHAENON_PHP86` set,
  each output passes `php -l` under its target's php, and both outputs print the same under PHP 8.6
  and the 8.5 one the same under PHP 8.5. PHPStan runs on the 8.5 output only when the source has
  placeholders: the parity PHPStan 2.2 does not parse PHP 8.6.
- The checker's PHPStan parity skips goldens with placeholders for the same reason.
- A checker golden whose only Phaenon syntax is that of `../design/functions.md` (`fun`, `=>`
  bodies, `throws`) is compiled by the emitter and PHPStan judges the output (2026-11-02), each
  finding's line read through the output's line map (since D19); goldens with other Phaenon syntax
  are still skipped, covered by the emit goldens. `functions.phn`, `expression-bodies.phn` and
  `throws.phn` run that way.

## The line map (2026-11-02)

- `internal/linemap` tests read `testdata/vectors.json`, the reference of the three readers;
  `TestVectorCopiesFresh` compares it with the PhpStorm plugin's and `phaenon/runtime`'s copies
  (`src/test/resources/linemap/vectors.json`, `tests/Debug/vectors.json`) and skips a missing one,
  as it does in a git worktree, where the siblings are not three directories up.
- `TestLineMapProperty` (`internal/emit`) is the emitter's line invariant over the emit goldens,
  the stdlib's `.phn` and the checker corpus: every source byte that reaches the output unchanged
  sits on a line whose forward lookup gives its line. `TestLineMapTree` runs it over a project:
  `PHAENON_LINEMAP_TREE=~/labs/tequila/core/src` (`PHAENON_LINEMAP_OUT=DIR` writes the outputs).
  On 2026-11-02 it held on all five dogfood trees (core 411 + 401, tq-cli 585 + 274, odm 76 + 49,
  object-mapper 23, reflector 4 files), and the 812 core outputs passed `php -l`.
- php-cs-fixer on compiled output, checked by hand: core's `tools/php-cs-fixer` with a copy of a
  project's `.php-cs-fixer.dist.php` outside its checkout, the compiled-tree exceptions removed,
  `->in()` pointed at `PHAENON_LINEMAP_OUT`. 2026-11-02: core's 812 and tq-cli's 585 compiled
  sources, 0 findings. On the emit goldens what fires is the fixtures' own text, plus the two
  limits in [[open-items]] § D19 — the compiled layout.

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

## The php-src stubs and the version table (2026-11-02)

`stdlib/php-src` was a copy of php-src master (2026-10-28) until the PHP 8.6 milestone; it now
holds the stubs of the newest supported release branch, and `stdlib/versions_gen.go` the release
each library symbol appeared in. Both are generated, never edited:

1. The clone is read-only, so the release branches are whatever it holds; `stdlib/gen` reads them
   with `git archive` only and records each branch's commit in `versions_gen.go`.
2. `PHAENON_PHP_SRC=$HOME/labs/phaenon/php-src go generate ./stdlib` writes every `*.stub.php` and
   `LICENSE` of `origin/PHP-8.6` (removing stubs the branch no longer has) and diffs the stubs of
   `origin/PHP-8.5` and `origin/PHP-8.6` into the table. It prints the counts (43 classes, 57
   functions, 27 methods, 66 constants, 4 class constants, 7 parameters, 44 deprecations on
   2026-11-02) and any symbol a release removed.
3. `go run ./stdlib/gen -php /opt/homebrew/opt/php@8.6/bin/php` also rewrites `stdlib/constants.php`
   (every constant of the embedded extensions, `true`/`false`/`null` and resources aside); run with
   the 8.5.3 php it reproduced the committed file, which is how the procedure was validated. The
   values come from 8.6.0-dev today: regenerate once 8.6.0 is installed.
4. `go test ./internal/program -run TestOverlayMatchesStubs` fails when an overlay function or
   method is missing from the new stubs or declares other parameters; fix the overlay.
5. A new release line: add it to `releases` in `stdlib/gen/main.go` and to `internal/phpversion`
   (`Floor`/`Newest` must match the list).

`TestFresh` (`stdlib/gen`, with `PHAENON_PHP_SRC`) fails when the committed files are stale.

The PHPStan parity run uses PHPStan 2.2, which predates 8.6: it refuses readonly defaults,
`#[\Override]` on constants and `__debugInfo()` on enums, and does not know the members 8.6 added to
existing classes. Those findings are ignored per file in `internal/checker/phpstan_test.go`
(`allowIn`), each with its reason; `skipParity` lists the files PHPStan cannot judge at all.

## The stdlib written in Phaenon

`stdlib/runtime/**/*.phn` is read as the compiler's declaration of `Bytes` and the `Debug` classes
(`Seq`, `Map`, `Str` went with D20 on 2026-11-03) and compiled for the emit goldens' PHPStan run. `TestRuntimeStubs` (`internal/checker/stdlib_test.go`)
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

## Xdebug for the plugin's debugger test (2026-11-02)

`../jetbrains`' `PhaenonXdebugTest` drives a real Xdebug over DBGp when `PHAENON_XDEBUG` names the
extension (`PHAENON_PHP` the php, default `php`). Xdebug 3.5.3 is built from the PECL source in
`~/labs/phaenon/.cache/xdebug/src/xdebug-3.5.3` (`phpize && ./configure
--with-php-config=/opt/homebrew/bin/php-config && make`) into
`~/labs/phaenon/.cache/xdebug/xdebug-3.5.3-php8.5.so`, and loaded only by `php -n -d
zend_extension=…`: no php.ini carries it. It does not build for 8.6-dev. Measured there: Xdebug
resolves a breakpoint on a compiled signature line to the body's first statement, and a void
expression body stops twice (statement, then the compiled `}`), which is why holds exist
(`../design/line-map.md` §3.2). The plugin's `src/test/resources/linemap/vectors.json` is a verbatim
copy of `internal/linemap/testdata/vectors.json`.

## Coverage reports (2026-11-03)

The local `php` has no coverage driver. Real Clover, Cobertura and JUnit reports came from a
throwaway image, PHP 8.5 with PCOV:

```sh
printf 'FROM php:8.5-cli\nRUN pecl install pcov && docker-php-ext-enable pcov\n' > Dockerfile
docker build -t phaenon-scratch/php-pcov:8.5 .
docker run --rm -v ~/labs:$HOME/labs:ro -v "$OUT:$OUT" -w "$TREE" phaenon-scratch/php-pcov:8.5 \
  php -d pcov.enabled=1 -d pcov.directory="$TREE/.src" vendor/bin/phpunit --cache-directory "$OUT/cache" \
  --log-junit "$OUT/junit.xml" --coverage-clover "$OUT/clover.xml" --coverage-cobertura "$OUT/cobertura.xml"
docker image rm phaenon-scratch/php-pcov:8.5   # when done
```

- The workspace is mounted read-only at its own path, so the reports name the host's paths and
  `phn trace` reads the compiled files they name.
- tq-cli's suite fits the plain image (`ext-ctype`, `iconv`, `phar`, `zlib`). It fails 174 tests
  there for want of `git` and a writable tree, which leaves failure texts to trace. Core needs
  `ext-redis`, `ext-mongodb` and `ext-apcu`.
- The Xdebug built for the plugin's debugger test (below) collects coverage too, with `-d
  xdebug.mode=coverage`.
- `internal/trace/testdata/reports` holds such reports on its `project/`, the project path written
  `@DIR@`. To refresh them, rebuild the project with `phn build --emit-on-error` (its test has no
  vendor), rerun the reports, put `@DIR@` back, then run `PHAENON_UPDATE=1 go test ./internal/trace`
  and read the diffs of the `*.want.*` files.

## Toolchain

`go.mod` says `go 1.27`; the local toolchain is go1.27.2. The module has no third-party
requirements. `phn` is not installed on `PATH`: build it from the repo (`AGENTS.md`), and put it on
`PATH` before running a dogfood target's `composer build` ([[dogfood]]).

← [[Home]]

## Generated files kept fresh by tests (2026-10-31)

- `TestRuntimePackageFresh` (`internal/emit`) compares the compiled stdlib with `../runtime/src`;
  `TestConcurrentPackageFresh` compares the compiled `Scope.php` with `../concurrent/src`
  (2026-11-01). Both skip without the checkout; `phn stdlib --out ../runtime/src --concurrent-out
  ../concurrent/src` rewrites both (`--check` to verify). It deletes no file whose source is gone.
  (`TestPluginStubsFresh` and `--plugin-stubs` went with D20 on 2026-11-03.)
- `TestRuntimeStubs` type-checks `stdlib/concurrent/Scope.phn` as well. The behaviour of the
  scope — joins, cancellation, deadlines — is tested only in `../concurrent` (`composer test`,
  14 tests against the real amp 3.1 / Revolt 1.0; `composer stan` at level max): the compiler's
  goldens see the lowering, not the loop.

## The language server's tests (2026-11-04)

- `internal/lsp` drives a server over pipes against a fake host (stubs only, files under a
  temporary root): diagnostics on open and change, the project's on save and build on save,
  PhpStorm's diagnostics-only capabilities, hover, definition (into a stub's cached copy),
  symbols, completion (members, variables, classes with their import, functions, `|>`
  stages), signature help, formatting, the P1005 quick-fix, related information, UTF-16
  positions. `go test -race ./internal/lsp/` is clean.
- `cmd/phn` `TestLSP` runs the `phn lsp` binary over stdio in a temporary Composer project
  (diagnostics, nothing built on open, the outputs on save with the broken file withheld), and
  `TestWatch` the `phn watch` binary (a build at start, another after a change).
- The numbers in `../design/tooling.md` §6.5 come from a scratch LSP client that opened a core
  or tq-cli file, edited it, sent each request and saved, timing each; it is not kept.

## Execution suites on 2026-11-04

After D21 (tabs replaced by migrate), with the cached release-branch corpora:
- `PHP-8.6` under PHP 8.6.0-dev: emit execution 4,853/4,853, printer execution 4,584/4,584.
- `PHP-8.5` under PHP 8.5.3: emit execution 4,605/4,606, printer execution 4,361/4,361. The one
  difference is `Zend/tests/gh21603.phpt`, whose original run crashes PHP 8.5.3 itself
  (`zend_mm_heap corrupted`, a use-after-free in `__unset` fixed after 8.5.3); the test has no tab.
- Both count apart 9 tests of PHP's heredoc indentation errors written with tabs ("Invalid
  indentation - tabs and spaces cannot be mixed", "Invalid body indentation level"): the
  migration's spaces make the indentation consistent or count it in other columns, so PHP reports
  another error or none.
- The `master` checkout pairs badly with PHP 8.5.3 for execution: its 8.6 tests other than the
  placeholder ones (`#[\Override]` on constants, 19) fail in the original run and are not filtered
  as newer than the oracle. Use `.cache/php-src-8.5` and `.cache/php-src-8.6`.
