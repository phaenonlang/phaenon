---
type: moc
tags: [phaenon, home]
updated: 2026-10-28
---

# Phaenon — project memory

The durable memory of the Phaenon project: what was built and when, what is open, what bites.
It **supplements** the always-loaded brief — `AGENTS.md` (and `CLAUDE.md`) at the repository root —
and never repeats it. Language design and its decisions live in the `design` repo; notes here link
to them by path.

- **Agents:** these notes are Serena memories. Serena lists their names when the project activates;
  read one with `read_memory` (name = file name without `.md`) or open
  `.serena/memories/<name>.md`. Start here, follow links, read only what fits the task.
- **Humans:** open `.serena/memories/` as an Obsidian vault. It is a hidden folder: press ⌘⇧. in
  the macOS file dialog to see it.

## Org map

The full map — role, remote, build and test command for each repo — is `AGENTS.md` § *Org map*,
the single source. What the vault adds per repo:

| Repo | Path (from the compiler repo) | Notes here |
|---|---|---|
| `phaenon` — the compiler | `.` | [[milestones]] · [[testing]] · [[open-items]] |
| `design` — specs and decisions | `../design` | [[working-method]] (when to update which doc) |
| `runtime` — `phaenon/runtime` | `../runtime` | [[gotcha-runtime-stub-drift]] |
| `jetbrains` — PhpStorm plugin | `../jetbrains` | [[open-items]] § PhpStorm plugin |
| `php-src` — read-only reference | `../php-src` | [[testing]] § Conformance |
| Tequila dogfood targets | `~/labs/tequila/…` | [[dogfood]] |

## Notes

| Note | Holds |
|---|---|
| [[milestones]] | M0–M3: what each delivered, the numbers, the commits; what comes next |
| [[working-method]] | The per-feature pipeline as a checklist, and the close-out of a milestone |
| [[testing]] | Every `PHAENON_*` variable, the slow suites, which `php` and which PHPStan |
| [[dogfood]] | The Tequila targets: state, branch, how to rebuild and verify |
| [[open-items]] | Deferred work by target milestone — strike an item when it is done |
| [[conventions]] | How notes in this vault are written |

## Gotchas

- [[gotcha-goland-rewrites-go-mod]] — imports stop resolving after GoLand opens the repo
- [[gotcha-phpstan-one-tag-per-line]] — why generic declarations add lines to the output
- [[gotcha-serena-goland-vs-claude-code]] — Serena in GoLand is not Serena in Claude Code
- [[gotcha-runtime-stub-drift]] — the embedded `\Phaenon\Type` declaration is hand-kept
