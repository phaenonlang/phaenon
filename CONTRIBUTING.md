# Contributing to Phaenon

Phaenon is pre-alpha: the language still changes, and a change to the language starts as a change
to its design.

## Before you start

- **A bug:** open an issue with the smallest `.phn` or `.php` that shows it, the command, what
  `phn` printed and what you expected. For anything that is PHP, what `php` itself does is the
  reference.
- **A language change** (new syntax, a new diagnostic, a change in what the checker accepts or the
  emitter writes) starts in [`phaenonlang/design`](https://github.com/phaenonlang/design), as a
  change to the feature's design document. The maintainers decide design changes, so open an issue
  to discuss the idea before writing the code.
- **A security issue** goes through private reporting, never a public issue: see
  [SECURITY.md](SECURITY.md).

## Building and testing

The Go toolchain `go.mod` names is all the build needs; the module has no dependencies. With `php`
8.5 on `PATH`, the tests that take PHP as the oracle run too.

```sh
go build -o bin/phn ./cmd/phn
go test ./...
```

The suites that need a php-src clone, a PHPStan install, a PHP 8.6 or Composer skip without their
variable. [`AGENTS.md`](AGENTS.md), "Build and test", lists them all; run the ones your change
touches.

## What a pull request must satisfy

Pull requests are squash-merged: the PR title becomes the commit on `main` from which
release-please versions the next release and writes the changelog. The PR semantics gate
(`.github/scripts/pr-semantics.sh`) checks that

- the title is a [Conventional Commits](https://www.conventionalcommits.org) header,
  `type(scope): subject`, such as `fix(checker): …` or `feat(emit): …`, with `!` for a breaking
  change to the language or the CLI;
- every commit in the PR is a Conventional Commit too, with a body that says what changed and why;
- the title's release impact equals the highest of the commits': breaking (`!` or a
  `BREAKING CHANGE:` footer), then `feat`, then `fix` and `revert`, then the rest (`docs`,
  `refactor`, `test`, `build`, `ci`, `chore`, …).

CI runs `gofmt -l`, `go vet` (native and WebAssembly) and `go test ./...`. Beyond what CI checks,
the conventions in `AGENTS.md` hold, chiefly:

- **gofmt**, and the standard library only: adding a requirement to `go.mod` is the maintainers'
  decision.
- **Golden files are regenerated, never edited by hand** (`PHAENON_UPDATE=1`), and their diff is
  read before it is committed, the line map included. Golden files and `stdlib/php-src` are
  byte-sensitive: no formatter or whitespace clean-up touches them.
- **PHPStan parity:** the checker may be stricter than PHPStan, never more lenient, and emitted PHP
  passes `php -l` and PHPStan at level max. A PHPStan rule Phaenon deliberately lacks goes into the
  allow-list in `internal/checker/phpstan_test.go` with its reason.
- **Diagnostic codes are stable** once published: one mistake, one diagnostic.
- **A language change comes with its design-doc change** in `phaenonlang/design`, linked from the
  PR.

## License

Contributions are licensed under the [MIT License](LICENSE), as the rest of the repository is.
