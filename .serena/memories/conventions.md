---
type: reference
tags: [phaenon, meta]
updated: 2026-10-28
---

# Vault conventions

How notes here are written, so Serena, Claude and Obsidian read the same files the same way.

## Rules

1. **Supplement, never duplicate.** `AGENTS.md` and `CLAUDE.md` are loaded into every session; the
   README and the `design` docs are one read away. Link to them by path (`../design/generics.md`
   §11) instead of restating them. A note holds what none of those hold: history, current state,
   gotchas, open items, test-environment detail.
2. **One idea per note**, linked with `[[name]]`. A link to a note that does not exist yet is a
   to-do, not an error.
3. **Flat folder, kebab-case names.** The file name is the Serena memory name and the wiki-link
   text, so `read_memory("gotcha-phpstan-one-tag-per-line")` and `[[gotcha-phpstan-one-tag-per-line]]`
   name the same file. Prefix gotchas with `gotcha-`. No subfolders: a folder would put a prefix in
   the Serena name (`gotchas/x`) that the wiki-link does not carry.
4. **Every new note is linked from [[Home]].**
5. **Absolute dates** (`2026-10-28`), taken from the session context, never guessed or relative.
6. **Durable, shared knowledge only.** The vault is committed and will be public with the repo.
   Private working state and the personal preferences of whoever runs the session belong in
   Claude's harness memory.

## Frontmatter

```yaml
---
type: moc | reference | status | backlog | gotcha
tags: [phaenon, ...]      # always `phaenon`, plus topical tags
updated: YYYY-MM-DD       # last meaningful edit
---
```

| `type` | Used for |
|---|---|
| `moc` | [[Home]] |
| `reference` | How things work: [[testing]], [[working-method]], [[dogfood]], this note |
| `status` | [[milestones]] |
| `backlog` | [[open-items]] |
| `gotcha` | One trap each: what happens, how to recognise it, what to do |

## Maintenance

- A milestone closes → update [[milestones]] (and the roadmap entry in `../design/roadmap.md`).
- A deferred item is done → strike it in [[open-items]] with the date and the commit.
- Something bit you and will bite the next session → a `gotcha-` note, linked from [[Home]].
- A note turns out wrong → fix it in place and bump `updated`; do not append contradictions.

## Memory layers

| Store | Loaded | Committed | Use for |
|---|---|---|---|
| `AGENTS.md` + `CLAUDE.md` | always | yes | The brief: org map, commands, rules |
| This vault | on demand | yes | History, state, test environment, gotchas, open items |
| `../design` docs | on demand | yes (`phaenonlang/design`) | Language design and every decision |
| Claude harness memory | Claude, this machine | no | Private working state, the developer's own preferences |

← [[Home]]
