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
| **runtime** | `../runtime` | `phaenonlang/runtime` | Composer package `phaenon/runtime`: `\Phaenon\Type`, behind structural `is` / `as`; `\Phaenon\Bytes`, the checked conversions between `bytes` and text; `Phaenon\Debug`: the line map reader (`LineMap`), a Monolog processor and a Sentry integration that point production errors at `.phn` positions (`../design/line-map.md` §4.3); `src/` except `Type.php` is compiled from `stdlib/runtime/**/*.phn` | `composer install`, `composer test`, `composer stan` |
| **concurrent** | `../concurrent` | `phaenonlang/concurrent` | Composer package `phaenon/concurrent`: `\Phaenon\Concurrent\Scope` behind `concurrently { }` and `spawn`, on amp v3 / Revolt; `src/Scope.php` is compiled from `stdlib/concurrent/Scope.phn` here | `composer install`, `composer test`, `composer stan` |
| **jetbrains** | `../jetbrains` | `phaenonlang/jetbrains` | PhpStorm plugin: `.phn` read as PHP code without the open tag (Kotlin, Gradle); `phn lsp`'s diagnostics and build on save; Reformat Code ends with `phn fmt --stdin` | `./gradlew test` (`PHAENON_PHN=bin/phn` of this repo for the layout tests), `./gradlew buildPlugin` |
| **vscode** | `../vscode` | `phaenonlang/vscode` | VS Code extension (TypeScript): the TextMate grammar, `source.php` plus injections for Phaenon's syntax (the PhpStorm plugin's TextMate bundle copies it); one `phn lsp` per project with §6.1's whole list; build, check and watch as tasks; `phn trace` of the selection or the terminal | `npm install`, `npm test` (`PHAENON_TREES` for whole trees), `PHAENON_PHN=../phaenon/bin/phn npm run test:e2e`, `npm run package` (`.vsix`) |
| **php-src** | `../php-src` | clone of `php/php-src` | **Read-only reference.** The scanner and grammar the lexer and parser port, the `.phpt` corpus of the conformance suite, the source of `stdlib/php-src` | Never modify, build or change its git state |

**Tequila dogfood targets** — the PHP platform behind tequila.dev, the codebase Phaenon must run.
Those repos follow their own rules (`~/labs/tequila/platform/CLAUDE.md`: Conventional Commits,
squash merges).

