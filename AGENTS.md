# Phaenon — agent guide

Phaenon is a typed dialect of PHP 8.5+ that compiles to idiomatic PHP ("TypeScript for PHP"):
the `.phn` sources of a Composer project compile into a sibling output directory (`src/` →
`.src/`, named in `phaenon.json`) holding the PHP you would have written by hand. That output
depends on nothing but PHP (`../design/roadmap.md` D24): the compiler adds the header,
`declare(strict_types=1)`, docblocks, and PHP's own functions and classes where the source used
`is`, `as` or a construct it lowers, and never a call into a package. The type checker is the
product. This repository is the compiler, `phn`, written in Go. Pre-alpha; MIT-licensed; GitHub
org `phaenonlang`.

**This repository is the entry point for agents.** Start here; the map below says where everything
else lives. The language design and every decision are in the `design` repository. Milestone
history, the dogfood notes and the rest of the project's working memory are the maintainers' local
working notes, not part of this repository: what a change needs is here, in `../design` and in the
README.

## Org map

The other repositories are sibling checkouts beside this one, reached as `../<name>`.

| Repo | Path | Remote | Holds | Build / test |
|---|---|---|---|---|
| **phaenon** | `.` | `phaenonlang/phaenon` | The compiler `phn`: lexer, parser, checker, emitter, migrate; embedded PHP signatures | [Build and test](#build-and-test) |
| **design** | `../design` | `phaenonlang/design` | The language design and every decision: `language-overview.md` (scope), `generics.md` (decisions §11), `checker.md` (decisions §9, open §10), `roadmap.md` (decisions D1–D8, milestones M0–M7, working method §4) | Markdown only |
| **debug** | `../debug` | `phaenonlang/debug` | The package `phaenon/debug` (`phaenon/runtime` until 0.3): `Phaenon\Debug`, the line map reader (`LineMap`), a Monolog processor and a Sentry integration that point production errors at `.phn` positions (`../design/line-map.md` §4.3, L6); opt-in, suggested by the plugin and never called by compiled code (`../design/tooling.md` T16); `src/` is compiled from `stdlib/runtime/**/*.phn` | `composer install`, `composer test`, `composer stan` |
| **jetbrains** | `../jetbrains` | `phaenonlang/jetbrains` | PhpStorm plugin: `.phn` read as PHP code without the open tag (Kotlin, Gradle); `phn lsp`'s diagnostics and build on save; Reformat Code ends with `phn fmt --stdin` | `./gradlew test` (`PHAENON_PHN=bin/phn` of this repo for the layout tests), `./gradlew buildPlugin` |
| **vscode** | `../vscode` | `phaenonlang/vscode` | VS Code extension (TypeScript): the TextMate grammar, `source.php` plus injections for Phaenon's syntax (the PhpStorm plugin's TextMate bundle copies it); one `phn lsp` per project with §6.1's whole list; build, check and watch as tasks; `phn trace` of the selection or the terminal | `npm install`, `npm test` (`PHAENON_TREES` for whole trees), `PHAENON_PHN=../phaenon/bin/phn npm run test:e2e`, `npm run package` (`.vsix`) |
| **website** | `../website` | `phaenonlang/website` | phaenon.dev: the landing page, the playground and the compiler's release binaries as public, immutable downloads under `/releases/{version}/{file}` (`tooling.md` §3.2), uploaded from each release once `release.yml` sends it the tag | its README |
| **php-src** | `../php-src` | a read-only clone of `php/php-src` beside this repository | **Read-only reference.** The scanner and grammar the lexer and parser port, the `.phpt` corpus of the conformance suite, the source of `stdlib/php-src` | Never modify, build or change its git state |

