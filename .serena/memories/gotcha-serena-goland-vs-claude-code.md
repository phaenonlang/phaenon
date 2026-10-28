---
type: gotcha
tags: [phaenon, serena, tooling]
updated: 2026-10-28
---

# Serena in GoLand is not Serena in Claude Code

**What happened.** Serena set up in GoLand (the Serena JetBrains plugin, GoLand's MCP server) was
not available as tools in Claude Code sessions. Claude Code only starts the MCP servers in its own
configuration: user scope (none on this machine) or the project's `.mcp.json`.

**The fix (2026-10-28).** The repo's `.mcp.json` starts a Serena server for Claude Code:
`serena start-mcp-server --context claude-code --project-from-cwd --language-backend JetBrains`
(`SERENA_BIN` and `SERENA_LANGUAGE_BACKEND` override the binary and the backend). Claude Code asks
once to approve it.

**What the backends need.**

| Backend | Needs | On this machine (2026-10-28) |
|---|---|---|
| `JetBrains` (default) | GoLand running with this repo open and the Serena plugin installed; Serena talks to the plugin | Plugin installed in GoLand 2026.1 and 2026.2 |
| `LSP` | `gopls` on `PATH` (`go install golang.org/x/tools/gopls@latest`) | gopls **not** installed |

Symbol tools carry a `jet_brains_` prefix on the JetBrains backend (`jet_brains_find_symbol`) and
none on LSP (`find_symbol`). If they fail to connect, open the repo in GoLand and retry.

**Version note.** The installed `serena-agent` is 1.1.2 (uv tool). Its `project.yml` loader rewrites
the file when a field is missing, so `.serena/project.yml` was written complete from that version's
template.

← [[Home]]
