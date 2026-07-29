<h1 align="center">codebase</h1>

<p align="center">
  <strong>An AI coding agent that lives in your terminal.</strong><br/>
  Any LLM. Reads your project, writes code, runs commands, ships work.
</p>

<p align="center">
  <a href="https://www.npmjs.com/package/codebase-cli"><img alt="npm" src="https://img.shields.io/npm/v/codebase-cli?style=flat-square" /></a>
  <a href="https://github.com/codebase/codebase-cli/blob/main/LICENSE"><img alt="license" src="https://img.shields.io/badge/license-MIT-blue?style=flat-square" /></a>
</p>

## Install

Requires **Node.js ≥ 20**.

```sh
# macOS / Linux
curl -fsSL https://codebase.design/install.sh | sh

# Windows (PowerShell)
irm https://codebase.design/install.ps1 | iex

# any platform, with npm
npm i -g codebase-cli
```

Then, in any project:

```sh
cd your-project
codebase
```

Type, hit enter. It reads files, edits code, runs tests, and shows its work. `/help` lists everything.

For a trusted one-shot build from scripts or CI:

```sh
codebase auto "build a small dashboard and run the tests"
codebase auto --reliable "fix the auth refresh race and prove it"
```

`--reliable` fails the run unless the agent keeps a task list, moves completed
tasks through `in_progress` without overlapping active work, attaches evidence
to each completed task, records a passing verification command after the final
file change, ties file mutations and verification to completed task work, and
requires the final answer to positively name the fresh verification command. For
read-only or memory-only runs with no file mutations, the final answer must
state that no file-change verification was needed. With `--output json`, the
result includes a receipt: task lifecycle, per-task evidence, file mutations,
verification evidence, final-answer proof, usage, and rewind checkpoints.
Obvious secret-looking values are redacted before receipts are saved.
Failed receipt summaries show gate status and next actions instead of only
dumping raw audit strings.
Inspect the latest one with `codebase receipt`, list saved runs with
`codebase receipt list`, or export markdown with
`codebase receipt export --out receipt.md`.

For launch-proof benchmark sweeps:

```sh
codebase bench run --scenario all --runs 3 --reliable true
codebase bench report <sweep-id> --out docs/benchmarks/<sweep-id>.md --json-out docs/benchmarks/<sweep-id>.json
```

Benchmark rows include CLI version, repo commit/dirty state, Node version,
isolated-HOME status, receipt health, task evidence, verification evidence, tool
calls, cost, and redacted public artifacts. The packaged command writes sweep
results under `./bench/results/<sweep-id>` in the directory where you run it.

## Pick your LLM

**Bring your own key** — Anthropic, OpenAI, Groq, OpenRouter, Mistral, Ollama, or any OpenAI-compatible endpoint:

```sh
ANTHROPIC_API_KEY=sk-ant-... codebase     # or OPENAI_API_KEY, GROQ_API_KEY, …
```

**Or sign in once** and skip the key wrangling — [codebase.design](https://codebase.design) gives you a free taste of open-weight models, and a paid account uncaps those plus the frontier models (Claude Opus/Sonnet, GPT-5, …) behind one bill. First run walks you through it; it even auto-detects a local LLM (LM Studio / Ollama / vLLM).

```sh
codebase auth login
codebase project build --wait "build a launch waitlist page"
```

OAuth uses Codebase Auto by default (`codebase/d4f`, in-house DeepSeek V4 Flash). Swap models live with `/model`. Set reasoning depth with `/effort`. `project build` hands a prompt to the web builder and prints the session, status, event stream, and preview URL when you pass `--wait`.

## Use Codebase from an ACP client

Codebase speaks the [Agent Client Protocol](https://agentclientprotocol.com/) over stdio:

```sh
codebase acp
```

For a Buzz custom harness, use:

```text
Name: Codebase
Command: codebase
Arguments: acp
```

Buzz can then discover the models available to your Codebase account. Each ACP
session gets its own working directory and conversation, accepts client-supplied
MCP servers, streams assistant and tool progress, supports cancellation, and
routes write/shell approvals through the client's permission UI.

## What makes it good

- **🏁 Tournaments.** `/tournament <task>` races several agents on the same change in isolated worktrees, a judge ranks them, you merge the winner. `--models opus,sonnet,haiku` pits models head-to-head on *your* code.
- **Receipts.** `codebase auto --reliable` turns a one-shot task into an audited run: task lifecycle, per-task evidence, verification, tool calls, usage, and checkpoints are saved locally and inspectable with `codebase receipt`.
- **↺ Rewind anything.** `/rewind` rolls the conversation *and* the files back to before any earlier prompt — a bad turn fully un-happens. Every edit is checkpointed.
- **🧠 Remembers across sessions.** Pulls durable facts (your prefs, project decisions, the rules you set) out of a session, then recalls matching notes with file/source/session/last-used/staleness labels. `#note` to add one by hand; `/memory list|show|forget` to inspect or clean them up.
- **🔌 MCP.** Connect external tool servers (filesystem, Postgres, git, fetch, …) over stdio or remote HTTP, OAuth and all. Their tools splice straight into the agent.
- **ACP.** Run `codebase acp` to use Codebase from Buzz and other ACP clients, with model discovery, streamed tool activity, cancellation, permissions, and client-supplied MCP servers.
- **🤖 Subagents.** Fan out read-only researchers or write-capable workers that keep their tool-noise out of your main context — each can run in its own git worktree, on its own model and reasoning level.
- **🪝 Hooks.** Shell commands on lifecycle events (pre/post tool, edit, prompt, session start/end) — run a formatter on save, block secrets, commit on exit.
- **🌐 SSH.** Run commands on enrolled remote hosts by name, behind the same safety validator as the local shell.

…plus a fast differential TUI (clean copy-mode with `Ctrl-O`, image paste with `Ctrl-V`, history search with `Ctrl-R`, `$EDITOR` compose with `Ctrl-G`), **plan mode** for a cheap Q&A pass before editing, **auto-compaction** of long sessions, **multi-session resume** (`/resume`, `/rename`, `/tag`), **TS/JS code navigation** for definitions/type definitions/implementations/references/hover/symbols/diagnostics, **skills** & **output styles** as drop-in markdown, **45+ tools** behind one interface, and **effect-based permissions** you can teach with `/permissions` or preview with `/permissions simulate "npm test && git status"`.

## Cheat sheet

```
/model /effort /plan /context /memory /tournament /rewind /resume /permissions /mcp /help
!cmd     run a shell command without spending a turn
@path    pin a file into the next prompt
#note    save a memory   ·   \<Enter>  multi-line   ·   Ctrl-C  stop turn / exit
```

Type while it's working — your prompt queues and fires when the turn ends.

## Built on pi

The agent loop, provider adapters, and session protocol come from
**[pi](https://github.com/earendil-works/pi)** (MIT). Go give them a star.

## More

- [`CLAUDE.md`](CLAUDE.md) — full feature reference
- [`.settings/`](.settings/) — tenets, architecture, extending, testing
- [`docs/MIGRATION_v1_to_v2.md`](docs/MIGRATION_v1_to_v2.md) — upgrading from the Go v1 binary
- `/help` inside the CLI — every command and shortcut

## License

MIT.
