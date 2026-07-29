# Migrating from v1 (Go) to v2 (TypeScript)

codebase-cli v2 is a complete rewrite of the v1 Go binary on top of the
[pi-mono](https://github.com/earendil-works/pi-mono) TypeScript runtime.
The user-facing surface is intentionally compatible: same data
directory, same OAuth tokens, same env vars, same project conventions.
The internals are different — see [`.settings/architecture.md`](../.settings/architecture.md)
if you want the technical detail.

This guide is for v1 users upgrading.

## TL;DR

```sh
curl -fsSL https://codebase.design/install.sh | sh
```

That's the whole migration. The installer detects your v1 binary, asks
before removing it, and installs v2. Your existing sign-in, sessions,
and project memory all keep working.

## What's preserved

| Path | What | v1 → v2 |
|---|---|---|
| `~/.codebase/credentials.json` | OAuth access + refresh tokens | **No re-auth needed.** Same JSON shape, mode 0600. |
| `~/.codebase/sessions/` | Saved conversations (`/resume`) | Compatible. v2 can read v1 session files; new sessions write the v2 schema. |
| `~/.codebase/projects/<sha256>/memory/` | Cross-session memory (4-type taxonomy) | Compatible. Memory files are markdown with frontmatter. |
| `~/.codebase/config.json` | User config (theme, MCP servers, hooks) | Compatible. New fields are added; existing ones are read as before. |
| `CLAUDE.md`, `AGENTS.md`, `CODEX.md`, `.cursorrules` | Project instructions | Identical pickup logic. |
| `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_MODEL` | Provider env vars | Identical. |
| `GLUE_*`, `TAVILY_API_KEY`, `BRAVE_API_KEY`, `SEARXNG_URL` | Sidecar + search keys | Still supported. Signed-in Codebase sessions can use hosted search without a separate search key. |
| `CODEBASE_NOBOOT`, `CODEBASE_NOSOUND` | Behavior toggles | Identical. |

If you sign in via codebase.foundation in v1, you stay signed in
through the upgrade. The first v2 launch will refresh the token if
needed and surface its source via `/status`.

## What's changed

### Command surface

The auth flow moved from a flat command to a subcommand to make room
for `auth status`, `auth refresh`, and `auth <key>` (manual API key
ingest for headless / SSH).

| v1 | v2 |
|---|---|
| `codebase login` | `codebase auth login` |
| `codebase login --key cbk_xxx` | `codebase auth cbk_xxx` |
| `codebase logout` | `codebase auth logout` |
| — | `codebase auth status` |
| — | `codebase auth refresh` |

Flags use the long-form `--flag` style instead of single-dash. The
short-dash forms (`-dir`, `-model`, `-resume`, `-version`) are not
supported in v2 — use `--dir`, `--model`, `--resume`, `--version`.

### Headless mode

```sh
codebase --headless "fix the build"
```

One-shot, no TUI, exits when the agent finishes. Stdout is plain text
suitable for piping. v1's planning hooks (`enter_plan_mode`) are
respected; the agent will refuse write tools until you allow them via
the `--auto-approve` policy.

### Slash commands

All v1 slash commands are present. New in v2:

- `/cost` — detailed token + cost breakdown including cache hit rate.
- `/copy` — copy last assistant message, last code block, or message N
  to the system clipboard via OSC 52 (works over SSH and tmux).

### Skills, templates, prompts

Phase 7+ ships a three-source asset registry: bundled (in the package),
local (`~/.codebase/skills/*.md`), and **platform** (fetched from
codebase.foundation if you're signed in). Local files behave exactly
like v1; the platform path is new and only kicks in when authenticated.

### Tool surface

29 of 30 tools from v1 are present. The remaining one (`config`) is
tracked in the Phase 7 polish list and lands before the public 2.0.0
release. All tool names, schemas, and effect categories are unchanged.

### MCP and IDE bridge

Both are ported. Configuration format is unchanged (`mcp_servers` in
`~/.codebase/config.json`). The IDE bridge auto-detects VS Code,
Cursor, and JetBrains via lockfiles in the project root.

## What's gone

- **Boot animation and audio** — `CODEBASE_NOBOOT` and `CODEBASE_NOSOUND`
  still parse for compatibility but currently do nothing. The boot
  experience is on the v2.1 wishlist; for now startup is silent and
  immediate.
- **Single-binary distribution** — v2 currently requires Node.js ≥ 20.
  A Bun-compiled standalone binary is on the post-2.0 roadmap and will
  be served from GitHub Releases when ready, distributable via the same
  `install.sh` one-liner.

## Rollback

If you need to roll back to v1:

```sh
# 1. Uninstall v2
npm uninstall -g codebase-cli

# 2. Reinstall v1 from the tagged Go release
curl -fsSL https://github.com/codebase-foundation/codebase-cli/releases/download/v1.0.0/install.sh | sh
```

Your `~/.codebase/` is untouched by either install or uninstall.
Sessions written by v2 may include fields v1 ignores; v1 will treat
them as forward-compatible no-ops.

## Reporting migration issues

If something breaks after upgrade, please open an issue:
<https://github.com/codebase-foundation/codebase-cli/issues> with:

1. Output of `codebase --version`
2. Output of `codebase auth status`
3. The first error you see (and the command that triggered it)
4. Whether `~/.codebase/credentials.json` exists and has version `1` in
   the JSON header