| Package | Repo | Role |
|---|---|---|
| `tequila/reflection` | `~/labs/tequila/reflector`, branch `phaenon-dogfood` (local only) | M1: built from `.phn`; its phpunit, PHPStan 10 and php-cs-fixer pass. M6: sibling layout (`src`/`tests` → `.src`/`.tests`, no committed PHP), consumed by core as a path repository |
| `tequila/object-mapper` | `~/labs/tequila/object-mapper`, branch `phaenon-dogfood` (local only) | M2: `phn check src` = 0 findings (2 errors since the checker grew: P2202 on `ObjectMapper::map()`, vault `dogfood`). M3: migrated with real generics (51 `.phn`), suite + PHPStan + cs pass. `tools/phpstan` is the parity PHPStan. M6: sibling layout, consumed by core as a path repository |
| `tequila/mongodb-odm` | `~/labs/tequila/mongodb-odm`, branch `phaenon-dogfood` (local only) | M3: migrated (125 `.phn`), suite + PHPStan max + cs pass, `phn check` 0 errors at M3, 3 since the checker grew (vault `dogfood`) (`main` went to level max for it). Needs ext-mongodb in the local `php`. M6: sibling layout, consumed by core as a path repository |
| `tequila/core` | `~/labs/tequila/core`, branch `phaenon-dogfood` (local only) | M4: migrated (800 `.phn`; the 12 PHPStan rule fixtures in `tests/PHPStan/Data` are plain `.php`, copied as written) on a `main` at PHPStan level max; `phaenon.json` builds `src`/`tests` into `.src`/`.tests` (gitignored), where Composer, PHPUnit, PHPStan and cs look; suite (1188 tests), PHPStan max and cs (no compiled-tree exceptions) pass on the output; `phn check` 0 findings since 2026-11-05, and `build` runs without `--emit-on-error` (vault `dogfood`); `exclude` lists the analyser tooling and what `phpstan.neon` excludes. Run `composer update` after switching to the branch: `composer.lock` is ignored. M6: reflection, object-mapper and mongodb-odm come from the sibling dogfood checkouts as path repositories (`dev-phaenon-dogfood as` the released version), `phaenon/phn` from this checkout, whose `bin/phn` the plugin runs to build them at every autoload dump; its own build stays `composer build` (`extra.phaenon.build: false`) |
| `tq-cli` | `~/labs/tequila/tq-cli-phaenon-dogfood` (a worktree of `~/labs/tequila/tq-cli`), branch `phaenon-dogfood` (local only) | M5: 859 `.phn`, the fifteen `Api::concurrently` fan-outs as `concurrently` blocks; suite (2052 tests), PHPStan 10 and cs pass on the output, `phn check` 0 over `src` and `tests`. Requires `phaenon/concurrent` and `phaenon/runtime` as path repositories from the sibling checkouts, and `phaenon/phn` from this checkout: since M6 the plugin builds `src`/`tests` at every autoload dump with `bin/phn` |
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
cmd/phn/              CLI: build, check, fix, fmt, lsp, watch, migrate, stdlib, trace, lex, print
internal/token        token set: PHP 8.5's plus Phaenon's, named like token_name()
internal/lexer        hand-written port of zend_language_scanner.l; lossless
internal/ast          typed node families; Phaenon constructs are additions to the PHP tree
internal/parser       recursive descent, Pratt expressions (precedence from zend_language_parser.y)
internal/printer      AST → canonical PHP, token-faithful round trip
internal/docblock     PHPDoc tags and the PHPStan/Psalm type grammar
internal/types        the type lattice (PHPStan semantics): subtyping, narrowing, printing
internal/program      program model: project files, Composer vendor code (lazy), stubs + overlay
internal/checker      the type checker behind `phn check`
internal/emit         patch-based emitter: PER Coding Style 3.0 layout of what it writes (D22), the line map; generics erasure, is/as lowering
internal/linemap      the `// phaenon-map v1 …` comment every compiled file ends with: parse, write, lookups
internal/format       `phn fmt`'s first layer: Phaenon's layout (spaces for tabs, migrate's braces and docblocks, the spacing of Phaenon's syntax)
internal/tabs         the tabs Phaenon forbids (D21: outside strings and comments' text), found for P1005 and replaced by migrate and fmt
internal/lsp          `phn lsp`: JSON-RPC on stdio, a program per check (parsed trees shared), hover, definition, completion, build on save
internal/fix          `phn fix`: a PHP tool's changes of the outputs carried back to the sources through the emitter's verbatim spans
internal/trace        `phn trace`: positions in compiled files → `.phn` positions, in text, GitHub workflow commands and CI reports (Checkstyle, JUnit, Clover, Cobertura)
internal/migrate      .php → .phn: tag stripping, generic docblocks lifted into syntax, `function` → `fun`
internal/conformance  suites over the php-src corpus with `php` as the oracle
stdlib/               embedded: php-src stubs, Phaenon's overlay, the phaenon/runtime and phaenon/concurrent sources (.phn), the amp/Revolt declarations;
                      suggested/: the Monolog and Sentry API the runtime's Debug integrations are checked against, loaded only by the stdlib's own programs
stdlib/gen            generator of stdlib/php-src (the newest release branch's stubs), versions_gen.go (the release each symbol came in) and constants.php
internal/distribution tests of the phaenon/phn Composer plugin under a real Composer (PHAENON_COMPOSER)
composer/             the phaenon/phn Composer plugin (PHP): the pinned binary, vendor/bin/phn, the builds at the autoload dump; composer.json at the root, .gitattributes keeps the dist to it
tools/release         the release binaries: reproducible builds for five platforms, their checksums in composer/checksums.json
.github/workflows     the release PR pins the checksums, the tag publishes the binaries (design/tooling.md §3.2)
```

## Build and test

```sh
go build -o bin/phn ./cmd/phn      # bin/ is gitignored; or `go run ./cmd/phn <command> …`
go test ./...                      # unit and golden tests; suites below skip without their variable

PHAENON_PHP_SRC=$HOME/labs/phaenon/php-src go test ./internal/conformance/
PHAENON_PHP_SRC=$HOME/labs/phaenon/.cache/php-src-8.6 PHAENON_PHP=/opt/homebrew/opt/php@8.6/bin/php \
  go test ./internal/conformance/                                      # PHP-8.6 corpus under PHP 8.6 (vault testing)
PHAENON_EXEC=1 PHAENON_PHP_SRC=$HOME/labs/phaenon/php-src \
  go test ./internal/conformance/ -run Execution -timeout 30m          # ≈2.5 min per suite
PHAENON_PHPSTAN=$HOME/labs/tequila/object-mapper/tools/phpstan/vendor/bin/phpstan \
  go test ./internal/checker ./internal/emit                           # PHPStan level max on goldens
PHAENON_UPDATE=1 go test ./internal/emit                               # rewrite emitter goldens
PHAENON_PHP86=/opt/homebrew/opt/php@8.6/bin/php go test ./internal/emit  # goldens compiled for 8.6: php -l, behaviour
PHAENON_PHP_SRC=$HOME/labs/phaenon/php-src go generate ./stdlib        # refresh stubs + version table (vault `testing`)
PHAENON_COMPOSER=$(which composer) go test ./internal/distribution     # the Composer plugin under a real Composer
```

`phn` itself: `phn build|check|fix|fmt|lsp|watch|migrate|stdlib|trace|lex|print` — run `phn` without arguments for usage; the
README explains the Composer workflow and `phaenon.json` (roots → sibling output directories). Dogfood targets call `phn` from `PATH`, or, through the `phaenon/phn` plugin, this checkout's `bin/phn` (`go build -o bin/phn ./cmd/phn`; `PHN_BINARY` names another). `phn stdlib` writes the
runtime package (`--out ../runtime/src`) and the concurrent package (`--concurrent-out
../concurrent/src`) from `stdlib/runtime/**/*.phn` (a subdirectory there is one in `src/`:
`Debug/LineMap.phn` → `src/Debug/LineMap.php`) and `stdlib/concurrent/Scope.phn`; the tests
`TestRuntimePackageFresh` and `TestConcurrentPackageFresh` fail when a checkout is stale. They
look for the checkouts beside the repository root (`../../../<repo>` from their package) and skip
when there are none, as in a git worktree kept elsewhere: there, `phn stdlib … --check` with the
sibling paths does the same check. `phn stdlib` writes and checks the files that have a source and
removes none: a source deleted here is deleted from the package by hand.

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
- The emitter edits spans; plain PHP passes through byte for byte. What it writes or rewrites (the
  header, imports, docblocks, expression bodies, declaration braces, the closures of lowerings)
  is laid out as PER Coding Style 3.0 wants it (php-cs-fixer's `@PER-CS3x0`, design D22; an empty
  body `{}` stays as written), and every compiled file ends with a line map (`../design/line-map.md`): an edit is in place
  (its line breaks stand for the span's) or free (its own lines, holds in the map). The invariant
  is `TestLineMapProperty`: every source byte that reaches the output sits on a line whose forward
  lookup gives its source line. A position reported in compiled PHP goes through the map
  (`phn trace`), never by line number alone.
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
  never edit by hand, and read the diff before committing it, the map line included.
- **Line map vectors** `internal/linemap/testdata/vectors.json` are the reference of the format's
  three readers; the plugin and the runtime keep copies, which `TestVectorCopiesFresh` compares.
- **Migrate goldens** `internal/migrate/testdata/lift/X.php` → `X.phn` + `X.notes` (the tags left
  in place and why): same `PHAENON_UPDATE=1` rule; the `.phn` must parse and lift to itself.
- **Format goldens** `internal/format/testdata/X.phn` → `X.golden.phn`: the same `PHAENON_UPDATE=1`
  rule; the golden formats to itself. `PHAENON_FMT_TREE=<dir> go test ./internal/format -run
  TestTree` formats a dogfood tree in memory.
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
  source. The same rule holds in `design`, `runtime`, `concurrent`, `jetbrains` and `vscode`.
- `go.mod`'s module line is `github.com/phaenonlang/phaenon`; GoLand has rewritten it before (vault
  `gotcha-goland-rewrites-go-mod`).
