# Changelog

From 0.3.0 on, release-please writes each entry from the Conventional Commits merged since the
previous release (`release-please-config.json`). The two releases before were cut by hand.

## [0.2.1](https://github.com/phaenonlang/phaenon/compare/v0.2.0...v0.2.1) (2026-11-06)


### Bug Fixes

* **checker:** iterable narrowing and property redeclaration ([#7](https://github.com/phaenonlang/phaenon/issues/7)) ([a415429](https://github.com/phaenonlang/phaenon/commit/a415429dd04d12c0cca94f47b7b667437d21db49))

## [0.2.0](https://github.com/phaenonlang/phaenon/compare/v0.1.0...v0.2.0) (2026-11-06)

The first release on phaenon.dev. Since 0.1.0: exact tuples, the list-types codemod, throw
analysis and unused private members, `phn fix`, `phn fmt`, `phn lsp`, the VS Code extension, the
CI report formats, the erasure of closures in attributes, and the playground's WebAssembly build
beside the five binaries.

### ⚠ BREAKING CHANGES

* **types:** a tuple admits no longer value
* **emit:** lay out the compiled PHP as PER Coding Style 3.0
* **checker:** Phaenon's whitespace is spaces (D21)

## [0.1.0](https://github.com/phaenonlang/phaenon/releases/tag/v0.1.0) (2026-11-03)

The first release of `phn` and of the `phaenon/phn` Composer plugin (design/tooling.md §3).
