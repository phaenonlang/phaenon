# Changelog

From 0.3.0 on, release-please writes each entry from the Conventional Commits merged since the
previous release (`release-please-config.json`). The two releases before were cut by hand.

## [0.5.0](https://github.com/phaenonlang/phaenon/compare/v0.4.1...v0.5.0) (2026-11-13)


### Features

* **build:** a library's committed roots are left as installed ([#20](https://github.com/phaenonlang/phaenon/issues/20)) ([14e215c](https://github.com/phaenonlang/phaenon/commit/14e215cc83efb82839d6def46e1ad460244b772c))

## [0.4.1](https://github.com/phaenonlang/phaenon/compare/v0.4.0...v0.4.1) (2026-11-10)


### Bug Fixes

* **emit:** a pipe in an expression body maps to the pipeline's line ([9eea388](https://github.com/phaenonlang/phaenon/commit/9eea3880446f9448d0954d7b22ca2a87d55500d6))

## [0.4.0](https://github.com/phaenonlang/phaenon/compare/v0.3.0...v0.4.0) (2026-11-09)


### ⚠ BREAKING CHANGES

* `fun`, `is`, `as` and `as?` are no longer Phaenon syntax; phn fmt rewrites a tree that holds them.
* `phaenon/phn` no longer requires `phaenon/runtime` and conflicts with it below 0.4; a project that used the Debug integrations requires `phaenon/debug`.
* `concurrently` and `spawn` are no longer syntax; a program that wants amp writes amp.
* **emit:** `\Phaenon\Bytes` is removed; `$b as string` is the checked conversion, `is bytes` and `as bytes` are errors.

### Features

* concurrently and spawn leave the language ([cf521e8](https://github.com/phaenonlang/phaenon/commit/cf521e80a9df444352b9d7ba467f45f588e83efe))
* **emit:** bytes as string is the checked conversion to text ([26427df](https://github.com/phaenonlang/phaenon/commit/26427df578a4b123dc65d3d262443627020fe453))
* methods without a keyword; is, as and fun leave the language ([b9e86d8](https://github.com/phaenonlang/phaenon/commit/b9e86d8e3bf6074cdf71abb7ddcf5ba2a533e1e8))
* phaenon/phn requires no package ([e3c9d6d](https://github.com/phaenonlang/phaenon/commit/e3c9d6d47b9e9de83075b3f2941cf64d86f2f7b5))

## [0.3.0](https://github.com/phaenonlang/phaenon/compare/v0.2.1...v0.3.0) (2026-11-07)


### Features

* **checker:** P3010 warns of a type argument nothing determines ([#10](https://github.com/phaenonlang/phaenon/issues/10)) ([6b9a0cd](https://github.com/phaenonlang/phaenon/commit/6b9a0cd7db62c5b21a7af02eb2dbd9c4aa6bdd94))

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