**Dogfood.** The language is developed against the Tequila Platform's PHP codebase
([tequila.dev](https://tequila.dev)) as its dogfood, from the libraries to the services: a feature
is done when a dogfood codebase's own suite, PHPStan and coding-style checks pass on its compiled
output. `../design/roadmap.md` §3 names the milestones and what each one brings onto Phaenon.

## Where to look things up

| Question | Look in |
|---|---|
| What the language is, and will be | `../design/language-overview.md`, then the feature's doc (`functions.md` for `fun` and closures in constant expressions) |
| Why something is the way it is | The decision sections: `roadmap.md` §1, `checker.md` §9, `generics.md` §11 — they win over the draft text above them |
| Milestone status and what is next | `../design/roadmap.md` §3 |
| Open questions and deferred work | `../design/roadmap.md` §6, `checker.md` §10, `tooling.md` §13; the repository's issues |
| Every `PHAENON_*` test variable | [Test variables](#test-variables) |
| A diagnostic code | `internal/checker/codes.go` |
| What PHP does | `php` itself (the oracle), then `../php-src` |
| A PHP function's signature | `stdlib/php-src/**/*.stub.php`, refined by `stdlib/overlay/` |

## This repository

```text
cmd/phn/              CLI: build, check, fix, fmt, lsp, watch, migrate, stdlib, trace, lex, print
cmd/phn-wasm/         the compiler for WebAssembly (js/wasm only): globalThis.phaenon.compile for phaenon.dev's playground
internal/token        token set: PHP 8.5's plus Phaenon's, named like token_name()
internal/lexer        hand-written port of zend_language_scanner.l; lossless
internal/ast          typed node families; Phaenon constructs are additions to the PHP tree
internal/parser       recursive descent, Pratt expressions (precedence from zend_language_parser.y)
internal/printer      AST → canonical PHP, token-faithful round trip
internal/docblock     PHPDoc tags and the PHPStan/Psalm type grammar
internal/types        the type lattice (PHPStan semantics): subtyping, narrowing, printing
internal/program      program model: project files, Composer vendor code (lazy), stubs + overlay
internal/checker      the type checker behind `phn check`; lowers is/as to PHP's own tests (typetest.go)
internal/emit         patch-based emitter: PER Coding Style 3.0 layout of what it writes (D22), the line map; generics erasure, is/as as the checker lowered them
internal/linemap      the `// phaenon-map v1 …` comment every compiled file ends with: parse, write, lookups
internal/format       `phn fmt`'s first layer: Phaenon's layout (spaces for tabs, migrate's braces and docblocks, the spacing of Phaenon's syntax)
internal/tabs         the tabs Phaenon forbids (D21: outside strings and comments' text), found for P1005 and replaced by migrate and fmt
internal/lsp          `phn lsp`: JSON-RPC on stdio, a program per check (parsed trees shared), hover, definition, completion, build on save
internal/fix          `phn fix`: a PHP tool's changes of the outputs carried back to the sources through the emitter's verbatim spans
internal/trace        `phn trace`: positions in compiled files → `.phn` positions, in text, GitHub workflow commands and CI reports (Checkstyle, JUnit, Clover, Cobertura)
internal/migrate      .php → .phn: tag stripping, generic docblocks lifted into syntax, `function` → `fun`
internal/playground   one file checked and compiled in memory, the embedded stdlib alone: what cmd/phn-wasm answers (design/tooling.md §3.6)
internal/conformance  suites over the php-src corpus with `php` as the oracle
stdlib/               embedded: php-src stubs, Phaenon's overlay, the phaenon/debug sources (runtime/**/*.phn);
                      suggested/: the Monolog and Sentry API the Debug integrations are checked against, loaded only by the stdlib's own programs
stdlib/gen            generator of stdlib/php-src (the newest release branch's stubs), versions_gen.go (the release each symbol came in) and constants.php
internal/distribution tests of the phaenon/phn Composer plugin under a real Composer (PHAENON_COMPOSER)
composer/             the phaenon/phn Composer plugin (PHP): the pinned binary, vendor/bin/phn, the builds at the autoload dump; composer.json at the root, .gitattributes keeps the dist to it
tools/release         the release assets: reproducible builds for five platforms and the playground's phn.wasm.gz + wasm_exec.js, their checksums in composer/checksums.json
.github/workflows     CI; the PR semantics gate (.github/scripts); release-please's release PR, pinned by release-checksums.yml, published when merged (design/tooling.md §3.2)
```

## Build and test

```sh
go build -o bin/phn ./cmd/phn      # bin/ is gitignored; or `go run ./cmd/phn <command> …`
go test ./...                      # unit and golden tests; suites below skip without their variable

PHAENON_PHP_SRC=/path/to/php-src go test ./internal/conformance/
PHAENON_PHP_SRC=/path/to/php-src-8.6 PHAENON_PHP=/path/to/php8.6 \
  go test ./internal/conformance/                                      # a release branch's corpus under its php
PHAENON_EXEC=1 PHAENON_PHP_SRC=/path/to/php-src \
  go test ./internal/conformance/ -run Execution -timeout 30m          # ≈2.5 min per suite
PHAENON_PHPSTAN=/path/to/phpstan \
  go test ./internal/checker ./internal/emit                           # PHPStan level max on goldens
PHAENON_UPDATE=1 go test ./internal/emit                               # rewrite emitter goldens
PHAENON_PHP86=/path/to/php8.6 go test ./internal/emit                  # goldens compiled for 8.6: php -l, loading, behaviour
go generate ./stdlib                                                   # refresh stubs + version table from the php-src clone
PHAENON_COMPOSER=$(which composer) go test ./internal/distribution     # the Composer plugin under a real Composer
GOOS=js GOARCH=wasm go build -o bin/phn.wasm ./cmd/phn-wasm            # the playground's compiler (tests: internal/playground)
```

A release branch's corpus is extracted from the clone, never checked out, so that the clone stays
read-only:

```sh
mkdir -p /path/to/php-src-8.6
git -C /path/to/php-src archive origin/PHP-8.6 -- ':(glob)**/*.phpt' ':(glob)**/*.inc' | tar -x -f - -C /path/to/php-src-8.6
```

`go generate ./stdlib` reads the clone `PHAENON_PHP_SRC` names, else `../php-src`, through `git
archive` only; `go run ./stdlib/gen -php /path/to/php8.6` also rewrites `stdlib/constants.php` from
a php of the newest release.

CI (`.github/workflows/ci.yml`) runs `gofmt -l`, `go vet` (native and wasm) and `go test ./...` with
PHP 8.5 and Composer on the runner; the suites gated by a variable skip there, while the plugin's
suite runs, `PHAENON_COMPOSER` naming the runner's Composer.

### Test variables

Without its variable a suite skips, so a green `go test ./...` says nothing about conformance or
PHPStan parity.

| Variable | Read by | Effect |
|---|---|---|
| `PHAENON_PHP_SRC` | `internal/conformance`; `TestFresh` (`stdlib/gen`) | Absolute path of a php-src tree (`/path/to/php-src`): the `.phpt` corpus, a clone or a release branch extracted as above; for `TestFresh`, the clone whose release branches the committed stubs and version table must match |
| `PHAENON_PHP` | `internal/conformance`; `TestPHPStanParity` | The php taken as the oracle, `php` on `PATH` by default; the emit execution suite compiles for its release line, and an 8.6 here also serves the parity run's 8.6 goldens |
| `PHAENON_PHP86` | `TestGenerics` (emit), `TestLift` (migrate), `TestPHPStanParity` | A PHP 8.6 (`/path/to/php8.6`): `php -l`, the loading and the behaviour of the outputs compiled for 8.6, and PHPStan on it for the goldens whose header says `// target: 8.6` |
| `PHAENON_EXEC` | `TestPrinterExecution`, `TestEmitExecution` | Any non-empty value runs the execution comparisons (≈2.5 min each; pass `-timeout 30m`) |
| `PHAENON_PHPT_FILTER` | `internal/conformance` | Only the `.phpt` paths containing this substring, e.g. `Zend/tests` |
| `PHAENON_SHOW_DIFFS` | printer round trip, execution suites | How many differences to print (default 25) |
| `PHAENON_SHOW_FALSE_ACCEPT` | `TestParserAgainstPHP` | Lists (up to 30) files PHP rejects but Phaenon parses |
| `PHAENON_PHPSTAN` | `TestPHPStanParity` (checker), `TestGenerics` (emit) | Path to a PHPStan binary (`/path/to/phpstan`), run at level max with `treatPhpDocTypesAsCertain: false` (`../design/checker.md` §9) |
| `PHAENON_UPDATE` | the emit, format, migrate and trace goldens | Rewrites the expected files from the current output |
| `PHAENON_GOLDEN` | `TestGolden` (checker) | Only the golden files whose name contains this substring |
| `PHAENON_COMPOSER` | `internal/distribution` | A composer binary: the `phaenon/phn` plugin runs under it in temporary projects that install it alone; one of them builds `phn` and runs what it compiles |
| `PHAENON_COMPOSER_PROJECT` | `TestComposer` (program) | A Composer project to exercise the vendor reader on |
| `PHAENON_FMT_TREE` | `TestTree` (format) | Any Phaenon tree: each `.phn` must format, and to itself a second time (`-v` lists the files it would change) |
| `PHAENON_LINEMAP_TREE` | `TestLineMapTree` (emit) | Any Phaenon tree: `TestLineMapProperty`'s invariant over every `.phn` in it, and `TestOutputsDependOnPHPOnly`'s promise over every output (nothing under `Phaenon\`) |
| `PHAENON_LINEMAP_OUT` | `TestLineMapTree` (emit) | A directory the compiled files of that tree are written to |
| `PHAENON_LISTTYPES` | `TestListTypesMeasure` (`cmd/phn`) | Phaenon trees (each a `phaenon.json`'s directory, separated as `PATH` is) the `list-types` codemod is measured on |
| `PHAENON_LISTTYPES_MIN` | `TestListTypesMeasure` | The floor, in percent, of declarations that must come back as written or narrower (0 by default) |
| `PHAENON_LISTTYPES_FINDINGS` | `TestListTypesMeasure` | Lists the findings left with the codemod's types |
| `PHAENON_THROWS` | `TestThrowsMeasure` (`cmd/phn`) | Phaenon trees, as above, the `throws` rules are measured on (`../design/tooling.md` §8.3) |
| `PHAENON_THROWS_UNCHECKED` | `TestThrowsMeasure` | More unchecked exception classes, comma-separated |
| `PHAENON_THROWS_OWNED` | `TestThrowsMeasure` | Counts only the exception classes under these namespace prefixes, comma-separated |
| `PHAENON_THROWS_LIST` | `TestThrowsMeasure` | Lists every clause the own rule writes |
| `PHAENON_THROWS_FINDINGS` | `TestThrowsMeasure` | Lists the findings the clauses add |

The editor repositories read variables of their own (`PHAENON_PHN`, `PHAENON_TREES`, …): see the
org map above and their READMEs.

`phn` itself: `phn build|check|fix|fmt|lsp|watch|migrate|stdlib|trace|lex|print` — run `phn` without arguments for usage; the
README explains the Composer workflow and `phaenon.json` (roots → sibling output directories). Dogfood targets call `phn` from `PATH`, or, through the `phaenon/phn` plugin, this checkout's `bin/phn` (`go build -o bin/phn ./cmd/phn`; `PHN_BINARY` names another). `phn stdlib` writes the
debug package (`--out ../debug/src`) from `stdlib/runtime/**/*.phn` (a subdirectory there is
one in `src/`: `Debug/LineMap.phn` → `src/Debug/LineMap.php`); the test `TestDebugPackageFresh`
fails when the checkout is stale. It looks for the checkout beside the repository root
(`../../../debug` from its package) and skips when there is none, as in a git worktree kept
elsewhere: there, `phn stdlib --out ../debug/src --check` does the same check. `phn stdlib`
writes and checks the files that have a source and removes none: a source deleted here is deleted
from the package by hand.

## How work is done

- **Design doc → golden tests → implementation → PHPStan on the output → Tequila dogfood**, for
  every feature. A feature starts as a section in a `../design` doc and as golden tests that fail;
  it is done when the dogfood target's own suite passes on compiled output. The working method is
  `../design/roadmap.md` §4.
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
- Emitted PHP must pass `php -l`, load under php (which links the classes it declares) and pass
  PHPStan at level max, with nothing beside it: it names no class, function or constant under
  `Phaenon\` (`TestOutputsDependOnPHPOnly`, `../design/roadmap.md` D24).

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
  three readers; the PhpStorm plugin and `phaenon/debug` keep copies, which
  `TestVectorCopiesFresh` compares.
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

- One change per commit, staged by path: a reviewer reads a diff that is about one thing, and
  goldens, generated files and the design text that go with a change travel in the same commit.
- **Commit messages follow [Conventional Commits](https://www.conventionalcommits.org):** `type(scope):
  subject`, the scope a package or area (`feat(checker):`, `fix(emit):`, `docs(design):`), `!` for
  a breaking change to the language or the CLI, and a body that says what changed and why. **No
  attribution lines, no `Co-Authored-By`, no "Generated with"** — the repositories are open
  source. The same rule holds in `design`, `debug`, `jetbrains` and `vscode`.
- **A PR is squash-merged, so its title becomes the commit on `main` that release-please reads** to
  version the next release and write `CHANGELOG.md`: the title is a Conventional Commit header whose
  release impact (`!` or a `BREAKING CHANGE:` footer, then `feat`, then `fix` and `revert`, then the
  rest) equals the highest of the PR's commits, which `pr-semantics.yml` checks.
- `go.mod`'s module line is `github.com/phaenonlang/phaenon`. GoLand has rewritten it to `module
  phaenon` before, which breaks every import of the repository's own packages: put the line back by
  hand and never commit the rewritten one (`go mod init` or `go mod tidy` repairs it the wrong way).
