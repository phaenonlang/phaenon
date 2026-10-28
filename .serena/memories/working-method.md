---
type: reference
tags: [phaenon, process]
updated: 2026-10-28
---

# Working method — the checklist

`AGENTS.md` states the rule (design doc → golden tests → implementation → PHPStan on the output →
Tequila dogfood) and the "ask before a milestone" habit; `../design/roadmap.md` §4 has the
reasoning. This note is the concrete checklist, with where each artifact lives.

## Per feature

1. **Design.** A section in the feature's doc in `../design` — a new doc for a new area
   (`language-overview.md` §5 names the pending ones: `collections.md`, `strings.md`,
   `stdlib.md`, `concurrency.md`, `tooling.md`). New diagnostic codes are listed there too, as
   `generics.md` §11 lists `P3001`–`P3009`; the code table itself is `internal/checker/codes.go`.
2. **Golden tests that fail.**
   - Checker: a `.phn` in `internal/checker/testdata/` with `// want Pxxxx` on each offending line.
   - Emitter: `internal/emit/testdata/X.phn` plus the hand-reviewed expected `X.php`.
   - Anything that is plain PHP behaviour: the conformance suites, with `php` as the oracle
     ([[testing]]).
3. **Implementation** in `internal/…`, until the goldens pass and `go test ./...` is green.
4. **PHPStan on the output.** Run the parity and emitter tests with `PHAENON_PHPSTAN` set
   ([[testing]]). The checker may be stricter than PHPStan, never more lenient; a PHPStan rule
   Phaenon deliberately lacks goes into the allow-list in `internal/checker/phpstan_test.go` with a
   comment naming the reason or the milestone.
5. **Dogfood.** Rebuild the Tequila target and run *its* suite on the compiled output
   ([[dogfood]]). Nothing is done until that passes (roadmap §3).

## Closing a milestone

- `../design/<doc>.md`: add or extend **"Decisions made while building Mx"** — a table
  *Topic | Decision | Why*. The draft text above it stays as written; the section wins where they
  differ (the pattern of `checker.md` §9 and `generics.md` §11).
- `../design/roadmap.md` §3: the milestone entry gets ✅ items with numbers, *Found on the way*,
  *Open*, *Target*; the status line gets the date.
- `README.md` here: the status paragraph.
- This vault: [[milestones]] and [[open-items]].
- One commit per repo, prose messages (`AGENTS.md` § Git and commits). Then report to Petr and ask
  what comes next.

## Division of labour (roadmap §4)

Mechanical, parallelisable work — corpus classification, stub conversion, codemod corpora — goes
to subagents. Design and the checker core stay in the main session. Petr does design review and
decisions, the Tequila-side migrations, and the PHP semantics that need someone who has been
bitten.

← [[Home]]
