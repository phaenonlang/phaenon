# Phaenon — agent guide

Phaenon is a typed dialect of PHP 8.5+ that compiles to idiomatic PHP ("TypeScript for PHP"):
the `.phn` sources of a Composer project compile into a sibling output directory (`src/` →
`.src/`, named in `phaenon.json`) holding the PHP you would have written by hand. The type checker is the product. This repository is the compiler, `phn`, written in Go.
Pre-alpha; GitHub org `phaenonlang`, repositories private until they are published under MIT.

**This repository is the entry point for agents.** Start sessions here; the map below says where
everything else lives. Project memory — milestone history, test environment, gotchas, open items —
is the vault in `.serena/memories/` (Serena memories, also an Obsidian vault); start at `Home.md`.

## Org map

The workspace is `~/labs/phaenon/`; the other repos are siblings, reached as `../<name>`.

| Repo | Path | Remote | Holds | Build / test |
|---|---|---|---|---|
| **phaenon** | `.` | `phaenonlang/phaenon` | The compiler `phn`: lexer, parser, checker, emitter, migrate; embedded PHP signatures | [Build and test](#build-and-test) |
| **design** | `../design` | `phaenonlang/design` | The language design and every decision: `language-overview.md` (scope), `generics.md` (decisions §11), `checker.md` (decisions §9, open §10), `roadmap.md` (decisions D1–D8, milestones M0–M7, working method §4) | Markdown only |
| **runtime** | `../runtime` | `phaenonlang/runtime` | Composer package `phaenon/runtime`: `\Phaenon\Type`, behind structural `is` / `as`; `Seq`, `Map`, `Str`, `Bytes` behind method syntax | `composer install`, `composer test`, `composer stan` |
| **concurrent** | `../concurrent` | `phaenonlang/concurrent` | Composer package `phaenon/concurrent`: `\Phaenon\Concurrent\Scope` behind `concurrently { }` and `spawn`, on amp v3 / Revolt; `src/Scope.php` is compiled from `stdlib/concurrent/Scope.phn` here | `composer install`, `composer test`, `composer stan` |
| **jetbrains** | `../jetbrains` | `phaenonlang/jetbrains` | PhpStorm plugin: `.phn` read as PHP code without the open tag (Kotlin, Gradle); no Phaenon-only syntax yet | `./gradlew test`, `./gradlew buildPlugin` |
| **php-src** | `../php-src` | clone of `php/php-src` | **Read-only reference.** The scanner and grammar the lexer and parser port, the `.phpt` corpus of the conformance suite, the source of `stdlib/php-src` | Never modify, build or change its git state |

**Tequila dogfood targets** — the PHP platform behind tequila.dev, the codebase Phaenon must run.
Those repos follow their own rules (`~/labs/tequila/platform/CLAUDE.md`: Conventional Commits,
squash merges).

| Package | Repo | Role |
|---|---|---|
| `tequila/reflection` | `~/labs/tequila/reflector`, branch `phaenon-dogfood` (local only) | M1: built from `.phn`; its phpunit, PHPStan 10 and php-cs-fixer pass |
| `tequila/object-mapper` | `~/labs/tequila/object-mapper`, branch `phaenon-dogfood` (local only) | M2: `phn check src` = 0 findings. M3: migrated with real generics (51 `.phn`), suite + PHPStan + cs pass. `tools/phpstan` is the parity PHPStan |
| `tequila/mongodb-odm` | `~/labs/tequila/mongodb-odm`, branch `phaenon-dogfood` (local only) | M3: migrated (125 `.phn`), suite + PHPStan max + cs pass, `phn check` 0 errors (`main` went to level max for it). Needs ext-mongodb in the local `php` |
| `tequila/core` | `~/labs/tequila/core`, branch `phaenon-dogfood` (local only) | M4: migrated (812 `.phn`) on a `main` at PHPStan level max; `phaenon.json` builds `src`/`tests` into `.src`/`.tests` (gitignored), where Composer, PHPUnit, PHPStan and cs look; suite (1188 tests), PHPStan max and cs pass on the output; `phn check` 11 findings, all PHPStan rules deliberately not matched (vault `dogfood`, `open-items`); `exclude` lists the analyser tooling and what `phpstan.neon` excludes. Run `composer update` after switching to the branch: `composer.lock` is ignored |
| `tq-cli` | `~/labs/tequila/tq-cli-phaenon-dogfood` (a worktree of `~/labs/tequila/tq-cli`), branch `phaenon-dogfood` (local only) | M5: 859 `.phn`, the fifteen `Api::concurrently` fan-outs as `concurrently` blocks; suite (2052 tests), PHPStan 10 and cs pass on the output, `phn check` 0 over `src` and `tests`. Requires `phaenon/concurrent` and `phaenon/runtime` as path repositories from the sibling checkouts |
| `tequila/nats` | `~/labs/tequila/nats` | M5: `RevoltTransport`, the pump lease and the opt-in reader task merged upstream (tequila/nats#13); no Phaenon sources |
| later | `php-service-template` (M6), `iam` (M7) | `../design/roadmap.md` §3 |

## Where to look things up

| Question | Look in |
|---|---|
| What the language is, and will be | `../design/language-overview.md`, then the feature's doc (`functions.md` for `fun` and closures in constant expressions, `concurrency.md` for `concurrently` and `spawn`) |
| Why something is the way it is | The decision sections: `roadmap.md` §1, `checker.md` §9, `generics.md` §11 — they win over the draft text above them |
| Milestone status and what is next | `../design/roadmap.md` §3; vault `milestones` |
| Deferred work | vault `open-items` |
| Every `PHAENON_*` test variable, which `php` and which PHPStan | vault `testing` |
| A diagnostic code | `internal/checker/codes.go` |
| What PHP does | `php` itself (the oracle), then `../php-src` |
| A PHP function's signature | `stdlib/php-src/**/*.stub.php`, refined by `stdlib/overlay/` |

## This repository

```text
cmd/phn/              CLI: build, check, migrate, lex, print
internal/token        token set: PHP 8.5's plus Phaenon's, named like token_name()
internal/lexer        hand-written port of zend_language_scanner.l; lossless
internal/ast          typed node families; Phaenon constructs are additions to the PHP tree
internal/parser       recursive descent, Pratt expressions (precedence from zend_language_parser.y)
internal/printer      AST → canonical PHP, token-faithful round trip
internal/docblock     PHPDoc tags and the PHPStan/Psalm type grammar
internal/types        the type lattice (PHPStan semantics): subtyping, narrowing, printing
internal/program      program model: project files, Composer vendor code (lazy), stubs + overlay
internal/checker      the type checker behind `phn check`
internal/emit         patch-based, line-preserving emitter; generics erasure, is/as lowering
internal/migrate      .php → .phn: tag stripping, generic docblocks lifted into syntax, `function` → `fun`
internal/conformance  suites over the php-src corpus with `php` as the oracle
stdlib/               embedded: php-src stubs, Phaenon's overlay, the phaenon/runtime and phaenon/concurrent sources (.phn), the amp/Revolt declarations
stdlib/gen            generator of stdlib/php-src (the newest release branch's stubs), versions_gen.go (the release each symbol came in) and constants.php
```

## Build and test

```sh
go build -o bin/phn ./cmd/phn      # bin/ is gitignored; or `go run ./cmd/phn <command> …`
go test ./...                      # unit and golden tests; suites below skip without their variable

PHAENON_PHP_SRC=$HOME/labs/phaenon/php-src go test ./internal/conformance/
PHAENON_EXEC=1 PHAENON_PHP_SRC=$HOME/labs/phaenon/php-src \
  go test ./internal/conformance/ -run Execution -timeout 30m          # ≈2.5 min per suite
PHAENON_PHPSTAN=$HOME/labs/tequila/object-mapper/tools/phpstan/vendor/bin/phpstan \
  go test ./internal/checker ./internal/emit                           # PHPStan level max on goldens
PHAENON_UPDATE=1 go test ./internal/emit                               # rewrite emitter goldens
PHAENON_PHP_SRC=$HOME/labs/phaenon/php-src go generate ./stdlib        # refresh stubs + version table (vault `testing`)
```

`phn` itself: `phn build|check|migrate|stdlib|lex|print` — run `phn` without arguments for usage; the
README explains the Composer workflow and `phaenon.json` (roots → sibling output directories). Dogfood targets call `phn` from `PATH`. `phn stdlib` writes the
runtime package (`--out ../runtime/src`), the concurrent package (`--concurrent-out
../concurrent/src`) and the PhpStorm plugin's pseudo-classes (`--plugin-stubs
../jetbrains/src/main/resources/stubs/phaenon/Phaenon/Internal`) from `stdlib/runtime/*.phn` and
`stdlib/concurrent/Scope.phn`; the tests `TestRuntimePackageFresh`, `TestConcurrentPackageFresh`
and `TestPluginStubsFresh` fail when a checkout is stale.

## How work is done

- **Design doc → golden tests → implementation → PHPStan on the output → Tequila dogfood**, for
  every feature. A feature starts as a section in a `../design` doc and as golden tests that fail;
  it is done when the dogfood target's own suite passes on compiled output. Checklist, including
  how a milestone is closed: vault `working-method`.
- **A milestone starts, and the roadmap's order changes, only on a maintainer's go-ahead.** Finish
  the current step, report, and wait for the choice.
- Weigh every choice against "can Tequila still run on it?": compatibility with Symfony, PHPStan,
  Composer and extensions is the hard constraint.
- For anything that is PHP, `php` is the oracle; for what Phaenon adds, golden files are.

## Conventions

### Go

- Standard library only: `go.mod` has no requirements. Adding one is the maintainers' decision.
- gofmt. A package's doc comment states what it is and its invariants (see `internal/lexer`,
  `internal/emit`); comments explain why, not what.
- Diagnostic codes are stable once published: `P1xxx` syntax and removed constructs, `P2xxx` types
  and flow, `P3xxx` generics, `P4xxx` interop. One mistake, one diagnostic.
- The emitter edits spans and keeps every edit's line count, so line N of the `.php` is line N of
  the `.phn`. The only exception, a generic declaration's docblock, is a recorded decision; a new
  exception needs one too.
- Emitted PHP must pass `php -l` and PHPStan at level max.

### Tests, fixtures, golden files

- **Checker corpus** `internal/checker/testdata/*.phn`: one file per feature; expected diagnostics as
  `// want P2103` on the offending line (several codes comma-separated, optional `"message
  substring"`); the set of (line, code) pairs must match exactly. Vendor fixtures live under
  `testdata/vendor/` (PSR-4 root `Acme\`). The file's opening comment lines may say `// target: 8.6`
  (the floor, 8.5, otherwise) and `// deprecations: on`.
- **PHPStan parity:** the checker may be stricter than PHPStan, never more lenient. A PHPStan rule
  Phaenon deliberately lacks goes into the allow-list in `internal/checker/phpstan_test.go` with its
  reason.
- **Emitter goldens** `internal/emit/testdata/X.phn` → `X.php`: regenerate with `PHAENON_UPDATE=1`,
  never edit by hand, and read the diff before committing it.
- **Migrate goldens** `internal/migrate/testdata/lift/X.php` → `X.phn` + `X.notes` (the tags left
  in place and why): same `PHAENON_UPDATE=1` rule; the `.phn` must parse and lift to itself.
- **Printer golden** `internal/printer/testdata/sample.golden`: regenerate with
  `phn print --php testdata/sample.php`.
- A `.phn` fixture is PHP without `<?php`. Docblocks: one tag per line (PHPStan ignores the rest).
- Golden files and `stdlib/php-src` are byte-sensitive: no formatters, no whitespace clean-up
  (`.editorconfig` exempts them).

### Git and commits

- Commit only when the developer running the session asks. Stage by path, never `git add -A` /
  `git add .` / `git commit -a`, and account for every line of `git status --short`: parallel
  sessions leave work in these trees.
- **Commit messages follow [Conventional Commits](https://www.conventionalcommits.org):** `type(scope):
  subject`, the scope a package or area (`feat(checker):`, `fix(emit):`, `docs(design):`), `!` for
  a breaking change to the language or the CLI, and a body that says what changed and why. **No
  attribution lines, no `Co-Authored-By`, no "Generated with"** — the repositories will be open
  source. The same rule holds in `design`, `runtime` and `jetbrains`.
- `go.mod`'s module line is `github.com/phaenonlang/phaenon`; GoLand has rewritten it before (vault
  `gotcha-goland-rewrites-go-mod`).
