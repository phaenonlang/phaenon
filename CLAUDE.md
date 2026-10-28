@AGENTS.md

# Claude Code specifics

## Serena

- `.mcp.json` starts Serena for this repo: built-in `claude-code` context, project detected from the
  working directory and active from the start (no `activate_project`). Approve it when Claude Code
  asks.
- The backend is **JetBrains**: GoLand must have this repo open (the Serena plugin is installed in
  GoLand 2026.x). If symbol tools fail to connect, ask Petr to open the repo in GoLand, then retry.
  `SERENA_LANGUAGE_BACKEND=LSP` switches to gopls, which must be installed first
  (`go install golang.org/x/tools/gopls@latest`).
- For Go, navigate by symbol — `get_symbols_overview`, `find_symbol`, `find_referencing_symbols`
  (`jet_brains_`-prefixed on the JetBrains backend) — rather than reading whole files: checker and
  emitter files run to 1,000–1,800 lines.
- Without Serena everything still works with the built-in tools; the memories are plain files.

## Memory

| Layer | Loaded | Use for |
|---|---|---|
| `AGENTS.md` + this file | always | The brief; edit it when a rule, a command or the map changes |
| Vault `.serena/memories/` | on demand: Serena lists the names; `read_memory`, or read the file | History, state, test environment, gotchas, open items |
| `../design` | on demand | Language design and decisions: the vault links to them, never restates them |
| Harness auto-memory | Claude only, this machine | Petr's preferences and private working state, not project knowledge |

- Start at the `Home` note; read a note only when its name fits the task.
- Durable project knowledge goes into the vault (`write_memory`, or a file in
  `.serena/memories/`), written to the `conventions` note: frontmatter, `[[links]]`, linked from
  `Home`. Close milestones and deferred items there as well as in the design docs.
- Never copy `AGENTS.md`, this file, the README or design-doc text into a note; link by path.
- Subagents do not inherit this context: point them at `AGENTS.md` and the notes they need.
