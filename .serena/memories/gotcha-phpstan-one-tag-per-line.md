---
type: gotcha
tags: [phaenon, phpstan, emitter, generics]
updated: 2026-11-02
---

# PHPStan reads one docblock tag per line

**What happens.** In `/** @template T @param T $x */` PHPStan keeps only the first tag (verified
against PHPStan 2.2 while building M3). Tags packed onto one line are silently lost.

**Consequence for the emitter.** Every docblock the emitter writes or extends is a multi-line
block, one tag per line (`../design/line-map.md` §2). Until D19 (2026-11-02) that made a generic
declaration the one exception to the line rule; since then the emitter lays its text out freely and
the line map holds the added lines to the declaration's source line. The one-line form survives in
one place: a closure in an expression with a single generated tag (`$f = /** @return T */
function …`), where one tag on its line is all PHPStan needs.

**Consequence for fixtures.** Hand-written docblocks in golden files and test fixtures: one tag per
line, or PHPStan parity runs see less than the fixture says.

← [[Home]]
