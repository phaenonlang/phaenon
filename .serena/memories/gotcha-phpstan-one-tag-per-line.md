---
type: gotcha
tags: [phaenon, phpstan, emitter, generics]
updated: 2026-10-28
---

# PHPStan reads one docblock tag per line

**What happens.** In `/** @template T @param T $x */` PHPStan keeps only the first tag (verified
against PHPStan 2.2 while building M3). Tags packed onto one line are silently lost.

**Consequence for the emitter.** A generic declaration's erased types come back as a multi-line
docblock, so that declaration adds lines to the generated `.php`. It is the one exception to M1's
line preservation: every other edit keeps its line count, and the emitter's strict line-count check
still applies to them. Lines below a generic declaration in the `.php` are therefore offset from the
`.phn`. Decision and reasoning: `../design/generics.md` §11 *Line preservation*. A line map for
stack traces is in [[open-items]].

**Consequence for fixtures.** Hand-written docblocks in golden files and test fixtures: one tag per
line, or PHPStan parity runs see less than the fixture says.

← [[Home]]
