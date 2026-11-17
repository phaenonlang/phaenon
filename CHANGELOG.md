# Changelog

From 0.3.0 on, release-please writes each entry from the Conventional Commits merged since the
previous release (`release-please-config.json`). The two releases before were cut by hand.

## [0.7.2](https://github.com/phaenonlang/phaenon/compare/v0.7.1...v0.7.2) (2026-11-17)


### Bug Fixes

* **checker:** a ?-&gt; on null is null, and a loop over it settles ([#58](https://github.com/phaenonlang/phaenon/issues/58)) ([a285d1e](https://github.com/phaenonlang/phaenon/commit/a285d1e9c0235edbc70a64d20814c8d6d5e19b62))
* **checker:** a ?-&gt; property fetch under ??, isset() or empty() is P2307 ([#57](https://github.com/phaenonlang/phaenon/issues/57)) ([b68d2bb](https://github.com/phaenonlang/phaenon/commit/b68d2bb495ee6b6765a71c46e4a71a0d45d9e6c2))
* **checker:** a key of a few literals reads the entries it may name ([#61](https://github.com/phaenonlang/phaenon/issues/61)) ([0361a6b](https://github.com/phaenonlang/phaenon/commit/0361a6b8e27702f4b410e2470049258a08bc1502))
* **checker:** arrays built in loops, spreads and merges keep their shapes ([#66](https://github.com/phaenonlang/phaenon/issues/66)) ([64fb234](https://github.com/phaenonlang/phaenon/commit/64fb2347f361304cd44698519b139736acc70585))
* **checker:** five mistakes are reported once each ([#56](https://github.com/phaenonlang/phaenon/issues/56)) ([18364c9](https://github.com/phaenonlang/phaenon/commit/18364c99bd1f537543cfbbd46203d30ce1117200))
* **checker:** four reads of a missing offset get PHPStan's finding ([#60](https://github.com/phaenonlang/phaenon/issues/60)) ([ba431d0](https://github.com/phaenonlang/phaenon/commit/ba431d0710e3d8c47f43e3fd78098955e88e0818))
* **checker:** integer, double and boolean in a declaration are P2001 ([#59](https://github.com/phaenonlang/phaenon/issues/59)) ([bfea29f](https://github.com/phaenonlang/phaenon/commit/bfea29f6fc6f2ad2277cac3c04778ae6780517bf))
* **fmt:** the tabs of a heredoc are replaced in one run ([#64](https://github.com/phaenonlang/phaenon/issues/64)) ([d570590](https://github.com/phaenonlang/phaenon/commit/d5705900562028609df7bcf62a85071ccbace032))
* **parser:** a throws clause may name static ([#55](https://github.com/phaenonlang/phaenon/issues/55)) ([6a7379b](https://github.com/phaenonlang/phaenon/commit/6a7379be5d19c5e2cd926f75aa0968056ab496c2))

## [0.7.1](https://github.com/phaenonlang/phaenon/compare/v0.7.0...v0.7.1) (2026-11-16)


### Bug Fixes

* **checker:** a partial application of sprintf or max is its closure ([#52](https://github.com/phaenonlang/phaenon/issues/52)) ([0dafeff](https://github.com/phaenonlang/phaenon/commit/0dafeff7dd7476b05071d32ffa6843f7e01e09e0))
* **checker:** a write through a reference to a variable writes both ([#49](https://github.com/phaenonlang/phaenon/issues/49)) ([37e4b8f](https://github.com/phaenonlang/phaenon/commit/37e4b8fcff67769d340d471baf5df8ad568dc591))
* **checker:** gettype() compared with a type's name narrows its argument ([#53](https://github.com/phaenonlang/phaenon/issues/53)) ([c586c8e](https://github.com/phaenonlang/phaenon/commit/c586c8e34ec8db1151dda2a1b1aa48cf44f2245e))
* **migrate:** array-key|null is lifted as int|string|null ([#50](https://github.com/phaenonlang/phaenon/issues/50)) ([728290e](https://github.com/phaenonlang/phaenon/commit/728290e38391ac9e544791c890f0694df68af70e))
* **program:** a Composer files entry is declared once, from its source ([#51](https://github.com/phaenonlang/phaenon/issues/51)) ([3e57db7](https://github.com/phaenonlang/phaenon/commit/3e57db7c6bfcbebfa891ff88d8960986544a3315))

## [0.7.0](https://github.com/phaenonlang/phaenon/compare/v0.6.0...v0.7.0) (2026-11-15)


### Features

* **checker:** an unguarded offset a shape might lack is P2321 ([#46](https://github.com/phaenonlang/phaenon/issues/46)) ([7abd5e1](https://github.com/phaenonlang/phaenon/commit/7abd5e1f1726f865ebcfbdbeec999cd230460a93))


### Bug Fixes

* **checker:** unset() of an offset types the array as PHPStan does ([#44](https://github.com/phaenonlang/phaenon/issues/44)) ([16d79d8](https://github.com/phaenonlang/phaenon/commit/16d79d8f08c8164c47b361ff98fa7434ec78cfa0))
* **emit:** closures lowered in expression bodies are indented as a block ([#45](https://github.com/phaenonlang/phaenon/issues/45)) ([dd65e25](https://github.com/phaenonlang/phaenon/commit/dd65e25ecad56cf84fe81909be06f41ecb768c15))

## [0.6.0](https://github.com/phaenonlang/phaenon/compare/v0.5.0...v0.6.0) (2026-11-15)


### ⚠ BREAKING CHANGES

* **checker:** a long cast or an `(unset)` cast that compiled before is now P1001, an error. Run `phn fmt` over the tree to write the canonical casts; replace `(unset) $x` by `null`, or by `unset($x);`.

### Bug Fixes

* **checker:** a null for a built-in's omitted argument is P2103 ([#42](https://github.com/phaenonlang/phaenon/issues/42)) ([a31d9f5](https://github.com/phaenonlang/phaenon/commit/a31d9f56cf9cc3e2abcbb209df180495791fd2ec))
* **checker:** a type keyword in a throws clause is P2319, and only that ([#41](https://github.com/phaenonlang/phaenon/issues/41)) ([ccae585](https://github.com/phaenonlang/phaenon/commit/ccae58512d1f395aa0036cee0ece291b1c534d32))
* **checker:** bytes compared with text stay checked, and the findings name mb_check_encoding() ([#40](https://github.com/phaenonlang/phaenon/issues/40)) ([9f9721b](https://github.com/phaenonlang/phaenon/commit/9f9721b4e78053f0692a44962b1544e3589974bd))
* **checker:** the long casts and (unset) are refused ([#39](https://github.com/phaenonlang/phaenon/issues/39)) ([d1d37f4](https://github.com/phaenonlang/phaenon/commit/d1d37f44823e126569e1629ae04d405569819bc2))

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
