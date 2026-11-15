# Changelog

From 0.3.0 on, release-please writes each entry from the Conventional Commits merged since the
previous release (`release-please-config.json`). The two releases before were cut by hand.

## [0.5.0](https://github.com/phaenonlang/phaenon/compare/v0.4.1...v0.5.0) (2026-11-15)


### ⚠ BREAKING CHANGES

* a word Phaenon adds written in another letter case than lower case no longer compiles where it did: `THROWS X`, `NoThrow`, `TYPE Row = …;`, `<T OF Item>`, `<OUT T>`, `List<int>`, `CLASS<T>` and `(BYTES)` are P1006. Write the word in lower case, or run `phn fmt`, which does.
* aliases share one table of names per namespace. Two files of a namespace that each declared a `type Row` for themselves, an alias beside a class of its name, and two aliases whose names differ in case only compiled before and are now P2511: rename one of the two.

### Features

* **build:** a library's build checks its conflict on older compilers ([#26](https://github.com/phaenonlang/phaenon/issues/26)) ([17302aa](https://github.com/phaenonlang/phaenon/commit/17302aa41b017e9c133bde4eac28667f5dd29a32))
* **build:** a library's committed roots are left as installed ([#20](https://github.com/phaenonlang/phaenon/issues/20)) ([14e215c](https://github.com/phaenonlang/phaenon/commit/14e215cc83efb82839d6def46e1ad460244b772c))
* **composer:** the plugin leaves committed roots as installed ([#23](https://github.com/phaenonlang/phaenon/issues/23)) ([3775a64](https://github.com/phaenonlang/phaenon/commit/3775a645eb581a8b215f47a6fc1a1bdf24adc7bb))
* named shapes, and a type alias belongs to its namespace ([#34](https://github.com/phaenonlang/phaenon/issues/34)) ([1df5785](https://github.com/phaenonlang/phaenon/commit/1df5785a685a1330252062775e91a409a18e8c23))
* nothrow, the clause spelling of [@throws](https://github.com/throws) void ([#33](https://github.com/phaenonlang/phaenon/issues/33)) ([e2c3e26](https://github.com/phaenonlang/phaenon/commit/e2c3e26204ce70e7061b76df96abe13da9fa10b2))
* the words Phaenon adds to PHP are written in lower case ([#38](https://github.com/phaenonlang/phaenon/issues/38)) ([90f1de7](https://github.com/phaenonlang/phaenon/commit/90f1de72a2cba9285eac2054a7669cea50c05c7f))


### Bug Fixes

* **checker:** an override is held to Phaenon's types, against every interface it implements ([#28](https://github.com/phaenonlang/phaenon/issues/28)) ([53007f8](https://github.com/phaenonlang/phaenon/commit/53007f80dbc27ebbcf1166b91011d8a6394bfae2))
* **checker:** count() on either side of a comparison, a constructor's visibility as PHP checks it ([#35](https://github.com/phaenonlang/phaenon/issues/35)) ([115e29e](https://github.com/phaenonlang/phaenon/commit/115e29e36d190460500a5cc5de5d4104355b5bae))
* **checker:** report a named argument that overwrites an earlier one ([#25](https://github.com/phaenonlang/phaenon/issues/25)) ([19f7987](https://github.com/phaenonlang/phaenon/commit/19f7987862e30ebe8db63806a975f357e9912939))
* **checker:** shapes are held as PHPStan holds them ([#37](https://github.com/phaenonlang/phaenon/issues/37)) ([b7c7bc1](https://github.com/phaenonlang/phaenon/commit/b7c7bc197c024a4c0c2784f92315240d1b518bde))
* **checker:** static inside a shape, a void arrow function and three more misjudgements ([#29](https://github.com/phaenonlang/phaenon/issues/29)) ([13aa9f9](https://github.com/phaenonlang/phaenon/commit/13aa9f93c92eb896eb8ab85df3e5006297a7b644))
* **cli:** watch from a subdirectory and on project changes, fmt on directories and broken files ([#32](https://github.com/phaenonlang/phaenon/issues/32)) ([b231123](https://github.com/phaenonlang/phaenon/commit/b231123df7910c20cf9c6a369ecc144a865101c8))
* **emit:** anonymous classes, typed constants, hooks and a dropped `...` compile to valid PHP ([#30](https://github.com/phaenonlang/phaenon/issues/30)) ([2356fd4](https://github.com/phaenonlang/phaenon/commit/2356fd4acf80e260f2a3b4412b087a8cb06c2797))
* **parser:** a type-argument guess leaves the tokens as it found them ([#27](https://github.com/phaenonlang/phaenon/issues/27)) ([7938815](https://github.com/phaenonlang/phaenon/commit/793881529f61012f9dc3768b916e0ba666895b0f))
* **program:** an alias cycle, an [@method](https://github.com/method) default and an extension's DNF type are read as PHP reads them ([#31](https://github.com/phaenonlang/phaenon/issues/31)) ([142e3ea](https://github.com/phaenonlang/phaenon/commit/142e3ea29dfc42dbccefcdbf8b6d528db6b7d14b))
* **program:** an override is erased against the declarations PHP holds it to ([#36](https://github.com/phaenonlang/phaenon/issues/36)) ([7c43fde](https://github.com/phaenonlang/phaenon/commit/7c43fdea8a54a4b46f4ee1990aebb625ad4b49c2))

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
