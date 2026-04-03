# CLAUDE.md — Codebase CLI

## What This Is

An AI coding agent CLI written in Go. Goal: build a **better Claude Code** — simpler
architecture, any LLM provider, single binary, zero runtime dependencies.

This is NOT a Claude Code clone. We study CC's source as a benchmark for feature coverage
and learn from their architectural mistakes. Every feature we add should be a better
implementation, not a copy-paste of their approach.

## Quick Reference

```
Language:    Go 1.24
TUI:         BubbleTea (Charm)
Styling:     Lipgloss
Highlighting: Chroma
Binary name: codebase
Config dir:  ~/.codebase/
Sessions:    ~/.codebase/sessions/
```

## Build & Run

```sh
go build -o codebase .
./codebase                    # run in current dir
./codebase -dir /some/path    # run in specific dir
./codebase -model claude-sonnet-4-20250514  # override model
./codebase -resume            # resume previous session
CODEBASE_NOBOOT=1 ./codebase  # skip boot animation
```

## Test

```sh
go test ./...                 # all tests
go test -run TestToolReadFile # specific test
go test -v -count=1 ./...    # verbose, no cache
```

## Architecture Overview

See `docs/ARCHITECTURE.md` for the full blueprint and migration plan.

### Current Structure (pre-refactor)

```
main.go           Entry point, config loading, flag parsing
app.go            BubbleTea root model — routes between screens (boot/setup/chat)
chat.go           Main chat TUI (1,603 LOC — god object, being refactored)
agent.go          Agent loop — goroutine that drives LLM ↔ tool cycle
tools.go          Tool definitions + execution dispatcher (switch statement)
git_tools.go      Git-specific tools (status, diff, log, commit, branch)
llm.go            OpenAI-compatible LLM client + SSE streaming
llm_anthropic.go  Anthropic Messages API adapter + streaming
permission.go     Permission system (ask/trust-tool/trust-all)
compact.go        Conversation compaction via LLM summarization
session.go        Session persistence to ~/.codebase/sessions/
commands.go       Slash commands (/help, /clear, /compact, /status, etc.)
glue.go           Sidecar LLM for intent routing, narration, titles
plan.go           Planning mode (Q&A before building)
render.go         Viewport rendering helpers, tool block formatters
search.go         Web search (Tavily, Brave, SearXNG, DuckDuckGo)
boot.go           Demoscene boot animation (plasma, 3D cube, pixel font)
audio.go          Chiptune synthesizer (4-channel MOD tracker, pure Go)
setup.go          First-run setup wizard
diagnostics.go    Post-edit language checkers (Go, TypeScript, Python)
notify.go         Notification manager (status bar messages)
highlight.go      Syntax highlighting via Chroma
theme.go          Color themes (dark, light, retro)
dotenv.go         .env file loader
```

### Data Flow

```
User Input → chatModel.Update()
  → slash command? → commands.go handler
  → agent message? → agent.go goroutine
    → LLM streaming via llm.go / llm_anthropic.go
    → tool calls dispatched via tools.go
    → permission checks via permission.go
    → results sent back as AgentEvent on channel
  → chatModel renders events via render.go
```

### Key Patterns

- **Channel-based concurrency**: Agent runs in goroutine, sends events on `eventCh`,
  TUI receives them in `Update()`. No shared mutable state.
- **Segment-based rendering**: Conversation is `[]segment` (text/user/tool/divider/error).
  Avoids fragile ANSI string manipulation.
- **Debounced viewport**: Streaming rebuilds throttled to 50ms to prevent TUI thrashing.
- **Read-only parallel execution**: Tools declaring `EffectReadsFS` run concurrently.
- **Atomic writes**: Multi-edit uses write→rename for crash safety.

## Environment Variables

### Required (one of)
```
OPENAI_API_KEY          OpenAI or compatible provider API key
ANTHROPIC_API_KEY       Anthropic API key (auto-detects protocol)
```

### Optional — LLM
```
OPENAI_BASE_URL         Custom endpoint (Groq, Ollama, OpenRouter, etc.)
OPENAI_MODEL            Override default model
```

### Optional — Glue sidecar
```
GLUE_API_KEY            Separate key for cheap/fast model
GLUE_BASE_URL           Separate endpoint for glue
GLUE_FAST_MODEL         Fast model for intent/titles (default: same as main)
GLUE_SMART_MODEL        Smart model for planning/narration
```

### Optional — Web Search
```
TAVILY_API_KEY          Tavily search (recommended)
BRAVE_API_KEY           Brave Search
SEARXNG_URL             Self-hosted SearXNG instance
SEARCH_API_KEY          Alias for TAVILY_API_KEY
```

