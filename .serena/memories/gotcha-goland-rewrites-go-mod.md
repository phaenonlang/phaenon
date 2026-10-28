---
type: gotcha
tags: [phaenon, goland, go]
updated: 2026-10-28
---

# GoLand rewrites `go.mod`'s module line

**What happens.** When GoLand opened the compiler repo (2026-10-28) it rewrote the first line of
`go.mod` from `module github.com/phaenonlang/phaenon` to `module phaenon`. Every import of
`github.com/phaenonlang/phaenon/internal/...` then stops resolving.

**How to recognise it.** Builds or tests suddenly fail on the repo's own imports (package not
found / not in std) although no Go file changed. `git diff go.mod` shows the module line.

**What to do.** Put the line back by hand — `module github.com/phaenonlang/phaenon` — and never commit
the rewritten one. Do not "repair" it with `go mod init` / `go mod tidy`: that fixes the symptom in
the wrong direction.

**Same family.** GoLand also left a tracked `go.mod` (`module design`) and `.idea/` files in the
`design` repo, and a stray `go.mod` in `../php-src` had to be deleted early on (roadmap §5 step 1).
See [[open-items]].

← [[Home]]
