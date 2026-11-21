# Changelog

From 0.3.0 on, release-please writes each entry from the Conventional Commits merged since the
previous release (`release-please-config.json`). The two releases before were cut by hand.

## [0.8.0](https://github.com/phaenonlang/phaenon/compare/v0.7.2...v0.8.0) (2026-11-21)


### ⚠ BREAKING CHANGES

* **build:** a project whose plain .php under a root has an error, which phn check reports, now fails phn build and the Composer plugin's build with it. Correct the file, or name it under "exclude" in
* **checker:** a bare `@phaenon-ignore`, or one that names PHPStan identifiers such as `argument.type`, silenced every finding of its line and silences nothing now; it is the warning P4015, and the findings it hid are reported again. Name their codes, as in `@phaenon-ignore P2103`, or write `@phaenon-ignore-line`.
* **checker:** a generic closure is P1007 until it is designed ([#84](https://github.com/phaenonlang/phaenon/issues/84))
* **composer:** vendor/phaenon/phn/bin/phn no longer exists. Anything that ran it directly runs vendor/bin/phn instead. A runtime image that drops the compiler with `rm -f vendor/phaenon/phn/bin/phn` now removes nothing and keeps the binary: remove the directory instead, `rm -rf vendor/phaenon/phn/bin`, or the `phn-*` files in it. If vendor/bin/phn is run there afterwards, it fetches the binary again, or says that it cannot where the directory cannot be written.

### Features

* **build:** without dev mode the development roots are left out ([#80](https://github.com/phaenonlang/phaenon/issues/80)) ([df45a78](https://github.com/phaenonlang/phaenon/commit/df45a7890942413e7ae5e354873a4e784f7c8238))
* **checker:** [@phaenon-ignore](https://github.com/phaenon-ignore) silences only the codes it names ([#94](https://github.com/phaenonlang/phaenon/issues/94)) ([cc6b869](https://github.com/phaenonlang/phaenon/commit/cc6b869d3b57cc6b376b6a66da83b76a5225cf53))
* **checker:** a generic closure is P1007 until it is designed ([#84](https://github.com/phaenonlang/phaenon/issues/84)) ([1104fa0](https://github.com/phaenonlang/phaenon/commit/1104fa08ab981152aac4efc666c12cef01f2d522))
* **checker:** extra checks are a kind of finding, with a switch ([#108](https://github.com/phaenonlang/phaenon/issues/108)) ([2d7ebb8](https://github.com/phaenonlang/phaenon/commit/2d7ebb824a170226c1050721bd4089fd806f73b3))
* **cli:** phaenon.json writes unused.reflected-attributes with a dash ([#92](https://github.com/phaenonlang/phaenon/issues/92)) ([4eea0cd](https://github.com/phaenonlang/phaenon/commit/4eea0cddfee8a4e50066f553878e2cae9c9c3a68))
* **composer:** each platform's binary is kept under its own name ([#81](https://github.com/phaenonlang/phaenon/issues/81)) ([117366b](https://github.com/phaenonlang/phaenon/commit/117366bb133f5361e1cd68f2b76bf9917572daa7))
* **composer:** PHN_BUILD=0 installs phn without building ([#90](https://github.com/phaenonlang/phaenon/issues/90)) ([86ee88a](https://github.com/phaenonlang/phaenon/commit/86ee88a7f68e5efd28eb3a8f691a63c3ef3e1d25))
* **trace:** phn trace -- command runs it and exits with its status ([#88](https://github.com/phaenonlang/phaenon/issues/88)) ([595b5d3](https://github.com/phaenonlang/phaenon/commit/595b5d30af934fc0c7f2359da9b294d91f9dada3))


### Bug Fixes

* **build:** --emit-on-error counts and reports a refused file once ([#115](https://github.com/phaenonlang/phaenon/issues/115)) ([95c3ea6](https://github.com/phaenonlang/phaenon/commit/95c3ea69566083e80428f8213034728ae6eb1ceb))
* **build:** a file that does not parse is one failed file under --emit-on-error ([#103](https://github.com/phaenonlang/phaenon/issues/103)) ([b9f2b06](https://github.com/phaenonlang/phaenon/commit/b9f2b06d1979aa23d2007d736bd1d0462dee5677))
* **build:** phn build checks the plain .php that phn check checks ([#96](https://github.com/phaenonlang/phaenon/issues/96)) ([6517bc3](https://github.com/phaenonlang/phaenon/commit/6517bc35f39632598afe22a92dafd69043e1db6c))
* **check:** --format=json is JSON whatever a message holds ([#93](https://github.com/phaenonlang/phaenon/issues/93)) ([d976263](https://github.com/phaenonlang/phaenon/commit/d976263725ae8c50671928f7e9da5b5b59606e90))
* **checker:** \null, \true and \false are their literals ([#99](https://github.com/phaenonlang/phaenon/issues/99)) ([e9a6103](https://github.com/phaenonlang/phaenon/commit/e9a6103107857374518f1e4bf36ac10496af5c64))
* **checker:** + and array_replace() of lists are lists ([#75](https://github.com/phaenonlang/phaenon/issues/75)) ([a4381f8](https://github.com/phaenonlang/phaenon/commit/a4381f87e3aa6956ae5a490c22ad3db5bbb68e09))
* **checker:** a bitwise and of ints is an int ([#91](https://github.com/phaenonlang/phaenon/issues/91)) ([8c2e0fa](https://github.com/phaenonlang/phaenon/commit/8c2e0fa50faab996c3d120692d00b6a5364b1188))
* **checker:** a docblock's type words in a declaration are P2001 ([#82](https://github.com/phaenonlang/phaenon/issues/82)) ([238d0da](https://github.com/phaenonlang/phaenon/commit/238d0dafc97092a24dd5b252f34d55dab4009f56))
* **checker:** a key no array has is P2313 once ([#72](https://github.com/phaenonlang/phaenon/issues/72)) ([8fbc563](https://github.com/phaenonlang/phaenon/commit/8fbc5633bdef4f117f90a5fa5189e9684a919abc))
* **checker:** a lowered default is the one the callee that runs reads ([#128](https://github.com/phaenonlang/phaenon/issues/128)) ([3a175b6](https://github.com/phaenonlang/phaenon/commit/3a175b6ac967a415207b1546d78082920d086add))
* **checker:** a match inside a try that catches its error is no P2304 ([#100](https://github.com/phaenonlang/phaenon/issues/100)) ([649e68f](https://github.com/phaenonlang/phaenon/commit/649e68f3db0eb7097c1274fed96f87f6c95fa231))
* **checker:** a partial application of a magic method is lowered ([#113](https://github.com/phaenonlang/phaenon/issues/113)) ([68fca81](https://github.com/phaenonlang/phaenon/commit/68fca81b1ccb5bdfb409fbdb404e045e81394ca0))
* **checker:** a pipe's closure for PHP 8.5 has no [@var](https://github.com/var) for its parameter ([#105](https://github.com/phaenonlang/phaenon/issues/105)) ([28447c8](https://github.com/phaenonlang/phaenon/commit/28447c83556f7582bd2e4dfca0d2539e8038f88b))
* **checker:** a step is typed as the sum with 1 ([#85](https://github.com/phaenonlang/phaenon/issues/85)) ([6cc78b9](https://github.com/phaenonlang/phaenon/commit/6cc78b97e1e064912a11af9cbc09c90445938080))
* **checker:** a syntax error is P1000 at its token, with its line ([#86](https://github.com/phaenonlang/phaenon/issues/86)) ([2ce3a3c](https://github.com/phaenonlang/phaenon/commit/2ce3a3c8ff7112ed7077c64931e3b0a65059c916))
* **checker:** a ternary kept in a variable narrows where its branches overlap ([#101](https://github.com/phaenonlang/phaenon/issues/101)) ([4a86b6f](https://github.com/phaenonlang/phaenon/commit/4a86b6f5ba20bb4b1a3fdbba263550de3db5c601))
* **checker:** a trait reads the constants of the classes using it ([#122](https://github.com/phaenonlang/phaenon/issues/122)) ([b40e358](https://github.com/phaenonlang/phaenon/commit/b40e358276a9aaa82a570f9232ad4820e0387565))
* **checker:** a value that reads itself is reported, and the check ends ([#118](https://github.com/phaenonlang/phaenon/issues/118)) ([c905769](https://github.com/phaenonlang/phaenon/commit/c905769a4d327d647c5b223224bc2e40ffaf907b))
* **checker:** a write into a bounded type parameter writes its bound ([#127](https://github.com/phaenonlang/phaenon/issues/127)) ([3ea1b24](https://github.com/phaenonlang/phaenon/commit/3ea1b2454ef66cd97c1deeb65b75c7d924da6a0c))
* **checker:** an arrow function that yields is a generator ([#120](https://github.com/phaenonlang/phaenon/issues/120)) ([ca5bf23](https://github.com/phaenonlang/phaenon/commit/ca5bf236b2adaeef37aebafa1211713ef5889e3e))
* **checker:** an enum case's value and a default are read where declared ([#104](https://github.com/phaenonlang/phaenon/issues/104)) ([260d8ec](https://github.com/phaenonlang/phaenon/commit/260d8eca963f20a78db3ab2184a559123592b801))
* **checker:** an offset passed by reference binds its variable in a try ([#123](https://github.com/phaenonlang/phaenon/issues/123)) ([dc092b2](https://github.com/phaenonlang/phaenon/commit/dc092b27d27733a78cdd686134837c907bc6f479))
* **checker:** an untyped callee's partial application is refused for 8.5 ([#117](https://github.com/phaenonlang/phaenon/issues/117)) ([427512c](https://github.com/phaenonlang/phaenon/commit/427512c68b41c34ffeaf6dd2d0812510dcad040b))
* **checker:** array_slice, array_fill, array_chunk keep PHP's keys ([#106](https://github.com/phaenonlang/phaenon/issues/106)) ([37689bc](https://github.com/phaenonlang/phaenon/commit/37689bcb1881fbde2016e7d23068f52cd7ef4e51))
* **checker:** four more integer operations past the integers are float ([#67](https://github.com/phaenonlang/phaenon/issues/67)) ([0512efb](https://github.com/phaenonlang/phaenon/commit/0512efbb070da19f3387089778a4f098066fd6a4))
* **checker:** no ignore comment silences Phaenon's syntax ([#131](https://github.com/phaenonlang/phaenon/issues/131)) ([34bcba1](https://github.com/phaenonlang/phaenon/commit/34bcba19648f46f153f754e343fe1436b0e58e57))
* **checker:** P4015 says what is true of the codes a comment names ([#107](https://github.com/phaenonlang/phaenon/issues/107)) ([1b089f5](https://github.com/phaenonlang/phaenon/commit/1b089f5d7b4ddc5385db6e7fd989ac96693c9483))
* **checker:** PHP's own attributes are held to their targets ([#121](https://github.com/phaenonlang/phaenon/issues/121)) ([6d0ee25](https://github.com/phaenonlang/phaenon/commit/6d0ee25140f16659ee693ae30fbf6351a27b1a8c))
* **checker:** static:: constants are their declared type ([#83](https://github.com/phaenonlang/phaenon/issues/83)) ([a0453fe](https://github.com/phaenonlang/phaenon/commit/a0453fe62ad31d1e3c68a0f84f7a49a1a89f143c))
* **checker:** strval(), intval() and floatval() take what PHP converts ([#114](https://github.com/phaenonlang/phaenon/issues/114)) ([47e003c](https://github.com/phaenonlang/phaenon/commit/47e003cd39d3039e5dda7fa99955280ce83541a1))
* **checker:** unset() and + of arrays bound the shapes as writes do ([#74](https://github.com/phaenonlang/phaenon/issues/74)) ([5455d1a](https://github.com/phaenonlang/phaenon/commit/5455d1a26112b0150ce4731750ce06ac90595e57))
* **checker:** what PHP 8.5 deprecates in code is reported ([#95](https://github.com/phaenonlang/phaenon/issues/95)) ([9a8d6f9](https://github.com/phaenonlang/phaenon/commit/9a8d6f94ff8224d6fde7b687aa1e7051c72d5a08))
* **cli:** a project file without roots says that paths are needed ([#89](https://github.com/phaenonlang/phaenon/issues/89)) ([55a5d38](https://github.com/phaenonlang/phaenon/commit/55a5d38bf1068cf56144366c3aaec3448ccae333))
* **emit:** a fallback closure's arguments have the callee's types ([#111](https://github.com/phaenonlang/phaenon/issues/111)) ([f3bbb0e](https://github.com/phaenonlang/phaenon/commit/f3bbb0e7fcd14db9e7487f993f7674ccf579475e))
* **emit:** a lowered closure's parameter keeps #[\SensitiveParameter] ([#112](https://github.com/phaenonlang/phaenon/issues/112)) ([04783a3](https://github.com/phaenonlang/phaenon/commit/04783a3bd55fafd31d53f4aec0332def798129dc))
* **emit:** a panic of the lowering phn check asks answers every stage ([#124](https://github.com/phaenonlang/phaenon/issues/124)) ([714f91d](https://github.com/phaenonlang/phaenon/commit/714f91d524d33aa1f184ddb47ea99b69e61a0f9a))
* **emit:** a static variable's initialiser is compiled as code ([#119](https://github.com/phaenonlang/phaenon/issues/119)) ([1caac1d](https://github.com/phaenonlang/phaenon/commit/1caac1d019bfd226a1956cbc52417a57f9fc824d))
* **emit:** tags join a docblock whose first or last line holds one ([#98](https://github.com/phaenonlang/phaenon/issues/98)) ([54e3cfc](https://github.com/phaenonlang/phaenon/commit/54e3cfcc0d0542e699e11cd4a3b0bcec9a32d989))
* **fmt:** a docblock after code on its line is left as written ([#65](https://github.com/phaenonlang/phaenon/issues/65)) ([9b83d4e](https://github.com/phaenonlang/phaenon/commit/9b83d4e5b37618826aa85fa13ba18021a36c7161))
* **fmt:** a lone \r ends a line where tabs are replaced ([#77](https://github.com/phaenonlang/phaenon/issues/77)) ([6d2dc44](https://github.com/phaenonlang/phaenon/commit/6d2dc44ddf1ade2942ac6d921c60b4acc08e9c4e))
* **migrate:** a docblock type named by a PHP keyword stays a tag ([#76](https://github.com/phaenonlang/phaenon/issues/76)) ([2eaaf4f](https://github.com/phaenonlang/phaenon/commit/2eaaf4ff6cfaff94a2ac854df640e72da58fb092))
* **migrate:** a map key the checker refuses is not lifted ([#87](https://github.com/phaenonlang/phaenon/issues/87)) ([b7a75d1](https://github.com/phaenonlang/phaenon/commit/b7a75d1c489d092bce293271df3e0f8f00f12365))
* **parser:** a heredoc PHP refuses for its indentation is a syntax error ([#78](https://github.com/phaenonlang/phaenon/issues/78)) ([d4b538f](https://github.com/phaenonlang/phaenon/commit/d4b538f3aa2160956192b5a2dec51bcaf9a26700))
* **program:** a file reached through a symbolic link is one file ([#79](https://github.com/phaenonlang/phaenon/issues/79)) ([6542ed6](https://github.com/phaenonlang/phaenon/commit/6542ed6caf36037b64b2a4f9e8b8e31fcaff7c58))
* **trace:** the two streams of phn trace -- share the tracer's maps safely ([#102](https://github.com/phaenonlang/phaenon/issues/102)) ([13463a1](https://github.com/phaenonlang/phaenon/commit/13463a177debd07675f94567d05670364f93951b))
* **types:** one enum case never overlaps another ([#126](https://github.com/phaenonlang/phaenon/issues/126)) ([d3a17c0](https://github.com/phaenonlang/phaenon/commit/d3a17c03cee6c34fb6a23d3fee187c6c91d1ed78))


### Documentation

* Phaenon is alpha ([#71](https://github.com/phaenonlang/phaenon/issues/71)) ([7939109](https://github.com/phaenonlang/phaenon/commit/7939109c2e5f44a4d693b5122e4baf043750f187))

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
