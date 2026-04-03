# Codebase CLI

AI coding agent in your terminal. Reads your project, writes code, runs commands, searches the web. Works with any LLM provider or use ours.

## Install

**macOS / Linux:**

```sh
curl -sSL https://raw.githubusercontent.com/codebase-foundation/codebase-cli/main/install.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/codebase-foundation/codebase-cli/main/install.ps1 | iex
```

**With Go:**

```sh
go install github.com/codebase-foundation/cli@latest
```

**From source:**

```sh
git clone https://github.com/codebase-foundation/codebase-cli.git
cd codebase-cli
go build -o codebase .
sudo mv codebase /usr/local/bin/   # or: mv codebase ~/.local/bin/
```

After install, run `codebase` from any project directory.

## Quick Start

**Option 1: Login with Codebase (easiest)**

```sh
codebase login
```

Opens your browser, logs you into codebase.foundation, and you're ready. Uses our inference providers (MiniMax, Claude, etc.) with your account credits. No API keys needed.

**Option 2: Bring your own API key**

```sh
export ANTHROPIC_API_KEY=sk-ant-...
codebase
```

Works with any provider:

```sh
# OpenAI
export OPENAI_API_KEY=sk-...

# Groq
export OPENAI_BASE_URL=https://api.groq.com/openai/v1
export OPENAI_API_KEY=gsk-...
export OPENAI_MODEL=llama-3.3-70b-versatile

# Ollama (local, free)
export OPENAI_BASE_URL=http://localhost:11434/v1
export OPENAI_API_KEY=ollama
export OPENAI_MODEL=qwen2.5-coder:32b
```

**Option 3: First-run setup wizard**

Just run `codebase` with no config. The setup wizard walks you through picking a provider, entering your key, and selecting a model. "Login with Codebase" is the first option.

## What It Does

You describe what you want. The agent reads your code, makes changes, runs commands, and explains what it did.

```
> add a /health endpoint that returns uptime and version

  read_file server.go                    ✓
  read_file go.mod                       ✓
  edit_file server.go                    ✓
  shell go build ./...                   ✓

Added GET /health endpoint at server.go:47 returning JSON with
uptime, version, and go runtime. Build passes.
```

## Tools (30)

| Category | Tools |
|----------|-------|
| **File read** | `read_file`, `list_files`, `glob`, `grep`, `search_files` |
| **File write** | `write_file`, `edit_file`, `multi_edit`, `notebook_edit` |
| **Shell** | `shell` (input-aware parallelism — read-only commands run concurrently) |
| **Git** | `git_status`, `git_diff`, `git_log`, `git_commit`, `git_branch`, `enter_worktree`, `exit_worktree` |
| **Web** | `web_search`, `web_fetch` |
| **Agent** | `dispatch_agent` (explore, plan, or general-purpose subagents with optional worktree isolation) |
| **Tasks** | `create_task`, `update_task`, `list_tasks`, `get_task` |
| **Planning** | `enter_plan_mode`, `exit_plan_mode` |
| **Memory** | `save_memory`, `read_memory` (persist context across sessions) |
| **Other** | `config`, `ask_user` |

Plus any tools from connected MCP servers.

## Commands (22)

| Command | What it does |
|---------|-------------|
| `/help` | List all commands |
| `/status` | Model, tokens, turns, cost |
| `/cost` | Token usage and estimated cost |
| `/context` | Visual context window usage bar |
| `/model [name]` | Show or switch model |
| `/commit` | Generate commit from current diff |
| `/review` | Code review of uncommitted changes |
| `/plan` | Enter planning mode |
| `/diff` | Open diff in VS Code/Cursor |
| `/compact` | Manually compact conversation |
| `/clear` | Clear display |
| `/memory` | View saved project memories |
| `/undo [file]` | Revert file from session history or git |
| `/export [file]` | Export conversation to markdown |
| `/tasks` | Show task checklist |
| `/theme` | Switch color theme |
| `/trust` | Set permission level |
| `/diagnostics` | Toggle language checkers |
| `/copy` | Copy last response to clipboard |
| `/session` | Full session info |
| `/setup` | Re-run setup wizard |
| `/quit` | Exit (or ctrl+c twice) |

