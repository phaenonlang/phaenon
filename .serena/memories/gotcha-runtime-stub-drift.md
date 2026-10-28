---
type: gotcha
tags: [phaenon, runtime, stdlib]
updated: 2026-10-28
---

# The embedded `\Phaenon\Type` declaration is kept by hand

**What it is.** The checker learns the runtime API from `stdlib/runtime/Type.php`, a hand-written
declaration (with PHPStan generics, `@template-covariant T`) embedded by `stdlib/stdlib.go`. The
implementation is `../runtime/src/Type.php`. Nothing generates one from the other or checks that
they agree.

**Why it matters.** Phaenon code that calls `\Phaenon\Type` directly is checked against the stub; a
method missing there is an error in `phn check` although it exists at runtime. The emitter writes
calls to the real package (`RuntimeType` in `internal/emit/generics.go`), so renaming a runtime
method breaks compiled code.

**State on 2026-10-28.** The stub lacks `classString()` and `resource()`, which the runtime package
declares ([[open-items]]).

**Rule.** Change the runtime's public API and the stub in the same piece of work.

← [[Home]]
