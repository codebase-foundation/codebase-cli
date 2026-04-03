# Codebase CLI — Architecture Blueprint

> Competitive analysis of Claude Code + architectural plan for the next generation.
> Written April 2026. Based on source-level analysis of both codebases.
>
> **Core principle**: Claude Code is a benchmark and feature reference, NOT a template.
> For every CC feature, we ask: "What problem does this solve, and what's a fundamentally
> better way to solve it?" If the answer is "the same way CC does it," we're not thinking
> hard enough. CC has 512K LOC of accumulated decisions — many are tech debt, not wisdom.

---

## Table of Contents

1. [Where We Are Now](#1-where-we-are-now)
2. [Claude Code: What They Got Right](#2-claude-code-what-they-got-right)
3. [Claude Code: What They Got Wrong](#3-claude-code-what-they-got-wrong)
4. [The Gap (Honest Numbers)](#4-the-gap)
5. [Why Stay in Go](#5-why-stay-in-go)
6. [Target Architecture](#6-target-architecture)
7. [Tool System Redesign](#7-tool-system-redesign)
8. [Permission Engine Redesign](#8-permission-engine-redesign)
9. [Agent Loop as State Machine](#9-agent-loop-as-state-machine)
10. [Token Budget Manager](#10-token-budget-manager)
11. [MCP Integration](#11-mcp-integration)
12. [Feature Comparison: CC Feature → Our Better Approach](#12-feature-comparison)
13. [Migration Plan](#13-migration-plan)
14. [What NOT to Build](#14-what-not-to-build)

---

## 1. Where We Are Now

**Codebase CLI** — ~12K LOC Go, 43 files, 14 tools, BubbleTea TUI.

### Current Strengths
- Multi-provider LLM support (OpenAI, Anthropic, Groq, Ollama, any compatible endpoint)
- Demoscene boot with synthesized chiptune audio (unique — CC has nothing like it)
- Atomic multi-edit with rollback
- 30% test coverage for a POC
- Conversation compaction (context-aware)
- Parallel read-only tool execution
- Subagent dispatch for isolated research
- Glue sidecar for cheap routing/narration tasks
- Session persistence and resume

### Current Weaknesses
- Tools are a single 1,267-line file with a switch statement dispatcher
- `chatModel` is a god object (1,603 lines, 30+ fields, 10+ responsibilities)
- Permissions are inlined if/switch with no abstraction per tool
- Two duplicate streaming implementations (OpenAI + Anthropic SSE parsing)
- Context management uses char-based heuristic (`charsPerToken=3.8`)
- No MCP support
- No plugin/extension system
- No slash command system
- Single read-only subagent (depth 1)
- `<think>` tag filtering is ad-hoc state machine
- Search providers have no common interface (4 implementations, copy-paste)

---

## 2. Claude Code: What They Got Right

Source: ~512K LOC TypeScript, 1,884 files, React+Ink TUI, Bun runtime.

### Genuinely Good Ideas to Steal

**a) Tools as self-contained modules.**
Each CC tool is its own directory with schema validation (Zod), permission declaration,
concurrency safety flag, custom UI renderer, and execute logic. Adding a tool is
(theoretically) isolated to one directory. We should match this — in Go, as an interface.

**b) Streaming tool executor with concurrency control.**
`StreamingToolExecutor` runs concurrent-safe tools in parallel and non-concurrent tools
exclusively. Results buffered and yielded in receipt order. Sibling abort control kills
hanging processes. Our parallel execution is simpler but the same idea — extend it.

**c) Permission hooks cascade.**
Static config rules -> tool-specific validators -> optional ML classifier -> user prompt ->
pre-execution hooks. The layering is overcomplicated but the concept of a policy cascade
is correct. We need something similar, just simpler.

**d) Context management as a first-class system.**
Three complementary strategies: snip (temporal fragmentation), reactive compact (mid-turn
compression), context collapse (aggressive summarization). They don't coordinate well
(see section 3), but the idea of multiple strategies managed by a single budget is right.

**e) Slash command system.**
50+ user-facing commands (/commit, /review, /plan, /config, /doctor, /tasks, etc.).
Makes the tool discoverable and gives users shortcuts for common workflows. We have
zero slash commands.

**f) Memory system.**
File-based persistent memory at `~/.claude/` with frontmatter metadata, auto-indexed,
loaded into system prompt. Survives across sessions. We have session persistence but no
cross-session memory.

**g) MCP (Model Context Protocol).**
119K lines of MCP client. Connects to arbitrary external tool servers. This is the
extensibility layer that makes CC pluggable without forking the source. Any tool
provider can expose tools via MCP, and CC picks them up. This is table-stakes for
a serious coding agent.

**h) Git worktree isolation.**
`EnterWorktreeTool`/`ExitWorktreeTool` let agents work in isolated git worktrees.
Critical for multi-agent workflows where parallel agents shouldn't conflict.

---

## 3. Claude Code: What They Got Wrong

These are patterns visible in the source that indicate regret or accumulated debt.

### a) React/Ink for a TUI is massive overkill.

They're running a full virtual DOM reconciliation loop to output ANSI escape codes.
`useTypeahead.tsx` is 212K lines. `BashTool.tsx` is 160K bytes. `main.tsx` is 4,683 lines.

For a CLI that's fundamentally sequential text output with occasional prompts, this is
a complexity tax that buys very little. The component model is powerful but the cost in
bundle size, cognitive overhead, and re-render debugging is enormous.

**Our advantage**: BubbleTea is the right weight for this. Don't copy their mistake.
Keep the TUI layer thin and purely a view over events.

### b) No clean tool interface from day one.

Tools grew organically. BashTool alone is 160K bytes because it accumulated security
analysis, path validation, command parsing, custom rendering, and permission logic over
time. There's no mechanical process for "add a new tool" — each tool is a snowflake.

**Lesson**: Define the `Tool` interface before writing the second tool.

### c) Permissions became a monster (300K+ lines).

AST-parsing shell commands to determine if they're destructive. ML classifiers for
auto-approval. Bash-specific path validation (1,303 lines). Read-only constraint
checking (1,990 lines). This exists because the original model was "ask the user" and
they kept bolting on heuristics. The permission model wasn't designed for the problem
it now solves.

**Lesson**: Design effect-based permissions from day one. Tools declare effects, policies
match on effects. Don't try to reverse-engineer intent from command strings.

### d) AppState is a god object.

30+ fields across UI state, conversation state, agent state, permission state, and
config. Classic "one struct to rule them all." Same problem as our `chatModel`.

**Lesson**: Explicit state machine with typed transitions, not a state bag.

### e) Context management strategies don't coordinate.

Three compaction strategies (snip, reactive compact, context collapse) with different
triggers and no shared budget. Reactive compaction is literally "the API returned a
context-too-long error, panic and compress." This is a band-aid on poor proactive
budgeting.

**Lesson**: Single token budget manager. One compaction strategy, triggered proactively.
Never hit the API error path for context overflow.

### f) Bun lock-in without clear benefit.

Bun was chosen for startup performance, but CLI startup is dominated by the first API
call, not runtime boot. Meanwhile, contributors need Bun installed, and some Node
ecosystem packages need workarounds.

**Lesson**: Runtime choice should be driven by ecosystem compatibility, not microbenchmarks
that are dwarfed by network latency.

### g) MCP was retrofitted.

119K lines of MCP client code exists because the tool system wasn't designed for
external tools. MCP is a separate subsystem layered on top. If the tool interface had
been "anything that serves tools over a protocol" from the start, MCP would be a
transport adapter, not a major integration project.

**Lesson**: Design the tool interface so local tools and remote tools (MCP) share the
same contract.

### h) Dual implementation patterns everywhere.

Two streaming parsers (one per LLM provider API shape). Multiple permission system layers.
Three compaction strategies. 88 command subdirectories. When the same concept has
multiple implementations, the abstraction is wrong.

**Lesson**: One interface, multiple implementations behind it. Not multiple parallel systems.

---

## 4. The Gap

Honest comparison as of April 2026:

| Dimension              | Codebase CLI      | Claude Code         | Gap     |
|------------------------|-------------------|---------------------|---------|
| Source LOC             | ~12K              | ~512K               | 43x     |
| Tools                  | 14                | 40+                 | 3x      |
| Slash commands         | 0                 | 50+                 | Total   |
| MCP support            | None              | Full client (119K)  | Total   |
| IDE integration        | None              | VS Code + JetBrains | Total   |
| Multi-agent            | 1 subagent        | Coordinator swarms  | Large   |
| Permissions            | Basic trust levels| AST+ML+hooks cascade| Large   |
| Context management     | Heuristic compact | 3-strategy system   | Medium  |
| LLM providers          | Any compatible    | Anthropic only      | **Ahead** |
| Single binary          | Yes               | No (needs Bun)      | **Ahead** |
| Boot experience        | Demoscene+audio   | Plain text           | **Ahead** |
| Codebase complexity    | Simple            | Enormous            | **Ahead** |

**We're behind on features but ahead on architecture simplicity and provider flexibility.**
The strategy is not to close the 43x LOC gap — it's to deliver 80% of the value in 5% of
the code by making better foundational decisions.

---

## 5. Why Stay in Go

The initial analysis suggested pivoting to TypeScript. After studying Claude Code's pain
points, the recommendation reverses: **stay in Go.**

### Go's constraints are features

| Concern                  | TypeScript trap                              | Go forcing function              |
|--------------------------|----------------------------------------------|----------------------------------|
| UI framework             | React/Ink (212K line typeahead, 160K BashTool)| BubbleTea — right-sized for TUI  |
| Ecosystem gravity        | npm pulls in complexity (38+ top-level deps)  | Stdlib covers 80% of needs       |
| Tool system              | Each tool becomes a React component           | Tools are `interface{}` + handler|
| Distribution             | Needs Bun runtime installed                   | Single static binary             |
| Concurrency              | Promise chains + React render cycle           | Goroutines + channels (natural)  |
| Dependency count         | Explodes with transitive deps                 | go.sum is manageable             |

### Go's real differentiator: deployment targets CC can't reach

- Air-gapped / classified environments (no npm, no Bun)
- Embedded / IoT dev environments
- Corporate environments with strict dependency policies
- Self-hosted LLM setups (Ollama, llama.cpp, vLLM)
- CI/CD pipelines (single binary, no runtime)

### What Go lacks (and how to compensate)

| Missing                   | Compensation                                       |
|---------------------------|----------------------------------------------------|
| Zod-like runtime schemas  | Use `json.RawMessage` for tool schemas, `mapstructure` for args |
| MCP SDK maturity          | Go MCP SDK exists (`github.com/mark3labs/mcp-go`), growing fast |
| React component model     | BubbleTea models + message passing (already works)  |
| npm AI ecosystem          | We only need HTTP + SSE — Go is excellent at both   |

---

## 6. Target Architecture

```
                    ┌──────────────────────────────┐
                    │        TUI Layer (thin)       │
                    │  BubbleTea: receives events,  │
                    │  renders text. NOT a god obj. │
                    └──────────────┬───────────────┘
                                   │ AgentEvent channel
                    ┌──────────────┴───────────────┐
                    │     Agent Loop (FSM)          │
                    │  States: idle | thinking |    │
                    │    tool_call | permission |   │
                    │    executing | done | error   │
                    └───┬──────────┬───────────┬───┘
                        │          │           │
           ┌────────────┘          │           └────────────┐
           ▼                       ▼                        ▼
┌─────────────────┐   ┌─────────────────────┐   ┌──────────────────┐
│  Tool Registry  │   │  Token Budget Mgr   │   │ Permission Engine│
│                 │   │                     │   │                  │
│ Local tools     │   │ Tracks all usage    │   │ Tools declare    │
│ MCP tools       │   │ Single compact      │   │ effects, policies│
│ Same interface  │   │ strategy, proactive │   │ match on effects │
└────────┬────────┘   └─────────────────────┘   └──────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────┐
│                  Tool Interface                      │
│                                                      │
│  type Tool interface {                               │
│      Name() string                                   │
│      Schema() json.RawMessage                        │
│      Effects() []Effect        // reads_fs, writes_  │
│      ConcurrencySafe() bool    // can run in parallel│
│      Execute(ctx, args) Result // does the work      │
│  }                                                   │
│                                                      │
│  Local: read_file, write_file, shell, git_*, ...     │
│  MCP:   any server via mcp-go transport              │
│  Agent: subagent as a tool (recursive loop)          │
└─────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────┐
│              LLM Provider Interface                  │
│                                                      │
│  type Provider interface {                           │
│      StreamChat(ctx, messages, tools) <-chan Chunk   │
│      Name() string                                   │
│      Protocol() string // "openai" | "anthropic"     │
│  }                                                   │
│                                                      │
│  Implementations: OpenAI, Anthropic, Ollama, ...     │
│  Single SSE parser, provider-specific message        │
│  conversion as an adapter (not inline in parser).    │
└─────────────────────────────────────────────────────┘
```

### Key Principles

1. **Everything is a tool.** Local tools, MCP tools, and subagents all implement `Tool`.
   The agent loop doesn't know or care where a tool comes from.

2. **The TUI is a view.** It receives `AgentEvent`s on a channel and renders them.
   It does NOT contain agent logic, permission logic, or tool execution.

3. **State machine, not state bag.** The agent loop has explicit states with typed
   transitions. Invalid transitions are compile-time errors, not runtime surprises.

4. **Single token budget.** One number, one threshold, one compaction strategy.
   Never hit the API's context limit error.

5. **Effect-based permissions.** Tools declare what they do (`reads_fs`, `writes_fs`,
   `runs_process`, `network`). Policies match on effects. Adding a new tool never
   requires editing permission code.

6. **Provider adapters, not protocol branches.** One streaming parser. Provider-specific
   message format conversion happens in adapter functions, not inline in the parser.

---

## 7. Tool System Redesign

### Current (bad)

```go
// tools.go — everything in one file, switch dispatch
var toolDefs = []ToolDef{ /* 340 lines of inline schema */ }

func ExecuteTool(name string, argsJSON string, workDir string) (string, bool) {
    switch name {
    case "read_file":
        return toolReadFile(args, workDir)
    // ... 12 more cases
    }
}
```

### Target

```go
// tool.go — interface definition
type Effect string
const (
    EffectReadsFS   Effect = "reads_fs"
    EffectWritesFS  Effect = "writes_fs"
    EffectRunsProc  Effect = "runs_process"
    EffectNetwork   Effect = "network"
    EffectGit       Effect = "git_write"
)

type ToolResult struct {
    Output  string
    Success bool
    Files   []string // files touched (for diagnostics)
}

type Tool interface {
    Name() string
    Description() string
    Schema() json.RawMessage       // JSON Schema for arguments
    Effects() []Effect             // what this tool does
    ConcurrencySafe() bool         // safe to run in parallel?
    Execute(ctx context.Context, args map[string]any, workDir string) ToolResult
}

// registry.go — auto-registration
type Registry struct {
    tools map[string]Tool
}

func NewRegistry() *Registry { ... }
func (r *Registry) Register(t Tool) { r.tools[t.Name()] = t }
func (r *Registry) Get(name string) (Tool, bool) { ... }
func (r *Registry) All() []Tool { ... }
func (r *Registry) Schemas() []json.RawMessage { ... } // for LLM tool definitions

// tools/read_file.go — one file per tool
type ReadFileTool struct{}
func (t ReadFileTool) Name() string { return "read_file" }
func (t ReadFileTool) Effects() []Effect { return []Effect{EffectReadsFS} }
func (t ReadFileTool) ConcurrencySafe() bool { return true }
func (t ReadFileTool) Execute(ctx context.Context, args map[string]any, workDir string) ToolResult {
    // implementation
}

// MCP tools implement the same interface
type MCPTool struct {
    name    string
    schema  json.RawMessage
    client  *mcp.Client
    effects []Effect // declared by MCP server
}
func (t MCPTool) Name() string { return t.name }
func (t MCPTool) Execute(ctx context.Context, args map[string]any, _ string) ToolResult {
    result, err := t.client.CallTool(ctx, t.name, args)
    // ...
}
```

### Migration path
1. Define `Tool` interface and `Registry`
2. Extract `read_file` as first standalone tool (simplest, read-only)
3. Move tools one at a time from `tools.go` switch statement to individual files
4. Once all local tools are migrated, add MCP tool loader
5. Delete the old `toolDefs` array and `ExecuteTool` switch

---

## 8. Permission Engine Redesign

### Current (bad)

```go
// permission.go — hardcoded tool names, inline logic
func NeedsPermission(toolName string, args map[string]any) bool {
    switch toolName {
    case "write_file", "edit_file", "multi_edit":
        return true
    case "shell":
        return shellNeedsPermission(args)
    // ...
    }
}
```

### Target

```go
// permission.go — effect-based policy engine
type Policy struct {
    Effect Effect
    Action PolicyAction // Allow, Deny, Ask
}

type PolicyEngine struct {
    policies []Policy
    session  map[string]PolicyAction // per-session overrides (user chose "always allow")
}

// Check uses the tool's declared effects, not its name
func (e *PolicyEngine) Check(tool Tool, args map[string]any) PolicyAction {
    effects := tool.Effects()

    for _, effect := range effects {
        // Session overrides first
        if action, ok := e.session[string(effect)]; ok {
            if action == Deny { return Deny }
            continue
        }
        // Then policy rules
        for _, p := range e.policies {
            if p.Effect == effect {
                if p.Action == Deny { return Deny }
                if p.Action == Ask { return Ask }
            }
        }
    }
    return Allow
}

// After user approves, record their trust decision
func (e *PolicyEngine) RecordDecision(effect Effect, action PolicyAction) {
    e.session[string(effect)] = action
}
```

### Why this is better

- Adding a new tool NEVER requires editing permission code
- MCP tools automatically get permission checks (they declare effects)
- The shell tool declares `EffectRunsProc` — we don't need to AST-parse the command
- User trust decisions are per-effect ("I trust filesystem writes") not per-tool
- Easy to add policy sources later (config file, project-level, org-level)

---

## 9. Agent Loop as State Machine

### Current (bad)

The agent loop is a goroutine with implicit state tracked via booleans and counters
in `chatModel` (30+ fields). State transitions happen through side effects scattered
across 1,603 lines.

### Target

```go
type AgentState int
const (
    StateIdle       AgentState = iota
    StateThinking              // waiting for LLM response
    StateToolCall              // LLM wants to call a tool
    StatePermission            // waiting for user approval
    StateExecuting             // tool is running
    StateDone                  // turn complete
    StateError                 // recoverable error
)

type Transition struct {
    From  AgentState
    Event string
    To    AgentState
    Action func(*AgentContext) error
}

var transitions = []Transition{
    {StateIdle, "user_message", StateThinking, sendToLLM},
    {StateThinking, "text_delta", StateThinking, streamText},
    {StateThinking, "tool_call", StateToolCall, prepareToolCall},
    {StateToolCall, "needs_permission", StatePermission, requestPermission},
    {StateToolCall, "auto_approved", StateExecuting, executeTool},
    {StatePermission, "approved", StateExecuting, executeTool},
    {StatePermission, "denied", StateThinking, sendDeniedResult},
    {StateExecuting, "tool_done", StateThinking, sendToolResult},
    {StateThinking, "stop", StateDone, finalize},
    {StateThinking, "error", StateError, handleError},
    {StateError, "retry", StateThinking, retryWithContext},
    {StateError, "fatal", StateDone, reportError},
}
```

### Why this is better

- Every state and transition is explicit and enumerable
- Invalid transitions are caught at compile time (or with a simple check)
- Easy to add new states (e.g., `StatePlanning`, `StateCompacting`)
- Easy to test: inject state + event, assert new state + side effects
- The TUI just renders based on current state — no god object needed
- Debug logging is trivial: "transition: Thinking -> ToolCall (tool_call event)"

---

## 10. Token Budget Manager

### Current (bad)

```go
const charsPerToken = 3.8 // heuristic in compact.go
```

### Target

```go
type TokenBudget struct {
    ContextWindow  int     // model's max context (e.g., 128000)
    CompactAt      float64 // trigger compaction at this ratio (e.g., 0.75)
    ReserveOutput  int     // tokens reserved for model output (e.g., 4096)

    used           int     // current estimated usage
    messages       []MessageCost
}

type MessageCost struct {
    MessageIndex int
    Tokens       int
    Compactable  bool // system prompt = false, recent messages = false
}

func (b *TokenBudget) Add(msg Message, tokens int, compactable bool) {
    b.used += tokens
    b.messages = append(b.messages, MessageCost{...})
}

func (b *TokenBudget) Available() int {
    return b.ContextWindow - b.used - b.ReserveOutput
}

func (b *TokenBudget) NeedsCompaction() bool {
    return float64(b.used) / float64(b.ContextWindow) > b.CompactAt
}

// Compact returns indices of messages to summarize
func (b *TokenBudget) CompactionCandidates() []int {
    var candidates []int
    for _, mc := range b.messages {
        if mc.Compactable {
            candidates = append(candidates, mc.MessageIndex)
        }
    }
    // Keep most recent N messages, compact the rest
    if len(candidates) > keepRecent {
        return candidates[:len(candidates)-keepRecent]
    }
    return nil
}
```

### Token estimation strategy

Use the LLM provider's token count from responses (most APIs return `usage.prompt_tokens`
and `usage.completion_tokens`). Calibrate the char-based estimator against real counts.
Over time, the heuristic self-corrects.

```go
func (b *TokenBudget) Calibrate(estimatedChars int, actualTokens int) {
    b.charsPerToken = float64(estimatedChars) / float64(actualTokens)
}
```

**Single strategy, proactive trigger, calibrated estimation. Never hit the API error.**

---

## 11. MCP Integration

### Why this matters

MCP (Model Context Protocol) is the universal extension mechanism. Without it, every new
capability requires forking the CLI and adding code. With it, users connect to any tool
server and the agent picks up new tools automatically.

### Implementation with mcp-go

```go
import "github.com/mark3labs/mcp-go/client"

type MCPManager struct {
    clients  map[string]*client.Client
    registry *Registry // same tool registry as local tools
}

// Connect to an MCP server and register its tools
func (m *MCPManager) Connect(name string, transport Transport) error {
    c, err := client.NewClient(transport)
    if err != nil { return err }

    tools, err := c.ListTools(ctx)
    if err != nil { return err }

    for _, t := range tools {
        m.registry.Register(&MCPTool{
            name:   t.Name,
            schema: t.InputSchema,
            client: c,
            effects: inferEffects(t), // or declared by server
        })
    }
    m.clients[name] = c
    return nil
}
```

### Config

```json
{
  "mcp_servers": {
    "github": {
      "command": "mcp-server-github",
      "args": ["--token", "$GITHUB_TOKEN"],
      "transport": "stdio"
    },
    "postgres": {
      "url": "http://localhost:3001/mcp",
      "transport": "sse"
    }
  }
}
```

### Priority

MCP is the **highest priority new feature** after the tool interface redesign. It's what
turns a coding CLI into a platform. Without MCP, we're a fixed-tool CLI. With MCP, we're
extensible without recompilation.

---

## 12. Feature Comparison: CC Feature → Our Better Approach

This is the core reference. For every significant CC feature, we document what problem
it solves, how CC implemented it, where they went wrong, and how we do it better.

**Rule**: If our approach is identical to CC's, we haven't thought enough. CC's codebase
is 512K LOC of accumulated decisions made under time pressure. We have the luxury of
hindsight.

### Tool Execution

| | Claude Code | Codebase CLI (target) |
|---|---|---|
| **Approach** | Each tool is a React component class (BashTool.tsx = 160K bytes) with custom rendering, permissions, and execution interleaved | Each tool implements a 5-method Go interface. Rendering, permissions, and execution are separate concerns. |
| **Adding a tool** | Create directory, implement class, register in tools.ts, add permissions in multiple files | Implement `Tool` interface in one file. Auto-registered. Permissions derived from `Effects()`. |
| **CC's mistake** | Tools grew into monoliths because rendering/permission/execution weren't separated | |
| **Our advantage** | Clean interface = MCP tools and local tools are the same thing. CC had to bolt on MCP as a 119K-line subsystem. |

### Permission System

| | Claude Code | Codebase CLI (target) |
|---|---|---|
| **Approach** | Multi-layer cascade: static rules → tool validators → ML classifier → user prompt → hooks. BashTool has 200K+ lines of AST parsing for command safety. | Effect-based policies. Tools declare what they do (`writes_fs`, `runs_process`). Policies match on effects. |
| **CC's mistake** | Started with "ask the user" and kept adding heuristics. Now they AST-parse shell commands to guess intent — a fundamentally losing game (adversarial input always wins). | |
| **Our advantage** | We don't try to understand what a command does. We know the tool declares `runs_process`. The policy says "ask before running processes." Simple, composable, can't be bypassed. |
| **Trade-off** | CC can auto-approve `ls` but ask for `rm`. We ask for all shell commands (or user sets a blanket process policy). Acceptable trade-off for 200K fewer lines of security theater. |

### Context Management

| | Claude Code | Codebase CLI (target) |
|---|---|---|
| **Approach** | Three strategies (snip, reactive compact, context collapse) with different triggers and no shared budget. Reactive compact = "API returned context-too-long, panic." | Single `TokenBudget` manager. Calibrated from API response `usage` fields. One compaction strategy, triggered proactively at 75% capacity. |
| **CC's mistake** | Multiple strategies were added ad-hoc as different context problems appeared. They don't coordinate and can conflict. | |
| **Our advantage** | One budget, one strategy, one trigger. Self-calibrating (uses real token counts from API, not just char heuristics). Never hits the API error for context overflow. |

### Streaming & LLM Integration

| | Claude Code | Codebase CLI (target) |
|---|---|---|
| **Approach** | Anthropic SDK + custom streaming. Locked to one provider. | Provider interface with adapters. Shared SSE parser, provider-specific message format conversion. Works with any OpenAI-compatible or Anthropic endpoint. |
| **CC's mistake** | Tight coupling to Anthropic API. Can't use Groq, Ollama, OpenRouter, etc. | |
| **Our advantage** | Provider flexibility is our #1 differentiator. Users bring their own LLM. |

### TUI / Terminal UI

| | Claude Code | Codebase CLI (target) |
|---|---|---|
| **Approach** | React + Ink (custom fork). Full virtual DOM for terminal rendering. 140 React components, custom hooks, re-render optimization. | BubbleTea (Elm architecture). Message passing, model updates, view function. |
| **CC's mistake** | React's component model is designed for interactive web UIs with complex state. A CLI is 90% "print streaming text." The overhead (212K-line typeahead, custom Ink fork) is enormous for what's fundamentally a text output problem. | |
| **Our advantage** | BubbleTea is purpose-built for terminal UIs. No virtual DOM overhead. Elm architecture naturally prevents state management issues. ~3K LOC for our entire UI vs ~100K+ for CC's. |
| **What we lose** | React's component composition is powerful for complex UIs. If we ever need rich interactive elements (split panes, file trees), BubbleTea requires more manual work. Acceptable for now. |

### Slash Commands

| | Claude Code | Codebase CLI (target) |
|---|---|---|
| **Approach** | 50+ commands in 88 subdirectories. Commander.js parsing. Skills system (reusable workflows). | `Command` interface with auto-discovery. One file per command. |
| **CC's mistake** | 88 subdirectories for 50 commands = lots of boilerplate. Skills system adds another layer of indirection. | |
| **Our improvement** | Commands are a simple interface: `Name()`, `Description()`, `Execute(*Agent, string)`. No framework, no subdirectories unless needed. Start with 10 essential commands, add on demand. |

### MCP (Model Context Protocol)

| | Claude Code | Codebase CLI (target) |
|---|---|---|
| **Approach** | 119K-line MCP client with OAuth, credentials, error handling, resource/prompt discovery. | `MCPTool` wraps `mcp-go` client and implements our `Tool` interface. ~200 lines. |
| **CC's mistake** | MCP was retrofitted onto a tool system not designed for external tools. Hence the massive integration surface. | |
| **Our advantage** | `Tool` interface was designed from day one to be transport-agnostic. An MCP tool and a local tool are indistinguishable to the agent loop. |

### Multi-Agent / Subagents

| | Claude Code | Codebase CLI (target) |
|---|---|---|
| **Approach** | Coordinator mode with worker swarms, cross-agent scratchpads, git worktree isolation. | Single subagent (read-only, depth 1) for now. Planned: depth > 1, git worktree isolation. |
| **CC's approach is good here** | Worktree isolation is genuinely clever — parallel agents working on isolated copies. We should implement this when we extend subagent depth. |
| **What we skip for now** | Coordinator/swarm mode. It's impressive engineering but fragile in practice. Ship when depth-1 subagents are proven reliable. |

### Memory / Cross-Session Persistence

| | Claude Code | Codebase CLI (target) |
|---|---|---|
| **Approach** | File-based memory at `~/.claude/` with frontmatter YAML, auto-indexed in MEMORY.md, loaded into system prompt at session start. | Session persistence exists. Cross-session memory planned (same file-based approach — CC got this right). |
| **CC's mistake** | Memory is frozen at session start. If files change during a session, the agent's memory context is stale. | |
| **Our improvement** | When we add memory, make it refreshable during a session. Memory entries that reference files should be re-validated on access, not just on load. |

### IDE Integration

| | Claude Code | Codebase CLI (target) |
|---|---|---|
| **Approach** | Bidirectional bridge to VS Code and JetBrains. JWT auth, file sync, diff display, permission callbacks. | Not planned. |
| **Why skip** | IDE integration is a separate product. Our thesis is terminal-native. Users who want IDE integration can use CC. Our niche is: works everywhere a terminal works (SSH, Docker, CI, air-gapped). |

### Security / Sandboxing

| | Claude Code | Codebase CLI (target) |
|---|---|---|
| **Approach** | AST-based shell command analysis, path validation, read-only mode enforcement, sandbox option. | Effect-based permissions (see above) + `safePath()` for directory traversal prevention. |
| **What CC does better** | Their `safePath` equivalent is more thorough (symlink resolution, canonical path checking). | |
| **What we should add** | Symlink-aware path resolution in `safePath()`. Optional `--sandbox` flag using OS-level isolation (seccomp on Linux, sandbox-exec on macOS). Don't try to AST-parse shell commands. |

### Diagnostics / Language Integration

| | Claude Code | Codebase CLI (target) |
|---|---|---|
| **Approach** | LSP integration for real-time diagnostics across any language with an LSP server. | Built-in checkers for Go, TypeScript, Python (runs compiler/linter after file edits). |
| **CC's advantage** | LSP is language-agnostic. One integration covers all languages. | |
| **Our path** | Add LSP client support as a future phase. Until then, our built-in checkers cover the most common languages. Low priority — diagnostics are nice-to-have, not core. |

---

## 13. Migration Plan

Ordered by dependency. Each phase is independently shippable.

### Phase 1: Tool Interface (foundation)

**Goal**: Replace switch-statement dispatch with `Tool` interface and `Registry`.

1. Define `Tool` interface, `Effect` type, `ToolResult`, `Registry`
2. Extract `read_file` as first standalone tool (simplest)
3. Extract remaining read-only tools: `list_files`, `search_files`
4. Extract write tools: `write_file`, `edit_file`, `multi_edit`
5. Extract `shell` tool
6. Extract git tools from `git_tools.go`
7. Delete old `toolDefs` array and `ExecuteTool` switch
8. Registry auto-generates LLM tool definitions from `Schema()`

**Files created**: `tool.go` (interface), `registry.go`, `tools/` directory (one file per tool)
**Files deleted**: none yet (old code removed as tools migrate)
**Risk**: Low — pure refactor, same behavior, tests validate

### Phase 2: Permission Engine

**Goal**: Replace hardcoded tool-name checks with effect-based policies.

1. Define `PolicyEngine` with effect-based matching
2. Each tool already declares `Effects()` from Phase 1
3. Migrate `NeedsPermission()` logic to policies
4. Migrate `shellNeedsPermission()` — shell tool declares `EffectRunsProc`
5. Migrate per-session trust from `PermissionState` to `PolicyEngine.session`
6. Delete old `permission.go` switch statements

**Files modified**: `permission.go` (rewrite), all tools (already have Effects)
**Risk**: Low — permission behavior must not regress, add specific tests

### Phase 3: LLM Provider Interface

**Goal**: Replace protocol-branching with provider adapters.

1. Define `Provider` interface with `StreamChat` method
2. Extract shared SSE parser (both protocols use SSE, just different JSON shapes)
3. Implement `OpenAIProvider` with message format adapter
4. Implement `AnthropicProvider` with message format adapter
5. Delete `streamChatOpenAI()`, `streamChatAnthropic()`, `convertMessagesToAnthropic()`
6. Provider selection at startup, not per-request

**Files created**: `provider.go` (interface), `provider_openai.go`, `provider_anthropic.go`
**Files deleted**: `llm_anthropic.go` (logic moves to adapter)
**Risk**: Medium — streaming is critical path, test thoroughly

### Phase 4: Agent State Machine

**Goal**: Replace implicit state in `chatModel` with explicit FSM.

1. Define `AgentState`, `Transition`, `AgentContext`
2. Extract agent loop from `chat.go` into `agent_fsm.go`
3. TUI receives events from FSM, renders based on `AgentState`
4. `chatModel` shrinks to: viewport, input, current state, event channel
5. Planning becomes a state (`StatePlanning`), not a separate code path

**Files created**: `agent_fsm.go`
**Files modified**: `chat.go` (massive shrink), `agent.go` (refactor)
**Risk**: Medium-High — biggest refactor, touch carefully

### Phase 5: Token Budget Manager

**Goal**: Replace heuristic compaction with proactive budget system.

1. Define `TokenBudget` with calibration from API responses
2. Track per-message token costs
3. Single compaction strategy triggered by budget threshold
4. Remove reactive "context too long" error recovery (should never happen)
5. Delete `charsPerToken` heuristic (replaced by calibrated estimator)

**Files created**: `budget.go`
**Files modified**: `compact.go` (simplify), `agent.go` (budget integration)
**Risk**: Low — compaction behavior improves, doesn't break

### Phase 6: MCP Support

**Goal**: Connect to MCP servers, expose their tools through the same `Tool` interface.

1. Add `mcp-go` dependency
2. Define `MCPTool` implementing `Tool` interface
3. Define `MCPManager` for connection lifecycle
4. Add `mcp_servers` config section
5. On startup, connect to configured servers, register their tools
6. MCP tools appear in LLM tool definitions automatically

**Files created**: `mcp.go`, `mcp_tool.go`
**Config change**: `~/.codebase/config.json` gains `mcp_servers` key
**Risk**: Low — additive feature, no existing behavior changes

### Phase 7: Slash Commands

**Goal**: User-facing command system for common workflows.

1. Define `Command` interface (Name, Description, Execute)
2. Implement core commands: /help, /compact, /clear, /config, /status
3. Implement workflow commands: /commit, /plan, /tasks
4. Input parser routes `/` prefix to command system
5. Commands are discoverable via /help

**Files created**: `command.go` (interface), `commands/` directory
**Files modified**: `chat.go` (input routing)

---

## 14. What NOT to Build

Things Claude Code has that we should deliberately skip:

| Feature              | Why skip it                                                  |
|----------------------|--------------------------------------------------------------|
| IDE bridge           | Focus on terminal-native. IDE integration is a separate product. |
| ML permission classifier | Overcomplicated. Effect-based policies solve the same problem. |
| Voice input          | Gimmick. Focus on text-first workflows.                     |
| React/Ink renderer   | BubbleTea is the right tool. Don't switch.                  |
| Feature flag system  | We're not a SaaS with staged rollouts. Ship or don't.      |
| OAuth/JWT/Keychain   | API keys work. Add auth when the hosted proxy (cli-auth-plan.md) ships. |
| OpenTelemetry        | Structured logging is enough for a CLI. Add observability when needed. |
| Custom Ink fork      | Maintaining a forked UI framework is a full-time job. No.    |
| Coordinator mode     | Multi-agent swarms are impressive demos but fragile in practice. Add when subagent depth > 1 is proven. |

---

## Appendix: File Map (target state after all phases)

```
cli/
├── main.go              # entry point, flag parsing, startup
├── app.go               # BubbleTea app coordinator (boot → setup → chat)
├── chat.go              # TUI view (thin — renders AgentState, handles input)
├── boot.go              # boot animation
├── setup.go             # first-run wizard
├── render.go            # viewport rendering helpers
│
├── tool.go              # Tool interface, Effect type, ToolResult
├── registry.go          # Tool registry (local + MCP)
├── tools/
│   ├── read_file.go
│   ├── write_file.go
│   ├── edit_file.go
│   ├── multi_edit.go
│   ├── list_files.go
│   ├── search_files.go
│   ├── shell.go
│   ├── git_status.go
│   ├── git_diff.go
│   ├── git_log.go
│   ├── git_commit.go
│   ├── git_branch.go
│   ├── web_search.go
│   └── dispatch_agent.go
│
├── agent_fsm.go         # Agent state machine (states, transitions, loop)
├── agent.go             # Agent context, turn management
│
├── provider.go          # LLM Provider interface
├── provider_openai.go   # OpenAI adapter (message format + SSE parsing)
├── provider_anthropic.go# Anthropic adapter
├── sse.go               # Shared SSE stream parser
│
├── permission.go        # PolicyEngine (effect-based)
├── budget.go            # TokenBudget manager
├── compact.go           # Compaction strategy (single, proactive)
│
├── mcp.go               # MCP manager + MCPTool
├── command.go           # Command interface
├── commands/
│   ├── help.go
│   ├── compact.go
│   ├── commit.go
│   ├── plan.go
│   ├── config.go
│   ├── status.go
│   └── tasks.go
│
├── session.go           # session persistence
├── memory.go            # cross-session memory (file-based)
├── glue.go              # sidecar LLM for cheap tasks
├── notify.go            # notification system
├── diagnostics.go       # language checker integration
├── highlight.go         # syntax highlighting
├── theme.go             # color themes
├── dotenv.go            # .env loader
│
├── docs/
│   ├── ARCHITECTURE.md  # this file
│   └── cli-auth-plan.md # authentication roadmap
│
└── *_test.go            # tests alongside source
```

---

*This document is the source of truth for architectural decisions. Update it as
decisions change. If the code disagrees with this doc, either the code or the doc
needs to be fixed — not ignored.*