## Features

- **30 tools** with schema validation and input-dependent parallel execution
- **Streaming tool execution** — tools start running before the model finishes responding
- **3 agent types** — explore (read-only), plan (architecture), general-purpose (full access)
- **Worktree isolation** — subagents can work in isolated git branches
- **Cross-session memory** — remembers your preferences and project context
- **Error recovery** — auto-retries on context overflow, output limits, rate limits
- **Structured compaction** — 9-section summaries when context gets long
- **Hooks system** — automate lint/test/format after edits
- **7 language checkers** — Go, TypeScript, ESLint, Python (pyright/mypy), Rust
- **MCP support** — connect external tool servers for extensibility
- **IDE detection** — discovers VS Code/Cursor/JetBrains via lockfiles
- **File history** — undo any edit within the session, even without git
- **Glue models** — route cheap tasks to a fast model, save money
- **Permission explainer** — risk-rated permission prompts (LOW/MEDIUM/HIGH)
- **Multi-provider** — OpenAI, Anthropic, MiniMax, Groq, Ollama, any compatible endpoint
- **Single binary** — no runtime dependencies, works on air-gapped systems

## Authentication

```sh
codebase login          # browser OAuth — log in with Google, GitHub, or wallet
codebase login --key cbk_xxx   # API key from dashboard (for SSH/headless)
codebase logout         # revoke session
```

Credentials stored at `~/.codebase/credentials.json` (mode 0600).

When logged in, the CLI routes through `codebase.foundation` — you get access to all providers (MiniMax, Claude, Qwen, etc.) using your account credits. No API keys to manage.

## MCP (External Tool Servers)

Connect to any MCP-compatible server. Add to `~/.codebase/config.json`:

```json
{
  "mcp_servers": {
    "github": {
      "command": "mcp-server-github",
      "args": ["--token", "$GITHUB_TOKEN"],
      "transport": "stdio"
    }
  }
}
```

MCP tools appear alongside built-in tools automatically.

## Hooks

Automate actions on events. Add to `~/.codebase/hooks.json`:

```json
[
  {
    "event": "PostEdit",
    "matcher": "write_file|edit_file",
    "command": "go vet ./...",
    "timeout": 15
  }
]
```

Events: `PreToolUse`, `PostToolUse`, `PostEdit`, `UserPromptSubmit`, `SessionStart`, `Stop`.

## Project Instructions

The CLI reads these files from your project root for context:

- `CLAUDE.md` — project instructions (Claude Code convention)
- `AGENTS.md` — agent instructions (OpenAI Codex convention)
- `CODEX.md` — project instructions (Codex convention)
- `.cursorrules` — project instructions (Cursor convention)

## Flags

```
codebase                     # run in current directory
codebase -dir /path/to/proj  # run in specific directory
codebase -model claude-sonnet-4-20250514  # override model
codebase -resume             # resume previous session
codebase -version            # print version
codebase login               # authenticate with codebase.foundation
codebase logout              # revoke authentication
```

## Environment Variables

```sh
# LLM Provider (pick one)
OPENAI_API_KEY=...           # OpenAI or compatible
ANTHROPIC_API_KEY=...        # Anthropic (auto-detected)
OPENAI_BASE_URL=...          # Custom endpoint
OPENAI_MODEL=...             # Override model

# Glue (optional — cheap model for routing/narration)
GLUE_API_KEY=...
GLUE_BASE_URL=...
GLUE_FAST_MODEL=...
GLUE_SMART_MODEL=...

# Web Search (optional — DuckDuckGo works without keys)
TAVILY_API_KEY=...
BRAVE_API_KEY=...

# Behavior
CODEBASE_NOBOOT=1            # skip boot animation
CODEBASE_NOSOUND=1           # skip boot audio
```

## License

MIT