### Optional — Behavior
```
CODEBASE_NOBOOT         Skip boot animation
CODEBASE_NOSOUND        Skip boot audio
```

## Coding Conventions

### Go style
- Standard `gofmt` formatting, no exceptions
- Error wrapping with context: `fmt.Errorf("toolReadFile: %w", err)`
- Unexported by default. Only export what's needed cross-package (currently single package)
- Table-driven tests with `t.Run()` subtests
- Channel direction on parameters: `ch chan<- AgentEvent`, `stop <-chan struct{}`

### File organization
- One responsibility per file (aspiration — chat.go violates this, fix in progress)
- Tests in `*_test.go` alongside source
- Constants and types at top of file, functions below
- Section headers with `// ──────────` dividers

### Naming
- Tool functions: `toolReadFile()`, `toolWriteFile()`, etc.
- Event types: `EventTextDelta`, `EventToolStart`, etc.
- States: `chatIdle`, `chatStreaming`, `chatPermission`, etc.
- Messages (BubbleTea): `agentEventMsg`, `flashTickMsg`, etc.

### What to avoid
- Don't use `panic()` except for true programmer errors
- Don't use `init()` except for command registration (commands.go)
- Don't add dependencies without justification — stdlib first
- Don't add features without updating `docs/ARCHITECTURE.md`

## Refactoring in Progress

The codebase is undergoing a major architectural refactoring. See `docs/ARCHITECTURE.md`
sections 7-12 for the migration plan. The phases are:

1. **Tool Interface** — Replace switch dispatch with `Tool` interface + `Registry`
2. **Permission Engine** — Effect-based policies instead of tool-name checks
3. **LLM Provider Interface** — Adapter pattern instead of protocol branching
4. **Agent State Machine** — Explicit FSM instead of implicit state in chatModel
5. **Token Budget Manager** — Proactive budget instead of heuristic compaction
6. **MCP Support** — External tool servers via mcp-go
7. **Slash Commands** — Command interface with auto-discovery

When working on this codebase, check which phase is current and follow that plan.

## Competitive Context: Claude Code Benchmark

We have Claude Code's source code for reference. Key things to know:

### What CC does well (learn from)
- Tools as self-contained modules with schema validation
- Streaming tool executor with concurrency control
- Slash command system (50+ commands)
- MCP integration for extensibility
- Git worktree isolation for parallel agents
- Persistent cross-session memory

### What CC got wrong (do better)
- **React/Ink for TUI**: 512K LOC for a terminal app. BubbleTea is right-sized.
- **No clean tool interface**: BashTool is 160K bytes. Each tool is a snowflake.
  We use a `Tool` interface — adding a tool is mechanical.
- **Permission monster**: 300K+ lines of AST parsing, ML classifiers, hooks cascade.
  We use effect-based policies — tools declare effects, policies match on effects.
- **God object state**: 30+ field AppState. We use an explicit state machine (FSM).
- **Three compaction strategies**: They don't coordinate. We use one strategy,
  proactive, with a calibrated token budget.
- **Retrofitted MCP**: 119K lines because tool system wasn't designed for it.
  Our `Tool` interface treats local and MCP tools identically.
- **Bun lock-in**: No real benefit for a CLI. We ship a single Go binary.

### Design philosophy difference
CC optimizes for Anthropic-only with maximum features. We optimize for any-LLM-provider
with maximum simplicity. Our thesis: 80% of CC's value in 5% of the code, by making
better foundational choices.

When adding features, always ask: "How did CC do this, and what's a simpler way that
avoids their pain points?" Check `docs/ARCHITECTURE.md` for the specific analysis.

## Project Files Recognized

The CLI reads these files from the project root for context:
- `AGENTS.md` — agent instructions (OpenAI Codex convention)
- `CLAUDE.md` — project instructions (Claude Code convention)
- `CODEX.md` — project instructions (Codex convention)
- `.cursorrules` — project instructions (Cursor convention)

All are injected into the system prompt at session start.

## Dependencies (keep minimal)

Direct dependencies only:
- `charmbracelet/bubbletea` — TUI framework
- `charmbracelet/bubbles` — TUI components (spinner, textinput, viewport)
- `charmbracelet/lipgloss` — styling
- `alecthomas/chroma/v2` — syntax highlighting
- `lucasb-eyer/go-colorful` — color space conversions (boot animation)

Planned additions:
- `mark3labs/mcp-go` — MCP client (Phase 6)

Do NOT add dependencies for things the stdlib can do (HTTP, JSON, crypto, etc.).
